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
			name: "missing backport conflict declaration",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/pull_request_template.md", "Conflict resolution", "Resolution")
			},
			wantError: "Conflict resolution",
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
			name: "missing dependency automation",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, ".github", "dependabot.yml")); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "read .github/dependabot.yml",
		},
		{
			name: "duplicate dependency ecosystem",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/dependabot.yml", `package-ecosystem: "github-actions"`, `package-ecosystem: "gomod"`)
			},
			wantError: `package ecosystem "gomod" must be configured exactly once`,
		},
		{
			name: "dependency ecosystem outside repository root",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/dependabot.yml", `directory: "/"`, `directory: "/tools"`)
			},
			wantError: `package ecosystem "gomod" must monitor directory "/"`,
		},
		{
			name: "dependency updates more frequent than weekly",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/dependabot.yml", `interval: "weekly"`, `interval: "daily"`)
			},
			wantError: `package ecosystem "gomod" must use a weekly schedule`,
		},
		{
			name: "unbounded dependency pull requests",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/dependabot.yml", "open-pull-requests-limit: 4", "open-pull-requests-limit: 20")
			},
			wantError: `package ecosystem "gomod" must limit open pull requests to 1 through 5`,
		},
		{
			name: "dependency updates without grouping",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/dependabot.yml", `    groups:
      go-minor-patch:
        patterns:
          - "*"
        update-types:
          - "minor"
          - "patch"
`, "")
			},
			wantError: `package ecosystem "gomod" must define group "go-minor-patch"`,
		},
		{
			name: "dependency group includes major updates",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/dependabot.yml", `          - "patch"`, `          - "major"`)
			},
			wantError: `group "go-minor-patch" must contain only minor and patch updates`,
		},
		{
			name: "dependency automation with unknown field",
			mutate: func(t *testing.T, root string) {
				appendFixtureFile(t, root, ".github/dependabot.yml", "unknown: true\n")
			},
			wantError: ".github/dependabot.yml is invalid",
		},
		{
			name: "dependency automation with duplicate field",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/dependabot.yml", `    directory: "/"
    schedule:
`, `    directory: "/"
    directory: "/"
    schedule:
`)
			},
			wantError: ".github/dependabot.yml is invalid",
		},
		{
			name: "missing release-note category",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/release.yml", `    - title: Maintenance
      labels:
        - maintenance
`, "")
			},
			wantError: ".github/release.yml must define 7 release-note categories",
		},
		{
			name: "missing release policy",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "docs", "release", "POLICY.md")); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "read docs/release/POLICY.md",
		},
		{
			name: "missing code of conduct",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "CODE_OF_CONDUCT.md")); err != nil {
					t.Fatal(err)
				}
			},
			wantError: "read CODE_OF_CONDUCT.md",
		},
		{
			name: "code of conduct missing report contact",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, "CODE_OF_CONDUCT.md", "open_contact@oceanbase.com", "example@example.invalid")
			},
			wantError: "open_contact@oceanbase.com",
		},
		{
			name: "release workflow missing semantic tag trigger",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/workflows/release.yml", `      - "v[0-9]*"
      - "powercontext-v[0-9]*"
`, `      - "powercontext-v[0-9]*"
`)
			},
			wantError: `release workflow must run for tag pattern "v[0-9]*"`,
		},
		{
			name: "release workflow missing manual release tag input",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/workflows/release.yml", "      release_tag:\n", "      source_tag:\n")
			},
			wantError: `release workflow must require workflow_dispatch input "release_tag"`,
		},
		{
			name: "release workflow publishes without an explicit manual input",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/workflows/release.yml", "      publish_release:\n", "      publish_now:\n")
			},
			wantError: `release workflow must require workflow_dispatch input "publish_release"`,
		},
		{
			name: "release workflow enables publication by default",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/workflows/release.yml", "        default: false\n", "        default: true\n")
			},
			wantError: `release workflow workflow_dispatch input "publish_release" must default to false`,
		},
		{
			name: "release workflow omits exact previous tag notes",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/workflows/release.yml", `--notes-start-tag "$PREVIOUS_TAG"`, "--notes-start-tag")
			},
			wantError: "$PREVIOUS_TAG",
		},
		{
			name: "release workflow publishes on a tag push",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/workflows/release.yml", "if: github.event_name == 'workflow_dispatch' && inputs.publish_release", "if: github.event_name == 'push'")
			},
			wantError: "if: github.event_name == 'workflow_dispatch' && inputs.publish_release",
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
		{
			name: "missing proposal field label",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/ISSUE_TEMPLATE/proposal.yml", "      label: compatibility\n", "")
			},
			wantError: "must define a label",
		},
		{
			name: "proposal contract surface must be a dropdown",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/ISSUE_TEMPLATE/proposal.yml", `  - type: dropdown
    id: contract_surface
    attributes:
      label: Contract surface
      options:
        - Public Go API
        - Persistence format
    validations:
      required: true
`, `  - type: textarea
    id: contract_surface
    attributes:
      label: Contract surface
    validations:
      required: true
`)
			},
			wantError: `required field ID "contract_surface" must use type "dropdown"`,
		},
		{
			name: "confirmations must be checkboxes",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/ISSUE_TEMPLATE/bug_report.yml", `  - type: checkboxes
    id: confirmations
    attributes:
      label: Confirmations
      options:
        - label: I verified the report.
          required: true
`, `  - type: textarea
    id: confirmations
    attributes:
      label: Confirmations
    validations:
      required: true
`)
			},
			wantError: `required field ID "confirmations" must use type "checkboxes"`,
		},
		{
			name: "proposal contract surface must be required",
			mutate: func(t *testing.T, root string) {
				replaceFixtureText(t, root, ".github/ISSUE_TEMPLATE/proposal.yml", `  - type: dropdown
    id: contract_surface
    attributes:
      label: Contract surface
      options:
        - Public Go API
        - Persistence format
    validations:
      required: true
`, `  - type: dropdown
    id: contract_surface
    attributes:
      label: Contract surface
      options:
        - Public Go API
        - Persistence format
`)
			},
			wantError: `field "contract_surface" must be required`,
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
		"GOTOOLCHAIN=local", "make lint", "make check-generated", "tracking issue", "AI assistance", "Code of Conduct",
	}, "\n"))
	writeFixtureFile(t, root, "CODE_OF_CONDUCT.md", validCodeOfConduct())
	writeFixtureFile(t, root, ".github/pull_request_template.md", validPullRequestTemplate(true))
	writeFixtureFile(t, root, ".github/ISSUE_TEMPLATE/bug_report.yml", validIssueForm("Bug report", []string{
		"current_behavior", "expected_behavior", "reproduction", "evidence", "environment", "confirmations",
	}))
	writeFixtureFile(t, root, ".github/ISSUE_TEMPLATE/feature_request.yml", validIssueForm("Feature request", []string{
		"problem", "outcome", "scope", "evidence", "confirmations",
	}))
	writeFixtureFile(t, root, ".github/ISSUE_TEMPLATE/proposal.yml", validIssueForm("Contract proposal", []string{
		"problem", "contract_surface", "outcome", "compatibility", "scope", "non_goals", "alternatives", "evidence", "confirmations",
	}))
	writeFixtureFile(t, root, ".github/ISSUE_TEMPLATE/config.yml", "blank_issues_enabled: false\ncontact_links: []\n")
	writeFixtureFile(t, root, ".github/dependabot.yml", validDependabotConfig())
	writeFixtureFile(t, root, ".github/release.yml", validReleaseNotesConfig())
	writeFixtureFile(t, root, ".github/workflows/main.yml", validWorkflow(true))
	writeFixtureFile(t, root, ".github/workflows/release.yml", validReleaseWorkflow())
	writeFixtureFile(t, root, "docs/release/POLICY.md", validReleasePolicy())
	return root
}

func validDependabotConfig() string {
	return `version: 2
updates:
  - package-ecosystem: "gomod"
    directory: "/"
    schedule:
      interval: "weekly"
      day: "monday"
    open-pull-requests-limit: 4
    groups:
      go-minor-patch:
        patterns:
          - "*"
        update-types:
          - "minor"
          - "patch"
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule:
      interval: "weekly"
      day: "tuesday"
    open-pull-requests-limit: 4
    groups:
      actions-minor-patch:
        patterns:
          - "*"
        update-types:
          - "minor"
          - "patch"
`
}

func validPullRequestTemplate(includeRelationship bool) string {
	parts := []string{
		"## Tracking", "Closes #", "## Behavior and compatibility", "## Backport declaration (release/v* only)",
		"Original Issue and change", "Target release line", "Conflict resolution", "Compatibility impact", "Validation on target release line",
		"## Validation", "git diff --check", "Formatter evidence", "Generated-consumer evidence", "Compatibility evidence", "## AI usage",
	}
	if includeRelationship {
		parts = append(parts, "Part of #")
	}
	return strings.Join(parts, "\n")
}

func validIssueForm(name string, ids []string) string {
	var builder strings.Builder
	builder.WriteString("name: " + name + "\ndescription: Evidence-bearing form\nbody:\n  - type: markdown\n    attributes:\n      value: Provide bounded evidence.\n")
	for _, id := range ids {
		if id == "contract_surface" {
			builder.WriteString("  - type: dropdown\n    id: contract_surface\n    attributes:\n      label: Contract surface\n      options:\n        - Public Go API\n        - Persistence format\n    validations:\n      required: true\n")
			continue
		}
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

func validReleaseWorkflow() string {
	return `name: Release
on:
  push:
    tags:
      - "v[0-9]*"
      - "powercontext-v[0-9]*"
  workflow_dispatch:
    inputs:
      release_tag:
        description: Existing release tag
        required: true
        type: string
      publish_release:
        description: Publish the previously reviewed draft
        required: true
        default: false
        type: boolean
permissions:
  contents: read
jobs:
  prepare:
    runs-on: ubuntu-24.04
    timeout-minutes: 10
    steps:
      - run: |
          previous_tag: ${{ steps.release.outputs.previous_tag }}
          git tag --merged "${commit}^"
  draft:
    runs-on: ubuntu-24.04
    timeout-minutes: 10
    permissions:
      contents: write
    steps:
      - run: |
          args=(--draft --generate-notes --verify-tag)
          args+=(--notes-start-tag "$PREVIOUS_TAG")
          gh release create "$RELEASE_TAG"
  publish:
    if: github.event_name == 'workflow_dispatch' && inputs.publish_release
    runs-on: ubuntu-24.04
    timeout-minutes: 10
    permissions:
      contents: write
    steps:
      - run: |
          gh release view "$RELEASE_TAG" --json isDraft --jq .isDraft
          gh release edit "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" --draft=false
  release-verify:
    if: needs.publish.result == 'success'
    uses: ./.github/workflows/release-verify.yml
`
}

func validReleaseNotesConfig() string {
	return `changelog:
  categories:
    - title: Breaking changes
      labels:
        - breaking
    - title: Features
      labels:
        - enhancement
    - title: Bug fixes
      labels:
        - bug
    - title: Security
      labels:
        - security
    - title: Dependencies
      labels:
        - dependencies
    - title: Documentation
      labels:
        - documentation
    - title: Maintenance
      labels:
        - maintenance
  exclude:
    labels:
      - duplicate
      - invalid
      - question
      - wontfix
`
}

func validReleasePolicy() string {
	return strings.Join([]string{
		"default branch receives features and fixes",
		"supported release branches receive approved fixes and security backports only",
		"release branch naming convention",
		"backport PR",
		"original Issue",
		"target release line",
		"compatibility impact",
		"validation performed on that line",
		"force-push and deletion",
		"squash merge",
		"breaking-change label",
		"version decision",
		"release draft",
		"previous-tag comparison",
		"contributor attribution",
		"root-module",
		"CLI",
		"generator",
		"adapter",
		"binary versions",
		"DCO sign-off is not required",
	}, "\n")
}

func validCodeOfConduct() string {
	return strings.Join([]string{
		"Contributor Covenant Code of Conduct",
		"open_contact@oceanbase.com",
		"Contributor Covenant, version 2.1",
	}, "\n")
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
