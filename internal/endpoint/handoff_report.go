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
	"strings"
	"time"

	"github.com/go-faster/jx"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/internal/handoffreport"
	"github.com/ob-labs/powercontext-go/internal/runtime"
)

// HandoffReportOperations is deliberately shaped around the optional product
// surface. It keeps the generated HTTP contract out of Runtime and allows the
// same operations to be called directly by MCP without loopback HTTP.
type HandoffReportOperations interface {
	ListKnownScopes(context.Context, *string, int) (runtime.KnownHandoffScopePage, error)
	CreateProject(context.Context, runtime.CreateHandoffReportProject) (handoffreport.ProjectDescriptor, error)
	GetProject(context.Context, string) (handoffreport.ProjectDescriptor, error)
	UpdateProject(context.Context, handoffreport.ProjectDescriptor, int) (handoffreport.ProjectDescriptor, error)
	ListProjects(context.Context, *string, int, bool) (handoffreport.Page[handoffreport.ProjectDescriptor], error)
	RegisterWorkstream(context.Context, runtime.RegisterHandoffReportWorkstream) (handoffreport.WorkstreamDescriptor, error)
	UpdateWorkstream(context.Context, handoffreport.WorkstreamDescriptor, int) (handoffreport.WorkstreamDescriptor, error)
	ListWorkstreams(context.Context, string, *string, int, bool) (handoffreport.Page[handoffreport.WorkstreamDescriptor], error)
	RecordActivity(context.Context, runtime.RecordHandoffReportActivity) (handoffreport.StoredActivity, error)
	ListActivities(context.Context, runtime.HandoffReportActivityList) (handoffreport.ActivityPage, error)
	PurgeActivities(context.Context, string, time.Time) (int64, error)
	GetWorkspaceBinding(context.Context, string) (handoffreport.WorkspaceBinding, error)
	AttachWorkspaceBinding(context.Context, string, string, handoffreport.RepositoryRef, *int) (handoffreport.WorkspaceBinding, error)
	DetachWorkspaceBinding(context.Context, string, int) (handoffreport.WorkspaceBinding, error)
	GetReport(context.Context, runtime.GetHandoffReport) (handoffreport.Report, error)
}

func (h *Handler) ListHandoffReportKnownScopes(ctx context.Context, req *v1.ListHandoffReportKnownScopesRequest) (v1.ListHandoffReportKnownScopesRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	page, err := h.handoffReport.ListKnownScopes(
		ctx, optionalString(req.Cursor), req.Limit.Or(handoffreport.DefaultCatalogPageSize),
	)
	if err != nil {
		return nil, err
	}
	items := make([]v1.KnownHandoffScope, len(page.Items))
	for index, scope := range page.Items {
		items[index] = v1.KnownHandoffScope{ScopeID: scope}
	}
	return &v1.KnownHandoffScopePageHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response:               v1.KnownHandoffScopePage{Items: items, NextCursor: optionalNullableString(page.NextCursor)},
	}, nil
}

func (h *Handler) CreateHandoffReportProject(ctx context.Context, req *v1.CreateHandoffReportProjectRequest) (v1.CreateHandoffReportProjectRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	value, err := h.handoffReport.CreateProject(ctx, runtime.CreateHandoffReportProject{
		ProjectKey:    req.ProjectKey,
		Title:         req.Title,
		Description:   optionalString(req.Description),
		DefaultLocale: handoffreport.Locale(req.DefaultLocale.Or(v1.ReportLocaleZhCN)),
		Timezone:      req.Timezone.Or("UTC"),
	})
	if err != nil {
		return nil, err
	}
	return projectResponse(ctx, value), nil
}

func (h *Handler) GetHandoffReportProject(ctx context.Context, req *v1.GetHandoffReportProjectRequest) (v1.GetHandoffReportProjectRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	value, err := h.handoffReport.GetProject(ctx, req.ProjectID)
	if err != nil {
		return nil, err
	}
	return projectResponse(ctx, value), nil
}

