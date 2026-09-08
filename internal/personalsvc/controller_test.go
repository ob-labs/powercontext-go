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

package personalsvc_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

func TestControllerInstallIsIdempotentAndReportsIndependentStatus(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	adapter := newFakeAdapter()
	controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
	if err != nil {
		t.Fatal(err)
	}

	first, installErr := controller.Install(t.Context(), desired)
	if installErr != nil {
		t.Fatal(installErr)
	}
	if !first.Healthy() || first.Support() != personalsvc.SupportSupported ||
		first.Registration() != personalsvc.RegistrationInstalled ||
		first.Definition() != personalsvc.DefinitionCurrent ||
		first.ManagerOwnership() != personalsvc.ManagerOwnershipOwned ||
		first.Manager() != personalsvc.ManagerActive ||
		first.Liveness() != personalsvc.LivenessLive ||
		first.Recovery() != personalsvc.RecoveryNone {
		t.Fatalf("first Status = %#v", first)
	}
	assertEvents(t, adapter.events, []string{
		"support", "probe", "lock", "inspect_artifact", "inspect_manager",
		"write", "reload", "enable", "start", "unlock",
		"support", "inspect_artifact", "inspect_manager", "manager_state", "probe",
	})

	adapter.events = nil
	second, installErr := controller.Install(t.Context(), desired)
	if installErr != nil {
		t.Fatal(installErr)
	}
	if !second.Healthy() {
		t.Fatalf("second Status = %#v", second)
	}
	assertEvents(t, adapter.events, []string{
		"support", "probe", "lock", "inspect_artifact", "inspect_manager", "enable", "unlock",
		"support", "inspect_artifact", "inspect_manager", "manager_state", "probe",
	})
}

func TestControllerInstallPrecommitFailureRestoresPreviousOwnedRegistration(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	previous := testRegistration(t, "1.2.2")

	for _, test := range []struct {
		stage string
		want  []string
	}{
		{
			stage: "write",
			want: []string{
				"support", "probe", "lock", "inspect_artifact", "inspect_manager", "write",
				"disable", "write", "reload", "enable", "unlock",
			},
		},
		{
			stage: "reload",
			want: []string{
				"support", "probe", "lock", "inspect_artifact", "inspect_manager", "write", "reload",
				"disable", "write", "reload", "enable", "unlock",
			},
		},
		{
			stage: "enable",
			want: []string{
				"support", "probe", "lock", "inspect_artifact", "inspect_manager", "write", "reload", "enable",
				"disable", "write", "reload", "enable", "unlock",
			},
		},
	} {
		t.Run(test.stage, func(t *testing.T) {
			adapter := newFakeAdapter()
			adapter.artifact = personalsvc.InstalledArtifact(previous)
			adapter.failOnce(test.stage)
			controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
			if err != nil {
				t.Fatal(err)
			}

			_, installErr := controller.Install(t.Context(), desired)
			_ = assertOperationError(t, installErr, personalsvc.ErrorOperation, personalsvc.OperationStage(test.stage))
			assertRedacted(t, installErr)
			assertEvents(t, adapter.events, test.want)
			stored, found := adapter.artifact.Registration()
			if !found || stored != previous {
				t.Fatalf("restored registration = %#v, %t", stored, found)
			}
		})
	}
}

func TestControllerInstallPostCommitFailurePreservesRegistrationWithRetryRecovery(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	adapter := newFakeAdapter()
	adapter.failOnce("start")
	controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
	if err != nil {
		t.Fatal(err)
	}

	_, installErr := controller.Install(t.Context(), desired)
	operation := assertOperationError(t, installErr, personalsvc.ErrorPostCommit, personalsvc.StageStart)
	assertRedacted(t, installErr)
	if operation.Status().Registration() != personalsvc.RegistrationInstalled ||
		operation.Status().Recovery() != personalsvc.RecoveryRetryInstall {
		t.Fatalf("post-commit Status = %#v", operation.Status())
	}
	stored, found := adapter.artifact.Registration()
	if !found || stored != desired {
		t.Fatalf("preserved registration = %#v, %t", stored, found)
	}
	assertEvents(t, adapter.events, []string{
		"support", "probe", "lock", "inspect_artifact", "inspect_manager",
		"write", "reload", "enable", "start", "unlock",
		"support", "inspect_artifact", "inspect_manager", "probe",
	})
}

