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

package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/skill"
)

const remoteSkillTargetEnrollmentTTL = 15 * time.Minute

// RemoteSkillTargetStore is the persistence boundary consumed by the lifecycle
// application. Implementations must make each operation one short transaction.
type RemoteSkillTargetStore interface {
	Create(context.Context, skill.RemoteTarget) (skill.RemoteTarget, error)
	List(context.Context, string) ([]skill.RemoteTarget, error)
	FindByEnrollmentDigest(context.Context, string) (skill.RemoteTarget, bool, error)
	ConsumePending(context.Context, skill.RemoteTarget, string, time.Time) (skill.RemoteTarget, bool, error)
	Replace(context.Context, skill.RemoteTarget, int) (skill.RemoteTarget, error)
}

type RemoteSkillTargetApplicationOptions struct {
	Clock     func() time.Time
	NewID     func() string
	NewSecret func() string
}

type RemoteSkillTargetCreateInput struct {
	ScopeID     string
	DisplayName string
	AgentKind   skill.AgentKind
}

type RemoteSkillTargetCreateResult struct {
	Target              skill.RemoteTargetView
	EnrollmentCode      string
	EnrollmentExpiresAt time.Time
}

type RemoteSkillTargetEnrollInput struct {
	EnrollmentCode         string
	InstallationID         string
	CredentialSubject      string
	ReceiverVersion        string
	EnvironmentFingerprint string
	MachineHostname        string
	WorkspaceName          string
}

type RemoteSkillTargetEnrollResult struct {
	Target     skill.RemoteTargetView
	Credential string
}

type RemoteSkillTargetRenameInput struct {
	ScopeID            string
	TargetID           string
	DisplayName        string
	ExpectedGeneration int
}

type RemoteSkillTargetRevokeInput struct {
	ScopeID            string
	TargetID           string
	ExpectedGeneration int
}

// RemoteSkillTargetEnrollmentRefusedError deliberately represents every
// unusable enrollment code without exposing target identity or lifecycle state.
type RemoteSkillTargetEnrollmentRefusedError struct{}

func (*RemoteSkillTargetEnrollmentRefusedError) Error() string {
	return "remote Skill target enrollment was refused"
}

type RemoteSkillTargetApplication struct {
	runtime   *Runtime
	store     RemoteSkillTargetStore
	clock     func() time.Time
	newID     func() string
	newSecret func() string
}

func NewRemoteSkillTargetApplication(
	runtime *Runtime,
	store RemoteSkillTargetStore,
	options RemoteSkillTargetApplicationOptions,
) (*RemoteSkillTargetApplication, error) {
	if runtime == nil {
		return nil, errors.New("runtime: remote Skill target Runtime must not be nil")
	}
	if store == nil || options.Clock == nil || options.NewID == nil || options.NewSecret == nil {
		return nil, errors.New("runtime: remote Skill target dependencies must not be nil")
	}
	return &RemoteSkillTargetApplication{runtime: runtime, store: store, clock: options.Clock, newID: options.NewID, newSecret: options.NewSecret}, nil
}

func (a *RemoteSkillTargetApplication) Create(
	ctx context.Context,
	input RemoteSkillTargetCreateInput,
) (result RemoteSkillTargetCreateResult, returnErr error) {
	return result, a.runtime.ScopedWrite(ctx, input.ScopeID, func(ctx context.Context, scopeID string) error {
		now := a.clock().UTC()
		enrollmentCode := a.newSecret()
		target, err := skill.NewRemoteTarget(skill.RemoteTargetInput{
			ScopeID: scopeID, TargetID: a.newID(), DisplayName: input.DisplayName, AgentKind: input.AgentKind,
			InstallationScope: skill.RemoteTargetProjectScope, DeliveryMode: skill.RemoteTargetAgentPull,
			State: skill.RemoteTargetPending, EnrollmentCodeDigest: remoteSkillTargetDigest(enrollmentCode),
			EnrollmentExpiresAt: now.Add(remoteSkillTargetEnrollmentTTL), CreatedAt: now, UpdatedAt: now,
		})
		if err != nil {
			return err
		}
		stored, err := a.store.Create(ctx, target)
		if err != nil {
			return err
		}
		result = RemoteSkillTargetCreateResult{
			Target: stored.View(), EnrollmentCode: enrollmentCode, EnrollmentExpiresAt: stored.EnrollmentExpiresAt(),
		}
		return nil
	})
}

func (a *RemoteSkillTargetApplication) List(ctx context.Context, scopeID string) (result []skill.RemoteTargetView, returnErr error) {
	return result, a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		targets, err := a.store.List(ctx, scope)
		if err != nil {
			return err
		}
		result = make([]skill.RemoteTargetView, len(targets))
		for index, target := range targets {
			result[index] = target.View()
		}
		return nil
	})
}

