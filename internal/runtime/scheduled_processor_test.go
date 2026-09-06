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
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/source"
)

func TestScheduledProcessorRecordsBackgroundRootsAndOutcomes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		operation   string
		outcomes    []string
		wantOutcome string
		wantErr     error
	}{
		{
			name: "source window success", operation: ProcessSourceWindowOperation,
			outcomes: []string{ScheduledProcessingSuccess}, wantOutcome: ScheduledProcessingSuccess,
		},
		{
			name: "source window noop", operation: ProcessSourceWindowOperation,
			outcomes: []string{ScheduledProcessingNoop}, wantOutcome: ScheduledProcessingNoop,
		},
		{
			name: "source window failure", operation: ProcessSourceWindowOperation,
			outcomes: []string{ScheduledProcessingFailure}, wantOutcome: ScheduledProcessingFailure,
		},
		{
			name: "source window cancellation", operation: ProcessSourceWindowOperation,
			outcomes: []string{ScheduledProcessingCancelled}, wantOutcome: ScheduledProcessingCancelled, wantErr: context.Canceled,
		},
		{
			name: "experience success", operation: IncubateExperienceOperation,
			outcomes: []string{ScheduledProcessingSuccess}, wantOutcome: ScheduledProcessingSuccess,
		},
		{
			name: "experience noop", operation: IncubateExperienceOperation,
			outcomes: []string{ScheduledProcessingNoop}, wantOutcome: ScheduledProcessingNoop,
		},
		{
			name: "experience failure", operation: IncubateExperienceOperation,
			outcomes: []string{ScheduledProcessingFailure}, wantOutcome: ScheduledProcessingFailure,
		},
		{
			name: "experience cancellation", operation: IncubateExperienceOperation,
			outcomes: []string{ScheduledProcessingCancelled}, wantOutcome: ScheduledProcessingCancelled, wantErr: context.Canceled,
		},
		{
			name: "failure outranks success", operation: ProcessSourceWindowOperation,
			outcomes: []string{ScheduledProcessingSuccess, ScheduledProcessingFailure}, wantOutcome: ScheduledProcessingFailure,
		},
		{
			name: "success outranks noop", operation: ProcessSourceWindowOperation,
			outcomes: []string{ScheduledProcessingNoop, ScheduledProcessingSuccess}, wantOutcome: ScheduledProcessingSuccess,
		},
		{
			name: "cancellation outranks failure", operation: IncubateExperienceOperation,
			outcomes: []string{ScheduledProcessingFailure, ScheduledProcessingCancelled}, wantOutcome: ScheduledProcessingCancelled, wantErr: context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracing := &recordingStageTracing{}
			lifecycle, err := NewConfigured(RuntimeOptions{Tracing: tracing}, nil)
			if err != nil {
				t.Fatal(err)
			}
			processor := &ScheduledProcessor{runtime: lifecycle, scopes: staticScopes(testScopes(test.outcomes))}
			index := 0
			err = processor.process(t.Context(), test.operation, func(context.Context, string) ScheduledObservation {
				outcome := test.outcomes[index]
				index++
				observation := ScheduledObservation{Operation: test.operation, Outcome: outcome}
				if outcome == ScheduledProcessingCancelled {
					observation.Err = context.Canceled
				}
				if outcome == ScheduledProcessingFailure {
					observation.Err = errors.New("scheduled failure")
				}
				return observation
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("process error = %v, want %v", err, test.wantErr)
			}

			tracing.mu.Lock()
			defer tracing.mu.Unlock()
			if len(tracing.backgrounds) != 1 {
				t.Fatalf("background roots = %#v, want one", tracing.backgrounds)
			}
			root := tracing.backgrounds[0]
			if root.name != test.operation || root.outcome != test.wantOutcome || root.err != test.wantErr || root.attributes != nil {
				t.Fatalf("background root = %#v", root)
			}
			wantStageNames := make([]string, 0, len(test.outcomes)*2)
			for range test.outcomes {
				wantStageNames = append(wantStageNames, "scope.context", "scope.lock")
			}
			stageNames := make([]string, len(tracing.stages))
			for index, stage := range tracing.stages {
				stageNames[index] = stage.name
				for _, value := range stage.attributes {
					if value == "private-scheduled-scope" {
						t.Fatal("scheduled trace leaked raw scope")
					}
				}
			}
			if !reflect.DeepEqual(stageNames, wantStageNames) {
				t.Fatalf("stage names = %v, want %v", stageNames, wantStageNames)
			}
		})
	}
}

