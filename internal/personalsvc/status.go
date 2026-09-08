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

// Support describes whether a qualified native personal-service adapter is
// available on the current host.
type Support string

const (
	// SupportSupported means a qualified native adapter is available.
	SupportSupported Support = "supported"
	// SupportUnsupported means no qualified native adapter is available.
	SupportUnsupported Support = "unsupported"
)

// RegistrationState describes the exact native personal-service artifact.
type RegistrationState string

const (
	// RegistrationInstalled means an owned artifact was inspected.
	RegistrationInstalled RegistrationState = "installed"
	// RegistrationNotInstalled means no artifact exists at the owned location.
	RegistrationNotInstalled RegistrationState = "not_installed"
	// RegistrationInvalid means the artifact cannot be verified as owned.
	RegistrationInvalid RegistrationState = "invalid"
	// RegistrationUnknown means the adapter could not determine artifact state.
	RegistrationUnknown RegistrationState = "unknown"
)

// DefinitionState compares an installed definition with the desired
// distribution definition without exposing either definition's contents.
type DefinitionState string

const (
	// DefinitionCurrent means the installed definition equals the desired one.
	DefinitionCurrent DefinitionState = "current"
	// DefinitionStale means the installed definition differs from the desired one.
	DefinitionStale DefinitionState = "stale"
	// DefinitionMissingExecutable means the adapter found a missing executable.
	DefinitionMissingExecutable DefinitionState = "missing_executable"
	// DefinitionUnknown means no verified installed definition is available.
	DefinitionUnknown DefinitionState = "unknown"
)

// ManagerOwnership describes the loaded native manager object independently
// from the on-disk artifact.
type ManagerOwnership string

const (
	// ManagerOwnershipOwned means the loaded object was independently verified.
	ManagerOwnershipOwned ManagerOwnership = "owned"
	// ManagerOwnershipNotLoaded means the native manager has no loaded object.
	ManagerOwnershipNotLoaded ManagerOwnership = "not_loaded"
	// ManagerOwnershipForeign means another owner controls the loaded object.
	ManagerOwnershipForeign ManagerOwnership = "foreign"
	// ManagerOwnershipUnknown means ownership could not be verified.
	ManagerOwnershipUnknown ManagerOwnership = "unknown"
)

// ManagerState describes the native manager state after ownership verification.
type ManagerState string

const (
	// ManagerActive means the verified manager object is active.
	ManagerActive ManagerState = "active"
	// ManagerInactive means no verified manager object is active.
	ManagerInactive ManagerState = "inactive"
	// ManagerFailed means the verified manager object reports a failure.
	ManagerFailed ManagerState = "failed"
	// ManagerUnknown means the manager state could not be determined safely.
	ManagerUnknown ManagerState = "unknown"
)

// Liveness describes the recorded loopback Server endpoint.
type Liveness string

const (
	// LivenessLive means the endpoint returned the PowerContext liveness contract.
	LivenessLive Liveness = "live"
	// LivenessUnreachable means the endpoint did not return a valid liveness response.
	LivenessUnreachable Liveness = "unreachable"
	// LivenessUnknown means no verified endpoint is available to probe.
	LivenessUnknown Liveness = "unknown"
)

// Recovery identifies a fixed, safe next operation. It never contains a
// definition field, native path, endpoint, or adapter diagnostic.
type Recovery string

const (
	// RecoveryNone means no recovery action is required.
	RecoveryNone Recovery = ""
	// RecoveryRetryInstall means a committed registration needs an install retry.
	RecoveryRetryInstall Recovery = "retry_install"
	// RecoveryRetryUninstall means an uninstall should be retried safely.
	RecoveryRetryUninstall Recovery = "retry_uninstall"
	// RecoveryInspectManager means native manager ownership needs manual inspection.
	RecoveryInspectManager Recovery = "inspect_manager"
	// RecoveryResolveEndpoint means a conflicting endpoint needs resolution.
	RecoveryResolveEndpoint Recovery = "resolve_endpoint"
)

// Status is an immutable observation of one personal-service lifecycle.
// It intentionally exposes only closed state values and a fixed recovery
// action, never the underlying Definition or native adapter diagnostics.
type Status struct {
	support          Support
	registration     RegistrationState
	definition       DefinitionState
	managerOwnership ManagerOwnership
	manager          ManagerState
	liveness         Liveness
	recovery         Recovery
}

func newStatus(
	support Support,
	registration RegistrationState,
	definition DefinitionState,
	managerOwnership ManagerOwnership,
	manager ManagerState,
	liveness Liveness,
	recovery Recovery,
) Status {
	return Status{
		support:          support,
		registration:     registration,
		definition:       definition,
		managerOwnership: managerOwnership,
		manager:          manager,
		liveness:         liveness,
		recovery:         recovery,
	}
}

// Support returns the native adapter availability state.
func (s Status) Support() Support { return s.support }

// Registration returns the exact artifact state.
func (s Status) Registration() RegistrationState { return s.registration }

// Definition returns the installed-definition comparison state.
func (s Status) Definition() DefinitionState { return s.definition }

