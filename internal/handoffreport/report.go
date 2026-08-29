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
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
)

type Format string

const (
	ReportSchemaVersion        = "powercontext.handoff-report.v1"
	ReportTrust                = "untrusted_history"
	ReportSelectionConsistency = "optimistic_stable"
)

const (
	FormatJSON     Format = "json"
	FormatMarkdown Format = "markdown"
)

type ReportKind string

const (
	ReportHandoff  ReportKind = "handoff"
	ReportPeriodic ReportKind = "periodic"
)

type ActivityCoverageStatus string

const (
	ActivityNotConfigured ActivityCoverageStatus = "not_configured"
	ActivityCaptured      ActivityCoverageStatus = "captured"
	ActivityUnavailable   ActivityCoverageStatus = "unavailable"
)

type (
	WorkStatus              string
	ActivityStatus          string
	ReportingStatus         string
	HandoffActivityRelation string
)

const (
	WorkContinuable WorkStatus = "continuable"
	WorkBlocked     WorkStatus = "blocked"
	WorkComplete    WorkStatus = "complete"
	WorkNoHandoff   WorkStatus = "no_handoff"

	ActivityNone           ActivityStatus = "no_observed_activity"
	ActivityAfterHandoff   ActivityStatus = "activity_after_handoff"
	ActivityWithoutHandoff ActivityStatus = "activity_without_handoff"
	ActivityCurrentOnly    ActivityStatus = "current_only"
	ActivityUnknown        ActivityStatus = "unknown"

	ReportingReported        ReportingStatus         = "reported"
	ReportingWithOmissions   ReportingStatus         = "reported_with_omissions"
	ReportingEvidenceMissing ReportingStatus         = "evidence_unavailable"
	ReportingNoHandoff       ReportingStatus         = "no_handoff"
	RelationAfterHandoff     HandoffActivityRelation = "activity_after_handoff"
	RelationNoActivityAfter  HandoffActivityRelation = "no_observed_activity_after_handoff"
	RelationUnknown          HandoffActivityRelation = "unknown"
)

type Coverage struct {
	TotalIncludedWorkstreams          int
	CatalogMatchedWorkstreams         int
	SelectedWorkstreams               int
	MissingHandoffWorkstreams         int
	ReportedWithOmissions             int
	UncheckedEvidenceWorkstreams      int
	UnavailableEvidenceWorkstreams    int
	ActivityWithoutHandoffWorkstreams int
	ActivityAfterHandoffWorkstreams   int
	UnknownTimeEvents                 int
	UnassignedActivityCount           int
	UnassignedActivityEvents          int
	ActivityCoverage                  ActivityCoverageStatus
}

type Summary struct {
	ContinuableCount int
	BlockedCount     int
	CompleteCount    int
	NoHandoffCount   int
}

type PeriodComparison struct {
	PreviousStart         time.Time
	PreviousEnd           time.Time
	CurrentActivityCount  int
	PreviousActivityCount int
	ActivityDelta         int
}

type HandoffRevisionSummary struct {
	reference         artifact.Ref
	objectiveExcerpt  string
	disposition       handoff.Disposition
	nextActionExcerpt *string
	stateCount        int
	omissionCount     int
}

func (v HandoffRevisionSummary) Reference() artifact.Ref          { return v.reference }
func (v HandoffRevisionSummary) ObjectiveExcerpt() string         { return v.objectiveExcerpt }
func (v HandoffRevisionSummary) Disposition() handoff.Disposition { return v.disposition }
func (v HandoffRevisionSummary) NextActionExcerpt() *string {
	return cloneString(v.nextActionExcerpt)
}
func (v HandoffRevisionSummary) StateCount() int    { return v.stateCount }
func (v HandoffRevisionSummary) OmissionCount() int { return v.omissionCount }
func (v HandoffRevisionSummary) Validate() error {
	if err := v.reference.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(v.objectiveExcerpt) == "" || utf8.RuneCountInString(v.objectiveExcerpt) > MaxReportHistoryExcerptLength {
		return fieldError("objective_excerpt", "must be non-empty and within the Handoff history excerpt limit")
	}
	if v.disposition != handoff.Continuable && v.disposition != handoff.Blocked && v.disposition != handoff.Complete {
		return fieldError("disposition", "has an unsupported value")
	}
	if v.nextActionExcerpt != nil && utf8.RuneCountInString(*v.nextActionExcerpt) > MaxReportHistoryExcerptLength {
		return fieldError("next_action_excerpt", "exceeds the Handoff history excerpt limit")
	}
	if v.stateCount < 1 || v.omissionCount < 0 {
		return fieldError("handoff_history", "contains invalid item counts")
	}
	return nil
}

