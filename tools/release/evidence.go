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
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type spdxDocument struct {
	SPDXVersion string        `json:"spdxVersion"`
	Packages    []spdxPackage `json:"packages"`
}

type spdxPackage struct {
	ExternalReferences []spdxExternalReference `json:"externalRefs"`
}

type spdxExternalReference struct {
	Category string `json:"referenceCategory"`
	Type     string `json:"referenceType"`
	Locator  string `json:"referenceLocator"`
}

func runVerifyEvidence(arguments []string) error {
	flags := flag.NewFlagSet("verify-evidence", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "extracted release root")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *root == "" {
		return errors.New("verify-evidence requires an extracted release root")
	}
	return verifyReleaseEvidence(*root)
}

func verifyReleaseEvidence(root string) error {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	manifestPath := filepath.Join(absoluteRoot, "DEPENDENCIES.json")
	manifestBytes, err := readBoundedFile(manifestPath, maxMetadataBytes)
	if err != nil {
		return fmt.Errorf("read dependency manifest: %w", err)
	}
	var manifest dependencyManifest
	if decodeErr := json.Unmarshal(manifestBytes, &manifest, json.RejectUnknownMembers(true)); decodeErr != nil {
		return fmt.Errorf("read dependency manifest: %w", decodeErr)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Modules) == 0 || len(manifest.Native) == 0 {
		return errors.New("dependency manifest must contain schema version 1 with Go modules and native dependencies")
	}
	if moduleErr := verifyDependencyRecords(manifest.Modules, "Go module"); moduleErr != nil {
		return moduleErr
	}
	if nativeErr := verifyDependencyRecords(manifest.Native, "native dependency"); nativeErr != nil {
		return nativeErr
	}
	if noticeErr := verifyLicenseNotices(filepath.Join(absoluteRoot, "THIRD-PARTY-LICENSES.txt"), manifest); noticeErr != nil {
		return noticeErr
	}

	sbomPath := filepath.Join(absoluteRoot, "SBOM.spdx.json")
	sbomBytes, err := readBoundedFile(sbomPath, maxMetadataBytes)
	if err != nil {
		return fmt.Errorf("read SPDX SBOM: %w", err)
	}
	var sbom spdxDocument
	if decodeErr := json.Unmarshal(sbomBytes, &sbom); decodeErr != nil {
		return fmt.Errorf("read SPDX SBOM: %w", decodeErr)
	}
	if !strings.HasPrefix(sbom.SPDXVersion, "SPDX-") {
		return errors.New("SPDX SBOM is missing its version")
	}

	recorded := make(map[string]struct{})
	for _, packageRecord := range sbom.Packages {
		for _, reference := range packageRecord.ExternalReferences {
			if reference.Category == "PACKAGE-MANAGER" && reference.Type == "purl" {
				recorded[reference.Locator] = struct{}{}
			}
		}
	}
	for _, module := range manifest.Modules {
		if module.Path == "" || module.Version == "" {
			return errors.New("dependency manifest contains a Go module without a path or version")
		}
		purl := "pkg:golang/" + module.Path + "@" + module.Version
		if _, ok := recorded[purl]; !ok {
			return fmt.Errorf("SPDX SBOM is missing Go module %q at version %q", module.Path, module.Version)
		}
	}
	return nil
}

func verifyDependencyRecords(records []dependencyRecord, kind string) error {
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if record.Path == "" || record.Version == "" {
			return fmt.Errorf("dependency manifest contains a %s without a path or version", kind)
		}
		key := record.Path + "\x00" + record.Version
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("dependency manifest contains duplicate %s %q at version %q", kind, record.Path, record.Version)
		}
		seen[key] = struct{}{}
		if len(record.Licenses) == 0 {
			return fmt.Errorf("dependency manifest contains a %s with a missing license record", kind)
		}
		for _, license := range record.Licenses {
			if license.Name == "" || !validSHA256(license.SHA256) {
				return fmt.Errorf("dependency manifest contains an invalid license record for %s %q at version %q", kind, record.Path, record.Version)
			}
		}
	}
	return nil
}

func verifyLicenseNotices(path string, manifest dependencyManifest) error {
	noticeBytes, err := readBoundedFile(path, maxMetadataBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("dependency license notices are missing license notice file: %w", err)
		}
		return fmt.Errorf("read dependency license notices: %w", err)
	}
	notices := string(noticeBytes)
	dependencies := append(slices.Clone(manifest.Modules), manifest.Native...)
	headers := make([]string, len(dependencies))
	for index, dependency := range dependencies {
		var header strings.Builder
		writeNoticeHeader(&header, dependency.Path, dependency.Version)
		headers[index] = header.String()
	}
	for index, dependency := range dependencies {
		start := strings.Index(notices, headers[index])
		if start < 0 {
			return fmt.Errorf("dependency license notices are missing license notice for %q at version %q", dependency.Path, dependency.Version)
		}
		end := len(notices)
		for otherIndex, otherHeader := range headers {
			if otherIndex == index {
				continue
			}
			if candidate := strings.Index(notices[start+len(headers[index]):], otherHeader); candidate >= 0 {
				end = min(end, start+len(headers[index])+candidate)
			}
		}
		section := notices[start:end]
		for _, license := range dependency.Licenses {
			if !strings.Contains(section, "-- "+license.Name+" --\n") {
				return fmt.Errorf("dependency license notices are missing license notice %q for %q at version %q", license.Name, dependency.Path, dependency.Version)
			}
		}
	}
	return nil
}
