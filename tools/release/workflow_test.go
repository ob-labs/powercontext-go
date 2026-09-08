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
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
)

func TestScorecardWorkflowContract(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "scorecard.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkScorecardWorkflow(payload); err != nil {
		t.Fatal(err)
	}
}

func TestNightlyReliabilityWorkflowContract(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "nightly-reliability.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkNightlyReliabilityWorkflow(payload); err != nil {
		t.Fatal(err)
	}
}

func TestSupplyChainWorkflowsRejectContractMutants(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	tests := []struct {
		name        string
		workflow    string
		old         string
		replacement string
		check       func([]byte) error
	}{
		{name: "Scorecard token permission", workflow: "scorecard.yml", old: "      id-token: write\n", check: checkScorecardWorkflow},
		{name: "Scorecard immutable action", workflow: "scorecard.yml", old: "ossf/scorecard-action@2d1146689b8cda280b9bc96326124645441f03bc", replacement: "ossf/scorecard-action@v2.4.4", check: checkScorecardWorkflow},
		{name: "Scorecard SARIF upload", workflow: "scorecard.yml", old: "github/codeql-action/upload-sarif@cdf488f595d80d6e07e03d4674febd5ab45fa938", replacement: "github/codeql-action/init@cdf488f595d80d6e07e03d4674febd5ab45fa938", check: checkScorecardWorkflow},
		{name: "Scorecard result publication", workflow: "scorecard.yml", old: "publish_results: true", replacement: "publish_results: false", check: checkScorecardWorkflow},
		{name: "nightly fuzz inventory", workflow: "nightly-reliability.yml", old: "          - package: ./internal/httpapi\n            target: FuzzValidJSONUnicodeNeverPanics\n", check: checkNightlyReliabilityWorkflow},
		{name: "nightly fuzz duration", workflow: "nightly-reliability.yml", old: "-fuzztime=4m", replacement: "-fuzztime=3s", check: checkNightlyReliabilityWorkflow},
		{name: "nightly non-fail-fast matrix", workflow: "nightly-reliability.yml", old: "fail-fast: false", replacement: "fail-fast: true", check: checkNightlyReliabilityWorkflow},
		{name: "nightly race detector", workflow: "nightly-reliability.yml", old: "go test -json -race -shuffle=on -count=25", replacement: "go test -json -shuffle=on -count=25", check: checkNightlyReliabilityWorkflow},
		{name: "nightly repetition count", workflow: "nightly-reliability.yml", old: "go test -json -race -shuffle=on -count=25", replacement: "go test -json -race -shuffle=on", check: checkNightlyReliabilityWorkflow},
		{name: "nightly pipeline status", workflow: "nightly-reliability.yml", old: "test_status=\"${PIPESTATUS[0]}\"", replacement: "test_status=\"$?\"", check: checkNightlyReliabilityWorkflow},
		{name: "nightly bounded output", workflow: "nightly-reliability.yml", old: "tail -c 1048576", replacement: "cp", check: checkNightlyReliabilityWorkflow},
		{name: "nightly failure-only output", workflow: "nightly-reliability.yml", old: "- name: Upload bounded failing test output\n        if: failure()", replacement: "- name: Upload bounded failing test output\n        if: always()", check: checkNightlyReliabilityWorkflow},
		{name: "nightly aggregate cancellation", workflow: "nightly-reliability.yml", old: "test \"$FUZZ_RESULT\" = success", replacement: "test \"$FUZZ_RESULT\" != failure", check: checkNightlyReliabilityWorkflow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", test.workflow))
			if err != nil {
				t.Fatal(err)
			}
			mutant := strings.Replace(string(payload), test.old, test.replacement, 1)
			if mutant == string(payload) {
				t.Fatalf("mutant did not change %s", test.workflow)
			}
			if err := test.check([]byte(mutant)); err == nil {
				t.Fatalf("contract accepted %s mutant", test.name)
			}
		})
	}
}

func checkScorecardWorkflow(payload []byte) error {
	return checkWorkflowPhrases(payload, "scorecard.yml", []string{
		"name: Scorecard supply-chain security", "branch_protection_rule:", "branches: [main]", "schedule:", "workflow_dispatch:",
		"permissions: {}", "timeout-minutes: 15", "contents: read", "security-events: write", "id-token: write",
		"ossf/scorecard-action@2d1146689b8cda280b9bc96326124645441f03bc", "results_file: results.sarif",
		"results_format: sarif", "publish_results: true", "retention-days: 5",
		"github/codeql-action/upload-sarif@cdf488f595d80d6e07e03d4674febd5ab45fa938", "sarif_file: results.sarif",
	})
}

func checkNightlyReliabilityWorkflow(payload []byte) error {
	return checkWorkflowPhrases(payload, "nightly-reliability.yml", []string{
		"name: Nightly reliability", "schedule:", "workflow_dispatch:", "contents: read", "fail-fast: false",
		"FuzzMarshalCanonicalJSONIsIdempotent", "FuzzRestrictedPickleJobDecoder", "FuzzAgentSkillFrontmatterParser",
		"FuzzTruncateUTF8PreservesRuneBoundariesAndBudget", "FuzzValidJSONUnicodeNeverPanics", "-fuzztime=4m",
		"- name: Upload failing fuzz corpus\n        if: failure()", "if-no-files-found: warn",
		"fuzzing-status:", "needs: [fuzz]", "if: always()", "test \"$FUZZ_RESULT\" = success",
		"go test -json -race -shuffle=on -count=25", "./internal/runtime ./internal/sqlstore ./source ./server",
		"test_status=\"${PIPESTATUS[0]}\"", "GITHUB_STEP_SUMMARY", "tail -c 1048576",
		`s|$RUNNER_TEMP|[runner-temp]|g`, `s|$GITHUB_WORKSPACE|[workspace]|g`, `s|$HOME|[home]|g`,
		"- name: Upload bounded stability summary\n        if: always()",
		"- name: Upload bounded failing test output\n        if: failure()", "retention-days: 14",
	})
}

func checkWorkflowPhrases(payload []byte, name string, required []string) error {
	var document any
	if err := yaml.Unmarshal(payload, &document); err != nil {
		return fmt.Errorf("%s is invalid YAML: %w", name, err)
	}
	contents := string(payload)
	for _, phrase := range required {
		if !strings.Contains(contents, phrase) {
			return fmt.Errorf("%s is missing %q", name, phrase)
		}
	}
	return nil
}

