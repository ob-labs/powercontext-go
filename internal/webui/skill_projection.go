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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	"github.com/ob-labs/powercontext-go/internal/review"
)

const maximumProjectionRequestBytes = 64 * 1024

type projectionRequest struct {
	ScopeID     string             `json:"scope_id"`
	CandidateID string             `json:"candidate_id"`
	Artifact    projectionArtifact `json:"artifact"`
}

type publishRequest struct {
	ScopeID     string             `json:"scope_id"`
	CandidateID string             `json:"candidate_id"`
	Artifact    projectionArtifact `json:"artifact"`
	TargetID    string             `json:"target_id"`
}

type projectionArtifact struct {
	Family     string `json:"family"`
	ArtifactID string `json:"artifact_id"`
	Revision   int64  `json:"revision"`
}

type projectionResponse struct {
	Artifact projectionArtifact         `json:"artifact"`
	Name     string                     `json:"name"`
	Targets  []projectionTargetResponse `json:"targets"`
}

type projectionTargetResponse struct {
	TargetID          string  `json:"target_id"`
	AgentKind         string  `json:"agent_kind"`
	InstallationScope string  `json:"installation_scope"`
	Destination       string  `json:"destination"`
	State             string  `json:"state"`
	PublishedRevision *int64  `json:"published_revision"`
	Reason            *string `json:"reason"`
	Discovery         string  `json:"discovery"`
	ExternalSkillID   *string `json:"external_skill_id"`
}

type webErrorDetail struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type webErrorEnvelope struct {
	Error webErrorDetail `json:"error"`
}

func (p *pages) skillProjectionStatus(writer http.ResponseWriter, request *http.Request) {
	var selection projectionRequest
	if err := decodeProjectionRequest(writer, request, &selection); err != nil {
		writeWebError(writer, http.StatusUnprocessableEntity, "invalid_request", "The request is invalid.", nil)
		return
	}
	ref, ok := validateProjectionSelection(selection.ScopeID, selection.CandidateID, selection.Artifact)
	if !ok {
		writeWebError(writer, http.StatusUnprocessableEntity, "invalid_request", "The request is invalid.", nil)
		return
	}
	value, status, detail := p.managedSkill(request, selection.ScopeID, selection.CandidateID, ref)
	if detail != nil {
		writeWebError(writer, status, detail.Code, detail.Message, detail.Details)
		return
	}
	response, err := p.projectionResponse(request, selection.ScopeID, value)
	if err != nil {
		p.writeOperationError(writer, err)
		return
	}
	writeWebJSON(writer, http.StatusOK, response)
}

func (p *pages) skillProjectionPublish(writer http.ResponseWriter, request *http.Request) {
	var selection publishRequest
	if err := decodeProjectionRequest(writer, request, &selection); err != nil ||
		!validBoundedText(selection.TargetID, 64) {
		writeWebError(writer, http.StatusUnprocessableEntity, "invalid_request", "The request is invalid.", nil)
		return
	}
	ref, ok := validateProjectionSelection(selection.ScopeID, selection.CandidateID, selection.Artifact)
	if !ok {
		writeWebError(writer, http.StatusUnprocessableEntity, "invalid_request", "The request is invalid.", nil)
		return
	}
	value, status, detail := p.managedSkill(request, selection.ScopeID, selection.CandidateID, ref)
	if detail != nil {
		writeWebError(writer, status, detail.Code, detail.Message, detail.Details)
		return
	}
	targetIndex := slices.IndexFunc(p.projectionTargets, func(target skill.AgentSkillTarget) bool {
		return target.ID() == selection.TargetID
	})
	if targetIndex < 0 {
		writeWebError(
			writer, http.StatusNotFound, "skill_publish_target_not_found",
			"The Agent Skill publication target was not found.", nil,
		)
		return
	}
	target := p.projectionTargets[targetIndex]
	expected := skill.InspectSkillProjection(value.Ref(), value.Content(), target)
	if _, err := skill.PublishSkillProjection(value.Ref(), value.Content(), target, &expected); err != nil {
		var conflict *skill.ProjectionConflictError
		if errors.As(err, &conflict) {
			reason := any(nil)
			if conflict.Status.Reason() != "" {
				reason = conflict.Status.Reason()
			}
			writeWebError(
				writer, http.StatusConflict, "skill_projection_conflict",
				"The Agent Skill publication target changed or cannot be updated safely.",
				map[string]any{"state": conflict.Status.State(), "reason": reason},
			)
			return
		}
		writeWebError(
			writer, http.StatusUnprocessableEntity, "skill_projection_failed",
			"The approved managed Skill could not be published to the configured Agent target.",
			map[string]any{"reason": err.Error()},
		)
		return
	}
	if p.projections == nil {
		writeWebError(writer, http.StatusServiceUnavailable, "runtime_not_ready", "The Runtime is not ready.", nil)
		return
	}
	if _, err := p.projections.ScanExternalSkills(request.Context(), selection.ScopeID); err != nil {
		serverlogging.LogSafely(
			request.Context(), p.logger, slog.LevelWarn,
			"PowerContext external Skill scan failed after publication",
			slog.String("event", "skill_projection.registry_scan.degraded"),
			slog.String("operation", "scan_external_skills"),
			slog.String("outcome", "degraded"),
			slog.String("unit", "application"),
			slog.String("error_code", "external_skill_scan_failed"),
		)
	}
	response, err := p.projectionResponse(request, selection.ScopeID, value)
	if err != nil {
		p.writeOperationError(writer, err)
		return
	}
	writeWebJSON(writer, http.StatusOK, response)
}

