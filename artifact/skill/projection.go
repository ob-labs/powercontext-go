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

package skill

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/artifact"
)

const (
	ProjectionSchema       = "powercontext.agent-skill-projection.v1"
	legacyProjectionSchema = "powercontext.codex-skill-projection.v1"

	MaxCodexProjectionNameLength             = 64
	MaxCodexProjectionDescriptionLength      = 1_024
	MaxClaudeCodeProjectionNameLength        = 64
	MaxClaudeCodeProjectionDescriptionLength = 1_536
)

var projectionNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type ProjectionState string

const (
	ProjectionUnpublished     ProjectionState = "unpublished"
	ProjectionCurrent         ProjectionState = "current"
	ProjectionUpdateAvailable ProjectionState = "update_available"
	ProjectionConflict        ProjectionState = "conflict"
	ProjectionDrifted         ProjectionState = "drifted"
	ProjectionIncompatible    ProjectionState = "incompatible"
)

type ProjectionStatus struct {
	state             ProjectionState
	destination       string
	publishedArtifact *artifact.Ref
	reason            string
}

func newProjectionStatus(
	state ProjectionState,
	destination string,
	published *artifact.Ref,
	reason string,
) ProjectionStatus {
	var copied *artifact.Ref
	if published != nil {
		value := *published
		copied = &value
	}
	return ProjectionStatus{
		state: state, destination: destination, publishedArtifact: copied, reason: reason,
	}
}

func (s ProjectionStatus) State() ProjectionState { return s.state }
func (s ProjectionStatus) Destination() string    { return s.destination }
func (s ProjectionStatus) PublishedArtifact() *artifact.Ref {
	if s.publishedArtifact == nil {
		return nil
	}
	value := *s.publishedArtifact
	return &value
}
func (s ProjectionStatus) Reason() string { return s.reason }

func (s ProjectionStatus) Equal(other ProjectionStatus) bool {
	if s.state != other.state || s.destination != other.destination || s.reason != other.reason {
		return false
	}
	if s.publishedArtifact == nil || other.publishedArtifact == nil {
		return s.publishedArtifact == nil && other.publishedArtifact == nil
	}
	return *s.publishedArtifact == *other.publishedArtifact
}

type ProjectionConflictError struct{ Status ProjectionStatus }

func (e *ProjectionConflictError) Error() string {
	if e.Status.reason != "" {
		return e.Status.reason
	}
	return string(e.Status.state)
}

func ProjectSkill(ref artifact.Ref, content Content, target AgentSkillTarget) (string, error) {
	if ref.Family() != Family {
		return "", fmt.Errorf("artifact must identify a managed Skill")
	}
	destination := filepath.Join(target.path, content.Name())
	return projectSkillTo(ref, content, target.agentKind, destination)
}

func InspectSkillProjection(
	ref artifact.Ref,
	content Content,
	target AgentSkillTarget,
) ProjectionStatus {
	root := target.path
	destination := filepath.Join(root, content.Name())
	if err := validateAgentProjection(content, destination, target.agentKind); err != nil {
		return newProjectionStatus(ProjectionIncompatible, destination, nil, err.Error())
	}

	projections := managedProjections(root, ref.ID(), target.agentKind)
	if len(projections) > 1 {
		return newProjectionStatus(
			ProjectionConflict, destination, nil,
			"multiple managed projections identify this Artifact",
		)
	}
	if len(projections) == 0 {
		if pathExists(destination) {
			return newProjectionStatus(
				ProjectionConflict, destination, nil,
				"the target Skill directory is already occupied",
			)
		}
		return newProjectionStatus(ProjectionUnpublished, destination, nil, "")
	}

	published := projections[0]
	if !projectionIsIntact(published.path, published.ref, target.agentKind) {
		return newProjectionStatus(
			ProjectionDrifted, destination, &published.ref,
			"the published package no longer matches its PowerContext manifest",
		)
	}
	if published.path != destination && pathExists(destination) {
		return newProjectionStatus(
			ProjectionConflict, destination, &published.ref,
			"the renamed target Skill directory is already occupied",
		)
	}
	if published.ref.Revision() > ref.Revision() {
		return newProjectionStatus(
			ProjectionConflict, destination, &published.ref,
			"a newer managed Skill Revision is already published",
		)
	}
	if published.ref.Revision() == ref.Revision() {
		if published.path == destination {
			return newProjectionStatus(ProjectionCurrent, destination, &published.ref, "")
		}
		return newProjectionStatus(
			ProjectionDrifted, destination, &published.ref,
			"the exact managed Skill Revision is published under a different directory name",
		)
	}
	return newProjectionStatus(ProjectionUpdateAvailable, destination, &published.ref, "")
}

