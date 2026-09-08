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
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/ghodss/yaml"
)

const (
	compatibilitySurfaceSchemaVersion = 2
	legacyBaselineOperationCount      = 53
	canonicalOperationCount           = 94
	upstreamOnlyOperationCount        = 55
	retainedExtensionCount            = 14
	explicitMCPToolCount              = 23

	compatibilityStatusDeferred             = "deferred"
	compatibilityStatusImplementedCanonical = "implemented-canonical"
)

var generatedOpenAPIToolAllowlist = map[string]struct{}{
	"acknowledge_handoff":              {},
	"activate_handoff":                 {},
	"approve_artifact_candidate":       {},
	"capture_content_source":           {},
	"commit_handoff":                   {},
	"continue_handoff":                 {},
	"create_work_contract":             {},
	"finalize_handoff":                 {},
	"get_artifact_candidate":           {},
	"get_handoff_report":               {},
	"get_handoff_report_workspace":     {},
	"get_memory_entry":                 {},
	"handoff_current_work":             {},
	"list_artifact_candidates":         {},
	"list_handoff_report_known_scopes": {},
	"list_memory_entries":              {},
	"record_task_outcome":              {},
	"reject_artifact_candidate":        {},
	"remember_memory":                  {},
	"retire_memory_entry":              {},
	"revise_artifact_candidate":        {},
	"revise_memory_entry":              {},
	"search_memory":                    {},
}

// compatibilitySurface records a pinned upstream operation inventory without
// claiming that every canonical operation has a generated Go transport yet.
// The legacy contract remains the only input to ogen until each canonical
// cluster has a separate implementation and compatibility review.
type compatibilitySurface struct {
	SchemaVersion                 int                      `json:"schema_version"`
	Upstream                      compatibilityUpstream    `json:"upstream"`
	Legacy                        compatibilityLegacy      `json:"legacy"`
	Canonical                     compatibilityCanonical   `json:"canonical"`
	RetainedGoExtensions          []compatibilityOperation `json:"retained_go_extensions"`
	MethodMigrations              []compatibilityMigration `json:"method_migrations"`
	MCPGeneratedOpenAPIOperations []string                 `json:"mcp_generated_openapi_operations"`
}

type compatibilityUpstream struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
}

type compatibilityLegacy struct {
	BaselineOperationCount int                      `json:"baseline_operation_count"`
	BaselineOperations     []compatibilityOperation `json:"baseline_operations"`
}

type compatibilityCanonical struct {
	OperationCount         int                            `json:"operation_count"`
	UpstreamOnlyOperations []compatibilityStagedOperation `json:"upstream_only_operations"`
}

type compatibilityOperation struct {
	OperationID string `json:"operation_id"`
	Method      string `json:"method"`
	Path        string `json:"path"`
}

