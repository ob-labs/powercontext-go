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

package openapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ghodss/yaml"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

func TestContractUsesNamespacedRequestIDHeader(t *testing.T) {
	t.Parallel()
	_, raw := loadPythonContract(t)
	if !strings.Contains(string(raw), "X-PowerContext-Request-ID") {
		t.Fatal("canonical OpenAPI does not contain X-PowerContext-Request-ID")
	}
	if strings.Contains(string(raw), "X-Request-ID") {
		t.Fatal("canonical OpenAPI contains the non-namespaced X-Request-ID header")
	}
}

func TestContractDeclaresOptionalBearerAuthentication(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	security := mustArray(t, contract["security"], "security")
	if len(security) != 2 || len(mustObject(t, security[0], "security[0]")) != 1 ||
		len(mustObject(t, security[1], "security[1]")) != 0 {
		t.Fatalf("top-level security = %#v, want bearer-or-anonymous", security)
	}
	bearer := mustObjectAt(t, contract, "components", "securitySchemes", "BearerAuth")
	assertStringValue(t, bearer, "type", "http")
	assertStringValue(t, bearer, "scheme", "bearer")
	assertStringValue(t, bearer, "description", "Static bearer token used when local Server authentication is enabled.")

	for item := range contractOperations(t, contract) {
		if strings.HasPrefix(item.path, "/health/") {
			if values := mustArray(t, item.operation["security"], item.path+" security"); len(values) != 0 {
				t.Fatalf("%s %s security = %#v, want public", item.method, item.path, values)
			}
			continue
		}
		unauthorized := mustObjectAt(t, item.operation, "responses", "401")
		assertStringValue(t, unauthorized, "$ref", "#/components/responses/Unauthorized")
	}
}

func TestCapabilitiesReportSemanticsWithoutRuntimeTuningValues(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	schemas := mustObjectAt(t, contract, "components", "schemas")
	properties := mustObjectAt(t, schemas, "Capabilities", "properties")
	assertKeySet(t, properties,
		"source_types", "artifact_families", "memory_extraction", "experience_generation",
		"managed_skill_generation", "external_skill_registry", "handoff_generation",
		"search_modes", "context_versions",
	)
	if _, found := schemas["CapabilityLimit"]; found {
		t.Fatal("CapabilityLimit leaked runtime tuning values into the public contract")
	}
}

func TestReadinessOperationDeclaresUnavailableResponse(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	responses := mustObjectAt(t, operationAt(t, contract, "/health/ready", http.MethodGet), "responses")
	if _, found := responses["503"]; !found {
		t.Fatal("get_readiness does not declare 503")
	}
}

func TestCaptureOperationDeclaresTypedAcceptedExchange(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	operation := operationAt(t, contract, "/v1/sources/content", http.MethodPost)
	assertOperationExchange(t, operation, "CaptureContentSourceRequest", "202", "CaptureContentSourceResponse")
}

func TestStatsOperationExposesDashboardReadyScopedValues(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	operation := operationAt(t, contract, "/v1/stats", http.MethodGet)
	if _, found := operation["requestBody"]; found {
		t.Fatal("get_stats unexpectedly has a request body")
	}
	parameters := mustArray(t, operation["parameters"], "get_stats parameters")
	if got := []string{
		mustString(t, mustObject(t, parameters[0], "parameter 0")["name"], "parameter 0 name"),
		mustString(t, mustObject(t, parameters[1], "parameter 1")["name"], "parameter 1 name"),
	}; !slices.Equal(got, []string{"scope_id", "period"}) {
		t.Fatalf("get_stats parameters = %v", got)
	}
	assertResponseSchema(t, operation, "200", "ScopedStats")

	schemas := mustObjectAt(t, contract, "components", "schemas")
	assertKeySet(t, mustObjectAt(t, schemas, "ScopedStats", "properties"), "scope_id", "as_of", "inventory", "usage", "recall")
	assertNumberValue(t, mustObjectAt(t, schemas, "UsageStatistics", "properties", "by_purpose"), "maxItems", 16)
	assertNumberValue(t, mustObjectAt(t, schemas, "UsageStatistics", "properties", "daily"), "maxItems", 30)
	assertBoolValue(t, mustObjectAt(t, schemas, "ModelUsageValue", "properties", "input_tokens"), "nullable", true)
	assertBoolValue(t, mustObjectAt(t, schemas, "ModelUsageValue", "properties", "output_tokens"), "nullable", true)
	assertBoolValue(t, mustObjectAt(t, schemas, "RecallTokenStatistics", "properties", "estimator"), "nullable", true)
	assertNumberValue(t, mustObjectAt(t, schemas, "RecallTokenStatistics", "properties", "daily"), "maxItems", 30)
	assertStringValue(t, mustObjectAt(t, schemas, "GetStatsRequest", "properties", "period"), "default", "30d")
}