func PublishSkillProjection(
	ref artifact.Ref,
	content Content,
	target AgentSkillTarget,
	expected *ProjectionStatus,
) (ProjectionStatus, error) {
	current := InspectSkillProjection(ref, content, target)
	if expected != nil && !current.Equal(*expected) {
		return ProjectionStatus{}, &ProjectionConflictError{Status: current}
	}
	if current.state == ProjectionCurrent {
		return current, nil
	}
	if current.state != ProjectionUnpublished && current.state != ProjectionUpdateAvailable {
		return ProjectionStatus{}, &ProjectionConflictError{Status: current}
	}

	root := target.path
	if err := os.MkdirAll(root, 0o750); err != nil {
		return ProjectionStatus{}, err
	}
	destination := filepath.Join(root, content.Name())
	var existing string
	if current.publishedArtifact != nil {
		projections := managedProjections(root, ref.ID(), target.agentKind)
		if len(projections) != 1 {
			status := InspectSkillProjection(ref, content, target)
			return ProjectionStatus{}, &ProjectionConflictError{Status: status}
		}
		existing = projections[0].path
	}

	temporary, err := os.MkdirTemp(root, ".powercontext-publish-")
	if err != nil {
		return ProjectionStatus{}, err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	staged, err := projectSkillTo(
		ref, content, target.agentKind, filepath.Join(temporary, "staged", content.Name()),
	)
	if err != nil {
		return ProjectionStatus{}, err
	}
	backup := filepath.Join(temporary, "previous")
	if existing != "" {
		if err := os.Rename(existing, backup); err != nil {
			return ProjectionStatus{}, err
		}
	}
	if err := os.Rename(staged, destination); err != nil {
		if existing != "" && pathExists(backup) && !pathExists(existing) {
			_ = os.Rename(backup, existing)
		}
		return ProjectionStatus{}, err
	}

	published := InspectSkillProjection(ref, content, target)
	if published.state != ProjectionCurrent {
		return ProjectionStatus{}, &ProjectionConflictError{Status: published}
	}
	return published, nil
}

func projectSkillTo(
	ref artifact.Ref,
	content Content,
	agentKind AgentKind,
	destination string,
) (string, error) {
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", err
	}
	destination = filepath.Clean(absolute)
	if validationErr := validateAgentProjection(content, destination, agentKind); validationErr != nil {
		return "", validationErr
	}
	if pathExists(destination) {
		return "", &os.PathError{Op: "project", Path: destination, Err: fs.ErrExist}
	}
	parent := filepath.Dir(destination)
	if mkdirErr := os.MkdirAll(parent, 0o750); mkdirErr != nil {
		return "", mkdirErr
	}
	temporary, err := os.MkdirTemp(parent, ".powercontext-skill-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	staging := filepath.Join(temporary, "projection")
	if mkdirErr := os.Mkdir(staging, 0o750); mkdirErr != nil {
		return "", mkdirErr
	}
	skillText, err := projectionMarkdown(ref, content)
	if err != nil {
		return "", err
	}
	if writeErr := os.WriteFile(filepath.Join(staging, "SKILL.md"), []byte(skillText), 0o600); writeErr != nil {
		return "", writeErr
	}
	manifest, err := projectionManifest(ref, agentKind, skillText)
	if err != nil {
		return "", err
	}
	if writeErr := os.WriteFile(filepath.Join(staging, "powercontext.json"), manifest, 0o600); writeErr != nil {
		return "", writeErr
	}
	if err := os.Rename(staging, destination); err != nil {
		return "", err
	}
	return destination, nil
}

func validateAgentProjection(content Content, destination string, agentKind AgentKind) error {
	maximumNameLength := MaxCodexProjectionNameLength
	maximumDescriptionLength := MaxCodexProjectionDescriptionLength
	if agentKind == ClaudeCodeAgent {
		maximumNameLength = MaxClaudeCodeProjectionNameLength
		maximumDescriptionLength = MaxClaudeCodeProjectionDescriptionLength
	}
	if utf8.RuneCountInString(content.Name()) > maximumNameLength ||
		!projectionNamePattern.MatchString(content.Name()) {
		return fmt.Errorf(
			"managed Skill name must be at most %d lowercase letters, digits, and single hyphens for %s",
			maximumNameLength, agentLabel(agentKind),
		)
	}
	if filepath.Base(destination) != content.Name() {
		return fmt.Errorf("%s Skill directory name must match the managed Skill name", agentLabel(agentKind))
	}
	invalidCodexDescription := agentKind == CodexAgent && strings.ContainsAny(content.Description(), "<>")
	if utf8.RuneCountInString(content.Description()) > maximumDescriptionLength || invalidCodexDescription {
		suffix := ""
		if agentKind == CodexAgent {
			suffix = " and contain no angle brackets"
		}
		return fmt.Errorf(
			"managed Skill description must be at most %d characters%s for %s",
			maximumDescriptionLength, suffix, agentLabel(agentKind),
		)
	}
	return nil
}

func agentLabel(agentKind AgentKind) string {
	if agentKind == CodexAgent {
		return "Codex"
	}
	return "Claude Code"
}

func projectionMarkdown(ref artifact.Ref, content Content) (string, error) {
	name, err := jsonString(content.Name())
	if err != nil {
		return "", err
	}
	description, err := jsonString(content.Description())
	if err != nil {
		return "", err
	}
	validation := make([]string, len(content.Validation()))
	for index, item := range content.Validation() {
		validation[index] = "- " + item
	}
	exactRef := fmt.Sprintf("artifact:%s/%s@%d", ref.Family(), ref.ID(), ref.Revision())
	return "---\n" +
		"name: " + name + "\n" +
		"description: " + description + "\n" +
		"---\n\n" +
		"<!-- Generated from " + exactRef + ". The Artifact Revision remains authoritative. -->\n\n" +
		trimPythonRight(content.Instructions()) + "\n\n" +
		"## Validation\n\n" +
		strings.Join(validation, "\n") + "\n", nil
}

func jsonString(value string) (string, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(encoded.String(), "\n"), nil
}

func trimPythonRight(value string) string {
	return strings.TrimRightFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || character >= '\u001c' && character <= '\u001f'
	})
}

