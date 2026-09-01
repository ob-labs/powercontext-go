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

package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/source"
)

func TestDashboardSkillProjectionStatusAndPublish(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	target, err := skill.NewAgentSkillTarget("codex-project", skill.CodexAgent, skill.ProjectScope, root, true)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := skill.NewAgentSkillProvider("workstation-1", []skill.AgentSkillTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	operations := projectionOperationsFixture(t, provider)
	mux := http.NewServeMux()
	if err := Mount(mux, Options{
		DashboardEnabled:  true,
		Scopes:            []Scope{{ScopeID: "project:example", DisplayName: "Example"}},
		AgentSkillTargets: []skill.AgentSkillTarget{target},
		SkillProjections:  operations,
	}); err != nil {
		t.Fatal(err)
	}
	selection := map[string]any{
		"scope_id": "project:example", "candidate_id": operations.candidate.ID(),
		"artifact": map[string]any{
			"family": operations.managed.Ref().Family(), "artifact_id": operations.managed.Ref().ID(),
			"revision": operations.managed.Ref().Revision(),
		},
	}

	status := requestJSON(t, mux, "/dashboard/skill-projections/status", selection)
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	assertProjectionResponse(t, status, "unpublished", "not_published")

	selection["target_id"] = "codex-project"
	published := requestJSON(t, mux, "/dashboard/skill-projections/publish", selection)
	if published.Code != http.StatusOK {
		t.Fatalf("publish = %d %s", published.Code, published.Body.String())
	}
	assertProjectionResponse(t, published, "current", "available")
	if _, err := os.Stat(filepath.Join(root, operations.managed.Content().Name(), "powercontext.json")); err != nil {
		t.Fatalf("published projection is absent: %v", err)
	}
	if operations.scanCalls != 1 {
		t.Fatalf("external Skill scans = %d, want 1", operations.scanCalls)
	}

	if err := os.WriteFile(
		filepath.Join(root, operations.managed.Content().Name(), "SKILL.md"), []byte("locally edited\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	conflict := requestJSON(t, mux, "/dashboard/skill-projections/publish", selection)
	if conflict.Code != http.StatusConflict || !bytes.Contains(conflict.Body.Bytes(), []byte(`"code":"skill_projection_conflict"`)) {
		t.Fatalf("drift publish = %d %s", conflict.Code, conflict.Body.String())
	}
}

func TestDashboardSkillProjectionPublishKeepsPublishedSkillWhenScanFails(t *testing.T) {
	mux, operations, selection := projectionPublishFixture(t)
	operations.scanErr = errors.New("scan failure /private/registry API_KEY=secret")

	published := requestJSON(t, mux, "/dashboard/skill-projections/publish", selection)
	assertPublishedProjectionRemainsCurrent(t, published, operations)
}

func TestDashboardSkillProjectionPublishKeepsPublishedSkillWhenDiscoveryListFails(t *testing.T) {
	mux, operations, selection := projectionPublishFixture(t)
	operations.listErr = errors.New("discovery failure /private/registry API_KEY=secret")

	published := requestJSON(t, mux, "/dashboard/skill-projections/publish", selection)
	assertPublishedProjectionRemainsCurrent(t, published, operations)
}

func assertPublishedProjectionRemainsCurrent(
	t *testing.T,
	published *httptest.ResponseRecorder,
	operations *projectionOperations,
) {
	t.Helper()
	if published.Code != http.StatusOK {
		t.Fatalf("publish = %d %s", published.Code, published.Body.String())
	}
	assertProjectionResponse(t, published, "current", "unavailable")
	if _, err := os.Stat(filepath.Join(operations.root, operations.managed.Content().Name(), "SKILL.md")); err != nil {
		t.Fatalf("published projection is absent: %v", err)
	}
	for _, secret := range []string{"/private/registry", "API_KEY=secret"} {
		if bytes.Contains(published.Body.Bytes(), []byte(secret)) {
			t.Fatalf("publish response leaked %q: %s", secret, published.Body.String())
		}
	}
}

func TestDashboardSkillProjectionRoutesValidateAuthorityAndScope(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "skills")
	target, err := skill.NewAgentSkillTarget("readonly", skill.CodexAgent, skill.ProjectScope, root, false)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := skill.NewAgentSkillProvider("workstation-1", []skill.AgentSkillTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	operations := projectionOperationsFixture(t, provider)
	mux := http.NewServeMux()
	if err := Mount(mux, Options{
		DashboardEnabled:  true,
		Scopes:            []Scope{{ScopeID: "project:example", DisplayName: "Example"}},
		AgentSkillTargets: []skill.AgentSkillTarget{target}, SkillProjections: operations,
	}); err != nil {
		t.Fatal(err)
	}
	selection := map[string]any{
		"scope_id": "project:unknown", "candidate_id": operations.candidate.ID(),
		"artifact": map[string]any{"family": "skill", "artifact_id": operations.managed.ID(), "revision": 1},
	}
	unknown := requestJSON(t, mux, "/dashboard/skill-projections/status", selection)
	if unknown.Code != http.StatusNotFound || !bytes.Contains(unknown.Body.Bytes(), []byte(`"code":"dashboard_scope_not_found"`)) {
		t.Fatalf("unknown scope = %d %s", unknown.Code, unknown.Body.String())
	}
	selection["scope_id"] = "project:example"
	selection["target_id"] = "readonly"
	readonly := requestJSON(t, mux, "/dashboard/skill-projections/publish", selection)
	if readonly.Code != http.StatusNotFound || !bytes.Contains(readonly.Body.Bytes(), []byte(`"code":"skill_publish_target_not_found"`)) {
		t.Fatalf("read-only target = %d %s", readonly.Code, readonly.Body.String())
	}
	selection["unexpected"] = true
	invalid := requestJSON(t, mux, "/dashboard/skill-projections/status", selection)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown request field = %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestSkillsAndReviewPagesExposeLatestProductSurface(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	if err := Mount(mux, Options{DashboardEnabled: true}); err != nil {
		t.Fatal(err)
	}
	for path, fragments := range map[string][]string{
		"/skills":  {`id="skills-library"`, `skills.js?v=agent-targets-v1`, `id="skills-delivery"`},
		"/reviews": {`id="review-inbox"`, `review.js?v=agent-targets-v1`, `id="review-publication"`},
	} {
		response := request(mux, http.MethodGet, path)
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s = %d %#v", path, response.Code, response.Header())
		}
		for _, fragment := range fragments {
			if !bytes.Contains(response.Body.Bytes(), []byte(fragment)) {
				t.Errorf("GET %s does not contain %q", path, fragment)
			}
		}
	}
}

type projectionOperations struct {
	candidate review.Snapshot
	managed   skill.Skill
	provider  *skill.AgentSkillProvider
	listed    []skill.Resolution
	scanCalls int
	root      string
	scanErr   error
	listErr   error
}

func projectionOperationsFixture(t *testing.T, provider *skill.AgentSkillProvider) *projectionOperations {
	t.Helper()
	content, err := skill.NewContent(
		"safe-skill", "Use for a bounded task.", "Perform the bounded task.",
		[]string{"The expected result exists."},
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := source.NewRef("content", "evidence")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := skill.NewDraft(content, []source.Ref{evidence}, nil)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := artifact.New("skill-123", 1, draft)
	if err != nil {
		t.Fatal(err)
	}
	result := managed.Ref()
	candidate, err := review.NewCandidate(
		"candidate-123", 1, skill.Family, review.Approved, content,
		[]source.Ref{evidence}, nil, nil, nil, &result, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return &projectionOperations{candidate: candidate, managed: managed, provider: provider}
}

func (o *projectionOperations) GetCandidate(context.Context, string, string) (review.Snapshot, error) {
	return o.candidate, nil
}

func (o *projectionOperations) GetSkill(context.Context, string, artifact.Ref) (skill.Skill, error) {
	return o.managed, nil
}

func (o *projectionOperations) ListExternalSkills(context.Context, string, bool) ([]skill.Resolution, error) {
	if o.listErr != nil {
		return nil, o.listErr
	}
	return append([]skill.Resolution(nil), o.listed...), nil
}

func (o *projectionOperations) ScanExternalSkills(ctx context.Context, _ string) (skill.ProviderScan, error) {
	o.scanCalls++
	if o.scanErr != nil {
		return skill.ProviderScan{}, o.scanErr
	}
	scan, err := o.provider.Scan(ctx)
	if err != nil {
		return skill.ProviderScan{}, err
	}
	o.listed = nil
	for _, registration := range scan.Registrations() {
		resolved, err := o.provider.Resolve(ctx, registration)
		if err != nil {
			return skill.ProviderScan{}, err
		}
		o.listed = append(o.listed, resolved)
	}
	return scan, nil
}

func projectionPublishFixture(t *testing.T) (*http.ServeMux, *projectionOperations, map[string]any) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	target, err := skill.NewAgentSkillTarget("codex-project", skill.CodexAgent, skill.ProjectScope, root, true)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := skill.NewAgentSkillProvider("workstation-1", []skill.AgentSkillTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	operations := projectionOperationsFixture(t, provider)
	operations.root = root
	mux := http.NewServeMux()
	if err := Mount(mux, Options{
		DashboardEnabled:  true,
		Scopes:            []Scope{{ScopeID: "project:example", DisplayName: "Example"}},
		AgentSkillTargets: []skill.AgentSkillTarget{target},
		SkillProjections:  operations,
	}); err != nil {
		t.Fatal(err)
	}
	return mux, operations, map[string]any{
		"scope_id": "project:example", "candidate_id": operations.candidate.ID(), "target_id": target.ID(),
		"artifact": map[string]any{
			"family": operations.managed.Ref().Family(), "artifact_id": operations.managed.Ref().ID(),
			"revision": operations.managed.Ref().Revision(),
		},
	}
}

func requestJSON(t *testing.T, handler http.Handler, target string, value any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(response, request)
	return response
}

func assertProjectionResponse(t *testing.T, response *httptest.ResponseRecorder, state, discovery string) {
	t.Helper()
	var value projectionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Targets) != 1 || value.Targets[0].State != state || value.Targets[0].Discovery != discovery {
		t.Fatalf("projection response = %#v", value)
	}
}
