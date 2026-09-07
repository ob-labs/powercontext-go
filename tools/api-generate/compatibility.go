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
	"strings"

	"github.com/ghodss/yaml"
)

const (
	legacyOperationCount       = 53
	canonicalOperationCount    = 77
	upstreamOnlyOperationCount = 38
	retainedExtensionCount     = 14
	explicitMCPToolCount       = 23
)

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
	OperationCount int `json:"operation_count"`
}

type compatibilityCanonical struct {
	OperationCount         int                      `json:"operation_count"`
	UpstreamOnlyOperations []compatibilityOperation `json:"upstream_only_operations"`
}

type compatibilityOperation struct {
	OperationID string `json:"operation_id"`
	Method      string `json:"method"`
	Path        string `json:"path"`
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
	if surface.SchemaVersion != 1 {
		return fmt.Errorf("compatibility surface schema_version = %d, want 1", surface.SchemaVersion)
	}
	if surface.Upstream.Repository != "oceanbase/powercontext" ||
		surface.Upstream.Commit != "74b961fbb07165595314726715d412a3d0d90589" {
		return fmt.Errorf("unexpected upstream snapshot %q at %q", surface.Upstream.Repository, surface.Upstream.Commit)
	}
	if surface.Legacy.OperationCount != legacyOperationCount || len(legacy) != legacyOperationCount {
		return fmt.Errorf("legacy operation count = %d/%d, want %d", surface.Legacy.OperationCount, len(legacy), legacyOperationCount)
	}
	if surface.Canonical.OperationCount != canonicalOperationCount {
		return fmt.Errorf("canonical operation count = %d, want %d", surface.Canonical.OperationCount, canonicalOperationCount)
	}

	retained, err := validateRetainedExtensions(surface.RetainedGoExtensions, legacy)
	if err != nil {
		return err
	}
	upstreamOnly, err := validateUpstreamOnlyOperations(surface.Canonical.UpstreamOnlyOperations, legacy)
	if err != nil {
		return err
	}
	if len(legacy)-len(retained)+len(upstreamOnly) != surface.Canonical.OperationCount {
		return fmt.Errorf("canonical operation calculation = %d, want %d", len(legacy)-len(retained)+len(upstreamOnly), surface.Canonical.OperationCount)
	}
	if err := validateMethodMigrations(surface.MethodMigrations, legacy, upstreamOnly); err != nil {
		return err
	}
	return validateGeneratedOpenAPITools(surface.MCPGeneratedOpenAPIOperations, legacy, upstreamOnly)
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

func validateUpstreamOnlyOperations(
	entries []compatibilityOperation,
	legacy map[string]compatibilityEndpoint,
) (map[string]struct{}, error) {
	if len(entries) != upstreamOnlyOperationCount {
		return nil, fmt.Errorf("upstream-only operations = %d, want %d", len(entries), upstreamOnlyOperationCount)
	}
	result := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := validateOperation(entry); err != nil {
			return nil, fmt.Errorf("upstream-only operation: %w", err)
		}
		if _, exists := result[entry.OperationID]; exists {
			return nil, fmt.Errorf("duplicate upstream-only operation %q", entry.OperationID)
		}
		if _, exists := legacy[entry.OperationID]; exists {
			return nil, fmt.Errorf("upstream-only operation %q already exists in legacy OpenAPI", entry.OperationID)
		}
		result[entry.OperationID] = struct{}{}
	}
	return result, nil
}

func validateMethodMigrations(
	entries []compatibilityMigration,
	legacy map[string]compatibilityEndpoint,
	upstreamOnly map[string]struct{},
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
	if _, found := upstreamOnly[migration.OperationID]; found {
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
	upstreamOnly map[string]struct{},
) error {
	if len(operations) != explicitMCPToolCount {
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
		if _, found := upstreamOnly[operationID]; found {
			return fmt.Errorf("generated OpenAPI MCP tool %q is not implemented", operationID)
		}
		seen[operationID] = struct{}{}
	}
	return nil
}

func validateOperation(entry compatibilityOperation) error {
	if entry.OperationID == "" || entry.Path == "" || !strings.HasPrefix(entry.Path, "/") || !isHTTPMethod(entry.Method) {
		return fmt.Errorf("invalid %q %q %q", entry.OperationID, entry.Method, entry.Path)
	}
	return nil
}
