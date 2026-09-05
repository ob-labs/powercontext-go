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
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"
)

var semanticVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

type packageOptions struct {
	Binary         string
	ONNXRuntimeDir string
	Edition        string
	Version        string
	Commit         string
	BuildDate      string
	Output         string
	Repository     string
	Syft           string
}

type binaryFacts struct {
	Info        *buildinfo.BuildInfo
	GOOS        string
	GOARCH      string
	CGOEnabled  string
	BuildTags   []string
	BinaryHash  string
	BinaryBytes int64
}

type buildManifest struct {
	SchemaVersion int                 `json:"schema_version"`
	Product       string              `json:"product"`
	Edition       string              `json:"edition"`
	Version       string              `json:"version"`
	Commit        string              `json:"commit"`
	BuildDate     string              `json:"build_date"`
	GoVersion     string              `json:"go_version"`
	Target        string              `json:"target"`
	CGOEnabled    bool                `json:"cgo_enabled"`
	BuildTags     []string            `json:"build_tags"`
	Oracle        oracleManifest      `json:"python_oracle"`
	Binary        fileRecord          `json:"binary"`
	NativeAssets  []nativeAssetRecord `json:"native_assets"`
}

type fileRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type nativeAssetRecord struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Source       string `json:"source"`
	SourceDigest string `json:"source_digest"`
	PayloadHash  string `json:"payload_sha256"`
}

type packageResult struct {
	Archive     string `json:"archive"`
	ArchiveHash string `json:"archive_sha256"`
	SBOM        string `json:"sbom"`
	SBOMHash    string `json:"sbom_sha256"`
}

