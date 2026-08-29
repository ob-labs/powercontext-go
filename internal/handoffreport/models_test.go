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

package handoffreport_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/internal/handoffreport"
)

var modelNow = time.Date(2026, time.August, 5, 4, 0, 0, 0, time.UTC)

func TestProjectDescriptorIsVersionedImmutableAndSerializesSchemaAlias(t *testing.T) {
	t.Parallel()
	description := "A durable project handoff"
	project := modelProject(t, &description, handoffreport.LocaleEnglish, "Asia/Shanghai")
	description = "changed outside"

	encoded, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["schema"] != handoffreport.ProjectSchemaVersion ||
		project.Schema() != handoffreport.ProjectSchemaVersion ||
		project.DefaultLocale() != handoffreport.LocaleEnglish ||
		project.Description() == nil || *project.Description() != "A durable project handoff" {
		t.Fatalf("Project descriptor or schema payload changed: project=%#v payload=%#v", project, payload)
	}
	returned := project.Description()
	*returned = "changed through accessor"
	if project.Description() == nil || *project.Description() != "A durable project handoff" {
		t.Fatal("Project description changed through its accessor")
	}
}

func TestProjectDescriptorRejectsInvalidTimezone(t *testing.T) {
	t.Parallel()
	for _, timezone := range []string{"", " Asia/Shanghai", "Not/A_Timezone", "Local"} {
		t.Run(timezone, func(t *testing.T) {
			t.Parallel()
			if _, err := handoffreport.NewProjectDescriptor(
				"prj_01K", "powercontext", "PowerContext", nil,
				handoffreport.LocaleChinese, timezone, handoffreport.CatalogIncluded, 1,
			); err == nil {
				t.Fatalf("timezone %q was accepted", timezone)
			}
		})
	}
}

