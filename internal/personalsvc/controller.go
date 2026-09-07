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

import (
	"context"
	"errors"
)

// Controller orchestrates one immutable personal-service registration through
// a supplied native Adapter. It does not read the environment, access storage,
// open a network connection, start a goroutine, or choose a native manager.
type Controller struct {
	adapter  Adapter
	boundary OperationBoundary
}

// NewController constructs a pure lifecycle controller from explicit native
// boundaries. Platform support belongs to the supplied Adapter.
func NewController(adapter Adapter, boundary OperationBoundary) (*Controller, error) {
	if adapter == nil || boundary == nil {
		return nil, newOperationError(ErrorInvalidController, StageNone, emptyStatus())
	}
	return &Controller{adapter: adapter, boundary: boundary}, nil
}

// Install validates and commits an owned registration. A native artifact
// transaction ends at enablement; later start or liveness failures preserve the
// committed registration and return RecoveryRetryInstall.
func (c *Controller) Install(ctx context.Context, desired Registration) (Status, error) {
	if err := desired.Definition().Validate(); err != nil {
		return emptyStatus(), newOperationError(ErrorInvalidRegistration, StageNone, emptyStatus())
	}
	support, err := c.adapter.Support(ctx)
	if err != nil {
		return emptyStatus(), newOperationError(ErrorOperation, StageSupport, emptyStatus())
	}
	if support != SupportSupported {
		return statusForUnsupported(), newOperationError(ErrorUnsupported, StageSupport, statusForUnsupported())
	}
	initialProbe, err := c.adapter.Probe(ctx, desired.Definition().Endpoint())
	if err != nil {
		return statusForSupportedUnknown(), newOperationError(ErrorOperation, StageProbe, statusForSupportedUnknown())
	}
	if initialProbe == ProbeConflict {
		status := newStatus(
			SupportSupported, RegistrationNotInstalled, DefinitionUnknown,
			ManagerOwnershipUnknown, ManagerUnknown, LivenessUnreachable, RecoveryResolveEndpoint,
		)
		return status, newOperationError(ErrorEndpointConflict, StageProbe, status)
	}

	started := false
	operationErr := c.boundary.Run(ctx, func(operationCtx context.Context) error {
		var installErr error
		started, installErr = c.installLocked(operationCtx, desired, initialProbe)
		return installErr
	})
	if operationErr != nil {
		if operation, found := errors.AsType[*OperationError](operationErr); found {
			if operation.Kind() != ErrorPostCommit {
				return operation.Status(), operation
			}
			return c.postCommitStatus(ctx, desired, operation.Stage())
		}
		return statusForSupportedUnknown(), newOperationError(ErrorOperation, StageLock, statusForSupportedUnknown())
	}

	status, statusErr := c.Status(ctx, desired)
	if statusErr != nil {
		return c.postCommitStatus(ctx, desired, StageProbe)
	}
	if started && status.Liveness() != LivenessLive {
		status = withRecovery(status, RecoveryRetryInstall)
		return status, newOperationError(ErrorPostCommit, StageProbe, status)
	}
	return status, nil
}

// Uninstall removes only a verified owned registration. Stop happens before
// disable, remove, and reload; a failure leaves remaining state in place.
func (c *Controller) Uninstall(ctx context.Context, desired Registration) (Status, error) {
	if err := desired.Definition().Validate(); err != nil {
		return emptyStatus(), newOperationError(ErrorInvalidRegistration, StageNone, emptyStatus())
	}
	support, err := c.adapter.Support(ctx)
	if err != nil {
		return emptyStatus(), newOperationError(ErrorOperation, StageSupport, emptyStatus())
	}
	if support != SupportSupported {
		return statusForUnsupported(), newOperationError(ErrorUnsupported, StageSupport, statusForUnsupported())
	}

	operationErr := c.boundary.Run(ctx, func(operationCtx context.Context) error {
		return c.uninstallLocked(operationCtx)
	})
	if operationErr != nil {
		if operation, found := errors.AsType[*OperationError](operationErr); found {
			if operation.Kind() == ErrorOperation {
				return c.uninstallFailureStatus(ctx, desired, operation.Stage())
			}
			return operation.Status(), operation
		}
		return statusForSupportedUnknown(), newOperationError(ErrorOperation, StageLock, statusForSupportedUnknown())
	}
	return c.Status(ctx, desired)
}

