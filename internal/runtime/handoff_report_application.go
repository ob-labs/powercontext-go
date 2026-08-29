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
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/internal/handoffreport"
)

type HandoffReportBackend interface {
	CreateProject(context.Context, handoffreport.ProjectDescriptor, time.Time) (handoffreport.ProjectDescriptor, error)
	GetProject(context.Context, string) (handoffreport.ProjectDescriptor, error)
	UpdateProject(context.Context, handoffreport.ProjectDescriptor, int, time.Time) (handoffreport.ProjectDescriptor, error)
	ListProjects(context.Context, *string, int, bool) (handoffreport.Page[handoffreport.ProjectDescriptor], error)
	RegisterWorkstream(context.Context, handoffreport.WorkstreamDescriptor, time.Time) (handoffreport.WorkstreamDescriptor, error)
	UpdateWorkstream(context.Context, handoffreport.WorkstreamDescriptor, int, time.Time) (handoffreport.WorkstreamDescriptor, error)
	ListWorkstreams(context.Context, string, *string, int, bool) (handoffreport.Page[handoffreport.WorkstreamDescriptor], error)
	RecordActivity(context.Context, handoffreport.ActivityEvent) (handoffreport.StoredActivity, error)
	ListActivities(context.Context, string, *time.Time, *time.Time, []handoffreport.ActivitySource, int64, *int64, int) (handoffreport.ActivityPage, error)
	PurgeActivities(context.Context, string, time.Time) (int64, error)
	GetWorkspaceBinding(context.Context, string) (handoffreport.WorkspaceBinding, error)
	AttachWorkspaceBinding(context.Context, string, string, handoffreport.RepositoryRef, *int, time.Time) (handoffreport.WorkspaceBinding, error)
	DetachWorkspaceBinding(context.Context, string, int) (handoffreport.WorkspaceBinding, error)
	ReadHandoffReportInputs(context.Context, string, bool, *time.Time, *time.Time, *time.Time, *time.Time) (handoffreport.ProjectDescriptor, []handoffreport.WorkstreamDescriptor, []handoffreport.ActivityEvent, int64, int, error)
}

type HandoffReportActivityList struct {
	ProjectID              string
	PeriodStart, PeriodEnd *time.Time
	Sources                []handoffreport.ActivitySource
	AfterCursor            int64
	ThroughCursor          *int64
	Limit                  int
}

type HandoffReportIDFactory func(prefix string) (string, error)

type CreateHandoffReportProject struct {
	ProjectKey, Title string
	Description       *string
	DefaultLocale     handoffreport.Locale
	Timezone          string
}
type RegisterHandoffReportWorkstream struct {
	ProjectID, ScopeID string
	Key                *string
	Title              string
	Kind               handoffreport.WorkstreamKind
	CatalogState       handoffreport.CatalogState
	ExternalRefs       []handoffreport.ExternalReference
	Labels             []string
}
type RecordHandoffReportActivity struct {
	ProjectID      string
	ScopeID        *string
	Source         handoffreport.ActivitySource
	SourceEventID  string
	SourceRef      *handoffreport.ExternalReference
	OccurredAt     *time.Time
	TimeBasis      handoffreport.TimeBasis
	Title, Summary *string
	Agent          *handoffreport.ActivityAgent
	SessionID      *string
	VCSContext     *handoffreport.ActivityVCSContext
	EvidenceRefs   []handoffreport.ExternalReference
}
type HandoffReportPeriod struct {
	Start, End              time.Time
	Timezone                *string
	CompareToPreviousPeriod bool
}
type GetHandoffReport struct {
	ScopeID               string
	Locale                *handoffreport.Locale
	IncludeEvidenceChecks bool
	Format                handoffreport.Format
	IncludeArchived       bool
	Period                *HandoffReportPeriod
}

type KnownHandoffScopePage struct {
	Items      []string
	NextCursor *string
}

type HandoffScopeIDs func(context.Context) ([]string, error)

type HandoffReportApplication struct {
	runtime *Runtime
	backend HandoffReportBackend
	reports *handoffreport.Service
	clock   Clock
	ids     HandoffReportIDFactory
	scopes  HandoffScopeIDs
}

