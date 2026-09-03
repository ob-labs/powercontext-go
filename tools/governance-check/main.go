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
	"slices"
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

type dependabotConfig struct {
	Version int                `yaml:"version"`
	Updates []dependabotUpdate `yaml:"updates"`
}

type dependabotUpdate struct {
	PackageEcosystem      string                     `yaml:"package-ecosystem"`
	Directory             string                     `yaml:"directory"`
	Schedule              dependabotSchedule         `yaml:"schedule"`
	OpenPullRequestsLimit int                        `yaml:"open-pull-requests-limit"`
	Groups                map[string]dependabotGroup `yaml:"groups"`
}

type dependabotSchedule struct {
	Interval string `yaml:"interval"`
	Day      string `yaml:"day"`
}

type dependabotGroup struct {
	Patterns    []string `yaml:"patterns"`
	UpdateTypes []string `yaml:"update-types"`
}

type dependabotExpectation struct {
	Ecosystem string
	Day       string
	Group     string
}

type releaseNotesConfig struct {
	Changelog struct {
		Categories []releaseNoteCategory `yaml:"categories"`
		Exclude    struct {
			Labels []string `yaml:"labels"`
		} `yaml:"exclude"`
	} `yaml:"changelog"`
}

type releaseNoteCategory struct {
	Title  string   `yaml:"title"`
	Labels []string `yaml:"labels"`
}

type workflowContract struct {
	Permissions map[string]any         `yaml:"permissions"`
	Jobs        map[string]workflowJob `yaml:"jobs"`
}