// Status independently inspects native support, artifact, loaded ownership,
// manager state, and the recorded endpoint. It never reuses mutation-time
// observations as status facts.
func (c *Controller) Status(ctx context.Context, desired Registration) (Status, error) {
	support, err := c.adapter.Support(ctx)
	if err != nil {
		return emptyStatus(), newOperationError(ErrorOperation, StageSupport, emptyStatus())
	}
	if support != SupportSupported {
		return statusForUnsupported(), nil
	}
	artifact, err := c.adapter.InspectArtifact(ctx)
	if err != nil {
		return statusForSupportedUnknown(), newOperationError(ErrorOperation, StageInspectArtifact, statusForSupportedUnknown())
	}
	managerRegistration, err := c.adapter.InspectManager(ctx)
	if err != nil {
		return statusForSupportedUnknown(), newOperationError(ErrorOperation, StageInspectManager, statusForSupportedUnknown())
	}
	if artifact.State() != RegistrationInstalled {
		return newStatus(
			SupportSupported, artifact.State(), DefinitionUnknown,
			managerRegistration.Ownership(), managerStateForOwnership(managerRegistration.Ownership()),
			LivenessUnknown, recoveryForAbsentArtifact(managerRegistration.Ownership()),
		), nil
	}
	stored, found := artifact.Registration()
	if !found || stored.Definition().Validate() != nil {
		return newStatus(
			SupportSupported, RegistrationInvalid, DefinitionUnknown,
			managerRegistration.Ownership(), ManagerUnknown, LivenessUnknown, RecoveryInspectManager,
		), nil
	}
	ownership := managerRegistration.Ownership()
	loadedDefinitionStale := false
	if ownership == ManagerOwnershipOwned {
		loaded, found := managerRegistration.Registration()
		if !found || loaded.Definition().Validate() != nil {
			ownership = ManagerOwnershipUnknown
		} else {
			loadedDefinitionStale = loaded != desired
		}
	}
	definition := artifact.DefinitionState()
	if definition != DefinitionMissingExecutable {
		if stored == desired && !loadedDefinitionStale {
			definition = DefinitionCurrent
		} else {
			definition = DefinitionStale
		}
	}

	manager := managerStateForOwnership(ownership)
	if ownership == ManagerOwnershipOwned {
		manager, err = c.adapter.ManagerState(ctx)
		if err != nil {
			return statusForSupportedUnknown(), newOperationError(ErrorOperation, StageInspectManager, statusForSupportedUnknown())
		}
	}
	probe, err := c.adapter.Probe(ctx, stored.Definition().Endpoint())
	if err != nil {
		return newStatus(
				SupportSupported, RegistrationInstalled, definition,
				ownership, manager, LivenessUnknown, RecoveryRetryInstall,
			), newOperationError(
				ErrorOperation,
				StageProbe,
				newStatus(
					SupportSupported, RegistrationInstalled, definition,
					ownership, manager, LivenessUnknown, RecoveryRetryInstall,
				),
			)
	}
	liveness := livenessForProbe(probe)
	return newStatus(
		SupportSupported, RegistrationInstalled, definition,
		ownership, manager, liveness,
		recoveryForInstalled(definition, ownership, manager, probe),
	), nil
}

