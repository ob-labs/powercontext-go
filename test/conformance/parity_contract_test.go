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

package conformance_test

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"gopkg.in/yaml.v2"
)

// The parity contract records four separate concepts from the WP1 epic (#3):
// the upstream repository, the exact target SHA, the frozen release Oracle,
// and the active parity target. They must stay distinct, well-formed facts.
type parityContract struct {
	SchemaVersion int `json:"schema_version"`
	Upstream      struct {
		Repository    string `json:"repository"`
		URL           string `json:"url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"upstream"`
	FrozenReleaseOracle struct {
		Commit            string `json:"commit"`
		Baseline          string `json:"baseline"`
		EvidenceDirectory string `json:"evidence_directory"`
	} `json:"frozen_release_oracle"`
	ReleaseTarget struct {
		Tag           string `json:"tag"`
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		TestCaseCount int    `json:"test_case_count"`
		TestFileCount int    `json:"test_file_count"`
		Wheel         struct {
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
		} `json:"wheel"`
		Sdist struct {
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
		} `json:"sdist"`
	} `json:"release_target"`
	ExactTargetSHA      string `json:"exact_target_sha"`
	TargetTestCaseCount int    `json:"target_test_case_count"`
	ActiveParityTarget  string `json:"active_parity_target"`
}

var commitSHA1Pattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func TestParityContractRejectsMalformedRepositorySlug(t *testing.T) {
	for _, slug := range []string{"oceanbase/powercontext/extra", "oceanbase/\tpowercontext"} {
		if validGitHubRepositorySlug(slug) {
			t.Errorf("accepted malformed GitHub repository slug %q", slug)
		}
	}
}

func TestParityContractRejectsAmbiguousJSON(t *testing.T) {
	contents, err := os.ReadFile("parity-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutant string
	}{
		{
			name:   "duplicate member",
			mutant: strings.Replace(string(contents), `"schema_version": 2,`, `"schema_version": 999, "schema_version": 2,`, 1),
		},
		{
			name:   "unknown member",
			mutant: strings.Replace(string(contents), `"schema_version": 2,`, `"schema_version": 2, "future_semantics": true,`, 1),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.mutant == string(contents) {
				t.Fatal("mutant construction did not change the parity contract")
			}
			if _, err := decodeParityContract([]byte(test.mutant)); err == nil {
				t.Error("accepted ambiguous parity contract JSON")
			}
		})
	}
}

func TestWorkflowRejectsNonVerifyingIdentitySteps(t *testing.T) {
	tests := []struct {
		name         string
		checkoutPath string
		commit       string
	}{
		{name: "frozen Oracle", checkoutPath: "_oracle", commit: oracleCommit},
		{name: "active parity target", checkoutPath: "_target", commit: "target-commit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if validCheckoutIdentityCommand("echo "+test.commit, test.checkoutPath, test.commit) {
				t.Errorf("accepted a non-verifying identity step for %s", test.checkoutPath)
			}
		})
	}
}

