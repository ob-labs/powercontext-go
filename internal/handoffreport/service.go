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

package handoffreport

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/internal/work"
)

type GenerateInput struct {
	Project               ProjectDescriptor
	Workstreams           []WorkstreamDescriptor
	Locale                *Locale
	IncludeEvidenceChecks *bool
	Activities            []ActivityEvent
	ActivityCursor        int64
	ActivityCoverage      ActivityCoverageStatus
	GeneratedAt           time.Time
	SelectionAttempts     int
	Format                Format
	ReportKind            ReportKind
	NormalizedFilters     map[string]any
	NormalizedPeriod      map[string]any
	PeriodComparison      *PeriodComparison
}

type Service struct {
	reader     HandoffReader
	continuity WorkContinuityReader
}

func NewService(reader HandoffReader, continuityReaders ...WorkContinuityReader) (*Service, error) {
	if reader == nil {
		return nil, errors.New("handoffreport: Handoff reader must not be nil")
	}
	if len(continuityReaders) > 1 {
		return nil, errors.New("handoffreport: at most one Work continuity reader may be configured")
	}
	var continuity WorkContinuityReader
	if len(continuityReaders) == 1 {
		if continuityReaders[0] == nil {
			return nil, errors.New("handoffreport: Work continuity reader must not be nil")
		}
		continuity = continuityReaders[0]
	}
	return &Service{reader: reader, continuity: continuity}, nil
}

func (s *Service) Generate(ctx context.Context, input GenerateInput) (Report, error) {
	ordered, err := validateGenerateInput(input)
	if err != nil {
		return Report{}, err
	}
	attempts := input.SelectionAttempts
	if attempts == 0 {
		attempts = DefaultSelectionAttempts
	}
	selection, err := SelectOptimisticStable(ctx, s.reader, ordered, attempts)
	if err != nil {
		return Report{}, err
	}
	activitiesByScope, unassigned := groupActivities(input.Activities, ordered)
	includeEvidenceChecks := true
	if input.IncludeEvidenceChecks != nil {
		includeEvidenceChecks = *input.IncludeEvidenceChecks
	}
	projected := make([]WorkstreamReport, 0, len(ordered))
	for index, descriptor := range ordered {
		entry := selection[index]
		activities := activitiesByScope[descriptor.ScopeID()]
		continuity, err := s.readContinuity(ctx, descriptor.ScopeID(), entry.HandoffRef())
		if err != nil {
			return Report{}, err
		}
		if entry.HandoffRef() == nil {
			status := ActivityNone
			if len(activities) > 0 {
				status = ActivityWithoutHandoff
			}
			projected = append(projected, WorkstreamReport{
				workstream: descriptor, continuity: continuity, activities: slices.Clone(activities), workStatus: WorkNoHandoff,
				reportingStatus: ReportingNoHandoff, activityStatus: status, observedActivityCount: len(activities),
			})
			continue
		}
		ref := entry.HandoffRef()
		selected, err := s.reader.Get(ctx, descriptor.ScopeID(), *ref)
		if err != nil {
			return Report{}, err
		}
		if selected.Ref() != *ref {
			return Report{}, &InconsistentError{ScopeID: descriptor.ScopeID()}
		}
		content := selected.Content()
		revisionCount, revisionHistory, err := s.revisionHistory(ctx, descriptor.ScopeID(), *ref)
		if err != nil {
			return Report{}, err
		}
		checks := []handoff.EvidenceCheck(nil)
		checked, evidenceUnavailable := false, false
		if includeEvidenceChecks {
			checks, err = s.reader.CheckEvidence(ctx, descriptor.ScopeID(), *ref)
			if err != nil {
				var unavailable *EvidenceCheckUnavailableError
				if !errors.As(err, &unavailable) {
					return Report{}, err
				}
				evidenceUnavailable = true
			} else {
				checked = true
			}
		}
		var relation *HandoffActivityRelation
		if len(activities) > 0 {
			value := RelationUnknown
			relation = &value
		}
		projected = append(projected, WorkstreamReport{
			workstream: descriptor, continuity: continuity, handoffRef: ref, content: &content,
			handoffRevisionCount: revisionCount, handoffHistoryTruncated: revisionCount > len(revisionHistory),
			handoffHistory: slices.Clone(revisionHistory),
			evidenceChecks: cloneEvidenceChecks(checks), evidenceChecked: checked,
			evidenceUnavailable: evidenceUnavailable, activities: slices.Clone(activities),
			workStatus:      WorkStatus(content.Disposition()),
			reportingStatus: reportingStatus(content.Omissions(), checks, checked, evidenceUnavailable),
			activityStatus:  activityStatus(activities), handoffActivityRelation: relation,
			observedActivityCount: len(activities),
		})
	}
	activitySelection := make([]string, 0, len(input.Activities))
	for _, item := range projected {
		for _, event := range item.activities {
			activitySelection = append(activitySelection, event.EventID())
		}
	}
	for _, event := range unassigned {
		activitySelection = append(activitySelection, event.EventID())
	}
	locale := input.Project.DefaultLocale()
	if input.Locale != nil {
		locale = *input.Locale
	}
	format := input.Format
	if format == "" {
		format = FormatJSON
	}
	kind := input.ReportKind
	if kind == "" {
		kind = ReportHandoff
	}
	renderer := "canonical-v1"
	if format == FormatMarkdown {
		renderer = "markdown-v1"
	}
	filters := input.NormalizedFilters
	if filters == nil {
		filters = map[string]any{}
	}
	activityCoverage := input.ActivityCoverage
	if activityCoverage == "" {
		activityCoverage = ActivityNotConfigured
	}
	report := Report{
		locale: locale, format: format, reportKind: kind, rendererVersion: renderer,
		generatedAt: input.GeneratedAt.UTC(), project: input.Project,
		normalizedFilters: cloneJSONMap(filters), normalizedPeriod: cloneJSONMap(input.NormalizedPeriod),
		periodComparison: cloneComparison(input.PeriodComparison), endSelection: slices.Clone(selection),
		activityCursor: input.ActivityCursor, activitySelection: activitySelection,
		coverage: deriveCoverage(projected, unassigned, activityCoverage), summary: deriveSummary(projected),
		unassignedActivity: slices.Clone(unassigned), workstreams: projected,
	}
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return FinalizeDigests(report)
}

