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

func TestManagedSkillSidecarProjectionAndIntegrity(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	upstream, _, legacy, compatibility := readScopeSidecarInputs(t, root)
	manifestPath := filepath.Join(root, "openapi", "canonical", "managed-skills-manifest.json")
	manifest, err := loadManagedSkillSidecarManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	projected, err := projectManagedSkillSidecar(upstream, manifest, legacy, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := parseOpenAPIOperations(projected)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]compatibilityEndpoint{
		"get_skill_package_manifest": {Method: "post", Path: "/v1/skill/package/manifest"},
		"download_skill_package":     {Method: "post", Path: "/v1/skill/package/download"},
		"record_skill_usage":         {Method: "post", Path: "/v1/skill/usage"},
	}
	if len(operations) != len(want) {
		t.Fatalf("unexpected operations: %v", operations)
	}
	for id, endpoint := range want {
		if operations[id] != endpoint {
			t.Fatalf("%s = %v, want %v", id, operations[id], endpoint)
		}
	}
	document, err := decodeScopeSidecarDocument(projected)
	if err != nil {
		t.Fatal(err)
	}
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	expectedSchemas := []string{
		"GetSkillPackageRequest", "ArtifactReference", "SkillPackageManifest", "SkillPackageDownload", "SkillPackageReference", "SkillPackageFile",
		"RecordSkillUsageRequest", "CaptureContentSourceResponse", "CaptureStatus", "SourceReference", "ErrorResponse", "ErrorDetail",
	}
	if len(schemas) != len(expectedSchemas) {
		t.Fatalf("unexpected schemas: %v", schemas)
	}
	for _, name := range expectedSchemas {
		if _, ok := schemas[name]; !ok {
			t.Fatalf("missing schema %s", name)
		}
	}
	committed, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "managed-skills.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(projected, committed) {
		t.Fatal("managed Skill projection is stale")
	}
	for name, mutate := range map[string]func(*managedSkillSidecarManifest){
		"pin":    func(m *managedSkillSidecarManifest) { m.Upstream.Commit = "main" },
		"digest": func(m *managedSkillSidecarManifest) { m.Upstream.SHA256 = strings.Repeat("0", 64) },
		"extra operation": func(m *managedSkillSidecarManifest) {
			m.Operations = append(m.Operations, scopeSidecarOperation{OperationID: "propose_skill_package"})
		},
		"wrong route": func(m *managedSkillSidecarManifest) { m.Operations[0].Path = "/v1/skill/remote/package/manifest" },
	} {
		t.Run(name, func(t *testing.T) {
			mutant := manifest
			mutant.Operations = slices.Clone(manifest.Operations)
			mutate(&mutant)
			if _, projectErr := projectManagedSkillSidecar(upstream, mutant, legacy, compatibility); projectErr == nil {
				t.Fatal("invalid projection accepted")
			}
		})
	}
	if _, projectErr := projectManagedSkillSidecar(append(slices.Clone(upstream), '\n'), manifest, legacy, compatibility); projectErr == nil {
		t.Fatal("modified source bytes accepted")
	}
	for _, id := range []string{"get_skill_package_manifest", "download_skill_package", "record_skill_usage"} {
		for _, field := range []string{"status", "method", "path", "operation_id"} {
			t.Run(id+"/"+field, func(t *testing.T) {
				ledger, decodeErr := decodeCompatibilitySurface(compatibility)
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				changed := false
				for i := range ledger.Canonical.UpstreamOnlyOperations {
					op := &ledger.Canonical.UpstreamOnlyOperations[i]
					if op.OperationID != id {
						continue
					}
					changed = true
					switch field {
					case "status":
						op.Status = "deferred"
					case "method":
						op.Method = "get"
					case "path":
						op.Path = "/v1/skill/wrong"
					case "operation_id":
						op.OperationID = "invented_operation"
					}
				}
				if !changed {
					t.Fatal("ledger mutant did not change")
				}
				encoded, encodeErr := json.Marshal(ledger)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				if _, projectErr := projectManagedSkillSidecar(upstream, manifest, legacy, encoded); projectErr == nil {
					t.Fatal("ledger mismatch accepted")
				}
			})
		}
	}
	valid, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	for _, prefix := range []string{`{"extra":true,`, `{"schema_version":1,`} {
		if writeErr := os.WriteFile(path, append([]byte(prefix), valid[1:]...), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if _, loadErr := loadManagedSkillSidecarManifest(path); loadErr == nil {
			t.Fatal("unknown or duplicate manifest member accepted")
		}
	}
}