func projectionManifest(ref artifact.Ref, agentKind AgentKind, skillText string) ([]byte, error) {
	digest := sha256.Sum256([]byte(skillText))
	value := map[string]any{
		"schema":     ProjectionSchema,
		"agent_kind": string(agentKind),
		"artifact": map[string]any{
			"family": ref.Family(), "artifact_id": ref.ID(), "revision": ref.Revision(),
		},
		"skill_sha256": hex.EncodeToString(digest[:]),
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return pythonASCIIJSON(encoded.Bytes()), nil
}

func pythonASCIIJSON(value []byte) []byte {
	if bytes.IndexFunc(value, func(r rune) bool { return r > unicode.MaxASCII }) < 0 {
		return value
	}
	var encoded strings.Builder
	encoded.Grow(len(value))
	for len(value) > 0 {
		r, size := utf8.DecodeRune(value)
		value = value[size:]
		if r <= unicode.MaxASCII {
			encoded.WriteRune(r)
			continue
		}
		if r <= 0xffff {
			fmt.Fprintf(&encoded, `\u%04x`, r)
			continue
		}
		r -= 0x10000
		fmt.Fprintf(&encoded, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
	}
	return []byte(encoded.String())
}

type managedProjection struct {
	path string
	ref  artifact.Ref
}

func managedProjections(root, artifactID string, agentKind AgentKind) []managedProjection {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	result := make([]managedProjection, 0)
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		packagePath := filepath.Join(root, entry.Name())
		published, ok := publishedArtifact(packagePath, agentKind)
		if ok && published.Family() == Family && published.ID() == artifactID {
			result = append(result, managedProjection{path: packagePath, ref: published})
		}
	}
	slices.SortFunc(result, func(left, right managedProjection) int {
		return strings.Compare(left.path, right.path)
	})
	return result
}

func publishedArtifact(packagePath string, agentKind AgentKind) (artifact.Ref, bool) {
	manifest, err := readProjectionManifest(filepath.Join(packagePath, "powercontext.json"))
	if err != nil || !manifestMatchesAgent(manifest, agentKind) {
		return artifact.Ref{}, false
	}
	ref, err := manifestArtifact(manifest)
	return ref, err == nil
}

func projectionIsIntact(packagePath string, ref artifact.Ref, agentKind AgentKind) bool {
	entries, err := os.ReadDir(packagePath)
	if err != nil || len(entries) != 2 || entries[0].Name() != "SKILL.md" || entries[1].Name() != "powercontext.json" {
		return false
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	manifest, err := readProjectionManifest(filepath.Join(packagePath, "powercontext.json"))
	if err != nil || !manifestMatchesAgent(manifest, agentKind) {
		return false
	}
	stored, err := manifestArtifact(manifest)
	if err != nil || stored != ref {
		return false
	}
	skillText, err := os.ReadFile(filepath.Join(packagePath, "SKILL.md"))
	if err != nil || !utf8.Valid(skillText) {
		return false
	}
	digest := sha256.Sum256(skillText)
	want, ok := manifest["skill_sha256"].(string)
	return ok && want == hex.EncodeToString(digest[:])
}

func readProjectionManifest(path string) (map[string]any, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("projection manifest is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	var manifest map[string]any
	if err := decoder.Decode(&manifest); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("projection manifest contains trailing JSON")
		}
		return nil, err
	}
	return manifest, nil
}

func manifestMatchesAgent(manifest map[string]any, agentKind AgentKind) bool {
	schema, _ := manifest["schema"].(string)
	if schema == ProjectionSchema {
		value, _ := manifest["agent_kind"].(string)
		return value == string(agentKind)
	}
	return schema == legacyProjectionSchema && agentKind == CodexAgent
}

func manifestArtifact(manifest map[string]any) (artifact.Ref, error) {
	value, ok := manifest["artifact"].(map[string]any)
	if !ok {
		return artifact.Ref{}, errors.New("projection artifact is invalid")
	}
	family, familyOK := value["family"].(string)
	id, idOK := value["artifact_id"].(string)
	revisionNumber, revisionOK := value["revision"].(json.Number)
	if !familyOK || !idOK || !revisionOK {
		return artifact.Ref{}, errors.New("projection artifact is invalid")
	}
	revision, err := revisionNumber.Int64()
	if err != nil {
		return artifact.Ref{}, err
	}
	return artifact.NewRef(family, id, revision)
}

func pathExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil || !errors.Is(err, os.ErrNotExist)
}