func TestControllerInstallPreservesRegistrationWhenStartedServiceIsStillUnreachable(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	adapter := newFakeAdapter()
	adapter.leaveEndpointUnreachableAfterStart = true
	controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
	if err != nil {
		t.Fatal(err)
	}

	_, installErr := controller.Install(t.Context(), desired)
	operation := assertOperationError(t, installErr, personalsvc.ErrorPostCommit, personalsvc.StageProbe)
	if operation.Status().Registration() != personalsvc.RegistrationInstalled ||
		operation.Status().Manager() != personalsvc.ManagerActive ||
		operation.Status().Liveness() != personalsvc.LivenessUnreachable ||
		operation.Status().Recovery() != personalsvc.RecoveryRetryInstall {
		t.Fatalf("unreachable post-start Status = %#v", operation.Status())
	}
	stored, found := adapter.artifact.Registration()
	if !found || stored != desired {
		t.Fatalf("preserved registration = %#v, %t", stored, found)
	}
	assertEvents(t, adapter.events, []string{
		"support", "probe", "lock", "inspect_artifact", "inspect_manager",
		"write", "reload", "enable", "start", "unlock",
		"support", "inspect_artifact", "inspect_manager", "manager_state", "probe",
	})
}

func TestControllerInstallRefusesUnverifiedManagerBeforeArtifactMutation(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	for _, test := range []struct {
		name    string
		manager personalsvc.ManagerRegistration
	}{
		{name: "foreign", manager: personalsvc.ForeignManager()},
		{name: "unknown", manager: personalsvc.UnknownManager()},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newFakeAdapter()
			adapter.managerRegistration = test.manager
			controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
			if err != nil {
				t.Fatal(err)
			}

			_, installErr := controller.Install(t.Context(), desired)
			_ = assertOperationError(t, installErr, personalsvc.ErrorManagerConflict, personalsvc.StageInspectManager)
			assertRedacted(t, installErr)
			assertEvents(t, adapter.events, []string{
				"support", "probe", "lock", "inspect_artifact", "inspect_manager", "unlock",
			})
			if adapter.artifact.State() != personalsvc.RegistrationNotInstalled {
				t.Fatalf("unverified manager mutation installed artifact: %#v", adapter.artifact)
			}
		})
	}
}

func TestControllerInstallLeavesExistingLiveEndpointUntouched(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	adapter := newFakeAdapter()
	adapter.probe = personalsvc.ProbeLive
	controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
	if err != nil {
		t.Fatal(err)
	}

	status, installErr := controller.Install(t.Context(), desired)
	if installErr != nil {
		t.Fatal(installErr)
	}
	if status.Liveness() != personalsvc.LivenessLive || status.ManagerOwnership() != personalsvc.ManagerOwnershipNotLoaded ||
		status.Manager() != personalsvc.ManagerInactive {
		t.Fatalf("live foreground Status = %#v", status)
	}
	assertEvents(t, adapter.events, []string{
		"support", "probe", "lock", "inspect_artifact", "inspect_manager",
		"write", "reload", "enable", "unlock",
		"support", "inspect_artifact", "inspect_manager", "probe",
	})
}

func TestControllerReconcilesStaleLoadedManagerDefinition(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	previous := testRegistration(t, "1.2.2")
	adapter := newFakeAdapter()
	adapter.artifact = personalsvc.InstalledArtifact(desired)
	adapter.managerRegistration = personalsvc.OwnedManager(previous)
	controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
	if err != nil {
		t.Fatal(err)
	}

	before, statusErr := controller.Status(t.Context(), desired)
	if statusErr != nil || before.Definition() != personalsvc.DefinitionStale {
		t.Fatalf("pre-install Status = %#v, %v", before, statusErr)
	}
	adapter.events = nil
	status, installErr := controller.Install(t.Context(), desired)
	if installErr != nil || !status.Healthy() {
		t.Fatalf("Install() = (%#v, %v)", status, installErr)
	}
	assertEvents(t, adapter.events, []string{
		"support", "probe", "lock", "inspect_artifact", "inspect_manager",
		"write", "reload", "enable", "start", "unlock",
		"support", "inspect_artifact", "inspect_manager", "manager_state", "probe",
	})
}

func TestControllerInstallRejectsEndpointConflictBeforeArtifactMutation(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	adapter := newFakeAdapter()
	adapter.probe = personalsvc.ProbeConflict
	controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
	if err != nil {
		t.Fatal(err)
	}

	_, installErr := controller.Install(t.Context(), desired)
	_ = assertOperationError(t, installErr, personalsvc.ErrorEndpointConflict, personalsvc.StageProbe)
	assertRedacted(t, installErr)
	assertEvents(t, adapter.events, []string{"support", "probe"})
}