func (p *pages) managedSkill(
	request *http.Request,
	scopeID, candidateID string,
	ref artifact.Ref,
) (skill.Skill, int, *webErrorDetail) {
	if !p.hasScope(scopeID) {
		return skill.Skill{}, http.StatusNotFound, &webErrorDetail{
			Code: "dashboard_scope_not_found", Message: "The Dashboard scope was not found.",
		}
	}
	if p.projections == nil {
		return skill.Skill{}, http.StatusServiceUnavailable, &webErrorDetail{
			Code: "runtime_not_ready", Message: "The Runtime is not ready.",
		}
	}
	candidate, err := p.projections.GetCandidate(request.Context(), scopeID, candidateID)
	if err != nil {
		mapped := endpoint.MapError(err)
		return skill.Skill{}, mapped.StatusCode, &webErrorDetail{
			Code: mapped.Code, Message: mapped.Message, Details: mapped.Details,
		}
	}
	result := candidate.ResultArtifact()
	if candidate.Family() != skill.Family || candidate.Status() != review.Approved || result == nil || *result != ref {
		return skill.Skill{}, http.StatusConflict, &webErrorDetail{
			Code:    "skill_projection_not_approved",
			Message: "The selected Artifact is not the exact approved result of this Skill Candidate.",
		}
	}
	value, err := p.projections.GetSkill(request.Context(), scopeID, ref)
	if err != nil {
		mapped := endpoint.MapError(err)
		return skill.Skill{}, mapped.StatusCode, &webErrorDetail{
			Code: mapped.Code, Message: mapped.Message, Details: mapped.Details,
		}
	}
	return value, 0, nil
}

func (p *pages) projectionResponse(
	request *http.Request,
	scopeID string,
	value skill.Skill,
) (projectionResponse, error) {
	if len(p.projectionTargets) > 0 {
		if p.projections == nil {
			return projectionResponse{}, &endpoint.RuntimeNotReadyError{}
		}
		registrations, err := p.projections.ListExternalSkills(request.Context(), scopeID, true)
		if err != nil {
			serverlogging.LogSafely(
				request.Context(), p.logger, slog.LevelWarn,
				"PowerContext external Skill registry discovery failed",
				slog.String("event", "skill_projection.registry_discovery.degraded"),
				slog.String("operation", "list_external_skills"),
				slog.String("outcome", "degraded"),
				slog.String("unit", "application"),
				slog.String("error_code", "external_skill_discovery_failed"),
			)
			registrations = nil
		}
		return p.projectionResponseWithRegistrations(value, registrations), nil
	}
	return p.projectionResponseWithRegistrations(value, nil), nil
}