func TestGoPrimaryMonorepoLinguistPolicy(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	wantDetectable := map[string]bool{
		"evaluation/**":          false,
		"integrations/**":        false,
		"test/conformance/*.py":  false,
		"test/differential/*.py": false,
	}
	for _, line := range strings.Split(string(payload), "\n") {
		fields := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(fields) < 2 {
			continue
		}
		path := fields[0]
		for _, attribute := range fields[1:] {
			switch attribute {
			case "-linguist-detectable":
				if _, ok := wantDetectable[path]; !ok {
					t.Errorf(".gitattributes has broad or unowned linguist classification %q", line)
					continue
				}
				wantDetectable[path] = true
			case "linguist-vendored", "linguist-generated":
				t.Errorf(".gitattributes must not classify maintained sources as %q", attribute)
			}
		}
	}
	for path, found := range wantDetectable {
		if !found {
			t.Errorf(".gitattributes is missing %q", path+" -linguist-detectable")
		}
	}
}

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
		"codeql.yml":              true,
		"migration-gates.yml":     true,
		"nightly-reliability.yml": true,
		"provider-smoke.yml":      true,
		"scorecard.yml":           true,
		"windows-contract.yml":    true,
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
			"name: Main", "go-compat:", "quality:", "run: make check", "run: make contract-test",
			"license-dependencies:", "run: make license-dependencies",
			"dependency-security:", "run: make dependency-security",
			"tests:", "run: make unit-test", "run: make e2e-test", "Write bounded process diagnostics", "Upload process diagnostics", "check-docs:",
			"migration-assurance:", "uses: ./.github/workflows/migration-gates.yml",
		},
		"migration-gates.yml": {
			"name: Go migration assurance", "workflow_call:", "make test-race",
			"docker build --pull --target powercontext -t powercontext:ci .",
			"FuzzRestrictedPickleJobDecoder", "Frozen Python Oracle and differential fixtures",
			"Run Python to Go to Python compatibility tests", "Run the frozen Python versus Go HTTP differential",
			"Standard (", "Full build tags (", "Pre-WP6 host adapters", "Codex/SQLite evaluation control plane",
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
		"scorecard.yml": {
			"name: Scorecard supply-chain security", "branch_protection_rule:", "schedule:", "workflow_dispatch:",
			"ossf/scorecard-action@", "github/codeql-action/upload-sarif@",
		},
		"nightly-reliability.yml": {
			"name: Nightly reliability", "schedule:", "workflow_dispatch:", "Fuzzing", "-fuzztime=4m",
			"go test -json -race -shuffle=on -count=25",
		},
		"windows-contract.yml": {
			"name: Windows contract checkout", "runs-on: windows-2025", "timeout-minutes: 10",
			"Verify LF attributes and frozen fixture hashes", "Get-FileHash -Algorithm SHA256",
			"git check-attr eol", "git diff --exit-code",
		},
		"e2e-harness.yml": {
			"name: E2E harness", "validate:", "acceptance:", "database: [sqlite]",
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
			"name: Release", "push:", "tags:", "workflow_dispatch:", "release-verify:", "deploy-docs:",
			"uses: ./.github/workflows/release-verify.yml", "uses: ./.github/workflows/deploy-docs.yml",
		},
	}
	for name, values := range required {
		payload, readErr := os.ReadFile(filepath.Join(workflows, name))
		if readErr != nil {
			t.Fatal(readErr)
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

func TestActiveWorkflowsExcludeUnsupportedProductMatrix(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	tests := map[string][]string{
		"master.yml":           {"pi-package:", "make pi-test"},
		"migration-gates.yml":  {"oceanbase-live:", "retained-host-adapters:", "make test-oceanbase-live"},
		"e2e-harness.yml":      {"oceanbase"},
		"build-artifacts.yml":  {"integrations/dsh", "pnpm/action-setup@", "actions/setup-node@"},
		"windows-contract.yml": {"seekdb", "integrations/dsh", "integrations/opencode", "integrations/pi"},
	}
	for workflow, forbidden := range tests {
		payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", workflow))
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range forbidden {
			if strings.Contains(strings.ToLower(string(payload)), strings.ToLower(value)) {
				t.Errorf("%s still contains unsupported product evidence %q", workflow, value)
			}
		}
	}
}

func TestProviderSmokeForwardsRequiredEmbeddingDimension(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "provider-smoke.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			WorkflowDispatch struct {
				Inputs map[string]struct {
					Required bool   `yaml:"required"`
					Type     string `yaml:"type"`
				} `yaml:"inputs"`
			} `yaml:"workflow_dispatch"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	input, found := workflow.On.WorkflowDispatch.Inputs["embedding_dimension"]
	if !found || !input.Required || input.Type != "string" {
		t.Fatalf("embedding_dimension input = %#v, found %t", input, found)
	}
	for _, step := range workflow.Jobs["real-provider"].Steps {
		if step.Name != "Make one bounded real request" {
			continue
		}
		if got := step.Env["POWERCONTEXT_REAL_SMOKE_EMBEDDING_DIMENSION"]; got != "${{ inputs.embedding_dimension }}" {
			t.Fatalf("embedding dimension environment = %q", got)
		}
		return
	}
	t.Fatal("provider-smoke.yml has no real provider request step")
}

func TestMigrationQualityRunsModuleIntegrity(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	quality, ok := workflow.Jobs["quality"]
	if !ok {
		t.Fatal("migration-gates.yml has no quality job")
	}
	for _, step := range quality.Steps {
		if step.Name == "Verify owned Go module integrity" && strings.TrimSpace(step.Run) == "make module-integrity" {
			return
		}
	}
	t.Fatal("migration-gates.yml quality job does not execute make module-integrity")
}

func TestDependencyReviewRejectsUnsafePullRequestDependencyChanges(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "master.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			If      string `yaml:"if"`
			RunsOn  string `yaml:"runs-on"`
			Timeout int    `yaml:"timeout-minutes"`
			Steps   []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				With struct {
					FailOnSeverity     string `yaml:"fail-on-severity"`
					FailOnScopes       string `yaml:"fail-on-scopes"`
					LicenseCheck       string `yaml:"license-check"`
					VulnerabilityCheck string `yaml:"vulnerability-check"`
				} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["dependency-review"]
	if !ok {
		t.Fatal("master.yml has no dependency-review job")
	}
	if job.If != "github.event_name == 'pull_request'" || job.RunsOn != "ubuntu-24.04" || job.Timeout != 10 {
		t.Fatalf("dependency-review job contract = if %q, runs-on %q, timeout %d", job.If, job.RunsOn, job.Timeout)
	}
	if len(job.Steps) != 2 {
		t.Fatalf("dependency-review step count = %d, want 2", len(job.Steps))
	}
	if job.Steps[0].Name != "Check out" || job.Steps[0].Uses != "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1" {
		t.Fatalf("dependency-review checkout step = %#v", job.Steps[0])
	}
	step := job.Steps[1]
	if step.Name != "Review pull request dependency changes" || step.Uses != "actions/dependency-review-action@a1d282b36b6f3519aa1f3fc636f609c47dddb294" {
		t.Fatalf("dependency-review step = %#v", step)
	}
	if step.With.FailOnSeverity != "low" || step.With.FailOnScopes != "runtime,development,unknown" ||
		step.With.LicenseCheck != "true" || step.With.VulnerabilityCheck != "true" {
		t.Fatalf(
			"dependency-review policy = severity %q, scopes %q, license %q, vulnerability %q",
			step.With.FailOnSeverity, step.With.FailOnScopes, step.With.LicenseCheck, step.With.VulnerabilityCheck,
		)
	}
}

func TestMigrationRaceDebtValidatesTheLedgerBeforeTheFullRaceSuite(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Name           string `yaml:"name"`
			RunsOn         string `yaml:"runs-on"`
			TimeoutMinutes int    `yaml:"timeout-minutes"`
			Steps          []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Env  map[string]string `yaml:"env"`
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["race-debt"]
	if !ok {
		t.Fatal("migration-gates.yml has no race-debt job")
	}
	if job.Name != "Race debt" || job.RunsOn != "ubuntu-24.04" || job.TimeoutMinutes != 30 {
		t.Fatalf(
			"race-debt job identity = (%q, %q, %d), want (%q, %q, %d)",
			job.Name, job.RunsOn, job.TimeoutMinutes,
			"Race debt", "ubuntu-24.04", 30,
		)
	}
	wantSteps := []struct {
		name string
		if_  string
		env  map[string]string
		uses string
		run  string
	}{
		{name: "Check out", uses: "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"},
		{name: "Set up the Go environment", uses: "./.github/actions/setup-go-env"},
		{
			name: "Reject new temporary race exclusions",
			if_:  "github.event_name == 'pull_request'",
			env:  map[string]string{"BASE_SHA": "${{ github.event.pull_request.base.sha }}"},
			run: strings.TrimSpace(`
git fetch --no-tags --depth=1 origin "$BASE_SHA"
baseline="$RUNNER_TEMP/race-debt-base.json"
git show "$BASE_SHA:.github/race-debt.json" > "$baseline"
make race-debt-check RACE_DEBT_BASELINE="$baseline"
`),
		},
		{name: "Validate the race-debt ledger", run: "make race-debt-check"},
		{name: "Run all Go tests with the race detector", run: "make test-race"},
	}
	if len(job.Steps) != len(wantSteps) {
		t.Fatalf("race-debt step count = %d, want %d", len(job.Steps), len(wantSteps))
	}
	for index, want := range wantSteps {
		got := job.Steps[index]
		if got.Name != want.name || got.If != want.if_ || !maps.Equal(got.Env, want.env) || got.Uses != want.uses || strings.TrimSpace(got.Run) != want.run {
			t.Fatalf(
				"race-debt step %d = (%q, %q, %#v, %q, %q), want (%q, %q, %#v, %q, %q)",
				index, got.Name, got.If, got.Env, got.Uses, strings.TrimSpace(got.Run), want.name, want.if_, want.env, want.uses, want.run,
			)
		}
	}
}

func TestMigrationRaceDebtFunctionalCoverageRunsSeparately(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Name           string `yaml:"name"`
			RunsOn         string `yaml:"runs-on"`
			TimeoutMinutes int    `yaml:"timeout-minutes"`
			Steps          []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["race-debt-functional"]
	if !ok {
		t.Fatal("migration-gates.yml has no race-debt-functional job")
	}
	if job.Name != "Race debt functional coverage" || job.RunsOn != "ubuntu-24.04" || job.TimeoutMinutes != 20 {
		t.Fatalf(
			"race-debt-functional job identity = (%q, %q, %d), want (%q, %q, %d)",
			job.Name, job.RunsOn, job.TimeoutMinutes,
			"Race debt functional coverage", "ubuntu-24.04", 20,
		)
	}
	wantSteps := []struct {
		name string
		uses string
		run  string
	}{
		{name: "Check out", uses: "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"},
		{name: "Set up the Go environment", uses: "./.github/actions/setup-go-env"},
		{name: "Exercise temporary exclusions without the race detector", run: "make race-debt-functional"},
	}
	if len(job.Steps) != len(wantSteps) {
		t.Fatalf("race-debt-functional step count = %d, want %d", len(job.Steps), len(wantSteps))
	}
	for index, want := range wantSteps {
		got := job.Steps[index]
		if got.Name != want.name || got.Uses != want.uses || strings.TrimSpace(got.Run) != want.run {
			t.Fatalf(
				"race-debt-functional step %d = (%q, %q, %q), want (%q, %q, %q)",
				index, got.Name, got.Uses, strings.TrimSpace(got.Run), want.name, want.uses, want.run,
			)
		}
	}
}

type migrationWorkflowStep struct {
	Name  string `yaml:"name"`
	If    string `yaml:"if"`
	Shell string `yaml:"shell"`
	Run   string `yaml:"run"`
}

func TestMigrationQualityRejectsWorktreeSideEffects(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateMigrationQualityCleanliness(payload); err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name string
		old  string
		new  string
	}{
		{name: "wrong job", old: "  quality:\n", new: "  renamed-quality:\n"},
		{name: "wrong condition", old: "        if: always()\n", new: "        if: success()\n"},
		{name: "wrong shell", old: "        shell: bash\n", new: "        shell: sh\n"},
		{
			name: "incomplete command",
			old:  "          status=\"$(git status --porcelain)\"\n",
			new:  "          status=\"\"\n",
		},
		{
			name: "not final",
			old:  "\n  race-debt:\n",
			new: "\n      - name: Later quality step\n" +
				"        run: true\n\n" +
				"  race-debt:\n",
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			contents := string(payload)
			if !strings.Contains(contents, mutation.old) {
				t.Fatalf("test mutation source %q is missing", mutation.old)
			}
			mutant := strings.Replace(contents, mutation.old, mutation.new, 1)
			if err := validateMigrationQualityCleanliness([]byte(mutant)); err == nil {
				t.Fatal("invalid migration quality cleanliness contract was accepted")
			}
		})
	}
}

func TestMigrationQualityCleanlinessCommandReportsBoundedGitStatus(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	step, err := migrationQualityCleanlinessStep(payload)
	if err != nil {
		t.Fatal(err)
	}
	bash := workflowBash(t)

	tests := []struct {
		name         string
		mutate       func(t *testing.T, root string)
		wantDirty    bool
		wantOutput   string
		maximumLines int
	}{
		{name: "clean"},
		{
			name: "unstaged tracked file",
			mutate: func(t *testing.T, root string) {
				writeWorkflowFixture(t, filepath.Join(root, "tracked.txt"), "changed\n")
			},
			wantDirty:  true,
			wantOutput: " M tracked.txt",
		},
		{
			name: "staged tracked file",
			mutate: func(t *testing.T, root string) {
				writeWorkflowFixture(t, filepath.Join(root, "tracked.txt"), "changed\n")
				runWorkflowGit(t, root, "add", "tracked.txt")
			},
			wantDirty:  true,
			wantOutput: "M  tracked.txt",
		},
		{
			name: "untracked file",
			mutate: func(t *testing.T, root string) {
				writeWorkflowFixture(t, filepath.Join(root, "untracked.txt"), "new\n")
			},
			wantDirty:  true,
			wantOutput: "?? untracked.txt",
		},
		{
			name: "bounded diagnostics",
			mutate: func(t *testing.T, root string) {
				for index := range 105 {
					writeWorkflowFixture(t, filepath.Join(root, fmt.Sprintf("untracked-%03d.txt", index)), "new\n")
				}
			},
			wantDirty:    true,
			wantOutput:   "... 5 additional paths omitted",
			maximumLines: 101,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := initializeWorkflowGitRepository(t)
			if test.mutate != nil {
				test.mutate(t, root)
			}
			before := workflowGitStatus(t, root)
			command := exec.CommandContext(t.Context(), bash, "-eu", "-o", "pipefail", "-c", step.Run)
			command.Dir = root
			output, commandErr := command.CombinedOutput()
			after := workflowGitStatus(t, root)
			if after != before {
				t.Fatalf("cleanliness command changed repository status\nbefore:\n%s\nafter:\n%s", before, after)
			}
			if test.wantDirty && commandErr == nil {
				t.Fatalf("cleanliness command accepted a dirty repository:\n%s", output)
			}
			if !test.wantDirty && commandErr != nil {
				t.Fatalf("cleanliness command rejected a clean repository: %v\n%s", commandErr, output)
			}
			if test.wantOutput != "" && !strings.Contains(string(output), test.wantOutput) {
				t.Fatalf("cleanliness output is missing %q:\n%s", test.wantOutput, output)
			}
			if test.maximumLines > 0 {
				lines := strings.Split(strings.TrimSpace(string(output)), "\n")
				if len(lines) > test.maximumLines {
					t.Fatalf("cleanliness diagnostics have %d lines, want at most %d", len(lines), test.maximumLines)
				}
			}
		})
	}
}

func workflowBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		return "bash"
	}
	command := exec.CommandContext(t.Context(), "git", "--exec-path")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("locate Git executable directory: %v", err)
	}
	bash := filepath.Clean(filepath.Join(strings.TrimSpace(string(output)), "..", "..", "..", "bin", "bash.exe"))
	if _, statErr := os.Stat(bash); statErr != nil {
		t.Fatalf("locate Git Bash at %s: %v", bash, statErr)
	}
	return bash
}

func validateMigrationQualityCleanliness(payload []byte) error {
	got, err := migrationQualityCleanlinessStep(payload)
	if err != nil {
		return err
	}
	want := migrationWorkflowStep{
		Name:  "Verify repository cleanliness after quality checks",
		If:    "always()",
		Shell: "bash",
		Run:   boundedGitStatusCleanlinessScript(),
	}
	if got != want {
		return fmt.Errorf("migration-gates.yml final quality step = %#v, want %#v", got, want)
	}
	return nil
}

func boundedGitStatusCleanlinessScript() string {
	return strings.TrimSpace(`
status="$(git status --porcelain)"
if test -n "$status"; then
  count="$(printf '%s\n' "$status" | wc -l | tr -d '[:space:]')"
  printf '%s\n' "$status" | sed -n '1,100p'
  if test "$count" -gt 100; then
    printf '... %s additional paths omitted\n' "$((count - 100))"
  fi
  exit 1
fi
`)
}

func migrationQualityCleanlinessStep(payload []byte) (migrationWorkflowStep, error) {
	var workflow struct {
		Jobs map[string]struct {
			Steps []migrationWorkflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		return migrationWorkflowStep{}, fmt.Errorf("parse migration workflow: %w", err)
	}
	quality, ok := workflow.Jobs["quality"]
	if !ok {
		return migrationWorkflowStep{}, fmt.Errorf("migration-gates.yml has no quality job")
	}
	if len(quality.Steps) == 0 {
		return migrationWorkflowStep{}, fmt.Errorf("migration-gates.yml quality job has no steps")
	}

	got := quality.Steps[len(quality.Steps)-1]
	got.Run = strings.TrimSpace(got.Run)
	return got, nil
}

func initializeWorkflowGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runWorkflowGit(t, root, "init", "--quiet")
	runWorkflowGit(t, root, "config", "user.email", "ci@example.invalid")
	runWorkflowGit(t, root, "config", "user.name", "CI")
	writeWorkflowFixture(t, filepath.Join(root, "tracked.txt"), "original\n")
	runWorkflowGit(t, root, "add", "tracked.txt")
	runWorkflowGit(t, root, "commit", "--quiet", "-m", "initial")
	return root
}

func runWorkflowGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func workflowGitStatus(t *testing.T, root string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", "-C", root, "status", "--porcelain")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, output)
	}
	return string(output)
}

func writeWorkflowFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationGeneratedConsumersRunsFreshConsumerVerification(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Name           string            `yaml:"name"`
			RunsOn         string            `yaml:"runs-on"`
			TimeoutMinutes int               `yaml:"timeout-minutes"`
			Env            map[string]string `yaml:"env"`
			Steps          []struct {
				Name  string `yaml:"name"`
				If    string `yaml:"if"`
				Shell string `yaml:"shell"`
				Uses  string `yaml:"uses"`
				Run   string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["generated-consumers"]
	if !ok {
		t.Fatal("migration-gates.yml has no generated-consumers job")
	}
	if job.Name != "Generated consumers" || job.RunsOn != "ubuntu-24.04" || job.TimeoutMinutes != 20 {
		t.Fatalf(
			"generated-consumers job identity = (%q, %q, %d), want (%q, %q, %d)",
			job.Name, job.RunsOn, job.TimeoutMinutes,
			"Generated consumers", "ubuntu-24.04", 20,
		)
	}
	if job.Env["GOTOOLCHAIN"] != "local" || job.Env["GOFLAGS"] != "-mod=readonly" {
		t.Fatalf("generated-consumers job Go environment = %#v", job.Env)
	}
	wantSteps := []struct {
		name  string
		if_   string
		shell string
		uses  string
		run   string
	}{
		{name: "Check out", uses: "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"},
		{name: "Set up the Go environment", uses: "./.github/actions/setup-go-env"},
		{name: "Generate and test fresh Go consumers", run: "make generated-consumers"},
		{
			name:  "Verify repository cleanliness after fresh consumer checks",
			if_:   "always()",
			shell: "bash",
			run:   boundedGitStatusCleanlinessScript(),
		},
	}
	if len(job.Steps) != len(wantSteps) {
		t.Fatalf("generated-consumers step count = %d, want %d", len(job.Steps), len(wantSteps))
	}
	for index, want := range wantSteps {
		got := job.Steps[index]
		if got.Name != want.name || got.If != want.if_ || got.Shell != want.shell || got.Uses != want.uses || strings.TrimSpace(got.Run) != want.run {
			t.Fatalf(
				"generated-consumers step %d = (%q, %q, %q, %q, %q), want (%q, %q, %q, %q, %q)",
				index, got.Name, got.If, got.Shell, got.Uses, strings.TrimSpace(got.Run), want.name, want.if_, want.shell, want.uses, want.run,
			)
		}
	}
}

func TestMainTestsJobChecksRepositoryCleanlinessAfterTestExecution(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "master.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []migrationWorkflowStep `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["tests"]
	if !ok {
		t.Fatal("master.yml has no tests job")
	}
	if len(job.Steps) == 0 {
		t.Fatal("master.yml tests job has no steps")
	}
	want := migrationWorkflowStep{
		Name:  "Verify repository cleanliness after test execution",
		If:    "always()",
		Shell: "bash",
		Run:   boundedGitStatusCleanlinessScript(),
	}
	got := job.Steps[len(job.Steps)-1]
	got.Run = strings.TrimSpace(got.Run)
	if got != want {
		t.Fatalf("master.yml final tests step = %#v, want %#v", got, want)
	}
}

func TestGovernanceJobValidatesPullRequestTitleContract(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "master.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	governance, ok := workflow.Jobs["governance-contract"]
	if !ok {
		t.Fatal("master.yml has no governance-contract job")
	}
	for _, step := range governance.Steps {
		if step.Name != "Validate pull request title" {
			continue
		}
		if step.If != "github.event_name == 'pull_request'" ||
			step.Env["PR_TITLE"] != "${{ github.event.pull_request.title }}" ||
			!strings.Contains(step.Run, "PR_TITLE") ||
			!strings.Contains(step.Run, "build|ci|docs|feat|fix|perf|refactor|revert|security|style|test") ||
			!strings.Contains(step.Run, "title_pattern=") ||
			!strings.Contains(step.Run, "(\\([a-z0-9][a-z0-9._/-]*\\))?:[[:space:]]+.+$") ||
			!strings.Contains(step.Run, "=~ $title_pattern") {
			t.Fatalf("pull request title validation step = %#v", step)
		}
		if runtime.GOOS == "windows" {
			return
		}
		if _, err := exec.LookPath("bash"); err != nil {
			t.Skip("Bash is required to exercise the pull request title contract")
		}
		for _, title := range []string{
			"build: pin local formatting commands",
			"build(deps): bump actions",
			"feat(runtime): trace background operations",
			"fix(scheduler): reject stale marker",
			"test: guard operation mirrors",
		} {
			if err := runPullRequestTitleContract(t, step.Run, title); err != nil {
				t.Fatalf("valid title %q rejected: %v", title, err)
			}
		}
		if err := runPullRequestTitleContract(t, step.Run, "missing conventional prefix"); err == nil {
			t.Fatal("invalid title was accepted")
		}
		return
	}
	t.Fatal("governance-contract has no pull request title validation step")
}

func TestReleaseContractJobRunsFirstReleaseChecks(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "master.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseContractJob(payload); err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name string
		old  string
		new  string
	}{
		{name: "missing job", old: "  release-contract:\n", new: "  release-metadata:\n"},
		{
			name: "mutable runner",
			old:  "  release-contract:\n    name: release-contract\n    runs-on: ubuntu-24.04\n",
			new:  "  release-contract:\n    name: release-contract\n    runs-on: ubuntu-latest\n",
		},
		{
			name: "writable modules",
			old:  "  release-contract:\n    name: release-contract\n    runs-on: ubuntu-24.04\n    timeout-minutes: 10\n    env:\n      GOTOOLCHAIN: local\n      GOFLAGS: -mod=readonly\n",
			new:  "  release-contract:\n    name: release-contract\n    runs-on: ubuntu-24.04\n    timeout-minutes: 10\n    env:\n      GOTOOLCHAIN: local\n      GOFLAGS: -mod=mod\n",
		},
		{name: "missing governance check", old: "          make governance-check\n", new: ""},
		{name: "missing metadata check", old: "          make release-contract-check\n", new: ""},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			contents := string(payload)
			if !strings.Contains(contents, mutation.old) {
				t.Fatalf("test mutation source %q is missing", mutation.old)
			}
			mutant := strings.Replace(contents, mutation.old, mutation.new, 1)
			if err := validateReleaseContractJob([]byte(mutant)); err == nil {
				t.Fatal("invalid release-contract job was accepted")
			}
		})
	}
}

