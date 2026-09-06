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

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/handoffreport"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/work"
	"github.com/ob-labs/powercontext-go/source"
)

// ErrorMapping is the transport-neutral stable error contract emitted by an
// application operation. HTTP and MCP project this value independently.
type ErrorMapping struct {
	StatusCode int
	Code       string
	Message    string
	Details    map[string]any
}

// RuntimeNotReadyError reports an absent or not-yet-started application
// binding. It is distinct from runtime.StateError so configuration wiring can
// fail closed before a Runtime exists.
type RuntimeNotReadyError struct{}

func (*RuntimeNotReadyError) Error() string { return "PowerContext Runtime is not ready" }

type InvalidRequestError struct{ Field string }

func (*InvalidRequestError) Error() string { return "request is invalid" }

// GenerationUnavailableError reports a missing family-specific generator.
type GenerationUnavailableError struct{ Family string }

func (*GenerationUnavailableError) Error() string { return "Artifact generation is not configured" }

// MapError implements the frozen public error taxonomy. Unknown failures are
// intentionally collapsed to internal_error without exposing error strings.
func MapError(err error) ErrorMapping {
	if err == nil {
		return internalError()
	}
	var invalidWire *InvalidRequestError
	if errors.As(err, &invalidWire) {
		return invalidRequest()
	}

	var notReady *RuntimeNotReadyError
	var state *runtime.StateError
	if errors.As(err, &notReady) || (errors.As(err, &state) && state.Code == "closed") {
		return mapping(http.StatusServiceUnavailable, "runtime_not_ready", "The Runtime is not ready.", nil)
	}

	var registryUnavailable *skill.ExternalRegistryUnavailableError
	if errors.As(err, &registryUnavailable) {
		return mapping(http.StatusServiceUnavailable, "external_skill_registry_unavailable", "The external Skill Registry is unavailable.", nil)
	}
	var externalMissing *skill.ExternalNotFoundError
	if errors.As(err, &externalMissing) {
		return mapping(http.StatusNotFound, "external_skill_not_found", "The external Skill was not found.", nil)
	}
	var snapshotUnavailable *skill.ExternalSnapshotUnavailableError
	if errors.As(err, &snapshotUnavailable) {
		return mapping(http.StatusConflict, "external_skill_snapshot_unavailable", "The exact external Skill snapshot is unavailable.", nil)
	}
	var generationUnavailable *GenerationUnavailableError
	if errors.As(err, &generationUnavailable) {
		return mapping(http.StatusServiceUnavailable, "generation_unavailable", "Artifact generation is not configured.", map[string]any{
			"family": generationUnavailable.Family,
		})
	}
	var reviewGenerationUnavailable *review.GenerationCapabilityUnavailableError
	if errors.As(err, &reviewGenerationUnavailable) {
		return mapping(http.StatusServiceUnavailable, "generation_unavailable", "Artifact generation is not configured.", map[string]any{
			"family": reviewGenerationUnavailable.Family,
		})
	}

	if mapped, ok := mapCandidateError(err); ok {
		return mapped
	}
	if mapped, ok := mapHandoffReportError(err); ok {
		return mapped
	}
	if mapped, ok := mapHandoffError(err); ok {
		return mapped
	}
	return mapDomainError(err)
}