func (p *pages) publishedProjectionResponse(
	request *http.Request,
	scopeID string,
	value skill.Skill,
) (projectionResponse, error) {
	if p.projections == nil {
		return projectionResponse{}, &endpoint.RuntimeNotReadyError{}
	}
	if _, err := p.projections.ScanExternalSkills(request.Context(), scopeID); err != nil {
		return p.projectionResponseWithRegistrations(value, nil), nil
	}
	registrations, err := p.projections.ListExternalSkills(request.Context(), scopeID, true)
	if err != nil {
		return p.projectionResponseWithRegistrations(value, nil), nil
	}
	return p.projectionResponseWithRegistrations(value, registrations), nil
}

func (p *pages) projectionResponseWithRegistrations(
	value skill.Skill,
	registrations []skill.Resolution,
) projectionResponse {
	result := projectionResponse{
		Artifact: wireProjectionArtifact(value.Ref()), Name: value.Content().Name(),
		Targets: make([]projectionTargetResponse, 0, len(p.projectionTargets)),
	}
	for _, target := range p.projectionTargets {
		status := skill.InspectSkillProjection(value.Ref(), value.Content(), target)
		var registration *skill.Registration
		for _, candidate := range registrations {
			if candidate.Status == skill.Available &&
				candidate.Registration.AgentKind() == string(target.AgentKind()) &&
				candidate.Registration.Locator() == status.Destination() {
				value := candidate.Registration
				registration = &value
				break
			}
		}
		discovery := "not_published"
		if status.State() == skill.ProjectionCurrent {
			discovery = "unavailable"
			if registration != nil {
				discovery = "available"
			}
		}
		var publishedRevision *int64
		if published := status.PublishedArtifact(); published != nil {
			value := published.Revision()
			publishedRevision = &value
		}
		var reason *string
		if status.Reason() != "" {
			value := status.Reason()
			reason = &value
		}
		var externalSkillID *string
		if registration != nil {
			value := registration.ExternalSkillID()
			externalSkillID = &value
		}
		result.Targets = append(result.Targets, projectionTargetResponse{
			TargetID: target.ID(), AgentKind: string(target.AgentKind()),
			InstallationScope: string(target.InstallationScope()), Destination: status.Destination(),
			State: string(status.State()), PublishedRevision: publishedRevision, Reason: reason,
			Discovery: discovery, ExternalSkillID: externalSkillID,
		})
	}
	return result
}

func (p *pages) hasScope(scopeID string) bool {
	return slices.ContainsFunc(p.scopes, func(scope Scope) bool { return scope.ScopeID == scopeID })
}

func validateProjectionSelection(
	scopeID, candidateID string,
	wire projectionArtifact,
) (artifact.Ref, bool) {
	if !validBoundedText(scopeID, 256) || !validBoundedText(candidateID, artifact.MaxIDLength) ||
		wire.Family != skill.Family {
		return artifact.Ref{}, false
	}
	ref, err := artifact.NewRef(wire.Family, wire.ArtifactID, wire.Revision)
	return ref, err == nil
}

func validBoundedText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func decodeProjectionRequest(writer http.ResponseWriter, request *http.Request, value any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maximumProjectionRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request contains trailing JSON")
		}
		return err
	}
	return nil
}

func wireProjectionArtifact(ref artifact.Ref) projectionArtifact {
	return projectionArtifact{Family: ref.Family(), ArtifactID: ref.ID(), Revision: ref.Revision()}
}

func (p *pages) writeOperationError(writer http.ResponseWriter, err error) {
	mapped := endpoint.MapError(err)
	writeWebError(writer, mapped.StatusCode, mapped.Code, mapped.Message, mapped.Details)
}

func writeWebError(
	writer http.ResponseWriter,
	status int,
	code, message string,
	details map[string]any,
) {
	writeWebJSON(writer, status, webErrorEnvelope{Error: webErrorDetail{
		Code: code, Message: message, Details: details,
	}})
}

func writeWebJSON(writer http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		status = http.StatusInternalServerError
		payload = []byte(`{"error":{"code":"internal_error","message":"The Server failed.","details":null}}`)
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(payload)))
	writer.WriteHeader(status)
	_, _ = writer.Write(payload)
}