func validateReleaseContractJob(payload []byte) error {
	var workflow struct {
		Jobs map[string]struct {
			Name           string            `yaml:"name"`
			RunsOn         string            `yaml:"runs-on"`
			TimeoutMinutes int               `yaml:"timeout-minutes"`
			Env            map[string]string `yaml:"env"`
			Steps          []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		return err
	}
	job, ok := workflow.Jobs["release-contract"]
	if !ok {
		return fmt.Errorf("master.yml has no release-contract job")
	}
	if job.Name != "release-contract" || job.RunsOn != "ubuntu-24.04" || job.TimeoutMinutes != 10 {
		return fmt.Errorf(
			"release-contract identity = (%q, %q, %d), want (%q, %q, %d)",
			job.Name, job.RunsOn, job.TimeoutMinutes, "release-contract", "ubuntu-24.04", 10,
		)
	}
	if job.Env["GOTOOLCHAIN"] != "local" || job.Env["GOFLAGS"] != "-mod=readonly" {
		return fmt.Errorf("release-contract Go environment = %#v", job.Env)
	}
	wantSteps := []struct {
		name string
		uses string
		env  map[string]string
		run  string
	}{
		{name: "Check out", uses: "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"},
		{name: "Set up the environment", uses: "./.github/actions/setup-go-env"},
		{
			name: "Check first-release governance and upstream metadata",
			env:  map[string]string{"GITHUB_TOKEN": "${{ github.token }}"},
			run:  "make governance-check\nmake release-contract-check",
		},
	}
	if len(job.Steps) != len(wantSteps) {
		return fmt.Errorf("release-contract step count = %d, want %d", len(job.Steps), len(wantSteps))
	}
	for index, want := range wantSteps {
		got := job.Steps[index]
		if got.Name != want.name || got.Uses != want.uses || !maps.Equal(got.Env, want.env) || strings.TrimSpace(got.Run) != want.run {
			return fmt.Errorf(
				"release-contract step %d = (%q, %q, %#v, %q), want (%q, %q, %#v, %q)",
				index, got.Name, got.Uses, got.Env, strings.TrimSpace(got.Run), want.name, want.uses, want.env, want.run,
			)
		}
	}
	return nil
}

func runPullRequestTitleContract(t *testing.T, script, title string) error {
	t.Helper()
	command := exec.CommandContext(t.Context(), "bash", "-c", script)
	command.Env = append(os.Environ(), "PR_TITLE="+title)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	return nil
}

func TestFrozenOracleGeneratorsUseTemporaryGoldenOutput(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	oracle, ok := workflow.Jobs["frozen-oracle"]
	if !ok {
		t.Fatal("migration-gates.yml has no frozen-oracle job")
	}
	for _, step := range oracle.Steps {
		if step.Name != "Regenerate and compare frozen fixtures" {
			continue
		}
		for _, required := range []string{
			`fixture_manifest_root="$RUNNER_TEMP/powercontext-oracle-fixture-manifest"`,
			`cp "$fixture_root/$name" "$fixture_manifest_root/$name"`,
			`cp "test/conformance/testdata/python-v0.0.2/$name" "$fixture_manifest_root/$name"`,
			`go run ./tools/fixture-generate -python _oracle -output "$fixture_manifest_root/manifest.json"`,
			`cmp "$fixture_manifest_root/manifest.json" "test/conformance/testdata/python-v0.0.2/manifest.json"`,
			`parity_inventory="$RUNNER_TEMP/powercontext-parity-inventory.json"`,
			`go run ./tools/parity-inventory-generate -upstream _target -output "$parity_inventory"`,
			`cmp "$parity_inventory" "test/conformance/parity-inventory.json"`,
		} {
			if !strings.Contains(step.Run, required) {
				t.Fatalf("frozen fixture regeneration is missing %q:\n%s", required, step.Run)
			}
		}
		return
	}
	t.Fatal("frozen-oracle job has no fixture regeneration step")
}

func TestFrozenOracleFailureDiagnosticsAreBoundedAndSanitized(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	oracle, ok := workflow.Jobs["frozen-oracle"]
	if !ok {
		t.Fatal("migration-gates.yml has no frozen-oracle job")
	}
	var summary, upload *struct {
		Name string
		If   string
		Uses string
		With map[string]string
		Run  string
	}
	for index := range oracle.Steps {
		step := oracle.Steps[index]
		if step.Name == "Write bounded frozen Oracle diagnostics" {
			summary = &struct {
				Name string
				If   string
				Uses string
				With map[string]string
				Run  string
			}{step.Name, step.If, step.Uses, step.With, step.Run}
		}
		if step.Name == "Upload frozen Oracle diagnostics" {
			upload = &struct {
				Name string
				If   string
				Uses string
				With map[string]string
				Run  string
			}{step.Name, step.If, step.Uses, step.With, step.Run}
		}
	}
	if summary == nil || summary.If != "always()" || !strings.Contains(summary.Run, "powercontext-oracle-diagnostics") {
		t.Fatalf("frozen Oracle summary step = %#v", summary)
	}
	for _, forbidden := range []string{"_oracle/.venv", "fixture_root/authority.db", "fixture_root/scheduler.db", "cat "} {
		if strings.Contains(summary.Run, forbidden) {
			t.Fatalf("frozen Oracle summary exposes %q: %s", forbidden, summary.Run)
		}
	}
	if upload == nil || upload.If != "failure()" || upload.Uses != "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" {
		t.Fatalf("frozen Oracle upload step = %#v", upload)
	}
	if upload.With["path"] != "${{ runner.temp }}/powercontext-oracle-diagnostics/summary.txt" ||
		upload.With["if-no-files-found"] != "error" || upload.With["retention-days"] != "14" {
		t.Fatalf("frozen Oracle upload contract = %#v", upload.With)
	}
}

func TestPreWP6HostAdapterWorkflowContract(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Name  string `yaml:"name"`
			Steps []struct {
				ID   string            `yaml:"id"`
				Name string            `yaml:"name"`
				If   string            `yaml:"if"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["host-adapters"]
	if !ok {
		t.Fatal("migration-gates.yml has no host-adapters job")
	}
	if job.Name != "Pre-WP6 host adapters" {
		t.Fatalf("host-adapters name = %q, want Pre-WP6 host adapters", job.Name)
	}
	steps := map[string]struct {
		Name string
		If   string
		Uses string
		With map[string]string
		Env  map[string]string
		Run  string
	}{}
	for _, step := range job.Steps {
		if step.ID != "" {
			steps[step.ID] = struct {
				Name string
				If   string
				Uses string
				With map[string]string
				Env  map[string]string
				Run  string
			}{step.Name, step.If, step.Uses, step.With, step.Env, step.Run}
		}
	}
	codex, ok := steps["codex_adapter"]
	if !ok {
		t.Fatal("host-adapters has no codex_adapter step")
	}
	if !strings.Contains(codex.Run, "integrations/codex/tests") || strings.Contains(codex.Run, "integrations/workbuddy") {
		t.Errorf("Codex adapter command = %q", codex.Run)
	}
	workBuddy, ok := steps["workbuddy_adapter"]
	if !ok {
		t.Fatal("host-adapters has no workbuddy_adapter step")
	}
	if workBuddy.If != "success() || failure()" || !strings.Contains(workBuddy.Run, "integrations/workbuddy/tests") ||
		strings.Contains(workBuddy.Run, "integrations/codex") {
		t.Errorf("WorkBuddy adapter contract = %#v", workBuddy)
	}
	for _, deferred := range []string{"integrations/bub", "integrations/claude-code", "integrations/hermes", "integrations/langgraph"} {
		if strings.Contains(codex.Run, deferred) || strings.Contains(workBuddy.Run, deferred) {
			t.Errorf("pre-WP6 adapter commands must not include %q", deferred)
		}
	}
	for _, deferredID := range []string{"dsh_server", "dsh_adapter", "pi_adapter", "opencode_adapter", "openclaw_adapter"} {
		if _, found := steps[deferredID]; found {
			t.Errorf("pre-WP6 host-adapters must not include deferred step %q", deferredID)
		}
	}
	summary, ok := steps["pre_wp6_adapter_diagnostics"]
	if !ok {
		t.Fatal("host-adapters has no pre_wp6_adapter_diagnostics step")
	}
	if summary.If != "always()" ||
		summary.Env["CODEX_ADAPTER_OUTCOME"] != "${{ steps.codex_adapter.outcome }}" ||
		summary.Env["WORKBUDDY_ADAPTER_OUTCOME"] != "${{ steps.workbuddy_adapter.outcome }}" {
		t.Errorf("pre-WP6 diagnostics contract = %#v", summary)
	}
	for _, required := range []string{
		"powercontext-pre-wp6-host-adapter-diagnostics",
		"integrations/codex/plugins/powercontext/uv.lock",
		"integrations/workbuddy/plugins/powercontext/powercontext.json.example",
		"integrations/workbuddy/plugins/powercontext/hooks/workbuddy_powercontext_hook.py",
	} {
		if !strings.Contains(summary.Run, required) {
			t.Errorf("pre-WP6 diagnostics are missing %q: %s", required, summary.Run)
		}
	}
	upload, ok := steps["pre_wp6_adapter_upload"]
	if !ok {
		t.Fatal("host-adapters has no pre_wp6_adapter_upload step")
	}
	if upload.If != "failure()" || upload.Uses != "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a" ||
		upload.With["path"] != "${{ runner.temp }}/powercontext-pre-wp6-host-adapter-diagnostics/summary.txt" {
		t.Errorf("pre-WP6 upload contract = %#v", upload)
	}
}

func TestRetainedHostAdapterWorkflowIsNotActive(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]any `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	if _, ok := workflow.Jobs["retained-host-adapters"]; ok {
		t.Fatal("migration-gates.yml must not activate retained host adapters")
	}
}

func TestOceanBaseWorkflowIsNotActive(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]any `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	if _, ok := workflow.Jobs["oceanbase-live"]; ok {
		t.Fatal("migration-gates.yml must not activate OceanBase")
	}
}

func TestMigrationAPICompatRunsThePinnedPublicBaseline(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Name           string            `yaml:"name"`
			RunsOn         string            `yaml:"runs-on"`
			TimeoutMinutes int               `yaml:"timeout-minutes"`
			Env            map[string]string `yaml:"env"`
			Steps          []struct {
				Name string            `yaml:"name"`
				Uses string            `yaml:"uses"`
				With map[string]string `yaml:"with"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["api-compat"]
	if !ok {
		t.Fatal("migration-gates.yml has no api-compat job")
	}
	if job.Name != "Public API compatibility" || job.RunsOn != "ubuntu-24.04" || job.TimeoutMinutes != 15 {
		t.Fatalf(
			"api-compat job identity = (%q, %q, %d), want (%q, %q, %d)",
			job.Name, job.RunsOn, job.TimeoutMinutes,
			"Public API compatibility", "ubuntu-24.04", 15,
		)
	}
	if job.Env["GOTOOLCHAIN"] != "local" || job.Env["GOFLAGS"] != "-mod=readonly" {
		t.Fatalf("api-compat job Go environment = %#v", job.Env)
	}
	wantSteps := []struct {
		name string
		uses string
		run  string
	}{
		{name: "Check out", uses: "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"},
		{name: "Install SQLite development headers", run: "sudo apt-get update\nsudo apt-get install --yes --no-install-recommends libsqlite3-dev"},
		{name: "Set up the Go environment", uses: "./.github/actions/setup-go-env"},
		{name: "Compare deliberate public packages with the approved baseline", run: "make api-compat"},
	}
	if len(job.Steps) != len(wantSteps) {
		t.Fatalf("api-compat step count = %d, want %d", len(job.Steps), len(wantSteps))
	}
	for index, want := range wantSteps {
		got := job.Steps[index]
		if got.Name != want.name || got.Uses != want.uses || strings.TrimSpace(got.Run) != want.run {
			t.Fatalf(
				"api-compat step %d = (%q, %q, %q), want (%q, %q, %q)",
				index, got.Name, got.Uses, strings.TrimSpace(got.Run), want.name, want.uses, want.run,
			)
		}
		if index == 0 && got.With["fetch-depth"] != "0" {
			t.Fatalf("api-compat checkout fetch-depth = %q, want 0", got.With["fetch-depth"])
		}
	}
}

func TestLintExclusionsAreLimitedToOwnedGeneratedGoPaths(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".golangci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Linters struct {
			Exclusions struct {
				Generated string   `yaml:"generated"`
				Paths     []string `yaml:"paths"`
			} `yaml:"exclusions"`
		} `yaml:"linters"`
		Formatters struct {
			Exclusions struct {
				Generated string   `yaml:"generated"`
				Paths     []string `yaml:"paths"`
			} `yaml:"exclusions"`
		} `yaml:"formatters"`
	}
	if err := yaml.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	wantPaths := map[string]bool{
		"^api/v1/":                           true,
		"^api/canonical/scopes/":             true,
		"^api/canonical/sources/":            true,
		"^api/canonical/artifacts/":          true,
		"^api/canonical/managedskills/":      true,
		"^api/canonical/stats/":              true,
		"^client/invoker_gen\\.go$":          true,
		"^internal/mcpapi/schemas_gen\\.go$": true,
	}
	for _, exclusions := range []struct {
		name      string
		generated string
		paths     []string
	}{
		{name: "linters", generated: config.Linters.Exclusions.Generated, paths: config.Linters.Exclusions.Paths},
		{name: "formatters", generated: config.Formatters.Exclusions.Generated, paths: config.Formatters.Exclusions.Paths},
	} {
		if exclusions.generated != "disable" {
			t.Errorf("%s generated exclusion = %q, want disable so paths remain explicit", exclusions.name, exclusions.generated)
		}
		if len(exclusions.paths) != len(wantPaths) {
			t.Errorf("%s exclusion paths = %v, want %v", exclusions.name, exclusions.paths, wantPaths)
			continue
		}
		for _, path := range exclusions.paths {
			if !wantPaths[path] {
				t.Errorf("%s has broad or unowned generated exclusion %q", exclusions.name, path)
			}
		}
	}
}

