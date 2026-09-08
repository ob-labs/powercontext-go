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
	"bytes"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestSourceSidecarProjectionAndIntegrity(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	upstream, _, legacy, compatibility := readScopeSidecarInputs(t, root)
	manifestPath := filepath.Join(root, "openapi", "canonical", "sources-manifest.json")
	manifest, err := loadSourceSidecarManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := projectSourceSidecar(upstream, manifest, legacy, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := parseOpenAPIOperations(projected)
	if err != nil {
		t.Fatal(err)
	}
	wantOperations := map[string]compatibilityEndpoint{
		"create_source":               {Method: "post", Path: "/v1/scopes/{scope_id}/sources"},
		"get_source":                  {Method: "get", Path: "/v1/scopes/{scope_id}/sources/{source_type}/{source_id}"},
		"register_source_definition":  {Method: "post", Path: "/v1/source-definitions/register"},
		"submit_source_observation":   {Method: "post", Path: "/v1/source-observations"},
		"get_connector_checkpoint":    {Method: "post", Path: "/v1/connector-checkpoints/get"},
		"commit_connector_checkpoint": {Method: "post", Path: "/v1/connector-checkpoints/commit"},
	}
	if !reflect.DeepEqual(operations, wantOperations) {
		t.Fatalf("operations = %#v", operations)
	}
	var projectedDocument struct {
		Components struct{ Schemas map[string]any }
	}
	if decodeErr := json.Unmarshal(projected, &projectedDocument, json.MatchCaseInsensitiveNames(true)); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	wantSchemas := []string{
		"CreateSourceRequest", "SourceRecord", "ErrorDetail", "ErrorResponse",
		"RegisterSourceDefinitionRequest", "SourceDefinitionManifest", "SourceProjectionManifest", "SourceProjectionKey",
		"SubmitSourceObservationRequest", "SourceObservation", "SourceProjectionValue", "SourceObservationReceipt", "SourceReference",
		"GetConnectorCheckpointRequest", "ConnectorBinding", "ConnectorCheckpointState", "CommitConnectorCheckpointRequest",
	}
	if len(projectedDocument.Components.Schemas) != len(wantSchemas) {
		t.Fatalf("projected schemas = %#v", projectedDocument.Components.Schemas)
	}
	for _, name := range wantSchemas {
		if _, found := projectedDocument.Components.Schemas[name]; !found {
			t.Fatalf("missing schema %s", name)
		}
	}
	rawDocument, rawErr := decodeScopeSidecarDocument(upstream)
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	projectionDocument, projectionErr := decodeScopeSidecarDocument(projected)
	if projectionErr != nil {
		t.Fatal(projectionErr)
	}
	// Every selected operation and reachable component must retain its raw schema,
	// including opaque JSON and the checkpoint Get response's absent 404 variant.
	for operationID, endpoint := range wantOperations {
		rawPath := rawDocument["paths"].(map[string]any)[endpoint.Path].(map[string]any)
		projectedPath := projectionDocument["paths"].(map[string]any)[endpoint.Path].(map[string]any)
		if !reflect.DeepEqual(rawPath[endpoint.Method], projectedPath[endpoint.Method]) {
			t.Fatalf("operation %s differs from the pinned raw schema", operationID)
		}
	}
	for category, value := range projectionDocument["components"].(map[string]any) {
		rawComponents := rawDocument["components"].(map[string]any)[category].(map[string]any)
		for name, component := range value.(map[string]any) {
			if !reflect.DeepEqual(component, rawComponents[name]) {
				t.Fatalf("component %s/%s differs from the pinned raw schema", category, name)
			}
		}
	}
	committed, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "sources.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(projected, committed) {
		t.Fatal("Source projection is stale")
	}
	for name, mutate := range map[string]func(*sourceSidecarManifest){
		"pin":    func(m *sourceSidecarManifest) { m.Upstream.Commit = "main" },
		"digest": func(m *sourceSidecarManifest) { m.Upstream.SHA256 = "bad" },
		"extra operation": func(m *sourceSidecarManifest) {
			m.Operations = append(m.Operations, scopeSidecarOperation{OperationID: "list_sources"})
		},
		"route":             func(m *sourceSidecarManifest) { m.Operations[0].Path = "/v1/source" },
		"ingestion route":   func(m *sourceSidecarManifest) { m.Operations[2].Path = "/v1/source-definitions" },
		"checkpoint method": func(m *sourceSidecarManifest) { m.Operations[4].Method = "get" },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var mutant sourceSidecarManifest
			if err := json.Unmarshal(encoded, &mutant); err != nil {
				t.Fatal(err)
			}
			mutate(&mutant)
			if _, err := projectSourceSidecar(upstream, mutant, legacy, compatibility); err == nil {
				t.Fatal("invalid projection accepted")
			}
		})
	}
	if _, digestErr := projectSourceSidecar(append(slices.Clone(upstream), '\n'), manifest, legacy, compatibility); digestErr == nil {
		t.Fatal("changed raw upstream bytes accepted")
	}
}