func TestControllerUninstallDoesNotRemoveRegistrationAfterStopFailure(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	adapter := newFakeAdapter()
	adapter.artifact = personalsvc.InstalledArtifact(desired)
	adapter.managerRegistration = personalsvc.OwnedManager(desired)
	adapter.manager = personalsvc.ManagerActive
	adapter.failOnce("stop")
	controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
	if err != nil {
		t.Fatal(err)
	}

	_, uninstallErr := controller.Uninstall(t.Context(), desired)
	operation := assertOperationError(t, uninstallErr, personalsvc.ErrorOperation, personalsvc.StageStop)
	if operation.Status().Registration() != personalsvc.RegistrationInstalled ||
		operation.Status().Recovery() != personalsvc.RecoveryRetryUninstall {
		t.Fatalf("stop failure Status = %#v", operation.Status())
	}
	if adapter.artifact.State() != personalsvc.RegistrationInstalled {
		t.Fatal("stop failure removed the registration")
	}
	assertEvents(t, adapter.events, []string{
		"support", "lock", "inspect_artifact", "inspect_manager", "stop", "unlock",
		"support", "inspect_artifact", "inspect_manager", "manager_state", "probe",
	})
}

func TestControllerUninstallRefusesUnverifiedArtifactOrManagerBeforeStop(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	for _, test := range []struct {
		name       string
		configure  func(*fakeAdapter)
		kind       personalsvc.ErrorKind
		stage      personalsvc.OperationStage
		wantEvents []string
	}{
		{
			name: "artifact",
			configure: func(adapter *fakeAdapter) {
				adapter.artifact = personalsvc.InstalledArtifact(personalsvc.Registration{})
			},
			kind:       personalsvc.ErrorRegistrationConflict,
			stage:      personalsvc.StageInspectArtifact,
			wantEvents: []string{"support", "lock", "inspect_artifact", "unlock"},
		},
		{
			name: "manager",
			configure: func(adapter *fakeAdapter) {
				adapter.artifact = personalsvc.InstalledArtifact(desired)
				adapter.managerRegistration = personalsvc.OwnedManager(personalsvc.Registration{})
			},
			kind:       personalsvc.ErrorManagerConflict,
			stage:      personalsvc.StageInspectManager,
			wantEvents: []string{"support", "lock", "inspect_artifact", "inspect_manager", "unlock"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := newFakeAdapter()
			test.configure(adapter)
			controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
			if err != nil {
				t.Fatal(err)
			}

			_, uninstallErr := controller.Uninstall(t.Context(), desired)
			_ = assertOperationError(t, uninstallErr, test.kind, test.stage)
			assertRedacted(t, uninstallErr)
			assertEvents(t, adapter.events, test.wantEvents)
		})
	}
}

func TestControllerUninstallStopsDisablesRemovesAndReloadsOwnedRegistration(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	adapter := newFakeAdapter()
	adapter.artifact = personalsvc.InstalledArtifact(desired)
	adapter.managerRegistration = personalsvc.OwnedManager(desired)
	adapter.manager = personalsvc.ManagerActive
	adapter.probe = personalsvc.ProbeLive
	controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
	if err != nil {
		t.Fatal(err)
	}

	status, uninstallErr := controller.Uninstall(t.Context(), desired)
	if uninstallErr != nil {
		t.Fatal(uninstallErr)
	}
	if status.Registration() != personalsvc.RegistrationNotInstalled ||
		status.ManagerOwnership() != personalsvc.ManagerOwnershipNotLoaded {
		t.Fatalf("uninstall Status = %#v", status)
	}
	assertEvents(t, adapter.events, []string{
		"support", "lock", "inspect_artifact", "inspect_manager", "stop", "disable", "remove", "reload", "unlock",
		"support", "inspect_artifact", "inspect_manager",
	})
}

func TestControllerUninstallIsIdempotentWhenNothingIsInstalledOrLoaded(t *testing.T) {
	desired := testRegistration(t, "1.2.3")
	adapter := newFakeAdapter()
	controller, err := personalsvc.NewController(adapter, fakeBoundary{events: &adapter.events})
	if err != nil {
		t.Fatal(err)
	}

	status, uninstallErr := controller.Uninstall(t.Context(), desired)
	if uninstallErr != nil {
		t.Fatal(uninstallErr)
	}
	if status.Registration() != personalsvc.RegistrationNotInstalled ||
		status.ManagerOwnership() != personalsvc.ManagerOwnershipNotLoaded ||
		status.Recovery() != personalsvc.RecoveryNone {
		t.Fatalf("idempotent uninstall Status = %#v", status)
	}
	assertEvents(t, adapter.events, []string{
		"support", "lock", "inspect_artifact", "inspect_manager", "unlock",
		"support", "inspect_artifact", "inspect_manager",
	})
}

func testRegistration(t *testing.T, packageVersion string) personalsvc.Registration {
	t.Helper()
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    packageVersion,
		Binary:            `C:\\Program Files\\PowerContext\\powercontext.exe`,
		Endpoint:          "http://127.0.0.1:8123",
		DataDir:           `C:\\Users\\person\\AppData\\Local\\PowerContext`,
		EnvFile:           `C:\Users\person\AppData\Local\PowerContext\server.env`,
	})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := personalsvc.NewRegistration(definition)
	if err != nil {
		t.Fatal(err)
	}
	return registration
}