func TestMemoryOperationsUseFamilyPrefixedPathsAndTypedRequests(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	operations := map[string]string{
		"/v1/memory/flush":          "FlushMemoryRequest",
		"/v1/memory/remember":       "RememberMemoryRequest",
		"/v1/memory/search":         "SearchMemoryRequest",
		"/v1/memory/entries/list":   "ListMemoryEntriesRequest",
		"/v1/memory/entries/get":    "GetMemoryEntryRequest",
		"/v1/memory/entries/revise": "ReviseMemoryEntryRequest",
		"/v1/memory/entries/retire": "RetireMemoryEntryRequest",
		"/v1/memory/changes":        "ListMemoryChangesRequest",
	}
	for path, request := range operations {
		if !strings.HasPrefix(path, "/v1/memory/") {
			t.Fatalf("memory operation path = %q", path)
		}
		assertRequestSchema(t, operationAt(t, contract, path, http.MethodPost), request)
	}
}

func TestPreparedContextIsGenericTypedOperationOutsideMemoryTools(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	operation := operationAt(t, contract, "/v1/context/prepare", http.MethodPost)
	assertOperationExchange(t, operation, "PrepareContextRequest", "200", "PreparedContext")
	schemas := mustObjectAt(t, contract, "components", "schemas")
	assertKeySet(t, mustObjectAt(t, schemas, "PrepareContextRequest", "properties"), "scope_id", "query", "max_bytes")
	prepared := mustObjectAt(t, schemas, "PreparedContext", "properties")
	assertKeySet(t, prepared, "schema", "status", "content", "content_bytes")
	for _, forbidden := range []string{"memory", "mode", "selection"} {
		if _, found := prepared[forbidden]; found {
			t.Fatalf("PreparedContext unexpectedly contains %q", forbidden)
		}
	}
}

