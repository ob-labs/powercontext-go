// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package personalsvc

import "context"

// ProbeState is the adapter result for one endpoint probe. Conflict is kept
// separate from liveness because it must fail before an artifact is written.
type ProbeState string

const (
	// ProbeLive means the endpoint returned the PowerContext liveness contract.
	ProbeLive ProbeState = "live"
	// ProbeUnreachable means the endpoint cannot currently be reached.
	ProbeUnreachable ProbeState = "unreachable"
	// ProbeConflict means another listener occupies the endpoint.
	ProbeConflict ProbeState = "conflict"
)

// Artifact is an immutable inspection result for the exact native artifact.
// Its registration is present only when State is RegistrationInstalled.
type Artifact struct {
	state           RegistrationState
	definition      DefinitionState
	registration    Registration
	hasRegistration bool
	restoreSnapshot *restoreSnapshot
}

type restoreSnapshot struct{ value any }

// InstalledArtifact returns a verified owned artifact with its executable
// available. Adapters may use InstalledArtifactWithDefinition when they can
// independently determine a missing executable.
func InstalledArtifact(registration Registration) Artifact {
	return InstalledArtifactWithDefinition(registration, DefinitionCurrent)
}

// InstalledArtifactWithDefinition returns a verified owned artifact with a
// platform-observed definition state.
func InstalledArtifactWithDefinition(registration Registration, definition DefinitionState) Artifact {
	return Artifact{
		state:           RegistrationInstalled,
		definition:      definition,
		registration:    registration,
		hasRegistration: true,
	}
}

// NoArtifact returns the exact result for an absent owned artifact.
func NoArtifact() Artifact {
	return Artifact{state: RegistrationNotInstalled, definition: DefinitionUnknown}
}

// InvalidArtifact returns the exact result for a present but unverified artifact.
func InvalidArtifact() Artifact {
	return Artifact{state: RegistrationInvalid, definition: DefinitionUnknown}
}

// UnknownArtifact returns the exact result when artifact inspection is inconclusive.
func UnknownArtifact() Artifact {
	return Artifact{state: RegistrationUnknown, definition: DefinitionUnknown}
}

// State returns the exact native artifact state.
func (a Artifact) State() RegistrationState { return a.state }

// DefinitionState returns the adapter-observed stored definition state.
func (a Artifact) DefinitionState() DefinitionState { return a.definition }

// Registration returns the verified immutable registration when one exists.
func (a Artifact) Registration() (Registration, bool) { return a.registration, a.hasRegistration }

// WithRestoreSnapshot returns a copy carrying immutable adapter-owned rollback
// state. The snapshot is not part of status or ownership decisions.
func (a Artifact) WithRestoreSnapshot(snapshot any) Artifact {
	a.restoreSnapshot = &restoreSnapshot{value: snapshot}
	return a
}

// RestoreSnapshot returns opaque adapter-owned rollback state to the adapter
// that produced this observation.
func (a Artifact) RestoreSnapshot() (any, bool) {
	if a.restoreSnapshot == nil {
		return nil, false
	}
	return a.restoreSnapshot.value, true
}

// ManagerRegistration is an immutable inspection result for the object loaded
// by the native manager. It is separate from Artifact by design.
type ManagerRegistration struct {
	ownership       ManagerOwnership
	registration    Registration
	hasRegistration bool
	restoreSnapshot *restoreSnapshot
}

// OwnedManager returns a loaded native object verified as PowerContext-owned.
func OwnedManager(registration Registration) ManagerRegistration {
	return ManagerRegistration{
		ownership:       ManagerOwnershipOwned,
		registration:    registration,
		hasRegistration: true,
	}
}

// NotLoadedManager returns the exact result for no loaded manager object.
func NotLoadedManager() ManagerRegistration {
	return ManagerRegistration{ownership: ManagerOwnershipNotLoaded}
}

// ForeignManager returns the exact result for a foreign loaded manager object.
func ForeignManager() ManagerRegistration {
	return ManagerRegistration{ownership: ManagerOwnershipForeign}
}

// UnknownManager returns the exact result when loaded manager ownership is unknown.
func UnknownManager() ManagerRegistration {
	return ManagerRegistration{ownership: ManagerOwnershipUnknown}
}

// Ownership returns the independently observed native manager ownership.
func (r ManagerRegistration) Ownership() ManagerOwnership { return r.ownership }

// Registration returns the verified registration for an owned manager object.
func (r ManagerRegistration) Registration() (Registration, bool) {
	return r.registration, r.hasRegistration
}

// WithRestoreSnapshot returns a copy carrying immutable adapter-owned rollback
// state independently from the artifact observation.
func (r ManagerRegistration) WithRestoreSnapshot(snapshot any) ManagerRegistration {
	r.restoreSnapshot = &restoreSnapshot{value: snapshot}
	return r
}

// RestoreSnapshot returns opaque adapter-owned rollback state to the adapter
// that produced this observation.
func (r ManagerRegistration) RestoreSnapshot() (any, bool) {
	if r.restoreSnapshot == nil {
		return nil, false
	}
	return r.restoreSnapshot.value, true
}

// Adapter isolates every native, filesystem, process, and network operation.
// Implementations must inspect the artifact and loaded manager object through
// independent methods; Controller never infers ownership from an endpoint.
type Adapter interface {
	Support(context.Context) (Support, error)
	InspectArtifact(context.Context) (Artifact, error)
	InspectManager(context.Context) (ManagerRegistration, error)
	Probe(context.Context, string) (ProbeState, error)
	Write(context.Context, Registration) error
	Reload(context.Context) error
	Enable(context.Context) error
	Start(context.Context) error
	Stop(context.Context) error
	Disable(context.Context) error
	Remove(context.Context) error
	ManagerState(context.Context) (ManagerState, error)
}

// snapshotRestorer is an optional exact rollback boundary for adapters whose
// native manager object and stored artifact can exist independently.
type snapshotRestorer interface {
	Restore(context.Context, Artifact, ManagerRegistration) error
}

// OperationBoundary serializes one complete lifecycle mutation. It is injected
// so Controller remains pure Go and cannot select a host lock implementation.
type OperationBoundary interface {
	Run(context.Context, func(context.Context) error) error
}
