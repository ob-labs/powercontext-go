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
	"fmt"
	"time"
	"unicode/utf8"
)

// RemoteTargetState is the enrollment lifecycle of a remote managed-Skill
// receiver. A target is a remote identity, not a server-interpreted path.
type RemoteTargetState string

const (
	RemoteTargetPending RemoteTargetState = "pending"
	RemoteTargetActive  RemoteTargetState = "active"
	RemoteTargetRevoked RemoteTargetState = "revoked"
)

type RemoteTargetInstallationScope string

const RemoteTargetProjectScope RemoteTargetInstallationScope = "project"

type RemoteTargetDeliveryMode string

const RemoteTargetAgentPull RemoteTargetDeliveryMode = "agent_pull"

// RemoteTargetInput is the complete durable target record accepted by
// NewRemoteTarget. It contains digests only; raw enrollment codes and target
// credentials never belong to a RemoteTarget.
type RemoteTargetInput struct {
	ScopeID                string
	TargetID               string
	DisplayName            string
	AgentKind              AgentKind
	InstallationScope      RemoteTargetInstallationScope
	DeliveryMode           RemoteTargetDeliveryMode
	State                  RemoteTargetState
	InstallationID         string
	EnrollmentCodeDigest   string
	EnrollmentExpiresAt    time.Time
	CredentialSubject      string
	CredentialVerifier     string
	ReceiverVersion        string
	EnvironmentFingerprint string
	MachineHostname        string
	WorkspaceName          string
	LastSeenAt             time.Time
	Generation             int
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// RemoteTarget is an immutable durable remote managed-Skill target. The
// digest accessors exist solely for trusted persistence and authentication
// boundaries; administrative output must use View.
type RemoteTarget struct{ input RemoteTargetInput }

// NewRemoteTarget validates and constructs one immutable durable remote target.
func NewRemoteTarget(input RemoteTargetInput) (RemoteTarget, error) {
	if err := validateRemoteTarget(input); err != nil {
		return RemoteTarget{}, err
	}
	input.CreatedAt = input.CreatedAt.UTC()
	input.UpdatedAt = input.UpdatedAt.UTC()
	input.EnrollmentExpiresAt = normalizeRemoteTargetTime(input.EnrollmentExpiresAt)
	input.LastSeenAt = normalizeRemoteTargetTime(input.LastSeenAt)
	return RemoteTarget{input: input}, nil
}

func (t RemoteTarget) ScopeID() string      { return t.input.ScopeID }
func (t RemoteTarget) ID() string           { return t.input.TargetID }
func (t RemoteTarget) DisplayName() string  { return t.input.DisplayName }
func (t RemoteTarget) AgentKind() AgentKind { return t.input.AgentKind }
func (t RemoteTarget) InstallationScope() RemoteTargetInstallationScope {
	return t.input.InstallationScope
}
func (t RemoteTarget) DeliveryMode() RemoteTargetDeliveryMode { return t.input.DeliveryMode }
func (t RemoteTarget) State() RemoteTargetState               { return t.input.State }
func (t RemoteTarget) InstallationID() string                 { return t.input.InstallationID }
func (t RemoteTarget) EnrollmentCodeDigest() string           { return t.input.EnrollmentCodeDigest }
func (t RemoteTarget) EnrollmentExpiresAt() time.Time         { return t.input.EnrollmentExpiresAt }
func (t RemoteTarget) CredentialSubject() string              { return t.input.CredentialSubject }
func (t RemoteTarget) CredentialVerifier() string             { return t.input.CredentialVerifier }
func (t RemoteTarget) ReceiverVersion() string                { return t.input.ReceiverVersion }
func (t RemoteTarget) EnvironmentFingerprint() string         { return t.input.EnvironmentFingerprint }
func (t RemoteTarget) MachineHostname() string                { return t.input.MachineHostname }
func (t RemoteTarget) WorkspaceName() string                  { return t.input.WorkspaceName }
func (t RemoteTarget) LastSeenAt() time.Time                  { return t.input.LastSeenAt }
func (t RemoteTarget) Generation() int                        { return t.input.Generation }
func (t RemoteTarget) CreatedAt() time.Time                   { return t.input.CreatedAt }
func (t RemoteTarget) UpdatedAt() time.Time                   { return t.input.UpdatedAt }

// View returns the credential-free administrative representation. It omits
// enrollment and credential digests even though the durable target retains
// them for one-time enrollment and receiver authentication.
func (t RemoteTarget) View() RemoteTargetView {
	return RemoteTargetView{
		scopeID: t.ScopeID(), targetID: t.ID(), displayName: t.DisplayName(), agentKind: t.AgentKind(),
		installationScope: t.InstallationScope(), deliveryMode: t.DeliveryMode(), state: t.State(),
		installationID: t.InstallationID(), credentialSubject: t.CredentialSubject(), receiverVersion: t.ReceiverVersion(),
		machineHostname: t.MachineHostname(), workspaceName: t.WorkspaceName(), lastSeenAt: t.LastSeenAt(),
		generation: t.Generation(), createdAt: t.CreatedAt(), updatedAt: t.UpdatedAt(),
	}
}

// String and GoString deliberately avoid rendering durable digests.
func (t RemoteTarget) String() string   { return t.View().String() }
func (t RemoteTarget) GoString() string { return t.View().GoString() }

// RemoteTargetView is the immutable credential-free administrative view of a
// remote target. It has no enrollment or credential digest field or accessor.
type RemoteTargetView struct {
	scopeID           string
	targetID          string
	displayName       string
	agentKind         AgentKind
	installationScope RemoteTargetInstallationScope
	deliveryMode      RemoteTargetDeliveryMode
	state             RemoteTargetState
	installationID    string
	credentialSubject string
	receiverVersion   string
	machineHostname   string
	workspaceName     string
	lastSeenAt        time.Time
	generation        int
	createdAt         time.Time
	updatedAt         time.Time
}

func (v RemoteTargetView) ScopeID() string      { return v.scopeID }
func (v RemoteTargetView) TargetID() string     { return v.targetID }
func (v RemoteTargetView) DisplayName() string  { return v.displayName }
func (v RemoteTargetView) AgentKind() AgentKind { return v.agentKind }
func (v RemoteTargetView) InstallationScope() RemoteTargetInstallationScope {
	return v.installationScope
}
func (v RemoteTargetView) DeliveryMode() RemoteTargetDeliveryMode { return v.deliveryMode }
func (v RemoteTargetView) State() RemoteTargetState               { return v.state }
func (v RemoteTargetView) InstallationID() string                 { return v.installationID }
func (v RemoteTargetView) CredentialSubject() string              { return v.credentialSubject }
func (v RemoteTargetView) ReceiverVersion() string                { return v.receiverVersion }
func (v RemoteTargetView) MachineHostname() string                { return v.machineHostname }
func (v RemoteTargetView) WorkspaceName() string                  { return v.workspaceName }
func (v RemoteTargetView) LastSeenAt() time.Time                  { return v.lastSeenAt }
func (v RemoteTargetView) Generation() int                        { return v.generation }
func (v RemoteTargetView) CreatedAt() time.Time                   { return v.createdAt }
func (v RemoteTargetView) UpdatedAt() time.Time                   { return v.updatedAt }

func (v RemoteTargetView) String() string {
	return fmt.Sprintf("remote target (%s, %s)", v.agentKind, v.state)
}

func (v RemoteTargetView) GoString() string { return v.String() }

// RemoteTargetNotFoundError allows transport code to map a missing target to
// its public 404 contract without embedding a target identity in the error.
type RemoteTargetNotFoundError struct{}

func (*RemoteTargetNotFoundError) Error() string { return "remote Skill target was not found" }

// RemoteTargetGenerationConflictError allows transport code to map a stale
// lifecycle mutation to its public 409 contract without embedding identifiers.
type RemoteTargetGenerationConflictError struct{}

func (*RemoteTargetGenerationConflictError) Error() string {
	return "remote Skill target generation changed"
}

func validateRemoteTarget(input RemoteTargetInput) error {
	if err := trimmedBounded("remote Skill target scope ID", input.ScopeID, 256); err != nil {
		return err
	}
	if len(input.TargetID) < 1 || len(input.TargetID) > 64 || !rootIDPattern.MatchString(input.TargetID) {
		return fmt.Errorf("remote Skill target ID is invalid")
	}
	if err := trimmedBounded("remote Skill target display name", input.DisplayName, 128); err != nil {
		return err
	}
	if !validAgentKind(string(input.AgentKind)) {
		return &UnsupportedAgentKindError{}
	}
	if input.InstallationScope != RemoteTargetProjectScope {
		return fmt.Errorf("remote Skill target installation scope is invalid")
	}
	if input.DeliveryMode != RemoteTargetAgentPull {
		return fmt.Errorf("remote Skill target delivery mode is invalid")
	}
	if input.Generation < 0 {
		return fmt.Errorf("remote Skill target generation is invalid")
	}
	if input.CreatedAt.IsZero() || input.UpdatedAt.IsZero() || input.UpdatedAt.Before(input.CreatedAt) {
		return fmt.Errorf("remote Skill target timestamps are invalid")
	}
	if err := validateOptionalRemoteTargetText("installation ID", input.InstallationID, 128); err != nil {
		return err
	}
	if err := validateOptionalRemoteTargetText("credential subject", input.CredentialSubject, 128); err != nil {
		return err
	}
	if err := validateOptionalRemoteTargetText("receiver version", input.ReceiverVersion, 64); err != nil {
		return err
	}
	if err := validateOptionalRemoteTargetText("machine hostname", input.MachineHostname, 255); err != nil {
		return err
	}
	if err := validateOptionalRemoteTargetText("workspace name", input.WorkspaceName, 128); err != nil {
		return err
	}
	if input.EnvironmentFingerprint != "" && !lowerHexFingerprint.MatchString(input.EnvironmentFingerprint) {
		return fmt.Errorf("remote Skill target environment fingerprint is invalid")
	}
	if !input.LastSeenAt.IsZero() && input.LastSeenAt.Before(input.CreatedAt) {
		return fmt.Errorf("remote Skill target last-seen timestamp is invalid")
	}
	return validateRemoteTargetState(input)
}

func validateRemoteTargetState(input RemoteTargetInput) error {
	switch input.State {
	case RemoteTargetPending:
		if !lowerHexFingerprint.MatchString(input.EnrollmentCodeDigest) || input.EnrollmentExpiresAt.IsZero() ||
			!input.EnrollmentExpiresAt.After(input.CreatedAt) {
			return fmt.Errorf("pending remote Skill target requires an enrollment digest and future expiry")
		}
		if input.InstallationID != "" || input.CredentialSubject != "" || input.CredentialVerifier != "" ||
			input.ReceiverVersion != "" || input.EnvironmentFingerprint != "" || input.MachineHostname != "" ||
			input.WorkspaceName != "" || !input.LastSeenAt.IsZero() {
			return fmt.Errorf("pending remote Skill target cannot retain an installation credential or receiver metadata")
		}
	case RemoteTargetActive:
		if input.EnrollmentCodeDigest != "" || !input.EnrollmentExpiresAt.IsZero() {
			return fmt.Errorf("active remote Skill target cannot retain enrollment credentials")
		}
		if input.InstallationID == "" || input.CredentialSubject == "" || !lowerHexFingerprint.MatchString(input.CredentialVerifier) ||
			input.ReceiverVersion == "" || input.LastSeenAt.IsZero() {
			return fmt.Errorf("active remote Skill target requires an installation credential and receiver metadata")
		}
	case RemoteTargetRevoked:
		if input.EnrollmentCodeDigest != "" || !input.EnrollmentExpiresAt.IsZero() || input.CredentialVerifier != "" {
			return fmt.Errorf("revoked remote Skill target cannot retain usable credentials")
		}
		if input.InstallationID == "" && input.CredentialSubject != "" || input.InstallationID != "" && input.CredentialSubject == "" {
			return fmt.Errorf("revoked remote Skill target has incomplete historical installation identity")
		}
	default:
		return fmt.Errorf("remote Skill target state is invalid")
	}
	return nil
}

func validateOptionalRemoteTargetText(label, value string, maximum int) error {
	if value == "" {
		return nil
	}
	if value != trimPythonWhitespace(value) || utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("remote Skill target %s is invalid", label)
	}
	return nil
}

func normalizeRemoteTargetTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC()
}
