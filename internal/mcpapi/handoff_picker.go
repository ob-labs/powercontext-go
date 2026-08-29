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

package mcpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/text/cases"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
)

const (
	handoffPickerToolName = "select_handoff_workstream"
	maxPickerChoices      = 100
)

type handoffPickerInput struct {
	ProjectID       *string `json:"project_id"`
	WorkID          *string `json:"work_id"`
	Query           *string `json:"query"`
	IncludeArchived bool    `json:"include_archived"`
	Locale          string  `json:"locale"`
}

type handoffProjectChoice struct {
	ProjectID  string `json:"project_id"`
	ProjectKey string `json:"project_key"`
	Title      string `json:"title"`
}

type handoffWorkstreamChoice struct {
	WorkID         string            `json:"work_id"`
	ScopeID        string            `json:"scope_id"`
	ProjectID      string            `json:"project_id"`
	ProjectKey     string            `json:"project_key"`
	Title          string            `json:"title"`
	Kind           v1.WorkstreamKind `json:"kind"`
	CatalogVersion int               `json:"catalog_version"`
}

type handoffWorkstreamSelection struct {
	Status            string                    `json:"status"`
	Message           string                    `json:"message"`
	Stage             *string                   `json:"stage"`
	Selected          *handoffWorkstreamChoice  `json:"selected"`
	ProjectChoices    []handoffProjectChoice    `json:"project_choices"`
	WorkstreamChoices []handoffWorkstreamChoice `json:"workstream_choices"`
	Truncated         bool                      `json:"truncated"`
}

var handoffPickerInputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"properties": map[string]any{
		"project_id": map[string]any{
			"type": []string{"string", "null"}, "minLength": 1, "maxLength": 256,
			"pattern": ".*\\S.*", "description": "Exact Report Project ID. Omit it to choose a Project interactively.",
		},
		"work_id": map[string]any{
			"type": []string{"string", "null"}, "minLength": 1, "maxLength": 256,
			"pattern": ".*\\S.*", "description": "Workstream key or scope ID returned by an earlier picker result.",
		},
		"query": map[string]any{
			"type": []string{"string", "null"}, "minLength": 1, "maxLength": 256,
			"pattern": ".*\\S.*", "description": "Optional case-insensitive filter over Workstream title, key, scope, kind, and labels.",
		},
		"include_archived": map[string]any{
			"type": "boolean", "default": false,
			"description": "Include archived Projects and Workstreams in the choices.",
		},
		"locale": map[string]any{
			"type": "string", "enum": []string{"zh-CN", "en"}, "default": "zh-CN",
			"description": "Language used for picker prompts and result messages.",
		},
	},
}

var handoffPickerOutputSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required": []string{
		"status", "message", "stage", "selected", "project_choices", "workstream_choices", "truncated",
	},
	"properties": map[string]any{
		"status":  map[string]any{"type": "string", "enum": []string{"selected", "needs_selection", "empty", "cancelled", "declined"}},
		"message": map[string]any{"type": "string"},
		"stage": map[string]any{
			"type": []string{"string", "null"}, "enum": []any{"project", "workstream", nil},
		},
		"selected": map[string]any{
			"anyOf": []any{handoffWorkstreamChoiceSchema(), map[string]any{"type": "null"}},
		},
		"project_choices": map[string]any{
			"type": "array", "maxItems": maxPickerChoices, "items": handoffProjectChoiceSchema(),
		},
		"workstream_choices": map[string]any{
			"type": "array", "maxItems": maxPickerChoices, "items": handoffWorkstreamChoiceSchema(),
		},
		"truncated": map[string]any{"type": "boolean"},
	},
}

func handoffProjectChoiceSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"project_id", "project_key", "title"},
		"properties": map[string]any{
			"project_id":  map[string]any{"type": "string"},
			"project_key": map[string]any{"type": "string"},
			"title":       map[string]any{"type": "string"},
		},
	}
}

func handoffWorkstreamChoiceSchema() map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"work_id", "scope_id", "project_id", "project_key", "title", "kind", "catalog_version"},
		"properties": map[string]any{
			"work_id":         map[string]any{"type": "string"},
			"scope_id":        map[string]any{"type": "string"},
			"project_id":      map[string]any{"type": "string"},
			"project_key":     map[string]any{"type": "string"},
			"title":           map[string]any{"type": "string"},
			"kind":            map[string]any{"type": "string", "enum": []string{"feature", "bug", "refactor", "operations", "research", "other"}},
			"catalog_version": map[string]any{"type": "integer", "minimum": 1},
		},
	}
}

