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

	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/contextpack"
	"github.com/ob-labs/powercontext-go/internal/stats"
)

// ExperienceRecall is the narrow read surface Context preparation consumes.
// Persistence implementations remain free to use SQLite FTS, OceanBase
// FULLTEXT, or a deterministic fake without leaking those details here.
type ExperienceRecall interface {
	Search(context.Context, string, string, int) ([]experience.SearchHit, error)
}

type ExperienceRecallFunc func(context.Context, string, string, int) ([]experience.SearchHit, error)

func (f ExperienceRecallFunc) Search(
	ctx context.Context,
	scopeID, query string,
	limit int,
) ([]experience.SearchHit, error) {
	return f(ctx, scopeID, query, limit)
}

// RecallStatistics observes one final Context Pack after the per-scope gate is
// released but before lifecycle admission ends. Implementations are
// best-effort: persistence or estimation failures must be handled internally.
type RecallStatistics interface {
	ObservePreparedContext(context.Context, string, contextpack.Build)
}

type RecallStatisticsFunc func(context.Context, string, contextpack.Build)

func (f RecallStatisticsFunc) ObservePreparedContext(
	ctx context.Context,
	scopeID string,
	build contextpack.Build,
) {
	if f != nil {
		f(ctx, scopeID, build.Clone())
	}
}

// ContextApplication owns the composite preparation use case. Memory and
// Experience candidates are selected under one admitted per-scope gate, then
// the pure Builder owns final selection, trust labeling, and the byte budget.
type ContextApplication struct {
	runtime     *Runtime
	memory      *MemoryApplication
	experiences ExperienceRecall
	builder     contextpack.Builder
	recall      RecallStatistics
}

func NewContextApplication(
	runtime *Runtime,
	memoryApplication *MemoryApplication,
	experiences ExperienceRecall,
) (*ContextApplication, error) {
	return NewContextApplicationWithRecall(runtime, memoryApplication, experiences, nil)
}

func NewContextApplicationWithRecall(
	runtime *Runtime,
	memoryApplication *MemoryApplication,
	experiences ExperienceRecall,
	recall RecallStatistics,
) (*ContextApplication, error) {
	if runtime == nil {
		return nil, errors.New("runtime: Context application Runtime must not be nil")
	}
	if memoryApplication != nil && memoryApplication.runtime != runtime {
		return nil, errors.New("runtime: Context and Memory applications must share one Runtime")
	}
	return &ContextApplication{
		runtime: runtime, memory: memoryApplication, experiences: experiences, recall: recall,
	}, nil
}

func (a *ContextApplication) Prepare(
	ctx context.Context,
	scopeID string,
	request contextpack.Request,
) (contextpack.Prepared, error) {
	if err := request.Validate(); err != nil {
		return contextpack.Prepared{}, err
	}
	currentScopeID, err := ValidateScopeID(scopeID)
	if err != nil {
		return contextpack.Prepared{}, err
	}
	var build contextpack.Build
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		currentScope, resolveErr := a.runtime.resolveScope(ctx, currentScopeID)
		if resolveErr != nil {
			return resolveErr
		}
		scopeIDs := append([]string{currentScopeID}, currentScope.ContextReferences()...)
		for _, referencedScopeID := range scopeIDs[1:] {
			if _, referenceErr := a.runtime.resolveScope(ctx, referencedScopeID); referenceErr != nil {
				return referenceErr
			}
		}
		prepared, prepareErr := a.prepareScopes(ctx, scopeIDs, request)
		if prepareErr != nil {
			return prepareErr
		}
		build = prepared
		a.observePreparedContext(ctx, currentScopeID, build)
		return nil
	})
	return build.Context, err
}

func (a *ContextApplication) prepareScopes(
	ctx context.Context,
	scopeIDs []string,
	request contextpack.Request,
) (contextpack.Build, error) {
	memoryCandidates := make([]contextpack.MemoryCandidates, 0, len(scopeIDs))
	experienceCandidates := make([]contextpack.ExperienceCandidates, 0, len(scopeIDs))
	for _, scopeID := range scopeIDs {
		memoryValues, experienceValues, err := a.recallScope(ctx, scopeID, request)
		if err != nil {
			return contextpack.Build{}, err
		}
		memoryCandidates = append(memoryCandidates, memoryValues)
		experienceCandidates = append(experienceCandidates, experienceValues)
	}
	memoryCandidates = limitMemoryCandidates(memoryCandidates, contextpack.MemoryCandidateLimit)
	experienceCandidates = limitExperienceCandidates(experienceCandidates, contextpack.ExperienceCandidateLimit)
	return a.buildContext(ctx, scopeIDs[0], request, memoryCandidates, experienceCandidates)
}

func (a *ContextApplication) recallScope(
	ctx context.Context,
	scopeID string,
	request contextpack.Request,
) (contextpack.MemoryCandidates, contextpack.ExperienceCandidates, error) {
	lease, releaseLease := a.runtime.scopes.lease(scopeID)
	defer releaseLease()
	var release func()
	lockErr := a.runtime.runStage(ctx, "scope.lock", map[string]TraceAttribute{
		"powercontext.scope.lock.contended": lease.contended(),
	}, func(stageContext context.Context, _ StageSpan) error {
		var acquireErr error
		release, acquireErr = lease.acquire(stageContext)
		return acquireErr
	})
	if lockErr != nil {
		return contextpack.MemoryCandidates{}, contextpack.ExperienceCandidates{}, lockErr
	}
	defer release()
	ctx = a.runtime.withModelUsage(ctx, scopeID, "", stats.MemoryRecall)
	return a.recallScopeLocked(ctx, scopeID, request)
}

