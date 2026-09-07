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
	"strings"
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
	rerankReadiness      pcruntime.DependencyOperation
	resources            []pcruntime.Resource
}

type assembledProviderFactory interface {
	TextModel(string) (inference.TextModel, error)
	EmbeddingTransport(string) (inference.EmbeddingTransport, error)
}

type workloadProviderFactory interface {
	TextModelWithWorkload(string, modelprovider.WorkloadConfig) (inference.TextModel, error)
	EmbeddingTransportWithWorkload(string, modelprovider.WorkloadConfig) (inference.EmbeddingTransport, error)
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
	generationProviderModel, err := assembleGenerationDependencies(
		config, supplied, tracerProvider, newProviderFactory, &result,
	)
	if err != nil {
		return result, err
	}
	if err := assembleMemoryReranker(
		config, supplied, tracerProvider, newProviderFactory, generationProviderModel, &result,
	); err != nil {
		return result, err
	}

	if result.embeddingModel == nil && config.Inference.EmbeddingModel != "" {
		factory, err := newProviderFactory(supplied.HTTPClient)
		if err != nil {
			return result, err
		}
		transport, err := embeddingTransportForWorkload(factory, config.Inference.EmbeddingModel, config.Inference.Embedding)
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

func assembleGenerationDependencies(
	config ProcessConfig,
	supplied Dependencies,
	tracerProvider trace.TracerProvider,
	newProviderFactory providerFactoryBuilder,
	result *assembledDependencies,
) (inference.TextModel, error) {
	needsComponents := result.memoryCandidates == nil || result.experienceCandidates == nil ||
		result.experienceGenerator == nil || result.skillGenerator == nil || result.handoffGenerator == nil
	needsModel := needsComponents ||
		(config.Runtime.MemoryRerankEnabled && result.memoryReranker == nil && config.Inference.Rerank == nil)
	if config.Inference.GenerationModel == "" || !needsModel {
		return nil, nil
	}
	factory, err := newProviderFactory(supplied.HTTPClient)
	if err != nil {
		return nil, err
	}
	providerModel, err := textModelForWorkload(factory, config.Inference.GenerationModel, config.Inference.Generation)
	if err != nil {
		return nil, err
	}
	result.generationReadiness = func(ctx context.Context) error {
		return inference.ProbeTextModel(ctx, providerModel)
	}
	if !needsComponents {
		return providerModel, nil
	}
	model := inference.TraceTextModel(providerModel, tracerProvider)
	limits, err := inference.NewLimits(config.Inference.GenerationTimeout, config.Inference.GenerationMaxRequests)
	if err != nil {
		return nil, err
	}
	if err := assembleGenerationComponents(config, model, limits, result); err != nil {
		return nil, err
	}
	return providerModel, nil
}

func assembleGenerationComponents(
	config ProcessConfig,
	model inference.TextModel,
	limits inference.Limits,
	result *assembledDependencies,
) error {
	settings := inference.GenerationSettings{}
	if result.memoryCandidates == nil {
		generator, err := memory.NewExtractionPromptedGenerator(model, config.Runtime.MemoryExtractionProfile, &limits, settings)
		if err != nil {
			return err
		}
		pipeline, err := memory.NewLLMCandidatePipeline(
			pcruntime.ReportStructuredUsage(generator), memory.NewContentEvidenceProjector(nil),
		)
		if err != nil {
			return err
		}
		result.memoryCandidates = pipeline
	}
	if result.experienceGenerator == nil {
		generator, err := experience.NewPromptedGenerator(model, &limits, settings)
		if err != nil {
			return err
		}
		output, err := experience.NewLLMGenerator(pcruntime.ReportStructuredUsage(generator))
		if err != nil {
			return err
		}
		result.experienceGenerator = output
	}
	if result.experienceCandidates == nil {
		generator, err := experience.NewIncubationPromptedGenerator(model, &limits, settings)
		if err != nil {
			return err
		}
		pipeline, err := experience.NewLLMCandidatePipeline(pcruntime.ReportStructuredUsage(generator))
		if err != nil {
			return err
		}
		result.experienceCandidates = pipeline
	}
	if result.skillGenerator == nil {
		generator, err := skill.NewPromptedGenerator(model, &limits, settings)
		if err != nil {
			return err
		}
		output, err := skill.NewLLMGenerator(pcruntime.ReportStructuredUsage(generator))
		if err != nil {
			return err
		}
		result.skillGenerator = output
	}
	if result.handoffGenerator == nil {
		generator, err := handoff.NewPromptedGenerator(model, &limits, settings)
		if err != nil {
			return err
		}
		pipeline, err := handoff.NewLLMGenerationPipeline(
			pcruntime.ReportStructuredUsage(generator), handoff.NewContentEvidenceProjector(nil),
		)
		if err != nil {
			return err
		}
		result.handoffGenerator = pipeline
	}
	return nil
}

func assembleMemoryReranker(
	config ProcessConfig,
	supplied Dependencies,
	tracerProvider trace.TracerProvider,
	newProviderFactory providerFactoryBuilder,
	generationProviderModel inference.TextModel,
	result *assembledDependencies,
) error {
	if !config.Runtime.MemoryRerankEnabled || result.memoryReranker != nil {
		return nil
	}
	runModel := generationProviderModel
	timeout, maxRequests := config.Inference.GenerationTimeout, config.Inference.GenerationMaxRequests
	if rerank := config.Inference.Rerank; rerank != nil {
		runModelID := rerank.Model
		if runModelID == "" {
			runModelID = config.Inference.GenerationModel
		}
		factory, err := newProviderFactory(supplied.HTTPClient)
		if err != nil {
			return err
		}
		model, err := textModelForWorkload(factory, runModelID, resolvedRerankWorkload(config.Inference))
		if err != nil {
			return err
		}
		runModel = model
		result.rerankReadiness = func(ctx context.Context) error {
			return inference.ProbeTextModel(ctx, model)
		}
		if rerank.Timeout != nil {
			timeout = *rerank.Timeout
		}
		if rerank.MaxRequests != nil {
			maxRequests = *rerank.MaxRequests
		}
	}
	if runModel == nil {
		return inference.NewConfigurationError("model", "rerank model is required")
	}
	limits, err := inference.NewLimits(timeout, maxRequests)
	if err != nil {
		return err
	}
	generator, err := memory.NewRerankPromptedGenerator(inference.TraceTextModel(runModel, tracerProvider), &limits)
	if err != nil {
		return err
	}
	reranker, err := memory.NewLLMReranker(pcruntime.ReportStructuredUsage(generator))
	if err != nil {
		return err
	}
	result.memoryReranker = reranker
	return nil
}

func textModelForWorkload(
	factory assembledProviderFactory,
	modelID string,
	workload *InferenceWorkloadConfig,
) (inference.TextModel, error) {
	if workload == nil {
		return factory.TextModel(modelID)
	}
	configured, ok := factory.(workloadProviderFactory)
	if !ok {
		return nil, inference.NewConfigurationError("workload-provider", "provider factory does not support workload overrides")
	}
	return configured.TextModelWithWorkload(modelID, modelProviderWorkload(workload))
}

func embeddingTransportForWorkload(
	factory assembledProviderFactory,
	modelID string,
	workload *InferenceWorkloadConfig,
) (inference.EmbeddingTransport, error) {
	if workload == nil {
		return factory.EmbeddingTransport(modelID)
	}
	configured, ok := factory.(workloadProviderFactory)
	if !ok {
		return nil, inference.NewConfigurationError("workload-provider", "provider factory does not support workload overrides")
	}
	return configured.EmbeddingTransportWithWorkload(modelID, modelProviderWorkload(workload))
}

func modelProviderWorkload(config *InferenceWorkloadConfig) modelprovider.WorkloadConfig {
	result := modelprovider.WorkloadConfig{BaseURL: config.BaseURL, Headers: make(http.Header)}
	for name, value := range config.Headers.Values() {
		result.Headers.Set(name, value)
	}
	if len(config.ModelSettings) == 0 {
		return result
	}
	result.ModelSettings = make(map[string]any, len(config.ModelSettings))
	for name, value := range config.ModelSettings {
		result.ModelSettings[name] = value
	}
	return result
}

func resolvedRerankWorkload(config InferenceConfig) *InferenceWorkloadConfig {
	if config.Rerank == nil {
		return nil
	}
	var result *InferenceWorkloadConfig
	if config.Rerank.Model == "" {
		result = cloneInferenceWorkload(config.Generation)
	}
	result = overlayInferenceWorkload(result, config.Rerank.Workload)
	if result == nil || !result.hasOverrides() {
		return nil
	}
	if result.ModelSettings == nil {
		result.ModelSettings = make(map[string]any)
	}
	// The reranker is intentionally deterministic even when the inherited
	// generation workload supplies a sampling temperature.
	result.ModelSettings["temperature"] = 0.0
	return result
}

func cloneInferenceWorkload(config *InferenceWorkloadConfig) *InferenceWorkloadConfig {
	if config == nil {
		return nil
	}
	result := &InferenceWorkloadConfig{BaseURL: config.BaseURL, Headers: InferenceHeaders{values: config.Headers.Values()}}
	if len(config.ModelSettings) > 0 {
		result.ModelSettings = make(map[string]any, len(config.ModelSettings))
		for name, value := range config.ModelSettings {
			result.ModelSettings[name] = value
		}
	}
	return result
}

func overlayInferenceWorkload(
	base *InferenceWorkloadConfig,
	overlay *InferenceWorkloadConfig,
) *InferenceWorkloadConfig {
	if overlay == nil {
		return base
	}
	result := cloneInferenceWorkload(base)
	if result == nil {
		result = &InferenceWorkloadConfig{}
	}
	if overlay.BaseURL != "" {
		result.BaseURL = overlay.BaseURL
	}
	if overlay.HeadersConfigured() {
		if result.Headers.values == nil {
			result.Headers.values = make(map[string]string)
		}
		for name, value := range overlay.Headers.Values() {
			for existing := range result.Headers.values {
				if strings.EqualFold(existing, name) {
					delete(result.Headers.values, existing)
				}
			}
			result.Headers.values[name] = value
		}
	}
	if len(overlay.ModelSettings) > 0 {
		if result.ModelSettings == nil {
			result.ModelSettings = make(map[string]any, len(overlay.ModelSettings))
		}
		for name, value := range overlay.ModelSettings {
			result.ModelSettings[name] = value
		}
	}
	return result
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
