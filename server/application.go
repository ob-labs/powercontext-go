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
	"log/slog"
	"net/http"
	"sync"

	"go.opentelemetry.io/otel/trace"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/endpoint"
	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	servermetrics "github.com/ob-labs/powercontext-go/internal/observability/metrics"
	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/webui"
)

// Application is one fully assembled Runtime and its shared endpoint adapter.
// Close is idempotent and drains admitted work before closing persistence.
type Application struct {
	config            ProcessConfig
	runtime           *pcruntime.Runtime
	endpoint          *endpoint.Handler
	capabilities      pcruntime.Capabilities
	readiness         *pcruntime.ReadinessChecks
	metrics           *servermetrics.Server
	tracing           trace.TracerProvider
	logger            *slog.Logger
	review            *pcruntime.ReviewApplication
	externalSkills    *pcruntime.ExternalSkillApplication
	agentSkillTargets []skill.AgentSkillTarget

	readinessMu   sync.Mutex
	hasReadiness  bool
	lastReadiness pcruntime.ReadinessStatus
}

// OpenApplication initializes persistence projections before making any
// operation reachable. A partially initialized application is always closed.
func OpenApplication(ctx context.Context, config ProcessConfig, dependencies Dependencies) (*Application, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	storage, err := openApplicationStorage(ctx, config.Database)
	if err != nil {
		return nil, err
	}
	foundation, err := openApplicationFoundation(ctx, config, dependencies, storage)
	if err != nil {
		return nil, err
	}
	fail := func(cause error) (*Application, error) {
		return nil, foundation.closeWithError(cause)
	}
	repositories, err := buildApplicationRepositories(ctx, foundation)
	if err != nil {
		return fail(err)
	}
	services, err := buildApplicationServices(ctx, config, dependencies, foundation, repositories)
	if err != nil {
		return fail(err)
	}
	readiness, err := configuredReadiness(
		foundation.storage.database.Ping, foundation.assembled, foundation.statisticsClock,
	)
	if err != nil {
		return fail(err)
	}
	capabilities, err := runtimeCapabilities(foundation.assembled, repositories.memoryIndex.Capabilities())
	if err != nil {
		return fail(err)
	}
	application := &Application{
		config: config, runtime: foundation.lifecycle, capabilities: capabilities,
		readiness: readiness, metrics: foundation.metrics, tracing: foundation.tracing, logger: dependencies.Logger,
		review: services.review, externalSkills: services.externalSkills,
		agentSkillTargets: foundation.assembled.agentSkillTargets,
	}
	application.endpoint = endpoint.NewHandler(endpoint.HandlerOptions{
		Capabilities: application.getCapabilities,
		Readiness:    application.getReadiness,
		Sources:      services.sources, Memory: services.memory, Context: services.context,
		Review: services.review, Generation: services.generation, External: services.externalSkills,
		Handoff: services.handoff, Work: services.work, HandoffReport: services.handoffReport,
		Statistics: services.statistics,
	})
	if _, err := application.getReadiness(ctx); err != nil {
		return fail(err)
	}
	return application, nil
}

func (a *Application) Endpoint() v1.Handler { return a.endpoint }

func (a *Application) HTTPHandler() (http.Handler, error) {
	if a == nil || a.endpoint == nil {
		return nil, errors.New("server: application is not initialized")
	}
	token := ""
	if a.config.Auth.Enabled {
		token = a.config.Auth.Token
	}
	var webOptions *webui.Options
	if a.config.Dashboard.Enabled || a.config.HandoffReport.Enabled {
		scopes := make([]webui.Scope, len(a.config.Dashboard.Scopes))
		for index, scope := range a.config.Dashboard.Scopes {
			scopes[index] = webui.Scope{ScopeID: scope.ScopeID, DisplayName: scope.DisplayName}
		}
		webOptions = &webui.Options{
			DashboardEnabled: a.config.Dashboard.Enabled, Scopes: scopes,
			HandoffReportEnabled:   a.config.HandoffReport.Enabled,
			AuthenticationRequired: a.config.Auth.Enabled,
			AgentSkillTargets:      append([]skill.AgentSkillTarget(nil), a.agentSkillTargets...),
			SkillProjections:       webSkillProjectionOperations{review: a.review, external: a.externalSkills},
		}
	}
	return NewHTTPHandler(a.endpoint, HTTPOptions{
		BearerToken: token, HandoffReportRoutes: a.config.HandoffReport.Enabled,
		metrics: a.metrics, TracerProvider: a.tracing, Logger: a.logger, AccessLog: a.config.Logging.Access,
		MCP:   MCPOptions{Enabled: a.config.MCP.Enabled, Path: a.config.MCP.Path},
		webUI: webOptions,
	})
}

