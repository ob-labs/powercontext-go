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
	"fmt"

	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/stats"
	"github.com/ob-labs/powercontext-go/source"
)

// ExperienceIncubationBackend is the use-case-shaped two-transaction port.
// ObserveWindow commits before model inference; ApplyWindow atomically creates
// every Candidate and advances the independent Source cursor.
type ExperienceIncubationBackend interface {
	ObserveWindow(context.Context, string, int64) (
		previous source.Cursor,
		next source.Cursor,
		generation *int64,
		highWatermark int64,
		values []source.Value,
		available []source.Ref,
		err error,
	)
	ApplyWindow(
		context.Context,
		string,
		[]string,
		[]experience.CandidateInput,
		source.Cursor,
		*int64,
	) error
}

type ExperienceIncubationBackendFactory func(string) (ExperienceIncubationBackend, error)

type ExperienceIncubationResult struct {
	PreviousCursor       int64
	HighWatermark        int64
	CurrentCursor        int64
	ProcessedSourceCount int
	CandidateCount       int
}

func (r ExperienceIncubationResult) Processed() bool { return r.CurrentCursor > r.PreviousCursor }

type ExperienceIncubationApplication struct {
	runtime  *Runtime
	backends ExperienceIncubationBackendFactory
	pipeline experience.CandidatePipeline
	ids      review.IDFactory
}

func NewExperienceIncubationApplication(
	runtime *Runtime,
	backends ExperienceIncubationBackendFactory,
	pipeline experience.CandidatePipeline,
	idFactory review.IDFactory,
) (*ExperienceIncubationApplication, error) {
	if runtime == nil || backends == nil || pipeline == nil || idFactory == nil {
		return nil, errors.New("runtime: Experience incubation dependencies must not be nil")
	}
	return &ExperienceIncubationApplication{
		runtime: runtime, backends: backends, pipeline: pipeline, ids: idFactory,
	}, nil
}

func (a *ExperienceIncubationApplication) Incubate(
	ctx context.Context,
	scopeID string,
	limit int64,
) (ExperienceIncubationResult, error) {
	if limit < 1 {
		return ExperienceIncubationResult{}, fmt.Errorf("runtime: Experience incubation limit must be positive")
	}
	var result ExperienceIncubationResult
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		var operationErr error
		result, operationErr = a.incubate(ctx, scope, limit)
		return operationErr
	})
	return result, err
}

// incubate performs an already-admitted, already-serialized window.
func (a *ExperienceIncubationApplication) incubate(
	ctx context.Context,
	scope string,
	limit int64,
) (result ExperienceIncubationResult, err error) {
	err = a.runtime.runStage(ctx, "experience.incubation", nil, func(stageCtx context.Context, span StageSpan) error {
		var operationErr error
		result, operationErr = a.incubateWindow(stageCtx, scope, limit)
		setStageAttributes(span, map[string]TraceAttribute{
			"powercontext.experience.incubation.source_count":    result.ProcessedSourceCount,
			"powercontext.experience.incubation.candidate_count": result.CandidateCount,
		})
		return operationErr
	})
	return result, err
}

func (a *ExperienceIncubationApplication) incubateWindow(
	ctx context.Context,
	scope string,
	limit int64,
) (ExperienceIncubationResult, error) {
	ctx = a.runtime.withModelUsage(ctx, scope, stats.ExperienceGeneration, "")
	backend, err := a.backends(scope)
	if err != nil {
		return ExperienceIncubationResult{}, err
	}
	if backend == nil {
		return ExperienceIncubationResult{}, &StateError{Code: "experience-incubation"}
	}
	previous, next, generation, highWatermark, values, available, err := backend.ObserveWindow(
		ctx, experience.IncubationCursorName, limit,
	)
	if err != nil {
		return ExperienceIncubationResult{}, err
	}
	result := ExperienceIncubationResult{
		PreviousCursor: previous.Sequence(), HighWatermark: highWatermark,
		CurrentCursor: next.Sequence(), ProcessedSourceCount: len(values),
	}
	if previous.Sequence() == next.Sequence() {
		return result, nil
	}

	plans, err := a.pipeline.Incubate(ctx, values)
	if err != nil {
		return result, err
	}
	if err := validateIncubationSources(plans, available); err != nil {
		return result, err
	}
	candidateIDs := make([]string, len(plans))
	for index := range plans {
		candidateID, idErr := a.ids("candidate")
		if idErr != nil {
			return result, idErr
		}
		candidateIDs[index] = candidateID
	}
	if err := backend.ApplyWindow(
		ctx, experience.IncubationCursorName, candidateIDs, plans, next, generation,
	); err != nil {
		return result, err
	}
	result.CandidateCount = len(plans)
	return result, nil
}

func validateIncubationSources(plans []experience.CandidateInput, available []source.Ref) error {
	window := make(map[source.Ref]struct{}, len(available))
	for _, ref := range available {
		window[ref] = struct{}{}
	}
	for _, plan := range plans {
		for _, ref := range plan.Sources() {
			if _, ok := window[ref]; !ok {
				return inference.NewInvalidOutputError(
					"experience-incubate",
					"pipeline cited a Source outside the current incubation window",
				)
			}
		}
	}
	return nil
}