func (s *Service) readContinuity(ctx context.Context, scope string, selected *artifact.Ref) (work.Continuity, error) {
	if s.continuity != nil {
		return s.continuity.Continuity(ctx, scope, selected)
	}
	// This is the same explicit empty projection used by Python when no Work
	// adapter is configured. It does not infer history from Handoff revisions.
	return work.ProjectContinuity(scope, nil, nil)
}

func (s *Service) revisionHistory(
	ctx context.Context,
	scope string,
	selected artifact.Ref,
) (int, []HandoffRevisionSummary, error) {
	revisions, err := s.reader.Revisions(ctx, scope)
	if err != nil {
		return 0, nil, err
	}
	lifecycle := make([]handoff.Handoff, 0, len(revisions))
	selectedIndex := -1
	var previous int64
	for _, revision := range revisions {
		ref := revision.Ref()
		if ref.Family() != selected.Family() || ref.ID() != selected.ID() {
			continue
		}
		if ref.Revision() <= previous {
			return 0, nil, &InconsistentError{ScopeID: scope}
		}
		previous = ref.Revision()
		lifecycle = append(lifecycle, revision)
		if ref == selected {
			selectedIndex = len(lifecycle) - 1
		}
	}
	if selectedIndex < 0 {
		return 0, nil, &InconsistentError{ScopeID: scope}
	}
	selectedHistory := lifecycle[:selectedIndex+1]
	recent := selectedHistory
	if len(recent) > MaxReportHandoffHistory {
		recent = recent[len(recent)-MaxReportHandoffHistory:]
	}
	result := make([]HandoffRevisionSummary, len(recent))
	for index, revision := range recent {
		content := revision.Content()
		var nextExcerpt *string
		if next := content.NextAction(); next != nil {
			value := historyExcerpt(next.Text())
			nextExcerpt = &value
		}
		result[index] = HandoffRevisionSummary{
			reference: revision.Ref(), objectiveExcerpt: historyExcerpt(content.Objective()),
			disposition: content.Disposition(), nextActionExcerpt: nextExcerpt,
			stateCount: len(content.State()), omissionCount: len(content.Omissions()),
		}
	}
	return len(selectedHistory), result, nil
}

