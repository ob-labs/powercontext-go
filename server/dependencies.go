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
	"log/slog"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/trace"

	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/modelprovider"
	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
)

// Dependencies are deliberate extension points for deterministic tests and
// custom deployments. Nil values are assembled from InferenceConfig where
// possible; they are not service locators and do not own lifecycle.
type Dependencies struct {
	MemoryCandidates     memory.CandidatePipeline
	ExperienceCandidates experience.CandidatePipeline
	ExperienceGenerator  experience.Generator
	SkillGenerator       skill.Generator
	HandoffGenerator     handoff.GenerationPipeline
	EmbeddingModel       inference.EmbeddingModel
	MemoryReranker       memory.Reranker
	ExternalSkills       skill.ExternalProvider
	HTTPClient           *http.Client
	Clock                func() time.Time
	IDFactory            func(string) (string, error)
	Logger               *slog.Logger
	TracerProvider       trace.TracerProvider
}

// assembledDependencies is the concrete provider surface consumed by the
// composition root. Resources are owned only after Runtime construction.
type assembledDependencies struct {
	memoryCandidates     memory.CandidatePipeline
	experienceCandidates experience.CandidatePipeline
	experienceGenerator  experience.Generator
	skillGenerator       skill.Generator
	handoffGenerator     handoff.GenerationPipeline
	embeddingModel       inference.EmbeddingModel
	memoryReranker       memory.Reranker
	externalSkills       skill.ExternalProvider
	agentSkillTargets    []skill.AgentSkillTarget
	generationReadiness  pcruntime.DependencyOperation
	embeddingReadiness   pcruntime.DependencyOperation
	resources            []pcruntime.Resource
}

type assembledProviderFactory interface {
	TextModel(string) (inference.TextModel, error)
	EmbeddingTransport(string) (inference.EmbeddingTransport, error)
}

type providerFactoryBuilder func(*http.Client) (assembledProviderFactory, error)

func assembleDependencies(
	config ProcessConfig,
	supplied Dependencies,
	tracerProvider trace.TracerProvider,
) (assembledDependencies, error) {
	return assembleDependenciesWithProviderFactory(
		config, supplied, tracerProvider,
		func(client *http.Client) (assembledProviderFactory, error) { return providerFactory(client) },
	)
}

func assembleDependenciesWithProviderFactory(
	config ProcessConfig,
	supplied Dependencies,
	tracerProvider trace.TracerProvider,
	newProviderFactory providerFactoryBuilder,
) (assembledDependencies, error) {
	result := assembledDependencies{
		memoryCandidates: supplied.MemoryCandidates, experienceCandidates: supplied.ExperienceCandidates,
		experienceGenerator: supplied.ExperienceGenerator,
		skillGenerator:      supplied.SkillGenerator, handoffGenerator: supplied.HandoffGenerator,
		embeddingModel: supplied.EmbeddingModel, memoryReranker: supplied.MemoryReranker,
		externalSkills: supplied.ExternalSkills,
	}
	configuredTargets, err := agentSkillTargets(config.ExternalSkills)
	if err != nil {
		return result, err
	}
	result.agentSkillTargets = configuredTargets
	needsGenerated := result.memoryCandidates == nil || result.experienceCandidates == nil || result.experienceGenerator == nil ||
		result.skillGenerator == nil || result.handoffGenerator == nil ||
		(config.Runtime.MemoryRerankEnabled && result.memoryReranker == nil)
	if config.Inference.GenerationModel != "" && needsGenerated {
		factory, err := newProviderFactory(supplied.HTTPClient)
		if err != nil {
			return result, err
		}
		providerModel, err := factory.TextModel(config.Inference.GenerationModel)
		if err != nil {
			return result, err
		}
		result.generationReadiness = func(ctx context.Context) error {
			return inference.ProbeTextModel(ctx, providerModel)
		}
		model := inference.TraceTextModel(providerModel, tracerProvider)
		limits, err := inference.NewLimits(config.Inference.GenerationTimeout, config.Inference.GenerationMaxRequests)
		if err != nil {
			return result, err
		}
		settings := inference.GenerationSettings{}
		if result.memoryCandidates == nil {
			generator, err := memory.NewExtractionPromptedGenerator(model, config.Runtime.MemoryExtractionProfile, &limits, settings)
			if err != nil {
				return result, err
			}
			result.memoryCandidates, err = memory.NewLLMCandidatePipeline(
				pcruntime.ReportStructuredUsage(generator), memory.NewContentEvidenceProjector(nil),
			)
			if err != nil {
				return result, err
			}
		}
		if result.experienceGenerator == nil {
			generator, err := experience.NewPromptedGenerator(model, &limits, settings)
			if err != nil {
				return result, err
			}
			result.experienceGenerator, err = experience.NewLLMGenerator(pcruntime.ReportStructuredUsage(generator))
			if err != nil {
				return result, err
			}
		}
		if result.experienceCandidates == nil {
			generator, err := experience.NewIncubationPromptedGenerator(model, &limits, settings)
			if err != nil {
				return result, err
			}
			result.experienceCandidates, err = experience.NewLLMCandidatePipeline(pcruntime.ReportStructuredUsage(generator))
			if err != nil {
				return result, err
			}
		}
		if result.skillGenerator == nil {
			generator, err := skill.NewPromptedGenerator(model, &limits, settings)
			if err != nil {
				return result, err
			}
			result.skillGenerator, err = skill.NewLLMGenerator(pcruntime.ReportStructuredUsage(generator))
			if err != nil {
				return result, err
			}
		}
		if result.handoffGenerator == nil {
			generator, err := handoff.NewPromptedGenerator(model, &limits, settings)
			if err != nil {
				return result, err
			}
			result.handoffGenerator, err = handoff.NewLLMGenerationPipeline(
				pcruntime.ReportStructuredUsage(generator), handoff.NewContentEvidenceProjector(nil),
			)
			if err != nil {
				return result, err
			}
		}
		if config.Runtime.MemoryRerankEnabled && result.memoryReranker == nil {
			generator, err := memory.NewRerankPromptedGenerator(model, &limits)
			if err != nil {
				return result, err
			}
			result.memoryReranker, err = memory.NewLLMReranker(pcruntime.ReportStructuredUsage(generator))
			if err != nil {
				return result, err
			}
		}
	}

	if result.embeddingModel == nil && config.Inference.EmbeddingModel != "" {
		factory, err := newProviderFactory(supplied.HTTPClient)
		if err != nil {
			return result, err
		}
		transport, err := factory.EmbeddingTransport(config.Inference.EmbeddingModel)
		if err != nil {
			return result, err
		}
		if resource, ok := transport.(pcruntime.Resource); ok {
			result.resources = append(result.resources, resource)
		}
		profile, err := memory.NewEmbeddingProfile(
			config.Inference.EmbeddingProfileID, config.Inference.EmbeddingModel,
			config.Inference.EmbeddingDimension, config.Inference.EmbeddingNormalization,
		)
		if err != nil {
			return result, err
		}
		limits, err := inference.NewLimits(config.Inference.EmbeddingTimeout, 1)
		if err != nil {
			return result, err
		}
		readinessModel, err := inference.NewBatchedEmbeddingModel(
			transport, profile, config.Inference.EmbeddingBatchSize, &limits,
		)
		if err != nil {
			return result, err
		}
		result.embeddingReadiness = embeddingReadinessOperation(readinessModel)
		result.embeddingModel, err = inference.NewBatchedEmbeddingModel(
			inference.TraceEmbeddingTransport(transport, tracerProvider),
			profile, config.Inference.EmbeddingBatchSize, &limits,
		)
		if err != nil {
			return result, err
		}
	} else if result.embeddingModel != nil {
		result.embeddingReadiness = embeddingReadinessOperation(result.embeddingModel)
	}
	if result.embeddingModel != nil {
		result.embeddingModel = pcruntime.ReportEmbeddingUsage(result.embeddingModel)
	}
	if result.externalSkills == nil && len(configuredTargets) > 0 {
		provider, err := skill.NewAgentSkillProvider(config.ExternalSkills.HostID, configuredTargets)
		if err != nil {
			return result, err
		}
		result.externalSkills = provider
	}
	return result, nil
}

