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
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

func TestHandoffPickerReturnsStructuredFallbackAndExactSelection(t *testing.T) {
	t.Parallel()
	project := pickerProject("prj-powercontext", "powercontext", "PowerContext")
	handler := &pickerHandler{
		projects: []v1.ProjectDescriptor{project},
		workstreams: map[string][]v1.WorkstreamDescriptor{project.ProjectID: {
			pickerWorkstream(project.ProjectID, "scope-claude", "claude-compat", "Claude compatibility"),
			pickerWorkstream(project.ProjectID, "scope-ui", "handoff-ui", "Handoff workbench"),
		}},
	}
	server, err := NewServer(handler, Options{HandoffReportEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	client := connectInMemory(t, server)

	choices, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: handoffPickerToolName, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	choicesContent := choices.StructuredContent.(map[string]any)
	if choices.IsError || choicesContent["status"] != "needs_selection" || choicesContent["stage"] != "workstream" {
		t.Fatalf("fallback selection = %#v", choices)
	}
	workstreams := choicesContent["workstream_choices"].([]any)
	if got := []any{workstreams[0].(map[string]any)["work_id"], workstreams[1].(map[string]any)["work_id"]}; !reflect.DeepEqual(got, []any{"claude-compat", "handoff-ui"}) {
		t.Fatalf("Workstream choices = %#v", workstreams)
	}

	selected, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      handoffPickerToolName,
		Arguments: map[string]any{"project_id": project.ProjectID, "work_id": "handoff-ui"},
	})
	if err != nil {
		t.Fatal(err)
	}
	selectedContent := selected.StructuredContent.(map[string]any)
	choice := selectedContent["selected"].(map[string]any)
	if selected.IsError || selectedContent["status"] != "selected" ||
		choice["scope_id"] != "scope-ui" || choice["catalog_version"] != float64(1) {
		t.Fatalf("selected Workstream = %#v", selected)
	}
}

