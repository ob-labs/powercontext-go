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

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/server"
	"github.com/ob-labs/powercontext-go/source"
)

func TestHTTPSourceAndMemorySQLiteVerticalSlice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	workspaceRoot := t.TempDir()
	database, err := sqlstore.OpenSQLite(ctx, sqlstore.DefaultSQLiteConfig(filepath.Join(workspaceRoot, "powercontext.db")))
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := sqlstore.NewArtifactRepository(
		sqlstore.SQLiteDialect,
		sqlstore.MemoryArtifactCodec(),
		sqlstore.ExperienceArtifactCodec(),
		sqlstore.SkillArtifactCodec(),
		sqlstore.HandoffArtifactCodec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := sqlstore.NewSourceRepository(
		sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec(), sqlstore.ExternalSkillSnapshotSourceCodec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	sourceBackend, err := sqlstore.NewRuntimeSourceBackend(database, sources)
	if err != nil {
		t.Fatal(err)
	}

	lifecycle := runtime.New(database)
	t.Cleanup(func() {
		if closeErr := lifecycle.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	sourceApp, err := runtime.NewSourceApplication(lifecycle, sourceBackend)
	if err != nil {
		t.Fatal(err)
	}
	idFactory := deterministicMemoryIDs()
	memoryFactory := func(scopeID string) (*memory.Service, error) {
		repository, buildErr := sqlstore.NewMemoryRepository(database, scopeID, artifacts, nil)
		if buildErr != nil {
			return nil, buildErr
		}
		resolver, buildErr := sqlstore.NewMemorySourceResolver(database, scopeID, sources)
		if buildErr != nil {
			return nil, buildErr
		}
		return memory.NewService(repository, memory.ServiceOptions{
			CandidatePipeline: fixedMemoryCandidatePipeline{}, SourceResolver: resolver, IDFactory: idFactory,
		})
	}
	flushFactory := func(scopeID string) (runtime.MemoryFlushBackend, error) {
		repository, buildErr := sqlstore.NewMemoryRepository(database, scopeID, artifacts, nil)
		if buildErr != nil {
			return nil, buildErr
		}
		return sqlstore.NewMemoryFlushStore(database, scopeID, sources, repository)
	}
	memoryApp, err := runtime.NewMemoryApplicationWithFlush(
		lifecycle, memoryFactory, flushFactory, runtime.DefaultMemoryArtifactID, runtime.DefaultSourceWindowLimit,
	)
	if err != nil {
		t.Fatal(err)
	}
	contextApp, err := runtime.NewContextApplication(lifecycle, memoryApp, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := sqlstore.NewCandidateRepository(
		sqlstore.SQLiteDialect, sqlstore.ExperienceArtifactCodec(), sqlstore.SkillArtifactCodec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	reviewIDs := deterministicReviewIDs()
	reviewFactory := func(scopeID string) (*review.Service, error) {
		backend, buildErr := sqlstore.NewReviewBackend(database, scopeID, candidates, artifacts, sources, nil)
		if buildErr != nil {
			return nil, buildErr
		}
		return review.NewService(backend, reviewIDs)
	}
	reviewApp, err := runtime.NewReviewApplication(lifecycle, reviewFactory)
	if err != nil {
		t.Fatal(err)
	}
	generationFactory := func(scopeID string) (*review.GenerationService, error) {
		reviewService, buildErr := reviewFactory(scopeID)
		if buildErr != nil {
			return nil, buildErr
		}
		evidence, buildErr := sqlstore.NewGenerationEvidenceReader(database, scopeID, sources, artifacts)
		if buildErr != nil {
			return nil, buildErr
		}
		return review.NewGenerationService(
			evidence, reviewService, fixedExperienceGenerator{}, fixedSkillGenerator{},
		)
	}
	generationApp, err := runtime.NewGenerationApplication(lifecycle, generationFactory)
	if err != nil {
		t.Fatal(err)
	}
	externalRoot := filepath.Join(workspaceRoot, "external-skills")
	externalPackage := filepath.Join(externalRoot, "friendly-go")
	if mkdirErr := os.MkdirAll(externalPackage, 0o755); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	manifestPath := filepath.Join(externalPackage, "SKILL.md")
	if writeErr := os.WriteFile(manifestPath, []byte("---\nname: friendly-go\ndescription: Use when writing Go.\n---\n\nKeep boundaries explicit.\n"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	root, err := skill.NewCodexRoot("repository", skill.ProjectScope, externalRoot)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := skill.NewCodexProvider("workstation-1", []skill.CodexRoot{root})
	if err != nil {
		t.Fatal(err)
	}
	snapshotStore, err := sqlstore.NewExternalSkillSnapshotStore(database, sources)
	if err != nil {
		t.Fatal(err)
	}
	externalApp, err := runtime.NewExternalSkillApplication(
		lifecycle,
		func(scopeID string) (*skill.RegistryService, error) {
			store, buildErr := sqlstore.NewExternalSkillStore(database, scopeID)
			if buildErr != nil {
				return nil, buildErr
			}
			return skill.NewRegistryService(store, provider)
		},
		generationFactory,
		snapshotStore,
	)
	if err != nil {
		t.Fatal(err)
	}
	handoffFactory := func(scopeID string) (*handoff.Service, error) {
		memoryService, buildErr := memoryFactory(scopeID)
		if buildErr != nil {
			return nil, buildErr
		}
		backend, buildErr := sqlstore.NewHandoffBackend(database, scopeID, artifacts)
		if buildErr != nil {
			return nil, buildErr
		}
		resolver, buildErr := sqlstore.NewHandoffEvidenceResolver(
			database, scopeID, sources, artifacts, memoryService,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		return handoff.NewService(
			scopeID, runtime.DefaultHandoffArtifactID, backend, resolver, fixedHandoffPipeline{},
		)
	}
	activationStore, err := sqlstore.NewHandoffActivationStore(database, sources)
	if err != nil {
		t.Fatal(err)
	}
	handoffApp, err := runtime.NewHandoffApplication(lifecycle, handoffFactory, activationStore)
	if err != nil {
		t.Fatal(err)
	}
	workApp, err := runtime.NewWorkApplication(lifecycle, sourceBackend, handoffFactory)
	if err != nil {
		t.Fatal(err)
	}
	handoffReportStore, err := sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	if schemaErr := handoffReportStore.EnsureSchema(ctx); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	handoffReportReader, err := runtime.NewHandoffReportReader(handoffFactory)
	if err != nil {
		t.Fatal(err)
	}
	handoffReportApp, err := runtime.NewHandoffReportApplication(
		lifecycle,
		handoffReportStore,
		handoffReportReader,
		workApp,
		func() time.Time { return time.Date(2026, 8, 17, 13, 14, 15, 123456000, time.UTC) },
		deterministicHandoffReportIDs(),
		func(ctx context.Context) ([]string, error) { return sqlstore.HandoffScopeIDs(ctx, database) },
	)
	if err != nil {
		t.Fatal(err)
	}
	statisticsRepository, err := sqlstore.NewStatisticsRepository(sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	statisticsApp, err := runtime.NewStatisticsApplication(
		lifecycle,
		func(scopeID string) (runtime.StatisticsReader, error) {
			return sqlstore.NewScopedStatistics(
				database, scopeID, runtime.DefaultMemoryArtifactID, artifacts, statisticsRepository, nil,
			)
		},
		func() time.Time { return time.Date(2026, 8, 17, 13, 14, 15, 123456000, time.UTC) },
	)
	if err != nil {
		t.Fatal(err)
	}
	httpHandler, err := server.NewHTTPHandler(endpoint.NewHandler(endpoint.HandlerOptions{
		Sources:       sourceApp,
		Memory:        memoryApp,
		Context:       contextApp,
		Review:        reviewApp,
		Generation:    generationApp,
		External:      externalApp,
		Handoff:       handoffApp,
		Work:          workApp,
		HandoffReport: handoffReportApp,
		Statistics:    statisticsApp,
	}), server.HTTPOptions{HandoffReportRoutes: true})
	if err != nil {
		t.Fatal(err)
	}

	accepted := postJSON(t, httpHandler, "/v1/sources/content", map[string]any{
		"scope_id": "project:test", "source_id": "capture-1", "content": "hello <world>",
		"metadata": map[string]any{"nested": map[string]any{"value": 1}},
	})
	assertStatus(t, accepted, http.StatusAccepted)
	assertJSONPath(t, accepted, "status", "accepted")
	assertJSONNumber(t, accepted, "position", 1)

	idempotent := postJSON(t, httpHandler, "/v1/sources/content", map[string]any{
		"scope_id": "project:test", "source_id": "capture-1", "content": "hello <world>",
		"metadata": map[string]any{"nested": map[string]any{"value": 1}},
	})
	assertStatus(t, idempotent, http.StatusAccepted)
	assertJSONNumber(t, idempotent, "position", 1)

	conflict := postJSON(t, httpHandler, "/v1/sources/content", map[string]any{
		"scope_id": "project:test", "source_id": "capture-1", "content": "different",
	})
	assertError(t, conflict, http.StatusConflict, "source_conflict")

	missingBoundary := postJSON(t, httpHandler, "/v1/handoff/activate", map[string]any{
		"scope_id": "project:test", "boundary_source": map[string]any{
			"name": "content", "source_id": "missing-boundary",
		},
		"objective": "Transfer the current implementation state.",
	})
	assertError(t, missingBoundary, http.StatusNotFound, "handoff_evidence_not_found")

	activated := postJSON(t, httpHandler, "/v1/handoff/activate", map[string]any{
		"scope_id": "project:test", "boundary_source": map[string]any{
			"name": "content", "source_id": "capture-1",
		},
		"objective": "Transfer the current implementation state.",
	})
	assertStatus(t, activated, http.StatusOK)
	activationBody := object(t, activated)
	if activationBody["status"] != "generated" || activationBody["previous_position"] != float64(0) || activationBody["current_position"] != float64(1) {
		t.Fatalf("Handoff activation = %#v", activationBody)
	}
	handoffDraft := fieldObject(t, activationBody, "draft")
	if handoffDraft["next_action"] != nil {
		t.Fatalf("Handoff next action = %#v, want null", handoffDraft["next_action"])
	}

	repeatedActivation := postJSON(t, httpHandler, "/v1/handoff/activate", map[string]any{
		"scope_id": "project:test", "boundary_source": map[string]any{
			"name": "content", "source_id": "capture-1",
		},
		"objective": "Transfer the current implementation state.",
	})
	assertStatus(t, repeatedActivation, http.StatusOK)
	if body := object(t, repeatedActivation); body["status"] != "ignored" || body["draft"] != nil || body["current_position"] != float64(1) {
		t.Fatalf("repeated Handoff activation = %#v", body)
	}

	state := handoffDraft["state"].([]any)
	state[0].(map[string]any)["text"] = "The full Go Handoff lifecycle is connected."
	preparedResponse := postJSON(t, httpHandler, "/v1/handoff/finalize", map[string]any{
		"scope_id": "project:test", "draft": handoffDraft,
	})
	assertStatus(t, preparedResponse, http.StatusOK)
	preparedHandoff := object(t, preparedResponse)
	if preparedHandoff["schema"] != "powercontext.prepared-handoff.v1" || preparedHandoff["base"] != nil {
		t.Fatalf("Prepared Handoff = %#v", preparedHandoff)
	}

	temporary := postJSON(t, httpHandler, "/v1/handoff/continue", map[string]any{
		"scope_id": "project:test", "selection": "prepared", "prepared": preparedHandoff,
	})
	assertStatus(t, temporary, http.StatusOK)
	if body := object(t, temporary); body["trust"] != "untrusted_history" || body["selection"] != "prepared" || body["selected_revision"] != nil {
		t.Fatalf("temporary Handoff resolution = %#v", body)
	}

	committedResponse := postJSON(t, httpHandler, "/v1/handoff/commit", map[string]any{
		"scope_id": "project:test", "handoff": preparedHandoff,
	})
	assertStatus(t, committedResponse, http.StatusOK)
	committedHandoff := object(t, committedResponse)
	handoffRef := fieldObject(t, committedHandoff, "reference")
	if handoffRef["family"] != "handoff" || handoffRef["artifact_id"] != "handoff" || handoffRef["revision"] != float64(1) {
		t.Fatalf("Committed Handoff = %#v", committedHandoff)
	}
	if refs := fieldArray(t, committedHandoff, "source_refs"); len(refs) != 1 || refs[0].(map[string]any)["source_id"] != "capture-1" {
		t.Fatalf("Handoff Source lineage = %#v", refs)
	}
	idempotentCommit := postJSON(t, httpHandler, "/v1/handoff/commit", map[string]any{
		"scope_id": "project:test", "handoff": preparedHandoff,
	})
	assertStatus(t, idempotentCommit, http.StatusOK)
	if ref := fieldObject(t, object(t, idempotentCommit), "reference"); ref["revision"] != float64(1) {
		t.Fatalf("idempotent Handoff commit = %#v", ref)
	}

	exact := postJSON(t, httpHandler, "/v1/handoff/continue", map[string]any{
		"scope_id": "project:test", "selection": "exact", "revision": handoffRef,
	})
	assertStatus(t, exact, http.StatusOK)
	if body := object(t, exact); body["selection"] != "exact" || fieldObject(t, body, "selected_revision")["revision"] != float64(1) {
		t.Fatalf("exact Handoff resolution = %#v", body)
	}
	latest := postJSON(t, httpHandler, "/v1/handoff/continue", map[string]any{
		"scope_id": "project:test", "selection": "latest",
	})
	assertStatus(t, latest, http.StatusOK)
	if body := object(t, latest); body["selection"] != "latest" || fieldObject(t, body, "current_revision")["revision"] != float64(1) {
		t.Fatalf("latest Handoff resolution = %#v", body)
	}
	emptyHandoff := postJSON(t, httpHandler, "/v1/handoff/continue", map[string]any{
		"scope_id": "project:empty", "selection": "latest",
	})
	assertStatus(t, emptyHandoff, http.StatusOK)
	if body := object(t, emptyHandoff); body["status"] != "empty" || body["content"] != nil || body["selection"] != nil || body["selected_revision"] != nil || body["current_revision"] != nil {
		t.Fatalf("empty Handoff resolution = %#v", body)
	}
	invalidSelection := postJSON(t, httpHandler, "/v1/handoff/continue", map[string]any{
		"scope_id": "project:test", "selection": "latest", "revision": handoffRef,
	})
	assertError(t, invalidSelection, http.StatusUnprocessableEntity, "invalid_request")

	prepareRevision := func(text string) map[string]any {
		t.Helper()
		generated := postJSON(t, httpHandler, "/v1/handoff/prepare", map[string]any{
			"scope_id": "project:test", "objective": "Prepare the next milestone.",
			"evidence": []any{map[string]any{
				"kind": "source", "source_ref": map[string]any{"name": "content", "source_id": "capture-1"},
			}},
		})
		assertStatus(t, generated, http.StatusOK)
		draft := object(t, generated)
		draft["state"].([]any)[0].(map[string]any)["text"] = text
		finalized := postJSON(t, httpHandler, "/v1/handoff/finalize", map[string]any{
			"scope_id": "project:test", "draft": draft,
		})
		assertStatus(t, finalized, http.StatusOK)
		value := object(t, finalized)
		if fieldObject(t, value, "base")["revision"] != float64(1) {
			t.Fatalf("prepared Handoff base = %#v", value["base"])
		}
		return value
	}
	preparedA := prepareRevision("First competing milestone.")
	preparedB := prepareRevision("Second competing milestone.")
	committedA := postJSON(t, httpHandler, "/v1/handoff/commit", map[string]any{
		"scope_id": "project:test", "handoff": preparedA,
	})
	assertStatus(t, committedA, http.StatusOK)
	if ref := fieldObject(t, object(t, committedA), "reference"); ref["revision"] != float64(2) {
		t.Fatalf("revised Handoff = %#v", ref)
	}
	staleHandoff := postJSON(t, httpHandler, "/v1/handoff/commit", map[string]any{
		"scope_id": "project:test", "handoff": preparedB,
	})
	assertError(t, staleHandoff, http.StatusConflict, "revision_conflict")

	createdReportProject := postJSON(t, httpHandler, "/v1/handoff-reports/projects/create", map[string]any{
		"project_key": "powercontext-go", "title": "PowerContext Go",
	})
	assertStatus(t, createdReportProject, http.StatusCreated)
	reportProject := object(t, createdReportProject)
	if reportProject["project_id"] != "prj_report-1" || reportProject["version"] != float64(1) ||
		reportProject["default_locale"] != "zh-CN" || reportProject["timezone"] != "UTC" {
		t.Fatalf("created Report Project = %#v", reportProject)
	}

	listedReportProjects := postJSON(t, httpHandler, "/v1/handoff-reports/projects/list", map[string]any{})
	assertStatus(t, listedReportProjects, http.StatusOK)
	if projects := fieldArray(t, object(t, listedReportProjects), "items"); len(projects) != 1 {
		t.Fatalf("Report Project page = %#v", projects)
	}
	gotReportProject := postJSON(t, httpHandler, "/v1/handoff-reports/projects/get", map[string]any{
		"project_id": "prj_report-1",
	})
	assertStatus(t, gotReportProject, http.StatusOK)

	reportProject["title"] = "PowerContext Go Migration"
	reportProject["version"] = 2
	updatedReportProject := postJSON(t, httpHandler, "/v1/handoff-reports/projects/update", map[string]any{
		"project": reportProject, "expected_version": 1,
	})
	assertStatus(t, updatedReportProject, http.StatusOK)
	reportProject = object(t, updatedReportProject)
	if reportProject["version"] != float64(2) || reportProject["title"] != "PowerContext Go Migration" {
		t.Fatalf("updated Report Project = %#v", reportProject)
	}
	staleReportProject := postJSON(t, httpHandler, "/v1/handoff-reports/projects/update", map[string]any{
		"project": reportProject, "expected_version": 1,
	})
	assertError(t, staleReportProject, http.StatusConflict, "project_conflict")

	registeredWorkstream := postJSON(t, httpHandler, "/v1/handoff-reports/workstreams/register", map[string]any{
		"project_id": "prj_report-1", "scope_id": "project:test", "key": "migration",
		"title": "Go migration", "kind": "feature",
		"external_refs": []any{map[string]any{
			"kind": "issue", "provider": "github", "external_id": "42", "url": "https://github.com/ob-labs/powercontext-go/issues/42",
		}},
		"labels": []any{"go", "migration"},
	})
	assertStatus(t, registeredWorkstream, http.StatusCreated)
	workstream := object(t, registeredWorkstream)
	if workstream["scope_id"] != "project:test" || workstream["version"] != float64(1) {
		t.Fatalf("registered Workstream = %#v", workstream)
	}
	listedWorkstreams := postJSON(t, httpHandler, "/v1/handoff-reports/workstreams/list", map[string]any{
		"project_id": "prj_report-1",
	})
	assertStatus(t, listedWorkstreams, http.StatusOK)
	if workstreams := fieldArray(t, object(t, listedWorkstreams), "items"); len(workstreams) != 1 {
		t.Fatalf("Workstream page = %#v", workstreams)
	}
	workstream["title"] = "Industrial Go migration"
	workstream["version"] = 2
	updatedWorkstream := postJSON(t, httpHandler, "/v1/handoff-reports/workstreams/update", map[string]any{
		"workstream": workstream, "expected_version": 1,
	})
	assertStatus(t, updatedWorkstream, http.StatusOK)
	if body := object(t, updatedWorkstream); body["version"] != float64(2) || body["title"] != "Industrial Go migration" {
		t.Fatalf("updated Workstream = %#v", body)
	}

	repositoryRef := map[string]any{
		"provider": "github", "repository_id": "ob-labs/powercontext-go",
		"normalized_remote": "https://github.com/ob-labs/powercontext-go.git", "subpath": nil,
	}
	attachedWorkspace := postJSON(t, httpHandler, "/v1/handoff-reports/workspace-bindings/attach", map[string]any{
		"workspace_instance_id": "workspace-1", "project_id": "prj_report-1",
		"repository_ref": repositoryRef, "expected_version": nil,
	})
	assertStatus(t, attachedWorkspace, http.StatusOK)
	if body := object(t, attachedWorkspace); body["state"] != "confirmed" || body["version"] != float64(1) {
		t.Fatalf("attached Workspace = %#v", body)
	}
	gotWorkspace := postJSON(t, httpHandler, "/v1/handoff-reports/workspace-bindings/get", map[string]any{
		"workspace_instance_id": "workspace-1",
	})
	assertStatus(t, gotWorkspace, http.StatusOK)

	activityInput := map[string]any{
		"project_id": "prj_report-1", "scope_id": "project:test",
		"source": "coding_session", "source_event_id": "session-1",
		"occurred_at": "2026-08-17T20:00:00+08:00", "time_basis": "source_reported",
		"title": "Migration session", "summary": "Connected the report transport.",
		"agent":      map[string]any{"provider": "codex", "label": "desktop"},
		"session_id": "session-1", "vcs_context": map[string]any{"branch": "main", "head_revision": "abc123"},
		"evidence_refs": []any{},
	}
	recordedActivity := postJSON(t, httpHandler, "/v1/handoff-reports/activities/record", activityInput)
	assertStatus(t, recordedActivity, http.StatusCreated)
	recorded := object(t, recordedActivity)
	if recorded["cursor"] != float64(1) || fieldObject(t, recorded, "event")["event_id"] != "evt_report-1" {
		t.Fatalf("recorded Activity = %#v", recorded)
	}
	idempotentActivity := postJSON(t, httpHandler, "/v1/handoff-reports/activities/record", activityInput)
	assertStatus(t, idempotentActivity, http.StatusCreated)
	if body := object(t, idempotentActivity); body["cursor"] != float64(1) || fieldObject(t, body, "event")["event_id"] != "evt_report-1" {
		t.Fatalf("idempotent Activity = %#v", body)
	}
	conflictingActivityInput := maps.Clone(activityInput)
	conflictingActivityInput["summary"] = "Different content."
	conflictingActivity := postJSON(t, httpHandler, "/v1/handoff-reports/activities/record", conflictingActivityInput)
	assertError(t, conflictingActivity, http.StatusConflict, "activity_event_conflict")

	listedActivities := postJSON(t, httpHandler, "/v1/handoff-reports/activities/list", map[string]any{
		"project_id": "prj_report-1", "after_cursor": 0,
	})
	assertStatus(t, listedActivities, http.StatusOK)
	activityPage := object(t, listedActivities)
	if activityPage["high_watermark"] != float64(1) || activityPage["next_cursor"] != nil || len(fieldArray(t, activityPage, "items")) != 1 {
		t.Fatalf("Activity page = %#v", activityPage)
	}

	markdownReport := postJSON(t, httpHandler, "/v1/handoff-reports/get", map[string]any{
		"scope_id": "project:test", "project_id": "prj_report-1", "format": "markdown", "download": true,
	})
	assertStatus(t, markdownReport, http.StatusOK)
	if !strings.HasPrefix(markdownReport.Header().Get("Content-Type"), "text/markdown") ||
		markdownReport.Header().Get("Cache-Control") != "no-store" ||
		markdownReport.Header().Get("Content-Disposition") != `attachment; filename="handoff-report.md"` ||
		!strings.Contains(markdownReport.Body.String(), "PowerContext 项目交接报告") {
		t.Fatalf("Markdown Report = headers:%#v body:%q", markdownReport.Header(), markdownReport.Body.String())
	}
	if markdownReport.Header().Get("X-PowerContext-Selection-Digest") == "" || markdownReport.Header().Get("X-PowerContext-Report-Digest") == "" {
		t.Fatalf("Markdown Report digest headers = %#v", markdownReport.Header())
	}

	jsonReport := postJSON(t, httpHandler, "/v1/handoff-reports/get", map[string]any{
		"scope_id": "project:test", "project_id": "prj_report-1", "format": "json", "include_evidence_checks": false,
		"period": map[string]any{
			"start": "2026-08-17T00:00:00Z", "end": "2026-08-18T00:00:00Z",
			"timezone": "Asia/Shanghai", "compare_to_previous_period": true,
		},
	})
	assertStatus(t, jsonReport, http.StatusOK)
	jsonReportBody := object(t, jsonReport)
	if jsonReportBody["format"] != "json" || jsonReportBody["markdown"] != nil {
		t.Fatalf("JSON Report envelope = %#v", jsonReportBody)
	}
	reportPayload := fieldObject(t, jsonReportBody, "report")
	if reportPayload["report_kind"] != "periodic" || reportPayload["project_revision"] != float64(1) ||
		len(fieldArray(t, reportPayload, "workstreams")) != 1 {
		t.Fatalf("JSON Report = %#v", reportPayload)
	}

	detachedWorkspace := postJSON(t, httpHandler, "/v1/handoff-reports/workspace-bindings/detach", map[string]any{
		"workspace_instance_id": "workspace-1", "expected_version": 1,
	})
	assertStatus(t, detachedWorkspace, http.StatusOK)
	if body := object(t, detachedWorkspace); body["state"] != "detached" || body["version"] != float64(2) {
		t.Fatalf("detached Workspace = %#v", body)
	}
	missingWorkspace := postJSON(t, httpHandler, "/v1/handoff-reports/workspace-bindings/get", map[string]any{
		"workspace_instance_id": "workspace-1",
	})
	assertError(t, missingWorkspace, http.StatusNotFound, "workspace_not_bound")

	purgedActivities := postJSON(t, httpHandler, "/v1/handoff-reports/activities/purge", map[string]any{
		"project_id": "prj_report-1", "observed_before": "2026-08-17T13:14:16Z",
	})
	assertStatus(t, purgedActivities, http.StatusOK)
	assertJSONNumber(t, purgedActivities, "deleted_count", 1)

	var cursorGeneration int64
	var cursorPayload []byte
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT cursor, generation FROM pc_source_cursors
		WHERE scope_id = ? AND binding_name = ?`, "project:test", "handoff-participant-boundary").Scan(
		&cursorPayload, &cursorGeneration,
	); err != nil {
		t.Fatal(err)
	}
	if string(cursorPayload) != `{"sequence":1}` || cursorGeneration != 1 {
		t.Fatalf("Handoff activation cursor = (%s, %d)", cursorPayload, cursorGeneration)
	}

	remembered := postJSON(t, httpHandler, "/v1/memory/remember", map[string]any{
		"scope_id": "project:test", "kind": " fact ", "text": " Remember this. ", "reason": "seed",
	})
	assertStatus(t, remembered, http.StatusOK)
	rememberedBody := object(t, remembered)
	memoryRef := fieldObject(t, rememberedBody, "memory")
	if memoryRef["artifact_id"] != "memory" || memoryRef["revision"] != float64(1) {
		t.Fatalf("remembered Memory = %#v", memoryRef)
	}
	firstEntry := fieldObject(t, rememberedBody, "entry")
	if firstEntry["kind"] != "fact" || firstEntry["text"] != "Remember this." || firstEntry["state"] != "active" {
		t.Fatalf("remembered entry = %#v", firstEntry)
	}
	firstCitation := fieldObject(t, firstEntry, "citation")

	listed := postJSON(t, httpHandler, "/v1/memory/entries/list", map[string]any{"scope_id": "project:test"})
	assertStatus(t, listed, http.StatusOK)
	if entries := fieldArray(t, object(t, listed), "entries"); len(entries) != 1 {
		t.Fatalf("listed entries = %#v", entries)
	}

	got := postJSON(t, httpHandler, "/v1/memory/entries/get", map[string]any{
		"scope_id": "project:test", "citation": firstCitation,
	})
	assertStatus(t, got, http.StatusOK)
	assertJSONPath(t, got, "text", "Remember this.")

	revised := postJSON(t, httpHandler, "/v1/memory/entries/revise", map[string]any{
		"scope_id": "project:test", "citation": firstCitation,
		"kind": "decision", "text": "Remember this exactly.", "reason": "new observation",
	})
	assertStatus(t, revised, http.StatusOK)
	revisedBody := object(t, revised)
	if fieldObject(t, revisedBody, "memory")["revision"] != float64(2) {
		t.Fatalf("revised response = %#v", revisedBody)
	}
	currentCitation := fieldObject(t, fieldObject(t, revisedBody, "entry"), "citation")

	stale := postJSON(t, httpHandler, "/v1/memory/entries/retire", map[string]any{
		"scope_id": "project:test", "citation": firstCitation,
	})
	assertError(t, stale, http.StatusConflict, "revision_conflict")

	retired := postJSON(t, httpHandler, "/v1/memory/entries/retire", map[string]any{
		"scope_id": "project:test", "citation": currentCitation, "reason": "obsolete",
	})
	assertStatus(t, retired, http.StatusOK)
	retiredBody := object(t, retired)
	if fieldObject(t, retiredBody, "memory")["revision"] != float64(3) || fieldObject(t, retiredBody, "entry")["state"] != "inactive" {
		t.Fatalf("retired response = %#v", retiredBody)
	}

	active := postJSON(t, httpHandler, "/v1/memory/entries/list", map[string]any{"scope_id": "project:test"})
	if entries := fieldArray(t, object(t, active), "entries"); len(entries) != 0 {
		t.Fatalf("active entries = %#v", entries)
	}
	audit := postJSON(t, httpHandler, "/v1/memory/entries/list", map[string]any{
		"scope_id": "project:test", "include_inactive": true,
	})
	if entries := fieldArray(t, object(t, audit), "entries"); len(entries) != 1 {
		t.Fatalf("audit entries = %#v", entries)
	}

	changes := postJSON(t, httpHandler, "/v1/memory/changes", map[string]any{
		"scope_id": "project:test", "since_revision": 0,
	})
	assertStatus(t, changes, http.StatusOK)
	revisions := fieldArray(t, object(t, changes), "revisions")
	if len(revisions) != 3 {
		t.Fatalf("revision changes = %#v", revisions)
	}
	wantOperations := []string{"add", "revise", "deactivate"}
	for index, expected := range wantOperations {
		revision := revisions[index].(map[string]any)
		change := revision["changes"].([]any)[0].(map[string]any)
		if change["op"] != expected {
			t.Fatalf("revision %d operation = %#v, want %q", index+1, change["op"], expected)
		}
	}

	flushed := postJSON(t, httpHandler, "/v1/memory/flush", map[string]any{"scope_id": "project:test"})
	assertStatus(t, flushed, http.StatusOK)
	flushedBody := object(t, flushed)
	if flushedBody["status"] != "processed" || flushedBody["previous_cursor"] != float64(0) ||
		flushedBody["current_cursor"] != float64(1) || flushedBody["high_watermark"] != float64(1) ||
		flushedBody["processed_source_count"] != float64(1) {
		t.Fatalf("Memory Flush = %#v", flushedBody)
	}
	if ref := fieldObject(t, flushedBody, "memory"); ref["artifact_id"] != "memory" || ref["revision"] != float64(4) {
		t.Fatalf("flushed Memory = %#v", ref)
	}
	idleFlush := postJSON(t, httpHandler, "/v1/memory/flush", map[string]any{"scope_id": "project:test"})
	assertStatus(t, idleFlush, http.StatusOK)
	if body := object(t, idleFlush); body["status"] != "idle" || body["memory"] != nil ||
		body["previous_cursor"] != float64(1) || body["current_cursor"] != float64(1) {
		t.Fatalf("idle Memory Flush = %#v", body)
	}

	unsupportedSearch := postJSON(t, httpHandler, "/v1/memory/search", map[string]any{
		"scope_id": "project:test", "query": "remember", "mode": "auto",
	})
	assertError(t, unsupportedSearch, http.StatusUnprocessableEntity, "capability_not_supported")

	emptyContext := postJSON(t, httpHandler, "/v1/context/prepare", map[string]any{
		"scope_id": "project:empty", "query": "nothing stored",
	})
	assertStatus(t, emptyContext, http.StatusOK)
	emptyBody := object(t, emptyContext)
	if emptyBody["schema"] != "powercontext.prepared-context.v1" || emptyBody["status"] != "empty" || emptyBody["content"] != nil || emptyBody["content_bytes"] != float64(0) {
		t.Fatalf("empty prepared context = %#v", emptyBody)
	}

	proposedExperience := postJSON(t, httpHandler, "/v1/experience/propose", map[string]any{
		"scope_id": "project:test",
		"proposal": map[string]any{
			"situation": "The contract changed.", "action": "Regenerate the client.",
			"outcome": "The transport matches.", "lesson": "Regenerate before contract tests.",
		},
		"source_refs":   []any{map[string]any{"name": "content", "source_id": "capture-1"}},
		"artifact_refs": []any{},
	})
	assertStatus(t, proposedExperience, http.StatusCreated)
	experienceCandidate := object(t, proposedExperience)
	if experienceCandidate["candidate_id"] != "candidate-1" || experienceCandidate["version"] != float64(1) || experienceCandidate["status"] != "pending" {
		t.Fatalf("proposed Experience = %#v", experienceCandidate)
	}
	if experienceCandidate["target"] != nil || experienceCandidate["result_artifact"] != nil {
		t.Fatalf("nullable Candidate references = %#v", experienceCandidate)
	}
	gotCandidate := postJSON(t, httpHandler, "/v1/artifact-candidates/get", map[string]any{
		"scope_id": "project:test", "candidate_id": "candidate-1",
	})
	assertStatus(t, gotCandidate, http.StatusOK)
	if object(t, gotCandidate)["candidate_id"] != "candidate-1" {
		t.Fatalf("retrieved Candidate = %s", gotCandidate.Body.String())
	}

	pending := postJSON(t, httpHandler, "/v1/artifact-candidates/list", map[string]any{
		"scope_id": "project:test", "family": "experience",
	})
	assertStatus(t, pending, http.StatusOK)
	if values := fieldArray(t, object(t, pending), "candidates"); len(values) != 1 {
		t.Fatalf("pending Candidates = %#v", values)
	}
	if object(t, pending)["next_cursor"] != nil {
		t.Fatalf("next cursor = %#v, want null", object(t, pending)["next_cursor"])
	}

	revisedCandidate := postJSON(t, httpHandler, "/v1/artifact-candidates/revise", map[string]any{
		"scope_id": "project:test", "candidate_id": "candidate-1", "expected_version": 1,
		"proposal": map[string]any{
			"situation": "The contract changed.", "action": "Regenerate and inspect the client.",
			"outcome": "The checked-in transport matches.", "lesson": "Inspect generated changes before tests.",
		},
		"source_refs":   []any{map[string]any{"name": "content", "source_id": "capture-1"}},
		"artifact_refs": []any{},
	})
	assertStatus(t, revisedCandidate, http.StatusOK)
	assertJSONNumber(t, revisedCandidate, "version", 2)

	staleApproval := postJSON(t, httpHandler, "/v1/artifact-candidates/approve", map[string]any{
		"scope_id": "project:test", "candidate_id": "candidate-1", "expected_version": 1,
	})
	assertError(t, staleApproval, http.StatusConflict, "candidate_conflict")

	approvedExperience := postJSON(t, httpHandler, "/v1/artifact-candidates/approve", map[string]any{
		"scope_id": "project:test", "candidate_id": "candidate-1", "expected_version": 2,
	})
	assertStatus(t, approvedExperience, http.StatusOK)
	approvedExperienceBody := object(t, approvedExperience)
	if approvedExperienceBody["status"] != "approved" {
		t.Fatalf("approved Experience Candidate = %#v", approvedExperienceBody)
	}
	experienceRef := fieldObject(t, approvedExperienceBody, "result_artifact")
	if experienceRef["family"] != "experience" || experienceRef["artifact_id"] != "experience-1" || experienceRef["revision"] != float64(1) {
		t.Fatalf("Experience result = %#v", experienceRef)
	}

	terminalApproval := postJSON(t, httpHandler, "/v1/artifact-candidates/approve", map[string]any{
		"scope_id": "project:test", "candidate_id": "candidate-1", "expected_version": 2,
	})
	assertError(t, terminalApproval, http.StatusConflict, "candidate_terminal")

	gotExperience := postJSON(t, httpHandler, "/v1/experience/get", map[string]any{
		"scope_id": "project:test", "artifact": experienceRef,
	})
	assertStatus(t, gotExperience, http.StatusOK)
	if fieldObject(t, object(t, gotExperience), "content")["action"] != "Regenerate and inspect the client." {
		t.Fatalf("Experience = %s", gotExperience.Body.String())
	}

	proposedSkill := postJSON(t, httpHandler, "/v1/skill/propose", map[string]any{
		"scope_id": "project:test",
		"proposal": map[string]any{
			"name": "contract-check", "description": "Keep generated transport current.",
			"instructions": "Regenerate the client and inspect the diff.",
			"validation":   []any{"Run contract tests."},
		},
		"source_refs":   []any{},
		"artifact_refs": []any{experienceRef},
	})
	assertStatus(t, proposedSkill, http.StatusCreated)
	if object(t, proposedSkill)["candidate_id"] != "candidate-2" {
		t.Fatalf("proposed Skill = %s", proposedSkill.Body.String())
	}
	approvedSkill := postJSON(t, httpHandler, "/v1/artifact-candidates/approve", map[string]any{
		"scope_id": "project:test", "candidate_id": "candidate-2", "expected_version": 1,
	})
	assertStatus(t, approvedSkill, http.StatusOK)
	skillRef := fieldObject(t, object(t, approvedSkill), "result_artifact")
	gotSkill := postJSON(t, httpHandler, "/v1/skill/get", map[string]any{
		"scope_id": "project:test", "artifact": skillRef,
	})
	assertStatus(t, gotSkill, http.StatusOK)
	if fieldObject(t, object(t, gotSkill), "content")["name"] != "contract-check" {
		t.Fatalf("Skill = %s", gotSkill.Body.String())
	}

	generatedExperience := postJSON(t, httpHandler, "/v1/experience/generate", map[string]any{
		"scope_id":      "project:test",
		"source_refs":   []any{map[string]any{"name": "content", "source_id": "capture-1"}},
		"artifact_refs": []any{},
	})
	assertStatus(t, generatedExperience, http.StatusOK)
	if body := object(t, generatedExperience); body["status"] != "pending" || fieldObject(t, body, "candidate")["candidate_id"] != "candidate-3" {
		t.Fatalf("generated Experience = %#v", body)
	}

	generatedSkill := postJSON(t, httpHandler, "/v1/skill/generate", map[string]any{
		"scope_id": "project:test", "origin": "experience",
		"source_refs": []any{}, "artifact_refs": []any{experienceRef},
	})
	assertStatus(t, generatedSkill, http.StatusOK)
	if body := object(t, generatedSkill); body["status"] != "pending" || fieldObject(t, body, "candidate")["candidate_id"] != "candidate-4" {
		t.Fatalf("generated Skill = %#v", body)
	}

	scanned := postJSON(t, httpHandler, "/v1/external-skills/scan", map[string]any{"scope_id": "project:test"})
	assertStatus(t, scanned, http.StatusOK)
	registrations := fieldArray(t, object(t, scanned), "registrations")
	if len(registrations) != 1 {
		t.Fatalf("external registrations = %#v", registrations)
	}
	registration := registrations[0].(map[string]any)
	fingerprint := registration["fingerprint"].(string)
	externalSkillID := registration["external_skill_id"].(string)

	listedExternal := postJSON(t, httpHandler, "/v1/external-skills/list", map[string]any{"scope_id": "project:test"})
	assertStatus(t, listedExternal, http.StatusOK)
	if skills := fieldArray(t, object(t, listedExternal), "skills"); len(skills) != 1 || skills[0].(map[string]any)["status"] != "available" {
		t.Fatalf("external Skills = %#v", skills)
	}

	staleExternal := postJSON(t, httpHandler, "/v1/external-skills/resolve", map[string]any{
		"scope_id": "project:test", "external_skill_id": externalSkillID, "fingerprint": strings.Repeat("0", 64),
	})
	assertStatus(t, staleExternal, http.StatusOK)
	if body := object(t, staleExternal); body["status"] != "unavailable" || body["entrypoint"] != nil {
		t.Fatalf("stale external Skill = %#v", body)
	}

	imported := postJSON(t, httpHandler, "/v1/external-skills/import", map[string]any{
		"scope_id": "project:test", "external_skill_id": externalSkillID,
		"fingerprint": fingerprint, "mode": "import", "reason": "Govern selected package.",
	})
	assertStatus(t, imported, http.StatusOK)
	importedCandidate := fieldObject(t, object(t, imported), "candidate")
	if importedCandidate["candidate_id"] != "candidate-5" || importedCandidate["family"] != "skill" {
		t.Fatalf("imported external Skill = %#v", importedCandidate)
	}
	importSources := fieldArray(t, importedCandidate, "source_refs")
	if len(importSources) != 1 || importSources[0].(map[string]any)["name"] != skill.ExternalSnapshotSourceType {
		t.Fatalf("external import lineage = %#v", importSources)
	}

	if err := os.WriteFile(manifestPath, []byte("---\nname: friendly-go\ndescription: Use when writing Go.\n---\n\nChanged.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	drifted := postJSON(t, httpHandler, "/v1/external-skills/resolve", map[string]any{
		"scope_id": "project:test", "external_skill_id": externalSkillID, "fingerprint": fingerprint,
	})
	assertStatus(t, drifted, http.StatusOK)
	if body := object(t, drifted); body["status"] != "unavailable" || body["entrypoint"] != nil {
		t.Fatalf("drifted external Skill = %#v", body)
	}

	rejectionCandidate := postJSON(t, httpHandler, "/v1/experience/propose", map[string]any{
		"scope_id": "project:test",
		"proposal": map[string]any{
			"situation": "A result lacked evidence.", "action": "Inspect the source.",
			"outcome": "The lesson stayed unverified.", "lesson": "Reject unsupported lessons.",
		},
		"source_refs":   []any{map[string]any{"name": "content", "source_id": "capture-1"}},
		"artifact_refs": []any{},
	})
	assertStatus(t, rejectionCandidate, http.StatusCreated)
	rejectionCandidateID, ok := object(t, rejectionCandidate)["candidate_id"].(string)
	if !ok || rejectionCandidateID == "" {
		t.Fatalf("rejection Candidate = %s", rejectionCandidate.Body.String())
	}
	rejectedCandidate := postJSON(t, httpHandler, "/v1/artifact-candidates/reject", map[string]any{
		"scope_id": "project:test", "candidate_id": rejectionCandidateID, "expected_version": 1,
		"reason": "The outcome does not support the lesson.",
	})
	assertStatus(t, rejectedCandidate, http.StatusOK)
	if rejected := object(t, rejectedCandidate); rejected["status"] != "rejected" ||
		rejected["decision_reason"] != "The outcome does not support the lesson." {
		t.Fatalf("rejected Candidate = %#v", rejected)
	}

	statisticsResponse := getHTTP(t, httpHandler, "/v1/stats?scope_id=project%3Atest&period=today")
	assertStatus(t, statisticsResponse, http.StatusOK)
	if statisticsResponse.Header().Get("Cache-Control") != "no-store" ||
		statisticsResponse.Header().Get("X-PowerContext-Request-ID") == "" {
		t.Fatalf("statistics headers = %#v", statisticsResponse.Header())
	}
	statisticsBody := object(t, statisticsResponse)
	if statisticsBody["scope_id"] != "project:test" || statisticsBody["as_of"] != "2026-08-17T13:14:15.123456Z" {
		t.Fatalf("statistics identity = %#v", statisticsBody)
	}
	inventory := fieldObject(t, statisticsBody, "inventory")
	sourceInventory := fieldObject(t, inventory, "sources")
	if sourceInventory["total"] != float64(2) || sourceInventory["memory_processed"] != float64(1) ||
		sourceInventory["memory_pending"] != float64(1) {
		t.Fatalf("Source statistics = %#v", sourceInventory)
	}
	candidateInventory := fieldObject(t, inventory, "candidates")
	if candidateInventory["total"] != float64(6) || candidateInventory["pending"] != float64(3) ||
		candidateInventory["approved"] != float64(2) || candidateInventory["rejected"] != float64(1) {
		t.Fatalf("Candidate statistics = %#v", candidateInventory)
	}
	memoryInventory := fieldObject(t, fieldObject(t, inventory, "memory"), "entries")
	if memoryInventory["total"] != float64(2) || memoryInventory["active"] != float64(1) ||
		memoryInventory["inactive"] != float64(1) {
		t.Fatalf("Memory statistics = %#v", memoryInventory)
	}
	usageTotals := fieldObject(t, fieldObject(t, statisticsBody, "usage"), "totals")
	if generation := fieldObject(t, usageTotals, "generation"); generation["requests"] != float64(0) ||
		generation["input_tokens"] != float64(0) || generation["output_tokens"] != float64(0) {
		t.Fatalf("generation usage = %#v", generation)
	}
	recall := fieldObject(t, statisticsBody, "recall")
	if recall["estimator"] != nil || len(fieldArray(t, recall, "daily")) != 1 {
		t.Fatalf("recall statistics = %#v", recall)
	}

	var sourceRows, artifactRows int
	if err := database.SQLDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM pc_sources WHERE scope_id = ?", "project:test").Scan(&sourceRows); err != nil {
		t.Fatal(err)
	}
	if err := database.SQLDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM pc_artifacts WHERE scope_id = ? AND family = ?", "project:test", memory.Family).Scan(&artifactRows); err != nil {
		t.Fatal(err)
	}
	if sourceRows != 2 || artifactRows != 4 {
		t.Fatalf("stored rows = sources:%d artifacts:%d", sourceRows, artifactRows)
	}

	contractResponse := postJSON(t, httpHandler, "/v1/work/contracts/create", map[string]any{
		"scope_id": "project:test", "source_id": "contract-1",
		"contract": map[string]any{
			"schema": "powercontext.work-contract.v1", "trust": "untrusted_input",
			"objective": "Transfer the implementation safely.", "facts": []any{},
			"in_scope": []any{"Run the focused acceptance test."}, "exclusions": []any{},
			"completion_criteria": []any{"Record the receiver outcome."},
			"authorization_notes": []any{}, "open_questions": []any{},
		},
	})
	assertStatus(t, contractResponse, http.StatusAccepted)
	if body := object(t, contractResponse); body["kind"] != "work-contract" || body["position"] != float64(3) {
		t.Fatalf("Work Contract receipt = %#v", body)
	}

	currentResponse := postJSON(t, httpHandler, "/v1/work/handoffs/prepare-current", map[string]any{
		"scope_id": "project:test", "source_id": "work-boundary-1",
		"handoff": map[string]any{
			"schema": "powercontext.current-work-handoff.v1", "trust": "untrusted_input",
			"objective":   "Transfer the implementation safely.",
			"state":       []any{map[string]any{"text": "The implementation is ready.", "basis": "declared", "evidence": []any{}}},
			"disposition": "continuable", "next_action": nil, "omissions": []any{},
		},
	})
	assertStatus(t, currentResponse, http.StatusOK)
	preparedWork := object(t, currentResponse)
	if fieldObject(t, preparedWork, "boundary")["kind"] != "handoff-boundary" {
		t.Fatalf("Prepared Work Handoff = %#v", preparedWork)
	}
	workCommit := postJSON(t, httpHandler, "/v1/handoff/commit", map[string]any{
		"scope_id": "project:test", "handoff": preparedWork["handoff"],
	})
	assertStatus(t, workCommit, http.StatusOK)
	workRevision := fieldObject(t, object(t, workCommit), "reference")

	acknowledgement := postJSON(t, httpHandler, "/v1/work/handoffs/acknowledge", map[string]any{
		"scope_id": "project:test", "source_id": "receipt-1", "receiver": "receiver-agent",
		"status": "accepted", "selection": "exact", "revision": workRevision,
		"receiver_checks": map[string]any{
			"live_state": "confirmed", "capability": "confirmed", "authorization": "confirmed",
		},
		"prepared": nil, "message": nil,
	})
	assertStatus(t, acknowledgement, http.StatusOK)
	acknowledgementBody := object(t, acknowledgement)
	workReceipt := fieldObject(t, acknowledgementBody, "receipt")
	if workReceipt["kind"] != "handoff-receipt" {
		t.Fatalf("Handoff acknowledgement = %#v", acknowledgementBody)
	}

	outcomeResponse := postJSON(t, httpHandler, "/v1/work/outcomes/record", map[string]any{
		"scope_id": "project:test", "source_id": "outcome-1",
		"outcome": map[string]any{
			"schema": "powercontext.task-outcome.v1", "trust": "untrusted_observation",
			"objective": "Transfer the implementation safely.", "status": "succeeded", "summary": "The test passed.",
			"handoff_receipt_ref": workReceipt["source"],
			"observations":        []any{map[string]any{"text": "The receiver completed the test.", "basis": "declared", "evidence": []any{}}},
			"checks":              []any{}, "produced_artifacts": []any{}, "remaining_work": []any{},
		},
	})
	assertStatus(t, outcomeResponse, http.StatusAccepted)
	if body := object(t, outcomeResponse); body["kind"] != "task-outcome" || body["position"] != float64(6) {
		t.Fatalf("Task Outcome receipt = %#v", body)
	}

	knownScopes := postJSON(t, httpHandler, "/v1/handoff-reports/scopes/list-known", map[string]any{})
	assertStatus(t, knownScopes, http.StatusOK)
	items := fieldArray(t, object(t, knownScopes), "items")
	if len(items) != 1 || items[0].(map[string]any)["scope_id"] != "project:test" {
		t.Fatalf("known Handoff scopes = %#v", items)
	}
	continuityReport := postJSON(t, httpHandler, "/v1/handoff-reports/get", map[string]any{
		"scope_id": "project:test", "format": "json", "include_evidence_checks": false,
	})
	assertStatus(t, continuityReport, http.StatusOK)
	continuityEnvelope := object(t, continuityReport)
	continuityPayload := fieldObject(t, continuityEnvelope, "report")
	continuityWorkstreams := fieldArray(t, continuityPayload, "workstreams")
	continuityItem := continuityWorkstreams[0].(map[string]any)
	continuity := fieldObject(t, continuityItem, "continuity")
	coverage := fieldObject(t, continuity, "coverage")
	if coverage["transfer_state"] != "accepted" || coverage["outcome_state"] != "covered" ||
		coverage["handoff_result_covered"] != true {
		t.Fatalf("Work continuity coverage = %#v", coverage)
	}
	events := fieldArray(t, continuity, "events")
	kinds := make([]any, len(events))
	for index, event := range events {
		kinds[index] = event.(map[string]any)["kind"]
	}
	if !slices.Equal(kinds, []any{"work-contract", "handoff-boundary", "handoff-receipt", "task-outcome"}) {
		t.Fatalf("Work continuity events = %#v", events)
	}
	if continuityItem["handoff_revision_count"] != float64(3) || len(fieldArray(t, continuityItem, "handoff_history")) != 3 {
		t.Fatalf("Handoff Revision history = %#v", continuityItem)
	}
}

func deterministicMemoryIDs() memory.IDFactory {
	var mu sync.Mutex
	counters := map[string]int{}
	return func(kind string) (string, error) {
		if kind == "memory" {
			return runtime.DefaultMemoryArtifactID, nil
		}
		mu.Lock()
		defer mu.Unlock()
		prefixes := map[string]string{"entry": "mem_ent", "version": "mem_ver"}
		prefix, ok := prefixes[kind]
		if !ok {
			return "", fmt.Errorf("unexpected identity kind %q", kind)
		}
		counters[kind]++
		return fmt.Sprintf("%s_%d", prefix, counters[kind]), nil
	}
}

func deterministicReviewIDs() review.IDFactory {
	var mu sync.Mutex
	counters := map[string]int{}
	return func(kind string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if kind != "candidate" && kind != "experience" && kind != "skill" {
			return "", fmt.Errorf("unexpected Review identity kind %q", kind)
		}
		counters[kind]++
		return fmt.Sprintf("%s-%d", kind, counters[kind]), nil
	}
}

func deterministicHandoffReportIDs() runtime.HandoffReportIDFactory {
	var mu sync.Mutex
	counters := map[string]int{}
	return func(prefix string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if prefix != "prj_" && prefix != "evt_" {
			return "", fmt.Errorf("unexpected Handoff Report identity prefix %q", prefix)
		}
		counters[prefix]++
		return fmt.Sprintf("%sreport-%d", prefix, counters[prefix]), nil
	}
}

type fixedExperienceGenerator struct{}

type fixedMemoryCandidatePipeline struct{}

func (fixedMemoryCandidatePipeline) Extract(_ context.Context, request memory.CandidateRequest) ([]memory.EntryInput, error) {
	result := make([]memory.EntryInput, 0, len(request.Sources()))
	for _, value := range request.Sources() {
		content, ok := value.(source.ContentSource)
		if !ok {
			continue
		}
		result = append(result, memory.NewEntryInput(nil, "fact", content.Content(), []source.Value{value}, nil, nil))
	}
	return result, nil
}

func (fixedExperienceGenerator) Generate(context.Context, artifact.GenerationInput) (*experience.Content, error) {
	value, err := experience.NewContent(
		"A generated client became stale.", "Regenerate and inspect it.",
		"The contract and transport match.", "Regenerate before contract tests.",
	)
	return &value, err
}

type fixedSkillGenerator struct{}

func (fixedSkillGenerator) Generate(context.Context, artifact.GenerationInput) (*skill.Content, error) {
	value, err := skill.NewContent(
		"generated-contract-check", "Keep generated transport current.",
		"Regenerate the client and inspect the diff.", []string{"Run contract tests."},
	)
	return &value, err
}

type fixedHandoffPipeline struct{}

func (fixedHandoffPipeline) Generate(_ context.Context, request handoff.GenerationRequest) (handoff.Draft, error) {
	evidence := request.Evidence()
	if len(evidence) == 0 {
		return handoff.Draft{}, fmt.Errorf("missing Handoff evidence")
	}
	statement, err := handoff.NewStatement(
		"The Go Handoff lifecycle is connected.", []handoff.Citation{evidence[0].Citation()},
	)
	if err != nil {
		return handoff.Draft{}, err
	}
	return handoff.NewDraft(request.Objective(), []handoff.Statement{statement}, handoff.Continuable, nil, nil)
}

func postJSON(t *testing.T, handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func getHTTP(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertStatus(t *testing.T, response *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if response.Code != expected {
		t.Fatalf("status = %d, want %d: %s", response.Code, expected, response.Body.String())
	}
}

func assertError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	assertStatus(t, response, status)
	value := fieldObject(t, object(t, response), "error")
	if value["code"] != code {
		t.Fatalf("error = %#v, want code %q", value, code)
	}
}

func assertJSONPath(t *testing.T, response *httptest.ResponseRecorder, field string, expected any) {
	t.Helper()
	if got := object(t, response)[field]; got != expected {
		t.Fatalf("%s = %#v, want %#v", field, got, expected)
	}
}

func assertJSONNumber(t *testing.T, response *httptest.ResponseRecorder, field string, expected float64) {
	t.Helper()
	assertJSONPath(t, response, field, expected)
}

func object(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %q: %v", response.Body.String(), err)
	}
	return value
}

func fieldObject(t *testing.T, value map[string]any, field string) map[string]any {
	t.Helper()
	object, ok := value[field].(map[string]any)
	if !ok {
		t.Fatalf("field %q is %T in %#v", field, value[field], value)
	}
	return object
}

func fieldArray(t *testing.T, value map[string]any, field string) []any {
	t.Helper()
	array, ok := value[field].([]any)
	if !ok {
		t.Fatalf("field %q is %T in %#v", field, value[field], value)
	}
	return array
}
