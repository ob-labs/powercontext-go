// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"golang.org/x/mod/module"
)

const maxLicenseBytes = 2 << 20

//go:embed licenses/*.txt
var nativeLicenses embed.FS

type dependencyManifest struct {
	SchemaVersion int                `json:"schema_version"`
	Modules       []dependencyRecord `json:"go_modules"`
	Native        []dependencyRecord `json:"native_dependencies"`
}

type dependencyRecord struct {
	Path        string          `json:"path"`
	Version     string          `json:"version"`
	Replacement string          `json:"replacement,omitempty"`
	Licenses    []licenseRecord `json:"licenses"`
}

type licenseRecord struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func collectLicenses(
	binaryPath, repository, edition string,
	assets nativeAssets,
) (dependencyManifest, []byte, error) {
	info, err := buildinfo.ReadFile(binaryPath)
	if err != nil {
		return dependencyManifest{}, nil, err
	}
	moduleCache, err := goModuleCache(repository)
	if err != nil {
		return dependencyManifest{}, nil, err
	}

	dependencies := dependencyManifest{SchemaVersion: 1}
	var notices strings.Builder
	for _, dependency := range info.Deps {
		directory, replacement, err := moduleDirectory(dependency, moduleCache, repository)
		if err != nil {
			return dependencyManifest{}, nil, err
		}
		licenseFiles, err := findLicenseFiles(directory)
		if err != nil {
			return dependencyManifest{}, nil, fmt.Errorf("%s %s: %w", dependency.Path, dependency.Version, err)
		}
		record := dependencyRecord{Path: dependency.Path, Version: dependency.Version, Replacement: replacement}
		writeNoticeHeader(&notices, dependency.Path, dependency.Version)
		for _, licensePath := range licenseFiles {
			contents, err := readBoundedFile(licensePath, maxLicenseBytes)
			if err != nil {
				return dependencyManifest{}, nil, err
			}
			hash := sha256.Sum256(contents)
			record.Licenses = append(record.Licenses, licenseRecord{
				Name: filepath.Base(licensePath), SHA256: hex.EncodeToString(hash[:]),
			})
			writeLicense(&notices, filepath.Base(licensePath), contents)
		}
		dependencies.Modules = append(dependencies.Modules, record)
	}
	sort.Slice(dependencies.Modules, func(left, right int) bool {
		return dependencies.Modules[left].Path < dependencies.Modules[right].Path
	})

	nativeNames := []struct {
		path    string
		version string
		file    string
	}{
		{path: "github.com/asg017/sqlite-vec", version: assets.SQLiteVec.Version, file: "licenses/sqlite-vec.txt"},
	}
	if edition == "full" {
		nativeNames = append(nativeNames,
			struct{ path, version, file string }{"github.com/daulet/tokenizers/native", assets.Tokenizers.Version, "licenses/tokenizers.txt"},
			struct{ path, version, file string }{"github.com/microsoft/onnxruntime/native", assets.ONNXRuntime.Version, "licenses/onnxruntime.txt"},
		)
	}
	for _, native := range nativeNames {
		contents, err := fs.ReadFile(nativeLicenses, native.file)
		if err != nil {
			return dependencyManifest{}, nil, err
		}
		hash := sha256.Sum256(contents)
		dependencies.Native = append(dependencies.Native, dependencyRecord{
			Path: native.path, Version: native.version,
			Licenses: []licenseRecord{{Name: filepath.Base(native.file), SHA256: hex.EncodeToString(hash[:])}},
		})
		writeNoticeHeader(&notices, native.path, native.version)
		writeLicense(&notices, filepath.Base(native.file), contents)
	}
	return dependencies, []byte(notices.String()), nil
}