func TestExperienceSkillAndReviewOperationsAreTypedAndFamilyRouted(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	for _, exchange := range []struct {
		path, request, status, response string
	}{
		{"/v1/experience/propose", "ProposeExperienceRequest", "201", "ArtifactCandidate"},
		{"/v1/experience/generate", "GenerateExperienceRequest", "200", "GeneratedCandidateResponse"},
		{"/v1/skill/propose", "ProposeSkillRequest", "201", "ArtifactCandidate"},
		{"/v1/skill/generate", "GenerateSkillRequest", "200", "GeneratedCandidateResponse"},
	} {
		assertOperationExchange(t, operationAt(t, contract, exchange.path, http.MethodPost), exchange.request, exchange.status, exchange.response)
	}
	for _, path := range []string{
		"/v1/artifact-candidates/list", "/v1/artifact-candidates/get", "/v1/artifact-candidates/approve",
		"/v1/artifact-candidates/reject", "/v1/artifact-candidates/revise",
	} {
		if !strings.HasPrefix(path, "/v1/artifact-candidates/") {
			t.Fatalf("review operation path = %q", path)
		}
		_ = operationAt(t, contract, path, http.MethodPost)
	}
	assertRequestSchema(t, operationAt(t, contract, "/v1/artifact-candidates/approve", http.MethodPost), "ApproveArtifactCandidateRequest")

	schemas := mustObjectAt(t, contract, "components", "schemas")
	assertKeySet(t, mustObjectAt(t, schemas, "ExperienceProposal", "properties"), "situation", "action", "outcome", "lesson")
	assertKeySet(t, mustObjectAt(t, schemas, "SkillProposal", "properties"), "name", "description", "instructions", "validation")
	limit := mustObjectAt(t, schemas, "ListArtifactCandidatesRequest", "properties", "limit")
	assertNumberValue(t, limit, "minimum", 1)
	assertNumberValue(t, limit, "maximum", 100)
	assertNumberValue(t, limit, "default", 50)
	for _, schemaName := range []string{
		"ArtifactCandidate", "ProposeExperienceRequest", "GenerateExperienceRequest",
		"ProposeSkillRequest", "GenerateSkillRequest", "ReviseArtifactCandidateRequest",
	} {
		properties := mustObjectAt(t, schemas, schemaName, "properties")
		for _, field := range []string{"source_refs", "artifact_refs"} {
			value := mustObjectAt(t, properties, field)
			assertNumberValue(t, value, "maxItems", 32)
			if !strings.Contains(mustString(t, value["description"], schemaName+" "+field+" description"), "combined maximum of 32") {
				t.Fatalf("%s.%s does not describe the combined limit", schemaName, field)
			}
		}
	}
}

func TestManagedSkillTransportRejectsUntrimmedProjectionMetadata(t *testing.T) {
	t.Parallel()
	proposal := v1.SkillProposal{
		Name: " managed-skill ", Description: "Use for a bounded task.",
		Instructions: "Perform the bounded task.",
		Validation:   []v1.SkillValidationItem{"The expected result exists."},
	}
	if err := proposal.Validate(); err == nil {
		t.Fatal("generated SkillProposal accepted untrimmed name")
	}
}

func TestExternalSkillOperationsPreserveLocalAuthorityAndExactResolution(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	for _, exchange := range []struct {
		path, request, response string
	}{
		{"/v1/external-skills/scan", "ScanExternalSkillsRequest", "ScanExternalSkillsResponse"},
		{"/v1/external-skills/list", "ListExternalSkillsRequest", "ListExternalSkillsResponse"},
		{"/v1/external-skills/resolve", "ResolveExternalSkillRequest", "ExternalSkillResolution"},
		{"/v1/external-skills/import", "ImportExternalSkillRequest", "GeneratedCandidateResponse"},
	} {
		assertOperationExchange(t, operationAt(t, contract, exchange.path, http.MethodPost), exchange.request, "200", exchange.response)
	}
	schemas := mustObjectAt(t, contract, "components", "schemas")
	registration := mustObjectAt(t, schemas, "ExternalSkillRegistration", "properties")
	assertKeySet(t, registration,
		"external_skill_id", "provider", "agent_kind", "host_id", "installation_scope",
		"locator", "fingerprint", "name", "description",
	)
	if !strings.Contains(mustString(t, mustObjectAt(t, registration, "locator")["description"], "locator description"), "cross-Agent") {
		t.Fatal("external Skill locator does not preserve host-local authority")
	}
	resolve := mustObjectAt(t, schemas, "ResolveExternalSkillRequest")
	assertStringSlice(t, mustArray(t, resolve["required"], "ResolveExternalSkillRequest.required"), "scope_id", "external_skill_id", "fingerprint")
	if _, found := mustObjectAt(t, resolve, "properties")["mode"]; found {
		t.Fatal("ResolveExternalSkillRequest unexpectedly exposes mode")
	}
	importRequest := mustObjectAt(t, schemas, "ImportExternalSkillRequest")
	assertStringSlice(t, mustArray(t, importRequest["required"], "ImportExternalSkillRequest.required"), "scope_id", "external_skill_id", "fingerprint", "mode")
}