func assertOperationError(
	t *testing.T, err error, wantKind personalsvc.ErrorKind, wantStage personalsvc.OperationStage,
) *personalsvc.OperationError {
	t.Helper()
	operation, found := errors.AsType[*personalsvc.OperationError](err)
	if !found {
		t.Fatalf("error type = %T %v, want *OperationError", err, err)
	}
	if operation.Kind() != wantKind || operation.Stage() != wantStage {
		t.Fatalf("operation error = (%s, %s), want (%s, %s)", operation.Kind(), operation.Stage(), wantKind, wantStage)
	}
	return operation
}

func assertRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a redacted error")
	}
	for _, forbidden := range []string{"127.0.0.1", "8123", "Program Files", "secret-adapter-detail"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked protected definition value %q: %v", forbidden, err)
		}
	}
}

func assertEvents(t *testing.T, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

type fakeBoundary struct{ events *[]string }

func (b fakeBoundary) Run(ctx context.Context, operation func(context.Context) error) error {
	*b.events = append(*b.events, "lock")
	err := operation(ctx)
	*b.events = append(*b.events, "unlock")
	return err
}

type fakeAdapter struct {
	artifact                           personalsvc.Artifact
	managerRegistration                personalsvc.ManagerRegistration
	manager                            personalsvc.ManagerState
	probe                              personalsvc.ProbeState
	leaveEndpointUnreachableAfterStart bool
	events                             []string
	failures                           map[string]int
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{
		artifact:            personalsvc.NoArtifact(),
		managerRegistration: personalsvc.NotLoadedManager(),
		manager:             personalsvc.ManagerInactive,
		probe:               personalsvc.ProbeUnreachable,
		failures:            make(map[string]int),
	}
}

func (a *fakeAdapter) failOnce(stage string) { a.failures[stage]++ }

func (a *fakeAdapter) Support(context.Context) (personalsvc.Support, error) {
	a.events = append(a.events, "support")
	return personalsvc.SupportSupported, nil
}

func (a *fakeAdapter) InspectArtifact(context.Context) (personalsvc.Artifact, error) {
	a.events = append(a.events, "inspect_artifact")
	return a.artifact, a.failure("inspect_artifact")
}

func (a *fakeAdapter) InspectManager(context.Context) (personalsvc.ManagerRegistration, error) {
	a.events = append(a.events, "inspect_manager")
	return a.managerRegistration, a.failure("inspect_manager")
}

func (a *fakeAdapter) Probe(context.Context, string) (personalsvc.ProbeState, error) {
	a.events = append(a.events, "probe")
	return a.probe, a.failure("probe")
}

func (a *fakeAdapter) Write(_ context.Context, registration personalsvc.Registration) error {
	a.events = append(a.events, "write")
	a.artifact = personalsvc.InstalledArtifact(registration)
	return a.failure("write")
}

func (a *fakeAdapter) Reload(context.Context) error {
	a.events = append(a.events, "reload")
	return a.failure("reload")
}

func (a *fakeAdapter) Enable(context.Context) error {
	a.events = append(a.events, "enable")
	return a.failure("enable")
}

func (a *fakeAdapter) Start(context.Context) error {
	a.events = append(a.events, "start")
	if err := a.failure("start"); err != nil {
		return err
	}
	registration, found := a.artifact.Registration()
	if !found {
		return errors.New("secret-adapter-detail")
	}
	a.managerRegistration = personalsvc.OwnedManager(registration)
	a.manager = personalsvc.ManagerActive
	if !a.leaveEndpointUnreachableAfterStart {
		a.probe = personalsvc.ProbeLive
	}
	return nil
}

func (a *fakeAdapter) Stop(context.Context) error {
	a.events = append(a.events, "stop")
	if err := a.failure("stop"); err != nil {
		return err
	}
	a.managerRegistration = personalsvc.NotLoadedManager()
	a.manager = personalsvc.ManagerInactive
	a.probe = personalsvc.ProbeUnreachable
	return nil
}

func (a *fakeAdapter) Disable(context.Context) error {
	a.events = append(a.events, "disable")
	return a.failure("disable")
}

func (a *fakeAdapter) Remove(context.Context) error {
	a.events = append(a.events, "remove")
	if err := a.failure("remove"); err != nil {
		return err
	}
	a.artifact = personalsvc.NoArtifact()
	return nil
}

func (a *fakeAdapter) ManagerState(context.Context) (personalsvc.ManagerState, error) {
	a.events = append(a.events, "manager_state")
	return a.manager, a.failure("manager_state")
}

func (a *fakeAdapter) failure(stage string) error {
	if a.failures[stage] == 0 {
		return nil
	}
	a.failures[stage]--
	return errors.New("secret-adapter-detail")
}