func TestParityContractRecordsSeparateConcepts(t *testing.T) {
	contract := readParityContract(t)
	root := repositoryRoot(t)

	if contract.SchemaVersion != 2 {
		t.Fatalf("parity contract schema version = %d, want 2", contract.SchemaVersion)
	}

	// Concept 1: the upstream repository identity.
	slug := contract.Upstream.Repository
	if !validGitHubRepositorySlug(slug) {
		t.Fatalf("upstream repository %q is not a GitHub owner/name slug", slug)
	}
	if want := "https://github.com/" + slug; contract.Upstream.URL != want {
		t.Errorf("upstream URL = %q, want %q", contract.Upstream.URL, want)
	}
	if contract.Upstream.DefaultBranch == "" {
		t.Error("upstream default branch is empty")
	}

	// Concept 2: the frozen release Oracle.
	oracle := contract.FrozenReleaseOracle.Commit
	if !commitSHA1Pattern.MatchString(oracle) {
		t.Fatalf("frozen release Oracle commit %q is not a 40-hex SHA-1", oracle)
	}
	if oracle != oracleCommit {
		t.Errorf("frozen release Oracle commit = %s, want the oracleCommit constant %s", oracle, oracleCommit)
	}
	if contract.FrozenReleaseOracle.Baseline != "python-v0.0.2" {
		t.Errorf("frozen release Oracle baseline = %q, want %q", contract.FrozenReleaseOracle.Baseline, "python-v0.0.2")
	}
	evidence := filepath.Clean(filepath.FromSlash(contract.FrozenReleaseOracle.EvidenceDirectory))
	if filepath.IsAbs(evidence) || evidence == ".." || strings.HasPrefix(evidence, ".."+string(filepath.Separator)) {
		t.Fatalf("Oracle evidence directory escapes the repository: %q", evidence)
	}
	if info, err := os.Stat(filepath.Join(root, evidence)); err != nil || !info.IsDir() {
		t.Errorf("Oracle evidence directory %s is missing: %v", evidence, err)
	}
	var manifest struct {
		OracleCommit string `json:"oracle_commit"`
	}
	decodeJSONFile(t, filepath.Join(root, evidence, "manifest.json"), &manifest)
	if manifest.OracleCommit != oracle {
		t.Errorf("manifest.json oracle_commit = %s, want the contract frozen Oracle %s", manifest.OracleCommit, oracle)
	}

	// Concept 3: the exact target SHA and its recorded test inventory.
	target := contract.ExactTargetSHA
	if !commitSHA1Pattern.MatchString(target) {
		t.Fatalf("exact target SHA %q is not a 40-hex SHA-1", target)
	}
	if target == oracle {
		t.Errorf("exact target SHA collapses into the frozen release Oracle %s; they must stay separate concepts", oracle)
	}
	if contract.TargetTestCaseCount <= 0 {
		t.Errorf("target test case count = %d, want a positive recorded inventory", contract.TargetTestCaseCount)
	}

	// Concept 4: the active parity target.
	active := contract.ActiveParityTarget
	if !commitSHA1Pattern.MatchString(active) {
		t.Fatalf("active parity target %q is not a 40-hex SHA-1", active)
	}
	if active == oracle {
		t.Errorf("active parity target collapses into the frozen release Oracle %s; parity work must measure a newer upstream state", oracle)
	}
	if active != target || active != contract.ReleaseTarget.Commit {
		t.Errorf("active parity target = %s, exact target = %s, release target = %s", active, target, contract.ReleaseTarget.Commit)
	}
	if contract.TargetTestCaseCount != contract.ReleaseTarget.TestCaseCount {
		t.Errorf("active target cases = %d, release target cases = %d", contract.TargetTestCaseCount, contract.ReleaseTarget.TestCaseCount)
	}
}

func TestParityContractRecordsSignedV010ReleaseIdentity(t *testing.T) {
	contract := readParityContract(t)
	release := contract.ReleaseTarget
	if release.Tag != "powercontext-v0.1.0" {
		t.Errorf("release tag = %q, want powercontext-v0.1.0", release.Tag)
	}
	if release.Version != "0.1.0" {
		t.Errorf("release version = %q, want 0.1.0", release.Version)
	}
	if release.Commit != "7b736206a53a6de6f43d4b517893ee1a80e7183d" {
		t.Errorf("release commit = %q, want exact v0.1.0 release commit", release.Commit)
	}
	if release.TestCaseCount != 812 || release.TestFileCount != 132 {
		t.Errorf("release inventory = %d cases in %d files, want 812 cases in 132 files", release.TestCaseCount, release.TestFileCount)
	}
	if release.Wheel.Filename != "powercontext-0.1.0-py3-none-any.whl" ||
		release.Wheel.SHA256 != "94f8fef36d4afcee09dd5231fbe5edfe47e42e41994596b775e2f203ef6fac72" {
		t.Errorf("wheel identity = %#v", release.Wheel)
	}
	if release.Sdist.Filename != "powercontext-0.1.0.tar.gz" ||
		release.Sdist.SHA256 != "18d47a335340b0870216e2cc0fb1fd8e4d865880155daeea3b01187c950fd746" {
		t.Errorf("sdist identity = %#v", release.Sdist)
	}
}

