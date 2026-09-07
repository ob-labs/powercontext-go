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
	"fmt"
	"os"
	"strings"
	"testing"
)

type upstreamCompatibilityLedger struct {
	Upstream        compatibilityUpstream `json:"upstream"`
	OperationCounts struct {
		Common       int `json:"common"`
		UpstreamOnly int `json:"upstream_only"`
		GoOnly       int `json:"go_only"`
	} `json:"operation_counts"`
	MethodMigrations []struct {
		OperationID string                `json:"operation_id"`
		Go          compatibilityEndpoint `json:"go"`
		Upstream    compatibilityEndpoint `json:"upstream"`
	} `json:"method_migrations"`
	UpstreamOnly []compatibilityOperation `json:"upstream_only"`
	GoOnly       []compatibilityOperation `json:"go_only"`
}

func TestCompatibilitySurfaceProtectsLegacyContract(t *testing.T) {
	t.Parallel()
	specification, err := os.ReadFile("../../openapi/powercontext.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile("../../openapi/compatibility-surface.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCompatibilitySurface(specification, contents); err != nil {
		t.Fatal(err)
	}
}

func TestCompatibilitySurfaceSeparatesCanonicalImplementationFromLegacy(t *testing.T) {
	t.Parallel()
	specification, err := os.ReadFile("../../openapi/powercontext.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile("../../openapi/compatibility-surface.json")
	if err != nil {
		t.Fatal(err)
	}
	surface, err := decodeCompatibilitySurface(contents)
	if err != nil {
		t.Fatal(err)
	}
	index := stagedOperationIndex(t, surface, "create_scope")
	entry := surface.Canonical.UpstreamOnlyOperations[index]
	surface.Canonical.UpstreamOnlyOperations[index].Status = compatibilityStatusImplementedCanonical
	canonicalContents, err := json.Marshal(surface)
	if err != nil {
		t.Fatal(err)
	}
	if validationErr := validateCompatibilitySurface(specification, canonicalContents); validationErr != nil {
		t.Fatalf("canonical implementation without a legacy route was rejected: %v", validationErr)
	}
	legacy, err := parseOpenAPIOperations(specification)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := surface.projectCanonical(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != canonicalOperationCount {
		t.Fatalf("canonical operation count = %d, want %d", len(canonical), canonicalOperationCount)
	}
	if canonical[entry.OperationID] != (compatibilityEndpoint{Method: entry.Method, Path: entry.Path}) {
		t.Fatalf("implemented canonical operation = %#v", canonical[entry.OperationID])
	}
	if canonical["get_stats"] != (compatibilityEndpoint{Method: "post", Path: "/v1/stats"}) {
		t.Fatalf("canonical get_stats endpoint = %#v", canonical["get_stats"])
	}

	t.Run("rejects a canonical operation in frozen legacy", func(t *testing.T) {
		implementedSpecification := addSyntheticOpenAPIOperation(t, specification, entry)
		if err := validateCompatibilitySurface(implementedSpecification, canonicalContents); err == nil {
			t.Fatal("canonical operation present in frozen legacy OpenAPI was accepted")
		}
	})
	t.Run("rejects an implemented scope operation as a generated MCP tool", func(t *testing.T) {
		mcpMutant, cloneErr := cloneCompatibilitySurface(surface)
		if cloneErr != nil {
			t.Fatal(cloneErr)
		}
		mcpMutant.MCPGeneratedOpenAPIOperations[0] = entry.OperationID
		mcpContents, marshalErr := json.Marshal(mcpMutant)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := validateCompatibilitySurface(specification, mcpContents); err == nil {
			t.Fatal("implemented scope operation was accepted as a generated MCP tool")
		}
	})
	t.Run("rejects an unknown staged status", func(t *testing.T) {
		unknown, cloneErr := cloneCompatibilitySurface(surface)
		if cloneErr != nil {
			t.Fatal(cloneErr)
		}
		unknown.Canonical.UpstreamOnlyOperations[index].Status = "unknown"
		unknownContents, marshalErr := json.Marshal(unknown)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if err := validateCompatibilitySurface(specification, unknownContents); err == nil {
			t.Fatal("unknown staged status was accepted")
		}
	})
}

func stagedOperationIndex(t *testing.T, surface compatibilitySurface, operationID string) int {
	t.Helper()
	for index, operation := range surface.Canonical.UpstreamOnlyOperations {
		if operation.OperationID == operationID {
			return index
		}
	}
	t.Fatalf("missing staged operation %q", operationID)
	return 0
}

func addSyntheticOpenAPIOperation(
	t *testing.T,
	specification []byte,
	operation compatibilityStagedOperation,
) []byte {
	t.Helper()
	paths, components, found := strings.Cut(string(specification), "components:\n")
	if !found {
		t.Fatal("OpenAPI specification does not contain components")
	}
	return []byte(fmt.Sprintf("%s  %s:\n    %s:\n      operationId: %s\ncomponents:\n%s",
		paths, operation.Path, operation.Method, operation.OperationID, components))
}

func TestCompatibilitySurfaceMatchesPinnedUpstreamLedger(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../openapi/compatibility-surface.json")
	if err != nil {
		t.Fatal(err)
	}
	surface, err := decodeCompatibilitySurface(contents)
	if err != nil {
		t.Fatal(err)
	}
	ledgerContents, err := os.ReadFile("../../test/conformance/upstream-master-openapi-ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	var ledger upstreamCompatibilityLedger
	if err := json.Unmarshal(ledgerContents, &ledger); err != nil {
		t.Fatal(err)
	}
	if surface.Upstream != ledger.Upstream || surface.Canonical.OperationCount != ledger.OperationCounts.Common+ledger.OperationCounts.UpstreamOnly ||
		surface.Legacy.BaselineOperationCount != ledger.OperationCounts.Common+ledger.OperationCounts.GoOnly ||
		len(surface.Legacy.BaselineOperations) != ledger.OperationCounts.Common+ledger.OperationCounts.GoOnly {
		t.Fatalf("compatibility surface does not match pinned ledger: %#v", surface)
	}
	if !sameCompatibilityOperations(stagedCompatibilityOperations(surface.Canonical.UpstreamOnlyOperations), ledger.UpstreamOnly) ||
		!sameCompatibilityOperations(surface.RetainedGoExtensions, ledger.GoOnly) {
		t.Fatal("compatibility operation inventory does not match pinned ledger")
	}
	if len(surface.MethodMigrations) != 1 || len(ledger.MethodMigrations) != 1 ||
		surface.MethodMigrations[0].OperationID != ledger.MethodMigrations[0].OperationID ||
		surface.MethodMigrations[0].Legacy != ledger.MethodMigrations[0].Go ||
		surface.MethodMigrations[0].Canonical != ledger.MethodMigrations[0].Upstream {
		t.Fatal("compatibility get_stats migration does not match pinned ledger")
	}
}

func TestCompatibilitySurfaceRejectsAmbiguousJSON(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../openapi/compatibility-surface.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, mutant := range []string{
		strings.Replace(string(contents), `"schema_version": 2,`, `"schema_version": 2, "unknown": true,`, 1),
		strings.Replace(string(contents), `"schema_version": 2,`, `"schema_version": 1, "schema_version": 2,`, 1),
	} {
		if _, err := decodeCompatibilitySurface([]byte(mutant)); err == nil {
			t.Fatal("accepted ambiguous compatibility surface JSON")
		}
	}
}

func TestCompatibilitySurfaceRejectsContractMutants(t *testing.T) {
	t.Parallel()
	specification, err := os.ReadFile("../../openapi/powercontext.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile("../../openapi/compatibility-surface.json")
	if err != nil {
		t.Fatal(err)
	}
	surface, err := decodeCompatibilitySurface(contents)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*compatibilitySurface, *[]byte)
	}{
		{
			name: "missing retained extension",
			mutate: func(surface *compatibilitySurface, _ *[]byte) {
				surface.RetainedGoExtensions = surface.RetainedGoExtensions[1:]
			},
		},
		{
			name: "wrong canonical operation count",
			mutate: func(surface *compatibilitySurface, _ *[]byte) {
				surface.Canonical.OperationCount--
			},
		},
		{
			name: "canonical stats keeps legacy method name",
			mutate: func(surface *compatibilitySurface, _ *[]byte) {
				surface.MethodMigrations[0].CanonicalMethodName = "GetStats"
			},
		},
		{
			name: "legacy replaces a baseline operation",
			mutate: func(_ *compatibilitySurface, specification *[]byte) {
				before := string(*specification)
				after := strings.Replace(before, "operationId: get_liveness", "operationId: unexpected_liveness", 1)
				if before == after {
					*specification = []byte("not: OpenAPI")
					return
				}
				*specification = []byte(after)
			},
		},
		{
			name: "unimplemented canonical generated MCP tool",
			mutate: func(surface *compatibilitySurface, _ *[]byte) {
				surface.MCPGeneratedOpenAPIOperations = append(surface.MCPGeneratedOpenAPIOperations, "create_scope")
			},
		},
		{
			name: "stats stops preserving GET legacy endpoint",
			mutate: func(_ *compatibilitySurface, specification *[]byte) {
				before := string(*specification)
				after := strings.Replace(before, "  /v1/stats:\n    get:", "  /v1/stats:\n    post:", 1)
				if before == after {
					*specification = []byte("not: OpenAPI")
					return
				}
				*specification = []byte(after)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clone, cloneErr := cloneCompatibilitySurface(surface)
			if cloneErr != nil {
				t.Fatal(cloneErr)
			}
			mutantSpecification := append([]byte(nil), specification...)
			test.mutate(&clone, &mutantSpecification)
			mutantContents, marshalErr := json.Marshal(clone)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if err := validateCompatibilitySurface(mutantSpecification, mutantContents); err == nil {
				t.Fatal("invalid compatibility surface was accepted")
			}
		})
	}
}

func sameCompatibilityOperations(left, right []compatibilityOperation) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]compatibilityEndpoint, len(left))
	for _, operation := range left {
		values[operation.OperationID] = compatibilityEndpoint{Method: operation.Method, Path: operation.Path}
	}
	if len(values) != len(left) {
		return false
	}
	for _, operation := range right {
		if values[operation.OperationID] != (compatibilityEndpoint{Method: operation.Method, Path: operation.Path}) {
			return false
		}
	}
	return true
}

func stagedCompatibilityOperations(entries []compatibilityStagedOperation) []compatibilityOperation {
	operations := make([]compatibilityOperation, len(entries))
	for index, entry := range entries {
		operations[index] = compatibilityOperation{
			OperationID: entry.OperationID,
			Method:      entry.Method,
			Path:        entry.Path,
		}
	}
	return operations
}
