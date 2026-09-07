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

package endpoint

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/handoffreport"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

func TestMapErrorFrozenTaxonomy(t *testing.T) {
	t.Parallel()

	current, err := artifact.NewRef("experience", "exp_1", 3)
	if err != nil {
		t.Fatal(err)
	}
	expectedVersion, currentVersion, estimatedBytes := 2, 4, 10_485_761
	for _, test := range []struct {
		name    string
		err     error
		status  int
		code    string
		details map[string]any
	}{
		{name: "runtime", err: &runtime.StateError{Code: "closed"}, status: 503, code: "runtime_not_ready"},
		{name: "invalid artifact cursor", err: errors.Join(errors.New("private-token"), &runtime.InvalidArtifactCursorError{}), status: 400, code: "invalid_cursor"},
		{name: "expired artifact cursor", err: errors.Join(errors.New("private-token"), &runtime.ExpiredArtifactCursorError{}), status: 410, code: "cursor_expired"},
		{name: "scope missing", err: &scope.NotFoundError{}, status: 404, code: "scope_not_found"},
		{name: "binding missing", err: &scope.BindingNotFoundError{}, status: 404, code: "scope_binding_not_found"},
		{name: "scope validation", err: &scope.ValidationError{}, status: 422, code: "invalid_request"},
		{name: "scope version conflict", err: &scope.VersionConflictError{Expected: 2, Actual: 4}, status: 409, code: "scope_version_conflict", details: map[string]any{"expected_version": int64(2), "current_version": int64(4)}},
		{name: "scope idempotency conflict", err: &scope.IdempotencyConflictError{}, status: 409, code: "scope_idempotency_conflict"},
		{name: "candidate missing", err: &review.CandidateNotFoundError{}, status: 404, code: "candidate_not_found"},
		{name: "candidate CAS", err: &review.CandidateConflictError{ExpectedVersion: 2, CurrentVersion: 4}, status: 409, code: "candidate_conflict", details: map[string]any{"expected_version": int64(2), "current_version": int64(4)}},
		{name: "artifact CAS", err: &review.ArtifactTargetConflictError{Current: current}, status: 409, code: "artifact_conflict", details: map[string]any{"current": map[string]any{"family": "experience", "artifact_id": "exp_1", "revision": int64(3)}}},
		{name: "memory missing", err: &memory.EntryNotFoundError{}, status: 404, code: "memory_not_found"},
		{name: "capability", err: &memory.CapabilityNotSupportedError{Capability: "vector"}, status: 422, code: "capability_not_supported", details: map[string]any{"capability": "vector"}},
		{name: "bad scope", err: &runtime.InvalidScopeError{}, status: 422, code: "invalid_request"},
		{name: "inference timeout", err: inference.NewTimeoutError("generation", 1), status: 503, code: "inference_timeout"},
		{name: "inference unavailable", err: inference.NewUnavailableError("generation"), status: 503, code: "inference_unavailable"},
		{name: "bad generated handoff", err: &handoff.InvalidGenerationError{Code: "budget"}, status: 500, code: "invalid_handoff_generation", details: map[string]any{"reason": "budget"}},
		{name: "Report Project missing", err: &handoffreport.ProjectNotFoundError{ProjectID: "prj_1"}, status: 404, code: "project_not_found"},
		{name: "Report Workstream missing", err: &handoffreport.WorkstreamNotFoundError{ScopeID: "scope-1"}, status: 404, code: "scope_not_grouped"},
		{name: "Report Project CAS", err: &handoffreport.ProjectConflictError{ProjectID: "prj_1", ExpectedVersion: &expectedVersion, CurrentVersion: &currentVersion}, status: 409, code: "project_conflict", details: map[string]any{"expected_version": 2, "current_version": 4, "project_id": "prj_1"}},
		{name: "Report Activity identity", err: &handoffreport.ActivityEventConflictError{Source: handoffreport.ActivityCodingSession, SourceEventID: "session-1"}, status: 409, code: "activity_event_conflict", details: map[string]any{"source": "coding_session", "source_event_id": "session-1"}},
		{name: "Report busy", err: &handoffreport.BusyError{Attempts: 3}, status: 409, code: "handoff_report_busy", details: map[string]any{"attempts": 3}},
		{name: "Report too large", err: &handoffreport.TooLargeError{EstimatedBytes: &estimatedBytes, SelectedWorkstreams: 2, SelectedActivities: 3}, status: 413, code: "handoff_report_too_large", details: map[string]any{"estimated_bytes": 10_485_761, "selected_workstreams": 2, "selected_activities": 3}},
		{name: "Report invalid", err: &handoffreport.CatalogArgumentError{Field: "period", Detail: "invalid"}, status: 422, code: "invalid_request"},
		{name: "Report unavailable", err: &handoffreport.InvalidStoredCatalogError{Kind: "Project", Detail: "invalid"}, status: 503, code: "handoff_report_unavailable"},
		{name: "wrapped", err: errors.Join(errors.New("context"), &artifact.NotFoundError{}), status: 404, code: "artifact_not_found"},
		{name: "unknown", err: errors.New("secret backend detail"), status: 500, code: "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := MapError(test.err)
			if got.StatusCode != test.status || got.Code != test.code || !reflect.DeepEqual(got.Details, test.details) {
				t.Fatalf("MapError() = %#v, want status=%d code=%q details=%#v", got, test.status, test.code, test.details)
			}
			if got.Message == "" {
				t.Fatal("empty public message")
			}
			if strings.Contains(got.Message, "private-token") {
				t.Fatal("error mapping exposed cursor material")
			}
		})
	}
}

func TestMapErrorNilFailsClosed(t *testing.T) {
	t.Parallel()
	got := MapError(nil)
	if got.StatusCode != http.StatusInternalServerError || got.Code != "internal_error" {
		t.Fatalf("MapError(nil) = %#v", got)
	}
}