func TestWindowsContractExercisesTargetedGoRegressions(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "windows-contract.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		t.Fatal(err)
	}
	job, ok := workflow.Jobs["windows-contract"]
	if !ok {
		t.Fatal("windows-contract.yml has no windows-contract job")
	}
	setupIndex, apiTestIndex := -1, -1
	for index, step := range job.Steps {
		switch step.Name {
		case "Set up the Go environment":
			if step.Uses == "./.github/actions/setup-go-env" {
				setupIndex = index
			}
		case "Verify API baseline replacement on Windows":
			if strings.TrimSpace(step.Run) == "go test -count=1 ./tools/api-baseline -run '^TestWriteBaselineReplacesExistingOutput$'" {
				apiTestIndex = index
			}
		}
	}
	if setupIndex < 0 || apiTestIndex <= setupIndex {
		t.Fatalf(
			"Windows targeted Go steps = setup %d, API %d, want ordered setup and regression tests",
			setupIndex,
			apiTestIndex,
		)
	}
}

func TestFrozenTextAssetsDeclareLFCheckout(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	paths := []string{
		".gitattributes",
		"go.mod",
		"go.sum",
		"openapi/powercontext.yaml",
		"artifact/memory/prompts/conversation.txt",
		"artifact/memory/prompts/extraction.schema.json",
		"evaluation/tests/contract/fixtures/swebench_pro_public_v2.jsonl",
		"api/v1/oas_client_gen.go",
		"test/conformance/target-delta.json",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), "git", "check-attr", "eol", "--", path)
			command.Dir = repository
			output, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			got := strings.TrimSpace(string(output))
			want := path + ": eol: lf"
			if got != want {
				t.Fatalf("checkout attribute = %q, want %q", got, want)
			}
		})
	}
}