// ManagerOwnership returns the independently inspected manager ownership.
func (s Status) ManagerOwnership() ManagerOwnership { return s.managerOwnership }

// Manager returns the native manager state after ownership verification.
func (s Status) Manager() ManagerState { return s.manager }

// Liveness returns the recorded endpoint liveness state.
func (s Status) Liveness() Liveness { return s.liveness }

// Recovery returns a fixed next action for a degraded lifecycle state.
func (s Status) Recovery() Recovery { return s.recovery }

// Healthy reports whether every required personal-service state is healthy.
func (s Status) Healthy() bool {
	return s.support == SupportSupported &&
		s.registration == RegistrationInstalled &&
		s.definition == DefinitionCurrent &&
		s.managerOwnership == ManagerOwnershipOwned &&
		s.manager == ManagerActive &&
		s.liveness == LivenessLive
}

// ErrorKind identifies a redacted controller failure class.
type ErrorKind string

const (
	// ErrorInvalidController means a controller dependency was not supplied.
	ErrorInvalidController ErrorKind = "invalid_controller"
	// ErrorInvalidRegistration means the caller supplied an invalid registration.
	ErrorInvalidRegistration ErrorKind = "invalid_registration"
	// ErrorUnsupported means no qualified adapter is available.
	ErrorUnsupported ErrorKind = "unsupported"
	// ErrorEndpointConflict means another listener owns the intended endpoint.
	ErrorEndpointConflict ErrorKind = "endpoint_conflict"
	// ErrorRegistrationConflict means the artifact cannot be safely modified.
	ErrorRegistrationConflict ErrorKind = "registration_conflict"
	// ErrorManagerConflict means loaded manager ownership cannot be modified safely.
	ErrorManagerConflict ErrorKind = "manager_conflict"
	// ErrorOperation means a pre-commit or uninstall adapter operation failed.
	ErrorOperation ErrorKind = "operation"
	// ErrorPostCommit means a registration was committed but requires recovery.
	ErrorPostCommit ErrorKind = "post_commit"
)

// OperationStage identifies a safe lifecycle transition. Its closed values
// are suitable for error classification because none contains caller input.
type OperationStage string

const (
	// StageNone means no individual adapter stage applies.
	StageNone OperationStage = ""
	// StageSupport inspects adapter availability.
	StageSupport OperationStage = "support"
	// StageProbe probes the recorded loopback endpoint.
	StageProbe OperationStage = "probe"
	// StageLock enters the injected serialized operation boundary.
	StageLock OperationStage = "lock"
	// StageInspectArtifact inspects the native registration artifact.
	StageInspectArtifact OperationStage = "inspect_artifact"
	// StageInspectManager inspects native manager ownership.
	StageInspectManager OperationStage = "inspect_manager"
	// StageWrite writes an owned registration artifact.
	StageWrite OperationStage = "write"
	// StageReload reloads native-manager configuration.
	StageReload OperationStage = "reload"
	// StageEnable enables the owned registration.
	StageEnable OperationStage = "enable"
	// StageStart starts the registered native service.
	StageStart OperationStage = "start"
	// StageStop stops the verified native service.
	StageStop OperationStage = "stop"
	// StageDisable disables the owned registration.
	StageDisable OperationStage = "disable"
	// StageRemove removes the owned artifact.
	StageRemove OperationStage = "remove"
)

// OperationError is a typed, redacted lifecycle failure. It intentionally
// drops adapter errors because they can carry paths, endpoints, or credentials.
type OperationError struct {
	kind   ErrorKind
	stage  OperationStage
	status Status
	cause  error
}

func newOperationError(kind ErrorKind, stage OperationStage, status Status) *OperationError {
	return &OperationError{kind: kind, stage: stage, status: status}
}

func newOperationErrorWithCause(kind ErrorKind, stage OperationStage, status Status, cause error) *OperationError {
	return &OperationError{kind: kind, stage: stage, status: status, cause: cause}
}

// Error returns a stable, redacted failure description.
func (e *OperationError) Error() string {
	switch e.kind {
	case ErrorInvalidController:
		return "invalid personal service controller"
	case ErrorInvalidRegistration:
		return "invalid personal service registration"
	case ErrorUnsupported:
		return "personal service native adapter is unsupported"
	case ErrorEndpointConflict:
		return "personal service endpoint is already occupied"
	case ErrorRegistrationConflict:
		return "personal service registration cannot be safely modified"
	case ErrorManagerConflict:
		return "personal service native manager ownership cannot be safely modified"
	case ErrorPostCommit:
		return "personal service registration is installed but needs recovery"
	default:
		return "personal service operation failed during " + string(e.stage)
	}
}

// Unwrap retains only a caller-context cancellation classification. Adapter
// and native errors are never attached to an OperationError.
func (e *OperationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Kind returns the typed failure class.
func (e *OperationError) Kind() ErrorKind { return e.kind }

// Stage returns the safe lifecycle transition that failed.
func (e *OperationError) Stage() OperationStage { return e.stage }

// Status returns a content-free lifecycle observation captured for this error.
func (e *OperationError) Status() Status { return e.status }
