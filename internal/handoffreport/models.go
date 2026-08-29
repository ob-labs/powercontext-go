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
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/ob-labs/powercontext-go/artifact"
)

const (
	ProjectSchemaVersion            = "powercontext.project.v1"
	WorkstreamSchemaVersion         = "powercontext.workstream.v1"
	ActivitySchemaVersion           = "powercontext.handoff-report-activity.v1"
	ActivityTrust                   = "untrusted_observation"
	MaxReportIDLength               = 256
	MaxProjectKeyLength             = 64
	MaxWorkstreamKeyLength          = 64
	MaxReportTitleLength            = 256
	MaxReportDescriptionLength      = 2_000
	MaxReportLabelLength            = 128
	MaxReportProviderLength         = 64
	MaxReportActivitySourceLength   = 64
	MaxReportAgentLabelLength       = 128
	MaxReportSourceSummaryLength    = 2_000
	MaxReportExternalIDLength       = 256
	MaxReportURLLength              = 2_048
	MaxReportRepositoryIDLength     = 256
	MaxReportNormalizedRemoteLength = 2_048
	MaxReportSubpathLength          = 1_024
	MaxWorkspaceInstanceIDLength    = 256
	MaxScopeIDLength                = 256
	MaxReportExternalRefs           = 32
	MaxReportLabels                 = 32
	MaxReportEvidenceRefs           = 32
	DefaultCatalogPageSize          = 50
	MaxCatalogPageSize              = 100
	MaxReportWorkstreams            = 100
	MaxReportActivities             = 5_000
	MaxReportHandoffHistory         = 20
	MaxReportHistoryExcerptLength   = 240
	DefaultSelectionAttempts        = 3
	MaxSelectionAttempts            = 5
	MaxReportBytes                  = 10 * 1024 * 1024
)

type Locale string

const (
	LocaleChinese Locale = "zh-CN"
	LocaleEnglish Locale = "en"
)

type CatalogState string

const (
	CatalogIncluded CatalogState = "included"
	CatalogArchived CatalogState = "archived"
)

type WorkstreamKind string

const (
	WorkstreamFeature    WorkstreamKind = "feature"
	WorkstreamBug        WorkstreamKind = "bug"
	WorkstreamRefactor   WorkstreamKind = "refactor"
	WorkstreamOperations WorkstreamKind = "operations"
	WorkstreamResearch   WorkstreamKind = "research"
	WorkstreamOther      WorkstreamKind = "other"
)

type ExternalReferenceKind string

const (
	ExternalIssue       ExternalReferenceKind = "issue"
	ExternalTask        ExternalReferenceKind = "task"
	ExternalPullRequest ExternalReferenceKind = "pull_request"
	ExternalBranch      ExternalReferenceKind = "branch"
	ExternalFeature     ExternalReferenceKind = "feature"
	ExternalRelease     ExternalReferenceKind = "release"
	ExternalProgram     ExternalReferenceKind = "program"
	ExternalOther       ExternalReferenceKind = "other"
)

type ActivitySource string

const (
	ActivityHandoffObservation ActivitySource = "handoff_observation"
	ActivityGitCommit          ActivitySource = "git_commit"
	ActivityGitWorktree        ActivitySource = "git_worktree"
	ActivityCodingSession      ActivitySource = "coding_session"
	ActivityOther              ActivitySource = "other"
)

type TimeBasis string

const (
	TimeSourceReported TimeBasis = "source_reported"
	TimeHostObserved   TimeBasis = "host_observed"
	TimeFirstSeen      TimeBasis = "first_seen"
	TimeCurrentOnly    TimeBasis = "current_only"
	TimeUnknown        TimeBasis = "unknown"
)

type RepositoryProvider string

const (
	RepositoryGitHub RepositoryProvider = "github"
	RepositoryGitLab RepositoryProvider = "gitlab"
	RepositoryLocal  RepositoryProvider = "local"
	RepositoryOther  RepositoryProvider = "other"
)

type WorkspaceBindingState string

const (
	WorkspaceConfirmed WorkspaceBindingState = "confirmed"
	WorkspaceDetached  WorkspaceBindingState = "detached"
)

type SelectionStatus string

const (
	SelectionSelected  SelectionStatus = "selected"
	SelectionNoHandoff SelectionStatus = "no_handoff"
)

type Page[T any] struct {
	Items      []T
	NextCursor *string
}
type StoredActivity struct {
	Event  ActivityEvent
	Cursor int64
}
type ActivityPage struct {
	Items         []ActivityEvent
	NextCursor    *int64
	HighWatermark int64
}

func NormalizeSortText(value string) string { return cases.Fold().String(norm.NFC.String(value)) }
func UTCText(value time.Time) string        { return value.UTC().Format("2006-01-02T15:04:05.000000Z") }

func TimestampText(value time.Time) string { return value.Format("2006-01-02T15:04:05.000000Z07:00") }

func JSONTimestampText(value time.Time) string {
	value = value.Truncate(time.Microsecond)
	layout := "2006-01-02T15:04:05"
	if value.Nanosecond() != 0 {
		layout += ".000000"
	}
	_, offset := value.Zone()
	if offset == 0 {
		return value.Format(layout) + "Z"
	}
	return value.Format(layout + "Z07:00")
}

func CompareWorkstreams(left, right WorkstreamDescriptor) int {
	leftTitle, rightTitle := NormalizeSortText(left.Title()), NormalizeSortText(right.Title())
	if leftTitle < rightTitle {
		return -1
	}
	if leftTitle > rightTitle {
		return 1
	}
	if left.ScopeID() < right.ScopeID() {
		return -1
	}
	if left.ScopeID() > right.ScopeID() {
		return 1
	}
	return 0
}