func NewHandoffReportApplication(
	runtime *Runtime,
	backend HandoffReportBackend,
	reader handoffreport.HandoffReader,
	continuity handoffreport.WorkContinuityReader,
	clock Clock,
	ids HandoffReportIDFactory,
	scopeProviders ...HandoffScopeIDs,
) (*HandoffReportApplication, error) {
	if runtime == nil || backend == nil || reader == nil {
		return nil, errors.New("runtime: Handoff Report dependencies must not be nil")
	}
	if clock == nil {
		clock = time.Now
	}
	if ids == nil {
		ids = defaultHandoffReportID
	}
	var reports *handoffreport.Service
	var err error
	if continuity == nil {
		reports, err = handoffreport.NewService(reader)
	} else {
		reports, err = handoffreport.NewService(reader, continuity)
	}
	if err != nil {
		return nil, err
	}
	var scopes HandoffScopeIDs
	if len(scopeProviders) > 1 {
		return nil, errors.New("runtime: at most one Handoff scope provider may be configured")
	}
	if len(scopeProviders) == 1 {
		scopes = scopeProviders[0]
	}
	return &HandoffReportApplication{runtime: runtime, backend: backend, reports: reports, clock: clock, ids: ids, scopes: scopes}, nil
}

func (a *HandoffReportApplication) ListKnownScopes(ctx context.Context, cursor *string, limit int) (KnownHandoffScopePage, error) {
	var result KnownHandoffScopePage
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		if limit < 1 || limit > 100 {
			return &handoffreport.CatalogArgumentError{Field: "limit", Detail: "must be between 1 and 100"}
		}
		if cursor != nil && (strings.TrimSpace(*cursor) == "" || strings.TrimSpace(*cursor) != *cursor) {
			return &handoffreport.CatalogArgumentError{Field: "cursor", Detail: "must be non-empty trimmed text"}
		}
		var scopes []string
		if a.scopes != nil {
			values, err := a.scopes(ctx)
			if err != nil {
				return err
			}
			scopes = slices.Clone(values)
		}
		sort.Strings(scopes)
		scopes = slices.Compact(scopes)
		start := 0
		if cursor != nil {
			start = sort.Search(len(scopes), func(index int) bool { return scopes[index] > *cursor })
		}
		end := min(start+limit, len(scopes))
		result.Items = slices.Clone(scopes[start:end])
		if end < len(scopes) && len(result.Items) > 0 {
			next := result.Items[len(result.Items)-1]
			result.NextCursor = &next
		}
		return nil
	})
	return result, err
}

