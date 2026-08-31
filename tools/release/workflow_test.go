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
		"codeql.yml":           true,
		"migration-gates.yml":  true,
		"provider-smoke.yml":   true,
		"windows-contract.yml": true,
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
		"windows-contract.yml": {
			"name: Windows contract checkout", "runs-on: windows-2025", "timeout-minutes: 10",
			"Verify LF attributes and frozen fixture hashes", "Get-FileHash -Algorithm SHA256",
			"git check-attr eol", "git diff --exit-code",
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
				Name string `yaml:"name"`
				Uses string `yaml:"uses"`
				Run  string `yaml:"run"`
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
	setupIndex, apiTestIndex, seekDBTestIndex := -1, -1, -1
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
		case "Verify seekDB build selection on Windows":
			if strings.TrimSpace(step.Run) == "go test -count=1 ./tools/release -run '^TestSeekDBBuildConstraintsSelectImplementationAndNativeSourcesTogether$'" {
				seekDBTestIndex = index
			}
		}
	}
	if setupIndex < 0 || apiTestIndex <= setupIndex || seekDBTestIndex <= apiTestIndex {
		t.Fatalf(
			"Windows targeted Go steps = setup %d, API %d, seekDB %d, want ordered setup and regression tests",
			setupIndex,
			apiTestIndex,
			seekDBTestIndex,
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
