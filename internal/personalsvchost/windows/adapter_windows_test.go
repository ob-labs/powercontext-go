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

package windows

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

var testIdentity = userIdentity{
	sid:     "S-1-5-21-100-200-300-1001",
	account: `CONTOSO\alice`,
	name:    "alice",
}

func TestAdapterCompletesOwnedTaskLifecycleThroughExplicitBoundaries(t *testing.T) {
	root := t.TempDir()
	plan := adapterPlan(t, root)
	files := newMemoryArtifacts()
	scheduler := &memoryScheduler{files: files}
	adapter, err := newAdapter(
		plan,
		root,
		`\PowerContext\Tests\unit-lifecycle`,
		testIdentity,
		scheduler,
		files,
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}

	support, err := adapter.Support(t.Context())
	if err != nil || support != personalsvc.SupportSupported {
		t.Fatalf("support = %s, %v; want supported", support, err)
	}
	artifact, err := adapter.InspectArtifact(t.Context())
	if err != nil || artifact.State() != personalsvc.RegistrationNotInstalled {
		t.Fatalf("initial artifact = %s, %v; want not installed", artifact.State(), err)
	}
	if err := adapter.Write(t.Context(), plan.Registration()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Enable(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		t.Fatalf("manager = %s, %v; want owned", manager.Ownership(), err)
	}
	if err := adapter.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if state, stateErr := adapter.ManagerState(t.Context()); stateErr != nil || state != personalsvc.ManagerActive {
		t.Fatalf("manager state = %s, %v; want active", state, stateErr)
	}
	if err := adapter.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Disable(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Remove(t.Context()); err != nil {
		t.Fatal(err)
	}

	wantPath := filepath.Join(root, "PowerContext", "Services", "personal-server.xml")
	if files.writePath != wantPath || files.removePath != wantPath {
		t.Fatalf("artifact paths = write %q, remove %q; want %q", files.writePath, files.removePath, wantPath)
	}
	if bytes.Contains(files.lastWrite, []byte(personalsvc.TaskSchedulerInteractiveUser)) {
		t.Fatal("written task retained the unresolved interactive-user placeholder")
	}
	if !slices.Equal(scheduler.mutations, []string{"create", "enable", "run", "end", "disable", "delete"}) {
		t.Fatalf("scheduler mutations = %#v", scheduler.mutations)
	}
	artifact, err = adapter.InspectArtifact(t.Context())
	if err != nil || artifact.State() != personalsvc.RegistrationNotInstalled {
		t.Fatalf("final artifact = %s, %v; want not installed", artifact.State(), err)
	}
	manager, err = adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipNotLoaded {
		t.Fatalf("final manager = %s, %v; want not loaded", manager.Ownership(), err)
	}
}

func TestAdapterRejectsForeignLoadedTaskBeforeMutation(t *testing.T) {
	root := t.TempDir()
	plan := adapterPlan(t, root)
	files := newMemoryArtifacts()
	scheduler := &memoryScheduler{files: files}
	adapter, err := newAdapter(
		plan,
		root,
		`\PowerContext\Tests\unit-foreign`,
		testIdentity,
		scheduler,
		files,
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	owned, err := adapter.render()
	if err != nil {
		t.Fatal(err)
	}
	text, ok := decodeTaskDocument(owned)
	if !ok {
		t.Fatal("cannot decode owned fixture")
	}
	foreign := encodeTaskDocument(strings.Replace(text, personalsvc.OwnershipMarker, "foreign.owner", 1))
	if bytes.Equal(foreign, owned) {
		t.Fatal("foreign fixture did not change the ownership marker")
	}
	scheduler.present = true
	scheduler.document = foreign
	before := bytes.Clone(scheduler.document)

	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipForeign {
		t.Fatalf("manager = %s, %v; want foreign", manager.Ownership(), err)
	}
	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{name: "write", run: func() error { return adapter.Write(t.Context(), plan.Registration()) }},
		{name: "enable", run: func() error { return adapter.Enable(t.Context()) }},
		{name: "start", run: func() error { return adapter.Start(t.Context()) }},
		{name: "stop", run: func() error { return adapter.Stop(t.Context()) }},
		{name: "disable", run: func() error { return adapter.Disable(t.Context()) }},
		{name: "remove", run: func() error { return adapter.Remove(t.Context()) }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if err := operation.run(); err == nil {
				t.Fatal("operation accepted a foreign loaded task")
			}
		})
	}
	if len(scheduler.mutations) != 0 || !bytes.Equal(scheduler.document, before) {
		t.Fatalf("foreign task changed: mutations = %#v", scheduler.mutations)
	}
}

func TestAdapterLoginTriggerAcceptsOnlyCurrentUserIdentity(t *testing.T) {
	root := t.TempDir()
	base := adapterPlan(t, root)
	plan, err := personalsvc.NewTaskSchedulerSpec(
		base.Registration(), base.Arguments(), base.WorkingDirectory(), true,
	)
	if err != nil {
		t.Fatal(err)
	}
	files := newMemoryArtifacts()
	scheduler := &memoryScheduler{files: files}
	adapter, err := newAdapter(
		plan,
		root,
		`\PowerContext\Tests\unit-login-user`,
		testIdentity,
		scheduler,
		files,
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := adapter.render()
	if err != nil {
		t.Fatal(err)
	}
	scheduler.present, scheduler.document = true, document
	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		t.Fatalf("current-user trigger = %s, %v; want owned", manager.Ownership(), err)
	}
	text, ok := decodeTaskDocument(document)
	if !ok {
		t.Fatal("cannot decode login trigger fixture")
	}
	foreign := encodeTaskDocument(strings.Replace(
		text,
		taskXMLLeaf("UserId", testIdentity.account),
		taskXMLLeaf("UserId", `CONTOSO\foreign`),
		1,
	))
	if bytes.Equal(foreign, document) {
		t.Fatal("foreign trigger fixture did not change")
	}
	scheduler.document = foreign
	manager, err = adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipForeign {
		t.Fatalf("foreign-user trigger = %s, %v; want foreign", manager.Ownership(), err)
	}
}

func TestTaskSchedulerStateUsesNumericResultInsteadOfLocalizedText(t *testing.T) {
	for _, test := range []struct {
		name string
		code uint32
		want personalsvc.ManagerState
	}{
		{name: "running", code: 0x41301, want: personalsvc.ManagerActive},
		{name: "queued", code: 0x41325, want: personalsvc.ManagerActive},
		{name: "successful", code: 0, want: personalsvc.ManagerInactive},
		{name: "not yet run", code: 0x41303, want: personalsvc.ManagerInactive},
		{name: "failed", code: 5, want: personalsvc.ManagerFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			line := `"主机","任务","下次运行","任意本地化状态","登录","上次运行","` +
				fmt.Sprint(test.code) + `","作者","操作","目录","注释","任意状态"` + "\r\n"
			state, err := parseTaskState([]byte(line))
			if err != nil || state != test.want {
				t.Fatalf("parseTaskState() = %s, %v; want %s", state, err, test.want)
			}
		})
	}
	if _, err := parseTaskState([]byte(`"too","short"`)); err == nil {
		t.Fatal("parseTaskState() accepted a truncated row")
	}
}