func registerHandoffWorkstreamPicker(server *mcp.Server, handler v1.Handler, options Options) {
	server.AddTool(&mcp.Tool{
		Name:        handoffPickerToolName,
		Title:       "Select Handoff Workstream",
		Description: "Select one Report Workstream before handoff_current_work or continue_handoff. Uses a native MCP picker when supported and returns validated structured choices otherwise.",
		InputSchema: handoffPickerInputSchema, OutputSchema: handoffPickerOutputSchema,
		Annotations: annotations(handoffPickerToolName),
	}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runTool(ctx, options, handoffPickerToolName, func(ctx context.Context) (any, error) {
			return selectHandoffWorkstream(ctx, request, handler)
		})
	})
}

func selectHandoffWorkstream(
	ctx context.Context,
	request *mcp.CallToolRequest,
	handler v1.Handler,
) (handoffWorkstreamSelection, error) {
	input, err := decodeHandoffPickerInput(request.Params.Arguments)
	if err != nil {
		return handoffWorkstreamSelection{}, err
	}
	projects, projectsTruncated, err := listPickerProjects(ctx, handler, input.IncludeArchived)
	if err != nil {
		return handoffWorkstreamSelection{}, err
	}
	if len(projects) == 0 {
		return pickerSelection("empty", input.Locale, "no_projects", "", nil, nil, nil, false), nil
	}
	project, selection, err := choosePickerProject(ctx, request, projects, input.ProjectID, input.Locale, projectsTruncated)
	if err != nil || selection != nil {
		if selection == nil {
			return handoffWorkstreamSelection{}, err
		}
		return *selection, err
	}
	workstreams, workstreamsTruncated, err := listPickerWorkstreams(ctx, handler, project.ProjectID, input.IncludeArchived)
	if err != nil {
		return handoffWorkstreamSelection{}, err
	}
	workstreams = filterPickerWorkstreams(workstreams, input.Query)
	choices := make([]handoffWorkstreamChoice, len(workstreams))
	for index, item := range workstreams {
		choices[index] = pickerWorkstreamChoice(item, project)
	}
	truncated := projectsTruncated || workstreamsTruncated
	if len(choices) == 0 {
		key := "no_workstreams"
		if input.Query != nil {
			key = "no_matching_workstreams"
		}
		return pickerSelection("empty", input.Locale, key, "workstream", nil, nil, nil, truncated), nil
	}
	return choosePickerWorkstream(ctx, request, choices, input.WorkID, input.Locale, truncated)
}

func decodeHandoffPickerInput(raw json.RawMessage) (handoffPickerInput, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var input handoffPickerInput
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return handoffPickerInput{}, &endpoint.InvalidRequestError{Field: "arguments"}
	}
	if input.Locale == "" {
		input.Locale = "zh-CN"
	}
	if input.Locale != "zh-CN" && input.Locale != "en" {
		return handoffPickerInput{}, &endpoint.InvalidRequestError{Field: "arguments.locale"}
	}
	for name, value := range map[string]*string{"project_id": input.ProjectID, "work_id": input.WorkID, "query": input.Query} {
		if value != nil && (strings.TrimSpace(*value) == "" || utf8.RuneCountInString(*value) > 256) {
			return handoffPickerInput{}, &endpoint.InvalidRequestError{Field: "arguments." + name}
		}
	}
	return input, nil
}

func listPickerProjects(ctx context.Context, handler v1.Handler, archived bool) ([]v1.ProjectDescriptor, bool, error) {
	response, err := handler.ListHandoffReportProjects(ctx, &v1.ListHandoffReportProjectsRequest{
		Limit: v1.NewOptInt(maxPickerChoices), IncludeArchived: v1.NewOptBool(archived),
	})
	if err != nil {
		return nil, false, err
	}
	headers, ok := response.(*v1.ProjectPageHeaders)
	if !ok || v1.ValidatePowerContextContract(headers.Response) != nil {
		return nil, false, errUnexpectedEndpointResponse
	}
	_, truncated := headers.Response.NextCursor.Get()
	return headers.Response.Items, truncated, nil
}

func listPickerWorkstreams(ctx context.Context, handler v1.Handler, projectID string, archived bool) ([]v1.WorkstreamDescriptor, bool, error) {
	response, err := handler.ListHandoffReportWorkstreams(ctx, &v1.ListHandoffReportWorkstreamsRequest{
		ProjectID: projectID, Limit: v1.NewOptInt(maxPickerChoices), IncludeArchived: v1.NewOptBool(archived),
	})
	if err != nil {
		return nil, false, err
	}
	headers, ok := response.(*v1.WorkstreamPageHeaders)
	if !ok || v1.ValidatePowerContextContract(headers.Response) != nil {
		return nil, false, errUnexpectedEndpointResponse
	}
	_, truncated := headers.Response.NextCursor.Get()
	return headers.Response.Items, truncated, nil
}

