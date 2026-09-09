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

package skill_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/skill"
)

const remoteTargetDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRemoteTargetPendingShape(t *testing.T) {
	t.Parallel()

	target := mustNewRemoteTarget(t, remoteTargetInput(skill.RemoteTargetPending))
	if target.State() != skill.RemoteTargetPending || target.InstallationID() != "" || target.CredentialVerifier() != "" {
		t.Fatalf("pending target retained active fields: %#v", target)
	}
	if target.EnrollmentCodeDigest() != remoteTargetDigest || target.EnrollmentExpiresAt().IsZero() {
		t.Fatalf("pending target did not retain enrollment fields")
	}
}

func TestRemoteTargetActiveShape(t *testing.T) {
	t.Parallel()

	target := mustNewRemoteTarget(t, remoteTargetInput(skill.RemoteTargetActive))
	if target.State() != skill.RemoteTargetActive || target.InstallationID() != "workspace-a" || target.CredentialSubject() != "installation-a" {
		t.Fatalf("active target did not retain installation identity: %#v", target)
	}
	if target.EnrollmentCodeDigest() != "" || !target.EnrollmentExpiresAt().IsZero() {
		t.Fatalf("active target retained enrollment fields")
	}
	if target.CredentialVerifier() != remoteTargetDigest {
		t.Fatalf("active target did not retain credential verifier")
	}
}

func TestRemoteTargetRevokedShape(t *testing.T) {
	t.Parallel()

	input := remoteTargetInput(skill.RemoteTargetRevoked)
	input.EnrollmentExpiresAt = time.Time{}
	target := mustNewRemoteTarget(t, input)
	if target.State() != skill.RemoteTargetRevoked || target.InstallationID() != "workspace-a" || target.CredentialSubject() != "installation-a" {
		t.Fatalf("revoked target did not retain historical identity: %#v", target)
	}
	if target.EnrollmentCodeDigest() != "" || !target.EnrollmentExpiresAt().IsZero() || target.CredentialVerifier() != "" {
		t.Fatalf("revoked target retained usable credentials")
	}
}

func TestRemoteTargetActiveToRevokedRetainsIdentityAndClearsCredentials(t *testing.T) {
	t.Parallel()

	input := remoteTargetInput(skill.RemoteTargetActive)
	input.State = skill.RemoteTargetRevoked
	input.CredentialVerifier = ""
	target := mustNewRemoteTarget(t, input)
	if target.InstallationID() != "workspace-a" || target.CredentialSubject() != "installation-a" {
		t.Fatalf("revoked target lost historical identity: %#v", target)
	}
	if target.EnrollmentCodeDigest() != "" || !target.EnrollmentExpiresAt().IsZero() || target.CredentialVerifier() != "" {
		t.Fatalf("revoked target retained usable credentials: %#v", target)
	}
}

func TestRemoteTargetRejectsInvalidStateShapes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input skill.RemoteTargetInput
	}{
		{
			name: "pending installation identity",
			input: func() skill.RemoteTargetInput {
				input := remoteTargetInput(skill.RemoteTargetPending)
				input.InstallationID = "workspace-a"
				return input
			}(),
		},
		{
			name: "active enrollment expiry",
			input: func() skill.RemoteTargetInput {
				input := remoteTargetInput(skill.RemoteTargetActive)
				input.EnrollmentExpiresAt = input.CreatedAt.Add(time.Minute)
				return input
			}(),
		},
		{
			name: "revoked enrollment expiry",
			input: func() skill.RemoteTargetInput {
				input := remoteTargetInput(skill.RemoteTargetRevoked)
				input.EnrollmentExpiresAt = input.CreatedAt.Add(time.Minute)
				return input
			}(),
		},
		{
			name: "revoked credential verifier",
			input: func() skill.RemoteTargetInput {
				input := remoteTargetInput(skill.RemoteTargetRevoked)
				input.CredentialVerifier = remoteTargetDigest
				return input
			}(),
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := skill.NewRemoteTarget(testCase.input); err == nil {
				t.Fatal("NewRemoteTarget() error = nil, want state-shape refusal")
			}
		})
	}
}

func TestRemoteTargetRestrictsAgentKinds(t *testing.T) {
	t.Parallel()

	for _, agentKind := range []skill.AgentKind{skill.CodexAgent, skill.WorkBuddyAgent} {
		input := remoteTargetInput(skill.RemoteTargetPending)
		input.AgentKind = agentKind
		if _, err := skill.NewRemoteTarget(input); err != nil {
			t.Fatalf("NewRemoteTarget(%q) error = %v", agentKind, err)
		}
	}
	input := remoteTargetInput(skill.RemoteTargetPending)
	input.AgentKind = skill.ClaudeCodeAgent
	_, err := skill.NewRemoteTarget(input)
	var unsupported *skill.UnsupportedAgentKindError
	if !errors.As(err, &unsupported) {
		t.Fatalf("NewRemoteTarget(claude_code) error = %T %v, want UnsupportedAgentKindError", err, err)
	}
}