type compatibilityStagedOperation struct {
	OperationID string `json:"operation_id"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Status      string `json:"status"`
}

type compatibilityStagedOperations struct {
	All map[string]compatibilityOperation
}

type compatibilityMigration struct {
	OperationID         string                `json:"operation_id"`
	Legacy              compatibilityEndpoint `json:"legacy"`
	Canonical           compatibilityEndpoint `json:"canonical"`
	CanonicalMethodName string                `json:"canonical_method_name"`
}

type compatibilityEndpoint struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

func validateCompatibilitySurface(specification, contents []byte) error {
	surface, err := decodeCompatibilitySurface(contents)
	if err != nil {
		return err
	}
	operations, err := parseOpenAPIOperations(specification)
	if err != nil {
		return err
	}
	return surface.validate(operations)
}

func decodeCompatibilitySurface(contents []byte) (compatibilitySurface, error) {
	var surface compatibilitySurface
	if err := json.Unmarshal(contents, &surface, json.RejectUnknownMembers(true)); err != nil {
		return compatibilitySurface{}, fmt.Errorf("decode compatibility surface: %w", err)
	}
	return surface, nil
}

func cloneCompatibilitySurface(surface compatibilitySurface) (compatibilitySurface, error) {
	contents, err := json.Marshal(surface)
	if err != nil {
		return compatibilitySurface{}, err
	}
	return decodeCompatibilitySurface(contents)
}

func parseOpenAPIOperations(specification []byte) (map[string]compatibilityEndpoint, error) {
	encoded, err := yaml.YAMLToJSON(specification)
	if err != nil {
		return nil, fmt.Errorf("convert OpenAPI YAML: %w", err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, fmt.Errorf("decode OpenAPI: %w", err)
	}
	operations := make(map[string]compatibilityEndpoint)
	for path, item := range document.Paths {
		for method, operation := range item {
			if !isHTTPMethod(method) {
				continue
			}
			if operation.OperationID == "" {
				return nil, fmt.Errorf("%s %s has no operationId", method, path)
			}
			if _, exists := operations[operation.OperationID]; exists {
				return nil, fmt.Errorf("duplicate OpenAPI operationId %q", operation.OperationID)
			}
			operations[operation.OperationID] = compatibilityEndpoint{
				Method: strings.ToLower(method), Path: path,
			}
		}
	}
	if len(operations) == 0 {
		return nil, errors.New("OpenAPI contains no operations")
	}
	return operations, nil
}

func (surface compatibilitySurface) validate(legacy map[string]compatibilityEndpoint) error {
	if surface.SchemaVersion != compatibilitySurfaceSchemaVersion {
		return fmt.Errorf("compatibility surface schema_version = %d, want %d", surface.SchemaVersion, compatibilitySurfaceSchemaVersion)
	}
	if surface.Upstream.Repository != "oceanbase/powercontext" ||
		surface.Upstream.Commit != "e4ebdcdff64a9793aa30f5d087cc71cd7e9ba87c" {
		return fmt.Errorf("unexpected upstream snapshot %q at %q", surface.Upstream.Repository, surface.Upstream.Commit)
	}
	if surface.Canonical.OperationCount != canonicalOperationCount {
		return fmt.Errorf("canonical operation count = %d, want %d", surface.Canonical.OperationCount, canonicalOperationCount)
	}

	baseline, err := validateLegacyBaseline(surface.Legacy)
	if err != nil {
		return err
	}
	staged, err := validateStagedOperations(surface.Canonical.UpstreamOnlyOperations, baseline)
	if err != nil {
		return err
	}
	if legacyErr := validateLegacyOperations(legacy, baseline, staged); legacyErr != nil {
		return legacyErr
	}
	retained, err := validateRetainedExtensions(surface.RetainedGoExtensions, legacy)
	if err != nil {
		return err
	}
	if migrationErr := validateMethodMigrations(surface.MethodMigrations, legacy, staged.All); migrationErr != nil {
		return migrationErr
	}
	canonical, err := surface.projectCanonical(legacy)
	if err != nil {
		return err
	}
	if len(canonical) != surface.Canonical.OperationCount {
		return fmt.Errorf("canonical operation count = %d, want %d", len(canonical), surface.Canonical.OperationCount)
	}
	if err := validateDistinctEndpoints(canonical); err != nil {
		return err
	}
	if len(retained) != retainedExtensionCount {
		return fmt.Errorf("retained Go extensions = %d, want %d", len(retained), retainedExtensionCount)
	}
	return validateGeneratedOpenAPITools(surface.MCPGeneratedOpenAPIOperations, legacy)
}

func validateLegacyBaseline(legacy compatibilityLegacy) (map[string]compatibilityEndpoint, error) {
	if legacy.BaselineOperationCount != legacyBaselineOperationCount ||
		len(legacy.BaselineOperations) != legacyBaselineOperationCount {
		return nil, fmt.Errorf("legacy baseline operation count = %d/%d, want %d",
			legacy.BaselineOperationCount, len(legacy.BaselineOperations), legacyBaselineOperationCount)
	}
	operations := make(map[string]compatibilityEndpoint, len(legacy.BaselineOperations))
	for _, operation := range legacy.BaselineOperations {
		if err := validateOperation(operation); err != nil {
			return nil, fmt.Errorf("legacy baseline operation: %w", err)
		}
		if _, found := operations[operation.OperationID]; found {
			return nil, fmt.Errorf("duplicate legacy baseline operation %q", operation.OperationID)
		}
		operations[operation.OperationID] = compatibilityEndpoint{Method: operation.Method, Path: operation.Path}
	}
	return operations, nil
}

func validateStagedOperations(
	entries []compatibilityStagedOperation,
	baseline map[string]compatibilityEndpoint,
) (compatibilityStagedOperations, error) {
	if len(entries) != upstreamOnlyOperationCount {
		return compatibilityStagedOperations{}, fmt.Errorf("upstream-only operations = %d, want %d", len(entries), upstreamOnlyOperationCount)
	}
	staged := compatibilityStagedOperations{
		All: make(map[string]compatibilityOperation, len(entries)),
	}
	for _, entry := range entries {
		operation := compatibilityOperation{OperationID: entry.OperationID, Method: entry.Method, Path: entry.Path}
		if err := validateOperation(operation); err != nil {
			return compatibilityStagedOperations{}, fmt.Errorf("upstream-only operation: %w", err)
		}
		if _, found := staged.All[operation.OperationID]; found {
			return compatibilityStagedOperations{}, fmt.Errorf("duplicate upstream-only operation %q", operation.OperationID)
		}
		if _, found := baseline[operation.OperationID]; found {
			return compatibilityStagedOperations{}, fmt.Errorf("staged operation %q already exists in the legacy baseline", operation.OperationID)
		}
		staged.All[operation.OperationID] = operation
		switch entry.Status {
		case compatibilityStatusDeferred, compatibilityStatusImplementedCanonical:
		default:
			return compatibilityStagedOperations{}, fmt.Errorf("upstream-only operation %q has unknown status %q", operation.OperationID, entry.Status)
		}
	}
	return staged, nil
}

func validateLegacyOperations(
	legacy, expected map[string]compatibilityEndpoint,
	staged compatibilityStagedOperations,
) error {
	for operationID := range staged.All {
		if _, found := legacy[operationID]; found {
			return fmt.Errorf("upstream-only operation %q is present in frozen legacy OpenAPI", operationID)
		}
	}
	if len(legacy) != len(expected) {
		return fmt.Errorf("legacy operation count = %d, want %d", len(legacy), len(expected))
	}
	for operationID, endpoint := range expected {
		if legacy[operationID] != endpoint {
			return fmt.Errorf("legacy operation %q endpoint = %#v, want %#v", operationID, legacy[operationID], endpoint)
		}
	}
	return nil
}

func (surface compatibilitySurface) projectCanonical(
	legacy map[string]compatibilityEndpoint,
) (map[string]compatibilityEndpoint, error) {
	baseline, err := validateLegacyBaseline(surface.Legacy)
	if err != nil {
		return nil, err
	}
	staged, err := validateStagedOperations(surface.Canonical.UpstreamOnlyOperations, baseline)
	if err != nil {
		return nil, err
	}
	retained, err := validateRetainedExtensions(surface.RetainedGoExtensions, legacy)
	if err != nil {
		return nil, err
	}
	if err := validateMethodMigrations(surface.MethodMigrations, legacy, staged.All); err != nil {
		return nil, err
	}
	canonical := maps.Clone(legacy)
	for operationID := range retained {
		delete(canonical, operationID)
	}
	migration := surface.MethodMigrations[0]
	canonical[migration.OperationID] = migration.Canonical
	for operationID, operation := range staged.All {
		canonical[operationID] = compatibilityEndpoint{Method: operation.Method, Path: operation.Path}
	}
	return canonical, nil
}

func validateDistinctEndpoints(operations map[string]compatibilityEndpoint) error {
	seen := make(map[compatibilityEndpoint]string, len(operations))
	for operationID, endpoint := range operations {
		if previous, found := seen[endpoint]; found {
			return fmt.Errorf("canonical operations %q and %q share endpoint %s %s", previous, operationID, endpoint.Method, endpoint.Path)
		}
		seen[endpoint] = operationID
	}
	return nil
}

func validateRetainedExtensions(
	entries []compatibilityOperation,
	legacy map[string]compatibilityEndpoint,
) (map[string]struct{}, error) {
	if len(entries) != retainedExtensionCount {
		return nil, fmt.Errorf("retained Go extensions = %d, want %d", len(entries), retainedExtensionCount)
	}
	result := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := validateOperation(entry); err != nil {
			return nil, fmt.Errorf("retained Go extension: %w", err)
		}
		if _, exists := result[entry.OperationID]; exists {
			return nil, fmt.Errorf("duplicate retained Go extension %q", entry.OperationID)
		}
		endpoint, found := legacy[entry.OperationID]
		if !found || endpoint != (compatibilityEndpoint{Method: entry.Method, Path: entry.Path}) {
			return nil, fmt.Errorf("retained Go extension %q does not match legacy OpenAPI", entry.OperationID)
		}
		if !strings.HasPrefix(entry.Path, "/v1/handoff-reports/") {
			return nil, fmt.Errorf("retained Go extension %q is outside Handoff Report", entry.OperationID)
		}
		result[entry.OperationID] = struct{}{}
	}
	return result, nil
}

func validateMethodMigrations(
	entries []compatibilityMigration,
	legacy map[string]compatibilityEndpoint,
	staged map[string]compatibilityOperation,
) error {
	if len(entries) != 1 {
		return fmt.Errorf("method migrations = %d, want 1", len(entries))
	}
	migration := entries[0]
	if migration.OperationID != "get_stats" || migration.Legacy != (compatibilityEndpoint{Method: "get", Path: "/v1/stats"}) ||
		migration.Canonical != (compatibilityEndpoint{Method: "post", Path: "/v1/stats"}) ||
		migration.CanonicalMethodName != "GetStatsCanonical" {
		return errors.New("get_stats migration must reserve POST /v1/stats as GetStatsCanonical")
	}
	if legacy[migration.OperationID] != migration.Legacy {
		return errors.New("legacy GET /v1/stats is not preserved")
	}
	if _, found := staged[migration.OperationID]; found {
		return errors.New("get_stats migration cannot be upstream-only")
	}
	if migration.CanonicalMethodName == operationMethodName(migration.OperationID) {
		return errors.New("canonical get_stats method collides with legacy generated method")
	}
	return nil
}

func validateGeneratedOpenAPITools(
	operations []string,
	legacy map[string]compatibilityEndpoint,
) error {
	if len(operations) != explicitMCPToolCount || len(generatedOpenAPIToolAllowlist) != explicitMCPToolCount {
		return fmt.Errorf("generated OpenAPI MCP tools = %d, want %d", len(operations), explicitMCPToolCount)
	}
	seen := make(map[string]struct{}, len(operations))
	for _, operationID := range operations {
		if _, exists := seen[operationID]; exists {
			return fmt.Errorf("duplicate generated OpenAPI MCP tool %q", operationID)
		}
		if _, found := legacy[operationID]; !found {
			return fmt.Errorf("generated OpenAPI MCP tool %q is absent from the implemented legacy contract", operationID)
		}
		if _, allowed := generatedOpenAPIToolAllowlist[operationID]; !allowed {
			return fmt.Errorf("generated OpenAPI MCP tool %q is outside the curated legacy set", operationID)
		}
		seen[operationID] = struct{}{}
	}
	for operationID := range generatedOpenAPIToolAllowlist {
		if _, found := seen[operationID]; !found {
			return fmt.Errorf("curated generated OpenAPI MCP tool %q is absent", operationID)
		}
	}
	return nil
}

func validateOperation(entry compatibilityOperation) error {
	if entry.OperationID == "" || entry.Path == "" || !strings.HasPrefix(entry.Path, "/") || !isHTTPMethod(entry.Method) {
		return fmt.Errorf("invalid %q %q %q", entry.OperationID, entry.Method, entry.Path)
	}
	return nil
}