func testScopes(outcomes []string) []string {
	values := make([]string, len(outcomes))
	for index := range outcomes {
		values[index] = "private-scheduled-scope"
	}
	return values
}

func TestScheduledSourceWindowNoopRecordsFlushStage(t *testing.T) {
	t.Parallel()
	tracing := &recordingStageTracing{}
	lifecycle, err := NewConfigured(RuntimeOptions{Tracing: tracing}, nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewMemoryApplicationWithFlush(
		lifecycle,
		func(string) (*memory.Service, error) { return nil, nil },
		func(string) (MemoryFlushBackend, error) { return scheduledNoopFlushBackend{}, nil },
		"memory",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	processor, err := NewScheduledProcessor(lifecycle, staticScopes{"private-scheduled-scope"}, application, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.ProcessSourceWindows(t.Context()); err != nil {
		t.Fatal(err)
	}
	tracing.mu.Lock()
	defer tracing.mu.Unlock()
	if len(tracing.backgrounds) != 1 || tracing.backgrounds[0].outcome != ScheduledProcessingNoop {
		t.Fatalf("backgrounds = %#v", tracing.backgrounds)
	}
	if len(tracing.stages) != 3 || tracing.stages[2].name != "memory.flush" ||
		!reflect.DeepEqual(tracing.stages[2].attributes, map[string]TraceAttribute{
			"powercontext.memory.flush.source_count": 0,
		}) {
		t.Fatalf("stages = %#v", tracing.stages)
	}
}

func TestScheduledProcessorRejectsUnregisteredScopeBeforeLeaseOrCallback(t *testing.T) {
	t.Parallel()
	tracing := &recordingStageTracing{}
	lifecycle, err := NewConfigured(RuntimeOptions{
		Tracing: tracing,
		ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
			return scope.Descriptor{}, false, nil
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var observations []ScheduledObservation
	processor := &ScheduledProcessor{
		runtime: lifecycle,
		scopes:  staticScopes{"missing-scope"},
		observe: func(_ context.Context, observation ScheduledObservation) {
			observations = append(observations, observation)
		},
	}
	called := false
	if err := processor.process(t.Context(), ProcessSourceWindowOperation, func(context.Context, string) ScheduledObservation {
		called = true
		return ScheduledObservation{Operation: ProcessSourceWindowOperation, Outcome: ScheduledProcessingSuccess}
	}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("unregistered Scope reached scheduled process callback")
	}
	if len(observations) != 1 || observations[0].Outcome != ScheduledProcessingFailure {
		t.Fatalf("observations = %#v, want one Scope admission failure", observations)
	}
	var missing *scope.NotFoundError
	if !errors.As(observations[0].Err, &missing) {
		t.Fatalf("observation error = %v, want Scope NotFoundError", observations[0].Err)
	}
	lifecycle.scopes.mu.Lock()
	cached, active := lifecycle.scopes.countsLocked()
	lifecycle.scopes.mu.Unlock()
	if cached != 0 || active != 0 {
		t.Fatalf("unregistered Scope acquired a lease: cached=%d active=%d", cached, active)
	}
	tracing.mu.Lock()
	defer tracing.mu.Unlock()
	if len(tracing.stages) != 1 || tracing.stages[0].name != "scope.context" {
		t.Fatalf("stages = %#v, want only Scope admission", tracing.stages)
	}
}

type scheduledNoopFlushBackend struct{}

func (scheduledNoopFlushBackend) ObserveWindow(context.Context, string, int64) (source.Cursor, source.Cursor, *int64, int64, []source.Value, error) {
	return source.NewCursor(0), source.NewCursor(0), nil, 0, nil, nil
}

func (scheduledNoopFlushBackend) ApplyWindow(context.Context, string, memory.WritePlan, source.Cursor, *int64) (*memory.Memory, error) {
	return nil, errors.New("ApplyWindow must not run for a noop Source window")
}
