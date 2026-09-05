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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReleaseIntegrationEvidenceNamesReviewedInventoryWithoutLockDependencies(t *testing.T) {
	repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, nil)
	if err := os.WriteFile(filepath.Join(repository, "LICENSE"), []byte("project license\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(repository, "integrations", "bub", "uv.lock")
	if err := os.WriteFile(lockPath, []byte("lock-only.example==9.8.7\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	integrations, err := collectIntegrationEvidence(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(integrations) != len(reviewedReleaseIntegrationRecords) {
		t.Fatalf("redistributed integration evidence count = %d, want %d", len(integrations), len(reviewedReleaseIntegrationRecords))
	}
	for index, integration := range integrations {
		want := reviewedReleaseIntegrationRecords[index]
		if integration.ID != want.ID || integration.Class != want.Class ||
			integration.ConsumerMode != want.ConsumerMode ||
			!slices.Equal(integration.RequiredPaths, want.RequiredPaths) ||
			!slices.Equal(integration.LockPaths, want.LockPaths) {
			t.Fatalf("redistributed integration %d = %#v, want inventory record %#v", index, integration, want)
		}
		if len(integration.Licenses) != 1 || integration.Licenses[0].Name != "LICENSE" ||
			!validSHA256(integration.Licenses[0].SHA256) {
			t.Fatalf("redistributed integration %q license evidence = %#v", integration.ID, integration.Licenses)
		}
	}
	payload, err := json.Marshal(integrations)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "lock-only.example") {
		t.Fatal("lock-only dependency was misclassified as bundled integration evidence")
	}
}

func TestReleaseIntegrationEvidenceRejectsArchiveEvidenceDrift(t *testing.T) {
	tests := map[string]struct {
		mutate  func(t *testing.T, fixture releaseIntegrationEvidenceFixture)
		message string
	}{
		"omitted integration evidence": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				manifest := readIntegrationEvidenceManifest(t, fixture.root)
				manifest.Integrations = manifest.Integrations[1:]
				writeIntegrationEvidenceManifest(t, fixture.root, manifest)
				rewriteIntegrationEvidenceChecksums(t, fixture.root)
			},
			message: "does not match frozen release integration inventory",
		},
		"all integration evidence omitted": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				manifest := readIntegrationEvidenceManifest(t, fixture.root)
				manifest.Integrations = nil
				writeIntegrationEvidenceManifest(t, fixture.root, manifest)
				rewriteIntegrationEvidenceChecksums(t, fixture.root)
			},
			message: "redistributed integration inventory is empty",
		},
		"unclassified redistributed bundle": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				if err := os.Mkdir(filepath.Join(fixture.root, "integrations", "unclassified"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture.root, "integrations", "unclassified", "plugin.txt"), []byte("bundle\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				rewriteIntegrationEvidenceChecksums(t, fixture.root)
			},
			message: "absent from redistributed integration evidence",
		},
		"missing redistributed bundle": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				if err := os.RemoveAll(filepath.Join(fixture.root, "integrations", "bub")); err != nil {
					t.Fatal(err)
				}
				rewriteIntegrationEvidenceChecksums(t, fixture.root)
			},
			message: `redistributed integration "bub" root is missing`,
		},
		"missing internal checksum": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				if err := os.Remove(filepath.Join(fixture.root, "SHA256SUMS")); err != nil {
					t.Fatal(err)
				}
			},
			message: "internal checksum manifest",
		},
		"detached SPDX mismatch": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				if err := os.WriteFile(fixture.detachedSBOM, []byte(`{"spdxVersion":"SPDX-2.3","packages":[]}`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			message: "detached SPDX SBOM does not match",
		},
		"integration SPDX package omitted": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				mutateIntegrationEvidenceSBOM(t, fixture, func(document map[string]any) {
					packages := document["packages"].([]any)
					document["packages"] = slices.DeleteFunc(packages, func(value any) bool {
						packageRecord := value.(map[string]any)
						return packageRecord["SPDXID"] == "SPDXRef-Integration-bub"
					})
				})
			},
			message: `SPDX SBOM is missing redistributed integration package "bub"`,
		},
		"integration SPDX package drift": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				mutateIntegrationEvidenceSBOM(t, fixture, func(document map[string]any) {
					for _, value := range document["packages"].([]any) {
						packageRecord := value.(map[string]any)
						if packageRecord["SPDXID"] == "SPDXRef-Integration-bub" {
							packageRecord["name"] = "PowerContext integration drifted"
						}
					}
				})
			},
			message: `SPDX SBOM has invalid redistributed integration package "bub"`,
		},
		"integration SPDX relationship drift": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				mutateIntegrationEvidenceSBOM(t, fixture, func(document map[string]any) {
					relationships := document["relationships"].([]any)
					document["relationships"] = slices.DeleteFunc(relationships, func(value any) bool {
						relationship := value.(map[string]any)
						return relationship["relatedSpdxElement"] == "SPDXRef-Integration-bub"
					})
				})
			},
			message: `SPDX SBOM is missing redistributed integration relationship for "bub"`,
		},
		"integration SPDX license drift": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				mutateIntegrationEvidenceSBOM(t, fixture, func(document map[string]any) {
					for _, value := range document["packages"].([]any) {
						packageRecord := value.(map[string]any)
						if packageRecord["SPDXID"] == "SPDXRef-Integration-bub" {
							packageRecord["licenseDeclared"] = "NOASSERTION"
						}
					}
				})
			},
			message: `SPDX SBOM has invalid redistributed integration license for "bub"`,
		},
		"integration SPDX copyright drift": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				mutateIntegrationEvidenceSBOM(t, fixture, func(document map[string]any) {
					for _, value := range document["packages"].([]any) {
						packageRecord := value.(map[string]any)
						if packageRecord["SPDXID"] == "SPDXRef-Integration-bub" {
							packageRecord["copyrightText"] = "Copyright drift"
						}
					}
				})
			},
			message: `SPDX SBOM has invalid redistributed integration copyright for "bub"`,
		},
		"unreviewed integration SPDX package": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				mutateIntegrationEvidenceSBOM(t, fixture, func(document map[string]any) {
					document["packages"] = append(document["packages"].([]any), map[string]any{
						"SPDXID": "SPDXRef-Integration-unreviewed", "name": "PowerContext integration unreviewed",
					})
				})
			},
			message: "unreviewed redistributed integration packages",
		},
		"unreviewed integration SPDX relationship": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				mutateIntegrationEvidenceSBOM(t, fixture, func(document map[string]any) {
					document["relationships"] = append(document["relationships"].([]any), map[string]any{
						"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "CONTAINS", "relatedSpdxElement": "SPDXRef-Integration-unreviewed",
					})
				})
			},
			message: "unreviewed redistributed integration relationship",
		},
		"integration license mismatch": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				if err := os.WriteFile(filepath.Join(fixture.root, "LICENSE"), []byte("changed license\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				rewriteIntegrationEvidenceChecksums(t, fixture.root)
			},
			message: `redistributed integration "bub" license`,
		},
		"manifest and archive omit approved lock path": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				manifest := readIntegrationEvidenceManifest(t, fixture.root)
				manifest.Integrations[0].LockPaths = nil
				writeIntegrationEvidenceManifest(t, fixture.root, manifest)
				if err := os.Remove(filepath.Join(fixture.root, "integrations", "bub", "uv.lock")); err != nil {
					t.Fatal(err)
				}
				rewriteIntegrationEvidenceChecksums(t, fixture.root)
			},
			message: "does not match frozen release integration inventory",
		},
		"manifest and archive replace approved required path": {
			mutate: func(t *testing.T, fixture releaseIntegrationEvidenceFixture) {
				manifest := readIntegrationEvidenceManifest(t, fixture.root)
				manifest.Integrations[0].RequiredPaths = []string{"integrations/bub/project.toml"}
				writeIntegrationEvidenceManifest(t, fixture.root, manifest)
				if err := os.Remove(filepath.Join(fixture.root, "integrations", "bub", "pyproject.toml")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture.root, "integrations", "bub", "project.toml"), []byte("replacement\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				rewriteIntegrationEvidenceChecksums(t, fixture.root)
			},
			message: "does not match frozen release integration inventory",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := writeReleaseIntegrationEvidenceFixture(t)
			test.mutate(t, fixture)
			err := verifyReleaseEvidence(fixture.root, fixture.detachedSBOM, fixture.repository)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("verifyReleaseEvidence error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestReleaseIntegrationEvidenceAcceptsReconciledArchive(t *testing.T) {
	fixture := writeReleaseIntegrationEvidenceFixture(t)
	if err := verifyReleaseEvidence(fixture.root, fixture.detachedSBOM, fixture.repository); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseIntegrationSPDXGenerationCoversFrozenArchiveInventory(t *testing.T) {
	fixture := writeReleaseIntegrationEvidenceFixture(t)
	sbomPath := filepath.Join(t.TempDir(), "SBOM.spdx.json")
	if err := os.WriteFile(sbomPath, dependencyOnlySPDX(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addIntegrationEvidenceToSBOM(sbomPath, fixture.repository); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(sbomPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(fixture.root, "SBOM.spdx.json"), fixture.detachedSBOM} {
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rewriteIntegrationEvidenceChecksums(t, fixture.root)
	if err := verifyReleaseEvidence(fixture.root, fixture.detachedSBOM, fixture.repository); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "uv.lock") || strings.Contains(string(payload), "pnpm-lock.yaml") {
		t.Fatalf("generated SPDX SBOM misclassified lock-only dependencies: %s", payload)
	}
}

func TestGenerateSBOMRemovesLockfileDependencyPackages(t *testing.T) {
	document := map[string]any{
		"packages": []any{
			map[string]any{"SPDXID": "SPDXRef-Go", "externalRefs": []any{map[string]any{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": "pkg:golang/example.com/kept@v1.0.0"}}},
			map[string]any{"SPDXID": "SPDXRef-PyPI", "externalRefs": []any{map[string]any{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": "pkg:pypi/lock-only@1.0.0"}}},
			map[string]any{"SPDXID": "SPDXRef-NPM", "externalRefs": []any{map[string]any{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": "pkg:npm/lock-only@1.0.0"}}},
		},
		"relationships": []any{
			map[string]any{"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES", "relatedSpdxElement": "SPDXRef-PyPI"},
			map[string]any{"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES", "relatedSpdxElement": "SPDXRef-Go"},
		},
	}
	filterLockfileDependencyPackages(document)
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"SPDXRef-PyPI", "SPDXRef-NPM", "lock-only"} {
		if strings.Contains(string(payload), forbidden) {
			t.Fatalf("lockfile dependency remains in SPDX document: %s", payload)
		}
	}
	if !strings.Contains(string(payload), "SPDXRef-Go") {
		t.Fatalf("Go package was removed from SPDX document: %s", payload)
	}
}

type releaseIntegrationEvidenceFixture struct {
	root         string
	detachedSBOM string
	repository   string
}

func writeReleaseIntegrationEvidenceFixture(t *testing.T) releaseIntegrationEvidenceFixture {
	t.Helper()
	repository := writeReleaseIntegrationRepository(t, reviewedReleaseIntegrations, nil)
	if err := os.WriteFile(filepath.Join(repository, "LICENSE"), []byte("project license\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	integrations, err := collectIntegrationEvidence(repository)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, releasePath := range releaseIntegrationFixturePaths {
		archivePath := filepath.Join(root, filepath.FromSlash(releasePath))
		if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archivePath, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("project license\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := dependencyManifest{
		SchemaVersion: 1,
		Modules: []dependencyRecord{{
			Path: "example.com/covered", Version: "v1.2.3",
			Licenses: []licenseRecord{{Name: "LICENSE", SHA256: strings.Repeat("a", 64)}},
		}},
		Native: []dependencyRecord{{
			Path: "example.com/native", Version: "v4.5.6",
			Licenses: []licenseRecord{{Name: "NOTICE", SHA256: strings.Repeat("b", 64)}},
		}},
		Integrations: integrations,
	}
	writeIntegrationEvidenceManifest(t, root, manifest)
	var notices strings.Builder
	for _, dependency := range append(slices.Clone(manifest.Modules), manifest.Native...) {
		writeNoticeHeader(&notices, dependency.Path, dependency.Version)
		for _, license := range dependency.Licenses {
			writeLicense(&notices, license.Name, []byte("license text\n"))
		}
	}
	if err := os.WriteFile(filepath.Join(root, "THIRD-PARTY-LICENSES.txt"), []byte(notices.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	temporarySBOM := filepath.Join(t.TempDir(), "SBOM.spdx.json")
	if err := os.WriteFile(temporarySBOM, dependencyOnlySPDX(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addIntegrationEvidenceToSBOM(temporarySBOM, repository); err != nil {
		t.Fatal(err)
	}
	sbom, err := os.ReadFile(temporarySBOM)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SBOM.spdx.json"), sbom, 0o600); err != nil {
		t.Fatal(err)
	}
	detached := filepath.Join(t.TempDir(), "release.spdx.json")
	if err := os.WriteFile(detached, sbom, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeTreeChecksums(root); err != nil {
		t.Fatal(err)
	}
	return releaseIntegrationEvidenceFixture{root: root, detachedSBOM: detached, repository: repository}
}

func readIntegrationEvidenceManifest(t *testing.T, root string) dependencyManifest {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(root, "DEPENDENCIES.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest dependencyManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func writeIntegrationEvidenceManifest(t *testing.T, root string, manifest dependencyManifest) {
	t.Helper()
	payload, err := json.Marshal(&manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "DEPENDENCIES.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rewriteIntegrationEvidenceChecksums(t *testing.T, root string) {
	t.Helper()
	if err := os.Remove(filepath.Join(root, "SHA256SUMS")); err != nil {
		t.Fatal(err)
	}
	if err := writeTreeChecksums(root); err != nil {
		t.Fatal(err)
	}
}

func mutateIntegrationEvidenceSBOM(t *testing.T, fixture releaseIntegrationEvidenceFixture, mutate func(document map[string]any)) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(fixture.root, "SBOM.spdx.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(payload, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	payload, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(fixture.root, "SBOM.spdx.json"), fixture.detachedSBOM} {
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rewriteIntegrationEvidenceChecksums(t, fixture.root)
}

func dependencyOnlySPDX() []byte {
	return []byte(`{
  "spdxVersion": "SPDX-2.3",
  "packages": [
    {"externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:golang/example.com/covered@v1.2.3"}]},
    {"externalRefs":[{"referenceCategory":"PACKAGE-MANAGER","referenceType":"purl","referenceLocator":"pkg:generic/example.com/native@v4.5.6"}]}
  ]
}
`)
}
