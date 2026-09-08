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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestScopeSidecarStableSourceAcceptsMovingInventory(t *testing.T) {
	t.Parallel()
	source, manifest, legacy, compatibility := readScopeSidecarInputs(t, repositoryRootForScopeSidecarTest(t))
	surface, err := decodeCompatibilitySurface(compatibility)
	if err != nil {
		t.Fatal(err)
	}
	if surface.Upstream.Commit != "e4ebdcdff64a9793aa30f5d087cc71cd7e9ba87c" || surface.Canonical.OperationCount != 94 {
		t.Fatal("fixture must use the e4 inventory")
	}
	if manifest.Upstream.Commit != "74b961fbb07165595314726715d412a3d0d90589" {
		t.Fatal("fixture must retain the stable sidecar source")
	}
	if _, projectionErr := projectScopeSidecar(source, manifest, legacy, compatibility); projectionErr != nil {
		t.Fatalf("stable sidecar source rejected against moving inventory: %v", projectionErr)
	}
}

func TestScopeSidecarSelectedOperationsRequireInventoryAgreement(t *testing.T) {
	t.Parallel()
	source, _, legacy, compatibility := readScopeSidecarInputs(t, repositoryRootForScopeSidecarTest(t))
	for _, selected := range [][]scopeSidecarOperation{scopeSidecarOperations, sourceSidecarOperations, artifactSidecarOperations} {
		for _, operation := range selected {
			for _, mutation := range []string{"missing", "method", "path", "deferred"} {
				t.Run(operation.OperationID+"/"+mutation, func(t *testing.T) {
					surface, decodeErr := decodeCompatibilitySurface(compatibility)
					if decodeErr != nil {
						t.Fatal(decodeErr)
					}
					index := stagedOperationIndex(t, surface, operation.OperationID)
					switch mutation {
					case "missing":
						// Keep the inventory size valid so selected-operation membership must reject it.
						surface.Canonical.UpstreamOnlyOperations[index].OperationID += "_unselected"
					case "method":
						surface.Canonical.UpstreamOnlyOperations[index].Method = "delete"
					case "path":
						surface.Canonical.UpstreamOnlyOperations[index].Path += "/changed"
					case "deferred":
						surface.Canonical.UpstreamOnlyOperations[index].Status = compatibilityStatusDeferred
					}
					mutant, marshalErr := json.Marshal(surface)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if validationErr := validateCompatibilitySurface(legacy, mutant); validationErr != nil {
						t.Fatalf("mutant must remain a valid inventory: %v", validationErr)
					}
					if validationErr := validatePinnedScopeSidecarSource(source, selected, legacy, mutant); validationErr == nil ||
						!strings.Contains(validationErr.Error(), operation.OperationID) {
						t.Fatalf("selected operation mutant was not rejected specifically: %v", validationErr)
					}
				})
			}
		}
	}
	for _, selected := range [][]scopeSidecarOperation{scopeSidecarOperations, sourceSidecarOperations, artifactSidecarOperations} {
		for _, operation := range selected {
			t.Run(operation.OperationID+"/source mismatch", func(t *testing.T) {
				mutant := slices.Clone(selected)
				index := slices.Index(mutant, operation)
				mutant[index].Path += "/changed"
				surface, decodeErr := decodeCompatibilitySurface(compatibility)
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				surface.Canonical.UpstreamOnlyOperations[stagedOperationIndex(t, surface, operation.OperationID)].Path = mutant[index].Path
				contents, marshalErr := json.Marshal(surface)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if validationErr := validatePinnedScopeSidecarSource(source, mutant, legacy, contents); validationErr == nil ||
					!strings.Contains(validationErr.Error(), operation.OperationID) {
					t.Fatalf("source mismatch was not rejected specifically: %v", validationErr)
				}
			})
		}
	}
}

