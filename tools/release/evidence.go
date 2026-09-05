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
	"encoding/json/jsontext"
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
	SPDXVersion   string                    `json:"spdxVersion"`
	Packages      []spdxPackage             `json:"packages"`
	Relationships []spdxRelationship        `json:"relationships"`
	Extra         map[string]jsontext.Value `json:",embed"`
}

type spdxPackage struct {
	Name               string                    `json:"name,omitempty"`
	SPDXID             string                    `json:"SPDXID,omitempty"`
	Version            string                    `json:"versionInfo,omitempty"`
	DownloadLocation   string                    `json:"downloadLocation,omitempty"`
	LicenseConcluded   string                    `json:"licenseConcluded,omitempty"`
	LicenseDeclared    string                    `json:"licenseDeclared,omitempty"`
	CopyrightText      string                    `json:"copyrightText,omitempty"`
	FilesAnalyzed      *bool                     `json:"filesAnalyzed,omitempty"`
	ExternalReferences []spdxExternalReference   `json:"externalRefs,omitempty"`
	Extra              map[string]jsontext.Value `json:",embed"`
}

type spdxExternalReference struct {
	Category string                    `json:"referenceCategory"`
	Type     string                    `json:"referenceType"`
	Locator  string                    `json:"referenceLocator"`
	Extra    map[string]jsontext.Value `json:",embed"`
}

type spdxRelationship struct {
	ElementID        string                    `json:"spdxElementId"`
	Type             string                    `json:"relationshipType"`
	RelatedElementID string                    `json:"relatedSpdxElement"`
	Extra            map[string]jsontext.Value `json:",embed"`
}

func runVerifyEvidence(arguments []string) error {
	flags := flag.NewFlagSet("verify-evidence", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "extracted release root")
	repository := flags.String("repository", "", "released source checkout")
	sbom := flags.String("sbom", "", "detached SPDX SBOM")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *root == "" {
		return errors.New("verify-evidence requires an extracted release root")
	}
	return verifyReleaseEvidence(*root, *sbom, *repository)
}

