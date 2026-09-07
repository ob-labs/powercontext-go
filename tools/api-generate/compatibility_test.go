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
		surface.Legacy.OperationCount != ledger.OperationCounts.Common+ledger.OperationCounts.GoOnly {
		t.Fatalf("compatibility surface does not match pinned ledger: %#v", surface)
	}
	if !sameCompatibilityOperations(surface.Canonical.UpstreamOnlyOperations, ledger.UpstreamOnly) ||
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
		strings.Replace(string(contents), `"schema_version": 1,`, `"schema_version": 1, "unknown": true,`, 1),
		strings.Replace(string(contents), `"schema_version": 1,`, `"schema_version": 2, "schema_version": 1,`, 1),
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