func TestGoCompatibilityJobUsesLocalReadonlyBuild(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "master.yml"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(payload)
	start := strings.Index(contents, "\n  go-compat:\n")
	end := strings.Index(contents, "\n  quality:\n")
	if start < 0 || end <= start {
		t.Fatal("master.yml must define go-compat before quality")
	}
	job := contents[start:end]
	for _, value := range []string{
		"name: go-compat",
		"GOTOOLCHAIN: local",
		"GOFLAGS: -mod=readonly",
		"libsqlite3-dev",
		"run: make build-all",
	} {
		if !strings.Contains(job, value) {
			t.Errorf("go-compat job is missing %q", value)
		}
	}
}

func TestCoverageJobUsesRaceAtomicEvidenceContract(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "master.yml"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(payload)
	start := strings.Index(contents, "\n  coverage:\n")
	end := strings.Index(contents, "\n  quality:\n")
	if start < 0 || end <= start {
		t.Fatal("master.yml must define coverage before quality")
	}
	job := contents[start:end]
	for _, value := range []string{
		"name: coverage",
		"GOTOOLCHAIN: local",
		"GOFLAGS: -mod=readonly",
		"libsqlite3-dev",
		"run: make coverage",
		"if: always()",
		"coverage/coverage.out",
		"coverage/summary.txt",
		"if-no-files-found: warn",
		"retention-days: 14",
	} {
		if !strings.Contains(job, value) {
			t.Errorf("coverage job is missing %q", value)
		}
	}
}