func (h *Handler) UpdateHandoffReportProject(ctx context.Context, req *v1.UpdateHandoffReportProjectRequest) (v1.UpdateHandoffReportProjectRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	value, err := runtimeReportProject(req.Project)
	if err != nil {
		return nil, err
	}
	value, err = h.handoffReport.UpdateProject(ctx, value, req.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	return projectResponse(ctx, value), nil
}

func (h *Handler) ListHandoffReportProjects(ctx context.Context, req *v1.ListHandoffReportProjectsRequest) (v1.ListHandoffReportProjectsRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	page, err := h.handoffReport.ListProjects(ctx, optionalString(req.Cursor), req.Limit.Or(handoffreport.DefaultCatalogPageSize), req.IncludeArchived.Or(false))
	if err != nil {
		return nil, err
	}
	items := make([]v1.ProjectDescriptor, len(page.Items))
	for i, item := range page.Items {
		items[i] = wireReportProject(item)
	}
	return &v1.ProjectPageHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response:               v1.ProjectPage{Items: items, NextCursor: nullableString(page.NextCursor)},
	}, nil
}

func (h *Handler) RegisterHandoffReportWorkstream(ctx context.Context, req *v1.RegisterHandoffReportWorkstreamRequest) (v1.RegisterHandoffReportWorkstreamRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	refs, err := runtimeReportExternalReferences(req.ExternalRefs)
	if err != nil {
		return nil, err
	}
	value, err := h.handoffReport.RegisterWorkstream(ctx, runtime.RegisterHandoffReportWorkstream{
		ProjectID:    req.ProjectID,
		ScopeID:      req.ScopeID,
		Key:          optionalString(req.Key),
		Title:        req.Title,
		Kind:         handoffreport.WorkstreamKind(req.Kind),
		CatalogState: handoffreport.CatalogState(req.CatalogState.Or(v1.ReportCatalogStateIncluded)),
		ExternalRefs: refs,
		Labels:       req.Labels,
	})
	if err != nil {
		return nil, err
	}
	return workstreamResponse(ctx, value), nil
}

func (h *Handler) UpdateHandoffReportWorkstream(ctx context.Context, req *v1.UpdateHandoffReportWorkstreamRequest) (v1.UpdateHandoffReportWorkstreamRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	value, err := runtimeReportWorkstream(req.Workstream)
	if err != nil {
		return nil, err
	}
	value, err = h.handoffReport.UpdateWorkstream(ctx, value, req.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	return workstreamResponse(ctx, value), nil
}

func (h *Handler) ListHandoffReportWorkstreams(ctx context.Context, req *v1.ListHandoffReportWorkstreamsRequest) (v1.ListHandoffReportWorkstreamsRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	page, err := h.handoffReport.ListWorkstreams(ctx, req.ProjectID, optionalString(req.Cursor), req.Limit.Or(handoffreport.DefaultCatalogPageSize), req.IncludeArchived.Or(false))
	if err != nil {
		return nil, err
	}
	items := make([]v1.WorkstreamDescriptor, len(page.Items))
	for i, item := range page.Items {
		items[i] = wireReportWorkstream(item)
	}
	return &v1.WorkstreamPageHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response:               v1.WorkstreamPage{Items: items, NextCursor: nullableString(page.NextCursor)},
	}, nil
}

