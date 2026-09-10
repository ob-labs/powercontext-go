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

func TestRemoteSkillSidecarProjectionAndIntegrity(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	upstream, manifest, legacy, compatibility := readRemoteSkillSidecarInputs(t, root)
	projected, err := projectRemoteSkillSidecar(upstream, manifest, legacy, compatibility)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := parseOpenAPIOperations(projected)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]compatibilityEndpoint{
		"list_remote_skill_targets":  {Method: "post", Path: "/v1/skill/remote/targets"},
		"create_remote_skill_target": {Method: "post", Path: "/v1/skill/remote/target/create"},
		"enroll_remote_skill_target": {Method: "post", Path: "/v1/skill/remote/target/enroll"},
		"rename_remote_skill_target": {Method: "post", Path: "/v1/skill/remote/target/rename"},
		"revoke_remote_skill_target": {Method: "post", Path: "/v1/skill/remote/target/revoke"},
	}
	if !mapsEqual(operations, want) {
		t.Fatalf("projected operations = %v, want %v", operations, want)
	}
	document, err := decodeScopeSidecarDocument(projected)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := scopeSidecarObject(document["components"].(map[string]any), "schemas")
	if err != nil {
		t.Fatal(err)
	}
	remoteAgentKind, err := scopeSidecarObject(schemas, "RemoteAgentKind")
	if err != nil {
		t.Fatal(err)
	}
	values, ok := remoteAgentKind["enum"].([]any)
	if !ok || !slices.Equal(values, []any{"codex", "workbuddy"}) {
		t.Fatalf("RemoteAgentKind enum = %#v, want [codex workbuddy]", remoteAgentKind["enum"])
	}
	if bytes.Contains(projected, []byte("claude_code")) {
		t.Fatal("projected remote Skill sidecar retains claude_code")
	}
	committed, err := os.ReadFile(filepath.Join(root, "openapi", "canonical", "remote-skills.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(projected, committed) {
		t.Fatal("remote Skill projection is stale")
	}
	ledger, err := decodeCompatibilitySurface(compatibility)
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]string, len(ledger.Canonical.UpstreamOnlyOperations))
	for _, operation := range ledger.Canonical.UpstreamOnlyOperations {
		statuses[operation.OperationID] = operation.Status
	}
	for operationID := range want {
		if statuses[operationID] != compatibilityStatusImplementedCanonical {
			t.Fatalf("%s status = %q, want %q", operationID, statuses[operationID], compatibilityStatusImplementedCanonical)
		}
	}
	for _, operationID := range []string{
		"publish_remote_skill",
		"unpublish_remote_skill",
		"reconcile_remote_skills",
		"download_remote_skill_package",
		"record_remote_skill_receipt",
	} {
		if statuses[operationID] != compatibilityStatusDeferred {
			t.Fatalf("%s status = %q, want %q", operationID, statuses[operationID], compatibilityStatusDeferred)
		}
	}
	for name, mutate := range map[string]func(*remoteSkillSidecarManifest){
		"pin":    func(m *remoteSkillSidecarManifest) { m.Upstream.Commit = "main" },
		"digest": func(m *remoteSkillSidecarManifest) { m.Upstream.SHA256 = strings.Repeat("0", 64) },
		"extra operation": func(m *remoteSkillSidecarManifest) {
			m.Operations = append(m.Operations, scopeSidecarOperation{OperationID: "publish_remote_skill", Method: "post", Path: "/v1/skill/remote/publication/publish"})
		},
		"unsupported agent": func(m *remoteSkillSidecarManifest) {
			m.Overlays.RemoteAgentKind.Enum = append(m.Overlays.RemoteAgentKind.Enum, "claude_code")
		},
	} {
		t.Run(name, func(t *testing.T) {
			mutant := manifest
			mutant.Operations = slices.Clone(manifest.Operations)
			mutant.Overlays.RemoteAgentKind.Enum = slices.Clone(manifest.Overlays.RemoteAgentKind.Enum)
			mutate(&mutant)
			if _, projectErr := projectRemoteSkillSidecar(upstream, mutant, legacy, compatibility); projectErr == nil {
				t.Fatal("invalid remote Skill sidecar projection accepted")
			}
		})
	}
	for _, operationID := range []string{"list_remote_skill_targets", "create_remote_skill_target", "enroll_remote_skill_target", "rename_remote_skill_target", "revoke_remote_skill_target"} {
		t.Run("rejects deferred ledger status for "+operationID, func(t *testing.T) {
			mutant, err := decodeCompatibilitySurface(compatibility)
			if err != nil {
				t.Fatal(err)
			}
			for i := range mutant.Canonical.UpstreamOnlyOperations {
				operation := &mutant.Canonical.UpstreamOnlyOperations[i]
				if operation.OperationID == operationID {
					operation.Status = compatibilityStatusDeferred
				}
			}
			encoded, err := json.Marshal(mutant)
			if err != nil {
				t.Fatal(err)
			}
			if _, projectErr := projectRemoteSkillSidecar(upstream, manifest, legacy, encoded); projectErr == nil {
				t.Fatal("deferred ledger status accepted")
			}
		})
	}
}