func NewPeriodComparison(previousStart, previousEnd time.Time, current, previous int) (PeriodComparison, error) {
	value := PeriodComparison{
		PreviousStart: previousStart.UTC(), PreviousEnd: previousEnd.UTC(),
		CurrentActivityCount: current, PreviousActivityCount: previous,
		ActivityDelta: current - previous,
	}
	if err := value.Validate(); err != nil {
		return PeriodComparison{}, err
	}
	return value, nil
}

func (v PeriodComparison) Validate() error {
	if !v.PreviousStart.Before(v.PreviousEnd) {
		return fieldError("period", "previous period start must precede its end")
	}
	if v.CurrentActivityCount < 0 || v.PreviousActivityCount < 0 {
		return fieldError("period", "activity counts must be non-negative")
	}
	if v.ActivityDelta != v.CurrentActivityCount-v.PreviousActivityCount {
		return fieldError("activity_delta", "must match current minus previous Activity count")
	}
	return nil
}

type Report struct {
	locale             Locale
	format             Format
	reportKind         ReportKind
	rendererVersion    string
	generatedAt        time.Time
	project            ProjectDescriptor
	normalizedFilters  map[string]any
	normalizedPeriod   map[string]any
	periodComparison   *PeriodComparison
	baselineSelection  []SelectionEntry
	baselinePresent    bool
	endSelection       []SelectionEntry
	activityCursor     int64
	activitySelection  []string
	selectionDigest    string
	reportDigest       string
	coverage           Coverage
	summary            Summary
	unassignedActivity []ActivityEvent
	workstreams        []WorkstreamReport
}

func (v Report) Locale() Locale                    { return v.locale }
func (v Report) Format() Format                    { return v.format }
func (v Report) ReportKind() ReportKind            { return v.reportKind }
func (v Report) RendererVersion() string           { return v.rendererVersion }
func (v Report) GeneratedAt() time.Time            { return v.generatedAt }
func (v Report) Project() ProjectDescriptor        { return v.project }
func (v Report) ProjectRevision() int              { return v.project.Version() }
func (v Report) NormalizedFilters() map[string]any { return cloneJSONMap(v.normalizedFilters) }
func (v Report) NormalizedPeriod() map[string]any  { return cloneJSONMap(v.normalizedPeriod) }
func (v Report) PeriodComparison() *PeriodComparison {
	if v.periodComparison == nil {
		return nil
	}
	copy := *v.periodComparison
	return &copy
}

func (v Report) BaselineSelection() ([]SelectionEntry, bool) {
	return slices.Clone(v.baselineSelection), v.baselinePresent
}
func (v Report) EndSelection() []SelectionEntry      { return slices.Clone(v.endSelection) }
func (v Report) ActivityCursor() int64               { return v.activityCursor }
func (v Report) ActivitySelection() []string         { return slices.Clone(v.activitySelection) }
func (v Report) SelectionDigest() string             { return v.selectionDigest }
func (v Report) ReportDigest() string                { return v.reportDigest }
func (v Report) Coverage() Coverage                  { return v.coverage }
func (v Report) Summary() Summary                    { return v.summary }
func (v Report) UnassignedActivity() []ActivityEvent { return slices.Clone(v.unassignedActivity) }
func (v Report) Workstreams() []WorkstreamReport     { return slices.Clone(v.workstreams) }

func (v Report) Schema() string               { return ReportSchemaVersion }
func (v Report) Trust() string                { return ReportTrust }
func (v Report) SelectionConsistency() string { return ReportSelectionConsistency }

