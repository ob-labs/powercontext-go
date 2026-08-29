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
	scope, err := ValidateScopeID(scopeID)
	if err != nil {
		return contextpack.Prepared{}, err
	}
	var build contextpack.Build
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		lease, releaseLease := a.runtime.scopes.lease(scope)
		defer releaseLease()
		if resolveErr := a.runtime.resolveScope(ctx); resolveErr != nil {
			return resolveErr
		}
		var release func()
		stageErr := a.runtime.runStage(ctx, "scope.lock", map[string]TraceAttribute{
			"powercontext.scope.lock.contended": lease.contended(),
		}, func(stageContext context.Context, _ StageSpan) error {
			var acquireErr error
			release, acquireErr = lease.acquire(stageContext)
			return acquireErr
		})
		if stageErr != nil {
			return stageErr
		}
		ctx = a.runtime.withModelUsage(ctx, scope, "", stats.MemoryRecall)
		build, err = func() (contextpack.Build, error) {
			defer release()
			return a.prepareLocked(ctx, scope, request)
		}()
		if err != nil {
			return err
		}
		a.observePreparedContext(ctx, scope, build)
		return nil
	})
	return build.Context, err
}

func (a *ContextApplication) prepareLocked(
	ctx context.Context,
	scope string,
	request contextpack.Request,
) (contextpack.Build, error) {
	var memoryPage MemorySearchPage
	var err error
	if a.memory != nil {
		memoryPage, err = a.memory.search(
			ctx, scope, request.Query(), contextpack.MemoryCandidateLimit, memory.SearchAuto,
		)
		if err != nil {
			return contextpack.Build{}, err
		}
	}
	experienceHits := []experience.SearchHit{}
	err = a.runtime.runStage(ctx, "experience.search", map[string]TraceAttribute{
		"powercontext.experience.search.configured": a.experiences != nil,
		"powercontext.experience.search.limit":      contextpack.ExperienceCandidateLimit,
	}, func(stageContext context.Context, span StageSpan) error {
		if a.experiences != nil {
			var searchErr error
			experienceHits, searchErr = a.experiences.Search(
				stageContext, scope, request.Query(), contextpack.ExperienceCandidateLimit,
			)
			if searchErr != nil {
				return searchErr
			}
		}
		setStageAttributes(span, map[string]TraceAttribute{
			"powercontext.experience.search.result_count": len(experienceHits),
		})
		return nil
	})
	if err != nil {
		return contextpack.Build{}, err
	}
	var build contextpack.Build
	err = a.runtime.runStage(ctx, "context.build", map[string]TraceAttribute{
		"powercontext.context.build.memory_candidate_count":     len(memoryPage.Hits),
		"powercontext.context.build.experience_candidate_count": len(experienceHits),
	}, func(_ context.Context, span StageSpan) error {
		var buildErr error
		build, buildErr = a.builder.BuildResult(request, memoryPage.MemoryRef, memoryPage.Hits, experienceHits)
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
