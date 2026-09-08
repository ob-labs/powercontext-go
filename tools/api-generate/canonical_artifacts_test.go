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
	"slices"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestArtifactSidecarProjectionAndPolicy(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	upstream, _, legacy, compatibility := readScopeSidecarInputs(t, root)
	path := filepath.Join(root, "openapi", "canonical", "artifacts-manifest.json")
	manifest, err := loadArtifactSidecarManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := projectArtifactSidecar(upstream, manifest, legacy, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	document, err := decodeScopeSidecarDocument(projected)
	if err != nil {
		t.Fatal(err)
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	wantOperations := []scopeSidecarOperation{
		{OperationID: "list_artifacts", Method: "get", Path: "/v1/scopes/{scope_id}/artifacts/{family}"},
		{OperationID: "get_artifact", Method: "get", Path: "/v1/scopes/{scope_id}/artifacts/{family}/{artifact_id}"},
		{OperationID: "get_artifact_revision", Method: "get", Path: "/v1/scopes/{scope_id}/artifacts/{family}/{artifact_id}/revisions/{revision}"},
	}
	if !slices.Equal(manifest.Operations, wantOperations) {
		t.Fatalf("operations = %v", manifest.Operations)
	}
	paths := document["paths"].(map[string]any)
	if len(paths) != len(wantOperations) {
		t.Fatalf("paths = %v", paths)
	}
	for _, operation := range wantOperations {
		path, ok := paths[operation.Path].(map[string]any)
		if !ok {
			t.Fatalf("missing path %s", operation.Path)
		}
		projectedOperation, ok := path[operation.Method].(map[string]any)
		if !ok || projectedOperation["operationId"] != operation.OperationID {
			t.Fatalf("operation %s %s = %v", operation.Method, operation.Path, projectedOperation)
		}
	}
	wantSchemas := []string{"ArtifactCollectionItem", "ArtifactPage", "ArtifactReference", "ArtifactRevision", "BaseArtifactFamily", "ErrorDetail", "ErrorResponse", "SourceTypeReference"}
	if len(schemas) != len(wantSchemas) {
		t.Fatalf("schemas = %v", schemas)
	}
	for _, name := range wantSchemas {
		if _, ok := schemas[name]; !ok {
			t.Fatalf("missing %s", name)
		}
	}
	page := schemas["ArtifactPage"].(map[string]any)
	if !slices.Equal(page["required"].([]any), []any{"items", "next_cursor"}) {
		t.Fatalf("ArtifactPage required = %v", page["required"])
	}
	nextCursor := page["properties"].(map[string]any)["next_cursor"].(map[string]any)
	if nextCursor["nullable"] != true {
		t.Fatalf("ArtifactPage next_cursor = %v", nextCursor)
	}
	types := schemas["SourceTypeReference"].(map[string]any)["properties"].(map[string]any)["source_type"].(map[string]any)["enum"]
	encodedTypes, err := json.Marshal(types)
	if err != nil {
		t.Fatal(err)
	}
	if string(encodedTypes) != `["content","external-skill-snapshot","accepted-observation"]` {
		t.Fatalf("lineage enum = %s", encodedTypes)
	}
	for _, schemaName := range []string{"ArtifactRevision", "ArtifactCollectionItem"} {
		sourceType := schemas[schemaName].(map[string]any)["properties"].(map[string]any)["sources"].(map[string]any)["items"].(map[string]any)["$ref"]
		if sourceType != "#/components/schemas/SourceTypeReference" {
			t.Fatalf("%s source lineage = %v", schemaName, sourceType)
		}
	}
	committed, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "artifacts.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(projected, committed) {
		t.Fatal("Artifact projection is stale")
	}
	for name, mutate := range map[string]func(*artifactSidecarManifest){
		"pin":    func(m *artifactSidecarManifest) { m.Upstream.Commit = "main" },
		"digest": func(m *artifactSidecarManifest) { m.Upstream.SHA256 = strings.Repeat("0", 64) },
		"extra operation": func(m *artifactSidecarManifest) {
			m.Operations = append(m.Operations, scopeSidecarOperation{OperationID: "replace_artifact"})
		},
		"wrong route":           func(m *artifactSidecarManifest) { m.Operations[0].Path = "/v1/artifacts" },
		"overlay missing type":  func(m *artifactSidecarManifest) { m.Overlays.SourceTypes = m.Overlays.SourceTypes[:2] },
		"overlay invented type": func(m *artifactSidecarManifest) { m.Overlays.SourceTypes[0] = "raw-observation" },
		"untracked policy":      func(m *artifactSidecarManifest) { m.Overlays.Policy = "untracked" },
	} {
		t.Run(name, func(t *testing.T) {
			encoded, marshalErr := json.Marshal(manifest)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var mutant artifactSidecarManifest
			if decodeErr := json.Unmarshal(encoded, &mutant); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			mutate(&mutant)
			if _, projectErr := projectArtifactSidecar(upstream, mutant, legacy, compatibility); projectErr == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	if _, projectionErr := projectArtifactSidecar(append(slices.Clone(upstream), '\n'), manifest, legacy, compatibility); projectionErr == nil {
		t.Fatal("changed upstream accepted")
	}
	ledger := bytes.Replace(compatibility, []byte(`"operation_id": "list_artifacts", "method": "get", "path": "/v1/scopes/{scope_id}/artifacts/{family}", "status": "implemented-canonical"`), []byte(`"operation_id": "list_artifacts", "method": "get", "path": "/v1/scopes/{scope_id}/artifacts/{family}", "status": "deferred"`), 1)
	if bytes.Equal(ledger, compatibility) {
		t.Fatal("ledger mutant did not change")
	}
	if _, projectionErr := projectArtifactSidecar(upstream, manifest, legacy, ledger); projectionErr == nil {
		t.Fatal("unimplemented ledger accepted")
	}
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	mutantPath := filepath.Join(t.TempDir(), "manifest.json")
	for _, prefix := range []string{`{"extra":true,`, `{"schema_version":1,`} {
		if writeErr := os.WriteFile(mutantPath, append([]byte(prefix), valid[1:]...), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, manifestErr := loadArtifactSidecarManifest(mutantPath); manifestErr == nil {
			t.Fatal("unknown or duplicate manifest field accepted")
		}
	}
}

func TestArtifactSidecarGenerationIsIsolatedAndFreshConsumerBuilds(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	upstream, _, legacy, compatibility := scopeSidecarPaths(root)
	fixture := t.TempDir()
	manifestContents, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "artifacts-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(fixture, "artifacts-manifest.json")
	if writeErr := os.WriteFile(manifest, manifestContents, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	target := filepath.Join(fixture, "api", "canonical", "artifacts")
	frozen := []string{legacy, filepath.Join(root, "client", "invoker_gen.go"), filepath.Join(root, "internal", "mcpapi", "schemas_gen.go")}
	for _, directory := range []string{"api/v1", "api/canonical/scopes", "api/canonical/sources"} {
		entries, readErr := os.ReadDir(filepath.Join(root, directory))
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				frozen = append(frozen, filepath.Join(root, directory, entry.Name()))
			}
		}
	}
	before := readArtifacts(t, frozen)
	if generationErr := runArtifactSidecar(upstream, manifest, target, "artifacts", "", compatibility, legacy); generationErr != nil {
		t.Fatal(generationErr)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	outputs := []string{"openapi/canonical/artifacts.json"}
	for _, entry := range entries {
		outputs = append(outputs, "api/canonical/artifacts/"+entry.Name())
		generated, readErr := os.ReadFile(filepath.Join(target, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		golden, readErr := os.ReadFile(filepath.Join(root, "api", "canonical", "artifacts", entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(generated, golden) {
			t.Fatalf("stale artifact: %s", entry.Name())
		}
	}
	inventoryContents, err := os.ReadFile(filepath.Join(root, "test", "generator-inventory.json"))
	if err != nil {
		t.Fatal(err)
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
	var declared []string
	for _, entry := range inventory.Generators {
		if entry.Name == "canonical-artifact-sidecar" {
			declared = entry.Outputs
		}
	}
	if !slices.Equal(slices.Sorted(slices.Values(outputs)), slices.Sorted(slices.Values(declared))) {
		t.Fatalf("declared = %v, outputs = %v", declared, outputs)
	}
	for _, tc := range []struct{ target, pkg, invoker string }{
		{filepath.Join(fixture, "api", "v1"), "v1", ""},
		{filepath.Join(fixture, "api", "canonical", "sources"), "artifacts", ""},
		{target, "artifacts", filepath.Join(fixture, "invoker.go")},
	} {
		if generationErr := runArtifactSidecar(upstream, manifest, tc.target, tc.pkg, tc.invoker, compatibility, legacy); generationErr == nil {
			t.Fatal("forbidden output accepted")
		}
	}
	for path, want := range before {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("changed frozen artifact %s", path)
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
	consumer, err := modfile.Parse("go.mod", []byte("module example.com/artifact-consumer\n\ngo 1.27.0\n"), nil)
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
	if writeErr := os.WriteFile(filepath.Join(fixture, "go.mod"), encoded, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	consumerTest := `package artifactconsumer
import (
 "context"
 "testing"
 artifacts "example.com/artifact-consumer/api/canonical/artifacts"
)
var _ artifacts.Handler = artifacts.UnimplementedHandler{}
var _ artifacts.Invoker = (*artifacts.Client)(nil)
var _ func(context.Context, artifacts.ListArtifactsParams) (artifacts.ListArtifactsRes, error) = artifacts.UnimplementedHandler{}.ListArtifacts
func TestConsumer(t *testing.T) {
 for _, sourceType := range []artifacts.SourceTypeReferenceSourceType{artifacts.SourceTypeReferenceSourceTypeContent, artifacts.SourceTypeReferenceSourceTypeExternalSkillSnapshot, artifacts.SourceTypeReferenceSourceTypeAcceptedObservation} {
  reference := artifacts.SourceTypeReference{SourceType:sourceType, SourceID:"source"}
  if err:=reference.Validate();err!=nil{t.Fatal(err)}
 }
}
`
	if writeErr := os.WriteFile(filepath.Join(fixture, "consumer_test.go"), []byte(consumerTest), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	for _, arguments := range [][]string{{"mod", "tidy"}, {"mod", "verify"}, {"test", "-count=1", "./..."}} {
		command := exec.CommandContext(t.Context(), "go", arguments...)
		command.Dir = fixture
		command.Env = append(os.Environ(), "GOWORK=off")
		output, runErr := command.CombinedOutput()
		if runErr != nil {
			t.Fatalf("fresh Artifact consumer %s: %v\n%s", strings.Join(arguments, " "), runErr, output)
		}
		t.Logf("fresh Artifact consumer %s: %s", strings.Join(arguments, " "), output)
	}
}