func TestAdapterZeroValueFailsClosed(t *testing.T) {
	zero := &Adapter{}
	if _, err := zero.InspectManager(t.Context()); err == nil {
		t.Fatal("zero-value adapter inspected the scheduler")
	}
	if err := zero.Start(t.Context()); err == nil {
		t.Fatal("zero-value adapter started a task")
	}
}

func TestAdapterRejectsGlobalRoot(t *testing.T) {
	root := filepath.Join(filepath.VolumeName(t.TempDir())+string(filepath.Separator), "Windows")
	plan := adapterPlan(t, t.TempDir())
	adapter, err := newAdapter(
		plan,
		root,
		personalsvc.TaskSchedulerTaskName,
		testIdentity,
		&memoryScheduler{files: newMemoryArtifacts()},
		newMemoryArtifacts(),
		http.DefaultClient,
	)
	if adapter != nil || err == nil {
		t.Fatalf("newAdapter(global root) = %v, %v; want rejection", adapter, err)
	}
	if _, found := errors.AsType[*Error](err); !found {
		t.Fatalf("configuration error type = %T; want *Error", err)
	}
}

func TestInteractiveSessionRejectsServiceSession(t *testing.T) {
	if validInteractiveSession(0) {
		t.Fatal("session 0 was accepted as an interactive user session")
	}
	if !validInteractiveSession(1) {
		t.Fatal("nonzero user session was rejected")
	}
}