func mapHandoffReportError(err error) (ErrorMapping, bool) {
	var projectMissing *handoffreport.ProjectNotFoundError
	if errors.As(err, &projectMissing) {
		return mapping(http.StatusNotFound, "project_not_found", "The requested Handoff Report catalog value was not found.", nil), true
	}
	var workstreamMissing *handoffreport.WorkstreamNotFoundError
	if errors.As(err, &workstreamMissing) {
		return mapping(http.StatusNotFound, "scope_not_grouped", "The requested Handoff Report catalog value was not found.", nil), true
	}
	var workspaceMissing *handoffreport.WorkspaceBindingNotFoundError
	if errors.As(err, &workspaceMissing) {
		return mapping(http.StatusNotFound, "workspace_not_bound", "The requested Handoff Report catalog value was not found.", nil), true
	}

	var projectConflict *handoffreport.ProjectConflictError
	if errors.As(err, &projectConflict) {
		return reportConflict("project_conflict", map[string]any{
			"expected_version": nullableDetail(projectConflict.ExpectedVersion),
			"current_version":  nullableDetail(projectConflict.CurrentVersion),
			"project_id":       projectConflict.ProjectID,
		}), true
	}
	var workstreamConflict *handoffreport.WorkstreamConflictError
	if errors.As(err, &workstreamConflict) {
		return reportConflict("workstream_conflict", map[string]any{
			"expected_version": nullableDetail(workstreamConflict.ExpectedVersion),
			"current_version":  nullableDetail(workstreamConflict.CurrentVersion),
			"scope_id":         workstreamConflict.ScopeID,
		}), true
	}
	var grouped *handoffreport.ScopeAlreadyGroupedError
	if errors.As(err, &grouped) {
		return reportConflict("scope_already_grouped", map[string]any{
			"scope_id": grouped.ScopeID, "project_id": grouped.ProjectID,
		}), true
	}
	var workspaceConflict *handoffreport.WorkspaceBindingConflictError
	if errors.As(err, &workspaceConflict) {
		return reportConflict("workspace_binding_conflict", map[string]any{
			"expected_version":      nullableDetail(workspaceConflict.ExpectedVersion),
			"current_version":       nullableDetail(workspaceConflict.CurrentVersion),
			"workspace_instance_id": workspaceConflict.WorkspaceInstanceID,
		}), true
	}
	var activityConflict *handoffreport.ActivityEventConflictError
	if errors.As(err, &activityConflict) {
		return mapping(http.StatusConflict, "activity_event_conflict", "The Activity idempotency key already identifies different content.", map[string]any{
			"source": string(activityConflict.Source), "source_event_id": activityConflict.SourceEventID,
		}), true
	}
	var busy *handoffreport.BusyError
	if errors.As(err, &busy) {
		return mapping(http.StatusConflict, "handoff_report_busy", "Handoff heads changed while the report was being assembled.", map[string]any{"attempts": busy.Attempts}), true
	}
	var tooLarge *handoffreport.TooLargeError
	if errors.As(err, &tooLarge) {
		return mapping(http.StatusRequestEntityTooLarge, "handoff_report_too_large", "The Handoff Report is too large; narrow the Workstream or Activity selection.", map[string]any{
			"estimated_bytes":      nullableDetail(tooLarge.EstimatedBytes),
			"selected_workstreams": tooLarge.SelectedWorkstreams,
			"selected_activities":  tooLarge.SelectedActivities,
		}), true
	}
	var inconsistent *handoffreport.InconsistentError
	if errors.As(err, &inconsistent) {
		return mapping(http.StatusConflict, "handoff_report_inconsistent", "The frozen Handoff selection could not be read consistently.", map[string]any{"scope_id": inconsistent.ScopeID}), true
	}

	var catalogArgument *handoffreport.CatalogArgumentError
	var invalidActivity *handoffreport.InvalidActivityEventError
	var invalidRepository *handoffreport.InvalidActivityRepositoryArgumentError
	if errors.As(err, &catalogArgument) || errors.As(err, &invalidActivity) || errors.As(err, &invalidRepository) {
		return invalidRequest(), true
	}

	var unavailable *handoffreport.Error
	var stored *handoffreport.InvalidStoredCatalogError
	var evidence *handoffreport.EvidenceCheckUnavailableError
	var canonical *handoffreport.CanonicalizationError
	if errors.As(err, &unavailable) || errors.As(err, &stored) || errors.As(err, &evidence) || errors.As(err, &canonical) {
		return mapping(http.StatusServiceUnavailable, "handoff_report_unavailable", "Handoff Report is unavailable.", nil), true
	}
	return ErrorMapping{}, false
}

func reportConflict(code string, details map[string]any) ErrorMapping {
	return mapping(http.StatusConflict, code, "The Handoff Report catalog value is stale or conflicting.", details)
}

func nullableDetail[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}

func mapCandidateError(err error) (ErrorMapping, bool) {
	var missing *review.CandidateNotFoundError
	if errors.As(err, &missing) {
		return mapping(http.StatusNotFound, "candidate_not_found", "The requested Candidate was not found.", nil), true
	}
	var conflict *review.CandidateConflictError
	if errors.As(err, &conflict) {
		return mapping(http.StatusConflict, "candidate_conflict", "The Candidate version is stale.", map[string]any{
			"expected_version": conflict.ExpectedVersion,
			"current_version":  conflict.CurrentVersion,
		}), true
	}
	var target *review.ArtifactTargetConflictError
	if errors.As(err, &target) {
		return mapping(http.StatusConflict, "artifact_conflict", "The Candidate target Artifact is stale.", map[string]any{
			"current": artifactReferenceDetails(target.Current),
		}), true
	}
	var terminal *review.CandidateTerminalError
	if errors.As(err, &terminal) {
		return mapping(http.StatusConflict, "candidate_terminal", "The Candidate is already terminal.", map[string]any{
			"status": string(terminal.Status),
		}), true
	}
	var invalid *review.InvalidCandidateError
	if errors.As(err, &invalid) {
		return invalidRequest(), true
	}
	return ErrorMapping{}, false
}