func (a *ContextApplication) recallScopeLocked(
	ctx context.Context,
	scopeID string,
	request contextpack.Request,
) (contextpack.MemoryCandidates, contextpack.ExperienceCandidates, error) {
	resultMemory := contextpack.MemoryCandidates{ScopeID: scopeID}
	resultExperience := contextpack.ExperienceCandidates{ScopeID: scopeID}
	if a.memory != nil {
		memoryPage, memoryErr := a.memory.search(
			ctx, scopeID, request.Query(), contextpack.MemoryCandidateLimit, memory.SearchAuto,
		)
		if memoryErr != nil {
			return contextpack.MemoryCandidates{}, contextpack.ExperienceCandidates{}, memoryErr
		}
		resultMemory.MemoryRef = memoryPage.MemoryRef
		resultMemory.Hits = memoryPage.Hits
	}
	experienceErr := a.runtime.runStage(ctx, "experience.search", map[string]TraceAttribute{
		"powercontext.experience.search.configured": a.experiences != nil,
		"powercontext.experience.search.limit":      contextpack.ExperienceCandidateLimit,
	}, func(stageContext context.Context, span StageSpan) error {
		if a.experiences != nil {
			values, searchErr := a.experiences.Search(
				stageContext, scopeID, request.Query(), contextpack.ExperienceCandidateLimit,
			)
			if searchErr != nil {
				return searchErr
			}
			resultExperience.Hits = values
		}
		setStageAttributes(span, map[string]TraceAttribute{
			"powercontext.experience.search.result_count": len(resultExperience.Hits),
		})
		return nil
	})
	if experienceErr != nil {
		return contextpack.MemoryCandidates{}, contextpack.ExperienceCandidates{}, experienceErr
	}
	return resultMemory, resultExperience, nil
}

func (a *ContextApplication) buildContext(
	ctx context.Context,
	currentScopeID string,
	request contextpack.Request,
	memoryCandidates []contextpack.MemoryCandidates,
	experienceCandidates []contextpack.ExperienceCandidates,
) (contextpack.Build, error) {
	memoryCount := contextpackMemoryCandidateCount(memoryCandidates)
	experienceCount := contextpackExperienceCandidateCount(experienceCandidates)
	var build contextpack.Build
	err := a.runtime.runStage(ctx, "context.build", map[string]TraceAttribute{
		"powercontext.context.build.memory_candidate_count":     memoryCount,
		"powercontext.context.build.experience_candidate_count": experienceCount,
	}, func(_ context.Context, span StageSpan) error {
		var buildErr error
		build, buildErr = a.builder.BuildScopesResult(request, currentScopeID, memoryCandidates, experienceCandidates)
		if buildErr == nil {
			setStageAttributes(span, map[string]TraceAttribute{
				"powercontext.context.build.selected_count": len(build.Origins),
				"powercontext.context.build.status":         string(build.Context.Status()),
				"powercontext.context.build.content_bytes":  build.Context.ContentBytes(),
			})
		}
		return buildErr
	})
	return build, err
}

func limitMemoryCandidates(values []contextpack.MemoryCandidates, limit int) []contextpack.MemoryCandidates {
	counts := roundRobinCandidateCounts(memoryCandidateLengths(values), limit)
	result := make([]contextpack.MemoryCandidates, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Hits = value.Hits[:counts[index]]
	}
	return result
}

func limitExperienceCandidates(values []contextpack.ExperienceCandidates, limit int) []contextpack.ExperienceCandidates {
	counts := roundRobinCandidateCounts(experienceCandidateLengths(values), limit)
	result := make([]contextpack.ExperienceCandidates, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Hits = value.Hits[:counts[index]]
	}
	return result
}

func memoryCandidateLengths(values []contextpack.MemoryCandidates) []int {
	result := make([]int, len(values))
	for index, value := range values {
		result[index] = len(value.Hits)
	}
	return result
}

func experienceCandidateLengths(values []contextpack.ExperienceCandidates) []int {
	result := make([]int, len(values))
	for index, value := range values {
		result[index] = len(value.Hits)
	}
	return result
}

func roundRobinCandidateCounts(lengths []int, limit int) []int {
	counts := make([]int, len(lengths))
	for remaining := limit; remaining > 0; {
		advanced := false
		for index, length := range lengths {
			if counts[index] >= length {
				continue
			}
			counts[index]++
			remaining--
			advanced = true
			if remaining == 0 {
				break
			}
		}
		if !advanced {
			return counts
		}
	}
	return counts
}

func contextpackMemoryCandidateCount(values []contextpack.MemoryCandidates) int {
	result := 0
	for _, value := range values {
		result += len(value.Hits)
	}
	return result
}

func contextpackExperienceCandidateCount(values []contextpack.ExperienceCandidates) int {
	result := 0
	for _, value := range values {
		result += len(value.Hits)
	}
	return result
}

func (a *ContextApplication) observePreparedContext(
	ctx context.Context,
	scope string,
	build contextpack.Build,
) {
	if a.recall == nil {
		return
	}
	defer func() { _ = recover() }()
	a.recall.ObservePreparedContext(ctx, scope, build)
}