func collectModuleGraphLicenses(manifest *dependencyManifest, repository string, modules []string) error {
	moduleCache, err := goModuleCache(repository)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(manifest.Modules))
	for _, record := range manifest.Modules {
		seen[record.Path+"\x00"+record.Version+"\x00"+record.Replacement] = struct{}{}
	}
	for _, modulePath := range modules {
		if modulePath == "." {
			continue
		}
		dependencies, err := moduleGraphDependencies(repository, modulePath)
		if err != nil {
			return err
		}
		for _, dependency := range dependencies {
			if dependency.Version == "" || (dependency.Replace != nil && dependency.Replace.Version == "") {
				continue
			}
			directory, replacement, err := moduleDirectory(dependency, moduleCache, repository)
			if err != nil {
				return err
			}
			key := dependency.Path + "\x00" + dependency.Version + "\x00" + replacement
			if _, exists := seen[key]; exists {
				continue
			}
			licenses, err := findLicenseFiles(directory)
			if err != nil {
				return fmt.Errorf("%s %s: %w", dependency.Path, dependency.Version, err)
			}
			record := dependencyRecord{Path: dependency.Path, Version: dependency.Version, Replacement: replacement}
			for _, licensePath := range licenses {
				contents, err := readBoundedFile(licensePath, maxLicenseBytes)
				if err != nil {
					return err
				}
				hash := sha256.Sum256(contents)
				record.Licenses = append(record.Licenses, licenseRecord{Name: filepath.Base(licensePath), SHA256: hex.EncodeToString(hash[:])})
			}
			manifest.Modules = append(manifest.Modules, record)
			seen[key] = struct{}{}
		}
	}
	sort.Slice(manifest.Modules, func(left, right int) bool {
		if manifest.Modules[left].Path == manifest.Modules[right].Path {
			return manifest.Modules[left].Version < manifest.Modules[right].Version
		}
		return manifest.Modules[left].Path < manifest.Modules[right].Path
	})
	return nil
}

func moduleGraphDependencies(repository, modulePath string) ([]*debug.Module, error) {
	command := exec.Command("go", "-C", modulePath, "list", "-deps", "-test", "-f", "{{if .Module}}{{.Module.Path}}|{{.Module.Version}}|{{if .Module.Replace}}{{.Module.Replace.Path}}|{{.Module.Replace.Version}}{{end}}{{end}}", "./...")
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list module graph for %s: %w", modulePath, err)
	}
	var result []*debug.Module
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 2 || fields[0] == "" {
			return nil, fmt.Errorf("module graph for %s contains malformed entry", modulePath)
		}
		dependency := &debug.Module{Path: fields[0], Version: fields[1]}
		if len(fields) >= 3 && fields[2] != "" {
			dependency.Replace = &debug.Module{Path: fields[2]}
			if len(fields) >= 4 {
				dependency.Replace.Version = fields[3]
			}
		}
		result = append(result, dependency)
	}
	return result, nil
}

func goModuleCache(repository string) (string, error) {
	command := exec.Command("go", "env", "GOMODCACHE")
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("locate Go module cache: %w", err)
	}
	directory := strings.TrimSpace(string(output))
	if directory == "" {
		return "", errors.New("Go module cache is empty")
	}
	return directory, nil
}

func moduleDirectory(dependency *debug.Module, moduleCache, repository string) (string, string, error) {
	effective := dependency
	replacement := ""
	if dependency.Replace != nil {
		effective = dependency.Replace
		replacement = effective.Path
		if effective.Version != "" {
			replacement += "@" + effective.Version
		}
	}
	var directory string
	if effective.Version == "" {
		directory = effective.Path
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(repository, directory)
		}
	} else {
		escapedPath, err := module.EscapePath(effective.Path)
		if err != nil {
			return "", "", err
		}
		escapedVersion, err := module.EscapeVersion(effective.Version)
		if err != nil {
			return "", "", err
		}
		directory = filepath.Join(moduleCache, escapedPath+"@"+escapedVersion)
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("module source for %s %s is unavailable in the module cache", dependency.Path, dependency.Version)
	}
	return directory, replacement, nil
}

func findLicenseFiles(directory string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !licenseFileName(entry.Name()) {
			continue
		}
		paths = append(paths, filepath.Join(directory, entry.Name()))
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return nil, errors.New("no top-level license or notice file")
	}
	return paths, nil
}

func licenseFileName(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range []string{"license", "copying", "notice", "patents"} {
		if lower == prefix || strings.HasPrefix(lower, prefix+".") || strings.HasPrefix(lower, prefix+"-") {
			return true
		}
	}
	return false
}

func writeNoticeHeader(writer *strings.Builder, name, version string) {
	writer.WriteString("================================================================================\n")
	writer.WriteString(name)
	writer.WriteByte(' ')
	writer.WriteString(version)
	writer.WriteByte('\n')
	writer.WriteString("================================================================================\n")
}

func writeLicense(writer *strings.Builder, name string, contents []byte) {
	writer.WriteString("-- ")
	writer.WriteString(name)
	writer.WriteString(" --\n\n")
	writer.Write(contents)
	if len(contents) == 0 || contents[len(contents)-1] != '\n' {
		writer.WriteByte('\n')
	}
	writer.WriteByte('\n')
}
