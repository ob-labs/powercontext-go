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

package endpoint

import (
	"context"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/runtime"
)

type (
	CapabilityProvider func(context.Context) (runtime.Capabilities, error)
	ReadinessProvider  func(context.Context) (runtime.Readiness, error)
)

type HandlerOptions struct {
	Capabilities  CapabilityProvider
	Readiness     ReadinessProvider
	Sources       SourceOperations
	Memory        MemoryOperations
	Context       ContextOperations
	Review        ReviewOperations
	Generation    GenerationOperations
	External      ExternalSkillOperations
	Handoff       HandoffOperations
	Work          WorkOperations
	HandoffReport HandoffReportOperations
	Statistics    StatisticsOperations
}

// Handler is the single application-operation adapter shared by HTTP and MCP.
// Operations are added as complete vertical slices; embedding the generated
// implementation makes an omitted binding fail explicitly instead of being
// guessed at the transport layer.
type Handler struct {
	capabilities  CapabilityProvider
	readiness     ReadinessProvider
	sources       SourceOperations
	memory        MemoryOperations
	context       ContextOperations
	review        ReviewOperations
	generation    GenerationOperations
	external      ExternalSkillOperations
	handoff       HandoffOperations
	work          WorkOperations
	handoffReport HandoffReportOperations
	statistics    StatisticsOperations
}

var _ v1.Handler = (*Handler)(nil)

func NewHandler(options HandlerOptions) *Handler {
	return &Handler{
		capabilities:  options.Capabilities,
		readiness:     options.Readiness,
		sources:       options.Sources,
		memory:        options.Memory,
		context:       options.Context,
		review:        options.Review,
		generation:    options.Generation,
		external:      options.External,
		handoff:       options.Handoff,
		work:          options.Work,
		handoffReport: options.HandoffReport,
		statistics:    options.Statistics,
	}
}

func (h *Handler) GetLiveness(ctx context.Context) (*v1.HealthResponseHeaders, error) {
	return &v1.HealthResponseHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response:               v1.HealthResponse{Status: "ok"},
	}, nil
}

func (h *Handler) GetReadiness(ctx context.Context) (v1.GetReadinessRes, error) {
	if h.readiness == nil {
		return readinessResponse(ctx, runtime.NotReady, map[string]runtime.CheckStatus{"runtime": "not_ready"}), nil
	}
	value, err := h.readiness(ctx)
	if err != nil {
		return nil, err
	}
	return readinessResponse(ctx, value.Status(), value.Checks()), nil
}

func (h *Handler) GetCapabilities(ctx context.Context) (v1.GetCapabilitiesRes, error) {
	value := runtime.EmptyCapabilities()
	if h.capabilities != nil {
		resolved, err := h.capabilities(ctx)
		if err != nil {
			return nil, err
		}
		value = resolved
	}
	searchModes := make([]v1.MemorySearchMode, 0, len(value.SearchModes()))
	for _, mode := range value.SearchModes() {
		searchModes = append(searchModes, v1.MemorySearchMode(mode))
	}
	versions := make([]v1.PreparedContextSchema, 0, len(value.ContextVersions()))
	for _, version := range value.ContextVersions() {
		versions = append(versions, v1.PreparedContextSchema(version))
	}
	return &v1.CapabilitiesHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response: v1.Capabilities{
			SourceTypes: value.SourceTypes(), ArtifactFamilies: value.ArtifactFamilies(),
			MemoryExtraction:       value.MemoryExtraction(),
			ExperienceGeneration:   v1.NewOptBool(value.ExperienceGeneration()),
			ManagedSkillGeneration: v1.NewOptBool(value.ManagedSkillGeneration()),
			ExternalSkillRegistry:  v1.NewOptBool(value.ExternalSkillRegistry()),
			HandoffGeneration:      value.HandoffGeneration(), SearchModes: searchModes, ContextVersions: versions,
		},
	}, nil
}

func readinessResponse(ctx context.Context, status runtime.ReadinessStatus, checks map[string]runtime.CheckStatus) v1.GetReadinessRes {
	wireChecks := make(v1.ReadinessResponseChecks, len(checks))
	for key, value := range checks {
		wireChecks[key] = string(value)
	}
	headers := v1.ReadinessResponseHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response:               v1.ReadinessResponse{Status: v1.ReadinessStatus(status), Checks: wireChecks},
	}
	if status == runtime.NotReady {
		response := v1.GetReadinessServiceUnavailable(headers)
		return &response
	}
	response := v1.GetReadinessOK(headers)
	return &response
}
