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
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v2"
)

var goDirective = regexp.MustCompile(`(?m)^go[ \t]+1\.27\.0\r?$`)

type issueForm struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Body        []issueControl `yaml:"body"`
}

type issueControl struct {
	Type       string `yaml:"type"`
	ID         string `yaml:"id"`
	Attributes struct {
		Options []struct {
			Label    string `yaml:"label"`
			Required bool   `yaml:"required"`
		} `yaml:"options"`
	} `yaml:"attributes"`
	Validations struct {
		Required bool `yaml:"required"`
	} `yaml:"validations"`
}

type issueConfig struct {
	BlankIssuesEnabled *bool `yaml:"blank_issues_enabled"`
	ContactLinks       []any `yaml:"contact_links"`
}

func main() {
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "governance-check: positional arguments are not supported")
		os.Exit(2)
	}
	if err := checkRepository(*root); err != nil {
		fmt.Fprintln(os.Stderr, "governance-check:", err)
		os.Exit(1)
	}
}

func checkRepository(root string) error {
	module, err := readRepositoryFile(root, "go.mod")
	if err != nil {
		return err
	}
	if !goDirective.Match(module) {
		return errors.New("go.mod must declare the supported Go 1.27.0 toolchain")
	}
	if err := requirePhrases(root, "CONTRIBUTING.md", []string{
		"GOTOOLCHAIN=local", "make lint", "make check-generated", "tracking issue", "AI assistance",
	}); err != nil {
		return err
	}
	if err := requirePhrases(root, ".github/pull_request_template.md", []string{
		"## Tracking", "Part of #", "Closes #", "## Behavior and compatibility", "## Validation", "git diff --check", "## AI usage",
	}); err != nil {
		return err
	}
	if err := checkIssueForm(root, ".github/ISSUE_TEMPLATE/bug_report.yml", []string{
		"current_behavior", "expected_behavior", "reproduction", "evidence", "environment", "confirmations",
	}); err != nil {
		return err
	}
	if err := checkIssueForm(root, ".github/ISSUE_TEMPLATE/feature_request.yml", []string{
		"problem", "outcome", "scope", "non_goals", "evidence", "alternatives", "confirmations",
	}); err != nil {
		return err
	}
	return checkIssueConfig(root)
}

func requirePhrases(root, name string, phrases []string) error {
	contents, err := readRepositoryFile(root, name)
	if err != nil {
		return err
	}
	for _, phrase := range phrases {
		if !strings.Contains(string(contents), phrase) {
			return fmt.Errorf("%s must contain %q", name, phrase)
		}
	}
	return nil
}

func checkIssueForm(root, name string, requiredIDs []string) error {
	contents, err := readRepositoryFile(root, name)
	if err != nil {
		return err
	}
	var document any
	if err := yaml.UnmarshalStrict(contents, &document); err != nil {
		return fmt.Errorf("%s is not valid Issue form YAML: %w", name, err)
	}
	var form issueForm
	if err := yaml.Unmarshal(contents, &form); err != nil {
		return fmt.Errorf("%s is not valid Issue form YAML: %w", name, err)
	}
	if strings.TrimSpace(form.Name) == "" || strings.TrimSpace(form.Description) == "" || len(form.Body) == 0 {
		return fmt.Errorf("%s must define a name, description, and body", name)
	}
	seen := make(map[string]struct{})
	for _, control := range form.Body {
		if control.Type == "markdown" {
			continue
		}
		if control.Type != "textarea" && control.Type != "input" && control.Type != "dropdown" && control.Type != "checkboxes" {
			return fmt.Errorf("%s contains unsupported field type %q", name, control.Type)
		}
		if control.ID == "" {
			return fmt.Errorf("%s contains a field without an ID", name)
		}
		if _, exists := seen[control.ID]; exists {
			return fmt.Errorf("%s contains duplicate field ID %q", name, control.ID)
		}
		seen[control.ID] = struct{}{}
		if control.Type == "checkboxes" {
			if len(control.Attributes.Options) == 0 {
				return fmt.Errorf("%s checkboxes field %q must define options", name, control.ID)
			}
			for _, option := range control.Attributes.Options {
				if strings.TrimSpace(option.Label) == "" || !option.Required {
					return fmt.Errorf("%s checkboxes field %q must require every labeled option", name, control.ID)
				}
			}
		} else if !control.Validations.Required {
			return fmt.Errorf("%s field %q must be required", name, control.ID)
		}
	}
	for _, id := range requiredIDs {
		if _, exists := seen[id]; !exists {
			return fmt.Errorf("%s is missing required field ID %q", name, id)
		}
	}
	return nil
}

func checkIssueConfig(root string) error {
	const name = ".github/ISSUE_TEMPLATE/config.yml"
	contents, err := readRepositoryFile(root, name)
	if err != nil {
		return err
	}
	var config issueConfig
	if err := yaml.UnmarshalStrict(contents, &config); err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	if config.BlankIssuesEnabled == nil || *config.BlankIssuesEnabled {
		return fmt.Errorf("%s must disable blank issues", name)
	}
	if config.ContactLinks == nil {
		return fmt.Errorf("%s must declare contact_links, even when empty", name)
	}
	return nil
}

func readRepositoryFile(root, name string) ([]byte, error) {
	path := filepath.Join(root, filepath.FromSlash(name))
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return contents, nil
}