func (c *Controller) installLocked(ctx context.Context, desired Registration, initialProbe ProbeState) (bool, error) {
	artifact, err := c.adapter.InspectArtifact(ctx)
	if err != nil {
		return false, newOperationError(ErrorOperation, StageInspectArtifact, statusForSupportedUnknown())
	}
	if artifact.State() == RegistrationInvalid || artifact.State() == RegistrationUnknown {
		return false, newOperationError(ErrorRegistrationConflict, StageInspectArtifact, statusForSupportedUnknown())
	}
	if artifact.State() == RegistrationInstalled {
		stored, found := artifact.Registration()
		if !found || stored.Definition().Validate() != nil {
			return false, newOperationError(ErrorRegistrationConflict, StageInspectArtifact, statusForSupportedUnknown())
		}
	}
	managerRegistration, err := c.adapter.InspectManager(ctx)
	if err != nil {
		return false, newOperationError(ErrorOperation, StageInspectManager, statusForSupportedUnknown())
	}
	if managerRegistration.Ownership() == ManagerOwnershipForeign || managerRegistration.Ownership() == ManagerOwnershipUnknown {
		return false, newOperationError(ErrorManagerConflict, StageInspectManager, statusForSupportedUnknown())
	}
	loadedChanged := false
	if managerRegistration.Ownership() == ManagerOwnershipOwned {
		loaded, found := managerRegistration.Registration()
		if !found || loaded.Definition().Validate() != nil {
			return false, newOperationError(ErrorManagerConflict, StageInspectManager, statusForSupportedUnknown())
		}
		loadedChanged = loaded != desired
	}

	changed := artifact.State() != RegistrationInstalled || loadedChanged
	if !changed {
		stored, _ := artifact.Registration()
		changed = stored != desired
	}
	if changed {
		if err := c.commit(ctx, desired, artifact); err != nil {
			return false, err
		}
	} else if err := c.adapter.Enable(ctx); err != nil {
		return false, newOperationError(ErrorOperation, StageEnable, statusForSupportedUnknown())
	}
	if initialProbe == ProbeLive {
		return false, nil
	}
	if err := c.adapter.Start(ctx); err != nil {
		return false, newOperationError(ErrorPostCommit, StageStart, statusForSupportedUnknown())
	}
	return true, nil
}

func (c *Controller) commit(ctx context.Context, desired Registration, previous Artifact) error {
	if err := c.adapter.Write(ctx, desired); err != nil {
		c.restore(ctx, previous)
		return newOperationError(ErrorOperation, StageWrite, statusForSupportedUnknown())
	}
	if err := c.adapter.Reload(ctx); err != nil {
		c.restore(ctx, previous)
		return newOperationError(ErrorOperation, StageReload, statusForSupportedUnknown())
	}
	if err := c.adapter.Enable(ctx); err != nil {
		c.restore(ctx, previous)
		return newOperationError(ErrorOperation, StageEnable, statusForSupportedUnknown())
	}
	return nil
}

func (c *Controller) restore(ctx context.Context, previous Artifact) {
	_ = c.adapter.Disable(ctx)
	if previous.State() == RegistrationInstalled {
		registration, found := previous.Registration()
		if found {
			_ = c.adapter.Write(ctx, registration)
			_ = c.adapter.Reload(ctx)
			_ = c.adapter.Enable(ctx)
			return
		}
	}
	_ = c.adapter.Remove(ctx)
	_ = c.adapter.Reload(ctx)
}

func (c *Controller) uninstallLocked(ctx context.Context) error {
	artifact, err := c.adapter.InspectArtifact(ctx)
	if err != nil {
		return newOperationError(ErrorOperation, StageInspectArtifact, statusForSupportedUnknown())
	}
	if artifact.State() == RegistrationInvalid || artifact.State() == RegistrationUnknown {
		return newOperationError(ErrorRegistrationConflict, StageInspectArtifact, statusForSupportedUnknown())
	}
	if artifact.State() == RegistrationInstalled {
		stored, found := artifact.Registration()
		if !found || stored.Definition().Validate() != nil {
			return newOperationError(ErrorRegistrationConflict, StageInspectArtifact, statusForSupportedUnknown())
		}
	}
	managerRegistration, err := c.adapter.InspectManager(ctx)
	if err != nil {
		return newOperationError(ErrorOperation, StageInspectManager, statusForSupportedUnknown())
	}
	if managerRegistration.Ownership() == ManagerOwnershipForeign || managerRegistration.Ownership() == ManagerOwnershipUnknown {
		return newOperationError(ErrorManagerConflict, StageInspectManager, statusForSupportedUnknown())
	}
	if managerRegistration.Ownership() == ManagerOwnershipOwned {
		loaded, found := managerRegistration.Registration()
		if !found || loaded.Definition().Validate() != nil {
			return newOperationError(ErrorManagerConflict, StageInspectManager, statusForSupportedUnknown())
		}
	}
	if artifact.State() == RegistrationNotInstalled && managerRegistration.Ownership() == ManagerOwnershipNotLoaded {
		return nil
	}
	if err := c.adapter.Stop(ctx); err != nil {
		return newOperationError(ErrorOperation, StageStop, statusForSupportedUnknown())
	}
	if err := c.adapter.Disable(ctx); err != nil {
		return newOperationError(ErrorOperation, StageDisable, statusForSupportedUnknown())
	}
	if artifact.State() == RegistrationInstalled {
		if err := c.adapter.Remove(ctx); err != nil {
			return newOperationError(ErrorOperation, StageRemove, statusForSupportedUnknown())
		}
	}
	if err := c.adapter.Reload(ctx); err != nil {
		return newOperationError(ErrorOperation, StageReload, statusForSupportedUnknown())
	}
	return nil
}