func agentSkillTargets(config ExternalSkillsConfig) ([]skill.AgentSkillTarget, error) {
	targets := make([]skill.AgentSkillTarget, 0, len(config.Targets)+len(config.CodexRoots))
	for _, configured := range config.Targets {
		target, err := skill.NewAgentSkillTarget(
			configured.TargetID, skill.AgentKind(configured.AgentKind),
			skill.InstallationScope(configured.InstallationScope), configured.Path,
			configured.AllowManagedPublish,
		)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	for _, configured := range config.CodexRoots {
		target, err := skill.NewAgentSkillTarget(
			configured.RootID, skill.CodexAgent,
			skill.InstallationScope(configured.InstallationScope), configured.Path,
			configured.AllowManagedPublish,
		)
		if err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func embeddingReadinessOperation(model inference.EmbeddingModel) pcruntime.DependencyOperation {
	return func(ctx context.Context) error {
		_, err := model.Embed(ctx, []string{"PowerContext readiness probe"})
		return err
	}
}

func providerFactory(client *http.Client) (*modelprovider.Factory, error) {
	return modelprovider.NewFactory(modelprovider.MilestoneB, modelprovider.ProcessEnvironment, client)
}

func runtimeCapabilities(
	dependencies assembledDependencies,
	memoryCapabilities memory.Capabilities,
) (pcruntime.Capabilities, error) {
	modes := make([]memory.SearchMode, 0, 4)
	if memoryCapabilities.FTS || memoryCapabilities.Hybrid {
		modes = append(modes, memory.SearchAuto)
	}
	if memoryCapabilities.FTS {
		modes = append(modes, memory.SearchFTS)
	}
	if memoryCapabilities.Vector {
		modes = append(modes, memory.SearchVector)
	}
	if memoryCapabilities.Hybrid {
		modes = append(modes, memory.SearchHybrid)
	}
	return pcruntime.NewCapabilities(pcruntime.CapabilityOptions{
		SourceTypes:            []string{"content"},
		ArtifactFamilies:       []string{memory.Family, experience.Family, skill.Family, handoff.Family},
		MemoryExtraction:       dependencies.memoryCandidates != nil,
		ExperienceGeneration:   dependencies.experienceGenerator != nil,
		ManagedSkillGeneration: dependencies.skillGenerator != nil,
		ExternalSkillRegistry:  dependencies.externalSkills != nil,
		HandoffGeneration:      dependencies.handoffGenerator != nil,
		SearchModes:            modes, ContextVersions: []string{pcruntime.PreparedContextV1},
	})
}