func CompareActivities(left, right ActivityEvent) int {
	leftTime, rightTime := left.EffectivePeriodTime(), right.EffectivePeriodTime()
	if leftTime == nil && rightTime != nil {
		return 1
	}
	if leftTime != nil && rightTime == nil {
		return -1
	}
	if leftTime != nil && !leftTime.Equal(*rightTime) {
		if leftTime.Before(*rightTime) {
			return -1
		}
		return 1
	}
	if !left.ObservedAt().Equal(right.ObservedAt()) {
		if left.ObservedAt().Before(right.ObservedAt()) {
			return -1
		}
		return 1
	}
	if left.EventID() < right.EventID() {
		return -1
	}
	if left.EventID() > right.EventID() {
		return 1
	}
	return 0
}

func CompareSelections(left, right SelectionEntry) int {
	if left.ScopeID() < right.ScopeID() {
		return -1
	}
	if left.ScopeID() > right.ScopeID() {
		return 1
	}
	return 0
}

func normalizeRepositoryRemote(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	if !strings.Contains(*value, "://") {
		v := strings.TrimRight(*value, "/")
		return &v, nil
	}
	parsed, err := url.Parse(*value)
	if err != nil {
		return nil, fieldError("normalized_remote", "must contain a valid URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fieldError("normalized_remote", "must not contain credentials or query fragments")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return nil, fieldError("normalized_remote", "must contain a host")
	}
	if parsed.Port() != "" {
		host += ":" + parsed.Port()
	}
	parts := make([]string, 0)
	for _, part := range strings.Split(parsed.Path, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	result := strings.ToLower(parsed.Scheme) + "://" + host + "/" + strings.Join(parts, "/")
	return &result, nil
}

func normalizeRepositorySubpath(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	candidate := strings.ReplaceAll(*value, "\\", "/")
	for _, part := range strings.Split(candidate, "/") {
		if part == ".." {
			return nil, fieldError("subpath", "must not contain parent traversal")
		}
	}
	normalized := path.Clean(candidate)
	if normalized == "" || normalized == "." {
		normalized = "."
	}
	if strings.HasPrefix(normalized, "/") {
		return nil, fieldError("subpath", "must be relative")
	}
	return &normalized, nil
}

func requireText(field, value string, max int) error {
	if strings.TrimSpace(value) == "" {
		return fieldError(field, "must contain non-whitespace text")
	}
	if strings.TrimSpace(value) != value {
		return fieldError(field, "must not contain leading or trailing whitespace")
	}
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max {
		return fieldError(field, fmt.Sprintf("must not exceed %d characters", max))
	}
	return nil
}

func requireOptionalText(field string, value *string, max int) error {
	if value == nil {
		return nil
	}
	return requireText(field, *value, max)
}

func fieldError(field, detail string) error {
	return &CatalogArgumentError{Field: field, Detail: detail}
}

func invalidActivity(field, detail string) error {
	return &InvalidActivityEventError{Field: field, Detail: detail}
}

func activityField(err error) error {
	if e, ok := err.(*CatalogArgumentError); ok {
		return invalidActivity(e.Field, e.Detail)
	}
	return err
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneExternal(value *ExternalReference) *ExternalReference {
	if value == nil {
		return nil
	}
	copy := *value
	copy.url = cloneString(value.url)
	return &copy
}

func cloneAgent(value *ActivityAgent) *ActivityAgent {
	if value == nil {
		return nil
	}
	copy := *value
	copy.provider = cloneString(value.provider)
	copy.label = cloneString(value.label)
	return &copy
}

func cloneVCS(value *ActivityVCSContext) *ActivityVCSContext {
	if value == nil {
		return nil
	}
	copy := *value
	copy.branch = cloneString(value.branch)
	copy.headRevision = cloneString(value.headRevision)
	return &copy
}

func cloneArtifactRef(value *artifact.Ref) *artifact.Ref {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func optionalKey(value *string) string {
	if value == nil {
		return "\x00"
	}
	return *value
}

func artifactRefMap(value *artifact.Ref) any {
	if value == nil {
		return nil
	}
	return map[string]any{"family": value.Family(), "artifact_id": value.ID(), "revision": value.Revision()}
}

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func validExternalKind(v ExternalReferenceKind) bool {
	return slices.Contains([]ExternalReferenceKind{ExternalIssue, ExternalTask, ExternalPullRequest, ExternalBranch, ExternalFeature, ExternalRelease, ExternalProgram, ExternalOther}, v)
}

func validRepositoryProvider(v RepositoryProvider) bool {
	return slices.Contains([]RepositoryProvider{RepositoryGitHub, RepositoryGitLab, RepositoryLocal, RepositoryOther}, v)
}

func validWorkstreamKind(v WorkstreamKind) bool {
	return slices.Contains([]WorkstreamKind{WorkstreamFeature, WorkstreamBug, WorkstreamRefactor, WorkstreamOperations, WorkstreamResearch, WorkstreamOther}, v)
}

func validActivitySource(v ActivitySource) bool {
	return slices.Contains([]ActivitySource{ActivityHandoffObservation, ActivityGitCommit, ActivityGitWorktree, ActivityCodingSession, ActivityOther}, v)
}

func validTimeBasis(v TimeBasis) bool {
	return slices.Contains([]TimeBasis{TimeSourceReported, TimeHostObserved, TimeFirstSeen, TimeCurrentOnly, TimeUnknown}, v)
}
