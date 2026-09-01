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
	"time"

	experienceartifact "github.com/ob-labs/powercontext-go/artifact/experience"
)

const (
	ProcessSourceWindowOperation = "process_source_window"
	IncubateExperienceOperation  = "incubate_experience_candidates"
	ScheduledProcessingSuccess   = "success"
	ScheduledProcessingNoop      = "noop"
	ScheduledProcessingFailure   = "failure"
	ScheduledProcessingCancelled = "cancelled"
)

type ScheduledScopeLister interface {
	ScopeIDs(context.Context) ([]string, error)
}

type ScheduledObservation struct {
	Operation      string
	Outcome        string
	Duration       time.Duration
	SourceCount    int
	CandidateCount *int
	Err            error
}

type ScheduledObserver func(context.Context, ScheduledObservation)

// ScheduledProcessor serializes both persisted jobs globally, then visits
// Source-backed Scopes deterministically. Each Scope keeps its normal write
// gate, while one failure is observed and isolated from later Scopes.
type ScheduledProcessor struct {
	runtime    *Runtime
	scopes     ScheduledScopeLister
	memory     *MemoryApplication
	experience *ExperienceIncubationApplication
	observe    ScheduledObserver
	clock      func() time.Time
}

func NewScheduledProcessor(
	runtime *Runtime,
	scopes ScheduledScopeLister,
	memory *MemoryApplication,
	experience *ExperienceIncubationApplication,
	observer ScheduledObserver,
	clock func() time.Time,
) (*ScheduledProcessor, error) {
	if runtime == nil || scopes == nil || (memory == nil && experience == nil) {
		return nil, errors.New("runtime: scheduled processor dependencies must not be nil")
	}
	if clock == nil {
		clock = time.Now
	}
	return &ScheduledProcessor{
		runtime: runtime, scopes: scopes, memory: memory, experience: experience,
		observe: observer, clock: clock,
	}, nil
}

func (p *ScheduledProcessor) ProcessSourceWindows(ctx context.Context) error {
	if p.memory == nil {
		return &StateError{Code: "memory-flush"}
	}
	return p.process(ctx, ProcessSourceWindowOperation, func(ctx context.Context, scope string) ScheduledObservation {
		started := p.clock()
		result, err := p.memory.flush(ctx, scope)
		observation := ScheduledObservation{
			Operation: ProcessSourceWindowOperation, Duration: elapsed(p.clock(), started),
			SourceCount: result.ProcessedSourceCount, Err: err,
		}
		observation.Outcome = scheduledOutcome(result.Processed(), err)
		return observation
	})
}

func (p *ScheduledProcessor) IncubateExperiences(ctx context.Context) error {
	if p.experience == nil {
		return &StateError{Code: "experience-incubation"}
	}
	return p.process(ctx, IncubateExperienceOperation, func(ctx context.Context, scope string) ScheduledObservation {
		started := p.clock()
		result, err := p.experience.incubate(ctx, scope, experienceIncubationWindowLimit)
		candidateCount := result.CandidateCount
		observation := ScheduledObservation{
			Operation: IncubateExperienceOperation, Duration: elapsed(p.clock(), started),
			SourceCount: result.ProcessedSourceCount, CandidateCount: &candidateCount, Err: err,
		}
		observation.Outcome = scheduledOutcome(result.Processed(), err)
		return observation
	})
}

const experienceIncubationWindowLimit = int64(experienceartifact.IncubationWindowLimit)

func (p *ScheduledProcessor) process(
	ctx context.Context,
	operation string,
	processScope func(context.Context, string) ScheduledObservation,
) error {
	return p.runtime.BackgroundOperation(ctx, operation, func(ctx context.Context) (string, error) {
		outcome := ScheduledProcessingNoop
		scopes, err := p.scopes.ScopeIDs(ctx)
		if err != nil {
			return ScheduledProcessingFailure, err
		}
		for _, rawScope := range scopes {
			if p.runtime.isClosing() {
				break
			}
			scope, err := ValidateScopeID(rawScope)
			if err != nil {
				observation := ScheduledObservation{Operation: operation, Outcome: ScheduledProcessingFailure, Err: err}
				p.notify(ctx, observation)
				outcome = scheduledAggregateOutcome(outcome, observation.Outcome)
				continue
			}
			lease, releaseLease := p.runtime.scopes.lease(scope)
			if resolveErr := p.runtime.resolveScope(ctx); resolveErr != nil {
				releaseLease()
				observation := ScheduledObservation{Operation: operation, Outcome: ScheduledProcessingFailure, Err: resolveErr}
				p.notify(ctx, observation)
				outcome = scheduledAggregateOutcome(outcome, observation.Outcome)
				continue
			}
			var release func()
			err = p.runtime.runStage(ctx, "scope.lock", map[string]TraceAttribute{
				"powercontext.scope.lock.contended": lease.contended(),
			}, func(stageContext context.Context, _ StageSpan) error {
				var acquireErr error
				release, acquireErr = lease.acquire(stageContext)
				return acquireErr
			})
			if err != nil {
				releaseLease()
				lockOutcome := ScheduledProcessingFailure
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					lockOutcome = ScheduledProcessingCancelled
				}
				observation := ScheduledObservation{Operation: operation, Outcome: lockOutcome, Err: err}
				p.notify(ctx, observation)
				return observation.Outcome, err
			}
			observation := processScope(ctx, scope)
			release()
			releaseLease()
			if observation.Outcome == ScheduledProcessingCancelled {
				if observation.Err == nil {
					observation.Err = context.Canceled
				}
				p.notify(ctx, observation)
				outcome = scheduledAggregateOutcome(outcome, observation.Outcome)
				return outcome, observation.Err
			}
			p.notify(ctx, observation)
			outcome = scheduledAggregateOutcome(outcome, observation.Outcome)
		}
		return outcome, nil
	})
}

func (p *ScheduledProcessor) notify(ctx context.Context, observation ScheduledObservation) {
	if p.observe != nil {
		p.observe(ctx, observation)
	}
}

func (r *Runtime) isClosing() bool {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.closing || r.closed
}

func scheduledOutcome(processed bool, err error) string {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ScheduledProcessingCancelled
		}
		return ScheduledProcessingFailure
	}
	if processed {
		return ScheduledProcessingSuccess
	}
	return ScheduledProcessingNoop
}

func scheduledAggregateOutcome(current, next string) string {
	if scheduledOutcomePriority(next) > scheduledOutcomePriority(current) {
		return next
	}
	return current
}

func scheduledOutcomePriority(outcome string) int {
	switch outcome {
	case ScheduledProcessingCancelled:
		return 4
	case ScheduledProcessingSuccess:
		return 2
	case ScheduledProcessingNoop:
		return 1
	default:
		// Failure is the conservative default for an unknown observation.
		return 3
	}
}

func elapsed(now, started time.Time) time.Duration {
	if duration := now.Sub(started); duration > 0 {
		return duration
	}
	return 0
}