func TestScopeSidecarProjectsOnlyPinnedScopeMetadataOperations(t *testing.T) {
	t.Parallel()
	repository := repositoryRootForScopeSidecarTest(t)
	source, manifest, legacy, compatibility := readScopeSidecarInputs(t, repository)

	projected, err := projectScopeSidecar(source, manifest, legacy, compatibility)
	if err != nil {
		t.Fatalf("project scope sidecar: %v", err)
	}
	operations, err := parseOpenAPIOperations(projected)
	if err != nil {
		t.Fatalf("parse projected scope sidecar: %v", err)
	}
	want := map[string]compatibilityEndpoint{
		"list_scopes":             {Method: "get", Path: "/v1/scopes"},
		"create_scope":            {Method: "post", Path: "/v1/scopes"},
		"get_scope":               {Method: "get", Path: "/v1/scopes/{scope_id}"},
		"update_scope":            {Method: "put", Path: "/v1/scopes/{scope_id}"},
		"get_default_scope":       {Method: "get", Path: "/v1/scopes/default"},
		"set_default_scope":       {Method: "put", Path: "/v1/scopes/default"},
		"resolve_scope_selection": {Method: "post", Path: "/v1/scopes/selection/resolve"},
		"resolve_scope_binding":   {Method: "post", Path: "/v1/scope-bindings/resolve"},
		"set_scope_binding":       {Method: "put", Path: "/v1/scope-bindings"},
		"clear_scope_binding":     {Method: "post", Path: "/v1/scope-bindings/clear"},
	}
	if !sameCompatibilityOperations(mapCompatibilityOperations(operations), mapCompatibilityOperations(want)) {
		t.Fatalf("scope sidecar operations = %#v, want %#v", operations, want)
	}
	for _, deferred := range []string{"create_source"} {
		if _, found := operations[deferred]; found {
			t.Fatalf("projected scope sidecar contains deferred operation %q", deferred)
		}
	}
	if bytes.Contains(bytes.ToLower(projected), []byte("claude")) {
		t.Fatalf("projected scope sidecar retains an unsupported external agent:\n%s", projected)
	}
	if !bytes.Contains(projected, []byte(`"enum":["codex","workbuddy"]`)) {
		t.Fatalf("projected scope sidecar does not apply the exact supported-agent overlay:\n%s", projected)
	}
	for _, unsupported := range unsupportedScopeSidecarAgents {
		if bytes.Contains(bytes.ToLower(projected), []byte(fmt.Sprintf("%q", unsupported))) {
			t.Fatalf("projected scope sidecar retains unsupported external agent %q:\n%s", unsupported, projected)
		}
	}

	t.Run("rejects a pinned source route change after the digest is updated", func(t *testing.T) {
		mutated := bytes.Replace(source, []byte("/v1/scopes/selection/resolve:"), []byte("/v1/scopes/selection/changed:"), 1)
		if bytes.Equal(mutated, source) {
			t.Fatal("pinned source has no scope-selection route mutation point")
		}
		updated := manifest
		updated.Upstream.SHA256 = sha256Hex(mutated)
		if _, mutationErr := projectScopeSidecar(mutated, updated, legacy, compatibility); mutationErr == nil {
			t.Fatal("scope sidecar accepted changed pinned operation route")
		}
	})

	t.Run("rejects an extra deferred operation in the allowlist", func(t *testing.T) {
		mutated := manifest
		mutated.Operations = append(mutated.Operations, scopeSidecarOperation{
			OperationID: "create_source",
			Method:      "post",
			Path:        "/v1/scopes/{scope_id}/sources",
		})
		if _, mutationErr := projectScopeSidecar(source, mutated, legacy, compatibility); mutationErr == nil {
			t.Fatal("scope sidecar accepted a deferred operation")
		}
	})

	for _, operation := range manifest.Operations {
		t.Run("rejects deferred ledger status for "+operation.OperationID, func(t *testing.T) {
			surface, decodeErr := decodeCompatibilitySurface(compatibility)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			surface.Canonical.UpstreamOnlyOperations[stagedOperationIndex(t, surface, operation.OperationID)].Status = compatibilityStatusDeferred
			mutated, marshalErr := json.Marshal(surface)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if _, mutationErr := projectScopeSidecar(source, manifest, legacy, mutated); mutationErr == nil {
				t.Fatalf("scope sidecar accepted deferred ledger status for %q", operation.OperationID)
			}
		})
	}
}