func TestMemorySearchModeRemainsOnSearchRequest(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	mode := mustObjectAt(t, contract, "components", "schemas", "SearchMemoryRequest", "properties", "mode")
	assertStringValue(t, mode, "$ref", "#/components/schemas/MemorySearchMode")
	assertStringValue(t, mode, "default", "auto")

	var request v1.SearchMemoryRequest
	decodeAndValidate(t, `{"scope_id":"scope","query":"query"}`, &request)
	value, ok := request.Mode.Get()
	if !ok || value != v1.MemorySearchModeAuto {
		t.Fatalf("generated SearchMemoryRequest mode = (%q, %t), want (auto, true)", value, ok)
	}
}

func TestMemorySearchDeclaresRevisionConflictResponse(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	response := mustObjectAt(t, operationAt(t, contract, "/v1/memory/search", http.MethodPost), "responses", "409")
	assertStringValue(t, response, "$ref", "#/components/responses/Conflict")
}

func TestCandidateTransportRejectsCombinedEvidenceOverLimit(t *testing.T) {
	t.Parallel()
	sources := make([]v1.SourceReference, 20)
	artifacts := make([]v1.ArtifactReference, 13)
	for name, request := range map[string]any{
		"propose": &v1.ProposeExperienceRequest{SourceRefs: sources, ArtifactRefs: artifacts},
		"revise":  &v1.ReviseArtifactCandidateRequest{SourceRefs: sources, ArtifactRefs: artifacts},
	} {
		t.Run(name, func(t *testing.T) {
			err := v1.ValidatePowerContextContract(request)
			var limit *v1.CombinedEvidenceLimitError
			if !errors.As(err, &limit) {
				t.Fatalf("error = %#v, want CombinedEvidenceLimitError", err)
			}
		})
	}
}

func TestHandoffOperationsExposeCompleteExplicitLifecycle(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	for _, exchange := range []struct {
		path, request, response string
	}{
		{"/v1/handoff/activate", "ActivateHandoffRequest", "HandoffActivation"},
		{"/v1/handoff/prepare", "PrepareHandoffRequest", "HandoffDraft"},
		{"/v1/handoff/finalize", "FinalizeHandoffRequest", "PreparedHandoff"},
		{"/v1/handoff/commit", "CommitHandoffRequest", "CommittedHandoff"},
		{"/v1/handoff/continue", "ContinueHandoffRequest", "HandoffResolution"},
	} {
		assertOperationExchange(t, operationAt(t, contract, exchange.path, http.MethodPost), exchange.request, "200", exchange.response)
	}
}

func TestWorkOperationsExposeHighLevelContinuityLoop(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	for _, exchange := range []struct {
		path, request, status, response string
	}{
		{"/v1/work/contracts/create", "CreateWorkContractRequest", "202", "WorkSourceReceipt"},
		{"/v1/work/handoffs/prepare-current", "HandoffCurrentWorkRequest", "200", "PreparedWorkHandoff"},
		{"/v1/work/handoffs/acknowledge", "AcknowledgeHandoffRequest", "200", "HandoffAcknowledgement"},
		{"/v1/work/outcomes/record", "RecordTaskOutcomeRequest", "202", "WorkSourceReceipt"},
	} {
		assertOperationExchange(
			t, operationAt(t, contract, exchange.path, http.MethodPost),
			exchange.request, exchange.status, exchange.response,
		)
	}
}

func TestSourceReferenceKeepsNameAsSourceType(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	properties := mustObjectAt(t, contract, "components", "schemas", "SourceReference", "properties")
	assertKeySet(t, properties, "name", "source_id")
}

