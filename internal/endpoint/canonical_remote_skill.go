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
	"context"
	"errors"
	"time"

	remoteskills "github.com/ob-labs/powercontext-go/api/canonical/remoteskills"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

// RemoteSkillTargetOperations is the Runtime-owned lifecycle surface exposed
// by the canonical remote Skill sidecar.
type RemoteSkillTargetOperations interface {
	Create(context.Context, runtime.RemoteSkillTargetCreateInput) (runtime.RemoteSkillTargetCreateResult, error)
	List(context.Context, string) ([]skill.RemoteTargetView, error)
	Enroll(context.Context, runtime.RemoteSkillTargetEnrollInput) (runtime.RemoteSkillTargetEnrollResult, error)
	Rename(context.Context, runtime.RemoteSkillTargetRenameInput) (skill.RemoteTargetView, error)
	Revoke(context.Context, runtime.RemoteSkillTargetRevokeInput) (skill.RemoteTargetView, error)
}

// CanonicalRemoteSkillHandler maps the generated lifecycle contract to the
// Runtime without rendering enrollment codes, credentials, or durable digests
// in error values.
type CanonicalRemoteSkillHandler struct{ operations RemoteSkillTargetOperations }

var _ remoteskills.Handler = (*CanonicalRemoteSkillHandler)(nil)

func NewCanonicalRemoteSkillHandler(operations RemoteSkillTargetOperations) *CanonicalRemoteSkillHandler {
	return &CanonicalRemoteSkillHandler{operations: operations}
}

func (h *CanonicalRemoteSkillHandler) CreateRemoteSkillTarget(
	ctx context.Context,
	request *remoteskills.CreateRemoteSkillTargetRequest,
) (remoteskills.CreateRemoteSkillTargetRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return remoteSkillInvalidRequest(), nil
	}
	result, err := h.operations.Create(ctx, runtime.RemoteSkillTargetCreateInput{
		ScopeID: request.ScopeID, DisplayName: request.DisplayName, AgentKind: skill.AgentKind(request.AgentKind),
	})
	if err != nil {
		if remoteSkillScopeNotFound(err) {
			return nil, err
		}
		return remoteSkillCreateError(err), nil
	}
	target, err := canonicalRemoteSkillTarget(result.Target)
	if err != nil || result.EnrollmentCode == "" || result.EnrollmentExpiresAt.IsZero() {
		return remoteSkillInternalError(), nil
	}
	return &remoteskills.RemoteSkillTargetEnrollment{
		EnrollmentCode: result.EnrollmentCode, EnrollmentExpiresAt: result.EnrollmentExpiresAt, Target: target,
	}, nil
}

func (h *CanonicalRemoteSkillHandler) EnrollRemoteSkillTarget(
	ctx context.Context,
	request *remoteskills.EnrollRemoteSkillTargetRequest,
) (remoteskills.EnrollRemoteSkillTargetRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return remoteSkillInvalidRequest(), nil
	}
	result, err := h.operations.Enroll(ctx, runtime.RemoteSkillTargetEnrollInput{
		EnrollmentCode: request.EnrollmentCode, InstallationID: request.InstallationID, ReceiverVersion: request.ReceiverVersion,
		CredentialSubject:      request.InstallationID,
		EnvironmentFingerprint: remoteSkillOptionalString(request.EnvironmentFingerprint),
		MachineHostname:        remoteSkillOptionalString(request.MachineHostname), WorkspaceName: remoteSkillOptionalString(request.WorkspaceName),
	})
	if err != nil {
		var refused *runtime.RemoteSkillTargetEnrollmentRefusedError
		if errors.As(err, &refused) {
			return remoteSkillConflict("enrollment_refused", "The enrollment code is invalid or unavailable."), nil
		}
		return remoteSkillInternalError(), nil
	}
	if result.Credential == "" {
		return remoteSkillInternalError(), nil
	}
	return &remoteskills.RemoteSkillTargetCredential{
		AgentKind: remoteskills.RemoteAgentKind(result.Target.AgentKind()), Credential: result.Credential,
		ScopeID: result.Target.ScopeID(), TargetID: result.Target.TargetID(),
	}, nil
}

func (h *CanonicalRemoteSkillHandler) ListRemoteSkillTargets(
	ctx context.Context,
	request *remoteskills.ListRemoteSkillTargetsRequest,
) (remoteskills.ListRemoteSkillTargetsRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return remoteSkillInvalidRequest(), nil
	}
	values, err := h.operations.List(ctx, request.ScopeID)
	if err != nil {
		if remoteSkillScopeNotFound(err) {
			return nil, err
		}
		return remoteSkillInternalError(), nil
	}
	targetID := remoteSkillOptionalString(request.TargetID)
	limit := len(values)
	if value, found := request.Limit.Get(); found {
		limit = min(limit, value)
	}
	result := remoteskills.ListRemoteSkillTargetsResponse{Targets: make([]remoteskills.RemoteSkillTargetStatus, 0, limit)}
	for _, value := range values {
		if targetID != "" && value.TargetID() != targetID {
			continue
		}
		if len(result.Targets) == limit {
			break
		}
		target, targetErr := canonicalRemoteSkillTarget(value)
		if targetErr != nil {
			return remoteSkillInternalError(), nil
		}
		result.Targets = append(result.Targets, remoteskills.RemoteSkillTargetStatus{Target: target, Publications: []remoteskills.RemoteSkillPublication{}})
	}
	return &result, nil
}

