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
	executor, err := NewExecutor()
	if errors.Is(err, errExecutorUnavailable) {
		t.Skip("Windows Task Scheduler executable is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	userSID, err := currentUserSID()
	if err != nil || !validInteractiveSID(userSID) {
		t.Skip("current process does not have an interactive user identity")
	}

	root := t.TempDir()
	taskName := testTaskNamePrefix + "WP5-" + uuid.New().String()
	scheduler := &namedTaskScheduler{executor: executor, taskName: taskName}
	createdByTest := false
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		state, _, queryErr := scheduler.Query(cleanupCtx)
		if queryErr != nil {
			t.Errorf("cleanup query failed: %v", queryErr)
			return
		}
		if state == personalsvc.TaskSchedulerPresent {
			if !createdByTest {
				t.Errorf("random task identity unexpectedly existed; it was not changed")
				return
			}
			_ = scheduler.End(cleanupCtx)
			if deleteErr := scheduler.Delete(cleanupCtx); deleteErr != nil {
				t.Errorf("cleanup delete failed: %v", deleteErr)
				return
			}
		}
		state, _, queryErr = scheduler.Query(cleanupCtx)
		if queryErr != nil || state != personalsvc.TaskSchedulerAbsent {
			t.Errorf("cleanup final state = %s, %v; want absent", state, queryErr)
		}
	})

	state, _, err := scheduler.Query(t.Context())
	if errors.Is(err, errExecutorUnavailable) {
		t.Skip("Windows Task Scheduler service is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	if state != personalsvc.TaskSchedulerAbsent {
		t.Fatal("random Task Scheduler test identity already exists; refusing to alter it")
	}

	sentinel := filepath.Join(root, "scheduled-child.started")
	plan := nativeLifecyclePlan(t, root, sentinel)
	artifacts, err := newNativeArtifactStore(root)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := newAdapter(plan, root, taskName, userSID, scheduler, artifacts, loopbackHTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Write(t.Context(), plan.Registration()); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Enable(t.Context()); err != nil {
		t.Fatal(err)
	}
	createdByTest = true
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
	state, _, err = scheduler.Query(t.Context())
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
	createdByTest = true
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

func nativeLifecyclePlan(t *testing.T, root, sentinel string) personalsvc.TaskSchedulerSpec {
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
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

type namedTaskScheduler struct {
	executor Executor
	taskName string
	events   []schedulerEvent
}

type schedulerEvent struct {
	operation string
	exitCode  uint32
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
		s.events = append(s.events, schedulerEvent{operation: schedulerOperation(arguments), exitCode: result.ExitCode})
	}
	return result, err
}

func (s *namedTaskScheduler) summary() string {
	parts := make([]string, 0, len(s.events))
	for _, event := range s.events {
		parts = append(parts, fmt.Sprintf("%s=%#x", event.operation, event.exitCode))
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