func TestHandoffPickerPreservesAmbiguity(t *testing.T) {
	t.Parallel()
	project := pickerProject("prj-powercontext", "powercontext", "PowerContext")
	handler := &pickerHandler{
		projects: []v1.ProjectDescriptor{project},
		workstreams: map[string][]v1.WorkstreamDescriptor{project.ProjectID: {
			pickerWorkstream(project.ProjectID, "scope-claude", "shared-id", "Claude compatibility"),
			pickerWorkstream(project.ProjectID, "shared-id", "handoff-ui", "Handoff workbench"),
		}},
	}
	server, err := NewServer(handler, Options{HandoffReportEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := connectInMemory(t, server).CallTool(context.Background(), &mcp.CallToolParams{
		Name:      handoffPickerToolName,
		Arguments: map[string]any{"project_id": project.ProjectID, "work_id": "shared-id"},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := result.StructuredContent.(map[string]any)
	if result.IsError || content["status"] != "needs_selection" || content["selected"] != nil {
		t.Fatalf("ambiguous result = %#v", result)
	}
}

func TestHandoffPickerUsesNativeFormAndPreservesCancellation(t *testing.T) {
	t.Parallel()
	project := pickerProject("prj-powercontext", "powercontext", "PowerContext")
	handler := &pickerHandler{
		projects: []v1.ProjectDescriptor{project},
		workstreams: map[string][]v1.WorkstreamDescriptor{project.ProjectID: {
			pickerWorkstream(project.ProjectID, "scope-claude", "claude-compat", "Claude compatibility"),
			pickerWorkstream(project.ProjectID, "scope-ui", "handoff-ui", "Handoff workbench"),
		}},
	}

	t.Run("accept", func(t *testing.T) {
		server, err := NewServer(handler, Options{HandoffReportEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		var message string
		client := connectInMemoryWithOptions(t, server, &mcp.ClientOptions{
			ElicitationHandler: func(_ context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				message = request.Params.Message
				schema := request.Params.RequestedSchema.(map[string]any)
				properties := schema["properties"].(map[string]any)
				options := properties["value"].(map[string]any)["oneOf"].([]any)
				if len(options) != 2 || options[1].(map[string]any)["title"] != "Handoff workbench · handoff-ui · feature" {
					t.Fatalf("elicitation schema = %#v", schema)
				}
				return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"value": "option-2"}}, nil
			},
		})
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
			Name: handoffPickerToolName, Arguments: map[string]any{"locale": "en"},
		})
		if err != nil {
			t.Fatal(err)
		}
		content := result.StructuredContent.(map[string]any)
		selected := content["selected"].(map[string]any)
		if message != "Choose the work to hand off or continue." || selected["work_id"] != "handoff-ui" {
			t.Fatalf("native picker = message:%q result:%#v", message, result)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		server, err := NewServer(handler, Options{HandoffReportEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		client := connectInMemoryWithOptions(t, server, &mcp.ClientOptions{
			ElicitationHandler: func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				return &mcp.ElicitResult{Action: "cancel"}, nil
			},
		})
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: handoffPickerToolName, Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		content := result.StructuredContent.(map[string]any)
		if content["status"] != "cancelled" || content["stage"] != "workstream" || content["selected"] != nil {
			t.Fatalf("cancelled picker = %#v", result)
		}
	})
}

func TestHandoffPickerSelectsProjectThenWorkstream(t *testing.T) {
	t.Parallel()
	memory := pickerProject("prj-memory", "memory", "Memory")
	handoff := pickerProject("prj-handoff", "handoff", "Handoff")
	handler := &pickerHandler{
		projects: []v1.ProjectDescriptor{memory, handoff},
		workstreams: map[string][]v1.WorkstreamDescriptor{
			memory.ProjectID: {pickerWorkstream(memory.ProjectID, "scope-memory", "memory-core", "Memory Core")},
			handoff.ProjectID: {
				pickerWorkstream(handoff.ProjectID, "scope-cli", "handoff-cli", "Handoff CLI"),
				pickerWorkstream(handoff.ProjectID, "scope-web", "handoff-web", "Handoff Web"),
			},
		},
	}
	server, err := NewServer(handler, Options{HandoffReportEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	replies := []string{"option-2", "option-1"}
	messages := []string{}
	client := connectInMemoryWithOptions(t, server, &mcp.ClientOptions{
		ElicitationHandler: func(_ context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			messages = append(messages, request.Params.Message)
			value := replies[0]
			replies = replies[1:]
			return &mcp.ElicitResult{Action: "accept", Content: map[string]any{"value": value}}, nil
		},
	})
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: handoffPickerToolName, Arguments: map[string]any{"locale": "en"},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := result.StructuredContent.(map[string]any)
	selected := content["selected"].(map[string]any)
	wantMessages := []string{"Choose the Project that owns this Handoff.", "Choose the work to hand off or continue."}
	if !reflect.DeepEqual(messages, wantMessages) || selected["project_id"] != handoff.ProjectID || selected["work_id"] != "handoff-cli" {
		t.Fatalf("two-stage picker = messages:%#v result:%#v", messages, result)
	}
}

func connectInMemoryWithOptions(t *testing.T, server *mcp.Server, options *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "powercontext-picker-test", Version: "1"}, options)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

type pickerHandler struct {
	v1.UnimplementedHandler
	projects    []v1.ProjectDescriptor
	workstreams map[string][]v1.WorkstreamDescriptor
}

func (h *pickerHandler) ListHandoffReportProjects(context.Context, *v1.ListHandoffReportProjectsRequest) (v1.ListHandoffReportProjectsRes, error) {
	next := v1.NilString{}
	next.SetToNull()
	return &v1.ProjectPageHeaders{Response: v1.ProjectPage{Items: h.projects, NextCursor: next}}, nil
}

func (h *pickerHandler) ListHandoffReportWorkstreams(_ context.Context, request *v1.ListHandoffReportWorkstreamsRequest) (v1.ListHandoffReportWorkstreamsRes, error) {
	next := v1.NilString{}
	next.SetToNull()
	return &v1.WorkstreamPageHeaders{Response: v1.WorkstreamPage{Items: h.workstreams[request.ProjectID], NextCursor: next}}, nil
}

func pickerProject(id, key, title string) v1.ProjectDescriptor {
	description := v1.NilString{}
	description.SetToNull()
	return v1.ProjectDescriptor{
		Schema: v1.ProjectDescriptorSchemaPowercontextProjectV1, ProjectID: id, ProjectKey: key, Title: title,
		Description: description, DefaultLocale: v1.ReportLocaleZhCN, Timezone: "UTC",
		CatalogState: v1.ReportCatalogStateIncluded, Version: 1,
	}
}

func pickerWorkstream(projectID, scopeID, key, title string) v1.WorkstreamDescriptor {
	return v1.WorkstreamDescriptor{
		Schema: v1.WorkstreamDescriptorSchemaPowercontextWorkstreamV1, ScopeID: scopeID, ProjectID: projectID,
		Key: v1.NewNilString(key), Title: title, Kind: v1.WorkstreamKindFeature,
		CatalogState: v1.ReportCatalogStateIncluded, ExternalRefs: []v1.HandoffReportExternalReference{}, Labels: []string{}, Version: 1,
	}
}