func (h *CanonicalRemoteSkillHandler) RenameRemoteSkillTarget(
	ctx context.Context,
	request *remoteskills.RenameRemoteSkillTargetRequest,
) (remoteskills.RenameRemoteSkillTargetRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return remoteSkillInvalidRequest(), nil
	}
	value, err := h.operations.Rename(ctx, runtime.RemoteSkillTargetRenameInput{
		ScopeID: request.ScopeID, TargetID: request.TargetID, DisplayName: request.DisplayName, ExpectedGeneration: request.ExpectedGeneration,
	})
	if err != nil {
		if remoteSkillScopeNotFound(err) {
			return nil, err
		}
		return remoteSkillMutationError(err), nil
	}
	target, err := canonicalRemoteSkillTarget(value)
	if err != nil {
		return remoteSkillInternalError(), nil
	}
	return &target, nil
}

func (h *CanonicalRemoteSkillHandler) RevokeRemoteSkillTarget(
	ctx context.Context,
	request *remoteskills.RevokeRemoteSkillTargetRequest,
) (remoteskills.RevokeRemoteSkillTargetRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return remoteSkillInvalidRequest(), nil
	}
	value, err := h.operations.Revoke(ctx, runtime.RemoteSkillTargetRevokeInput{
		ScopeID: request.ScopeID, TargetID: request.TargetID, ExpectedGeneration: request.ExpectedGeneration,
	})
	if err != nil {
		if remoteSkillScopeNotFound(err) {
			return nil, err
		}
		return remoteSkillMutationError(err), nil
	}
	target, err := canonicalRemoteSkillTarget(value)
	if err != nil {
		return remoteSkillInternalError(), nil
	}
	return &target, nil
}

func canonicalRemoteSkillTarget(value skill.RemoteTargetView) (remoteskills.RemoteSkillTarget, error) {
	agentKind := remoteskills.RemoteAgentKind(value.AgentKind())
	if agentKind != remoteskills.RemoteAgentKindCodex && agentKind != remoteskills.RemoteAgentKindWorkbuddy {
		return remoteskills.RemoteSkillTarget{}, errors.New("remote Skill target has an unsupported agent kind")
	}
	target := remoteskills.RemoteSkillTarget{
		AgentKind: agentKind, DeliveryMode: remoteskills.RemoteSkillTargetDeliveryMode(value.DeliveryMode()),
		DisplayName: value.DisplayName(), Generation: value.Generation(),
		InstallationScope: remoteskills.RemoteSkillTargetInstallationScope(value.InstallationScope()),
		ScopeID:           value.ScopeID(), State: remoteskills.RemoteSkillTargetState(value.State()), TargetID: value.TargetID(),
		EnvironmentFingerprint: remoteSkillNilString(""), InstallationID: remoteSkillNilString(value.InstallationID()),
		LastSeenAt: remoteSkillNilDateTime(value.LastSeenAt()), MachineHostname: remoteSkillNilString(value.MachineHostname()),
		ReceiverVersion: remoteSkillNilString(value.ReceiverVersion()), WorkspaceName: remoteSkillNilString(value.WorkspaceName()),
	}
	if err := target.Validate(); err != nil {
		return remoteskills.RemoteSkillTarget{}, err
	}
	return target, nil
}

func remoteSkillOptionalString(value remoteskills.OptNilString) string {
	item, found := value.Get()
	if !found {
		return ""
	}
	return item
}

func remoteSkillNilString(value string) remoteskills.NilString {
	if value == "" {
		var result remoteskills.NilString
		result.SetToNull()
		return result
	}
	return remoteskills.NewNilString(value)
}

func remoteSkillNilDateTime(value time.Time) remoteskills.NilDateTime {
	if value.IsZero() {
		var result remoteskills.NilDateTime
		result.SetToNull()
		return result
	}
	return remoteskills.NewNilDateTime(value)
}

func remoteSkillCreateError(err error) remoteskills.CreateRemoteSkillTargetRes {
	return remoteSkillInternalError()
}

func remoteSkillScopeNotFound(err error) bool {
	_, missing := errors.AsType[*scope.NotFoundError](err)
	return missing
}

type remoteSkillMutationResponse interface {
	remoteskills.RenameRemoteSkillTargetRes
	remoteskills.RevokeRemoteSkillTargetRes
}

func remoteSkillMutationError(err error) remoteSkillMutationResponse {
	var missing *skill.RemoteTargetNotFoundError
	if errors.As(err, &missing) {
		return remoteSkillNotFound()
	}
	var conflict *skill.RemoteTargetGenerationConflictError
	if errors.As(err, &conflict) {
		return remoteSkillConflict("target_conflict", "The remote Skill target generation is stale.")
	}
	return remoteSkillInternalError()
}

func remoteSkillInvalidRequest() *remoteskills.InvalidRequestHeaders {
	return &remoteskills.InvalidRequestHeaders{Response: remoteSkillError("invalid_request", "The request is invalid.")}
}

func remoteSkillNotFound() *remoteskills.NotFoundHeaders {
	return &remoteskills.NotFoundHeaders{Response: remoteSkillError("not_found", "The requested resource was not found.")}
}

func remoteSkillConflict(code, message string) *remoteskills.ConflictHeaders {
	return &remoteskills.ConflictHeaders{Response: remoteSkillError(code, message)}
}

func remoteSkillInternalError() *remoteskills.InternalErrorHeaders {
	return &remoteskills.InternalErrorHeaders{Response: remoteSkillError("internal_error", "The Server failed.")}
}

func remoteSkillError(code, message string) remoteskills.ErrorResponse {
	var details remoteskills.NilErrorDetailDetails
	details.SetToNull()
	return remoteskills.ErrorResponse{Error: remoteskills.ErrorDetail{Code: code, Message: message, Details: details}}
}