func (a *RemoteSkillTargetApplication) Enroll(
	ctx context.Context,
	input RemoteSkillTargetEnrollInput,
) (result RemoteSkillTargetEnrollResult, returnErr error) {
	digest := remoteSkillTargetDigest(input.EnrollmentCode)
	target, found, err := a.store.FindByEnrollmentDigest(ctx, digest)
	if err != nil {
		return result, err
	}
	if !found || target.State() != skill.RemoteTargetPending || !target.EnrollmentExpiresAt().After(a.clock()) {
		return result, &RemoteSkillTargetEnrollmentRefusedError{}
	}
	return result, a.runtime.ScopedWrite(ctx, target.ScopeID(), func(ctx context.Context, scope string) error {
		credential := a.newSecret()
		now := a.clock().UTC()
		next, buildErr := skill.NewRemoteTarget(skill.RemoteTargetInput{
			ScopeID: scope, TargetID: target.ID(), DisplayName: target.DisplayName(), AgentKind: target.AgentKind(),
			InstallationScope: target.InstallationScope(), DeliveryMode: target.DeliveryMode(), State: skill.RemoteTargetActive,
			InstallationID: input.InstallationID, CredentialSubject: input.CredentialSubject,
			CredentialVerifier: remoteSkillTargetDigest(credential), ReceiverVersion: input.ReceiverVersion,
			EnvironmentFingerprint: input.EnvironmentFingerprint, MachineHostname: input.MachineHostname, WorkspaceName: input.WorkspaceName,
			LastSeenAt: now, Generation: target.Generation() + 1, CreatedAt: target.CreatedAt(), UpdatedAt: now,
		})
		if buildErr != nil {
			return buildErr
		}
		stored, consumed, consumeErr := a.store.ConsumePending(ctx, next, digest, now)
		if consumeErr != nil {
			return consumeErr
		}
		if !consumed {
			return &RemoteSkillTargetEnrollmentRefusedError{}
		}
		result = RemoteSkillTargetEnrollResult{Target: stored.View(), Credential: credential}
		return nil
	})
}

func (a *RemoteSkillTargetApplication) Rename(
	ctx context.Context,
	input RemoteSkillTargetRenameInput,
) (result skill.RemoteTargetView, returnErr error) {
	return result, a.runtime.ScopedWrite(ctx, input.ScopeID, func(ctx context.Context, scope string) error {
		targets, err := a.store.List(ctx, scope)
		if err != nil {
			return err
		}
		target, err := remoteSkillTargetByID(targets, input.TargetID)
		if err != nil {
			return err
		}
		if target.Generation() != input.ExpectedGeneration {
			return &skill.RemoteTargetGenerationConflictError{}
		}
		if target.DisplayName() == input.DisplayName {
			result = target.View()
			return nil
		}
		next, err := remoteSkillTargetCopy(target, input.DisplayName, target.State(), target.Generation()+1, a.clock().UTC())
		if err != nil {
			return err
		}
		stored, err := a.store.Replace(ctx, next, target.Generation())
		if err == nil {
			result = stored.View()
		}
		return err
	})
}

func (a *RemoteSkillTargetApplication) Revoke(
	ctx context.Context,
	input RemoteSkillTargetRevokeInput,
) (result skill.RemoteTargetView, returnErr error) {
	return result, a.runtime.ScopedWrite(ctx, input.ScopeID, func(ctx context.Context, scope string) error {
		targets, err := a.store.List(ctx, scope)
		if err != nil {
			return err
		}
		target, err := remoteSkillTargetByID(targets, input.TargetID)
		if err != nil {
			return err
		}
		if target.Generation() != input.ExpectedGeneration {
			return &skill.RemoteTargetGenerationConflictError{}
		}
		if target.State() == skill.RemoteTargetRevoked {
			result = target.View()
			return nil
		}
		next, err := remoteSkillTargetCopy(target, target.DisplayName(), skill.RemoteTargetRevoked, target.Generation()+1, a.clock().UTC())
		if err != nil {
			return err
		}
		stored, err := a.store.Replace(ctx, next, target.Generation())
		if err == nil {
			result = stored.View()
		}
		return err
	})
}

func remoteSkillTargetByID(targets []skill.RemoteTarget, targetID string) (skill.RemoteTarget, error) {
	for _, target := range targets {
		if target.ID() == targetID {
			return target, nil
		}
	}
	return skill.RemoteTarget{}, &skill.RemoteTargetNotFoundError{}
}

func remoteSkillTargetCopy(target skill.RemoteTarget, displayName string, state skill.RemoteTargetState, generation int, updatedAt time.Time) (skill.RemoteTarget, error) {
	input := skill.RemoteTargetInput{
		ScopeID: target.ScopeID(), TargetID: target.ID(), DisplayName: displayName, AgentKind: target.AgentKind(),
		InstallationScope: target.InstallationScope(), DeliveryMode: target.DeliveryMode(), State: state,
		InstallationID: target.InstallationID(), CredentialSubject: target.CredentialSubject(), ReceiverVersion: target.ReceiverVersion(),
		EnvironmentFingerprint: target.EnvironmentFingerprint(), MachineHostname: target.MachineHostname(), WorkspaceName: target.WorkspaceName(),
		LastSeenAt: target.LastSeenAt(), Generation: generation, CreatedAt: target.CreatedAt(), UpdatedAt: updatedAt,
	}
	if state == skill.RemoteTargetPending {
		input.EnrollmentCodeDigest = target.EnrollmentCodeDigest()
		input.EnrollmentExpiresAt = target.EnrollmentExpiresAt()
	}
	if state == skill.RemoteTargetActive {
		input.CredentialVerifier = target.CredentialVerifier()
	}
	return skill.NewRemoteTarget(input)
}

func remoteSkillTargetDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
