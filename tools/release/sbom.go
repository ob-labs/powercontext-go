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
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func generateSBOM(syft, root, output, syftVersion string, created time.Time) error {
	sourceName := filepath.Base(root)
	command := exec.Command(
		syft,
		"scan", "dir:"+root,
		"--source-name", sourceName,
		"--output", "spdx-json="+output,
	)
	command.Env = append(os.Environ(), "SYFT_CHECK_FOR_APP_UPDATE=false")
	combined, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("generate SPDX SBOM: %w: %s", err, strings.TrimSpace(string(combined)))
	}
	contents, err := readBoundedFile(output, maxMetadataBytes)
	if err != nil {
		return err
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil || document["spdxVersion"] == nil {
		return errors.New("Syft did not produce a valid SPDX JSON document")
	}
	creationInfo, ok := document["creationInfo"].(map[string]any)
	if !ok {
		return errors.New("Syft SPDX document has no creationInfo object")
	}
	document["name"] = sourceName
	document["documentNamespace"] = "https://github.com/ob-labs/powercontext-go/sbom/" + sourceName
	creationInfo["created"] = created.UTC().Format(time.RFC3339)
	creationInfo["creators"] = []string{
		"Organization: Anchore, Inc",
		"Tool: syft-" + syftVersion,
	}
	filterLockfileDependencyPackages(document)
	return writeJSON(output, document)
}

func filterLockfileDependencyPackages(document map[string]any) {
	packages, ok := document["packages"].([]any)
	if !ok {
		return
	}
	removed := make(map[string]struct{})
	kept := make([]any, 0, len(packages))
	for _, value := range packages {
		packageRecord, packageRecordOK := value.(map[string]any)
		if !packageRecordOK || !lockfileOnlyPackage(packageRecord) {
			kept = append(kept, value)
			continue
		}
		if id, idOK := packageRecord["SPDXID"].(string); idOK {
			removed[id] = struct{}{}
		}
	}
	document["packages"] = kept
	relationships, ok := document["relationships"].([]any)
	if !ok || len(removed) == 0 {
		return
	}
	keptRelationships := make([]any, 0, len(relationships))
	for _, value := range relationships {
		relationship, relationshipOK := value.(map[string]any)
		if !relationshipOK {
			keptRelationships = append(keptRelationships, value)
			continue
		}
		elementID, _ := relationship["spdxElementId"].(string)
		relatedID, _ := relationship["relatedSpdxElement"].(string)
		_, removesElement := removed[elementID]
		_, removesRelated := removed[relatedID]
		if !removesElement && !removesRelated {
			keptRelationships = append(keptRelationships, value)
		}
	}
	document["relationships"] = keptRelationships
}

func lockfileOnlyPackage(packageRecord map[string]any) bool {
	references, ok := packageRecord["externalRefs"].([]any)
	if !ok {
		return false
	}
	for _, value := range references {
		reference, referenceOK := value.(map[string]any)
		if !referenceOK || reference["referenceCategory"] != "PACKAGE-MANAGER" || reference["referenceType"] != "purl" {
			continue
		}
		locator, _ := reference["referenceLocator"].(string)
		if strings.HasPrefix(locator, "pkg:pypi/") || strings.HasPrefix(locator, "pkg:npm/") {
			return true
		}
	}
	return false
}

func addNativeDependenciesToSBOM(path string, dependencies []dependencyRecord) error {
	contents, err := readBoundedFile(path, maxMetadataBytes)
	if err != nil {
		return err
	}
	var document spdxDocument
	if decodeErr := jsonv2.Unmarshal(contents, &document); decodeErr != nil || !strings.HasPrefix(document.SPDXVersion, "SPDX-") {
		return errors.New("Syft did not produce a valid SPDX JSON document")
	}
	recorded := make(map[string]struct{})
	for _, packageRecord := range document.Packages {
		for _, reference := range packageRecord.ExternalReferences {
			if reference.Category == "PACKAGE-MANAGER" && reference.Type == "purl" {
				recorded[reference.Locator] = struct{}{}
			}
		}
	}
	for _, dependency := range dependencies {
		purl := "pkg:generic/" + dependency.Path + "@" + dependency.Version
		if _, exists := recorded[purl]; exists {
			continue
		}
		digest := sha256.Sum256([]byte(dependency.Path + "\x00" + dependency.Version))
		document.Packages = append(document.Packages, spdxPackage{
			Name: dependency.Path, SPDXID: fmt.Sprintf("SPDXRef-Native-%x", digest[:8]),
			Version: dependency.Version, DownloadLocation: "NOASSERTION", FilesAnalyzed: new(false),
			ExternalReferences: []spdxExternalReference{{
				Category: "PACKAGE-MANAGER", Type: "purl", Locator: purl,
			}},
		})
		recorded[purl] = struct{}{}
	}
	encoded, err := jsonv2.Marshal(&document, jsonv2.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}

func addIntegrationEvidenceToSBOM(path, repository string) error {
	integrations, err := readReleaseIntegrations(repository)
	if err != nil {
		return fmt.Errorf("read release integration inventory: %w", err)
	}
	if err := validateReleaseIntegrations(repository, integrations); err != nil {
		return fmt.Errorf("validate release integration inventory: %w", err)
	}
	contents, err := readBoundedFile(path, maxMetadataBytes)
	if err != nil {
		return err
	}
	var document spdxDocument
	if decodeErr := jsonv2.Unmarshal(contents, &document); decodeErr != nil || !strings.HasPrefix(document.SPDXVersion, "SPDX-") {
		return errors.New("Syft did not produce a valid SPDX JSON document")
	}
	recordedPackages := make(map[string]struct{}, len(document.Packages))
	for _, packageRecord := range document.Packages {
		recordedPackages[packageRecord.SPDXID] = struct{}{}
	}
	recordedRelationships := make(map[string]struct{}, len(document.Relationships))
	for _, relationship := range document.Relationships {
		if relationship.ElementID == "SPDXRef-DOCUMENT" && relationship.Type == "CONTAINS" {
			recordedRelationships[relationship.RelatedElementID] = struct{}{}
		}
	}
	for _, integration := range integrations {
		spdxID := integrationSPDXID(integration.ID)
		if _, exists := recordedPackages[spdxID]; exists {
			return fmt.Errorf("SPDX SBOM already contains redistributed integration package %q", integration.ID)
		}
		document.Packages = append(document.Packages, spdxPackage{
			Name: integrationSPDXName(integration.ID), SPDXID: spdxID,
			DownloadLocation: "NOASSERTION", FilesAnalyzed: new(false),
			LicenseConcluded: "Apache-2.0", LicenseDeclared: "Apache-2.0", CopyrightText: "NOASSERTION",
		})
		recordedPackages[spdxID] = struct{}{}
		if _, exists := recordedRelationships[spdxID]; exists {
			return fmt.Errorf("SPDX SBOM already contains redistributed integration relationship for %q", integration.ID)
		}
		document.Relationships = append(document.Relationships, spdxRelationship{
			ElementID: "SPDXRef-DOCUMENT", Type: "CONTAINS", RelatedElementID: spdxID,
		})
		recordedRelationships[spdxID] = struct{}{}
	}
	encoded, err := jsonv2.Marshal(&document, jsonv2.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(encoded, '\n'), 0o644)
}