func TestSourceSidecarRejectsSelectedIngestionLedgerDrift(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	upstream, _, legacy, compatibility := readScopeSidecarInputs(t, root)
	manifest, err := loadSourceSidecarManifest(filepath.Join(root, "openapi", "canonical", "sources-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, projectErr := projectSourceSidecar(upstream, manifest, legacy, compatibility); projectErr != nil {
		t.Fatal(projectErr)
	}
	for _, operationID := range []string{"register_source_definition", "submit_source_observation", "get_connector_checkpoint", "commit_connector_checkpoint"} {
		for name, mutate := range map[string]func(*compatibilityStagedOperation){
			"deferred":     func(entry *compatibilityStagedOperation) { entry.Status = compatibilityStatusDeferred },
			"method":       func(entry *compatibilityStagedOperation) { entry.Method = "get" },
			"path":         func(entry *compatibilityStagedOperation) { entry.Path += "/wrong" },
			"operation ID": func(entry *compatibilityStagedOperation) { entry.OperationID += "_wrong" },
		} {
			t.Run(operationID+"/"+name, func(t *testing.T) {
				ledger, decodeErr := decodeCompatibilitySurface(compatibility)
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				index := slices.IndexFunc(ledger.Canonical.UpstreamOnlyOperations, func(entry compatibilityStagedOperation) bool { return entry.OperationID == operationID })
				if index < 0 || ledger.Canonical.UpstreamOnlyOperations[index].Status != compatibilityStatusImplementedCanonical {
					t.Fatalf("%s is not implemented-canonical", operationID)
				}
				mutate(&ledger.Canonical.UpstreamOnlyOperations[index])
				encoded, encodeErr := json.Marshal(ledger)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				if _, projectErr := projectSourceSidecar(upstream, manifest, legacy, encoded); projectErr == nil {
					t.Fatal("selected operation ledger drift accepted")
				}
			})
		}
	}
}

func TestSourceSidecarRejectsUnknownManifestFields(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	valid, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "sources-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	for _, prefix := range []string{`{"extra":true,`, `{"schema_version":1,`} {
		if err := os.WriteFile(path, append([]byte(prefix), valid[1:]...), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSourceSidecarManifest(path); err == nil {
			t.Fatal("unknown or duplicate manifest field accepted")
		}
	}
}

func TestSourceSidecarGenerationIsIsolatedAndFreshConsumerBuilds(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	upstream, _, legacy, compatibility := scopeSidecarPaths(root)
	fixture := t.TempDir()
	manifestContents, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "sources-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(fixture, "sources-manifest.json")
	if writeErr := os.WriteFile(manifest, manifestContents, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	target := filepath.Join(fixture, "api", "canonical", "sources")
	frozen := []string{legacy, filepath.Join(root, "client", "invoker_gen.go"), filepath.Join(root, "internal", "mcpapi", "schemas_gen.go")}
	legacyEntries, err := os.ReadDir(filepath.Join(root, "api", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range legacyEntries {
		if !entry.IsDir() {
			frozen = append(frozen, filepath.Join(root, "api", "v1", entry.Name()))
		}
	}
	before := readArtifacts(t, frozen)
	if generationErr := runSourceSidecar(upstream, manifest, target, "sources", "", compatibility, legacy); generationErr != nil {
		t.Fatal(generationErr)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	actualOutputs := []string{"openapi/canonical/sources.json"}
	for _, entry := range entries {
		actualOutputs = append(actualOutputs, "api/canonical/sources/"+entry.Name())
	}
	inventoryContents, inventoryErr := os.ReadFile(filepath.Join(root, "test", "generator-inventory.json"))
	if inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	var inventory struct {
		Generators []struct {
			Name    string
			Outputs []string
		}
	}
	if decodeErr := json.Unmarshal(inventoryContents, &inventory, json.MatchCaseInsensitiveNames(true)); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	var declaredOutputs []string
	for _, entry := range inventory.Generators {
		if entry.Name == "canonical-source-sidecar" {
			declaredOutputs = entry.Outputs
		}
	}
	if !slices.Equal(slices.Sorted(slices.Values(actualOutputs)), slices.Sorted(slices.Values(declaredOutputs))) {
		t.Fatalf("Source generator inventory = %v, actual output = %v", declaredOutputs, actualOutputs)
	}
	for _, entry := range entries {
		generated, generatedErr := os.ReadFile(filepath.Join(target, entry.Name()))
		if generatedErr != nil {
			t.Fatal(generatedErr)
		}
		golden, goldenErr := os.ReadFile(filepath.Join(root, "api", "canonical", "sources", entry.Name()))
		if goldenErr != nil {
			t.Fatal(goldenErr)
		}
		if !bytes.Equal(generated, golden) {
			t.Fatalf("generated Source artifact is stale: %s", entry.Name())
		}
	}
	for _, tc := range []struct{ target, pkg, invoker string }{
		{filepath.Join(fixture, "api", "v1"), "v1", ""},
		{target, "sources", filepath.Join(fixture, "invoker.go")},
	} {
		if generationErr := runSourceSidecar(upstream, manifest, tc.target, tc.pkg, tc.invoker, compatibility, legacy); generationErr == nil {
			t.Fatal("legacy output accepted")
		}
	}
	for path, want := range before {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("Source generation modified frozen artifact %s", path)
		}
	}
	contents, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	locked, err := modfile.Parse("go.mod", contents, nil)
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := modfile.Parse("go.mod", []byte("module example.com/source-consumer\n\ngo 1.27.0\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range locked.Require {
		if requireErr := consumer.AddRequire(requirement.Mod.Path, requirement.Mod.Version); requireErr != nil {
			t.Fatal(requireErr)
		}
	}
	encoded, err := consumer.Format()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "go.mod"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	test := `package sourceconsumer
import (
 "testing"
 sources "example.com/source-consumer/api/canonical/sources"
)
var _ sources.Handler = sources.UnimplementedHandler{}
var _ sources.Invoker = (*sources.Client)(nil)
var (
 _ = (*sources.Client).RegisterSourceDefinition
 _ = (*sources.Client).SubmitSourceObservation
 _ = (*sources.Client).GetConnectorCheckpoint
 _ = (*sources.Client).CommitConnectorCheckpoint
)
func TestConsumer(t *testing.T) {
 request:=sources.CreateSourceRequest{Content:[]byte("null")}
 if err:=request.Validate();err!=nil{t.Fatal(err)}
}
`
	if err := os.WriteFile(filepath.Join(fixture, "consumer_test.go"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"mod", "tidy"}, {"mod", "verify"}, {"test", "-count=1", "./..."}} {
		command := exec.CommandContext(t.Context(), "go", arguments...)
		command.Dir = fixture
		command.Env = append(os.Environ(), "GOWORK=off")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fresh Source consumer %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
		t.Logf("fresh Source consumer %s: %s", strings.Join(arguments, " "), output)
	}
}