func TestRemoteTargetValidatesIdentityGenerationAndTimestamps(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input skill.RemoteTargetInput
	}{
		{"target ID", withRemoteTarget(func(input *skill.RemoteTargetInput) { input.TargetID = "Invalid" })},
		{"display name", withRemoteTarget(func(input *skill.RemoteTargetInput) { input.DisplayName = "  " })},
		{"installation scope", withRemoteTarget(func(input *skill.RemoteTargetInput) { input.InstallationScope = "user" })},
		{"delivery mode", withRemoteTarget(func(input *skill.RemoteTargetInput) { input.DeliveryMode = "push" })},
		{"generation", withRemoteTarget(func(input *skill.RemoteTargetInput) { input.Generation = -1 })},
		{"timestamps", withRemoteTarget(func(input *skill.RemoteTargetInput) { input.UpdatedAt = input.CreatedAt.Add(-time.Nanosecond) })},
		{"digest", withRemoteTarget(func(input *skill.RemoteTargetInput) { input.EnrollmentCodeDigest = strings.ToUpper(remoteTargetDigest) })},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := skill.NewRemoteTarget(testCase.input); err == nil {
				t.Fatal("NewRemoteTarget() error = nil, want validation refusal")
			}
		})
	}
}

func TestRemoteTargetViewOmitsEveryDigest(t *testing.T) {
	t.Parallel()

	target := mustNewRemoteTarget(t, remoteTargetInput(skill.RemoteTargetActive))
	view := target.View()
	if view.TargetID() != target.ID() || view.AgentKind() != target.AgentKind() || view.State() != target.State() || view.Generation() != target.Generation() {
		t.Fatalf("view did not preserve public identity: %#v", view)
	}
	if strings.Contains(view.String(), remoteTargetDigest) || strings.Contains(view.GoString(), remoteTargetDigest) ||
		strings.Contains(view.String(), target.EnvironmentFingerprint()) {
		t.Fatalf("credential-free view leaked a digest: %v", view)
	}
	for _, name := range []string{"EnrollmentCodeDigest", "CredentialVerifier", "EnvironmentFingerprint"} {
		if _, found := reflect.TypeOf(view).MethodByName(name); found {
			t.Fatalf("credential-free view exposes digest accessor %q", name)
		}
	}
}

func TestRemoteTargetTypedErrorsAreRedactedAndDistinct(t *testing.T) {
	t.Parallel()

	missing := error(&skill.RemoteTargetNotFoundError{})
	stale := error(&skill.RemoteTargetGenerationConflictError{})
	var missingTyped *skill.RemoteTargetNotFoundError
	var staleTyped *skill.RemoteTargetGenerationConflictError
	if !errors.As(missing, &missingTyped) || errors.As(missing, &staleTyped) ||
		!errors.As(stale, &staleTyped) || errors.As(stale, &missingTyped) {
		t.Fatalf("target errors are not distinct typed errors: missing=%T stale=%T", missing, stale)
	}
	if strings.Contains(missing.Error(), "target-a") || strings.Contains(stale.Error(), "target-a") {
		t.Fatalf("target errors leaked identity: missing=%q stale=%q", missing, stale)
	}
}

func remoteTargetInput(state skill.RemoteTargetState) skill.RemoteTargetInput {
	created := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	input := skill.RemoteTargetInput{
		ScopeID:           "project-a",
		TargetID:          "codex-a",
		DisplayName:       "Build machine A",
		AgentKind:         skill.CodexAgent,
		InstallationScope: skill.RemoteTargetProjectScope,
		DeliveryMode:      skill.RemoteTargetAgentPull,
		State:             state,
		Generation:        2,
		CreatedAt:         created,
		UpdatedAt:         created.Add(time.Second),
	}
	switch state {
	case skill.RemoteTargetPending:
		input.EnrollmentCodeDigest = remoteTargetDigest
		input.EnrollmentExpiresAt = created.Add(time.Minute)
	case skill.RemoteTargetActive:
		input.InstallationID = "workspace-a"
		input.CredentialSubject = "installation-a"
		input.CredentialVerifier = remoteTargetDigest
		input.ReceiverVersion = "0.1.0"
		input.EnvironmentFingerprint = remoteTargetDigest
		input.MachineHostname = "build-host-01"
		input.WorkspaceName = "project-a"
		input.LastSeenAt = created.Add(time.Second)
	case skill.RemoteTargetRevoked:
		input.InstallationID = "workspace-a"
		input.CredentialSubject = "installation-a"
		input.ReceiverVersion = "0.1.0"
		input.MachineHostname = "build-host-01"
	}
	return input
}

func withRemoteTarget(change func(*skill.RemoteTargetInput)) skill.RemoteTargetInput {
	input := remoteTargetInput(skill.RemoteTargetPending)
	change(&input)
	return input
}

func mustNewRemoteTarget(t *testing.T, input skill.RemoteTargetInput) skill.RemoteTarget {
	t.Helper()
	target, err := skill.NewRemoteTarget(input)
	if err != nil {
		t.Fatalf("NewRemoteTarget() error = %v", err)
	}
	return target
}