func historyExcerpt(value string) string {
	compact := strings.Join(strings.Fields(value), " ")
	runes := []rune(compact)
	if len(runes) <= MaxReportHistoryExcerptLength {
		return compact
	}
	return strings.TrimRightFunc(string(runes[:MaxReportHistoryExcerptLength-1]), unicode.IsSpace) + "…"
}

func validateGenerateInput(input GenerateInput) ([]WorkstreamDescriptor, error) {
	if err := input.Project.Validate(); err != nil {
		return nil, err
	}
	if input.Locale != nil && *input.Locale != LocaleChinese && *input.Locale != LocaleEnglish {
		return nil, fieldError("locale", "has an unsupported value")
	}
	if input.ActivityCursor < 0 {
		return nil, fieldError("activity_cursor", "must be non-negative")
	}
	if len(input.Workstreams) > MaxReportWorkstreams {
		return nil, fieldError("workstreams", fmt.Sprintf("a Handoff Report selects at most %d Workstreams", MaxReportWorkstreams))
	}
	if len(input.Activities) > MaxReportActivities {
		return nil, fieldError("activities", fmt.Sprintf("a Handoff Report selects at most %d Activity Events", MaxReportActivities))
	}
	if input.GeneratedAt.IsZero() {
		return nil, fieldError("generated_at", "must be set")
	}
	if input.Format != "" && input.Format != FormatJSON && input.Format != FormatMarkdown {
		return nil, fieldError("format", "has an unsupported value")
	}
	if input.ReportKind != "" && input.ReportKind != ReportHandoff && input.ReportKind != ReportPeriodic {
		return nil, fieldError("report_kind", "has an unsupported value")
	}
	if input.ReportKind == ReportPeriodic && input.NormalizedPeriod == nil {
		return nil, fieldError("normalized_period", "is required for periodic reports")
	}
	if (input.ReportKind == ReportHandoff || input.ReportKind == "") && (input.NormalizedPeriod != nil || input.PeriodComparison != nil) {
		return nil, fieldError("normalized_period", "is invalid for point-in-time reports")
	}
	if input.PeriodComparison != nil {
		if err := input.PeriodComparison.Validate(); err != nil {
			return nil, err
		}
	}
	if input.ActivityCoverage != "" && input.ActivityCoverage != ActivityNotConfigured &&
		input.ActivityCoverage != ActivityCaptured && input.ActivityCoverage != ActivityUnavailable {
		return nil, fieldError("activity_coverage", "has an unsupported value")
	}
	if _, err := normalizeCanonical(input.NormalizedFilters); err != nil {
		return nil, err
	}
	if _, err := normalizeCanonical(input.NormalizedPeriod); err != nil {
		return nil, err
	}
	ordered := slices.Clone(input.Workstreams)
	slices.SortFunc(ordered, func(a, b WorkstreamDescriptor) int {
		if a.ScopeID() < b.ScopeID() {
			return -1
		}
		if a.ScopeID() > b.ScopeID() {
			return 1
		}
		return 0
	})
	for i, item := range ordered {
		if err := item.Validate(); err != nil {
			return nil, err
		}
		if item.ProjectID() != input.Project.ProjectID() {
			return nil, fieldError("workstreams", "every Workstream must belong to the requested Project")
		}
		if i > 0 && ordered[i-1].ScopeID() == item.ScopeID() {
			return nil, fieldError("scope_id", "Workstream values must be unique")
		}
	}
	for _, event := range input.Activities {
		if err := event.Validate(); err != nil {
			return nil, err
		}
		if event.ProjectID() != input.Project.ProjectID() {
			return nil, fieldError("activities", "every Activity Event must belong to the requested Project")
		}
	}
	return ordered, nil
}