func runPackage(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("package", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := bindPackageFlags(flags)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	result, err := packageRelease(*options)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(result)
}

func bindPackageFlags(flags *flag.FlagSet) *packageOptions {
	options := new(packageOptions)
	flags.StringVar(&options.Binary, "binary", "", "built powercontext binary")
	flags.StringVar(&options.ONNXRuntimeDir, "onnxruntime-dir", "", "ONNX Runtime library directory")
	flags.StringVar(&options.Edition, "edition", "standard", "standard or full")
	flags.StringVar(&options.Version, "version", "", "release version")
	flags.StringVar(&options.Commit, "commit", "", "40-character source commit")
	flags.StringVar(&options.BuildDate, "build-date", "", "RFC3339 UTC build date")
	flags.StringVar(&options.Output, "output", "dist", "release output directory")
	flags.StringVar(&options.Repository, "repository", ".", "repository root")
	flags.StringVar(&options.Syft, "syft", "syft", "pinned Syft executable")
	return options
}

func packageRelease(options packageOptions) (packageResult, error) {
	buildTime, err := validatePackageOptions(options)
	if err != nil {
		return packageResult{}, err
	}
	repository, err := filepath.Abs(options.Repository)
	if err != nil {
		return packageResult{}, err
	}
	assets, err := readAssets(repository)
	if err != nil {
		return packageResult{}, err
	}
	oracle, err := readOracle(repository)
	if err != nil {
		return packageResult{}, err
	}
	facts, err := inspectBinary(options.Binary, options.Edition, options.Version)
	if err != nil {
		return packageResult{}, err
	}
	target := facts.GOOS + "-" + facts.GOARCH
	if _, ok := assets.Tokenizers.Assets[target]; !ok {
		return packageResult{}, fmt.Errorf("unsupported release target %q", target)
	}
	if _, ok := assets.ONNXRuntime.Assets[target]; !ok {
		return packageResult{}, fmt.Errorf("unsupported release target %q", target)
	}

	outputDirectory, err := filepath.Abs(options.Output)
	if err != nil {
		return packageResult{}, err
	}
	if mkdirErr := os.MkdirAll(outputDirectory, 0o755); mkdirErr != nil {
		return packageResult{}, mkdirErr
	}
	temporary, err := os.MkdirTemp(outputDirectory, ".powercontext-release-")
	if err != nil {
		return packageResult{}, err
	}
	defer func() { _ = os.RemoveAll(temporary) }()

	version := strings.TrimPrefix(options.Version, "v")
	artifactName := "powercontext-" + version + "-" + target
	if options.Edition == "full" {
		artifactName = "powercontext-full-" + version + "-" + target
	}
	archivePath := filepath.Join(outputDirectory, artifactName+".tar.gz")
	sbomPath := filepath.Join(outputDirectory, artifactName+".spdx.json")
	for _, releasePath := range []string{archivePath, sbomPath} {
		if _, statErr := os.Stat(releasePath); statErr == nil {
			return packageResult{}, fmt.Errorf("refusing to replace existing release file %q", filepath.Base(releasePath))
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return packageResult{}, statErr
		}
	}
	root := filepath.Join(temporary, artifactName)
	if stageErr := stageRelease(repository, root, options, facts); stageErr != nil {
		return packageResult{}, stageErr
	}

	onnxPayload := ""
	if options.Edition == "full" {
		onnxPayload = filepath.Join(root, "lib", "onnxruntime")
	}
	nativeRecords, err := describeNativeAssets(onnxPayload, options.Edition, facts, assets)
	if err != nil {
		return packageResult{}, err
	}
	manifest := newBuildManifest(options, buildTime, facts, oracle, nativeRecords)
	if writeErr := writeJSON(filepath.Join(root, "BUILD-INFO.json"), manifest); writeErr != nil {
		return packageResult{}, writeErr
	}
	dependencies, notices, err := collectLicenses(options.Binary, repository, options.Edition, assets)
	if err != nil {
		return packageResult{}, err
	}
	if writeErr := writeJSON(filepath.Join(root, "DEPENDENCIES.json"), dependencies); writeErr != nil {
		return packageResult{}, writeErr
	}
	if writeErr := os.WriteFile(filepath.Join(root, "THIRD-PARTY-LICENSES.txt"), notices, 0o644); writeErr != nil {
		return packageResult{}, writeErr
	}

	temporarySBOM := filepath.Join(temporary, artifactName+".spdx.json")
	if generateErr := generateSBOM(options.Syft, root, temporarySBOM, assets.Syft.Version, buildTime); generateErr != nil {
		return packageResult{}, generateErr
	}
	if augmentErr := addNativeDependenciesToSBOM(temporarySBOM, dependencies.Native); augmentErr != nil {
		return packageResult{}, augmentErr
	}
	if copyErr := copyRegularFile(temporarySBOM, filepath.Join(root, "SBOM.spdx.json"), 0o644); copyErr != nil {
		return packageResult{}, copyErr
	}
	if checksumErr := writeTreeChecksums(root); checksumErr != nil {
		return packageResult{}, checksumErr
	}

	temporaryArchive := filepath.Join(temporary, artifactName+".tar.gz")
	if archiveErr := archiveTree(root, temporaryArchive, buildTime); archiveErr != nil {
		return packageResult{}, archiveErr
	}
	archiveHash, _, err := hashFile(temporaryArchive)
	if err != nil {
		return packageResult{}, err
	}
	sbomHash, _, err := hashFile(temporarySBOM)
	if err != nil {
		return packageResult{}, err
	}
	if renameErr := os.Rename(temporaryArchive, archivePath); renameErr != nil {
		return packageResult{}, renameErr
	}
	if renameErr := os.Rename(temporarySBOM, sbomPath); renameErr != nil {
		_ = os.Remove(archivePath)
		return packageResult{}, renameErr
	}
	return packageResult{
		Archive: filepath.Base(archivePath), ArchiveHash: archiveHash,
		SBOM: filepath.Base(sbomPath), SBOMHash: sbomHash,
	}, nil
}

func runMetadata(arguments []string) error {
	flags := flag.NewFlagSet("metadata", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := bindPackageFlags(flags)
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	return writeImageMetadata(*options)
}

func writeImageMetadata(options packageOptions) error {
	buildTime, err := validatePackageOptions(options)
	if err != nil {
		return err
	}
	repository, err := filepath.Abs(options.Repository)
	if err != nil {
		return err
	}
	assets, err := readAssets(repository)
	if err != nil {
		return err
	}
	oracle, err := readOracle(repository)
	if err != nil {
		return err
	}
	facts, err := inspectBinary(options.Binary, options.Edition, options.Version)
	if err != nil {
		return err
	}
	target := facts.GOOS + "-" + facts.GOARCH
	if _, ok := assets.Tokenizers.Assets[target]; !ok {
		return fmt.Errorf("unsupported release target %q", target)
	}
	if _, ok := assets.ONNXRuntime.Assets[target]; !ok {
		return fmt.Errorf("unsupported release target %q", target)
	}
	output, err := filepath.Abs(options.Output)
	if err != nil {
		return err
	}
	if mkdirErr := os.MkdirAll(output, 0o755); mkdirErr != nil {
		return mkdirErr
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("metadata output directory must be empty")
	}
	nativeRecords, err := describeNativeAssets(options.ONNXRuntimeDir, options.Edition, facts, assets)
	if err != nil {
		return err
	}
	manifest := newBuildManifest(options, buildTime, facts, oracle, nativeRecords)
	if writeErr := writeJSON(filepath.Join(output, "BUILD-INFO.json"), manifest); writeErr != nil {
		return writeErr
	}
	dependencies, notices, err := collectLicenses(options.Binary, repository, options.Edition, assets)
	if err != nil {
		return err
	}
	if writeErr := writeJSON(filepath.Join(output, "DEPENDENCIES.json"), dependencies); writeErr != nil {
		return writeErr
	}
	if writeErr := os.WriteFile(filepath.Join(output, "THIRD-PARTY-LICENSES.txt"), notices, 0o644); writeErr != nil {
		return writeErr
	}
	return writeTreeChecksums(output)
}

func newBuildManifest(
	options packageOptions,
	buildTime time.Time,
	facts binaryFacts,
	oracle oracleManifest,
	nativeRecords []nativeAssetRecord,
) buildManifest {
	return buildManifest{
		SchemaVersion: 1, Product: "PowerContext", Edition: options.Edition,
		Version: strings.TrimPrefix(options.Version, "v"), Commit: options.Commit,
		BuildDate: buildTime.Format(time.RFC3339), GoVersion: facts.Info.GoVersion,
		Target: facts.GOOS + "-" + facts.GOARCH, CGOEnabled: facts.CGOEnabled == "1",
		BuildTags: slices.Clone(facts.BuildTags), Oracle: oracle,
		Binary:       fileRecord{Path: "bin/powercontext", SHA256: facts.BinaryHash, Size: facts.BinaryBytes},
		NativeAssets: nativeRecords,
	}
}

func validatePackageOptions(options packageOptions) (time.Time, error) {
	if options.Binary == "" || options.Version == "" || options.Commit == "" || options.BuildDate == "" {
		return time.Time{}, errors.New("binary, version, commit, and build-date are required")
	}
	if options.Edition != "standard" && options.Edition != "full" {
		return time.Time{}, errors.New("edition must be standard or full")
	}
	if options.Edition == "full" && options.ONNXRuntimeDir == "" {
		return time.Time{}, errors.New("full edition requires onnxruntime-dir")
	}
	if !semanticVersion.MatchString(options.Version) {
		return time.Time{}, errors.New("version must use semantic versioning")
	}
	if !commitHash.MatchString(options.Commit) {
		return time.Time{}, errors.New("commit must be a lowercase 40-character SHA-1")
	}
	buildTime, err := time.Parse(time.RFC3339, options.BuildDate)
	if err != nil || options.BuildDate != buildTime.UTC().Format(time.RFC3339) {
		return time.Time{}, errors.New("build-date must be a canonical UTC RFC3339 timestamp")
	}
	return buildTime, nil
}

func inspectBinary(path, edition, version string) (binaryFacts, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return binaryFacts{}, fmt.Errorf("read binary build information: %w", err)
	}
	if info.Main.Path != modulePath {
		return binaryFacts{}, fmt.Errorf("binary module is %q, want %q", info.Main.Path, modulePath)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	tags := splitBuildTags(settings["-tags"])
	required := []string{"sqlite_fts5"}
	if edition == "full" {
		required = append(required, "local_embeddings", "ORT")
	}
	for _, tag := range required {
		if !slices.Contains(tags, tag) {
			return binaryFacts{}, fmt.Errorf("binary is missing required build tag %q", tag)
		}
	}
	if settings["CGO_ENABLED"] != "1" {
		return binaryFacts{}, errors.New("release binary must be built with CGO_ENABLED=1")
	}
	if settings["GOOS"] != runtime.GOOS || settings["GOARCH"] != runtime.GOARCH {
		return binaryFacts{}, errors.New("release packaging must run on the binary's target platform")
	}
	versionOutput, err := exec.Command(path, "--version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(versionOutput)) != strings.TrimPrefix(version, "v") {
		return binaryFacts{}, errors.New("binary version metadata does not match release version")
	}
	hash, size, err := hashFile(path)
	if err != nil {
		return binaryFacts{}, err
	}
	return binaryFacts{
		Info: info, GOOS: settings["GOOS"], GOARCH: settings["GOARCH"],
		CGOEnabled: settings["CGO_ENABLED"], BuildTags: tags,
		BinaryHash: hash, BinaryBytes: size,
	}, nil
}

func splitBuildTags(value string) []string {
	parts := strings.FieldsFunc(value, func(character rune) bool { return character == ',' || character == ' ' })
	sort.Strings(parts)
	return slices.Compact(parts)
}

func stageRelease(repository, root string, options packageOptions, facts binaryFacts) error {
	for _, directory := range []string{
		filepath.Join(root, "bin"), filepath.Join(root, "lib"),
		filepath.Join(root, "openapi"), filepath.Join(root, "docs"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return err
		}
	}
	if err := copyRegularFile(options.Binary, filepath.Join(root, "bin", "powercontext"), 0o755); err != nil {
		return err
	}
	for _, pair := range [][2]string{
		{filepath.Join(repository, "LICENSE"), filepath.Join(root, "LICENSE")},
		{filepath.Join(repository, "README.md"), filepath.Join(root, "README.md")},
		{filepath.Join(repository, ".env.example"), filepath.Join(root, ".env.example")},
		{filepath.Join(repository, "openapi", "powercontext.yaml"), filepath.Join(root, "openapi", "powercontext.yaml")},
		{filepath.Join(repository, "docs", "release", "INSTALL.md"), filepath.Join(root, "docs", "INSTALL.md")},
	} {
		if err := copyRegularFile(pair[0], pair[1], 0o644); err != nil {
			return err
		}
	}
	if err := stageIntegrations(repository, root); err != nil {
		return err
	}
	if options.Edition == "full" {
		if err := copyONNXRuntime(
			options.ONNXRuntimeDir,
			filepath.Join(root, "lib", "onnxruntime"),
			facts.GOOS,
		); err != nil {
			return err
		}
	}
	return nil
}

func stageIntegrations(repository, root string) error {
	integrations, err := readReleaseIntegrations(repository)
	if err != nil {
		return err
	}
	if err := validateReleaseIntegrations(repository, integrations); err != nil {
		return err
	}
	tracked, err := trackedRepositoryFiles(repository, ".claude-plugin", "integrations")
	if err != nil {
		return err
	}
	trackedSet := make(map[string]struct{}, len(tracked))
	for _, path := range tracked {
		trackedSet[path] = struct{}{}
	}
	if _, ok := trackedSet[".claude-plugin/marketplace.json"]; !ok {
		return errors.New("Claude Code marketplace manifest is not tracked")
	}
	for _, integration := range integrations {
		for _, path := range append(slices.Clone(integration.RequiredPaths), integration.LockPaths...) {
			if _, ok := trackedSet[path]; !ok {
				return fmt.Errorf("release integration %q path %q is not tracked", integration.ID, path)
			}
		}
	}
	if err := copyRepositoryFiles(repository, root, tracked); err != nil {
		return err
	}
	return nil
}

func describeNativeAssets(
	onnxDirectory, edition string,
	facts binaryFacts,
	assets nativeAssets,
) ([]nativeAssetRecord, error) {
	records := []nativeAssetRecord{{
		Name: "sqlite-vec (statically embedded)", Version: assets.SQLiteVec.Version, Source: assets.SQLiteVec.SourceURL,
		SourceDigest: assets.SQLiteVec.SHA256, PayloadHash: facts.BinaryHash,
	}}
	if edition != "full" {
		return records, nil
	}
	target := facts.GOOS + "-" + facts.GOARCH
	tokenizers := assets.Tokenizers.Assets[target]
	records = append(records, nativeAssetRecord{
		Name: "Daulet Tokenizers static library", Version: assets.Tokenizers.Version,
		Source: tokenizers.Name, SourceDigest: tokenizers.SHA256, PayloadHash: facts.BinaryHash,
	})
	onnx := assets.ONNXRuntime.Assets[target]
	source, sourceDigest := onnx.Name, onnx.SHA256
	if onnx.BuildFromSource {
		source = "https://github.com/microsoft/onnxruntime.git"
		sourceDigest = assets.ONNXRuntime.Commit
	}
	onnxHash, err := hashTree(onnxDirectory)
	if err != nil {
		return nil, err
	}
	records = append(records, nativeAssetRecord{
		Name: "ONNX Runtime", Version: assets.ONNXRuntime.Version,
		Source: source, SourceDigest: sourceDigest, PayloadHash: onnxHash,
	})
	return records, nil
}