func TestWorkstreamDescriptorReusesScopeIdentityAndBoundsMetadata(t *testing.T) {
	t.Parallel()
	issue, err := handoffreport.NewExternalReference(handoffreport.ExternalIssue, "github", "42", nil)
	if err != nil {
		t.Fatal(err)
	}
	key := "handoff-report"
	workstream, err := handoffreport.NewWorkstreamDescriptor(
		"scope-report", "prj_01K", &key, "Handoff Report", handoffreport.WorkstreamFeature,
		handoffreport.CatalogIncluded, []handoffreport.ExternalReference{issue}, []string{"report", "v1"}, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if workstream.ScopeID() != "scope-report" || len(workstream.ExternalRefs()) != 1 ||
		workstream.ExternalRefs()[0].ExternalID() != issue.ExternalID() {
		t.Fatalf("workstream = %#v", workstream)
	}
	if _, err := handoffreport.NewWorkstreamDescriptor(
		"scope-report", "prj_01K", nil, "Handoff Report", handoffreport.WorkstreamFeature,
		handoffreport.CatalogIncluded, nil, []string{"report", "report"}, 1,
	); err == nil {
		t.Fatal("duplicate Workstream labels were accepted")
	}
	if _, err := handoffreport.NewWorkstreamDescriptor(
		"scope-report", "prj_01K", nil, "Handoff Report", handoffreport.WorkstreamFeature,
		handoffreport.CatalogIncluded, []handoffreport.ExternalReference{issue, issue}, nil, 1,
	); err == nil {
		t.Fatal("duplicate Workstream external references were accepted")
	}
	if _, err := handoffreport.NewWorkstreamDescriptor(
		"scope-report", "prj_01K", nil, "Handoff Report", handoffreport.WorkstreamFeature,
		handoffreport.CatalogIncluded, []handoffreport.ExternalReference{{}}, nil, 1,
	); err == nil {
		t.Fatal("unvalidated zero ExternalReference was accepted")
	}
}

func TestActivityEventKeepsObservationUntrustedAndExplicitlyTimed(t *testing.T) {
	t.Parallel()
	provider, label := "codex", "agent-1"
	agent, err := handoffreport.NewActivityAgent(&provider, &label)
	if err != nil {
		t.Fatal(err)
	}
	branch, head := "handoff_report", "abc123"
	vcs, err := handoffreport.NewActivityVCSContext(&branch, &head)
	if err != nil {
		t.Fatal(err)
	}
	scope, session := "scope-report", "session-1"
	event := modelActivity(t, handoffreport.ActivityEventInput{
		ScopeID: &scope, Agent: &agent, SessionID: &session, VCSContext: &vcs,
	})
	period := event.EffectivePeriodTime()
	if event.Trust() != handoffreport.ActivityTrust || event.Schema() != handoffreport.ActivitySchemaVersion ||
		period == nil || !period.Equal(modelNow.Add(-time.Hour)) {
		t.Fatalf("activity event = %#v, period = %v", event, period)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["schema"] != handoffreport.ActivitySchemaVersion || payload["trust"] != handoffreport.ActivityTrust {
		t.Fatalf("activity payload = %#v", payload)
	}
}

func TestActivityEventJSONMatchesPythonDatetimePrecision(t *testing.T) {
	t.Parallel()
	observed := time.Date(2026, time.August, 5, 10, 0, 0, 456789123, time.UTC)
	occurred := time.Date(2026, time.August, 5, 9, 0, 0, 123456789, time.UTC)
	event, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{
		EventID: "evt-precision", ProjectID: "prj_01K", Source: handoffreport.ActivityGitCommit,
		SourceEventID: "commit-precision", OccurredAt: &occurred, ObservedAt: observed,
		TimeBasis: handoffreport.TimeSourceReported,
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["observed_at"] != "2026-08-05T10:00:00.456789Z" ||
		payload["occurred_at"] != "2026-08-05T09:00:00.123456Z" {
		t.Fatalf("Activity timestamps = observed:%v occurred:%v", payload["observed_at"], payload["occurred_at"])
	}
	if event.ObservedAt().Nanosecond() != 456789000 || event.OccurredAt().Nanosecond() != 123456000 {
		t.Fatalf("Activity precision was not reduced to Python microseconds: %#v", event)
	}
}

func TestActivityEventDoesNotInventPeriodTime(t *testing.T) {
	t.Parallel()
	current := modelActivity(t, handoffreport.ActivityEventInput{
		EventID: "evt-current", Source: handoffreport.ActivityGitWorktree,
		SourceEventID: "worktree-current", TimeBasis: handoffreport.TimeCurrentOnly,
	})
	unknown := modelActivity(t, handoffreport.ActivityEventInput{
		EventID: "evt-unknown", Source: handoffreport.ActivityCodingSession,
		SourceEventID: "unknown-session", TimeBasis: handoffreport.TimeUnknown,
	})
	if current.EffectivePeriodTime() != nil || unknown.EffectivePeriodTime() != nil {
		t.Fatal("current-only or unknown activity invented a reportable period time")
	}
}

func TestActivityEventValidatesTimestampBasisAndUTCObservation(t *testing.T) {
	t.Parallel()
	if _, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{
		EventID: "evt-1", ProjectID: "prj_01K", Source: handoffreport.ActivityGitCommit,
		SourceEventID: "commit-abc", ObservedAt: modelNow, TimeBasis: handoffreport.TimeSourceReported,
	}); err == nil {
		t.Fatal("source-reported activity without occurred_at was accepted")
	}
	occurred := modelNow.Add(-time.Hour)
	if _, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{
		EventID: "evt-1", ProjectID: "prj_01K", Source: handoffreport.ActivityGitCommit,
		SourceEventID: "commit-abc", OccurredAt: &occurred, ObservedAt: modelNow, TimeBasis: handoffreport.TimeFirstSeen,
	}); err == nil {
		t.Fatal("occurred_at outside source-reported activity was accepted")
	}
	plusEight := modelNow.In(time.FixedZone("UTC+8", 8*60*60))
	if _, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{
		EventID: "evt-1", ProjectID: "prj_01K", Source: handoffreport.ActivityGitCommit,
		SourceEventID: "commit-abc", OccurredAt: &occurred, ObservedAt: plusEight,
		TimeBasis: handoffreport.TimeSourceReported,
	}); err == nil {
		t.Fatal("non-UTC observed_at was accepted")
	}

	for name, timestamps := range map[string][2]string{
		"non-UTC observation": {"2026-08-05T12:00:00+08:00", "2026-08-05T03:00:00Z"},
		"naive occurrence":    {"2026-08-05T04:00:00Z", "2026-08-05T03:00:00"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			observedAt, occurredAt := timestamps[0], timestamps[1]
			payload := map[string]any{
				"schema": handoffreport.ActivitySchemaVersion, "event_id": "evt-1", "project_id": "prj_01K",
				"source": handoffreport.ActivityGitCommit, "source_event_id": "commit-abc",
				"occurred_at": occurredAt, "observed_at": observedAt,
				"time_basis": handoffreport.TimeSourceReported, "trust": handoffreport.ActivityTrust,
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var event handoffreport.ActivityEvent
			if err := json.Unmarshal(encoded, &event); err == nil {
				t.Fatal("invalid timestamp payload was accepted")
			}
		})
	}
}

