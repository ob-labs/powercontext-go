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
	"context"
	"fmt"
	"regexp"
	"slices"
)

const (
	MaxExternalFiles         = 256
	MaxExternalPackageBytes  = 4 * 1024 * 1024
	MaxExternalManifestBytes = 128 * 1024
	MaxExternalHostIDLength  = 128
	MaxExternalLocatorLength = 2_000
)

var (
	lowerHexFingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)
	rootIDPattern       = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
)

type InstallationScope string

const (
	UserScope    InstallationScope = "user"
	ProjectScope InstallationScope = "project"
	PluginScope  InstallationScope = "plugin"
)

type AgentKind string

const (
	CodexAgent      AgentKind = "codex"
	ClaudeCodeAgent AgentKind = "claude_code"
)

func validAgentKind(value string) bool {
	return value == string(CodexAgent) || value == string(ClaudeCodeAgent)
}

type ResolutionStatus string

const (
	Available   ResolutionStatus = "available"
	Unavailable ResolutionStatus = "unavailable"
)

type ExternalNotFoundError struct{ ExternalSkillID string }

func (e *ExternalNotFoundError) Error() string { return "external Skill registration was not found" }

type ExternalRegistryUnavailableError struct{}

func (*ExternalRegistryUnavailableError) Error() string {
	return "external Skill Registry is not configured"
}

type ExternalSnapshotUnavailableError struct{ ExternalSkillID string }

func (e *ExternalSnapshotUnavailableError) Error() string {
	return "external Skill snapshot is unavailable"
}

type Registration struct {
	externalSkillID   string
	provider          string
	agentKind         string
	hostID            string
	installationScope InstallationScope
	locator           string
	fingerprint       string
	name              string
	description       string
}

func NewRegistration(
	externalSkillID, provider, agentKind, hostID string,
	installationScope InstallationScope,
	locator, fingerprint, name, description string,
) (Registration, error) {
	for _, value := range []struct {
		label   string
		text    string
		maximum int
	}{
		{"external_skill_id", externalSkillID, artifactIDLimit},
		{"provider", provider, 128},
		{"agent_kind", agentKind, 128},
		{"host_id", hostID, MaxExternalHostIDLength},
		{"locator", locator, MaxExternalLocatorLength},
		{"name", name, MaxNameLength},
		{"description", description, MaxDescriptionLength},
	} {
		if err := externalText(value.label, value.text, value.maximum); err != nil {
			return Registration{}, err
		}
	}
	if installationScope != UserScope && installationScope != ProjectScope && installationScope != PluginScope {
		return Registration{}, fmt.Errorf("invalid external Skill installation scope %q", installationScope)
	}
	if !validAgentKind(provider) {
		return Registration{}, fmt.Errorf("invalid external Skill provider %q", provider)
	}
	if !validAgentKind(agentKind) {
		return Registration{}, fmt.Errorf("invalid external Skill agent kind %q", agentKind)
	}
	if !lowerHexFingerprint.MatchString(fingerprint) {
		return Registration{}, fmt.Errorf("external Skill fingerprint must be 64 lowercase hexadecimal characters")
	}
	return Registration{
		externalSkillID: externalSkillID, provider: provider, agentKind: agentKind, hostID: hostID,
		installationScope: installationScope, locator: locator, fingerprint: fingerprint,
		name: name, description: description,
	}, nil
}

const artifactIDLimit = 128

func (r Registration) ExternalSkillID() string              { return r.externalSkillID }
func (r Registration) Provider() string                     { return r.provider }
func (r Registration) AgentKind() string                    { return r.agentKind }
func (r Registration) HostID() string                       { return r.hostID }
func (r Registration) InstallationScope() InstallationScope { return r.installationScope }
func (r Registration) Locator() string                      { return r.locator }
func (r Registration) Fingerprint() string                  { return r.fingerprint }
func (r Registration) Name() string                         { return r.name }
func (r Registration) Description() string                  { return r.description }

type Resolution struct {
	Registration Registration
	Status       ResolutionStatus
	Entrypoint   string
}

type ProviderScan struct {
	registrations []Registration
	skipped       int
}

func NewProviderScan(registrations []Registration, skipped int) (ProviderScan, error) {
	if skipped < 0 {
		return ProviderScan{}, fmt.Errorf("external Skill skipped count must not be negative")
	}
	return ProviderScan{registrations: slices.Clone(registrations), skipped: skipped}, nil
}

func (s ProviderScan) Registrations() []Registration { return slices.Clone(s.registrations) }
func (s ProviderScan) Skipped() int                  { return s.skipped }

type Snapshot struct {
	registration Registration
	manifest     string
}

func NewSnapshot(registration Registration, manifest string) (Snapshot, error) {
	if manifest == "" || len([]byte(manifest)) > MaxExternalManifestBytes {
		return Snapshot{}, fmt.Errorf("external Skill manifest must contain 1..%d UTF-8 bytes", MaxExternalManifestBytes)
	}
	return Snapshot{registration: registration, manifest: manifest}, nil
}

func (s Snapshot) Registration() Registration { return s.registration }
func (s Snapshot) Manifest() string           { return s.manifest }

type ExternalProvider interface {
	Name() string
	AgentKind() string
	HostID() string
	ProviderNames() []string
	Scan(context.Context) (ProviderScan, error)
	Resolve(context.Context, Registration) (Resolution, error)
}

func CaptureSnapshot(ctx context.Context, provider ExternalProvider, registration Registration) (Snapshot, error) {
	resolution, err := provider.Resolve(ctx, registration)
	if err != nil {
		return Snapshot{}, err
	}
	if resolution.Status != Available || resolution.Entrypoint == "" {
		return Snapshot{}, &ExternalSnapshotUnavailableError{ExternalSkillID: registration.externalSkillID}
	}
	manifest, err := readManifest(resolution.Entrypoint)
	if err != nil {
		return Snapshot{}, &ExternalSnapshotUnavailableError{ExternalSkillID: registration.externalSkillID}
	}
	confirmed, err := provider.Resolve(ctx, registration)
	if err != nil {
		return Snapshot{}, err
	}
	if confirmed.Status != Available || confirmed.Entrypoint != resolution.Entrypoint {
		return Snapshot{}, &ExternalSnapshotUnavailableError{ExternalSkillID: registration.externalSkillID}
	}
	return NewSnapshot(registration, manifest)
}

func unavailable(registration Registration) Resolution {
	return Resolution{Registration: registration, Status: Unavailable}
}
