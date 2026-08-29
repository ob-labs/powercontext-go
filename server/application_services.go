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

package server

import (
	"context"
	"errors"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/review"
	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scheduler"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

type applicationServices struct {
	sources        *pcruntime.SourceApplication
	memory         *pcruntime.MemoryApplication
	context        *pcruntime.ContextApplication
	review         *pcruntime.ReviewApplication
	generation     *pcruntime.GenerationApplication
	externalSkills *pcruntime.ExternalSkillApplication
	handoff        *pcruntime.HandoffApplication
	work           *pcruntime.WorkApplication
	handoffReport  *pcruntime.HandoffReportApplication
	statistics     *pcruntime.StatisticsApplication
}

func buildApplicationServices(
	ctx context.Context,
	config ProcessConfig,
	dependencies Dependencies,
	foundation applicationFoundation,
	repositories applicationRepositories,
) (applicationServices, error) {
	database := foundation.storage.database
	lifecycle := foundation.lifecycle
	assembled := foundation.assembled

	sourceBackend, err := sqlstore.NewRuntimeSourceBackend(database, repositories.sources)
	if err != nil {
		return applicationServices{}, err
	}
	sourceApplication, err := pcruntime.NewSourceApplication(lifecycle, sourceBackend)
	if err != nil {
		return applicationServices{}, err
	}

	idFactory := dependencies.IDFactory
	if idFactory == nil {
		idFactory = scopedIDFactory
	}
	memoryReranker := pcruntime.TraceMemoryReranker(lifecycle, assembled.memoryReranker)
	memoryFactory := func(scopeID string) (*memory.Service, error) {
		repository, buildErr := sqlstore.NewMemoryRepository(
			database, scopeID, repositories.artifacts, repositories.memoryIndex,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		resolver, buildErr := sqlstore.NewMemorySourceResolver(database, scopeID, repositories.sources)
		if buildErr != nil {
			return nil, buildErr
		}
		return memory.NewService(repository, memory.ServiceOptions{
			CandidatePipeline:    assembled.memoryCandidates,
			EmbeddingModel:       assembled.embeddingModel,
			Reranker:             memoryReranker,
			RerankCandidateLimit: config.Runtime.MemoryRerankCandidateLimit,
			SourceResolver:       resolver, IDFactory: idFactory, Clock: dependencies.Clock,
		})
	}
	flushFactory := func(scopeID string) (pcruntime.MemoryFlushBackend, error) {
		repository, buildErr := sqlstore.NewMemoryRepository(
			database, scopeID, repositories.artifacts, repositories.memoryIndex,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		return sqlstore.NewMemoryFlushStore(database, scopeID, repositories.sources, repository)
	}
	memoryApplication, err := pcruntime.NewMemoryApplicationWithFlush(
		lifecycle, memoryFactory, flushFactory,
		pcruntime.DefaultMemoryArtifactID, config.Runtime.SourceWindowLimit,
	)
	if err != nil {
		return applicationServices{}, err
	}
	experienceRecall := pcruntime.ExperienceRecallFunc(func(
		ctx context.Context, scopeID, query string, limit int,
	) ([]experience.SearchHit, error) {
		var result []experience.SearchHit
		transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
			var searchErr error
			result, searchErr = repositories.experienceIndex.Search(ctx, tx, scopeID, query, limit)
			return searchErr
		})
		return result, transactionErr
	})
	tokenEstimator := inference.CharacterTokenEstimator()
	contextApplication, err := pcruntime.NewContextApplicationWithRecall(
		lifecycle, memoryApplication, experienceRecall,
		relationalRecallStatistics{
			database: database, sources: repositories.sources, artifacts: repositories.artifacts,
			repository: foundation.statisticsRepository, estimator: tokenEstimator,
			clock: foundation.statisticsClock, logger: dependencies.Logger,
		},
	)
	if err != nil {
		return applicationServices{}, err
	}

	reviewFactory := func(scopeID string) (*review.Service, error) {
		backend, buildErr := sqlstore.NewReviewBackend(
			database, scopeID, repositories.candidates, repositories.artifacts,
			repositories.sources, repositories.experienceIndex,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		return review.NewService(backend, idFactory)
	}
	reviewApplication, err := pcruntime.NewReviewApplication(lifecycle, reviewFactory)
	if err != nil {
		return applicationServices{}, err
	}
	generationFactory := func(scopeID string) (*review.GenerationService, error) {
		service, buildErr := reviewFactory(scopeID)
		if buildErr != nil {
			return nil, buildErr
		}
		evidence, buildErr := sqlstore.NewGenerationEvidenceReader(
			database, scopeID, repositories.sources, repositories.artifacts,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		return review.NewGenerationService(
			evidence, service, assembled.experienceGenerator, assembled.skillGenerator,
		)
	}
	generationApplication, err := pcruntime.NewGenerationApplication(lifecycle, generationFactory)
	if err != nil {
		return applicationServices{}, err
	}
	var experienceIncubationApplication *pcruntime.ExperienceIncubationApplication
	if assembled.experienceCandidates != nil {
		experienceIncubationApplication, err = pcruntime.NewExperienceIncubationApplication(
			lifecycle,
			func(scopeID string) (pcruntime.ExperienceIncubationBackend, error) {
				return sqlstore.NewExperienceIncubationStore(
					database, scopeID, repositories.sources, repositories.candidates,
				)
			},
			assembled.experienceCandidates,
			idFactory,
		)
		if err != nil {
			return applicationServices{}, err
		}
	}

	var externalApplication *pcruntime.ExternalSkillApplication
	if assembled.externalSkills != nil {
		snapshots, buildErr := sqlstore.NewExternalSkillSnapshotStore(database, repositories.sources)
		if buildErr != nil {
			return applicationServices{}, buildErr
		}
		externalApplication, err = pcruntime.NewExternalSkillApplication(
			lifecycle,
			func(scopeID string) (*skill.RegistryService, error) {
				store, buildErr := sqlstore.NewExternalSkillStore(database, scopeID)
				if buildErr != nil {
					return nil, buildErr
				}
				return skill.NewRegistryService(store, assembled.externalSkills)
			},
			generationFactory, snapshots,
		)
		if err != nil {
			return applicationServices{}, err
		}
	}

	handoffFactory := func(scopeID string) (*handoff.Service, error) {
		memoryService, buildErr := memoryFactory(scopeID)
		if buildErr != nil {
			return nil, buildErr
		}
		backend, buildErr := sqlstore.NewHandoffBackend(database, scopeID, repositories.artifacts)
		if buildErr != nil {
			return nil, buildErr
		}
		resolver, buildErr := sqlstore.NewHandoffEvidenceResolver(
			database, scopeID, repositories.sources, repositories.artifacts, memoryService,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		return handoff.NewService(
			scopeID, pcruntime.DefaultHandoffArtifactID, backend, resolver, assembled.handoffGenerator,
		)
	}
	activationStore, err := sqlstore.NewHandoffActivationStore(database, repositories.sources)
	if err != nil {
		return applicationServices{}, err
	}
	if attachErr := attachApplicationScheduler(
		ctx, config, dependencies, lifecycle, sourceBackend,
		memoryApplication, experienceIncubationApplication,
	); attachErr != nil {
		return applicationServices{}, attachErr
	}
	handoffApplication, err := pcruntime.NewHandoffApplication(lifecycle, handoffFactory, activationStore)
	if err != nil {
		return applicationServices{}, err
	}
	workApplication, err := pcruntime.NewWorkApplication(lifecycle, sourceBackend, handoffFactory)
	if err != nil {
		return applicationServices{}, err
	}

	var handoffReportApplication *pcruntime.HandoffReportApplication
	if config.HandoffReport.Enabled {
		reportStore, buildErr := sqlstore.NewHandoffReportStore(database, foundation.storage.dialect)
		if buildErr != nil {
			return applicationServices{}, buildErr
		}
		if buildErr = reportStore.EnsureSchema(ctx); buildErr != nil {
			return applicationServices{}, buildErr
		}
		reader, buildErr := pcruntime.NewHandoffReportReader(handoffFactory)
		if buildErr != nil {
			return applicationServices{}, buildErr
		}
		handoffReportApplication, err = pcruntime.NewHandoffReportApplication(
			lifecycle, reportStore, reader, workApplication, dependencies.Clock, nil,
			func(ctx context.Context) ([]string, error) { return sqlstore.HandoffScopeIDs(ctx, database) },
		)
		if err != nil {
			return applicationServices{}, err
		}
	}

	tokenEstimatorProfile := tokenEstimator.Profile()
	statisticsApplication, err := pcruntime.NewStatisticsApplication(
		lifecycle,
		func(scopeID string) (pcruntime.StatisticsReader, error) {
			return sqlstore.NewScopedStatistics(
				database, scopeID, pcruntime.DefaultMemoryArtifactID,
				repositories.artifacts, foundation.statisticsRepository, &tokenEstimatorProfile,
			)
		},
		foundation.statisticsClock,
	)
	if err != nil {
		return applicationServices{}, err
	}
	return applicationServices{
		sources: sourceApplication, memory: memoryApplication, context: contextApplication,
		review: reviewApplication, generation: generationApplication, externalSkills: externalApplication,
		handoff: handoffApplication, work: workApplication, handoffReport: handoffReportApplication,
		statistics: statisticsApplication,
	}, nil
}

func attachApplicationScheduler(
	ctx context.Context,
	config ProcessConfig,
	dependencies Dependencies,
	lifecycle *pcruntime.Runtime,
	sourceBackend *sqlstore.RuntimeSourceBackend,
	memoryApplication *pcruntime.MemoryApplication,
	experienceIncubationApplication *pcruntime.ExperienceIncubationApplication,
) error {
	if config.Runtime.SourceWindowInterval == nil && config.Runtime.ExperienceIncubationInterval == nil {
		return nil
	}
	if config.Runtime.ExperienceIncubationInterval != nil && experienceIncubationApplication == nil {
		return errors.New("server: scheduled Experience incubation pipeline is not configured")
	}
	backgroundLogger := namedLogger(dependencies.Logger, "powercontext.builtin.runtime.application")
	processor, err := pcruntime.NewScheduledProcessor(
		lifecycle, sourceBackend, memoryApplication, experienceIncubationApplication,
		scheduledObserver(backgroundLogger), dependencies.Clock,
	)
	if err != nil {
		return err
	}
	scheduled, err := scheduler.Open(ctx, scheduler.Config{
		Path:                         config.SchedulerPath,
		SourceWindowInterval:         config.Runtime.SourceWindowInterval,
		ExperienceIncubationInterval: config.Runtime.ExperienceIncubationInterval,
		SourceWindow:                 processor.ProcessSourceWindows,
		ExperienceIncubation:         processor.IncubateExperiences,
		OnError:                      scheduledRunErrorObserver(backgroundLogger), Clock: dependencies.Clock,
	})
	if err != nil {
		return err
	}
	if err := lifecycle.AttachScheduler(scheduled); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		closeErr := scheduled.Close(closeCtx)
		cancel()
		return errors.Join(err, closeErr)
	}
	return nil
}
