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

package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

func TestExactManagedSkillProjectsToNewCodexSkillDirectory(t *testing.T) {
	value := testSkillArtifact("powercontext-openapi-change", "Use when changing PowerContext's public HTTP contract.")
	destination := filepath.Join(t.TempDir(), ".agents", "skills", value.Content.Name)
	projected, err := projectCodexSkill(value, destination)
	if err != nil {
		t.Fatal(err)
	}
	skillText, err := os.ReadFile(filepath.Join(projected, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`name: "powercontext-openapi-change"`,
		"Generated from artifact:skill/skill-123@2",
		"- make contract-test passes",
	} {
		if !strings.Contains(string(skillText), fragment) {
			t.Fatalf("projected Skill does not contain %q", fragment)
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(projected, "powercontext.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Schema      string               `json:"schema"`
		Artifact    v1.ArtifactReference `json:"artifact"`
		SkillSHA256 string               `json:"skill_sha256"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(skillText)
	if manifest.Schema != codexProjectionSchema || manifest.Artifact != value.Artifact || manifest.SkillSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected projection manifest: %#v", manifest)
	}
}

func TestCodexProjectionNeverOverwritesExistingDirectory(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "safe-skill")
	if err := os.Mkdir(destination, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := projectCodexSkill(testSkillArtifact("safe-skill", "Use for a bounded task."), destination); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("projection overwrite error = %v", err)
	}
}

func TestCodexProjectionRejectsInvalidManagedContent(t *testing.T) {
	for name, test := range map[string]struct{ skillName, description string }{
		"name":        {"Not-Hyphen-Case", "Use for a bounded task."},
		"description": {"safe-skill", "Do <anything> for a bounded task."},
	} {
		t.Run(name, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), test.skillName)
			if _, err := projectCodexSkill(testSkillArtifact(test.skillName, test.description), destination); err == nil {
				t.Fatal("invalid managed content was projected")
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("failed projection left destination behind: %v", err)
			}
		})
	}
}

func TestWorkBuddyProjectionUsesSharedAgentProjection(t *testing.T) {
	value := testSkillArtifact("safe-skill", "Use carefully for a bounded task.")
	destination := filepath.Join(t.TempDir(), ".workbuddy", "skills", value.Content.Name)
	projected, err := projectAgentSkill(value, destination, "workbuddy")
	if err != nil {
		t.Fatal(err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(projected, "powercontext.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Schema    string `json:"schema"`
		AgentKind string `json:"agent_kind"`
	}
	if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.Schema != codexProjectionSchema || manifest.AgentKind != "workbuddy" {
		t.Fatalf("WorkBuddy projection manifest = %s", manifestBytes)
	}
}

func testSkillArtifact(name, description string) v1.SkillArtifact {
	return v1.SkillArtifact{
		Artifact: v1.ArtifactReference{Family: "skill", ArtifactID: "skill-123", Revision: 2},
		Content: v1.SkillProposal{
			Name: name, Description: description,
			Instructions: "Regenerate clients, inspect the diff, and run contract tests.",
			Validation:   []v1.SkillValidationItem{"make api-generate-check passes", "make contract-test passes"},
		},
	}
}