type workflowJob struct {
	Uses               *string        `yaml:"uses"`
	TimeoutMinutes     *int           `yaml:"timeout-minutes"`
	Permissions        map[string]any `yaml:"permissions"`
	AdditionalKeywords map[string]any `yaml:",inline"`
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
		"GOTOOLCHAIN=local", "make lint", "make check-generated", "tracking issue", "AI assistance", "Code of Conduct",
	}); err != nil {
		return err
	}
	if err := checkCodeOfConduct(root); err != nil {
		return err
	}
	if err := requirePhrases(root, ".github/pull_request_template.md", []string{
		"## Tracking", "Part of #", "Closes #", "## Behavior and compatibility", "## Backport declaration (release/v* only)", "Original Issue and change", "Target release line", "Conflict resolution", "Compatibility impact", "Validation on target release line", "## Validation", "git diff --check", "Formatter evidence", "Generated-consumer evidence", "Compatibility evidence", "## AI usage",
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
	if err := checkIssueConfig(root); err != nil {
		return err
	}
	if err := checkDependabotConfig(root); err != nil {
		return err
	}
	if err := checkReleasePolicy(root); err != nil {
		return err
	}
	if err := checkReleaseNotesConfig(root); err != nil {
		return err
	}
	if err := checkReleaseWorkflow(root); err != nil {
		return err
	}
	return checkWorkflowContracts(root)
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

func checkDependabotConfig(root string) error {
	const name = ".github/dependabot.yml"
	contents, err := readRepositoryFile(root, name)
	if err != nil {
		return err
	}
	var config dependabotConfig
	if err := yaml.UnmarshalStrict(contents, &config); err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	if config.Version != 2 {
		return fmt.Errorf("%s must use version 2", name)
	}
	expectations := []dependabotExpectation{
		{Ecosystem: "gomod", Day: "monday", Group: "go-minor-patch"},
		{Ecosystem: "github-actions", Day: "tuesday", Group: "actions-minor-patch"},
	}
	if len(config.Updates) != len(expectations) {
		return fmt.Errorf("%s must configure exactly gomod and github-actions", name)
	}
	updates := make(map[string][]dependabotUpdate, len(config.Updates))
	for _, update := range config.Updates {
		updates[update.PackageEcosystem] = append(updates[update.PackageEcosystem], update)
	}
	for _, expectation := range expectations {
		matches := updates[expectation.Ecosystem]
		if len(matches) != 1 {
			return fmt.Errorf("package ecosystem %q must be configured exactly once", expectation.Ecosystem)
		}
		if err := checkDependabotUpdate(matches[0], expectation); err != nil {
			return err
		}
	}
	return nil
}

func checkDependabotUpdate(update dependabotUpdate, expectation dependabotExpectation) error {
	if update.Directory != "/" {
		return fmt.Errorf("package ecosystem %q must monitor directory %q", expectation.Ecosystem, "/")
	}
	if update.Schedule.Interval != "weekly" {
		return fmt.Errorf("package ecosystem %q must use a weekly schedule", expectation.Ecosystem)
	}
	if update.Schedule.Day != expectation.Day {
		return fmt.Errorf("package ecosystem %q must run on %s", expectation.Ecosystem, expectation.Day)
	}
	if update.OpenPullRequestsLimit < 1 || update.OpenPullRequestsLimit > 5 {
		return fmt.Errorf("package ecosystem %q must limit open pull requests to 1 through 5", expectation.Ecosystem)
	}
	group, ok := update.Groups[expectation.Group]
	if !ok || len(update.Groups) != 1 {
		return fmt.Errorf("package ecosystem %q must define group %q", expectation.Ecosystem, expectation.Group)
	}
	if len(group.Patterns) != 1 || group.Patterns[0] != "*" {
		return fmt.Errorf("group %q must match every dependency", expectation.Group)
	}
	if len(group.UpdateTypes) != 2 || !slices.Contains(group.UpdateTypes, "minor") || !slices.Contains(group.UpdateTypes, "patch") {
		return fmt.Errorf("group %q must contain only minor and patch updates", expectation.Group)
	}
	return nil
}

func checkReleasePolicy(root string) error {
	return requirePhrases(root, "docs/release/POLICY.md", []string{
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
	})
}

func checkCodeOfConduct(root string) error {
	return requirePhrases(root, "CODE_OF_CONDUCT.md", []string{
		"Contributor Covenant Code of Conduct",
		"open_contact@oceanbase.com",
		"Contributor Covenant, version 2.1",
	})
}

func checkReleaseNotesConfig(root string) error {
	const name = ".github/release.yml"
	contents, err := readRepositoryFile(root, name)
	if err != nil {
		return err
	}
	var document any
	if err := yaml.UnmarshalStrict(contents, &document); err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	var config releaseNotesConfig
	if err := yaml.Unmarshal(contents, &config); err != nil {
		return fmt.Errorf("%s cannot be inspected: %w", name, err)
	}
	expected := []releaseNoteCategory{
		{Title: "Breaking changes", Labels: []string{"breaking"}},
		{Title: "Features", Labels: []string{"enhancement"}},
		{Title: "Bug fixes", Labels: []string{"bug"}},
		{Title: "Security", Labels: []string{"security"}},
		{Title: "Dependencies", Labels: []string{"dependencies"}},
		{Title: "Documentation", Labels: []string{"documentation"}},
		{Title: "Maintenance", Labels: []string{"maintenance"}},
	}
	if len(config.Changelog.Categories) != len(expected) {
		return fmt.Errorf("%s must define %d release-note categories", name, len(expected))
	}
	seen := make(map[string]struct{}, len(config.Changelog.Categories))
	for _, category := range config.Changelog.Categories {
		title := strings.TrimSpace(category.Title)
		if title == "" {
			return fmt.Errorf("%s contains a release-note category without a title", name)
		}
		if _, exists := seen[title]; exists {
			return fmt.Errorf("%s contains duplicate release-note category %q", name, title)
		}
		seen[title] = struct{}{}
	}
	for _, category := range expected {
		if !releaseNotesCategoryContains(config.Changelog.Categories, category.Title, category.Labels[0]) {
			return fmt.Errorf("%s must define the %q category with label %q", name, category.Title, category.Labels[0])
		}
	}
	for _, label := range []string{"duplicate", "invalid", "question", "wontfix"} {
		if !slices.Contains(config.Changelog.Exclude.Labels, label) {
			return fmt.Errorf("%s must exclude label %q from release notes", name, label)
		}
	}
	return nil
}

func releaseNotesCategoryContains(categories []releaseNoteCategory, title, label string) bool {
	for _, category := range categories {
		if category.Title == title && slices.Contains(category.Labels, label) {
			return true
		}
	}
	return false
}

func checkReleaseWorkflow(root string) error {
	const name = ".github/workflows/release.yml"
	contents, err := readRepositoryFile(root, name)
	if err != nil {
		return err
	}
	var document map[any]any
	if err := yaml.UnmarshalStrict(contents, &document); err != nil {
		return fmt.Errorf("%s is invalid: %w", name, err)
	}
	on, ok := document["on"]
	if !ok {
		on = document[true]
	}
	onMap, ok := on.(map[any]any)
	if !ok {
		return fmt.Errorf("%s must define structured workflow triggers", name)
	}
	push, ok := onMap["push"].(map[any]any)
	if !ok {
		return fmt.Errorf("%s must run on tag pushes", name)
	}
	tags, ok := stringSlice(push["tags"])
	if !ok {
		return fmt.Errorf("%s push trigger must define tags", name)
	}
	for _, pattern := range []string{"v[0-9]*", "powercontext-v[0-9]*"} {
		if !slices.Contains(tags, pattern) {
			return fmt.Errorf("release workflow must run for tag pattern %q", pattern)
		}
	}
	dispatch, ok := onMap["workflow_dispatch"].(map[any]any)
	if !ok {
		return fmt.Errorf("%s must support workflow_dispatch", name)
	}
	inputs, ok := dispatch["inputs"].(map[any]any)
	if !ok {
		return errors.New(`release workflow must require workflow_dispatch input "release_tag"`)
	}
	releaseTag, ok := inputs["release_tag"].(map[any]any)
	if !ok {
		return errors.New(`release workflow must require workflow_dispatch input "release_tag"`)
	}
	required, ok := releaseTag["required"].(bool)
	if !ok || !required {
		return errors.New(`release workflow must require workflow_dispatch input "release_tag"`)
	}
	inputType, ok := releaseTag["type"].(string)
	if !ok || inputType != "string" {
		return errors.New(`release workflow workflow_dispatch input "release_tag" must be a string`)
	}
	publishRelease, ok := inputs["publish_release"].(map[any]any)
	if !ok {
		return errors.New(`release workflow must require workflow_dispatch input "publish_release"`)
	}
	required, ok = publishRelease["required"].(bool)
	if !ok || !required {
		return errors.New(`release workflow workflow_dispatch input "publish_release" must be required`)
	}
	inputType, ok = publishRelease["type"].(string)
	if !ok || inputType != "boolean" {
		return errors.New(`release workflow workflow_dispatch input "publish_release" must be a boolean`)
	}
	defaultValue, ok := publishRelease["default"].(bool)
	if !ok || defaultValue {
		return errors.New(`release workflow workflow_dispatch input "publish_release" must default to false`)
	}
	return requirePhrases(root, name, []string{
		"previous_tag: ${{ steps.release.outputs.previous_tag }}",
		`git tag --merged "${commit}^"`,
		"--draft --generate-notes --verify-tag",
		`--notes-start-tag "$PREVIOUS_TAG"`,
		`gh release create "$RELEASE_TAG"`,
		"if: github.event_name == 'workflow_dispatch' && inputs.publish_release",
		`--json isDraft --jq .isDraft`,
		`gh release edit "$RELEASE_TAG" --repo "$GITHUB_REPOSITORY" --draft=false`,
		"if: needs.publish.result == 'success'",
	})
}

func stringSlice(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func checkWorkflowContracts(root string) error {
	patterns := []string{
		filepath.Join(root, ".github", "workflows", "*.yml"),
		filepath.Join(root, ".github", "workflows", "*.yaml"),
	}
	var paths []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("enumerate workflows: %w", err)
		}
		paths = append(paths, matches...)
	}
	if len(paths) == 0 {
		return errors.New(".github/workflows must contain at least one workflow")
	}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read workflow %s: %w", filepath.Base(path), err)
		}
		var document any
		if err := yaml.UnmarshalStrict(contents, &document); err != nil {
			return fmt.Errorf("workflow %s is invalid YAML: %w", filepath.Base(path), err)
		}
		var workflow workflowContract
		if err := yaml.Unmarshal(contents, &workflow); err != nil {
			return fmt.Errorf("workflow %s cannot be inspected: %w", filepath.Base(path), err)
		}
		if len(workflow.Jobs) == 0 {
			return fmt.Errorf("workflow %s must define jobs", filepath.Base(path))
		}
		for name, job := range workflow.Jobs {
			if workflow.Permissions == nil && job.Permissions == nil {
				return fmt.Errorf("workflow %s job %s must declare least-privilege permissions", filepath.Base(path), name)
			}
			if job.Uses != nil {
				if err := job.checkReusableWorkflowCaller(); err != nil {
					return fmt.Errorf("workflow %s job %s %w", filepath.Base(path), name, err)
				}
				continue
			}
			if job.TimeoutMinutes == nil || *job.TimeoutMinutes <= 0 {
				return fmt.Errorf("workflow %s job %s must declare a positive timeout-minutes", filepath.Base(path), name)
			}
		}
	}
	return nil
}

func (job workflowJob) checkReusableWorkflowCaller() error {
	if strings.TrimSpace(*job.Uses) == "" {
		return errors.New("must name a reusable workflow")
	}
	if job.TimeoutMinutes != nil {
		return errors.New("may only use reusable-workflow caller keywords; found \"timeout-minutes\"")
	}
	for keyword := range job.AdditionalKeywords {
		switch keyword {
		case "name", "with", "secrets", "strategy", "needs", "if", "concurrency":
		default:
			return fmt.Errorf("may only use reusable-workflow caller keywords; found %q", keyword)
		}
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