func TestMemoryTransportHasOneReferenceShapeAndNestedCitations(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	schemas := mustObjectAt(t, contract, "components", "schemas")
	if _, found := schemas["MemoryReference"]; found {
		t.Fatal("MemoryReference duplicates ArtifactReference")
	}
	memoryRef := mustObjectAt(t, schemas, "MemoryCitation", "properties", "memory_ref")
	assertStringValue(t, memoryRef, "$ref", "#/components/schemas/ArtifactReference")
	for _, schemaName := range []string{"GetMemoryEntryRequest", "ReviseMemoryEntryRequest", "RetireMemoryEntryRequest"} {
		properties := mustObjectAt(t, schemas, schemaName, "properties")
		assertStringValue(t, mustObjectAt(t, properties, "citation"), "$ref", "#/components/schemas/MemoryCitation")
		for _, forbidden := range []string{"memory_id", "expected_revision"} {
			if _, found := properties[forbidden]; found {
				t.Fatalf("%s unexpectedly contains %s", schemaName, forbidden)
			}
		}
	}
}

func TestEntryListHidesInactiveEntriesUnlessExplicitlyRequested(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	includeInactive := mustObjectAt(t, contract, "components", "schemas", "ListMemoryEntriesRequest", "properties", "include_inactive")
	assertBoolValue(t, includeInactive, "default", false)

	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "default", raw: `{"scope_id":"scope"}`, want: false},
		{name: "explicit audit", raw: `{"scope_id":"scope","include_inactive":true}`, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request v1.ListMemoryEntriesRequest
			decodeAndValidate(t, test.raw, &request)
			got, ok := request.IncludeInactive.Get()
			if !ok || got != test.want {
				t.Fatalf("include_inactive = (%t, %t), want (%t, true)", got, ok, test.want)
			}
		})
	}
}

func TestGeneratedTransportRejectsValuesOutsideOpenAPI(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		raw    string
		target func() wireValidatable
	}{
		{"zero revision", `{"family":"memory","artifact_id":"memory-1","revision":0}`, func() wireValidatable { return new(v1.ArtifactReference) }},
		{"string revision", `{"family":"memory","artifact_id":"memory-1","revision":"1"}`, func() wireValidatable { return new(v1.ArtifactReference) }},
		{"spaced artifact ID", `{"family":"memory","artifact_id":"memory with spaces","revision":1}`, func() wireValidatable { return new(v1.ArtifactReference) }},
		{"boolean limit", `{"scope_id":"scope","query":"query","limit":true}`, func() wireValidatable { return new(v1.SearchMemoryRequest) }},
		{"integer boolean", `{"scope_id":"scope","include_inactive":1}`, func() wireValidatable { return new(v1.ListMemoryEntriesRequest) }},
		{"unicode citation ID", `{"scope_id":"scope","citation":{"memory_ref":{"family":"memory","artifact_id":"memory-1","revision":1},"entry_id":"记忆","entry_version_id":"version-1"}}`, func() wireValidatable { return new(v1.GetMemoryEntryRequest) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target := test.target()
			err := json.Unmarshal([]byte(test.raw), target)
			if err == nil {
				err = target.Validate()
			}
			if err == nil {
				t.Fatal("generated transport accepted an out-of-contract value")
			}
		})
	}
}

