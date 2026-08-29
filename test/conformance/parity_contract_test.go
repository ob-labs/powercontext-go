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
			mutant: strings.Replace(string(contents), `"schema_version": 1,`, `"schema_version": 999, "schema_version": 1,`, 1),
		},
		{
			name:   "unknown member",
			mutant: strings.Replace(string(contents), `"schema_version": 1,`, `"schema_version": 1, "future_semantics": true,`, 1),
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

func TestFrozenOracleWorkflowRejectsNonVerifyingIdentityStep(t *testing.T) {
	if validOracleIdentityCommand("echo "+oracleCommit, oracleCommit) {
		t.Error("accepted an Oracle identity step that only prints the expected commit")
	}
}

func TestParityContractRecordsSeparateConcepts(t *testing.T) {
	contract := readParityContract(t)
	root := repositoryRoot(t)

	if contract.SchemaVersion != 1 {
		t.Fatalf("parity contract schema version = %d, want 1", contract.SchemaVersion)
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
	checkout, verify := "", ""
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
			if !validOracleIdentityCommand(step.Run, contract.FrozenReleaseOracle.Commit) {
				t.Errorf("Verify Oracle identity step does not pin the contract frozen Oracle %q", contract.FrozenReleaseOracle.Commit)
			}
		}
	}
	if checkout == "" {
		t.Error("frozen-oracle job has no 'Check out frozen Python Oracle' step")
	}
	if verify == "" {
		t.Error("frozen-oracle job has no 'Verify Oracle identity' step")
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

func validOracleIdentityCommand(command, commit string) bool {
	want := `test "$(git -C _oracle rev-parse HEAD)" = ` + commit
	return strings.TrimSpace(command) == want
}