func (c *Controller) postCommitStatus(ctx context.Context, desired Registration, stage OperationStage) (Status, error) {
	status, err := c.Status(ctx, desired)
	if err != nil {
		status = statusForSupportedUnknown()
	}
	status = withRecovery(status, RecoveryRetryInstall)
	return status, newOperationError(ErrorPostCommit, stage, status)
}

func (c *Controller) uninstallFailureStatus(ctx context.Context, desired Registration, stage OperationStage) (Status, error) {
	status, err := c.Status(ctx, desired)
	if err != nil {
		status = statusForSupportedUnknown()
	}
	status = withRecovery(status, RecoveryRetryUninstall)
	return status, newOperationError(ErrorOperation, stage, status)
}

func emptyStatus() Status {
	return newStatus(
		SupportUnsupported, RegistrationUnknown, DefinitionUnknown,
		ManagerOwnershipUnknown, ManagerUnknown, LivenessUnknown, RecoveryNone,
	)
}

func statusForUnsupported() Status {
	return newStatus(
		SupportUnsupported, RegistrationUnknown, DefinitionUnknown,
		ManagerOwnershipUnknown, ManagerUnknown, LivenessUnknown, RecoveryNone,
	)
}

func statusForSupportedUnknown() Status {
	return newStatus(
		SupportSupported, RegistrationUnknown, DefinitionUnknown,
		ManagerOwnershipUnknown, ManagerUnknown, LivenessUnknown, RecoveryNone,
	)
}

func withRecovery(status Status, recovery Recovery) Status {
	return newStatus(
		status.Support(), status.Registration(), status.Definition(), status.ManagerOwnership(),
		status.Manager(), status.Liveness(), recovery,
	)
}

func managerStateForOwnership(ownership ManagerOwnership) ManagerState {
	if ownership == ManagerOwnershipNotLoaded {
		return ManagerInactive
	}
	return ManagerUnknown
}

func livenessForProbe(probe ProbeState) Liveness {
	if probe == ProbeLive {
		return LivenessLive
	}
	return LivenessUnreachable
}

func recoveryForAbsentArtifact(ownership ManagerOwnership) Recovery {
	if ownership == ManagerOwnershipForeign || ownership == ManagerOwnershipUnknown {
		return RecoveryInspectManager
	}
	return RecoveryNone
}

func recoveryForInstalled(
	definition DefinitionState,
	ownership ManagerOwnership,
	manager ManagerState,
	probe ProbeState,
) Recovery {
	if definition == DefinitionStale || definition == DefinitionMissingExecutable {
		return RecoveryRetryInstall
	}
	if ownership == ManagerOwnershipForeign || ownership == ManagerOwnershipUnknown {
		return RecoveryInspectManager
	}
	if probe == ProbeConflict {
		return RecoveryResolveEndpoint
	}
	if manager == ManagerInactive || manager == ManagerFailed || probe == ProbeUnreachable {
		return RecoveryRetryInstall
	}
	return RecoveryNone
}