func (a *HandoffReportApplication) CreateProject(ctx context.Context, input CreateHandoffReportProject) (handoffreport.ProjectDescriptor, error) {
	var result handoffreport.ProjectDescriptor
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		id, err := a.ids("prj_")
		if err != nil {
			return err
		}
		locale := input.DefaultLocale
		if locale == "" {
			locale = handoffreport.LocaleChinese
		}
		timezone := input.Timezone
		if timezone == "" {
			timezone = "UTC"
		}
		value, err := handoffreport.NewProjectDescriptor(id, input.ProjectKey, input.Title, input.Description, locale, timezone, handoffreport.CatalogIncluded, 1)
		if err != nil {
			return err
		}
		result, err = a.backend.CreateProject(ctx, value, a.clock().UTC())
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) GetProject(ctx context.Context, id string) (handoffreport.ProjectDescriptor, error) {
	var result handoffreport.ProjectDescriptor
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.GetProject(ctx, id)
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) UpdateProject(ctx context.Context, value handoffreport.ProjectDescriptor, expected int) (handoffreport.ProjectDescriptor, error) {
	var result handoffreport.ProjectDescriptor
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.UpdateProject(ctx, value, expected, a.clock().UTC())
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) ListProjects(ctx context.Context, cursor *string, limit int, archived bool) (handoffreport.Page[handoffreport.ProjectDescriptor], error) {
	var result handoffreport.Page[handoffreport.ProjectDescriptor]
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.ListProjects(ctx, cursor, limit, archived)
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) RegisterWorkstream(ctx context.Context, input RegisterHandoffReportWorkstream) (handoffreport.WorkstreamDescriptor, error) {
	var result handoffreport.WorkstreamDescriptor
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		state := input.CatalogState
		if state == "" {
			state = handoffreport.CatalogIncluded
		}
		value, err := handoffreport.NewWorkstreamDescriptor(input.ScopeID, input.ProjectID, input.Key, input.Title, input.Kind, state, input.ExternalRefs, input.Labels, 1)
		if err != nil {
			return err
		}
		result, err = a.backend.RegisterWorkstream(ctx, value, a.clock().UTC())
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) UpdateWorkstream(ctx context.Context, value handoffreport.WorkstreamDescriptor, expected int) (handoffreport.WorkstreamDescriptor, error) {
	var result handoffreport.WorkstreamDescriptor
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.UpdateWorkstream(ctx, value, expected, a.clock().UTC())
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) ListWorkstreams(ctx context.Context, project string, cursor *string, limit int, archived bool) (handoffreport.Page[handoffreport.WorkstreamDescriptor], error) {
	var result handoffreport.Page[handoffreport.WorkstreamDescriptor]
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.ListWorkstreams(ctx, project, cursor, limit, archived)
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) RecordActivity(ctx context.Context, input RecordHandoffReportActivity) (handoffreport.StoredActivity, error) {
	var result handoffreport.StoredActivity
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		id, err := a.ids("evt_")
		if err != nil {
			return err
		}
		event, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{EventID: id, ProjectID: input.ProjectID, ScopeID: input.ScopeID, Source: input.Source, SourceEventID: input.SourceEventID, SourceRef: input.SourceRef, OccurredAt: input.OccurredAt, ObservedAt: a.clock().UTC(), TimeBasis: input.TimeBasis, Title: input.Title, Summary: input.Summary, Agent: input.Agent, SessionID: input.SessionID, VCSContext: input.VCSContext, EvidenceRefs: input.EvidenceRefs})
		if err != nil {
			return err
		}
		result, err = a.backend.RecordActivity(ctx, event)
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) ListActivities(ctx context.Context, query HandoffReportActivityList) (handoffreport.ActivityPage, error) {
	var result handoffreport.ActivityPage
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.ListActivities(ctx, query.ProjectID, query.PeriodStart, query.PeriodEnd, query.Sources, query.AfterCursor, query.ThroughCursor, query.Limit)
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) PurgeActivities(ctx context.Context, project string, before time.Time) (int64, error) {
	var result int64
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.PurgeActivities(ctx, project, before)
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) GetWorkspaceBinding(ctx context.Context, id string) (handoffreport.WorkspaceBinding, error) {
	var result handoffreport.WorkspaceBinding
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.GetWorkspaceBinding(ctx, id)
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) AttachWorkspaceBinding(ctx context.Context, id, project string, repository handoffreport.RepositoryRef, expected *int) (handoffreport.WorkspaceBinding, error) {
	var result handoffreport.WorkspaceBinding
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.AttachWorkspaceBinding(ctx, id, project, repository, expected, a.clock().UTC())
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) DetachWorkspaceBinding(ctx context.Context, id string, expected int) (handoffreport.WorkspaceBinding, error) {
	var result handoffreport.WorkspaceBinding
	err := a.runtime.Operation(ctx, func(ctx context.Context) error {
		var err error
		result, err = a.backend.DetachWorkspaceBinding(ctx, id, expected)
		return err
	})
	return result, err
}

func (a *HandoffReportApplication) GetReport(ctx context.Context, input GetHandoffReport) (handoffreport.Report, error) {
	var result handoffreport.Report
	err := a.runtime.ScopedRead(ctx, input.ScopeID, func(ctx context.Context, scope string) error {
		_, _, _, _, normalized, _, err := normalizeHandoffReportPeriod(input.Period)
		if err != nil {
			return err
		}
		if normalized != nil && input.Period.Timezone == nil {
			normalized["timezone"] = "UTC"
		}
		project, err := handoffreport.NewProjectDescriptor(
			"unused", "unused", scope, nil, handoffreport.LocaleChinese, "UTC", handoffreport.CatalogIncluded, 1,
		)
		if err != nil {
			return err
		}
		workstream, err := handoffreport.NewWorkstreamDescriptor(
			scope, "unused", nil, scope, handoffreport.WorkstreamOther, handoffreport.CatalogIncluded, nil, nil, 1,
		)
		if err != nil {
			return err
		}
		includeEvidenceChecks := input.IncludeEvidenceChecks
		result, err = a.reports.Generate(ctx, handoffreport.GenerateInput{Project: project, Workstreams: []handoffreport.WorkstreamDescriptor{workstream}, Locale: input.Locale, IncludeEvidenceChecks: &includeEvidenceChecks, Activities: nil, ActivityCursor: 0, ActivityCoverage: handoffreport.ActivityNotConfigured, GeneratedAt: a.clock().UTC(), Format: input.Format, ReportKind: func() handoffreport.ReportKind {
			if input.Period == nil {
				return handoffreport.ReportHandoff
			}
			return handoffreport.ReportPeriodic
		}(), NormalizedFilters: map[string]any{}, NormalizedPeriod: normalized, PeriodComparison: nil})
		return err
	})
	return result, err
}