func (v Report) Validate() error {
	if v.locale != LocaleChinese && v.locale != LocaleEnglish {
		return fieldError("locale", "has an unsupported value")
	}
	if v.format != FormatJSON && v.format != FormatMarkdown {
		return fieldError("format", "has an unsupported value")
	}
	if v.reportKind != ReportHandoff && v.reportKind != ReportPeriodic {
		return fieldError("report_kind", "has an unsupported value")
	}
	if v.generatedAt.IsZero() {
		return fieldError("generated_at", "must be set")
	}
	_, generatedOffset := v.generatedAt.Zone()
	if generatedOffset != 0 {
		return fieldError("generated_at", "must be UTC")
	}
	if err := v.project.Validate(); err != nil {
		return err
	}
	if v.normalizedFilters == nil {
		return fieldError("normalized_filters", "must be an object")
	}
	if _, err := normalizeCanonical(v.normalizedFilters); err != nil {
		return err
	}
	if _, err := normalizeCanonical(v.normalizedPeriod); err != nil {
		return err
	}
	if v.reportKind == ReportPeriodic && v.normalizedPeriod == nil {
		return fieldError("normalized_period", "a periodic report must contain a normalized period")
	}
	if v.reportKind == ReportHandoff && (v.normalizedPeriod != nil || v.periodComparison != nil) {
		return fieldError("normalized_period", "a point-in-time Handoff report cannot contain period values")
	}
	if v.periodComparison != nil {
		if v.reportKind != ReportPeriodic {
			return fieldError("period_comparison", "is only valid for a periodic report")
		}
		if err := v.periodComparison.Validate(); err != nil {
			return err
		}
	}
	if len(v.baselineSelection) > MaxReportWorkstreams || len(v.endSelection) > MaxReportWorkstreams ||
		len(v.workstreams) > MaxReportWorkstreams {
		return fieldError("workstreams", fmt.Sprintf("must not exceed %d items", MaxReportWorkstreams))
	}
	for _, entry := range v.baselineSelection {
		if err := entry.Validate(); err != nil {
			return err
		}
	}
	if len(v.endSelection) != len(v.workstreams) {
		return fieldError("end_selection", "must exactly match Workstream reports")
	}
	selectionScopes := make(map[string]struct{}, len(v.endSelection))
	reportScopes := make(map[string]struct{}, len(v.workstreams))
	for index, item := range v.workstreams {
		if err := item.Validate(); err != nil {
			return err
		}
		entry := v.endSelection[index]
		if err := entry.Validate(); err != nil {
			return err
		}
		if _, exists := selectionScopes[entry.ScopeID()]; exists {
			return fieldError("end_selection", "Handoff Report selection scopes must be unique")
		}
		selectionScopes[entry.ScopeID()] = struct{}{}
		if _, exists := reportScopes[item.workstream.ScopeID()]; exists {
			return fieldError("workstreams", "Handoff Report Workstream scopes must be unique")
		}
		reportScopes[item.workstream.ScopeID()] = struct{}{}
		if entry.ScopeID() != item.workstream.ScopeID() {
			return fieldError("end_selection", "Workstream reports must exactly match selection scope order")
		}
		if item.workstream.ProjectID() != v.project.ProjectID() {
			return fieldError("workstreams", "every Workstream report must belong to the Report Project")
		}
		if entry.WorkstreamRevision() != item.workstream.Version() {
			return fieldError("end_selection", "selection Workstream revision must match the projected descriptor")
		}
		left, right := entry.HandoffRef(), item.HandoffRef()
		if (left == nil) != (right == nil) || (left != nil && *left != *right) {
			return fieldError("end_selection", "selection Handoff reference must match the Workstream report")
		}
	}
	if err := validateReportActivity(v); err != nil {
		return err
	}
	if err := validateReportCoverage(v); err != nil {
		return err
	}
	if err := validateReportSummary(v); err != nil {
		return err
	}
	if !validDigest(v.selectionDigest) || !validDigest(v.reportDigest) {
		return fieldError("digest", "must use sha256:<64 lowercase hexadecimal characters>")
	}
	return nil
}

func cloneEvidenceChecks(values []handoff.EvidenceCheck) []handoff.EvidenceCheck {
	return slices.Clone(values)
}