func (a *Application) Close(ctx context.Context) error {
	if a == nil || a.runtime == nil {
		return nil
	}
	if a.metrics != nil {
		a.metrics.SetReady(false)
	}
	return a.runtime.Close(ctx)
}

func (a *Application) getCapabilities(ctx context.Context) (pcruntime.Capabilities, error) {
	var result pcruntime.Capabilities
	err := a.runtime.Operation(ctx, func(context.Context) error {
		result = a.capabilities
		return nil
	})
	return result, err
}

func (a *Application) getReadiness(ctx context.Context) (pcruntime.Readiness, error) {
	var value pcruntime.Readiness
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var runErr error
		value, runErr = a.readiness.Run(ctx)
		return runErr
	})
	if a.metrics != nil {
		a.metrics.SetReady(err == nil && value.Status() != pcruntime.NotReady)
	}
	if err == nil {
		a.observeReadiness(ctx, value.Status())
	}
	return value, err
}

func configuredReadiness(
	database pcruntime.DependencyOperation,
	dependencies assembledDependencies,
	clock pcruntime.Clock,
) (*pcruntime.ReadinessChecks, error) {
	definitions := []pcruntime.ProbeDefinition{
		{
			Name: "runtime",
			Probe: func(context.Context) (pcruntime.CheckStatus, error) {
				return pcruntime.CheckReady, nil
			},
		},
		{
			Name: "database", Blocking: true,
			Probe: pcruntime.DependencyProbe(database, pcruntime.DefaultReadinessProbeTimeout),
		},
	}
	for _, configured := range []struct {
		name      string
		operation pcruntime.DependencyOperation
	}{
		{name: "inference.generation", operation: dependencies.generationReadiness},
		{name: "inference.embedding", operation: dependencies.embeddingReadiness},
	} {
		if configured.operation == nil {
			continue
		}
		probe := pcruntime.DependencyProbe(configured.operation, pcruntime.DefaultReadinessProbeTimeout)
		cached, err := pcruntime.NewCachedProbe(
			probe,
			pcruntime.DefaultReadinessCacheTTL,
			pcruntime.TransientReadinessCacheTTL,
			clock,
		)
		if err != nil {
			return nil, err
		}
		definitions = append(definitions, pcruntime.ProbeDefinition{
			Name: configured.name, Probe: cached.Probe,
		})
	}
	return pcruntime.NewReadinessChecks(definitions)
}

func (a *Application) observeReadiness(ctx context.Context, status pcruntime.ReadinessStatus) {
	if a == nil {
		return
	}
	a.readinessMu.Lock()
	changed := !a.hasReadiness || a.lastReadiness != status
	a.hasReadiness = true
	a.lastReadiness = status
	a.readinessMu.Unlock()
	if !changed {
		return
	}
	event, message := "server.not_ready", "PowerContext Server is not ready"
	switch status {
	case pcruntime.Ready:
		event, message = "server.ready", "PowerContext Server is ready"
	case pcruntime.Degraded:
		event, message = "server.degraded", "PowerContext Server is degraded"
	}
	serverlogging.LogLifecycle(ctx, namedLogger(a.logger, "powercontext.server.factory"), event, message)
}