func choosePickerProject(
	ctx context.Context,
	request *mcp.CallToolRequest,
	projects []v1.ProjectDescriptor,
	projectID *string,
	locale string,
	truncated bool,
) (v1.ProjectDescriptor, *handoffWorkstreamSelection, error) {
	if projectID != nil {
		for _, project := range projects {
			if project.ProjectID == *projectID {
				return project, nil, nil
			}
		}
		choices := make([]handoffProjectChoice, len(projects))
		for index, project := range projects {
			choices[index] = pickerProjectChoice(project)
		}
		value := pickerSelection("needs_selection", locale, "project_not_found", "project", nil, choices, nil, truncated)
		return v1.ProjectDescriptor{}, &value, nil
	}
	if len(projects) == 1 {
		return projects[0], nil, nil
	}
	if !supportsFormElicitation(request) {
		choices := make([]handoffProjectChoice, len(projects))
		for index, project := range projects {
			choices[index] = pickerProjectChoice(project)
		}
		value := pickerSelection("needs_selection", locale, "choose_project_fallback", "project", nil, choices, nil, truncated)
		return v1.ProjectDescriptor{}, &value, nil
	}
	selected, action, err := elicitPickerChoice(ctx, request, pickerCopy(locale, "choose_project"), pickerCopy(locale, "project_field"), len(projects), func(index int) string {
		return projects[index].Title + " · " + projects[index].ProjectKey
	})
	if err != nil {
		return v1.ProjectDescriptor{}, nil, err
	}
	if action != "selected" {
		value := pickerSelection(action, locale, action, "project", nil, nil, nil, false)
		return v1.ProjectDescriptor{}, &value, nil
	}
	return projects[selected], nil, nil
}

func choosePickerWorkstream(
	ctx context.Context,
	request *mcp.CallToolRequest,
	choices []handoffWorkstreamChoice,
	workID *string,
	locale string,
	truncated bool,
) (handoffWorkstreamSelection, error) {
	var selected *handoffWorkstreamChoice
	if workID != nil {
		matches := make([]int, 0, 1)
		for index, choice := range choices {
			if choice.WorkID == *workID || choice.ScopeID == *workID {
				matches = append(matches, index)
			}
		}
		if len(matches) == 1 {
			value := choices[matches[0]]
			selected = &value
		} else {
			return pickerSelection("needs_selection", locale, "workstream_not_found", "workstream", nil, nil, choices, truncated), nil
		}
	}
	if selected == nil && len(choices) == 1 {
		value := choices[0]
		selected = &value
	}
	if selected == nil && !supportsFormElicitation(request) {
		return pickerSelection("needs_selection", locale, "choose_workstream_fallback", "workstream", nil, nil, choices, truncated), nil
	}
	if selected == nil {
		index, action, err := elicitPickerChoice(ctx, request, pickerCopy(locale, "choose_workstream"), pickerCopy(locale, "workstream_field"), len(choices), func(index int) string {
			choice := choices[index]
			return fmt.Sprintf("%s · %s · %s", choice.Title, choice.WorkID, choice.Kind)
		})
		if err != nil {
			return handoffWorkstreamSelection{}, err
		}
		if action != "selected" {
			return pickerSelection(action, locale, action, "workstream", nil, nil, nil, false), nil
		}
		value := choices[index]
		selected = &value
	}
	return pickerSelection("selected", locale, "selected", "", selected, nil, nil, truncated), nil
}

func supportsFormElicitation(request *mcp.CallToolRequest) bool {
	if request == nil || request.Session == nil {
		return false
	}
	params := request.Session.InitializeParams()
	if params == nil || params.Capabilities == nil || params.Capabilities.Elicitation == nil {
		return false
	}
	capability := params.Capabilities.Elicitation
	return capability.Form != nil || capability.URL == nil
}

func elicitPickerChoice(
	ctx context.Context,
	request *mcp.CallToolRequest,
	message string,
	title string,
	count int,
	choiceTitle func(int) string,
) (int, string, error) {
	oneOf := make([]any, count)
	for index := range count {
		oneOf[index] = map[string]any{"const": fmt.Sprintf("option-%d", index+1), "title": choiceTitle(index)}
	}
	result, err := request.Session.Elicit(ctx, &mcp.ElicitParams{
		Message: message,
		RequestedSchema: map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"value"},
			"properties": map[string]any{
				"value": map[string]any{"type": "string", "title": title, "oneOf": oneOf},
			},
		},
	})
	if err != nil {
		return 0, "", err
	}
	if result.Action == "cancel" {
		return 0, "cancelled", nil
	}
	if result.Action != "accept" {
		return 0, "declined", nil
	}
	token, ok := result.Content["value"].(string)
	if !ok {
		return 0, "", errUnexpectedEndpointResponse
	}
	var selected int
	if _, err := fmt.Sscanf(token, "option-%d", &selected); err != nil || selected < 1 || selected > count {
		return 0, "", errUnexpectedEndpointResponse
	}
	return selected - 1, "selected", nil
}