func (h *Handler) RecordHandoffReportActivity(ctx context.Context, req *v1.RecordHandoffReportActivityRequest) (v1.RecordHandoffReportActivityRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	sourceRef, err := runtimeOptionalReportExternalReference(req.SourceRef)
	if err != nil {
		return nil, err
	}
	refs, err := runtimeReportExternalReferences(req.EvidenceRefs)
	if err != nil {
		return nil, err
	}
	agent, err := runtimeReportActivityAgent(req.Agent)
	if err != nil {
		return nil, err
	}
	vcs, err := runtimeReportActivityVCS(req.VcsContext)
	if err != nil {
		return nil, err
	}
	stored, err := h.handoffReport.RecordActivity(ctx, runtime.RecordHandoffReportActivity{
		ProjectID:     req.ProjectID,
		ScopeID:       optionalString(req.ScopeID),
		Source:        handoffreport.ActivitySource(req.Source),
		SourceEventID: req.SourceEventID,
		SourceRef:     sourceRef,
		OccurredAt:    optionalTime(req.OccurredAt),
		TimeBasis:     handoffreport.TimeBasis(req.TimeBasis),
		Title:         optionalString(req.Title),
		Summary:       optionalString(req.Summary),
		Agent:         agent,
		SessionID:     optionalString(req.SessionID),
		VCSContext:    vcs,
		EvidenceRefs:  refs,
	})
	if err != nil {
		return nil, err
	}
	return &v1.StoredHandoffReportActivityHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response:               v1.StoredHandoffReportActivity{Cursor: int(stored.Cursor), Event: wireReportActivity(stored.Event)},
	}, nil
}

func (h *Handler) ListHandoffReportActivities(ctx context.Context, req *v1.ListHandoffReportActivitiesRequest) (v1.ListHandoffReportActivitiesRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	sources := []handoffreport.ActivitySource(nil)
	if values, ok := req.Sources.Get(); ok {
		sources = make([]handoffreport.ActivitySource, len(values))
		for i, value := range values {
			sources[i] = handoffreport.ActivitySource(value)
		}
	}
	page, err := h.handoffReport.ListActivities(ctx, runtime.HandoffReportActivityList{
		ProjectID:     req.ProjectID,
		PeriodStart:   optionalTime(req.PeriodStart),
		PeriodEnd:     optionalTime(req.PeriodEnd),
		Sources:       sources,
		AfterCursor:   int64(req.AfterCursor.Or(0)),
		ThroughCursor: optionalInt64(req.ThroughCursor),
		Limit:         req.Limit.Or(handoffreport.DefaultCatalogPageSize),
	})
	if err != nil {
		return nil, err
	}
	items := make([]v1.HandoffReportActivity, len(page.Items))
	for i, item := range page.Items {
		items[i] = wireReportActivity(item)
	}
	return &v1.HandoffReportActivityPageHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response: v1.HandoffReportActivityPage{
			Items:         items,
			NextCursor:    nullableInt64(page.NextCursor),
			HighWatermark: int(page.HighWatermark),
		},
	}, nil
}

func (h *Handler) PurgeHandoffReportActivities(ctx context.Context, req *v1.PurgeHandoffReportActivitiesRequest) (v1.PurgeHandoffReportActivitiesRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	deleted, err := h.handoffReport.PurgeActivities(ctx, req.ProjectID, req.ObservedBefore)
	if err != nil {
		return nil, err
	}
	return &v1.PurgeHandoffReportActivitiesResponseHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response:               v1.PurgeHandoffReportActivitiesResponse{DeletedCount: int(deleted)},
	}, nil
}

func (h *Handler) GetHandoffReportWorkspace(ctx context.Context, req *v1.GetHandoffReportWorkspaceRequest) (v1.GetHandoffReportWorkspaceRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	value, err := h.handoffReport.GetWorkspaceBinding(ctx, req.WorkspaceInstanceID)
	if err != nil {
		return nil, err
	}
	return workspaceResponse(ctx, value), nil
}

func (h *Handler) AttachHandoffReportWorkspace(ctx context.Context, req *v1.AttachHandoffReportWorkspaceRequest) (v1.AttachHandoffReportWorkspaceRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	repository, err := runtimeReportRepository(req.RepositoryRef)
	if err != nil {
		return nil, err
	}
	value, err := h.handoffReport.AttachWorkspaceBinding(ctx, req.WorkspaceInstanceID, req.ProjectID, repository, nullableInt(req.ExpectedVersion))
	if err != nil {
		return nil, err
	}
	return workspaceResponse(ctx, value), nil
}

