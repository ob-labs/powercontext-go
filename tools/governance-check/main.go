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

var (
	goDirective  = regexp.MustCompile(`(?m)^go[ \t]+1\.27\.0\r?$`)
	issueFieldID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

type issueForm struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Body        []issueControl `yaml:"body"`
}

type issueFieldRequirement struct {
	ID   string
	Type string
}

type issueControl struct {
	Type       string `yaml:"type"`
	ID         string `yaml:"id"`
	Attributes struct {
		Label   string        `yaml:"label"`
		Value   string        `yaml:"value"`
		Options []issueOption `yaml:"options"`
	} `yaml:"attributes"`
	Validations struct {
		Required bool `yaml:"required"`
	} `yaml:"validations"`
}

type issueOption struct {
	Value      string
	Label      string
	Required   bool
	isCheckbox bool
}

func (option *issueOption) UnmarshalYAML(unmarshal func(any) error) error {
	var value string
	if err := unmarshal(&value); err == nil {
		option.Value = value
		return nil
	}
	var checkbox struct {
		Label    string `yaml:"label"`
		Required bool   `yaml:"required"`
	}
	if err := unmarshal(&checkbox); err != nil {
		return err
	}
	option.Label = checkbox.Label
	option.Required = checkbox.Required
	option.isCheckbox = true
	return nil
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
	if err := checkIssueForm(root, ".github/ISSUE_TEMPLATE/bug_report.yml", []issueFieldRequirement{
		{ID: "current_behavior", Type: "textarea"},
		{ID: "expected_behavior", Type: "textarea"},
		{ID: "reproduction", Type: "textarea"},
		{ID: "evidence", Type: "textarea"},
		{ID: "environment", Type: "textarea"},
		{ID: "confirmations", Type: "checkboxes"},
	}); err != nil {
		return err
	}
	if err := checkIssueForm(root, ".github/ISSUE_TEMPLATE/feature_request.yml", []issueFieldRequirement{
		{ID: "problem", Type: "textarea"},
		{ID: "outcome", Type: "textarea"},
		{ID: "scope", Type: "textarea"},
		{ID: "evidence", Type: "textarea"},
		{ID: "confirmations", Type: "checkboxes"},
	}); err != nil {
		return err
	}
	if err := checkIssueForm(root, ".github/ISSUE_TEMPLATE/proposal.yml", []issueFieldRequirement{
		{ID: "problem", Type: "textarea"},
		{ID: "contract_surface", Type: "dropdown"},
		{ID: "outcome", Type: "textarea"},
		{ID: "compatibility", Type: "textarea"},
		{ID: "scope", Type: "textarea"},
		{ID: "non_goals", Type: "textarea"},
		{ID: "alternatives", Type: "textarea"},
		{ID: "evidence", Type: "textarea"},
		{ID: "confirmations", Type: "checkboxes"},
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

func checkIssueForm(root, name string, requiredFields []issueFieldRequirement) error {
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
	seen := make(map[string]string)
	for _, control := range form.Body {
		if control.Type == "markdown" {
			if strings.TrimSpace(control.Attributes.Value) == "" {
				return fmt.Errorf("%s markdown element must define a value", name)
			}
			continue
		}
		if control.Type != "textarea" && control.Type != "input" && control.Type != "dropdown" && control.Type != "checkboxes" {
			return fmt.Errorf("%s contains unsupported field type %q", name, control.Type)
		}
		if control.ID == "" {
			return fmt.Errorf("%s contains a field without an ID", name)
		}
		if !issueFieldID.MatchString(control.ID) {
			return fmt.Errorf("%s contains invalid field ID %q", name, control.ID)
		}
		if strings.TrimSpace(control.Attributes.Label) == "" {
			return fmt.Errorf("%s field %q must define a label", name, control.ID)
		}
		if _, exists := seen[control.ID]; exists {
			return fmt.Errorf("%s contains duplicate field ID %q", name, control.ID)
		}
		seen[control.ID] = control.Type
		if control.Type == "checkboxes" {
			if len(control.Attributes.Options) == 0 {
				return fmt.Errorf("%s checkboxes field %q must define options", name, control.ID)
			}
			for _, option := range control.Attributes.Options {
				if !option.isCheckbox || strings.TrimSpace(option.Label) == "" || !option.Required {
					return fmt.Errorf("%s checkboxes field %q must require every labeled option", name, control.ID)
				}
			}
		} else if control.Type == "dropdown" {
			if len(control.Attributes.Options) == 0 {
				return fmt.Errorf("%s dropdown field %q must define options", name, control.ID)
			}
			seenOptions := make(map[string]struct{}, len(control.Attributes.Options))
			for _, option := range control.Attributes.Options {
				value := strings.TrimSpace(option.Value)
				if option.isCheckbox || value == "" {
					return fmt.Errorf("%s dropdown field %q must define non-empty text options", name, control.ID)
				}
				if _, exists := seenOptions[value]; exists {
					return fmt.Errorf("%s dropdown field %q contains duplicate option %q", name, control.ID, value)
				}
				seenOptions[value] = struct{}{}
			}
		}
		if control.Type != "checkboxes" && !control.Validations.Required {
			return fmt.Errorf("%s field %q must be required", name, control.ID)
		}
	}
	for _, field := range requiredFields {
		controlType, exists := seen[field.ID]
		if !exists {
			return fmt.Errorf("%s is missing required field ID %q", name, field.ID)
		}
		if controlType != field.Type {
			return fmt.Errorf("%s required field ID %q must use type %q", name, field.ID, field.Type)
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
