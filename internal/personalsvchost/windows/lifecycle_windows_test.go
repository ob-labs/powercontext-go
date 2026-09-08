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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

func TestNativeTaskSchedulerLifecycleUsesOnlyUniqueTestTask(t *testing.T) {
	fixture := newNativeTaskFixture(t)
	root, identity, scheduler := fixture.root, fixture.identity, fixture.scheduler

	sentinel := filepath.Join(root, "scheduled-child.started")
	plan := nativeLifecyclePlan(t, root, sentinel, false)
	artifacts, err := newNativeArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := newAdapter(plan, root, scheduler.taskName, identity, scheduler, artifacts, loopbackHTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Write(t.Context(), plan.Registration()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Enable(t.Context()); err != nil {
		t.Fatal(err)
	}
	fixture.created = true
	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		t.Fatalf("registered manager = %s, %v; want owned", manager.Ownership(), err)
	}
	if err := adapter.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, sentinel, 15*time.Second)
	waitForManagerState(t, adapter, personalsvc.ManagerActive, 10*time.Second)
	if probe, probeErr := adapter.Probe(t.Context(), plan.Registration().Definition().Endpoint()); probeErr != nil || probe != personalsvc.ProbeUnreachable {
		t.Fatalf("harmless child liveness = %s, %v; want unreachable", probe, probeErr)
	}
	if err := adapter.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitForManagerState(t, adapter, personalsvc.ManagerInactive, 10*time.Second)
	if err := adapter.Disable(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Remove(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, _, err := scheduler.Query(t.Context())
	if err != nil || state != personalsvc.TaskSchedulerAbsent {
		t.Fatalf("removed task state = %s, %v; want absent", state, err)
	}
	t.Logf("native scheduler lifecycle: %s; sentinel=observed; manager=active-to-inactive; final=absent", scheduler.summary())
	scheduler.events = nil

	foreignPath := filepath.Join(root, "foreign-task.xml")
	ownedDocument, err := adapter.render()
	if err != nil {
		t.Fatal(err)
	}
	text, ok := decodeTaskDocument(ownedDocument)
	if !ok {
		t.Fatal("cannot decode owned task fixture")
	}
	foreignDocument := encodeTaskDocument(strings.Replace(text, personalsvc.OwnershipMarker, "foreign.owner", 1))
	if err := os.WriteFile(foreignPath, foreignDocument, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Create(t.Context(), foreignPath); err != nil {
		t.Fatal(err)
	}
	fixture.created = true
	state, before, err := scheduler.Query(t.Context())
	if err != nil || state != personalsvc.TaskSchedulerPresent {
		t.Fatalf("foreign fixture state = %s, %v", state, err)
	}
	manager, err = adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipForeign {
		t.Fatalf("foreign manager = %s, %v; want foreign", manager.Ownership(), err)
	}
	if err := adapter.Remove(t.Context()); err == nil {
		t.Fatal("Remove() accepted the intentional foreign task")
	}
	state, after, err := scheduler.Query(t.Context())
	if err != nil || state != personalsvc.TaskSchedulerPresent || !bytes.Equal(before, after) {
		t.Fatalf("foreign task changed after refusal: state = %s, error = %v", state, err)
	}
	if err := scheduler.Delete(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, _, err = scheduler.Query(t.Context())
	if err != nil || state != personalsvc.TaskSchedulerAbsent {
		t.Fatalf("foreign fixture cleanup state = %s, %v; want absent", state, err)
	}
	t.Logf("foreign ownership refusal: %s; manager=foreign; adapter-mutation=refused; xml=unchanged; fixture-cleanup=absent", scheduler.summary())
}

func TestNativeTaskSchedulerLoginTriggerUsesCurrentUser(t *testing.T) {
	fixture := newNativeTaskFixture(t)
	sentinel := filepath.Join(fixture.root, "login-trigger-unused")
	plan := nativeLifecyclePlan(t, fixture.root, sentinel, true)
	artifacts, err := newNativeArtifactStore(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := newAdapter(
		plan,
		fixture.root,
		fixture.scheduler.taskName,
		fixture.identity,
		fixture.scheduler,
		artifacts,
		loopbackHTTPClient(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Write(t.Context(), plan.Registration()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Enable(t.Context()); err != nil {
		t.Fatal(err)
	}
	fixture.created = true
	manager, err := adapter.InspectManager(t.Context())
	if err != nil || manager.Ownership() != personalsvc.ManagerOwnershipOwned {
		t.Fatalf("login-trigger manager = %s, %v; want owned", manager.Ownership(), err)
	}
	state, document, err := fixture.scheduler.Query(t.Context())
	if err != nil || state != personalsvc.TaskSchedulerPresent {
		t.Fatalf("login-trigger query = %s, %v", state, err)
	}
	text, ok := decodeTaskDocument(document)
	currentUserIDs := strings.Count(text, taskXMLLeaf("UserId", fixture.identity.sid)) +
		strings.Count(text, taskXMLLeaf("UserId", fixture.identity.account)) +
		strings.Count(text, taskXMLLeaf("UserId", fixture.identity.name))
	if !ok || currentUserIDs != 2 {
		t.Fatal("registered login trigger is not bound to the current user")
	}
	if err := adapter.Disable(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Remove(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Logf("login trigger: %s; principal=current-user; trigger=current-user; final=absent", fixture.scheduler.summary())
}

func TestNativeTaskSchedulerCancellationAfterCreateRemovesTask(t *testing.T) {
	fixture := newNativeTaskFixture(t)
	sentinel := filepath.Join(fixture.root, "cancel-create-unused")
	plan := nativeLifecyclePlan(t, fixture.root, sentinel, false)
	artifacts, err := newNativeArtifactStore(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	canceling := &cancelAfterCreateScheduler{
		taskScheduler: fixture.scheduler,
		afterCreate: func() {
			fixture.created = true
			cancel()
		},
	}
	adapter, err := newAdapter(
		plan,
		fixture.root,
		fixture.scheduler.taskName,
		fixture.identity,
		canceling,
		artifacts,
		loopbackHTTPClient(),
	)
	if err != nil {
		t.Fatal(err)
	}
	boundary, err := newOperationBoundary(fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := personalsvc.NewController(adapter, boundary)
	if err != nil {
		t.Fatal(err)
	}

	_, installErr := controller.Install(ctx, plan.Registration())
	if !errors.Is(installErr, context.Canceled) {
		t.Fatalf("Install() error = %v; want context cancellation", installErr)
	}
	state, _, err := fixture.scheduler.Query(t.Context())
	if err != nil || state != personalsvc.TaskSchedulerAbsent {
		t.Fatalf("task after canceled create = %s, %v; want absent", state, err)
	}
	artifact, err := adapter.InspectArtifact(t.Context())
	if err != nil || artifact.State() != personalsvc.RegistrationNotInstalled {
		t.Fatalf("artifact after canceled create = %s, %v; want absent", artifact.State(), err)
	}
	t.Logf("canceled create cleanup: %s; caller=canceled; task=absent; artifact=absent", fixture.scheduler.summary())
}

func TestTaskSchedulerSentinelChild(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		return
	}
	if err := os.WriteFile(os.Args[separator+1], []byte("started\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Second)
}

func nativeLifecyclePlan(t *testing.T, root, sentinel string, startOnLogin bool) personalsvc.TaskSchedulerSpec {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    "e2e",
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
	plan, err := personalsvc.NewTaskSchedulerSpec(
		registration,
		[]string{"-test.run=^TestTaskSchedulerSentinelChild$", "-test.count=1", "--", sentinel},
		root,
		startOnLogin,
	)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

type nativeTaskFixture struct {
	root      string
	identity  userIdentity
	scheduler *namedTaskScheduler
	created   bool
}

func newNativeTaskFixture(t *testing.T) *nativeTaskFixture {
	t.Helper()
	executor, err := NewExecutor()
	if errors.Is(err, errExecutorUnavailable) {
		t.Skip("Windows Task Scheduler executable is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	identity, err := currentUserIdentity()
	if err != nil || !validUserIdentity(identity) {
		t.Skip("current process does not have an interactive user identity")
	}
	fixture := &nativeTaskFixture{
		root:     t.TempDir(),
		identity: identity,
		scheduler: &namedTaskScheduler{
			executor: executor,
			taskName: testTaskNamePrefix + "WP5-" + uuid.New().String(),
		},
	}
	state, _, err := fixture.scheduler.Query(t.Context())
	if errors.Is(err, errExecutorUnavailable) {
		t.Skip("Windows Task Scheduler service is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	if state != personalsvc.TaskSchedulerAbsent {
		t.Fatal("random Task Scheduler test identity already exists; refusing to alter it")
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		state, _, queryErr := fixture.scheduler.Query(cleanupCtx)
		if queryErr != nil {
			t.Errorf("cleanup query failed: %v", queryErr)
			return
		}
		if state == personalsvc.TaskSchedulerPresent {
			if !fixture.created {
				t.Errorf("random task identity unexpectedly existed; it was not changed")
				return
			}
			_ = fixture.scheduler.End(cleanupCtx)
			if deleteErr := fixture.scheduler.Delete(cleanupCtx); deleteErr != nil {
				t.Errorf("cleanup delete failed: %v", deleteErr)
				return
			}
		}
		state, _, queryErr = fixture.scheduler.Query(cleanupCtx)
		if queryErr != nil || state != personalsvc.TaskSchedulerAbsent {
			t.Errorf("cleanup final state = %s, %v; want absent", state, queryErr)
		}
	})
	return fixture
}

type cancelAfterCreateScheduler struct {
	taskScheduler
	afterCreate func()
}

func (s *cancelAfterCreateScheduler) Create(ctx context.Context, path string) error {
	if err := s.taskScheduler.Create(ctx, path); err != nil {
		return err
	}
	if s.afterCreate != nil {
		afterCreate := s.afterCreate
		s.afterCreate = nil
		afterCreate()
	}
	return nil
}

type namedTaskScheduler struct {
	executor Executor
	taskName string
	events   []schedulerEvent
}

type schedulerEvent struct {
	operation string
	outcome   string
}

func (s *namedTaskScheduler) Query(ctx context.Context) (personalsvc.TaskSchedulerState, []byte, error) {
	result, err := s.execute(ctx, "/Query", "/TN", s.taskName, "/XML", "/HRESULT")
	if err != nil {
		return personalsvc.TaskSchedulerUnknown, nil, err
	}
	switch result.ExitCode {
	case 0:
		return personalsvc.TaskSchedulerPresent, result.Output, nil
	case 0x80070002, 0x80070003:
		return personalsvc.TaskSchedulerAbsent, nil, nil
	default:
		return personalsvc.TaskSchedulerUnknown, nil, &taskCommandError{code: result.ExitCode}
	}
}

func (s *namedTaskScheduler) Create(ctx context.Context, path string) error {
	return s.required(ctx, "/Create", "/TN", s.taskName, "/XML", path, "/F", "/HRESULT")
}

func (s *namedTaskScheduler) Run(ctx context.Context) error {
	return s.required(ctx, "/Run", "/TN", s.taskName, "/HRESULT")
}

func (s *namedTaskScheduler) End(ctx context.Context) error {
	return s.required(ctx, "/End", "/TN", s.taskName, "/HRESULT")
}

func (s *namedTaskScheduler) Enable(ctx context.Context) error {
	return s.required(ctx, "/Change", "/TN", s.taskName, "/ENABLE", "/HRESULT")
}

func (s *namedTaskScheduler) Disable(ctx context.Context) error {
	return s.required(ctx, "/Change", "/TN", s.taskName, "/DISABLE", "/HRESULT")
}

func (s *namedTaskScheduler) Delete(ctx context.Context) error {
	return s.required(ctx, "/Delete", "/TN", s.taskName, "/F", "/HRESULT")
}

func (s *namedTaskScheduler) State(ctx context.Context) (personalsvc.ManagerState, error) {
	result, err := s.execute(ctx, "/Query", "/TN", s.taskName, "/FO", "CSV", "/NH", "/V", "/HRESULT")
	if err != nil || result.ExitCode != 0 {
		if err != nil {
			return personalsvc.ManagerUnknown, err
		}
		return personalsvc.ManagerUnknown, &taskCommandError{code: result.ExitCode}
	}
	return parseTaskState(result.Output)
}

func (s *namedTaskScheduler) required(ctx context.Context, arguments ...string) error {
	result, err := s.execute(ctx, arguments...)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return &taskCommandError{code: result.ExitCode}
	}
	return nil
}

func (s *namedTaskScheduler) execute(ctx context.Context, arguments ...string) (personalsvc.TaskSchedulerResult, error) {
	result, err := s.executor.Execute(ctx, taskSchedulerProgram, arguments)
	if len(s.events) < 64 {
		outcome := fmt.Sprintf("%#x", result.ExitCode)
		if errors.Is(err, context.Canceled) {
			outcome = "canceled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			outcome = "deadline"
		} else if err != nil {
			outcome = "error"
		}
		s.events = append(s.events, schedulerEvent{operation: schedulerOperation(arguments), outcome: outcome})
	}
	return result, err
}

func (s *namedTaskScheduler) summary() string {
	parts := make([]string, 0, len(s.events))
	for _, event := range s.events {
		parts = append(parts, event.operation+"="+event.outcome)
	}
	return strings.Join(parts, ",")
}

func schedulerOperation(arguments []string) string {
	if len(arguments) == 0 {
		return "invalid"
	}
	operation := strings.ToLower(strings.TrimPrefix(arguments[0], "/"))
	if operation == "query" && slices.Contains(arguments, "/XML") {
		return "query-xml"
	}
	if operation == "query" && slices.Contains(arguments, "CSV") {
		return "query-state"
	}
	if operation == "change" && slices.Contains(arguments, "/ENABLE") {
		return "enable"
	}
	if operation == "change" && slices.Contains(arguments, "/DISABLE") {
		return "disable"
	}
	return operation
}

type taskCommandError struct{ code uint32 }

func (e *taskCommandError) Error() string {
	return fmt.Sprintf("Task Scheduler command failed with code %#x", e.code)
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("scheduled child did not create its sentinel")
}

func waitForManagerState(t *testing.T, adapter *Adapter, want personalsvc.ManagerState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := adapter.ManagerState(t.Context())
		if err == nil && state == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	state, err := adapter.ManagerState(t.Context())
	t.Fatalf("manager state = %s, %v; want %s", state, err, want)
}
