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

package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/contextpack"
)

type recordedRuntimeStage struct {
	owner      *recordingStageTracing
	name       string
	attributes map[string]TraceAttribute
	outcome    string
	err        error
}

type recordingStageTracing struct {
	mu      sync.Mutex
	stages  []*recordedRuntimeStage
	panic   bool
	started chan string
}

func (t *recordingStageTracing) StartStage(
	ctx context.Context,
	name string,
	attributes map[string]TraceAttribute,
) (context.Context, StageSpan) {
	if t.panic {
		panic("tracer unavailable")
	}
	stage := &recordedRuntimeStage{owner: t, name: name, attributes: cloneTraceAttributes(attributes)}
	t.mu.Lock()
	t.stages = append(t.stages, stage)
	t.mu.Unlock()
	if t.started != nil {
		select {
		case t.started <- name:
		default:
		}
	}
	return ctx, stage
}

func (s *recordedRuntimeStage) SetAttributes(attributes map[string]TraceAttribute) {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	if s.attributes == nil {
		s.attributes = make(map[string]TraceAttribute)
	}
	for key, value := range attributes {
		s.attributes[key] = value
	}
}

func (s *recordedRuntimeStage) Finish(outcome string, err error) {
	s.owner.mu.Lock()
	defer s.owner.mu.Unlock()
	s.outcome, s.err = outcome, err
}

