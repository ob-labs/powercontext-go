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
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

func TestStatsSidecarProjectsOnlyCanonicalPost(t *testing.T) {
	t.Parallel()
	root := repositoryRootForScopeSidecarTest(t)
	upstream, _, legacy, compatibility := readScopeSidecarInputs(t, root)
	manifest, err := loadStatsSidecarManifest(filepath.Join(root, "openapi", "canonical", "stats-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	projected, err := projectStatsSidecar(upstream, manifest, legacy, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := parseOpenAPIOperations(projected)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]compatibilityEndpoint{"get_stats": {Method: "post", Path: "/v1/stats"}}
	if !sameCompatibilityOperations(mapCompatibilityOperations(operations), mapCompatibilityOperations(want)) {
		t.Fatalf("operations = %#v, want %#v", operations, want)
	}
	raw, err := decodeScopeSidecarDocument(upstream)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := decodeScopeSidecarDocument(projected)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := projection["paths"].(map[string]any)["/v1/stats"], raw["paths"].(map[string]any)["/v1/stats"]; !deepEqualSelectedPost(got, want) {
		t.Fatalf("stats projection differs from pinned raw schema")
	}
	committed, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "stats.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(projected, committed) {
		t.Fatal("Stats projection is stale")
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var deferred statsSidecarManifest
	if decodeErr := json.Unmarshal(encoded, &deferred); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	deferred.Operations[0].Path = "/v1/stats/changed"
	if _, projectionErr := projectStatsSidecar(upstream, deferred, legacy, compatibility); projectionErr == nil {
		t.Fatal("changed Stats sidecar route was accepted")
	}
	surface, err := decodeCompatibilitySurface(compatibility)
	if err != nil {
		t.Fatal(err)
	}
	surface.MethodMigrations[0].Canonical.Path = "/v1/stats/changed"
	changedCompatibility, err := json.Marshal(surface)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projectStatsSidecar(upstream, manifest, legacy, changedCompatibility); err == nil {
		t.Fatal("changed Stats compatibility migration was accepted")
	}
}

func TestStatsSidecarGenerationIsIsolatedAndFreshConsumerBuilds(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	upstream, _, legacy, compatibility := scopeSidecarPaths(root)
	workspace := t.TempDir()
	manifestContents, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "stats-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(workspace, "stats-manifest.json")
	if writeErr := os.WriteFile(manifest, manifestContents, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	target := filepath.Join(workspace, "api", "canonical", "stats")
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
	if generateErr := runStatsSidecar(upstream, manifest, target, "stats", "", compatibility, legacy); generateErr != nil {
		t.Fatal(generateErr)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	actualOutputs := []string{"openapi/canonical/stats.json"}
	for _, entry := range entries {
		actualOutputs = append(actualOutputs, "api/canonical/stats/"+entry.Name())
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
	var declaredOutputs []string
	for _, entry := range inventory.Generators {
		if entry.Name == "canonical-stats-sidecar" {
			declaredOutputs = entry.Outputs
		}
	}
	if !sameStringSet(actualOutputs, declaredOutputs) {
		t.Fatalf("Stats generator inventory = %v, actual output = %v", declaredOutputs, actualOutputs)
	}
	for _, entry := range entries {
		generated, readErr := os.ReadFile(filepath.Join(target, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		golden, goldenErr := os.ReadFile(filepath.Join(root, "api", "canonical", "stats", entry.Name()))
		if goldenErr != nil {
			t.Fatal(goldenErr)
		}
		if !bytes.Equal(generated, golden) {
			t.Fatalf("generated Stats artifact is stale: %s", entry.Name())
		}
	}
	for _, invalid := range []struct{ target, pkg, invoker string }{
		{filepath.Join(workspace, "api", "v1"), "v1", ""},
		{target, "stats", filepath.Join(workspace, "invoker.go")},
	} {
		if generateErr := runStatsSidecar(upstream, manifest, invalid.target, invalid.pkg, invalid.invoker, compatibility, legacy); generateErr == nil {
			t.Fatal("Stats sidecar accepted a legacy output target")
		}
	}
	for path, expected := range before {
		actual, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(actual, expected) {
			t.Fatalf("Stats generation modified frozen artifact %s", path)
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
	consumer, err := modfile.Parse("go.mod", []byte("module example.com/stats-consumer\n\ngo 1.27.0\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range locked.Require {
		if addErr := consumer.AddRequire(requirement.Mod.Path, requirement.Mod.Version); addErr != nil {
			t.Fatal(addErr)
		}
	}
	encoded, err := consumer.Format()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	consumerTest := `package statsconsumer
import (
 "testing"
 stats "example.com/stats-consumer/api/canonical/stats"
)
var _ stats.Handler = stats.UnimplementedHandler{}
var _ stats.Invoker = (*stats.Client)(nil)
var _ = (*stats.Client).GetStats
func TestConsumer(t *testing.T) {
 request := stats.GetStatsRequest{Selection: stats.NewAllScopeSelectionScopeSelection(stats.AllScopeSelection{Mode: stats.AllScopeSelectionModeAll})}
 if err := request.Validate(); err != nil { t.Fatal(err) }
}
`
	if err := os.WriteFile(filepath.Join(workspace, "consumer_test.go"), []byte(consumerTest), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"mod", "tidy"}, {"mod", "verify"}, {"test", "-count=1", "./..."}} {
		command := exec.CommandContext(t.Context(), "go", arguments...)
		command.Dir = workspace
		command.Env = append(os.Environ(), "GOWORK=off")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fresh Stats consumer %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
	}
}

func deepEqualSelectedPost(projected, raw any) bool {
	projectedPath, projectedOK := projected.(map[string]any)
	rawPath, rawOK := raw.(map[string]any)
	if !projectedOK || !rawOK {
		return false
	}
	return len(projectedPath) == 1 && bytes.Equal(mustDeterministicJSON(projectedPath["post"]), mustDeterministicJSON(rawPath["post"]))
}

func mustDeterministicJSON(value any) []byte {
	encoded, err := json.Marshal(value, json.Deterministic(true))
	if err != nil {
		panic(err)
	}
	return encoded
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[value] = struct{}{}
	}
	if len(values) != len(left) {
		return false
	}
	for _, value := range right {
		if _, found := values[value]; !found {
			return false
		}
	}
	return true
}