func TestScopeSidecarProjectionIsByteDeterministic(t *testing.T) {
	t.Parallel()
	repository := repositoryRootForScopeSidecarTest(t)
	source, manifest, legacy, compatibility := readScopeSidecarInputs(t, repository)
	first, err := projectScopeSidecar(source, manifest, legacy, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	for range 8 {
		next, projectionErr := projectScopeSidecar(source, manifest, legacy, compatibility)
		if projectionErr != nil {
			t.Fatal(projectionErr)
		}
		if !bytes.Equal(next, first) {
			t.Fatalf("Scope sidecar projection is not byte deterministic:\nfirst: %s\nnext: %s", first, next)
		}
	}
}

func TestScopeSidecarGenerationIsIsolatedAndFreshConsumerBuilds(t *testing.T) {
	repository := repositoryRootForScopeSidecarTest(t)
	sourcePath, manifestPath, legacyPath, compatibilityPath := copyScopeSidecarGenerationInputs(t, repository)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "api", "canonical", "scopes")
	if err := runScopeSidecar(sourcePath, manifestPath, target, "scopes", "", compatibilityPath, legacyPath); err != nil {
		t.Fatalf("generate scope sidecar: %v", err)
	}
	generated := readGeneratedScopePackage(t, target)
	for _, expected := range []string{
		"ListScopes",
		"CreateScope",
		"GetScope",
		"UpdateScope",
		"GetDefaultScope",
		"SetDefaultScope",
		"ResolveScopeSelection",
		"ResolveScopeBinding",
		"SetScopeBinding",
		"ClearScopeBinding",
	} {
		if !strings.Contains(generated, expected) {
			t.Fatalf("generated scope package lacks operation %q", expected)
		}
	}
	for _, absent := range []string{"CreateSource"} {
		if strings.Contains(generated, absent) {
			t.Fatalf("generated scope package unexpectedly contains %q", absent)
		}
	}
	for _, unsupported := range unsupportedScopeSidecarAgents {
		if strings.Contains(strings.ToLower(generated), fmt.Sprintf("%q", unsupported)) {
			t.Fatalf("generated scope package retains unsupported external agent %q", unsupported)
		}
	}
	operationNames, err := generatedScopeOperationNames(filepath.Join(target, "oas_operations_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	wantOperationNames := map[string]struct{}{
		"CreateScopeOperation":           {},
		"GetDefaultScopeOperation":       {},
		"GetScopeOperation":              {},
		"ListScopesOperation":            {},
		"ResolveScopeBindingOperation":   {},
		"ResolveScopeSelectionOperation": {},
		"SetScopeBindingOperation":       {},
		"SetDefaultScopeOperation":       {},
		"ClearScopeBindingOperation":     {},
		"UpdateScopeOperation":           {},
	}
	if len(operationNames) != len(wantOperationNames) {
		t.Fatalf("generated scope operation names = %#v, want %#v", operationNames, wantOperationNames)
	}
	for name := range wantOperationNames {
		if _, found := operationNames[name]; !found {
			t.Fatalf("generated scope package lacks operation name %q", name)
		}
	}

	for _, rejected := range []struct {
		name          string
		target        string
		packageName   string
		clientInvoker string
	}{
		{
			name:        "legacy v1 package",
			target:      filepath.Join(workspace, "api", "v1"),
			packageName: "v1",
		},
		{
			name:          "legacy Client Invoker",
			target:        filepath.Join(workspace, "api", "canonical", "scopes"),
			packageName:   "scopes",
			clientInvoker: filepath.Join(workspace, "client", "invoker_gen.go"),
		},
	} {
		t.Run(rejected.name, func(t *testing.T) {
			err := runScopeSidecar(
				sourcePath,
				manifestPath,
				rejected.target,
				rejected.packageName,
				rejected.clientInvoker,
				compatibilityPath,
				legacyPath,
			)
			if err == nil {
				t.Fatal("scope sidecar generation unexpectedly accepted a legacy output")
			}
		})
	}

	assertScopeSidecarLeavesLegacyArtifactsUntouched(t, repository, sourcePath, manifestPath, compatibilityPath, legacyPath)
	assertFreshScopeSidecarConsumerBuilds(t, repository)
}

func readScopeSidecarInputs(
	t *testing.T,
	repository string,
) ([]byte, scopeSidecarManifest, []byte, []byte) {
	t.Helper()
	sourcePath, manifestPath, legacyPath, compatibilityPath := scopeSidecarPaths(repository)
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := loadScopeSidecarManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	compatibility, err := os.ReadFile(compatibilityPath)
	if err != nil {
		t.Fatal(err)
	}
	return source, manifest, legacy, compatibility
}

func scopeSidecarPaths(repository string) (source, manifest, legacy, compatibility string) {
	return filepath.Join(repository, "openapi", "canonical", "upstream-powercontext.yaml"),
		filepath.Join(repository, "openapi", "canonical", "scopes-manifest.json"),
		filepath.Join(repository, "openapi", "powercontext.yaml"),
		filepath.Join(repository, "openapi", "compatibility-surface.json")
}

func copyScopeSidecarGenerationInputs(t *testing.T, repository string) (source, manifest, legacy, compatibility string) {
	t.Helper()
	source, manifest, legacy, compatibility = scopeSidecarPaths(repository)
	fixture := t.TempDir()
	for _, entry := range []struct {
		source      string
		destination string
	}{
		{source: source, destination: filepath.Join(fixture, "upstream-powercontext.yaml")},
		{source: manifest, destination: filepath.Join(fixture, "scopes-manifest.json")},
	} {
		contents, err := os.ReadFile(entry.source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(entry.destination, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(fixture, "upstream-powercontext.yaml"),
		filepath.Join(fixture, "scopes-manifest.json"), legacy, compatibility
}

func repositoryRootForScopeSidecarTest(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(directory, "..", ".."))
}

func mapCompatibilityOperations(values map[string]compatibilityEndpoint) []compatibilityOperation {
	operations := make([]compatibilityOperation, 0, len(values))
	for operationID, endpoint := range values {
		operations = append(operations, compatibilityOperation{
			OperationID: operationID,
			Method:      endpoint.Method,
			Path:        endpoint.Path,
		})
	}
	return operations
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

var unsupportedScopeSidecarAgents = []string{
	"claude",
	"claude-code",
	"dsh",
	"hermes",
	"openclaw",
	"opencode",
	"pi",
	"langchain",
	"langgraph",
	"pydantic",
}

func readGeneratedScopePackage(t *testing.T, directory string) string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" {
			continue
		}
		contents, readErr := os.ReadFile(filepath.Join(directory, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		output.Write(contents)
	}
	return output.String()
}

func generatedScopeOperationNames(path string) (map[string]struct{}, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	operations := make(map[string]struct{})
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 4 || fields[1] != "OperationName" || fields[2] != "=" ||
			!strings.HasSuffix(fields[0], "Operation") {
			continue
		}
		operations[fields[0]] = struct{}{}
	}
	return operations, nil
}

func assertScopeSidecarLeavesLegacyArtifactsUntouched(
	t *testing.T,
	repository, sourcePath, manifestPath, compatibilityPath, legacyPath string,
) {
	t.Helper()
	artifacts := []string{
		filepath.Join(repository, "openapi", "powercontext.yaml"),
		filepath.Join(repository, "client", "invoker_gen.go"),
		filepath.Join(repository, "internal", "mcpapi", "schemas_gen.go"),
	}
	entries, err := os.ReadDir(filepath.Join(repository, "api", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			artifacts = append(artifacts, filepath.Join(repository, "api", "v1", entry.Name()))
		}
	}
	before := readArtifacts(t, artifacts)
	target := filepath.Join(t.TempDir(), "api", "canonical", "scopes")
	if err := runScopeSidecar(sourcePath, manifestPath, target, "scopes", "", compatibilityPath, legacyPath); err != nil {
		t.Fatalf("generate isolated scope sidecar: %v", err)
	}
	after := readArtifacts(t, artifacts)
	for path, expected := range before {
		if !bytes.Equal(after[path], expected) {
			t.Fatalf("scope sidecar generation changed legacy artifact %s", path)
		}
	}
}

func readArtifacts(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	artifacts := make(map[string][]byte, len(paths))
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		artifacts[path] = contents
	}
	return artifacts
}

func assertFreshScopeSidecarConsumerBuilds(t *testing.T, repository string) {
	t.Helper()
	consumer := t.TempDir()
	writeScopeSidecarConsumerFile(t, filepath.Join(consumer, "go.mod"), fmt.Sprintf(`module example.com/powercontext-scope-consumer

go 1.25.0

require github.com/ob-labs/powercontext-go v0.0.0

replace github.com/ob-labs/powercontext-go => %s
`, filepath.ToSlash(repository)))
	writeScopeSidecarConsumerFile(t, filepath.Join(consumer, "consumer_test.go"), `package scopeconsumer

import scopes "github.com/ob-labs/powercontext-go/api/canonical/scopes"

type scopeHandler struct{ scopes.UnimplementedHandler }

var (
	_ scopes.Handler = scopeHandler{}
	_ scopes.Invoker = (*scopes.Client)(nil)
	_                = scopes.NewServer
	_                = scopes.NewClient
	_                = (*scopes.Client).ListScopes
	_                = (*scopes.Client).CreateScope
	_                = (*scopes.Client).UpdateScope
	_                = (*scopes.Client).SetDefaultScope
	_                = (*scopes.Client).ResolveScopeBinding
	_                = (*scopes.Client).SetScopeBinding
	_                = (*scopes.Client).ClearScopeBinding
)
`)
	for _, arguments := range [][]string{{"mod", "tidy"}, {"mod", "verify"}, {"build", "./..."}, {"test", "-count=1", "./..."}} {
		command := exec.CommandContext(t.Context(), "go", arguments...)
		command.Dir = consumer
		command.Env = append(os.Environ(), "GOWORK=off")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fresh scope sidecar consumer %v: %v\n%s", arguments, err, output)
		}
	}
}

func writeScopeSidecarConsumerFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
