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

package endpoint_test

import (
	"context"
	"testing"
	"time"

	remoteskills "github.com/ob-labs/powercontext-go/api/canonical/remoteskills"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
	"github.com/ob-labs/powercontext-go/internal/runtime"
)

func TestCanonicalRemoteSkillCreateProjectsOneTimeEnrollment(t *testing.T) {
	t.Parallel()

	operations := remoteSkillOperations{
		create: runtime.RemoteSkillTargetCreateResult{
			Target: remoteSkillTestView(t), EnrollmentCode: "enrollment-secret",
			EnrollmentExpiresAt: time.Date(2026, 9, 10, 1, 0, 0, 0, time.UTC),
		},
	}
	handler := endpoint.NewCanonicalRemoteSkillHandler(&operations)
	response, err := handler.CreateRemoteSkillTarget(t.Context(), &remoteskills.CreateRemoteSkillTargetRequest{
		ScopeID: "scope-a", DisplayName: "workstation", AgentKind: remoteskills.RemoteAgentKindCodex,
	})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, ok := response.(*remoteskills.RemoteSkillTargetEnrollment)
	if !ok || enrollment.EnrollmentCode != "enrollment-secret" {
		t.Fatalf("response = %#v, want enrollment code", response)
	}
	if operations.createInput.ScopeID != "scope-a" || operations.createInput.AgentKind != skill.CodexAgent {
		t.Fatalf("runtime input = %#v", operations.createInput)
	}
}

func remoteSkillTestView(t *testing.T) skill.RemoteTargetView {
	t.Helper()
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	target, err := skill.NewRemoteTarget(skill.RemoteTargetInput{
		ScopeID: "scope-a", TargetID: "target-a", DisplayName: "workstation", AgentKind: skill.CodexAgent,
		InstallationScope: skill.RemoteTargetProjectScope, DeliveryMode: skill.RemoteTargetAgentPull,
		State: skill.RemoteTargetPending, EnrollmentCodeDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		EnrollmentExpiresAt: now.Add(time.Minute), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return target.View()
}

func TestCanonicalRemoteSkillMapsLifecycleRefusalsWithoutSecrets(t *testing.T) {
	t.Parallel()

	operations := remoteSkillOperations{enrollErr: &runtime.RemoteSkillTargetEnrollmentRefusedError{}}
	handler := endpoint.NewCanonicalRemoteSkillHandler(&operations)
	response, err := handler.EnrollRemoteSkillTarget(t.Context(), &remoteskills.EnrollRemoteSkillTargetRequest{
		EnrollmentCode: "enrollment-secret", InstallationID: "install-a", ReceiverVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	conflict, ok := response.(*remoteskills.ConflictHeaders)
	if !ok || conflict.Response.Error.Code != "enrollment_refused" ||
		conflict.Response.Error.Message == "" {
		t.Fatalf("response = %#v, want redacted enrollment refusal", response)
	}
	if conflict.Response.Error.Details.IsNull() == false {
		t.Fatalf("refusal details = %#v, want null", conflict.Response.Error.Details)
	}
}

type remoteSkillOperations struct {
	create      runtime.RemoteSkillTargetCreateResult
	createInput runtime.RemoteSkillTargetCreateInput
	enrollErr   error
}

func (o *remoteSkillOperations) Create(
	_ context.Context,
	input runtime.RemoteSkillTargetCreateInput,
) (runtime.RemoteSkillTargetCreateResult, error) {
	o.createInput = input
	return o.create, nil
}

func (o *remoteSkillOperations) List(context.Context, string) ([]skill.RemoteTargetView, error) {
	return nil, nil
}

func (o *remoteSkillOperations) Enroll(
	context.Context,
	runtime.RemoteSkillTargetEnrollInput,
) (runtime.RemoteSkillTargetEnrollResult, error) {
	return runtime.RemoteSkillTargetEnrollResult{}, o.enrollErr
}

func (o *remoteSkillOperations) Rename(
	context.Context,
	runtime.RemoteSkillTargetRenameInput,
) (skill.RemoteTargetView, error) {
	return skill.RemoteTargetView{}, nil
}

func (o *remoteSkillOperations) Revoke(
	context.Context,
	runtime.RemoteSkillTargetRevokeInput,
) (skill.RemoteTargetView, error) {
	return skill.RemoteTargetView{}, nil
}