func mapHandoffError(err error) (ErrorMapping, bool) {
	var evidence *handoff.EvidenceUnavailableError
	if errors.As(err, &evidence) {
		return mapping(http.StatusNotFound, "handoff_evidence_not_found", "Cited Handoff evidence was not found.", nil), true
	}
	var unavailable *handoff.GenerationUnavailableError
	if errors.As(err, &unavailable) {
		return mapping(http.StatusServiceUnavailable, "handoff_generation_unavailable", "Handoff generation is unavailable.", nil), true
	}
	var invalidGeneration *handoff.InvalidGenerationError
	if errors.As(err, &invalidGeneration) {
		return mapping(http.StatusInternalServerError, "invalid_handoff_generation", "Handoff generation violated its contract.", map[string]any{
			"reason": invalidGeneration.Code,
		}), true
	}
	return ErrorMapping{}, false
}

func mapDomainError(err error) ErrorMapping {
	var scopeMissing *scope.NotFoundError
	var bindingMissing *scope.BindingNotFoundError
	if errors.As(err, &scopeMissing) || errors.As(err, &bindingMissing) {
		return mapping(http.StatusNotFound, "scope_not_found", "The requested Scope was not found.", nil)
	}
	var scopeVersion *scope.VersionConflictError
	if errors.As(err, &scopeVersion) {
		return mapping(http.StatusConflict, "scope_conflict", "The Scope metadata is stale.", nil)
	}
	var artifactMissing *artifact.NotFoundError
	if errors.As(err, &artifactMissing) {
		return mapping(http.StatusNotFound, "artifact_not_found", "The requested Artifact was not found.", nil)
	}
	var memoryMissing *memory.EntryNotFoundError
	if errors.As(err, &memoryMissing) {
		return mapping(http.StatusNotFound, "memory_not_found", "The requested Memory value was not found.", nil)
	}
	var sourceConflict *source.ConflictError
	if errors.As(err, &sourceConflict) {
		return mapping(http.StatusConflict, "source_conflict", "The Source identity has different content.", nil)
	}
	var revision *artifact.RevisionConflictError
	if errors.As(err, &revision) {
		return mapping(http.StatusConflict, "revision_conflict", "The Memory Revision is stale.", nil)
	}
	var inactive *memory.EntryInactiveError
	if errors.As(err, &inactive) {
		return mapping(http.StatusConflict, "memory_entry_inactive", "The Memory entry is inactive.", nil)
	}
	var capability *memory.CapabilityNotSupportedError
	if errors.As(err, &capability) {
		return mapping(http.StatusUnprocessableEntity, "capability_not_supported", "The requested capability is unavailable.", map[string]any{
			"capability": capability.Capability,
		})
	}
	if invalidDomainRequest(err) {
		return invalidRequest()
	}
	var timeout *inference.TimeoutError
	if errors.As(err, &timeout) {
		return mapping(http.StatusServiceUnavailable, "inference_timeout", "Model inference timed out.", nil)
	}
	var unavailable *inference.UnavailableError
	if errors.As(err, &unavailable) {
		return mapping(http.StatusServiceUnavailable, "inference_unavailable", "Model inference is unavailable.", nil)
	}
	return internalError()
}

func invalidDomainRequest(err error) bool {
	var candidate *memory.InvalidCandidateError
	var evidence *memory.InvalidEvidenceError
	var citation *memory.InvalidCitationError
	var operation *memory.InvalidOperationError
	var canonical *memory.CanonicalError
	var invalidRuntimeScope *runtime.InvalidScopeError
	var handoffScope *handoff.ScopeMismatchError
	var handoffRef *handoff.InvalidReferenceError
	var sourceRef *source.InvalidReferenceError
	var artifactRef *artifact.InvalidReferenceError
	var invalidWork *work.InvalidError
	var invalidWorkRequest *work.InvalidRequestError
	var invalidScope *scope.ValidationError
	var invalidRelationship *scope.RelationshipError
	return errors.As(err, &candidate) || errors.As(err, &evidence) || errors.As(err, &citation) ||
		errors.As(err, &operation) || errors.As(err, &canonical) || errors.As(err, &invalidRuntimeScope) ||
		errors.As(err, &handoffScope) || errors.As(err, &handoffRef) || errors.As(err, &sourceRef) ||
		errors.As(err, &artifactRef) || errors.As(err, &invalidWork) || errors.As(err, &invalidWorkRequest) ||
		errors.As(err, &invalidScope) || errors.As(err, &invalidRelationship)
}

func invalidRequest() ErrorMapping {
	return mapping(http.StatusUnprocessableEntity, "invalid_request", "The request is invalid.", nil)
}

func internalError() ErrorMapping {
	return mapping(http.StatusInternalServerError, "internal_error", "The Server failed.", nil)
}

func mapping(status int, code, message string, details map[string]any) ErrorMapping {
	return ErrorMapping{StatusCode: status, Code: code, Message: message, Details: details}
}

func artifactReferenceDetails(ref artifact.Ref) map[string]any {
	return map[string]any{
		"family":      ref.Family(),
		"artifact_id": ref.ID(),
		"revision":    ref.Revision(),
	}
}