func verifyReleaseEvidence(root, detachedSBOM, repository string) error {
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
	integrationRootInfo, integrationRootErr := os.Stat(filepath.Join(absoluteRoot, "integrations"))
	_, buildInfoErr := os.Stat(filepath.Join(absoluteRoot, "BUILD-INFO.json"))
	requiresIntegrationEvidence := len(manifest.Integrations) > 0 ||
		(integrationRootErr == nil && integrationRootInfo.IsDir()) || buildInfoErr == nil
	if requiresIntegrationEvidence {
		if checksumErr := verifyTreeChecksums(absoluteRoot); checksumErr != nil {
			return checksumErr
		}
		if len(manifest.Integrations) == 0 {
			return errors.New("redistributed integration inventory is empty")
		}
		if repository == "" {
			return errors.New("released source checkout is required for redistributed integration evidence")
		}
		if integrationErr := verifyIntegrationEvidence(absoluteRoot, repository, manifest.Integrations); integrationErr != nil {
			return integrationErr
		}
		if detachedSBOM == "" {
			return errors.New("detached SPDX SBOM is required for redistributed integration evidence")
		}
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
	if detachedSBOM != "" {
		detachedBytes, readErr := readBoundedFile(detachedSBOM, maxMetadataBytes)
		if readErr != nil {
			return fmt.Errorf("read detached SPDX SBOM: %w", readErr)
		}
		if !slices.Equal(sbomBytes, detachedBytes) {
			return errors.New("detached SPDX SBOM does not match the archived SPDX SBOM")
		}
	}
	if requiresIntegrationEvidence {
		if integrationSBOMErr := verifyIntegrationSPDXEvidence(sbom, manifest.Integrations); integrationSBOMErr != nil {
			return integrationSBOMErr
		}
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
		purl := "pkg:golang/" + module.Path + "@" + module.Version
		if _, ok := recorded[purl]; !ok {
			return fmt.Errorf("SPDX SBOM is missing Go module %q at version %q", module.Path, module.Version)
		}
	}
	for _, native := range manifest.Native {
		purl := "pkg:generic/" + native.Path + "@" + native.Version
		if _, ok := recorded[purl]; !ok {
			return fmt.Errorf("SPDX SBOM is missing native dependency %q at version %q", native.Path, native.Version)
		}
	}
	return nil
}

func verifyIntegrationSPDXEvidence(document spdxDocument, integrations []integrationEvidence) error {
	recordedPackages := make(map[string]spdxPackage, len(integrations))
	for _, packageRecord := range document.Packages {
		if strings.HasPrefix(packageRecord.SPDXID, "SPDXRef-Integration-") {
			if _, duplicate := recordedPackages[packageRecord.SPDXID]; duplicate {
				return fmt.Errorf("SPDX SBOM contains duplicate redistributed integration package %q", packageRecord.SPDXID)
			}
			recordedPackages[packageRecord.SPDXID] = packageRecord
		}
	}
	expectedRelationships := make(map[string]struct{}, len(integrations))
	for _, integration := range integrations {
		expectedRelationships[integrationSPDXID(integration.ID)] = struct{}{}
	}
	recordedRelationships := make(map[string]int, len(integrations))
	for _, relationship := range document.Relationships {
		involvesIntegration := strings.HasPrefix(relationship.ElementID, "SPDXRef-Integration-") ||
			strings.HasPrefix(relationship.RelatedElementID, "SPDXRef-Integration-")
		if !involvesIntegration {
			continue
		}
		if relationship.ElementID != "SPDXRef-DOCUMENT" || relationship.Type != "CONTAINS" {
			return fmt.Errorf("SPDX SBOM has invalid redistributed integration relationship %q", relationship.RelatedElementID)
		}
		if _, approved := expectedRelationships[relationship.RelatedElementID]; !approved {
			return fmt.Errorf("SPDX SBOM has unreviewed redistributed integration relationship %q", relationship.RelatedElementID)
		}
		recordedRelationships[relationship.RelatedElementID]++
	}
	for _, integration := range integrations {
		spdxID := integrationSPDXID(integration.ID)
		packageRecord, exists := recordedPackages[spdxID]
		if !exists {
			return fmt.Errorf("SPDX SBOM is missing redistributed integration package %q", integration.ID)
		}
		if packageRecord.Name != integrationSPDXName(integration.ID) ||
			packageRecord.DownloadLocation != "NOASSERTION" ||
			packageRecord.FilesAnalyzed == nil || *packageRecord.FilesAnalyzed {
			return fmt.Errorf("SPDX SBOM has invalid redistributed integration package %q", integration.ID)
		}
		if packageRecord.LicenseConcluded != "Apache-2.0" || packageRecord.LicenseDeclared != "Apache-2.0" {
			return fmt.Errorf("SPDX SBOM has invalid redistributed integration license for %q", integration.ID)
		}
		if packageRecord.CopyrightText != "NOASSERTION" {
			return fmt.Errorf("SPDX SBOM has invalid redistributed integration copyright for %q", integration.ID)
		}
		if recordedRelationships[spdxID] != 1 {
			return fmt.Errorf("SPDX SBOM is missing redistributed integration relationship for %q", integration.ID)
		}
	}
	if len(recordedPackages) != len(integrations) {
		return errors.New("SPDX SBOM has unreviewed redistributed integration packages")
	}
	return nil
}

func integrationSPDXID(id string) string {
	return "SPDXRef-Integration-" + id
}

func integrationSPDXName(id string) string {
	return "PowerContext integration " + id
}

func verifyIntegrationEvidence(root, repository string, records []integrationEvidence) error {
	integrations := make([]releaseIntegration, len(records))
	for index, record := range records {
		integrations[index] = record.releaseIntegration
	}
	if err := validateReleaseIntegrationRecords(integrations); err != nil {
		return fmt.Errorf("validate redistributed integration evidence: %w", err)
	}
	expected, err := readReleaseIntegrations(repository)
	if err != nil {
		return fmt.Errorf("read frozen release integration inventory: %w", err)
	}
	if err := validateReleaseIntegrations(repository, expected); err != nil {
		return fmt.Errorf("validate frozen release integration inventory: %w", err)
	}
	if !slices.EqualFunc(integrations, expected, equalReleaseIntegration) {
		return errors.New("redistributed integration evidence does not match frozen release integration inventory")
	}
	licenseHash, _, err := hashFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		return fmt.Errorf("read redistributed integration license: %w", err)
	}
	recorded := make(map[string]struct{}, len(records))
	for _, record := range records {
		recorded[record.ID] = struct{}{}
		integrationRoot := filepath.Join(root, "integrations", record.ID)
		info, statErr := os.Stat(integrationRoot)
		if statErr != nil {
			return fmt.Errorf("redistributed integration %q root is missing: %w", record.ID, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("redistributed integration %q root is not a directory", record.ID)
		}
		for _, releasePath := range append(slices.Clone(record.RequiredPaths), record.LockPaths...) {
			info, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(releasePath)))
			if statErr != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("redistributed integration %q is missing declared file %q", record.ID, releasePath)
			}
		}
		if len(record.Licenses) != 1 || record.Licenses[0].Name != "LICENSE" ||
			record.Licenses[0].SHA256 != licenseHash {
			return fmt.Errorf("redistributed integration %q license does not match the archived project license", record.ID)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "integrations"))
	if err != nil {
		return fmt.Errorf("read redistributed integration roots: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, exists := recorded[entry.Name()]; !exists {
			return fmt.Errorf("integration root %q is absent from redistributed integration evidence", entry.Name())
		}
	}
	return nil
}

func equalReleaseIntegration(left, right releaseIntegration) bool {
	return left.ID == right.ID && left.Class == right.Class &&
		left.ConsumerMode == right.ConsumerMode &&
		slices.Equal(left.RequiredPaths, right.RequiredPaths) &&
		slices.Equal(left.LockPaths, right.LockPaths)
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
