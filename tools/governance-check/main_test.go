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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckRepositoryEnforcesGovernanceContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mutate    func(t *testing.T, root string)
		wantError string
	}{
		{name: "valid contract"},
		{
			name: "missing pull request tracking relationship",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, ".github/pull_request_template.md", validPullRequestTemplate(false))
			},
			wantError: "Part of #",
		},
		{
			name: "duplicate issue form field",
			mutate: func(t *testing.T, root string) {
				contents := validIssueForm("Bug report", []string{
					"current_behavior", "current_behavior", "expected_behavior", "reproduction", "evidence", "environment", "confirmations",
				})
				writeFixtureFile(t, root, ".github/ISSUE_TEMPLATE/bug_report.yml", contents)
			},
			wantError: "duplicate field ID",
		},
		{
			name: "workflow job without timeout",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, ".github/workflows/main.yml", validWorkflow(false))
			},
			wantError: "timeout-minutes",
		},
		{
			name: "workflow job without permissions",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, ".github/workflows/main.yml", validWorkflowWithoutPermissions())
			},
			wantError: "least-privilege permissions",
		},
		{
			name: "valid reusable workflow caller",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, ".github/workflows/main.yml", reusableWorkflowCaller())
			},
		},
		{
			name: "reusable workflow caller with timeout",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, ".github/workflows/main.yml", reusableWorkflowCaller("    timeout-minutes: 10"))
			},
			wantError: "reusable-workflow caller keywords",
		},
		{
			name: "reusable workflow caller with blank reference",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, ".github/workflows/main.yml", reusableWorkflowCallerWithReference(" "))
			},
			wantError: "must name a reusable workflow",
		},
		{
			name: "reusable workflow caller with ordinary job fields",
			mutate: func(t *testing.T, root string) {
				writeFixtureFile(t, root, ".github/workflows/main.yml", reusableWorkflowCaller(
					"    runs-on: ubuntu-24.04",
					"    steps: []",
				))
			},
			wantError: "reusable-workflow caller keywords",
		},
		{
			name: "valid dropdown",
			mutate: func(t *testing.T, root string) {
				appendFixtureFile(t, root, ".github/ISSUE_TEMPLATE/feature_request.yml", `
  - type: dropdown
    id: priority
    attributes:
      label: Priority
      options:
        - Low
        - High
    validations:
      required: true
`)
			},
		},
		{
			name: "missing field label",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/ISSUE_TEMPLATE/bug_report.yml", "      label: current_behavior\n", "")
			},
			wantError: "must define a label",
		},
		{
			name: "missing markdown value",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/ISSUE_TEMPLATE/bug_report.yml", "      value: Provide bounded evidence.\n", "")
			},
			wantError: "markdown element must define a value",
		},
		{
			name: "invalid field ID",
			mutate: func(t *testing.T, root string) {
				appendFixtureFile(t, root, ".github/ISSUE_TEMPLATE/feature_request.yml", `
  - type: textarea
    id: invalid id
    attributes:
      label: Invalid ID
    validations:
      required: true
`)
			},
			wantError: "contains invalid field ID",
		},
		{
			name: "duplicate dropdown option",
			mutate: func(t *testing.T, root string) {
				appendFixtureFile(t, root, ".github/ISSUE_TEMPLATE/feature_request.yml", `
  - type: dropdown
    id: priority
    attributes:
      label: Priority
      options:
        - Low
        - Low
    validations:
      required: true
`)
			},
			wantError: "contains duplicate option",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := writeGovernanceFixture(t)
			if test.mutate != nil {
				test.mutate(t, root)
			}
			err := checkRepository(root)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("checkRepository() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("checkRepository() error = %v, want substring %q", err, test.wantError)
			}
		})
	}
}

func writeGovernanceFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, root, "go.mod", "module example.invalid/project\r\n\r\ngo 1.27.0\r\n")
	writeFixtureFile(t, root, "CONTRIBUTING.md", strings.Join([]string{
		"GOTOOLCHAIN=local", "make lint", "make check-generated", "tracking issue", "AI assistance",
	}, "\n"))
	writeFixtureFile(t, root, ".github/pull_request_template.md", validPullRequestTemplate(true))
	writeFixtureFile(t, root, ".github/ISSUE_TEMPLATE/bug_report.yml", validIssueForm("Bug report", []string{
		"current_behavior", "expected_behavior", "reproduction", "evidence", "environment", "confirmations",
	}))
	writeFixtureFile(t, root, ".github/ISSUE_TEMPLATE/feature_request.yml", validIssueForm("Feature proposal", []string{
		"problem", "outcome", "scope", "non_goals", "evidence", "alternatives", "confirmations",
	}))
	writeFixtureFile(t, root, ".github/ISSUE_TEMPLATE/config.yml", "blank_issues_enabled: false\ncontact_links: []\n")
	writeFixtureFile(t, root, ".github/workflows/main.yml", validWorkflow(true))
	return root
}

func validPullRequestTemplate(includeRelationship bool) string {
	parts := []string{"## Tracking", "Closes #", "## Behavior and compatibility", "## Validation", "git diff --check", "## AI usage"}
	if includeRelationship {
		parts = append(parts, "Part of #")
	}
	return strings.Join(parts, "\n")
}

func validIssueForm(name string, ids []string) string {
	var builder strings.Builder
	builder.WriteString("name: " + name + "\ndescription: Evidence-bearing form\nbody:\n  - type: markdown\n    attributes:\n      value: Provide bounded evidence.\n")
	for _, id := range ids {
		if id == "confirmations" {
			builder.WriteString("  - type: checkboxes\n    id: confirmations\n    attributes:\n      label: Confirmations\n      options:\n        - label: I verified the report.\n          required: true\n")
			continue
		}
		builder.WriteString("  - type: textarea\n    id: " + id + "\n    attributes:\n      label: " + id + "\n    validations:\n      required: true\n")
	}
	return builder.String()
}

func validWorkflow(includeTimeout bool) string {
	lines := []string{
		"name: CI",
		"permissions:",
		"  contents: read",
		"jobs:",
		"  test:",
		"    runs-on: ubuntu-24.04",
	}
	if includeTimeout {
		lines = append(lines, "    timeout-minutes: 10")
	}
	lines = append(lines, "    steps: []")
	return strings.Join(lines, "\n") + "\n"
}

func validWorkflowWithoutPermissions() string {
	return strings.Join([]string{
		"name: CI",
		"jobs:",
		"  test:",
		"    runs-on: ubuntu-24.04",
		"    timeout-minutes: 10",
		"    steps: []",
	}, "\n") + "\n"
}

func reusableWorkflowCaller(extraLines ...string) string {
	return reusableWorkflowCallerWithReference("./.github/workflows/reusable.yml", extraLines...)
}

func reusableWorkflowCallerWithReference(reference string, extraLines ...string) string {
	lines := []string{
		"name: CI",
		"permissions:",
		"  contents: read",
		"jobs:",
		"  delegated:",
		"    uses: \"" + reference + "\"",
	}
	lines = append(lines, extraLines...)
	return strings.Join(lines, "\n") + "\n"
}

func appendFixtureFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(existing, contents...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func replaceFixtureText(t *testing.T, root, name, old, replacement string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(contents), old, replacement, 1)
	if updated == string(contents) {
		t.Fatalf("%s does not contain %q", name, old)
	}
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeFixtureFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