func TestRemoteSkillSidecarGenerationIsIsolatedAndConsumable(t *testing.T) {
	root := repositoryRootForScopeSidecarTest(t)
	source, manifest, legacy, compatibility := remoteSkillSidecarPaths(root)
	workspace := t.TempDir()
	target := filepath.Join(workspace, "api", "canonical", "remoteskills")
	frozen := []string{legacy, filepath.Join(root, "client", "invoker_gen.go"), filepath.Join(root, "internal", "mcpapi", "schemas_gen.go")}
	entries, err := os.ReadDir(filepath.Join(root, "api", "v1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			frozen = append(frozen, filepath.Join(root, "api", "v1", entry.Name()))
		}
	}
	before := readArtifacts(t, frozen)
	if generateErr := runRemoteSkillSidecar(source, manifest, target, "remoteskills", "", compatibility, legacy); generateErr != nil {
		t.Fatal(generateErr)
	}
	generated := readGeneratedScopePackage(t, target)
	for _, expected := range []string{"ListRemoteSkillTargets", "CreateRemoteSkillTarget", "EnrollRemoteSkillTarget", "RenameRemoteSkillTarget", "RevokeRemoteSkillTarget", "RemoteAgentKindCodex", "RemoteAgentKindWorkbuddy"} {
		if !strings.Contains(generated, expected) {
			t.Fatalf("generated remote Skill package lacks %q", expected)
		}
	}
	for _, absent := range []string{"PublishRemoteSkill", "UnpublishRemoteSkill", "ReconcileRemoteSkills", "DownloadRemoteSkillPackage", "RecordRemoteSkillReceipt", "claude_code", "RemoteAgentKindClaudeCode"} {
		if strings.Contains(generated, absent) {
			t.Fatalf("generated remote Skill package retains %q", absent)
		}
	}
	actualOutputs := []string{"openapi/canonical/remote-skills.json"}
	generatedEntries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range generatedEntries {
		actualOutputs = append(actualOutputs, "api/canonical/remoteskills/"+entry.Name())
		contents, err := os.ReadFile(filepath.Join(target, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		golden, err := os.ReadFile(filepath.Join(root, "api", "canonical", "remoteskills", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(contents, golden) {
			t.Fatalf("generated remote Skill artifact is stale: %s", entry.Name())
		}
	}
	assertRemoteSkillGeneratorInventory(t, root, actualOutputs)
	for _, path := range frozen {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, before[path]) {
			t.Fatalf("remote Skill generation modified frozen artifact %s", path)
		}
	}
	for _, tc := range []struct{ target, packageName, clientInvoker string }{
		{filepath.Join(workspace, "api", "v1"), "v1", ""},
		{target, "remoteskills", filepath.Join(workspace, "client", "invoker.go")},
		{target, "v1", ""},
	} {
		if err := runRemoteSkillSidecar(source, manifest, tc.target, tc.packageName, tc.clientInvoker, compatibility, legacy); err == nil {
			t.Fatal("remote Skill sidecar generation accepted a legacy output")
		}
	}
	assertFreshRemoteSkillSidecarConsumerBuilds(t, root)
}

func readRemoteSkillSidecarInputs(t *testing.T, root string) ([]byte, remoteSkillSidecarManifest, []byte, []byte) {
	t.Helper()
	source, manifestPath, legacy, compatibility := remoteSkillSidecarPaths(root)
	upstream, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := loadRemoteSkillSidecarManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyContents, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	compatibilityContents, err := os.ReadFile(compatibility)
	if err != nil {
		t.Fatal(err)
	}
	return upstream, manifest, legacyContents, compatibilityContents
}

func remoteSkillSidecarPaths(root string) (source, manifest, legacy, compatibility string) {
	return filepath.Join(root, "openapi", "canonical", "upstream-powercontext.yaml"),
		filepath.Join(root, "openapi", "canonical", "remote-skills-manifest.json"),
		filepath.Join(root, "openapi", "powercontext.yaml"),
		filepath.Join(root, "openapi", "compatibility-surface.json")
}

func assertRemoteSkillGeneratorInventory(t *testing.T, root string, actualOutputs []string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, "test", "generator-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory struct {
		SchemaVersion int `json:"schema_version"`
		Generators    []struct {
			Name     string   `json:"name"`
			Command  string   `json:"command"`
			Inputs   []string `json:"inputs"`
			Outputs  []string `json:"outputs"`
			Evidence []string `json:"evidence"`
		} `json:"generators"`
	}
	if err := json.Unmarshal(contents, &inventory, json.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	for _, generator := range inventory.Generators {
		if generator.Name == "canonical-remote-skill-sidecar" {
			if slices.Equal(slices.Sorted(slices.Values(generator.Outputs)), slices.Sorted(slices.Values(actualOutputs))) {
				return
			}
			t.Fatalf("remote Skill generator inventory = %v, actual outputs = %v", generator.Outputs, actualOutputs)
		}
	}
	t.Fatal("remote Skill generator inventory is missing")
}

func assertFreshRemoteSkillSidecarConsumerBuilds(t *testing.T, root string) {
	t.Helper()
	consumer := t.TempDir()
	contents, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	locked, err := modfile.Parse("go.mod", contents, nil)
	if err != nil {
		t.Fatal(err)
	}
	consumerMod, err := modfile.Parse("go.mod", []byte("module example.com/remote-skill-consumer\n\ngo 1.27.0\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range locked.Require {
		if addRequireErr := consumerMod.AddRequire(requirement.Mod.Path, requirement.Mod.Version); addRequireErr != nil {
			t.Fatal(addRequireErr)
		}
	}
	if addReplaceErr := consumerMod.AddReplace("github.com/ob-labs/powercontext-go", "", filepath.ToSlash(root), ""); addReplaceErr != nil {
		t.Fatal(addReplaceErr)
	}
	encoded, err := consumerMod.Format()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "go.mod"), encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	test := `package remoteskillconsumer
import (
  "testing"
  remoteskills "github.com/ob-labs/powercontext-go/api/canonical/remoteskills"
)
var _ remoteskills.Handler = remoteskills.UnimplementedHandler{}
var _ remoteskills.Invoker = (*remoteskills.Client)(nil)
var _ = (*remoteskills.Client).ListRemoteSkillTargets
var _ = (*remoteskills.Client).CreateRemoteSkillTarget
var _ = (*remoteskills.Client).EnrollRemoteSkillTarget
var _ = (*remoteskills.Client).RenameRemoteSkillTarget
var _ = (*remoteskills.Client).RevokeRemoteSkillTarget
func TestRemoteAgentKind(t *testing.T) {
  values := remoteskills.RemoteAgentKind("").AllValues()
  if len(values) != 2 || values[0] != remoteskills.RemoteAgentKindCodex || values[1] != remoteskills.RemoteAgentKindWorkbuddy {
    t.Fatalf("RemoteAgentKind values = %v", values)
  }
}
`
	if err := os.WriteFile(filepath.Join(consumer, "consumer_test.go"), []byte(test), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, arguments := range [][]string{{"mod", "tidy"}, {"mod", "verify"}, {"test", "-count=1", "./..."}} {
		command := exec.CommandContext(t.Context(), "go", arguments...)
		command.Dir = consumer
		command.Env = append(os.Environ(), "GOWORK=off")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fresh remote Skill consumer %s: %v\n%s", strings.Join(arguments, " "), err, output)
		}
	}
}

func mapsEqual(left, right map[string]compatibilityEndpoint) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
}