func TestGeneratedServerRoutesMatchCanonicalOpenAPI(t *testing.T) {
	t.Parallel()
	contract, _ := loadPythonContract(t)
	server, err := v1.NewServer(v1.UnimplementedHandler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for item := range contractOperations(t, contract) {
		route, found := server.FindRoute(strings.ToUpper(item.method), item.path)
		if !found {
			t.Fatalf("generated Server has no route for %s %s", item.method, item.path)
		}
		operationID := mustString(t, item.operation["operationId"], item.method+" "+item.path+" operationId")
		if got := route.OperationID(); got != operationID {
			t.Fatalf("%s %s operationId = %q, want %q", item.method, item.path, got, operationID)
		}
		if got := route.PathPattern(); got != item.path {
			t.Fatalf("%s %s path pattern = %q", item.method, item.path, got)
		}
		count++
	}
	if count != 53 {
		t.Fatalf("verified generated routes = %d, want 53", count)
	}
}

type wireValidatable interface {
	Validate() error
}

func loadPythonContract(t *testing.T) (map[string]any, []byte) {
	t.Helper()
	raw, err := os.ReadFile("powercontext.yaml")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	var contract map[string]any
	if err := json.Unmarshal(encoded, &contract); err != nil {
		t.Fatal(err)
	}
	return contract, raw
}

type contractOperation struct {
	path      string
	method    string
	operation map[string]any
}

func contractOperations(t *testing.T, contract map[string]any) func(func(contractOperation) bool) {
	t.Helper()
	paths := mustObjectAt(t, contract, "paths")
	return func(yield func(contractOperation) bool) {
		for path, pathValue := range paths {
			item := mustObject(t, pathValue, path)
			for method, operationValue := range item {
				if !isContractHTTPMethod(method) {
					continue
				}
				if !yield(contractOperation{
					path: path, method: method,
					operation: mustObject(t, operationValue, method+" "+path),
				}) {
					return
				}
			}
		}
	}
}

func isContractHTTPMethod(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func operationAt(t *testing.T, contract map[string]any, path, method string) map[string]any {
	t.Helper()
	return mustObjectAt(t, contract, "paths", path, strings.ToLower(method))
}

func assertOperationExchange(t *testing.T, operation map[string]any, request, status, response string) {
	t.Helper()
	assertRequestSchema(t, operation, request)
	assertResponseSchema(t, operation, status, response)
}

func assertRequestSchema(t *testing.T, operation map[string]any, want string) {
	t.Helper()
	schema := mustObjectAt(t, operation, "requestBody", "content", "application/json", "schema")
	assertStringValue(t, schema, "$ref", "#/components/schemas/"+want)
}

func assertResponseSchema(t *testing.T, operation map[string]any, status, want string) {
	t.Helper()
	schema := mustObjectAt(t, operation, "responses", status, "content", "application/json", "schema")
	assertStringValue(t, schema, "$ref", "#/components/schemas/"+want)
}

func mustObjectAt(t *testing.T, root map[string]any, keys ...string) map[string]any {
	t.Helper()
	var current any = root
	for _, key := range keys {
		object := mustObject(t, current, strings.Join(keys, "."))
		value, found := object[key]
		if !found {
			t.Fatalf("%s has no key %q", strings.Join(keys, "."), key)
		}
		current = value
	}
	return mustObject(t, current, strings.Join(keys, "."))
}

func mustObject(t *testing.T, value any, label string) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s = %T, want object", label, value)
	}
	return object
}

func mustArray(t *testing.T, value any, label string) []any {
	t.Helper()
	array, ok := value.([]any)
	if !ok {
		t.Fatalf("%s = %T, want array", label, value)
	}
	return array
}

func mustString(t *testing.T, value any, label string) string {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		t.Fatalf("%s = %T, want string", label, value)
	}
	return text
}

func assertStringValue(t *testing.T, object map[string]any, key, want string) {
	t.Helper()
	if got := mustString(t, object[key], key); got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

func assertNumberValue(t *testing.T, object map[string]any, key string, want float64) {
	t.Helper()
	got, ok := object[key].(float64)
	if !ok || got != want {
		t.Fatalf("%s = %#v, want %v", key, object[key], want)
	}
}

func assertBoolValue(t *testing.T, object map[string]any, key string, want bool) {
	t.Helper()
	got, ok := object[key].(bool)
	if !ok || got != want {
		t.Fatalf("%s = %#v, want %t", key, object[key], want)
	}
}

func assertKeySet(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	got := make([]string, 0, len(object))
	for key := range object {
		got = append(got, key)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
}

func assertStringSlice(t *testing.T, values []any, want ...string) {
	t.Helper()
	got := make([]string, len(values))
	for index := range values {
		got[index] = mustString(t, values[index], fmt.Sprintf("value %d", index))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
}

func decodeAndValidate(t *testing.T, raw string, target wireValidatable) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), target); err != nil {
		t.Fatal(err)
	}
	if err := target.Validate(); err != nil {
		t.Fatal(err)
	}
}