func groupActivities(events []ActivityEvent, workstreams []WorkstreamDescriptor) (map[string][]ActivityEvent, []ActivityEvent) {
	known := map[string]struct{}{}
	for _, item := range workstreams {
		known[item.ScopeID()] = struct{}{}
	}
	ordered := slices.Clone(events)
	slices.SortFunc(ordered, CompareActivities)
	grouped := map[string][]ActivityEvent{}
	unassigned := []ActivityEvent{}
	for _, event := range ordered {
		scope := event.ScopeID()
		if scope == nil {
			unassigned = append(unassigned, event)
			continue
		}
		if _, ok := known[*scope]; !ok {
			unassigned = append(unassigned, event)
			continue
		}
		grouped[*scope] = append(grouped[*scope], event)
	}
	return grouped, unassigned
}

func reportingStatus(omissions []handoff.Omission, checks []handoff.EvidenceCheck, checked, unavailable bool) ReportingStatus {
	if unavailable {
		return ReportingEvidenceMissing
	}
	if checked {
		for _, check := range checks {
			if check.Status() == handoff.EvidenceUnavailable {
				return ReportingEvidenceMissing
			}
		}
	}
	if len(omissions) > 0 {
		return ReportingWithOmissions
	}
	return ReportingReported
}

func activityStatus(events []ActivityEvent) ActivityStatus {
	if len(events) == 0 {
		return ActivityNone
	}
	for _, event := range events {
		if event.TimeBasis() != TimeCurrentOnly {
			return ActivityUnknown
		}
	}
	return ActivityCurrentOnly
}

func deriveCoverage(items []WorkstreamReport, unassigned []ActivityEvent, status ActivityCoverageStatus) Coverage {
	result := Coverage{TotalIncludedWorkstreams: len(items), CatalogMatchedWorkstreams: len(items), SelectedWorkstreams: len(items), UnassignedActivityCount: len(unassigned), UnassignedActivityEvents: len(unassigned), ActivityCoverage: status}
	for _, item := range items {
		if item.handoffRef == nil {
			result.MissingHandoffWorkstreams++
		}
		if item.reportingStatus == ReportingWithOmissions {
			result.ReportedWithOmissions++
		}
		if item.handoffRef != nil && !item.evidenceChecked {
			result.UncheckedEvidenceWorkstreams++
		}
		if item.reportingStatus == ReportingEvidenceMissing {
			result.UnavailableEvidenceWorkstreams++
		}
		if item.activityStatus == ActivityWithoutHandoff {
			result.ActivityWithoutHandoffWorkstreams++
		}
		if item.handoffActivityRelation != nil && *item.handoffActivityRelation == RelationAfterHandoff {
			result.ActivityAfterHandoffWorkstreams++
		}
		for _, event := range item.activities {
			if event.EffectivePeriodTime() == nil {
				result.UnknownTimeEvents++
			}
		}
	}
	for _, event := range unassigned {
		if event.EffectivePeriodTime() == nil {
			result.UnknownTimeEvents++
		}
	}
	return result
}

func deriveSummary(items []WorkstreamReport) Summary {
	var result Summary
	for _, item := range items {
		switch item.workStatus {
		case WorkContinuable:
			result.ContinuableCount++
		case WorkBlocked:
			result.BlockedCount++
		case WorkComplete:
			result.CompleteCount++
		case WorkNoHandoff:
			result.NoHandoffCount++
		}
	}
	return result
}

func cloneComparison(value *PeriodComparison) *PeriodComparison {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