func TestScopedStagesAreBoundedAndLockStageEndsBeforeCriticalSection(t *testing.T) {
	t.Parallel()
	tracing := &recordingStageTracing{}
	runtime, err := NewConfigured(RuntimeOptions{Tracing: tracing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	operationError := errors.New("critical section failed")
	err = runtime.ScopedWrite(t.Context(), "private-scope", func(context.Context, string) error {
		return operationError
	})
	if !errors.Is(err, operationError) {
		t.Fatalf("ScopedWrite error = %v", err)
	}
	tracing.mu.Lock()
	defer tracing.mu.Unlock()
	if len(tracing.stages) != 2 {
		t.Fatalf("stages = %v", tracing.stages)
	}
	if tracing.stages[0].name != "scope.context" || tracing.stages[0].outcome != "success" {
		t.Fatalf("context stage = %#v", tracing.stages[0])
	}
	lock := tracing.stages[1]
	if lock.name != "scope.lock" || lock.outcome != "success" || lock.err != nil ||
		!reflect.DeepEqual(lock.attributes, map[string]TraceAttribute{"powercontext.scope.lock.contended": false}) {
		t.Fatalf("lock stage = %#v", lock)
	}
	for _, stage := range tracing.stages {
		for _, value := range stage.attributes {
			if value == "private-scope" {
				t.Fatal("stage leaked raw Scope")
			}
		}
	}
}

func TestMemoryReadStagesAreBoundedAndNestedWithinOneScopedOperation(t *testing.T) {
	tracing := &recordingStageTracing{}
	lifecycle, err := NewConfigured(RuntimeOptions{Tracing: tracing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := lifecycle.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	backend := &advancingSearchBackend{revisions: searchMemoryRevisions(t, 1)}
	service, err := memory.NewService(backend, memory.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memoryApplication, err := NewMemoryApplication(lifecycle, func(string) (*memory.Service, error) {
		return service, nil
	}, "memory")
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewContextApplication(lifecycle, memoryApplication, nil)
	if err != nil {
		t.Fatal(err)
	}
	request, err := contextpack.NewRequest("private query that must not be traced", 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := application.Prepare(t.Context(), "project:private-stage-scope", request)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status() != contextpack.Empty {
		t.Fatalf("prepared status = %q", prepared.Status())
	}

	tracing.mu.Lock()
	defer tracing.mu.Unlock()
	if len(tracing.stages) != 5 {
		t.Fatalf("stages = %#v", tracing.stages)
	}
	want := []struct {
		name       string
		attributes map[string]TraceAttribute
	}{
		{name: "scope.context", attributes: nil},
		{name: "scope.lock", attributes: map[string]TraceAttribute{"powercontext.scope.lock.contended": false}},
		{name: "memory.search", attributes: map[string]TraceAttribute{
			"powercontext.memory.search.requested_mode": string(memory.SearchAuto),
			"powercontext.memory.search.limit":          contextpack.MemoryCandidateLimit,
			"powercontext.memory.search.memory_present": true,
			"powercontext.memory.search.mode":           string(memory.SearchFTS),
			"powercontext.memory.search.result_count":   0,
		}},
		{name: "experience.search", attributes: map[string]TraceAttribute{
			"powercontext.experience.search.configured":   false,
			"powercontext.experience.search.limit":        contextpack.ExperienceCandidateLimit,
			"powercontext.experience.search.result_count": 0,
		}},
		{name: "context.build", attributes: map[string]TraceAttribute{
			"powercontext.context.build.memory_candidate_count":     0,
			"powercontext.context.build.experience_candidate_count": 0,
			"powercontext.context.build.selected_count":             0,
			"powercontext.context.build.status":                     string(contextpack.Empty),
			"powercontext.context.build.content_bytes":              0,
		}},
	}
	for index, expected := range want {
		stage := tracing.stages[index]
		if stage.name != expected.name || stage.outcome != "success" || stage.err != nil ||
			!reflect.DeepEqual(stage.attributes, expected.attributes) {
			t.Fatalf("stage %d = %#v, want %#v", index, stage, expected)
		}
		for _, value := range stage.attributes {
			if text, ok := value.(string); ok &&
				(strings.Contains(text, "private-stage-scope") || strings.Contains(text, "private query")) {
				t.Fatalf("stage %q leaked private input: %q", stage.name, text)
			}
		}
	}
}

func TestRuntimeStageClassifiesFailureAndCancellation(t *testing.T) {
	t.Parallel()
	tracing := &recordingStageTracing{}
	runtime, err := NewConfigured(RuntimeOptions{Tracing: tracing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("sensitive failure")
	if err := runtime.runStage(t.Context(), "memory.search", nil, func(context.Context, StageSpan) error {
		return failure
	}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runtime.runStage(canceled, "memory.search", nil, func(context.Context, StageSpan) error {
		return context.Canceled
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	tracing.mu.Lock()
	defer tracing.mu.Unlock()
	if tracing.stages[0].outcome != "failure" || tracing.stages[1].outcome != "cancelled" {
		t.Fatalf("stage outcomes = %q, %q", tracing.stages[0].outcome, tracing.stages[1].outcome)
	}
}

func TestRuntimeStageTracerFailureDoesNotChangeOperation(t *testing.T) {
	t.Parallel()
	runtime, err := NewConfigured(RuntimeOptions{Tracing: &recordingStageTracing{panic: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := runtime.ScopedRead(t.Context(), "scope", func(context.Context, string) error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("operation changed by tracer failure: called=%t err=%v", called, err)
	}
}

func TestScopeLockStageReportsContentionAndEndsAtAcquisition(t *testing.T) {
	t.Parallel()
	tracing := &recordingStageTracing{started: make(chan string, 8)}
	runtime, err := NewConfigured(RuntimeOptions{Tracing: tracing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- runtime.ScopedWrite(t.Context(), "private-scope", func(context.Context, string) error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered
	for range 2 {
		<-tracing.started
	}

	secondEntered := make(chan struct{})
	releaseSecond := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- runtime.ScopedWrite(t.Context(), "private-scope", func(context.Context, string) error {
			close(secondEntered)
			<-releaseSecond
			return nil
		})
	}()
	for range 2 {
		<-tracing.started
	}
	select {
	case <-secondEntered:
		t.Fatal("contending writer entered before the first writer released its Scope lock")
	default:
	}
	close(releaseFirst)
	<-secondEntered

	tracing.mu.Lock()
	lock := tracing.stages[len(tracing.stages)-1]
	if lock.name != "scope.lock" || lock.outcome != "success" ||
		!reflect.DeepEqual(lock.attributes, map[string]TraceAttribute{"powercontext.scope.lock.contended": true}) {
		tracing.mu.Unlock()
		t.Fatalf("contended lock stage = %#v", lock)
	}
	tracing.mu.Unlock()
	close(releaseSecond)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

type panicFinishTracing struct{}

func (panicFinishTracing) StartStage(ctx context.Context, _ string, _ map[string]TraceAttribute) (context.Context, StageSpan) {
	return ctx, panicFinishSpan{}
}

type panicFinishSpan struct{}

func (panicFinishSpan) SetAttributes(map[string]TraceAttribute) {}
func (panicFinishSpan) Finish(string, error)                    { panic("trace teardown failed") }

func TestScopeLockIsReleasedWhenTraceTeardownPanics(t *testing.T) {
	t.Parallel()
	runtime, err := NewConfigured(RuntimeOptions{Tracing: panicFinishTracing{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		called := false
		if err := runtime.ScopedWrite(t.Context(), "scope", func(context.Context, string) error {
			called = true
			return nil
		}); err != nil || !called {
			t.Fatalf("Scope write after trace teardown failure: called=%t err=%v", called, err)
		}
	}
}
