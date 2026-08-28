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
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestContinuousIntegrationPreservesPythonTopologyAndGoAssurance(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	workflows := filepath.Join(repository, ".github", "workflows")
	pythonTopology := map[string]bool{
		"build-artifacts.yml": true,
		"build-docker.yml":    true,
		"deploy-docs.yml":     true,
		"e2e-harness.yml":     true,
		"license-check.yml":   true,
		"master.yml":          true,
		"release-verify.yml":  true,
		"release.yml":         true,
	}
	goAssurance := map[string]bool{
		"codeql.yml":          true,
		"migration-gates.yml": true,
		"provider-smoke.yml":  true,
	}
	paths, err := filepath.Glob(filepath.Join(workflows, "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(pythonTopology)+len(goAssurance) {
		t.Errorf(
			"workflow count = %d, want %d Python-aligned workflows plus %d Go assurance workflows",
			len(paths),
			len(pythonTopology),
			len(goAssurance),
		)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		if !pythonTopology[name] && !goAssurance[name] {
			t.Errorf("workflow %s has no documented CI role", name)
		}
	}
	required := map[string][]string{
		"master.yml": {
			"name: Main", "quality:", "run: make check", "run: make contract-test",
			"tests:", "run: make unit-test", "run: make e2e-test", "pi-package:", "check-docs:",
			"migration-assurance:", "uses: ./.github/workflows/migration-gates.yml",
		},
		"migration-gates.yml": {
			"name: Go migration assurance", "workflow_call:", "make test-race",
			"docker build --pull --target powercontext -t powercontext:ci .",
			"FuzzRestrictedPickleJobDecoder", "Frozen Python Oracle and differential fixtures",
			"Run Python to Go to Python compatibility tests", "Run the frozen Python versus Go HTTP differential",
			"OceanBase live compatibility", "Standard (", "Full build tags (",
			"Host adapters", "Evaluation control plane and console",
		},
		"codeql.yml": {
			"name: CodeQL", "pull_request:", "push:", "branches: [main]", "schedule:", "workflow_dispatch:",
			"security-events: write", "persist-credentials: false",
			"github/codeql-action/init@", "build-mode: manual",
			"CGO_ENABLED=1 go build -tags sqlite_fts5 ./...",
			"github/codeql-action/analyze@",
		},
		"provider-smoke.yml": {
			"name: Provider smoke", "workflow_dispatch:", "environment: provider-smoke",
			"TestRealProviderSmoke", "timeout-minutes: 10",
		},
		"e2e-harness.yml": {
			"name: E2E harness", "validate:", "acceptance:", "database: [sqlite, oceanbase]",
			"make harness-compose-acceptance", "Scan acceptance evidence",
			"Upload sanitized acceptance diagnostics", "Enforce acceptance evidence policy",
			"scenario_outcome=", "--network none",
			"ghcr.io/trufflesecurity/trufflehog@sha256:",
			"steps.evidence_scan.outcome != 'success'", "retention-days: 14",
		},
		"deploy-docs.yml": {
			"name: Deploy documentation", "workflow_call:", "workflow_dispatch:",
			"run: make docs-build", "actions/deploy-pages@",
		},
		"release.yml": {
			"name: Release", "types: [published]", "release-verify:", "deploy-docs:",
			"uses: ./.github/workflows/release-verify.yml", "uses: ./.github/workflows/deploy-docs.yml",
		},
	}
	for name, values := range required {
		payload, err := os.ReadFile(filepath.Join(workflows, name))
		if err != nil {
			t.Fatal(err)
		}
		contents := string(payload)
		for _, value := range values {
			if !strings.Contains(contents, value) {
				t.Errorf("%s is missing %q", name, value)
			}
		}
	}
	e2eHarness, err := os.ReadFile(filepath.Join(workflows, "e2e-harness.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(e2eHarness), "continue-on-error:") {
		t.Error("e2e-harness.yml must not suppress acceptance or evidence failures")
	}
}

func TestCIThirdPartyExecutablesUseImmutableReferences(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	actionUse := regexp.MustCompile(
		"(?m)^[\\t ]*(?:-[\\t ]*)?uses:[\\t ]+([^@\\s]+)@([^\\s#]+)([^\\r\\n]*)$",
	)
	containerUse := regexp.MustCompile(
		"(?m)^[\\t ]*(?:container|image|[A-Z][A-Z0-9_]*_IMAGE):[\\t ]+([^\\s#]+)",
	)
	dockerActionUse := regexp.MustCompile(
		"(?m)^[\\t ]*(?:-[\\t ]*)?uses:[\\t ]+docker://([^\\s#]+)",
	)
	commit := regexp.MustCompile("^[0-9a-f]{40}$")
	containerDigest := regexp.MustCompile("^[^@\\s]+@sha256:[0-9a-f]{64}$")
	staticContainerReferences := 0

	err := filepath.WalkDir(filepath.Join(repository, ".github"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || (filepath.Ext(path) != ".yml" && filepath.Ext(path) != ".yaml") {
			return nil
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range actionUse.FindAllStringSubmatch(string(payload), -1) {
			action, ref, annotation := match[1], match[2], match[3]
			if strings.HasPrefix(action, "docker://") {
				continue
			}
			if !commit.MatchString(ref) {
				t.Errorf("%s uses mutable action reference %s@%s", filepath.ToSlash(path), action, ref)
			}
			if !strings.Contains(annotation, "# v") {
				t.Errorf("%s must keep a human-readable version comment for %s@%s", filepath.ToSlash(path), action, ref)
			}
		}
		for _, match := range containerUse.FindAllStringSubmatch(string(payload), -1) {
			reference := strings.Trim(match[1], "\"'")
			if strings.Contains(reference, "$"+"{{") {
				continue
			}
			staticContainerReferences++
			if !containerDigest.MatchString(reference) {
				t.Errorf("%s uses mutable container image %s", filepath.ToSlash(path), reference)
			}
		}
		for _, match := range dockerActionUse.FindAllStringSubmatch(string(payload), -1) {
			reference := strings.Trim(match[1], "\"'")
			staticContainerReferences++
			if !containerDigest.MatchString(reference) {
				t.Errorf("%s uses mutable Docker action image %s", filepath.ToSlash(path), reference)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if staticContainerReferences == 0 {
		t.Fatal("no static CI container references were checked")
	}
}

func TestWorkflowsReuseTheGoSetup(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	workflows, err := filepath.Glob(filepath.Join(repository, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range workflows {
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(payload), "uses: actions/setup-go@") {
			t.Errorf("%s bypasses .github/actions/setup-go-env", filepath.Base(path))
		}
	}
}

func TestCandidateDeliveryWorkflowsExerciseTheirArtifacts(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	tests := map[string][]string{
		"build-artifacts.yml": {
			"workflow_dispatch:",
			"make package-standard",
			"make package-full",
			"go run ./tools/process-smoke",
			"dist/*.spdx.json",
			"retention-days: 30",
		},
		"build-docker.yml": {
			"workflow_dispatch:",
			"target: powercontext",
			"target: powercontext-full",
			"platforms: linux/amd64,linux/arm64",
			"outputs: type=oci",
			`"$image" server run`,
			"retention-days: 30",
		},
	}
	for name, requiredValues := range tests {
		payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		workflow := string(payload)
		for _, required := range requiredValues {
			if !strings.Contains(workflow, required) {
				t.Errorf("%s is missing %q", name, required)
			}
		}
	}
}

func TestReleaseVerificationRechecksPublishedSurfaces(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "release-verify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(payload)
	for _, required := range []string{
		"workflow_call:",
		"workflow_dispatch:",
		"gh release download",
		"sha256sum --check --strict SHA256SUMS",
		"go run ./tools/process-smoke",
		"docker buildx imagetools inspect",
		`"$IMAGE" server run`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release verification workflow is missing %q", required)
		}
	}
}

func TestLicenseHeadersHaveOneLocalRepairAndCIContract(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	tests := map[string][]string{
		"Makefile": {
			"github.com/apache/skywalking-eyes/cmd/license-eye@v0.8.0",
			"license-check:",
			"header check",
			"license-fix:",
			"header fix",
		},
		filepath.Join(".github", "workflows", "license-check.yml"): {
			"pull_request:",
			"uses: apache/skywalking-eyes/header@61275cc80d0798a405cb070f7d3a8aaf7cf2c2c1 # v0.8.0",
			"config: .licenserc.yaml",
			"mode: check",
		},
		".licenserc.yaml": {
			"copyright-owner: OceanBase",
			"- '**/*_gen.go'",
			"internal/sqlstore/sqlitevec/sqlite-vec.c",
			"comment: never",
		},
	}
	for relative, requiredValues := range tests {
		payload, err := os.ReadFile(filepath.Join(repository, relative))
		if err != nil {
			t.Fatal(err)
		}
		contents := string(payload)
		for _, required := range requiredValues {
			if !strings.Contains(contents, required) {
				t.Errorf("%s is missing %q", filepath.ToSlash(relative), required)
			}
		}
	}
}