func filterPickerWorkstreams(values []v1.WorkstreamDescriptor, query *string) []v1.WorkstreamDescriptor {
	if query == nil {
		return values
	}
	normalized := cases.Fold().String(strings.TrimSpace(*query))
	if normalized == "" {
		return values
	}
	result := make([]v1.WorkstreamDescriptor, 0, len(values))
	for _, value := range values {
		key, _ := value.Key.Get()
		fields := []string{value.Title, key, value.ScopeID, string(value.Kind)}
		fields = append(fields, value.Labels...)
		if strings.Contains(cases.Fold().String(strings.Join(fields, "\n")), normalized) {
			result = append(result, value)
		}
	}
	return result
}

func pickerProjectChoice(value v1.ProjectDescriptor) handoffProjectChoice {
	return handoffProjectChoice{ProjectID: value.ProjectID, ProjectKey: value.ProjectKey, Title: value.Title}
}

func pickerWorkstreamChoice(value v1.WorkstreamDescriptor, project v1.ProjectDescriptor) handoffWorkstreamChoice {
	workID, ok := value.Key.Get()
	if !ok {
		workID = value.ScopeID
	}
	return handoffWorkstreamChoice{
		WorkID: workID, ScopeID: value.ScopeID, ProjectID: project.ProjectID, ProjectKey: project.ProjectKey,
		Title: value.Title, Kind: value.Kind, CatalogVersion: value.Version,
	}
}

func pickerSelection(
	status, locale, messageKey, stage string,
	selected *handoffWorkstreamChoice,
	projects []handoffProjectChoice,
	workstreams []handoffWorkstreamChoice,
	truncated bool,
) handoffWorkstreamSelection {
	var stageValue *string
	if stage != "" {
		stageValue = &stage
	}
	if projects == nil {
		projects = []handoffProjectChoice{}
	}
	if workstreams == nil {
		workstreams = []handoffWorkstreamChoice{}
	}
	return handoffWorkstreamSelection{
		Status: status, Message: pickerCopy(locale, messageKey), Stage: stageValue, Selected: selected,
		ProjectChoices: projects, WorkstreamChoices: workstreams, Truncated: truncated,
	}
}

func pickerCopy(locale, key string) string { return handoffPickerCopy[locale][key] }

var handoffPickerCopy = map[string]map[string]string{
	"zh-CN": {
		"cancelled": "已取消工作选择，未产生任何交接写入。", "choose_project": "选择这次交接所属的项目。",
		"choose_project_fallback":    "当前客户端不支持原生选择框，请从 project_choices 选择并重新调用。",
		"choose_workstream":          "选择要交接或继续的工作。",
		"choose_workstream_fallback": "当前客户端不支持原生选择框，请从 workstream_choices 选择并重新调用。",
		"declined":                   "已拒绝工作选择，未产生任何交接写入。", "no_matching_workstreams": "没有与查询条件匹配的工作。",
		"no_projects": "没有可供选择的交接项目。", "no_workstreams": "所选项目中没有可供选择的工作。",
		"project_field": "项目", "project_not_found": "找不到指定项目，请从 project_choices 重新选择。",
		"selected": "已选择工作；此操作尚未创建或提交交接。", "workstream_field": "工作",
		"workstream_not_found": "找不到指定工作，请从 workstream_choices 重新选择。",
	},
	"en": {
		"cancelled":                  "Work selection was cancelled; no Handoff data was written.",
		"choose_project":             "Choose the Project that owns this Handoff.",
		"choose_project_fallback":    "This client has no native picker; choose from project_choices and call again.",
		"choose_workstream":          "Choose the work to hand off or continue.",
		"choose_workstream_fallback": "This client has no native picker; choose from workstream_choices and call again.",
		"declined":                   "Work selection was declined; no Handoff data was written.",
		"no_matching_workstreams":    "No work matches the query.", "no_projects": "No Handoff Projects are available.",
		"no_workstreams": "The selected Project has no available work.", "project_field": "Project",
		"project_not_found": "The requested Project was not found; choose from project_choices.",
		"selected":          "Work selected; this operation has not created or committed a Handoff.",
		"workstream_field":  "Work", "workstream_not_found": "The requested work was not found; choose from workstream_choices.",
	},
}