func (h *Handler) DetachHandoffReportWorkspace(ctx context.Context, req *v1.DetachHandoffReportWorkspaceRequest) (v1.DetachHandoffReportWorkspaceRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	value, err := h.handoffReport.DetachWorkspaceBinding(ctx, req.WorkspaceInstanceID, req.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	return workspaceResponse(ctx, value), nil
}

func (h *Handler) GetHandoffReport(ctx context.Context, req *v1.GetHandoffReportRequest) (v1.GetHandoffReportRes, error) {
	if h.handoffReport == nil {
		return nil, &RuntimeNotReadyError{}
	}
	var locale *handoffreport.Locale
	if value, ok := req.Locale.Get(); ok {
		converted := handoffreport.Locale(value)
		locale = &converted
	}
	var period *runtime.HandoffReportPeriod
	if value, ok := req.Period.Get(); ok {
		period = &runtime.HandoffReportPeriod{
			Start:                   value.Start,
			End:                     value.End,
			Timezone:                optionalString(value.Timezone),
			CompareToPreviousPeriod: value.CompareToPreviousPeriod.Or(false),
		}
	}
	format := handoffreport.Format(req.Format.Or(v1.ReportFormatMarkdown))
	report, err := h.handoffReport.GetReport(ctx, runtime.GetHandoffReport{
		ScopeID:               req.ScopeID,
		Locale:                locale,
		IncludeEvidenceChecks: req.IncludeEvidenceChecks.Or(true),
		Format:                format,
		IncludeArchived:       req.IncludeArchived.Or(false),
		Period:                period,
	})
	if err != nil {
		return nil, err
	}

	selectionDigest, reportDigest := report.SelectionDigest(), report.ReportDigest()
	contentDisposition := v1.OptString{}
	if req.Download.Or(false) {
		extension := "json"
		if format == handoffreport.FormatMarkdown {
			extension = "md"
		}
		contentDisposition = v1.NewOptString(`attachment; filename="handoff-report.` + extension + `"`)
	}
	if format == handoffreport.FormatMarkdown {
		markdown, renderErr := handoffreport.RenderMarkdown(report)
		if renderErr != nil {
			return nil, renderErr
		}
		if sizeErr := enforceHandoffReportSize(report, len([]byte(markdown))); sizeErr != nil {
			return nil, sizeErr
		}
		return &v1.GetHandoffReportOKTextMarkdownHeaders{
			CacheControl:                 v1.NewOptGetHandoffReportOKCacheControl(v1.GetHandoffReportOKCacheControlNoStore),
			ContentDisposition:           contentDisposition,
			XPowerContextReportDigest:    v1.NewOptString(reportDigest),
			XPowerContextRequestID:       requestID(ctx),
			XPowerContextSelectionDigest: v1.NewOptString(selectionDigest),
			Response:                     v1.GetHandoffReportOKTextMarkdown{Data: strings.NewReader(markdown)},
		}, nil
	}

	wireReport, err := wireHandoffReportObject(report)
	if err != nil {
		return nil, err
	}
	response := v1.HandoffReportResponse{
		Format:          v1.ReportFormatJSON,
		Report:          v1.NewNilHandoffReportResponseReport(wireReport),
		SelectionDigest: selectionDigest,
		ReportDigest:    reportDigest,
	}
	response.Markdown.SetToNull()
	encoder := new(jx.Encoder)
	response.Encode(encoder)
	if err := enforceHandoffReportSize(report, len(encoder.Bytes())); err != nil {
		return nil, err
	}
	return &v1.HandoffReportResponseHeaders{
		CacheControl:                 v1.NewOptGetHandoffReportOKCacheControl(v1.GetHandoffReportOKCacheControlNoStore),
		ContentDisposition:           contentDisposition,
		XPowerContextReportDigest:    v1.NewOptString(reportDigest),
		XPowerContextRequestID:       requestID(ctx),
		XPowerContextSelectionDigest: v1.NewOptString(selectionDigest),
		Response:                     response,
	}, nil
}

var _ HandoffReportOperations = (*runtime.HandoffReportApplication)(nil)