func TestParityContractMatchesFrozenOracleWorkflow(t *testing.T) {
	contract := readParityContract(t)
	root := repositoryRoot(t)

	contents, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				With struct {
					Repository string `yaml:"repository"`
					Ref        string `yaml:"ref"`
				} `yaml:"with"`
				Run string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["frozen-oracle"]
	if !ok {
		t.Fatal("migration-gates.yml has no frozen-oracle job")
	}
	var delta struct {
		FromCommit string `json:"from_commit"`
	}
	decodeJSONFile(t, filepath.Join(root, "test", "conformance", "target-delta.json"), &delta)
	checkout, verify := "", ""
	previousCheckout, previousVerify := "", ""
	targetCheckout, targetVerify, deltaVerify := "", "", ""
	for _, step := range job.Steps {
		switch step.Name {
		case "Check out frozen Python Oracle":
			checkout = step.Name
			if step.With.Repository != contract.Upstream.Repository {
				t.Errorf("frozen-oracle checkout repository = %q, want the contract upstream %q", step.With.Repository, contract.Upstream.Repository)
			}
			if step.With.Ref != contract.FrozenReleaseOracle.Commit {
				t.Errorf("frozen-oracle checkout ref = %q, want the contract frozen Oracle %q", step.With.Ref, contract.FrozenReleaseOracle.Commit)
			}
		case "Verify Oracle identity":
			verify = step.Name
			if !validCheckoutIdentityCommand(step.Run, "_oracle", contract.FrozenReleaseOracle.Commit) {
				t.Errorf("Verify Oracle identity step does not pin the contract frozen Oracle %q", contract.FrozenReleaseOracle.Commit)
			}
		case "Check out the previous parity target":
			previousCheckout = step.Name
			if step.With.Repository != contract.Upstream.Repository || step.With.Ref != delta.FromCommit {
				t.Errorf("previous target checkout = %q@%q, want %q@%q", step.With.Repository, step.With.Ref, contract.Upstream.Repository, delta.FromCommit)
			}
		case "Verify previous parity target identity":
			previousVerify = step.Name
			if !validCheckoutIdentityCommand(step.Run, "_previous_target", delta.FromCommit) {
				t.Errorf("previous target identity step does not pin ledger from_commit %q", delta.FromCommit)
			}
		case "Check out the active parity target":
			targetCheckout = step.Name
			if step.With.Repository != contract.Upstream.Repository {
				t.Errorf("parity target checkout repository = %q, want the contract upstream %q", step.With.Repository, contract.Upstream.Repository)
			}
			if step.With.Ref != contract.ExactTargetSHA {
				t.Errorf("parity target checkout ref = %q, want the contract exact target SHA %q", step.With.Ref, contract.ExactTargetSHA)
			}
		case "Verify parity target identity":
			targetVerify = step.Name
			if !validCheckoutIdentityCommand(step.Run, "_target", contract.ExactTargetSHA) {
				t.Errorf("Verify parity target identity step does not pin the contract exact target SHA %q", contract.ExactTargetSHA)
			}
		case "Regenerate and compare frozen fixtures":
			if strings.Contains(step.Run, "-check-delta") && strings.Contains(step.Run, "-previous-upstream _previous_target") &&
				strings.Contains(step.Run, "-release-upstream _target") {
				deltaVerify = step.Name
			}
		}
	}
	if checkout == "" {
		t.Error("frozen-oracle job has no 'Check out frozen Python Oracle' step")
	}
	if verify == "" {
		t.Error("frozen-oracle job has no 'Verify Oracle identity' step")
	}
	if previousCheckout == "" {
		t.Error("frozen-oracle job has no 'Check out the previous parity target' step")
	}
	if previousVerify == "" {
		t.Error("frozen-oracle job has no 'Verify previous parity target identity' step")
	}
	if targetCheckout == "" {
		t.Error("frozen-oracle job has no 'Check out the active parity target' step")
	}
	if targetVerify == "" {
		t.Error("frozen-oracle job has no 'Verify parity target identity' step")
	}
	if deltaVerify == "" {
		t.Error("frozen-oracle job does not verify the reviewed target delta")
	}
}

func readParityContract(t *testing.T) parityContract {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("parity-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := decodeParityContract(contents)
	if err != nil {
		t.Fatal(err)
	}
	return contract
}

func decodeParityContract(contents []byte) (parityContract, error) {
	var contract parityContract
	if err := json.Unmarshal(contents, &contract, json.RejectUnknownMembers(true)); err != nil {
		return parityContract{}, err
	}
	return contract, nil
}

func validGitHubRepositorySlug(slug string) bool {
	owner, name, split := strings.Cut(slug, "/")
	return split && owner != "" && name != "" && !strings.ContainsRune(name, '/') &&
		strings.IndexFunc(slug, unicode.IsSpace) < 0
}

func validCheckoutIdentityCommand(command, checkoutPath, commit string) bool {
	want := `test "$(git -C ` + checkoutPath + ` rev-parse HEAD)" = ` + commit
	return strings.TrimSpace(command) == want
}