func normalizeHandoffReportPeriod(period *HandoffReportPeriod) (start, end, previousStart, previousEnd *time.Time, normalized map[string]any, comparison *handoffreport.PeriodComparison, err error) {
	if period == nil {
		return
	}
	startValue, endValue := period.Start.UTC(), period.End.UTC()
	if !startValue.Before(endValue) {
		err = &handoffreport.CatalogArgumentError{Field: "period", Detail: "start must precede end"}
		return
	}
	if endValue.Sub(startValue) > 366*24*time.Hour {
		err = &handoffreport.CatalogArgumentError{Field: "period", Detail: "must not exceed 366 days"}
		return
	}
	timezone := ""
	if period.Timezone != nil {
		timezone = *period.Timezone
		if _, loadErr := time.LoadLocation(timezone); loadErr != nil {
			err = &handoffreport.CatalogArgumentError{Field: "period.timezone", Detail: "must be a recognized IANA timezone"}
			return
		}
	}
	start, end = &startValue, &endValue
	normalized = map[string]any{"start": handoffreport.UTCText(startValue), "end": handoffreport.UTCText(endValue), "timezone": timezone, "compare_to_previous_period": period.CompareToPreviousPeriod}
	if period.CompareToPreviousPeriod {
		duration := endValue.Sub(startValue)
		previousStartValue, previousEndValue := startValue.Add(-duration), startValue
		previousStart, previousEnd = &previousStartValue, &previousEndValue
		placeholder := handoffreport.PeriodComparison{}
		comparison = &placeholder
	}
	return
}

func defaultHandoffReportID(prefix string) (string, error) {
	if prefix != "prj_" && prefix != "evt_" {
		return "", fmt.Errorf("unsupported Handoff Report ID prefix %q", prefix)
	}
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", ""), nil
}

// HandoffReportReader exposes only committed exact reads. Evidence checking is
// explicitly unavailable because reusing Continue would cross its trust and
// telemetry boundary.
type HandoffReportReader struct{ services HandoffServiceFactory }

func NewHandoffReportReader(services HandoffServiceFactory) (*HandoffReportReader, error) {
	if services == nil {
		return nil, errors.New("runtime: Handoff Report service factory must not be nil")
	}
	return &HandoffReportReader{services: services}, nil
}

func (r *HandoffReportReader) service(scope string) (*handoff.Service, error) {
	service, err := r.services(scope)
	if err != nil {
		return nil, err
	}
	if service == nil {
		return nil, &StateError{Code: "handoff"}
	}
	return service, nil
}

func (r *HandoffReportReader) Latest(ctx context.Context, scope string) (handoff.Handoff, bool, error) {
	service, err := r.service(scope)
	if err != nil {
		return handoff.Handoff{}, false, err
	}
	return service.Latest(ctx)
}

func (r *HandoffReportReader) Get(ctx context.Context, scope string, ref artifact.Ref) (handoff.Handoff, error) {
	service, err := r.service(scope)
	if err != nil {
		return handoff.Handoff{}, err
	}
	return service.Revision(ctx, ref)
}

func (r *HandoffReportReader) Revisions(ctx context.Context, scope string) ([]handoff.Handoff, error) {
	service, err := r.service(scope)
	if err != nil {
		return nil, err
	}
	return service.Revisions(ctx)
}

func (*HandoffReportReader) CheckEvidence(context.Context, string, artifact.Ref) ([]handoff.EvidenceCheck, error) {
	return nil, &handoffreport.EvidenceCheckUnavailableError{}
}