func TestNewComposesProductionAdapterAndOperationBoundary(t *testing.T) {
	if (&Composition{}).Adapter() != nil || (&Composition{}).OperationBoundary() != nil {
		t.Fatal("zero-value composition exposed a typed-nil boundary")
	}
	if _, err := currentUserIdentity(); err != nil {
		t.Skip("current process does not have an interactive user identity")
	}
	root := t.TempDir()
	composition, err := New(Config{UserDataRoot: root, Plan: adapterPlan(t, root)})
	if err != nil {
		t.Fatal(err)
	}
	if composition.Adapter() == nil || composition.OperationBoundary() == nil {
		t.Fatal("New() returned an incomplete composition")
	}
	if got := composition.Adapter().artifactPath; got != filepath.Join(root, "PowerContext", "Services", "personal-server.xml") {
		t.Fatalf("artifact path = %q", got)
	}
	if composition.Adapter().taskName != personalsvc.TaskSchedulerTaskName {
		t.Fatalf("production task = %q", composition.Adapter().taskName)
	}
}

func TestOperationBoundarySerializesAndHonorsCancellation(t *testing.T) {
	boundary, err := newOperationBoundary(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- boundary.Run(t.Context(), func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first operation did not enter the boundary")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	secondEntered := false
	err = boundary.Run(ctx, func(context.Context) error {
		secondEntered = true
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || secondEntered {
		t.Fatalf("contended Run() = %v, entered = %t", err, secondEntered)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

func TestProbeRequiresExactLoopbackLivenessContract(t *testing.T) {
	contacts := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		contacts++
		if request.URL.String() != "http://127.0.0.1:8123/health/live" {
			t.Errorf("probe URL = %q", request.URL)
		}
		header := make(http.Header)
		header.Set("Content-Type", "application/json")
		header.Set("X-PowerContext-Request-ID", "0123456789abcdef")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
		}, nil
	})}
	root := t.TempDir()
	plan := adapterPlan(t, root)
	adapter, err := newAdapter(
		plan,
		root,
		`\PowerContext\Tests\unit-probe`,
		testIdentity,
		&memoryScheduler{files: newMemoryArtifacts()},
		newMemoryArtifacts(),
		client,
	)
	if err != nil {
		t.Fatal(err)
	}

	state, err := adapter.Probe(t.Context(), "http://127.0.0.1:8123")
	if err != nil || state != personalsvc.ProbeLive {
		t.Fatalf("Probe() = %s, %v; want live", state, err)
	}
	state, err = adapter.Probe(t.Context(), "http://example.test:8123")
	if err == nil || state != personalsvc.ProbeUnreachable {
		t.Fatalf("remote Probe() = %s, %v; want rejected", state, err)
	}
	if contacts != 1 {
		t.Fatalf("probe contacts = %d; want one loopback request", contacts)
	}
}

func TestStopDoesNotEndOwnedInactiveTask(t *testing.T) {
	root := t.TempDir()
	plan := adapterPlan(t, root)
	files := newMemoryArtifacts()
	scheduler := &memoryScheduler{files: files}
	adapter, err := newAdapter(
		plan,
		root,
		`\PowerContext\Tests\unit-inactive`,
		testIdentity,
		scheduler,
		files,
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	document, err := adapter.render()
	if err != nil {
		t.Fatal(err)
	}
	scheduler.present = true
	scheduler.document = document
	scheduler.state = personalsvc.ManagerInactive

	if err := adapter.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(scheduler.mutations) != 0 {
		t.Fatalf("inactive Stop() mutations = %#v; want none", scheduler.mutations)
	}
}

func TestControllerRestoresPreviouslyInspectedTaskAfterEnableFailure(t *testing.T) {
	root := t.TempDir()
	desired := adapterPlanForPackage(t, root, "2.0.0")
	previous := adapterPlanForPackage(t, root, "1.0.0")
	files := newMemoryArtifacts()
	scheduler := &memoryScheduler{files: files, enableErr: errors.New("native enable detail")}
	adapter, err := newAdapter(
		desired,
		root,
		`\PowerContext\Tests\unit-rollback`,
		testIdentity,
		scheduler,
		files,
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	previousDocument, err := previous.XML()
	if err != nil {
		t.Fatal(err)
	}
	previousDocument, ok := rewriteTaskDocument(
		previousDocument,
		personalsvc.TaskSchedulerTaskName,
		adapter.taskName,
		personalsvc.TaskSchedulerInteractiveUser,
		testIdentity.sid,
	)
	if !ok {
		t.Fatal("cannot resolve previous task fixture")
	}
	files.content, files.exists = previousDocument, true
	scheduler.document, scheduler.present = bytes.Clone(previousDocument), true
	controller, err := personalsvc.NewController(adapter, directOperationBoundary{})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := controller.Install(t.Context(), desired.Registration()); err == nil {
		t.Fatal("Install() unexpectedly survived the injected enable failure")
	}
	artifact, err := adapter.InspectArtifact(t.Context())
	if err != nil || artifact.State() != personalsvc.RegistrationInstalled {
		t.Fatalf("restored artifact = %s, %v", artifact.State(), err)
	}
	registration, found := artifact.Registration()
	if !found || registration != previous.Registration() {
		t.Fatal("enable failure did not restore the previously inspected registration")
	}
}

func TestControllerCancellationAfterCreateRemovesNewTask(t *testing.T) {
	root := t.TempDir()
	plan := adapterPlan(t, root)
	files := newMemoryArtifacts()
	type cleanupContextKey struct{}
	parent := context.WithValue(t.Context(), cleanupContextKey{}, "preserved")
	ctx, cancel := context.WithCancel(parent)
	cleanupObserved := false
	scheduler := &memoryScheduler{
		files: files, cancelAfterCreate: cancel, rejectCanceled: true,
		observeCleanup: func(cleanupCtx context.Context) error {
			if cleanupCtx.Err() != nil || cleanupCtx.Value(cleanupContextKey{}) != "preserved" {
				return errors.New("cleanup context did not preserve values independently")
			}
			if _, bounded := cleanupCtx.Deadline(); !bounded {
				return errors.New("cleanup context is not bounded")
			}
			cleanupObserved = true
			return nil
		},
	}
	adapter, err := newAdapter(
		plan,
		root,
		`\PowerContext\Tests\unit-cancel-create`,
		testIdentity,
		scheduler,
		files,
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := personalsvc.NewController(adapter, directOperationBoundary{})
	if err != nil {
		t.Fatal(err)
	}

	_, installErr := controller.Install(ctx, plan.Registration())
	if !errors.Is(installErr, context.Canceled) {
		t.Fatalf("Install() error = %v; want context cancellation", installErr)
	}
	artifact, err := adapter.InspectArtifact(t.Context())
	if err != nil || artifact.State() != personalsvc.RegistrationNotInstalled {
		t.Fatalf("artifact after cancellation = %s, %v; want removed", artifact.State(), err)
	}
	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipNotLoaded {
		t.Fatalf("manager after cancellation = %s, %v; want not loaded", manager.Ownership(), err)
	}
	if !cleanupObserved {
		t.Fatal("cancellation recovery did not use the bounded cleanup context")
	}
}

func TestControllerCancellationAfterReplaceRestoresPreviousTask(t *testing.T) {
	root := t.TempDir()
	desired := adapterPlanForPackage(t, root, "2.0.0")
	previous := adapterPlanForPackage(t, root, "1.0.0")
	files := newMemoryArtifacts()
	ctx, cancel := context.WithCancel(t.Context())
	scheduler := &memoryScheduler{files: files, cancelAfterCreate: cancel, rejectCanceled: true}
	adapter, err := newAdapter(
		desired,
		root,
		`\PowerContext\Tests\unit-cancel-replace`,
		testIdentity,
		scheduler,
		files,
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	previousDocument, err := adapter.renderPlan(previous)
	if err != nil {
		t.Fatal(err)
	}
	files.content, files.exists = previousDocument, true
	scheduler.document, scheduler.present = bytes.Clone(previousDocument), true
	controller, err := personalsvc.NewController(adapter, directOperationBoundary{})
	if err != nil {
		t.Fatal(err)
	}

	_, installErr := controller.Install(ctx, desired.Registration())
	if !errors.Is(installErr, context.Canceled) {
		t.Fatalf("Install() error = %v; want context cancellation", installErr)
	}
	artifact, err := adapter.InspectArtifact(t.Context())
	if err != nil || artifact.State() != personalsvc.RegistrationInstalled {
		t.Fatalf("artifact after canceled replace = %s, %v", artifact.State(), err)
	}
	stored, found := artifact.Registration()
	if !found || stored != previous.Registration() {
		t.Fatal("canceled replace did not restore the previous artifact")
	}
	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		t.Fatalf("manager after canceled replace = %s, %v", manager.Ownership(), err)
	}
	loaded, found := manager.Registration()
	if !found || loaded != previous.Registration() {
		t.Fatal("canceled replace did not restore the previous loaded task")
	}
}

func TestControllerCancellationRestoresManagerWithoutArtifact(t *testing.T) {
	root := t.TempDir()
	desired := adapterPlanForPackage(t, root, "2.0.0")
	previous := adapterPlanForPackage(t, root, "1.0.0")
	files := newMemoryArtifacts()
	ctx, cancel := context.WithCancel(t.Context())
	scheduler := &memoryScheduler{files: files, cancelAfterCreate: cancel, rejectCanceled: true}
	adapter, err := newAdapter(
		desired,
		root,
		`\PowerContext\Tests\unit-mixed-manager`,
		testIdentity,
		scheduler,
		files,
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	previousDocument, err := adapter.renderPlan(previous)
	if err != nil {
		t.Fatal(err)
	}
	scheduler.document, scheduler.present = previousDocument, true
	controller, err := personalsvc.NewController(adapter, directOperationBoundary{})
	if err != nil {
		t.Fatal(err)
	}

	_, installErr := controller.Install(ctx, desired.Registration())
	if !errors.Is(installErr, context.Canceled) {
		t.Fatalf("Install() error = %v; want context cancellation", installErr)
	}
	artifact, err := adapter.InspectArtifact(t.Context())
	if err != nil || artifact.State() != personalsvc.RegistrationNotInstalled {
		t.Fatalf("mixed artifact after cancellation = %s, %v; want absent", artifact.State(), err)
	}
	assertOwnedManagerRegistration(t, adapter, previous.Registration())
}

func TestControllerCancellationRestoresArtifactWithoutManager(t *testing.T) {
	root := t.TempDir()
	desired := adapterPlanForPackage(t, root, "2.0.0")
	previous := adapterPlanForPackage(t, root, "1.0.0")
	files := newMemoryArtifacts()
	ctx, cancel := context.WithCancel(t.Context())
	scheduler := &memoryScheduler{files: files, cancelAfterCreate: cancel, rejectCanceled: true}
	adapter, err := newAdapter(
		desired,
		root,
		`\PowerContext\Tests\unit-mixed-artifact`,
		testIdentity,
		scheduler,
		files,
		http.DefaultClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	previousDocument, err := adapter.renderPlan(previous)
	if err != nil {
		t.Fatal(err)
	}
	files.content, files.exists = previousDocument, true
	controller, err := personalsvc.NewController(adapter, directOperationBoundary{})
	if err != nil {
		t.Fatal(err)
	}

	_, installErr := controller.Install(ctx, desired.Registration())
	if !errors.Is(installErr, context.Canceled) {
		t.Fatalf("Install() error = %v; want context cancellation", installErr)
	}
	artifact, err := adapter.InspectArtifact(t.Context())
	if err != nil || artifact.State() != personalsvc.RegistrationInstalled {
		t.Fatalf("mixed artifact after cancellation = %s, %v; want installed", artifact.State(), err)
	}
	stored, found := artifact.Registration()
	if !found || stored != previous.Registration() {
		t.Fatal("mixed cancellation did not restore the prior artifact")
	}
	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipNotLoaded {
		t.Fatalf("mixed manager after cancellation = %s, %v; want not loaded", manager.Ownership(), err)
	}
}

func assertOwnedManagerRegistration(t *testing.T, adapter *Adapter, want personalsvc.Registration) {
	t.Helper()
	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		t.Fatalf("manager after cancellation = %s, %v; want owned", manager.Ownership(), err)
	}
	loaded, found := manager.Registration()
	if !found || loaded != want {
		t.Fatal("manager after cancellation has the wrong registration")
	}
}

func TestNativeArtifactStoreReplacesExactArtifact(t *testing.T) {
	root := t.TempDir()
	store, err := newNativeArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "PowerContext", "Services", "personal-server.xml")
	if err := store.Write(t.Context(), path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := store.Write(t.Context(), path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	content, exists, err := store.Read(t.Context(), path)
	if err != nil || !exists || string(content) != "second" {
		t.Fatalf("replaced artifact = %q, exists %t, error %v", content, exists, err)
	}
	if _, _, err := store.Read(t.Context(), filepath.Join(root, "foreign.xml")); err == nil {
		t.Fatal("artifact store accepted a foreign path")
	}
}

func TestOperationBoundaryUsesCrossInstanceFileLock(t *testing.T) {
	root := t.TempDir()
	firstBoundary, err := newOperationBoundary(root)
	if err != nil {
		t.Fatal(err)
	}
	secondBoundary, err := newOperationBoundary(root)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- firstBoundary.Run(t.Context(), func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first operation did not acquire its file lock")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err = secondBoundary.Run(ctx, func(context.Context) error {
		t.Fatal("contended cross-instance operation entered")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended cross-instance Run() = %v", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func adapterPlan(t *testing.T, root string) personalsvc.TaskSchedulerSpec {
	return adapterPlanForPackage(t, root, "1.2.3")
}

func adapterPlanForPackage(t *testing.T, root, packageVersion string) personalsvc.TaskSchedulerSpec {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    packageVersion,
		Binary:            binary,
		Endpoint:          "http://127.0.0.1:1",
		DataDir:           root,
	})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := personalsvc.NewRegistration(definition)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := personalsvc.NewTaskSchedulerSpec(registration, []string{"server", "run"}, root, false)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

type memoryArtifacts struct {
	content    []byte
	exists     bool
	writePath  string
	removePath string
	lastWrite  []byte
}

func newMemoryArtifacts() *memoryArtifacts { return &memoryArtifacts{} }

func (f *memoryArtifacts) Read(_ context.Context, path string) ([]byte, bool, error) {
	return bytes.Clone(f.content), f.exists, nil
}

func (f *memoryArtifacts) Write(_ context.Context, path string, content []byte) error {
	f.content = bytes.Clone(content)
	f.exists = true
	f.writePath = path
	f.lastWrite = bytes.Clone(content)
	return nil
}

func (f *memoryArtifacts) Remove(_ context.Context, path string) error {
	f.content = nil
	f.exists = false
	f.removePath = path
	return nil
}

func (*memoryArtifacts) RegularFile(context.Context, string) (bool, error) { return true, nil }

type memoryScheduler struct {
	files             *memoryArtifacts
	document          []byte
	present           bool
	state             personalsvc.ManagerState
	mutations         []string
	enableErr         error
	cancelAfterCreate context.CancelFunc
	rejectCanceled    bool
	observeCleanup    func(context.Context) error
}

func (s *memoryScheduler) Query(ctx context.Context) (personalsvc.TaskSchedulerState, []byte, error) {
	if err := s.contextError(ctx); err != nil {
		return personalsvc.TaskSchedulerUnknown, nil, err
	}
	if !s.present {
		return personalsvc.TaskSchedulerAbsent, nil, nil
	}
	return personalsvc.TaskSchedulerPresent, bytes.Clone(s.document), nil
}

func (s *memoryScheduler) Create(ctx context.Context, path string) error {
	if err := s.contextError(ctx); err != nil {
		return err
	}
	content, exists, err := s.files.Read(ctx, path)
	if err != nil || !exists {
		return os.ErrNotExist
	}
	s.document = content
	s.present = true
	s.state = personalsvc.ManagerInactive
	s.mutations = append(s.mutations, "create")
	if s.cancelAfterCreate != nil {
		cancel := s.cancelAfterCreate
		s.cancelAfterCreate = nil
		cancel()
	}
	return nil
}

func (s *memoryScheduler) Run(context.Context) error {
	s.state = personalsvc.ManagerActive
	s.mutations = append(s.mutations, "run")
	return nil
}

func (s *memoryScheduler) End(context.Context) error {
	s.state = personalsvc.ManagerInactive
	s.mutations = append(s.mutations, "end")
	return nil
}

func (s *memoryScheduler) Enable(ctx context.Context) error {
	s.mutations = append(s.mutations, "enable")
	if err := s.contextError(ctx); err != nil {
		return err
	}
	if s.enableErr != nil {
		err := s.enableErr
		s.enableErr = nil
		return err
	}
	return nil
}

func (s *memoryScheduler) Disable(ctx context.Context) error {
	if err := s.contextError(ctx); err != nil {
		return err
	}
	if s.observeCleanup != nil {
		observe := s.observeCleanup
		s.observeCleanup = nil
		if err := observe(ctx); err != nil {
			return err
		}
	}
	s.state = personalsvc.ManagerInactive
	s.mutations = append(s.mutations, "disable")
	return nil
}

func (s *memoryScheduler) Delete(ctx context.Context) error {
	if err := s.contextError(ctx); err != nil {
		return err
	}
	s.document = nil
	s.present = false
	s.state = personalsvc.ManagerInactive
	s.mutations = append(s.mutations, "delete")
	return nil
}

func (s *memoryScheduler) State(context.Context) (personalsvc.ManagerState, error) {
	return s.state, nil
}

func (s *memoryScheduler) contextError(ctx context.Context) error {
	if s.rejectCanceled {
		return ctx.Err()
	}
	return nil
}

type directOperationBoundary struct{}

func (directOperationBoundary) Run(ctx context.Context, operation func(context.Context) error) error {
	return operation(ctx)
}