func TestCIThirdPartyExecutablesUseImmutableReferences(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	actionUse := regexp.MustCompile(
		`(?m)^[\t ]*(?:-[\t ]*)?uses:[\t ]+([^@\s]+)@([^\s#]+)([^\r\n]*)$`,
	)
	containerUse := regexp.MustCompile(
		`(?m)^[\t ]*(?:container|image|[A-Z][A-Z0-9_]*_IMAGE):[\t ]+([^\s#]+)`,
	)
	dockerActionUse := regexp.MustCompile(
		`(?m)^[\t ]*(?:-[\t ]*)?uses:[\t ]+docker://([^\s#]+)`,
	)
	commit := regexp.MustCompile("^[0-9a-f]{40}$")
	containerDigest := regexp.MustCompile(`^[^@\s]+@sha256:[0-9a-f]{64}$`)
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

func TestMakefilePinsWorkflowGoToolsInTheRepositoryToolDirectory(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(payload)
	for _, required := range []string{
		"LICENSE_EYE_VERSION := v0.8.0",
		"github.com/apache/skywalking-eyes/cmd/license-eye@$(LICENSE_EYE_VERSION)",
		"ACTIONLINT_VERSION := v1.7.12",
		"github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)",
		"license-eye-tools:",
		"actionlint-tools:",
		"license-check: license-eye-tools",
		"license-fix: license-eye-tools",
		"actionlint: actionlint-tools",
		"MODERN_GO_VERSION := v0.1.1",
		"github.com/JetBrains/go-modern-guidelines@$(MODERN_GO_VERSION)",
		"modern-go-tools:",
		"modern-go: modern-go-tools",
	} {
		if !strings.Contains(contents, required) {
			t.Errorf("Makefile is missing pinned repository-local tool contract %q", required)
		}
	}
}

func TestMigrationWorkflowRunsThePinnedActionlintTarget(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "migration-gates.yml"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(payload)
	if !strings.Contains(contents, "run: make actionlint") {
		t.Fatal("migration-gates.yml does not run the repository-local actionlint target")
	}
	if strings.Contains(contents, "go run github.com/rhysd/actionlint") {
		t.Fatal("migration-gates.yml bypasses the pinned repository-local actionlint target")
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

func TestReleaseProcessSmokeVerifiesPackagedSecurityDefaults(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	tests := map[string][]string{
		"build-artifacts.yml": {
			`go run ./tools/process-smoke -binary bin/powercontext -env-file .env.example -version "$VERSION"`,
			`go run ./tools/process-smoke -binary bin/powercontext-full -env-file .env.example -version "$VERSION"`,
		},
		"release.yml": {
			`go run ./tools/process-smoke -binary bin/powercontext -env-file .env.example -version "$VERSION"`,
			`go run ./tools/process-smoke -binary bin/powercontext-full -env-file .env.example -version "$VERSION"`,
		},
		"release-verify.yml": {
			`-env-file "${{ steps.archives.outputs.standard_root }}/.env.example"`,
			`-env-file "${{ steps.archives.outputs.full_root }}/.env.example"`,
		},
	}
	for name, required := range tests {
		payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		workflow := strings.Join(strings.Fields(string(payload)), " ")
		if count := strings.Count(workflow, "go run ./tools/process-smoke"); count != 2 {
			t.Errorf("%s process-smoke calls = %d, want 2", name, count)
		}
		for _, operation := range required {
			if !strings.Contains(workflow, operation) {
				t.Errorf("%s is missing complete security-default operation %q", name, operation)
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
		"go run ./tools/release verify-evidence -root \"$root\"",
		"go run ./tools/process-smoke",
		"docker buildx imagetools inspect",
		`"$IMAGE" server run`,
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release verification workflow is missing %q", required)
		}
	}
}

func TestReleaseWorkflowAttestsFinalArtifactsAndImages(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseProvenanceWorkflow(payload); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseVerificationConsumesSignedProvenanceBeforeExecution(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "release-verify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseProvenanceVerification(payload); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseProvenanceContractsRejectMutants(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	releasePayload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	verificationPayload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "release-verify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		payload []byte
		mutate  func(string) string
		check   func([]byte) error
	}{
		{
			name: "binary attestation permission", payload: releasePayload, check: validateReleaseProvenanceWorkflow,
			mutate: replaceWorkflowContractText("      id-token: write\n", ""),
		},
		{
			name: "binary attestation subject", payload: releasePayload, check: validateReleaseProvenanceWorkflow,
			mutate: replaceWorkflowContractText("subject-path: dist/*", "subject-path: dist/*.tar.gz"),
		},
		{
			name: "image attestation digest", payload: releasePayload, check: validateReleaseProvenanceWorkflow,
			mutate: replaceWorkflowContractText("subject-digest: ${{ steps.standard.outputs.digest }}", "subject-digest: ${{ steps.full.outputs.digest }}"),
		},
		{
			name: "image attestation mutable subject", payload: releasePayload, check: validateReleaseProvenanceWorkflow,
			mutate: replaceWorkflowContractText("subject-name: ${{ steps.metadata.outputs.standard_subject }}", "subject-name: ${{ steps.metadata.outputs.standard }}"),
		},
		{
			name: "image attestation registry publication", payload: releasePayload, check: validateReleaseProvenanceWorkflow,
			mutate: replaceWorkflowContractText("push-to-registry: true", "push-to-registry: false"),
		},
		{
			name: "image provenance declaration", payload: releasePayload, check: validateReleaseProvenanceWorkflow,
			mutate: replaceWorkflowContractText("github_provenance_attestations: true", "github_provenance_attestations: false"),
		},
		{
			name: "draft attestation order", payload: releasePayload, check: validateReleaseProvenanceWorkflow,
			mutate: func(contents string) string {
				return swapAdjacentWorkflowBlocks(contents, "      - name: Attest release metadata\n", "      - name: Generate or refresh the reviewed release draft\n", "\n  publish:\n")
			},
		},
		{
			name: "verification attestation permission", payload: verificationPayload, check: validateReleaseProvenanceVerification,
			mutate: replaceWorkflowContractText("  attestations: read\n", ""),
		},
		{
			name: "verification repository binding", payload: verificationPayload, check: validateReleaseProvenanceVerification,
			mutate: replaceWorkflowContractText(
				`gh attestation verify "$ASSET_DIR/$subject" \
              --repo "$GITHUB_REPOSITORY"`,
				`gh attestation verify "$ASSET_DIR/$subject" \
              --owner "$GITHUB_REPOSITORY_OWNER"`,
			),
		},
		{
			name: "verification signer binding", payload: verificationPayload, check: validateReleaseProvenanceVerification,
			mutate: replaceWorkflowContractText(`--signer-workflow "$GITHUB_REPOSITORY/.github/workflows/release.yml"`, ""),
		},
		{
			name: "artifact verification order", payload: verificationPayload, check: validateReleaseProvenanceVerification,
			mutate: func(contents string) string {
				return swapAdjacentWorkflowBlocks(contents, "      - name: Verify signed GitHub Release provenance\n", "      - name: Verify extracted Linux release contracts\n", "      - name: Verify Standard and Full integration inventory parity\n")
			},
		},
		{
			name: "OCI verification order", payload: verificationPayload, check: validateReleaseProvenanceVerification,
			mutate: replaceWorkflowContractText(
				`            gh attestation verify "oci://$value" \
              --repo "$GITHUB_REPOSITORY" \
              --signer-workflow "$GITHUB_REPOSITORY/.github/workflows/release.yml"
            docker buildx imagetools inspect "$value" >/dev/null`,
				`            docker buildx imagetools inspect "$value" >/dev/null
            gh attestation verify "oci://$value" \
              --repo "$GITHUB_REPOSITORY" \
              --signer-workflow "$GITHUB_REPOSITORY/.github/workflows/release.yml"`,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutant := test.mutate(string(test.payload))
			if mutant == string(test.payload) {
				t.Fatal("mutant did not change workflow")
			}
			if err := test.check([]byte(mutant)); err == nil {
				t.Fatal("release provenance contract accepted mutant")
			}
		})
	}
}

func replaceWorkflowContractText(old, replacement string) func(string) string {
	return func(contents string) string {
		return strings.Replace(contents, old, replacement, 1)
	}
}

func swapAdjacentWorkflowBlocks(contents, firstMarker, secondMarker, endMarker string) string {
	first := strings.Index(contents, firstMarker)
	second := strings.Index(contents, secondMarker)
	end := strings.Index(contents, endMarker)
	if first < 0 || second <= first || end <= second {
		return contents
	}
	return contents[:first] + contents[second:end] + contents[first:second] + contents[end:]
}

func validateReleaseProvenanceWorkflow(payload []byte) error {
	var workflow releaseIntegrationWorkflow
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		return err
	}
	prepare, ok := workflow.Jobs["prepare"]
	if !ok {
		return errors.New("release.yml has no prepare job")
	}
	_, release := findReleaseIntegrationWorkflowStep(prepare.Steps, "Resolve and validate immutable release metadata")
	if release == nil || !strings.Contains(release.Run, `test "$WORKFLOW_REF_TYPE" = tag`) ||
		!strings.Contains(release.Run, `test "$WORKFLOW_REF_NAME" = "$RELEASE_TAG"`) {
		return errors.New("release.yml does not bind the workflow ref to the immutable release tag")
	}

	binaries, ok := workflow.Jobs["binaries"]
	if !ok {
		return errors.New("release.yml has no binaries job")
	}
	if err := requireWorkflowPermissions("binaries", binaries.Permissions, map[string]string{
		"attestations": "write", "contents": "read", "id-token": "write",
	}); err != nil {
		return err
	}
	checksumIndex, _ := findReleaseIntegrationWorkflowStep(binaries.Steps, "Build the platform checksum manifest")
	attestIndex, attest := findReleaseIntegrationWorkflowStep(binaries.Steps, "Attest platform release assets")
	uploadIndex, _ := findReleaseIntegrationWorkflowStep(binaries.Steps, "Upload platform release assets")
	if checksumIndex < 0 || attestIndex <= checksumIndex || uploadIndex <= attestIndex || attest == nil ||
		attest.Uses != "actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8" ||
		attest.With["subject-path"] != "dist/*" {
		return errors.New("release.yml does not attest final platform assets before upload")
	}

	images, ok := workflow.Jobs["images"]
	if !ok {
		return errors.New("release.yml has no images job")
	}
	if err := requireWorkflowPermissions("images", images.Permissions, map[string]string{
		"attestations": "write", "contents": "read", "id-token": "write", "packages": "write",
	}); err != nil {
		return err
	}
	_, metadata := findReleaseIntegrationWorkflowStep(images.Steps, "Resolve image names")
	standardBuildIndex, _ := findReleaseIntegrationWorkflowStep(images.Steps, "Build and push the standard image")
	standardAttestIndex, standardAttest := findReleaseIntegrationWorkflowStep(images.Steps, "Attest standard image provenance")
	fullBuildIndex, _ := findReleaseIntegrationWorkflowStep(images.Steps, "Build and push the Full image")
	fullAttestIndex, fullAttest := findReleaseIntegrationWorkflowStep(images.Steps, "Attest Full image provenance")
	recordIndex, record := findReleaseIntegrationWorkflowStep(images.Steps, "Record immutable image digests")
	if metadata == nil || !strings.Contains(metadata.Run, "standard_subject=ghcr.io/$image_owner/powercontext") ||
		!strings.Contains(metadata.Run, "full_subject=ghcr.io/$image_owner/powercontext-full") {
		return errors.New("release.yml does not expose tag-free image subjects")
	}
	if standardBuildIndex < 0 || standardAttestIndex <= standardBuildIndex || fullBuildIndex <= standardAttestIndex ||
		fullAttestIndex <= fullBuildIndex || recordIndex <= fullAttestIndex {
		return errors.New("release.yml image build, attestation, and digest-record steps are out of order")
	}
	for name, step := range map[string]*releaseIntegrationWorkflowStep{"standard": standardAttest, "full": fullAttest} {
		if step == nil || step.Uses != "actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8" ||
			step.With["subject-name"] != "${{ steps.metadata.outputs."+name+"_subject }}" ||
			step.With["subject-digest"] != "${{ steps."+name+".outputs.digest }}" || step.With["push-to-registry"] != "true" {
			return fmt.Errorf("release.yml %s image attestation = %#v", name, step)
		}
	}
	if record == nil || !strings.Contains(record.Run, "github_provenance_attestations: true") {
		return errors.New("release.yml image digest record does not require GitHub provenance")
	}

	draft, ok := workflow.Jobs["draft"]
	if !ok {
		return errors.New("release.yml has no draft job")
	}
	if err := requireWorkflowPermissions("draft", draft.Permissions, map[string]string{
		"attestations": "write", "contents": "write", "id-token": "write",
	}); err != nil {
		return err
	}
	draftChecksumIndex, draftChecksum := findReleaseIntegrationWorkflowStep(draft.Steps, "Build the release-level checksum manifest")
	draftAttestIndex, draftAttest := findReleaseIntegrationWorkflowStep(draft.Steps, "Attest release metadata")
	draftReleaseIndex, draftRelease := findReleaseIntegrationWorkflowStep(draft.Steps, "Generate or refresh the reviewed release draft")
	if draftChecksumIndex < 0 || draftAttestIndex <= draftChecksumIndex || draftReleaseIndex <= draftAttestIndex ||
		draftAttest == nil || draftAttest.Uses != "actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8" ||
		strings.TrimSpace(draftAttest.With["subject-path"]) != "dist/SHA256SUMS\ndist/IMAGE-DIGESTS.json" {
		return errors.New("release.yml does not attest final release metadata before publication")
	}
	if draftChecksum == nil || !strings.Contains(draftChecksum.Run, "dist/SHA256SUMS-*") ||
		!strings.Contains(draftChecksum.Run, `test "$(wc -l < dist/SHA256SUMS | tr -d ' ')" = 21`) ||
		draftRelease == nil || !strings.Contains(draftRelease.Run, "dist/SHA256SUMS-*") {
		return errors.New("release.yml does not publish and checksum all platform manifests")
	}
	return nil
}

func validateReleaseProvenanceVerification(payload []byte) error {
	var workflow releaseIntegrationWorkflow
	if err := yaml.Unmarshal(payload, &workflow); err != nil {
		return err
	}
	if err := requireWorkflowPermissions("release verification", workflow.Permissions, map[string]string{
		"attestations": "read", "contents": "read", "packages": "read",
	}); err != nil {
		return err
	}
	verify, ok := workflow.Jobs["verify"]
	if !ok {
		return errors.New("release-verify.yml has no verify job")
	}
	assetsIndex, assets := findReleaseIntegrationWorkflowStep(verify.Steps, "Download and verify the complete GitHub Release")
	attestIndex, attest := findReleaseIntegrationWorkflowStep(verify.Steps, "Verify signed GitHub Release provenance")
	archivesIndex, _ := findReleaseIntegrationWorkflowStep(verify.Steps, "Verify extracted Linux release contracts")
	if assetsIndex < 0 || attestIndex <= assetsIndex || archivesIndex <= attestIndex {
		return errors.New("release-verify.yml does not verify artifact provenance before extraction")
	}
	if assets == nil || !strings.Contains(assets.Run, "SHA256SUMS-$target") ||
		!strings.Contains(assets.Run, `test "$(wc -l < "$ASSET_DIR/SHA256SUMS" | tr -d ' ')" = 21`) {
		return errors.New("release-verify.yml does not require every platform checksum manifest")
	}
	if attest == nil {
		return errors.New("release-verify.yml has no artifact provenance step")
	}
	for _, required := range []string{
		`gh attestation verify "$ASSET_DIR/$subject"`, `--repo "$GITHUB_REPOSITORY"`,
		`--signer-workflow "$GITHUB_REPOSITORY/.github/workflows/release.yml"`,
		"SHA256SUMS-$target", "SHA256SUMS IMAGE-DIGESTS.json", "${product}-${VERSION}-${target}.tar.gz",
		"${product}-${VERSION}-${target}.spdx.json",
	} {
		if !strings.Contains(attest.Run, required) {
			return fmt.Errorf("release-verify.yml artifact provenance is missing %q", required)
		}
	}
	imagesIndex, images := findReleaseIntegrationWorkflowStep(verify.Steps, "Verify immutable GHCR image manifests")
	standardRuntimeIndex, _ := findReleaseIntegrationWorkflowStep(verify.Steps, "Run the published standard image by digest")
	fullRuntimeIndex, _ := findReleaseIntegrationWorkflowStep(verify.Steps, "Run the published Full image by digest")
	if imagesIndex < 0 || standardRuntimeIndex <= imagesIndex || fullRuntimeIndex <= imagesIndex || images == nil {
		return errors.New("release-verify.yml image verification is not before runtime execution")
	}
	provenanceIndex := strings.Index(images.Run, `gh attestation verify "oci://$value"`)
	inspectIndex := strings.Index(images.Run, `docker buildx imagetools inspect "$value"`)
	if !strings.Contains(images.Run, ".github_provenance_attestations == true") || provenanceIndex < 0 ||
		inspectIndex <= provenanceIndex || !strings.Contains(images.Run, `--repo "$GITHUB_REPOSITORY"`) ||
		!strings.Contains(images.Run, `--signer-workflow "$GITHUB_REPOSITORY/.github/workflows/release.yml"`) {
		return errors.New("release-verify.yml does not verify immutable OCI provenance before inspection")
	}
	return nil
}

func requireWorkflowPermissions(name string, got, want map[string]string) error {
	if len(got) != len(want) {
		return fmt.Errorf("%s permissions = %#v, want %#v", name, got, want)
	}
	for permission, access := range want {
		if got[permission] != access {
			return fmt.Errorf("%s permission %q = %q, want %q", name, permission, got[permission], access)
		}
	}
	return nil
}

func TestReleaseWorkflowsExcludeUnsupportedConsumers(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	releasePayload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	verificationPayload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "release-verify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseIntegrationWorkflows(releasePayload, verificationPayload); err != nil {
		t.Fatal(err)
	}

	mutations := []struct {
		name            string
		releaseOld      string
		releaseNew      string
		verificationOld string
		verificationNew string
	}{
		{
			name:       "unsupported DSH build",
			releaseOld: "      - name: Acquire verified native release assets\n",
			releaseNew: "      - name: Build unsupported DSH adapter\n        run: pnpm --dir integrations/dsh/plugins/powercontext build\n\n      - name: Acquire verified native release assets\n",
		},
		{
			name:            "unsupported archive consumer",
			verificationOld: "      - name: Set up Buildx\n",
			verificationNew: "      - name: Consume unsupported Python adapters\n        run: go test -tags archive_consumer ./tools/release\n\n      - name: Set up Buildx\n",
		},
		{
			name:            "missing edition inventory comparison",
			verificationOld: "cmp --silent <(jq -S '.redistributed_integrations' \"${{ steps.archives.outputs.standard_root }}/DEPENDENCIES.json\") <(jq -S '.redistributed_integrations' \"${{ steps.archives.outputs.full_root }}/DEPENDENCIES.json\")",
			verificationNew: "true",
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			release := string(releasePayload)
			verification := string(verificationPayload)
			if mutation.releaseOld != "" {
				release = strings.Replace(release, mutation.releaseOld, mutation.releaseNew, 1)
			}
			if mutation.verificationOld != "" {
				verification = strings.Replace(verification, mutation.verificationOld, mutation.verificationNew, 1)
			}
			if err := validateReleaseIntegrationWorkflows([]byte(release), []byte(verification)); err == nil {
				t.Fatal("unsupported release workflow mutation was accepted")
			}
		})
	}
}

func TestLinuxPersonalServiceConsumerWorkflowContract(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	workflows := filepath.Join(repository, ".github", "workflows")
	master, err := os.ReadFile(filepath.Join(workflows, "master.yml"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := os.ReadFile(filepath.Join(workflows, "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	verification, err := os.ReadFile(filepath.Join(workflows, "release-verify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := os.ReadFile(filepath.Join(repository, "test", "systemd-user-consumer", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateLinuxPersonalServiceConsumerWorkflow(master, release, verification, runner); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxPersonalServiceConsumerWorkflowRejectsMutants(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	workflows := filepath.Join(repository, ".github", "workflows")
	master, err := os.ReadFile(filepath.Join(workflows, "master.yml"))
	if err != nil {
		t.Fatal(err)
	}
	release, err := os.ReadFile(filepath.Join(workflows, "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	verification, err := os.ReadFile(filepath.Join(workflows, "release-verify.yml"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := os.ReadFile(filepath.Join(repository, "test", "systemd-user-consumer", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutant := range []struct {
		name    string
		payload []byte
		old     string
		replace string
		which   string
	}{
		{name: "master consumer tag", payload: master, old: "-tags systemd_user_consumer", replace: "-tags archive_consumer", which: "master"},
		{name: "master archive build", payload: master, old: "make package-standard", replace: "make package-full", which: "master"},
		{name: "release consumer platform", payload: release, old: "matrix.target == 'linux-amd64'", replace: "matrix.target == 'linux-arm64'", which: "release"},
		{name: "verify provenance order", payload: verification, old: "Verify signed GitHub Release provenance", replace: "Verify signed GitHub Release provenance removed", which: "verification"},
		{name: "runner isolation", payload: runner, old: "env -i", replace: "env", which: "runner"},
		{name: "runner manager", payload: runner, old: "systemd --user", replace: "systemd --system", which: "runner"},
	} {
		t.Run(mutant.name, func(t *testing.T) {
			changed := strings.Replace(string(mutant.payload), mutant.old, mutant.replace, 1)
			if changed == string(mutant.payload) {
				t.Fatal("mutant did not change the contract")
			}
			changedMaster, changedRelease, changedVerification, changedRunner := master, release, verification, runner
			switch mutant.which {
			case "master":
				changedMaster = []byte(changed)
			case "release":
				changedRelease = []byte(changed)
			case "verification":
				changedVerification = []byte(changed)
			case "runner":
				changedRunner = []byte(changed)
			default:
				t.Fatal("unknown consumer workflow mutant")
			}
			if err := validateLinuxPersonalServiceConsumerWorkflow(changedMaster, changedRelease, changedVerification, changedRunner); err == nil {
				t.Fatal("consumer workflow accepted a weakened mutation")
			}
		})
	}
}

func validateLinuxPersonalServiceConsumerWorkflow(masterPayload, releasePayload, verificationPayload, runnerPayload []byte) error {
	var master, release, verification releaseIntegrationWorkflow
	for _, workflow := range []struct {
		name        string
		payload     []byte
		destination *releaseIntegrationWorkflow
	}{
		{name: "master.yml", payload: masterPayload, destination: &master},
		{name: "release.yml", payload: releasePayload, destination: &release},
		{name: "release-verify.yml", payload: verificationPayload, destination: &verification},
	} {
		if err := yaml.Unmarshal(workflow.payload, workflow.destination); err != nil {
			return fmt.Errorf("%s: %w", workflow.name, err)
		}
	}
	consumer, ok := master.Jobs["linux-personal-service-consumer"]
	if !ok {
		return errors.New("master.yml has no Linux personal-service consumer job")
	}
	if consumer.RunsOn != "ubuntu-24.04" || consumer.TimeoutMinutes != 45 {
		return fmt.Errorf("master consumer runner = (%q, %d)", consumer.RunsOn, consumer.TimeoutMinutes)
	}
	if err := requireWorkflowPermissions("master consumer", consumer.Permissions, map[string]string{"contents": "read"}); err != nil {
		return err
	}
	if continueOnErrorEnabled(consumer.ContinueOnError) {
		return errors.New("master consumer job tolerates failure")
	}
	masterArchiveIndex, archiveStep := findReleaseIntegrationWorkflowStep(consumer.Steps, "Build Standard Linux archive for personal-service consumer")
	consumerBuildIndex, _ := findReleaseIntegrationWorkflowStep(consumer.Steps, "Build Linux personal-service consumer")
	consumerExerciseIndex, _ := findReleaseIntegrationWorkflowStep(consumer.Steps, "Exercise Standard Linux personal-service archive")
	if archiveStep == nil || !strings.Contains(archiveStep.Run, "make package-standard") ||
		consumerBuildIndex <= masterArchiveIndex || consumerExerciseIndex <= consumerBuildIndex {
		return errors.New("master consumer does not build and consume the Standard archive in order")
	}
	if err := requirePersonalServiceConsumerBuild(consumer.Steps, ""); err != nil {
		return fmt.Errorf("master.yml: %w", err)
	}
	if err := requirePersonalServiceConsumerStep(consumer.Steps, "", "Exercise Standard Linux personal-service archive"); err != nil {
		return fmt.Errorf("master.yml: %w", err)
	}

	binaries, ok := release.Jobs["binaries"]
	if !ok {
		return errors.New("release.yml has no binaries job")
	}
	packagingIndex, packaging := findReleaseIntegrationWorkflowStep(binaries.Steps, "Build and package both editions")
	buildIndex, _ := findReleaseIntegrationWorkflowStep(binaries.Steps, "Build Linux personal-service consumer")
	exerciseIndex, _ := findReleaseIntegrationWorkflowStep(binaries.Steps, "Exercise Standard Linux personal-service archive")
	if packaging == nil || buildIndex <= packagingIndex || exerciseIndex <= buildIndex {
		return errors.New("release.yml does not run the consumer after packaging")
	}
	if err := requirePersonalServiceConsumerBuild(binaries.Steps, "matrix.target == 'linux-amd64'"); err != nil {
		return fmt.Errorf("release.yml: %w", err)
	}
	if err := requirePersonalServiceConsumerStep(binaries.Steps, "matrix.target == 'linux-amd64'", "Exercise Standard Linux personal-service archive"); err != nil {
		return fmt.Errorf("release.yml: %w", err)
	}

	verify, ok := verification.Jobs["verify"]
	if !ok {
		return errors.New("release-verify.yml has no verify job")
	}
	provenanceIndex, _ := findReleaseIntegrationWorkflowStep(verify.Steps, "Verify signed GitHub Release provenance")
	archiveIndex, _ := findReleaseIntegrationWorkflowStep(verify.Steps, "Verify extracted Linux release contracts")
	consumerIndex, consumerStep := findReleaseIntegrationWorkflowStep(verify.Steps, "Exercise published Linux personal-service archive")
	if provenanceIndex < 0 || archiveIndex <= provenanceIndex || consumerIndex <= archiveIndex || consumerStep == nil {
		return errors.New("release-verify.yml does not run the consumer after provenance and archive checks")
	}
	if !strings.Contains(consumerStep.Run, "systemd-user-consumer/run.sh") ||
		!strings.Contains(consumerStep.Run, "--archive") || !strings.Contains(consumerStep.Run, "--test-binary") ||
		!strings.Contains(consumerStep.Run, "-tags systemd_user_consumer") {
		return fmt.Errorf("release-verify consumer step = %#v", consumerStep)
	}

	runner := string(runnerPayload)
	for _, required := range []string{
		"env -i", "dbus-run-session", "systemd --user", "systemctl --user exit", "busctl --user", "timeout 90",
		"POWERCONTEXT_PERSONAL_SERVICE_ARCHIVE", "POWERCONTEXT_SYSTEMD_TEST_BINARY", "mktemp -d", "chmod 0700",
	} {
		if !strings.Contains(runner, required) {
			return fmt.Errorf("systemd user consumer runner is missing %q", required)
		}
	}
	for _, forbidden := range []string{"sudo", "linger", "--system", "--global", "--privileged", "journalctl"} {
		if strings.Contains(runner, forbidden) {
			return fmt.Errorf("systemd user consumer runner contains forbidden %q", forbidden)
		}
	}
	return nil
}

func requirePersonalServiceConsumerStep(steps []releaseIntegrationWorkflowStep, condition, name string) error {
	_, step := findReleaseIntegrationWorkflowStep(steps, name)
	if step == nil || step.If != condition || !strings.Contains(step.Run, "systemd-user-consumer/run.sh") ||
		!strings.Contains(step.Run, "--archive") || !strings.Contains(step.Run, "--test-binary") {
		return fmt.Errorf("missing personal-service consumer step %q", name)
	}
	return nil
}

func requirePersonalServiceConsumerBuild(steps []releaseIntegrationWorkflowStep, condition string) error {
	_, step := findReleaseIntegrationWorkflowStep(steps, "Build Linux personal-service consumer")
	if step == nil || step.If != condition || !strings.Contains(step.Run, "go test -c -tags systemd_user_consumer") ||
		!strings.Contains(step.Run, "powercontext-systemd-user-consumer.test") {
		return errors.New("missing personal-service consumer build step")
	}
	return nil
}

func validateReleaseIntegrationWorkflows(releasePayload, verificationPayload []byte) error {
	var releaseWorkflow, verificationWorkflow releaseIntegrationWorkflow
	if err := yaml.Unmarshal(releasePayload, &releaseWorkflow); err != nil {
		return err
	}
	if err := yaml.Unmarshal(verificationPayload, &verificationWorkflow); err != nil {
		return err
	}
	for name, workflow := range map[string]releaseIntegrationWorkflow{
		"release.yml":        releaseWorkflow,
		"release-verify.yml": verificationWorkflow,
	} {
		for jobName, job := range workflow.Jobs {
			if continueOnErrorEnabled(job.ContinueOnError) {
				return fmt.Errorf("%s job %q tolerates failure", name, jobName)
			}
			for _, step := range job.Steps {
				value := step.Name + "\n" + step.Uses + "\n" + step.Run
				for _, forbidden := range []string{
					"actions/setup-python@", "astral-sh/setup-uv@", "pnpm/action-setup@", "actions/setup-node@",
					"archive_consumer", "integrations/dsh", "integrations/bub", "integrations/claude-code",
					"integrations/hermes", "integrations/langchain", "integrations/langgraph",
					"integrations/openclaw", "integrations/opencode", "integrations/pi", "integrations/pydantic-ai",
				} {
					if strings.Contains(value, forbidden) {
						return fmt.Errorf("%s job %q contains unsupported release consumer %q", name, jobName, forbidden)
					}
				}
			}
		}
	}

	binaries, ok := releaseWorkflow.Jobs["binaries"]
	if !ok {
		return errors.New("release.yml has no binaries job")
	}
	_, packaging := findReleaseIntegrationWorkflowStep(binaries.Steps, "Build and package both editions")
	if packaging == nil || !strings.Contains(packaging.Run, "make package-standard") || !strings.Contains(packaging.Run, "make package-full") {
		return fmt.Errorf("release.yml packaging step = %#v", packaging)
	}

	verification, ok := verificationWorkflow.Jobs["verify"]
	if !ok {
		return errors.New("release-verify.yml has no verify job")
	}
	_, archives := findReleaseIntegrationWorkflowStep(verification.Steps, "Verify extracted Linux release contracts")
	_, parity := findReleaseIntegrationWorkflowStep(verification.Steps, "Verify Standard and Full integration inventory parity")
	if archives == nil || !strings.Contains(archives.Run, `go run ./tools/release verify-evidence -root "$root" -repository "$GITHUB_WORKSPACE" -sbom "$ASSET_DIR/${product}-${VERSION}-linux-amd64.spdx.json"`) {
		return fmt.Errorf("release-verify.yml archive evidence step = %#v", archives)
	}
	wantParity := `cmp --silent <(jq -S '.redistributed_integrations' "${{ steps.archives.outputs.standard_root }}/DEPENDENCIES.json") <(jq -S '.redistributed_integrations' "${{ steps.archives.outputs.full_root }}/DEPENDENCIES.json")`
	if parity == nil || strings.TrimSpace(parity.Run) != wantParity {
		return fmt.Errorf("release-verify.yml integration parity step = %#v", parity)
	}
	return nil
}

type releaseIntegrationWorkflow struct {
	Permissions map[string]string                        `yaml:"permissions"`
	Jobs        map[string]releaseIntegrationWorkflowJob `yaml:"jobs"`
}

type releaseIntegrationWorkflowJob struct {
	RunsOn          string                           `yaml:"runs-on"`
	TimeoutMinutes  int                              `yaml:"timeout-minutes"`
	Permissions     map[string]string                `yaml:"permissions"`
	ContinueOnError any                              `yaml:"continue-on-error"`
	Steps           []releaseIntegrationWorkflowStep `yaml:"steps"`
}

type releaseIntegrationWorkflowStep struct {
	Name            string            `yaml:"name"`
	If              string            `yaml:"if"`
	Uses            string            `yaml:"uses"`
	With            map[string]string `yaml:"with"`
	Env             map[string]string `yaml:"env"`
	Run             string            `yaml:"run"`
	ContinueOnError any               `yaml:"continue-on-error"`
}

func continueOnErrorEnabled(value any) bool {
	if value == nil {
		return false
	}
	enabled, boolean := value.(bool)
	return !boolean || enabled
}

func findReleaseIntegrationWorkflowStep(steps []releaseIntegrationWorkflowStep, name string) (int, *releaseIntegrationWorkflowStep) {
	for index := range steps {
		if steps[index].Name == name {
			return index, &steps[index]
		}
	}
	return -1, nil
}

func TestEvaluationLockUsesOfficialPyPI(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, "evaluation", "uv.lock"))
	if err != nil {
		t.Fatal(err)
	}
	lockfile := string(payload)
	if strings.Contains(lockfile, "pypi.tuna.tsinghua.edu.cn") {
		t.Fatal("evaluation lockfile contains a CI-unavailable PyPI mirror")
	}
	for _, required := range []string{
		`source = { registry = "https://pypi.org/simple" }`,
		`url = "https://files.pythonhosted.org/`,
	} {
		if !strings.Contains(lockfile, required) {
			t.Errorf("evaluation lockfile is missing official PyPI source evidence %q", required)
		}
	}
}

func TestLicenseHeadersHaveOneLocalRepairAndCIContract(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	tests := map[string][]string{
		"Makefile": {
			"github.com/apache/skywalking-eyes/cmd/license-eye@$(LICENSE_EYE_VERSION)",
			"LICENSE_EYE_VERSION := v0.8.0",
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
			"- 'api/v1/**'",
			"- 'api/canonical/scopes/**'",
			"- 'api/canonical/artifacts/**'",
			"- 'api/canonical/managedskills/**'",
			"- 'api/canonical/stats/**'",
			"- 'client/invoker_gen.go'",
			"- 'internal/mcpapi/schemas_gen.go'",
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