func TestManagedSkillSidecarGenerationIsIsolatedAndFreshConsumerBuilds(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	upstream, _, legacy, compatibility := scopeSidecarPaths(root)
	fixture := t.TempDir()
	manifestContents, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "managed-skills-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(fixture, "managed-skills-manifest.json")
	if writeErr := os.WriteFile(manifest, manifestContents, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	target := filepath.Join(fixture, "api", "canonical", "managedskills")
	frozen := []string{legacy, filepath.Join(root, "client", "invoker_gen.go"), filepath.Join(root, "internal", "mcpapi", "schemas_gen.go")}
	for _, directory := range []string{"api/v1", "api/canonical/scopes", "api/canonical/sources", "api/canonical/artifacts"} {
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
	if generationErr := runManagedSkillSidecar(upstream, manifest, target, "managedskills", "", compatibility, legacy); generationErr != nil {
		t.Fatal(generationErr)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	actualOutputs := []string{"openapi/canonical/managed-skills.json"}
	for _, entry := range entries {
		actualOutputs = append(actualOutputs, "api/canonical/managedskills/"+entry.Name())
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
		if entry.Name == "canonical-managed-skill-sidecar" {
			declaredOutputs = entry.Outputs
		}
	}
	if !slices.Equal(slices.Sorted(slices.Values(actualOutputs)), slices.Sorted(slices.Values(declaredOutputs))) {
		t.Fatalf("managed Skill generator inventory = %v, actual output = %v", declaredOutputs, actualOutputs)
	}
	for _, entry := range entries {
		generated, generatedErr := os.ReadFile(filepath.Join(target, entry.Name()))
		if generatedErr != nil {
			t.Fatal(generatedErr)
		}
		golden, goldenErr := os.ReadFile(filepath.Join(root, "api", "canonical", "managedskills", entry.Name()))
		if goldenErr != nil {
			t.Fatal(goldenErr)
		}
		if !bytes.Equal(generated, golden) {
			t.Fatalf("generated managed Skill artifact is stale: %s", entry.Name())
		}
	}
	for _, tc := range []struct{ target, pkg, invoker string }{
		{filepath.Join(fixture, "api", "v1"), "v1", ""},
		{target, "managedskills", filepath.Join(fixture, "invoker.go")},
		{filepath.Join(fixture, "other", "canonical", "managedskills"), "managedskills", ""},
		{target, "v1", ""},
	} {
		if generationErr := runManagedSkillSidecar(upstream, manifest, tc.target, tc.pkg, tc.invoker, compatibility, legacy); generationErr == nil {
			t.Fatal("legacy output accepted")
		}
	}
	for path, want := range before {
		got, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("managed Skill generation modified frozen artifact %s", path)
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
	consumer, err := modfile.Parse("go.mod", []byte("module example.com/managed-skill-consumer\n\ngo 1.27.0\n"), nil)
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
	test := `package managedskillconsumer
import (
 "context"
 "testing"
 managedskills "example.com/managed-skill-consumer/api/canonical/managedskills"
)
var _ managedskills.Handler = managedskills.UnimplementedHandler{}
var _ managedskills.Invoker = (*managedskills.Client)(nil)
var _ func(context.Context, *managedskills.GetSkillPackageRequest) (managedskills.GetSkillPackageManifestRes, error) = managedskills.UnimplementedHandler{}.GetSkillPackageManifest
var _ func(context.Context, *managedskills.GetSkillPackageRequest) (managedskills.DownloadSkillPackageRes, error) = managedskills.UnimplementedHandler{}.DownloadSkillPackage
var _ func(context.Context, *managedskills.RecordSkillUsageRequest) (managedskills.RecordSkillUsageRes, error) = managedskills.UnimplementedHandler{}.RecordSkillUsage
func TestConsumer(t *testing.T) {
 request:=managedskills.GetSkillPackageRequest{ScopeID:"scope", Artifact:managedskills.ArtifactReference{ArtifactID:"skill", Family:"skill", Revision:1}}
 if err:=request.Validate();err!=nil{t.Fatal(err)}
 request.Artifact.Revision=0
 if err:=request.Validate();err==nil{t.Fatal("zero revision accepted")}
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
			t.Fatalf("fresh managed Skill consumer %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
		t.Logf("fresh managed Skill consumer %s: %s", strings.Join(arguments, " "), output)
	}
}

func TestManagedSkillSidecarCLIRejectsMixedModes(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	binary := filepath.Join(t.TempDir(), "api-generate.exe")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./tools/api-generate")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build generator: %v\n%s", err, output)
	}
	for _, other := range []string{"scope", "source", "artifact"} {
		t.Run(other, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), binary, "-managed-skill-sidecar-manifest", "managed.json", "-"+other+"-sidecar-manifest", "other.json")
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "select exactly one sidecar manifest") {
				t.Fatalf("mixed modes: %v\n%s", err, output)
			}
		})
	}
}