func TestReportSelectionEntryRequiresExactHandoffOrExplicitAbsence(t *testing.T) {
	t.Parallel()
	handoffRef, err := artifact.NewRef("handoff", "handoff-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := handoffreport.NewSelectionEntry("scope-report", 3, handoffreport.SelectionSelected, &handoffRef)
	if err != nil {
		t.Fatal(err)
	}
	absent, err := handoffreport.NewSelectionEntry("scope-empty", 1, handoffreport.SelectionNoHandoff, nil)
	if err != nil {
		t.Fatal(err)
	}
	if selected.HandoffRef() == nil || *selected.HandoffRef() != handoffRef || absent.HandoffRef() != nil {
		t.Fatalf("selection values = %#v %#v", selected, absent)
	}
	if _, selectionErr := handoffreport.NewSelectionEntry("scope-report", 3, handoffreport.SelectionSelected, nil); selectionErr == nil {
		t.Fatal("selected entry without a Handoff was accepted")
	}
	if _, selectionErr := handoffreport.NewSelectionEntry("scope-report", 3, handoffreport.SelectionNoHandoff, &handoffRef); selectionErr == nil {
		t.Fatal("no_handoff entry containing a Handoff was accepted")
	}
	memoryRef, err := artifact.NewRef("memory", "memory-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, selectionErr := handoffreport.NewSelectionEntry("scope-report", 3, handoffreport.SelectionSelected, &memoryRef); selectionErr == nil {
		t.Fatal("selected entry referencing a non-Handoff family was accepted")
	}
}

func TestStableSortHelpersUseNormalizedTitleScopeAndEffectiveTime(t *testing.T) {
	t.Parallel()
	composed := modelWorkstream(t, "scope-2", "Café")
	decomposed := modelWorkstream(t, "scope-1", "Cafe\u0301")
	late := modelActivity(t, handoffreport.ActivityEventInput{EventID: "evt-late", SourceEventID: "late", OccurredAt: timePointer(modelNow)})
	early := modelActivity(t, handoffreport.ActivityEventInput{EventID: "evt-early", SourceEventID: "early", OccurredAt: timePointer(modelNow.Add(-24 * time.Hour))})
	current := modelActivity(t, handoffreport.ActivityEventInput{
		EventID: "evt-current", Source: handoffreport.ActivityGitWorktree,
		SourceEventID: "current", OccurredAt: nil, TimeBasis: handoffreport.TimeCurrentOnly,
	})
	handoffRef, err := artifact.NewRef("handoff", "handoff-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := handoffreport.NewSelectionEntry("scope-2", 1, handoffreport.SelectionSelected, &handoffRef)
	if err != nil {
		t.Fatal(err)
	}
	absent, err := handoffreport.NewSelectionEntry("scope-1", 1, handoffreport.SelectionNoHandoff, nil)
	if err != nil {
		t.Fatal(err)
	}

	if handoffreport.NormalizeSortText(composed.Title()) != handoffreport.NormalizeSortText(decomposed.Title()) {
		t.Fatal("canonically equivalent titles produced different normalized sort text")
	}
	workstreams := []handoffreport.WorkstreamDescriptor{composed, decomposed}
	activities := []handoffreport.ActivityEvent{current, late, early}
	selections := []handoffreport.SelectionEntry{selected, absent}
	slices.SortFunc(workstreams, handoffreport.CompareWorkstreams)
	slices.SortFunc(activities, handoffreport.CompareActivities)
	slices.SortFunc(selections, handoffreport.CompareSelections)
	if workstreams[0].ScopeID() != "scope-1" || activities[0].EventID() != "evt-early" ||
		activities[1].EventID() != "evt-late" || activities[2].EventID() != "evt-current" ||
		selections[0].ScopeID() != "scope-1" {
		t.Fatalf("stable orders = workstreams:%v activities:%v selections:%v", workstreams, activities, selections)
	}
}

func TestRepositoryRefNormalizesSafeRemoteAndRelativeSubpath(t *testing.T) {
	t.Parallel()
	remote := "HTTPS://GitHub.com/ob-labs/powercontext-go.git/"
	subpath := "./services/api"
	value, err := handoffreport.NewRepositoryRef(
		handoffreport.RepositoryGitHub,
		nil,
		&remote,
		&subpath,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := value.NormalizedRemote(); got == nil || *got != "https://github.com/ob-labs/powercontext-go.git" {
		t.Fatalf("normalized remote = %#v", got)
	}
	if got := value.Subpath(); got == nil || *got != "services/api" {
		t.Fatalf("normalized subpath = %#v", got)
	}

	credentials := "https://user@example.com/repo.git"
	if _, err := handoffreport.NewRepositoryRef(handoffreport.RepositoryGitHub, nil, &credentials, nil); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("credential-bearing remote error = %v", err)
	}
	parent := "../api"
	if _, err := handoffreport.NewRepositoryRef(handoffreport.RepositoryGitHub, nil, &remote, &parent); err == nil || !strings.Contains(err.Error(), "parent traversal") {
		t.Fatalf("parent traversal error = %v", err)
	}
	if _, err := handoffreport.NewRepositoryRef(handoffreport.RepositoryLocal, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "must contain") {
		t.Fatalf("empty local repository error = %v", err)
	}
}

func modelProject(t *testing.T, description *string, locale handoffreport.Locale, timezone string) handoffreport.ProjectDescriptor {
	t.Helper()
	value, err := handoffreport.NewProjectDescriptor(
		"prj_01K", "powercontext", "PowerContext", description,
		locale, timezone, handoffreport.CatalogIncluded, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func modelWorkstream(t *testing.T, scopeID, title string) handoffreport.WorkstreamDescriptor {
	t.Helper()
	value, err := handoffreport.NewWorkstreamDescriptor(
		scopeID, "prj_01K", nil, title, handoffreport.WorkstreamFeature,
		handoffreport.CatalogIncluded, nil, nil, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func modelActivity(t *testing.T, changes handoffreport.ActivityEventInput) handoffreport.ActivityEvent {
	t.Helper()
	occurred := modelNow.Add(-time.Hour)
	input := handoffreport.ActivityEventInput{
		EventID: "evt-1", ProjectID: "prj_01K", Source: handoffreport.ActivityGitCommit,
		SourceEventID: "commit-abc", OccurredAt: &occurred, ObservedAt: modelNow,
		TimeBasis: handoffreport.TimeSourceReported,
	}
	if changes.EventID != "" {
		input.EventID = changes.EventID
	}
	if changes.ProjectID != "" {
		input.ProjectID = changes.ProjectID
	}
	input.ScopeID = changes.ScopeID
	if changes.Source != "" {
		input.Source = changes.Source
	}
	if changes.SourceEventID != "" {
		input.SourceEventID = changes.SourceEventID
	}
	input.SourceRef = changes.SourceRef
	if changes.OccurredAt != nil || (changes.TimeBasis != "" && changes.TimeBasis != handoffreport.TimeSourceReported) {
		input.OccurredAt = changes.OccurredAt
	}
	if !changes.ObservedAt.IsZero() {
		input.ObservedAt = changes.ObservedAt
	}
	if changes.TimeBasis != "" {
		input.TimeBasis = changes.TimeBasis
	}
	input.Title, input.Summary = changes.Title, changes.Summary
	input.Agent, input.SessionID = changes.Agent, changes.SessionID
	input.VCSContext, input.EvidenceRefs = changes.VCSContext, changes.EvidenceRefs
	value, err := handoffreport.NewActivityEvent(input)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func timePointer(value time.Time) *time.Time { return &value }
