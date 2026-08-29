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

package skill_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
)

func TestExactManagedSkillProjectsToAgentDirectory(t *testing.T) {
	content := projectionContent(t, "powercontext-openapi-change", "Use when changing PowerContext's public HTTP contract.")
	ref := projectionRef(t, "skill-123", 2)
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	target := projectionTarget(t, "codex-project", skill.CodexAgent, root)

	projected, err := skill.ProjectSkill(ref, content, target)
	if err != nil {
		t.Fatal(err)
	}
	skillText, err := os.ReadFile(filepath.Join(projected, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(
		string(skillText),
		`name: "powercontext-openapi-change"`,
		"Generated from artifact:skill/skill-123@2",
		"- make contract-test passes",
	) {
		t.Fatalf("unexpected projected SKILL.md:\n%s", skillText)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(projected, "powercontext.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Schema      string `json:"schema"`
		AgentKind   string `json:"agent_kind"`
		SkillSHA256 string `json:"skill_sha256"`
		Artifact    struct {
			Family     string `json:"family"`
			ArtifactID string `json:"artifact_id"`
			Revision   int64  `json:"revision"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(skillText)
	if manifest.Schema != skill.ProjectionSchema || manifest.AgentKind != "codex" ||
		manifest.Artifact.Family != "skill" || manifest.Artifact.ArtifactID != "skill-123" ||
		manifest.Artifact.Revision != 2 || manifest.SkillSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected projection manifest: %s", manifestBytes)
	}
}

func TestManagedProjectionCanBeInspectedAndSafelyUpdated(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	target := projectionTarget(t, "codex-project", skill.CodexAgent, root)
	first := projectionRef(t, "skill-123", 1)
	second := projectionRef(t, "skill-123", 2)
	original := projectionContent(t, "safe-skill", "Use for a bounded task.")
	updated, err := skill.NewContent(
		"safe-skill-v2", "Use for a bounded task.",
		"Perform the bounded task and inspect the result.",
		[]string{"The expected result exists."},
	)
	if err != nil {
		t.Fatal(err)
	}

	unpublished := skill.InspectSkillProjection(first, original, target)
	published, err := skill.PublishSkillProjection(first, original, target, &unpublished)
	if err != nil {
		t.Fatal(err)
	}
	updateAvailable := skill.InspectSkillProjection(second, updated, target)
	current, err := skill.PublishSkillProjection(second, updated, target, &updateAvailable)
	if err != nil {
		t.Fatal(err)
	}

	if unpublished.State() != skill.ProjectionUnpublished || published.State() != skill.ProjectionCurrent ||
		updateAvailable.State() != skill.ProjectionUpdateAvailable || current.State() != skill.ProjectionCurrent {
		t.Fatalf("projection states = %s, %s, %s, %s", unpublished.State(), published.State(), updateAvailable.State(), current.State())
	}
	if got := updateAvailable.PublishedArtifact(); got == nil || *got != first {
		t.Fatalf("published prior revision = %#v, want %v", got, first)
	}
	if got := current.PublishedArtifact(); got == nil || *got != second {
		t.Fatalf("published current revision = %#v, want %v", got, second)
	}
	if _, statErr := os.Stat(filepath.Join(root, "safe-skill")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("renamed prior projection still exists: %v", statErr)
	}
	contents, err := os.ReadFile(filepath.Join(current.Destination(), "SKILL.md"))
	if err != nil || !containsAll(string(contents), "inspect the result") {
		t.Fatalf("updated projection = %q, %v", contents, err)
	}
}

func TestManagedProjectionRefusesModifiedOrForeignContent(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	target := projectionTarget(t, "codex-project", skill.CodexAgent, root)
	ref := projectionRef(t, "skill-123", 1)
	content := projectionContent(t, "safe-skill", "Use for a bounded task.")
	published, err := skill.PublishSkillProjection(ref, content, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(filepath.Join(published.Destination(), "SKILL.md"), []byte("locally edited\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	drifted := skill.InspectSkillProjection(ref, content, target)
	if drifted.State() != skill.ProjectionDrifted {
		t.Fatalf("drifted state = %s", drifted.State())
	}
	_, err = skill.PublishSkillProjection(ref, content, target, nil)
	var conflict *skill.ProjectionConflictError
	if !errors.As(err, &conflict) || conflict.Status.State() != skill.ProjectionDrifted {
		t.Fatalf("publish drift error = %T %v", err, err)
	}

	foreign := projectionContent(t, "foreign-skill", "Use for a bounded task.")
	if err := os.Mkdir(filepath.Join(root, foreign.Name()), 0o750); err != nil {
		t.Fatal(err)
	}
	status := skill.InspectSkillProjection(projectionRef(t, "skill-456", 1), foreign, target)
	if status.State() != skill.ProjectionConflict {
		t.Fatalf("foreign occupied state = %s", status.State())
	}
}

func TestProjectionEnforcesAgentSpecificCompatibility(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".claude", "skills")
	claude := projectionTarget(t, "claude-project", skill.ClaudeCodeAgent, root)
	content := projectionContent(t, "review-change", "Use <carefully> when reviewing a bounded change.")
	projected, err := skill.ProjectSkill(projectionRef(t, "skill-claude", 1), content, claude)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(projected, "powercontext.json"))
	if err != nil || !containsAll(string(manifest), `"agent_kind": "claude_code"`) {
		t.Fatalf("Claude Code manifest = %q, %v", manifest, err)
	}

	codex := projectionTarget(t, "codex-project", skill.CodexAgent, filepath.Join(t.TempDir(), "skills"))
	if _, err := skill.ProjectSkill(projectionRef(t, "skill-codex", 1), content, codex); err == nil {
		t.Fatal("Codex accepted a description containing angle brackets")
	}
}

func projectionContent(t *testing.T, name, description string) skill.Content {
	t.Helper()
	content, err := skill.NewContent(
		name, description, "Regenerate clients, inspect the diff, and run contract tests.",
		[]string{"make api-generate-check passes", "make contract-test passes"},
	)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func projectionRef(t *testing.T, id string, revision int64) artifact.Ref {
	t.Helper()
	ref, err := artifact.NewRef(skill.Family, id, revision)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func projectionTarget(t *testing.T, id string, kind skill.AgentKind, root string) skill.AgentSkillTarget {
	t.Helper()
	target, err := skill.NewAgentSkillTarget(id, kind, skill.ProjectScope, root, true)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func containsAll(value string, fragments ...string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}
