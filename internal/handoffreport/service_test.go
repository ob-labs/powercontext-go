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
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/internal/handoffreport"
	"github.com/ob-labs/powercontext-go/source"
)

func TestServiceFreezesExactHeadsAndBuildsCanonicalReport(t *testing.T) {
	t.Parallel()
	selected := reportHandoff(t, 1)
	reader := &reportReader{values: map[string]*handoff.Handoff{"scope-a": &selected, "scope-b": nil}, evidenceUnavailable: true}
	service, err := handoffreport.NewService(reader)
	if err != nil {
		t.Fatal(err)
	}
	project := domainProject(t)
	workstreams := []handoffreport.WorkstreamDescriptor{domainWorkstream(t, "scope-b"), domainWorkstream(t, "scope-a")}
	occurred := time.Date(2026, time.August, 5, 1, 0, 0, 0, time.UTC)
	activity := domainActivity(t, "evt-a", "scope-a", occurred)
	generated := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)
	report, err := service.Generate(context.Background(), handoffreport.GenerateInput{Project: project, Workstreams: workstreams, IncludeEvidenceChecks: reportBool(true), Activities: []handoffreport.ActivityEvent{activity}, ActivityCursor: 7, ActivityCoverage: handoffreport.ActivityCaptured, GeneratedAt: generated, Format: handoffreport.FormatJSON, ReportKind: handoffreport.ReportHandoff, NormalizedFilters: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	selection := report.EndSelection()
	if len(selection) != 2 || selection[0].ScopeID() != "scope-a" || selection[1].ScopeID() != "scope-b" {
		t.Fatalf("selection = %#v", selection)
	}
	summary := report.Summary()
	if summary.ContinuableCount != 1 || summary.NoHandoffCount != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	coverage := report.Coverage()
	if coverage.UnavailableEvidenceWorkstreams != 1 || coverage.ActivityWithoutHandoffWorkstreams != 0 || coverage.ActivityCoverage != handoffreport.ActivityCaptured {
		t.Fatalf("coverage = %#v", coverage)
	}
	const (
		pythonSelectionDigest = "sha256:d1a0d0bd8915f90be468f009b54a49ee89f3fba8f9f5fdec19c4912d6a43f557"
		pythonReportDigest    = "sha256:29084e12ef68a81ffa66845f6d843de2ad0cb230be11bbc1848b14e2e0e331ef"
	)
	if report.SelectionDigest() != pythonSelectionDigest || report.ReportDigest() != pythonReportDigest {
		t.Fatalf("digests = %q, %q; want frozen Python digests %q, %q", report.SelectionDigest(), report.ReportDigest(), pythonSelectionDigest, pythonReportDigest)
	}
	if reader.latestReads != 4 || reader.exactReads != 1 {
		t.Fatalf("reads latest=%d exact=%d", reader.latestReads, reader.exactReads)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["generated_at"] != "2026-08-06T00:00:00Z" {
		t.Fatalf("generated_at = %#v", payload["generated_at"])
	}
	items := payload["workstreams"].([]any)
	first := items[0].(map[string]any)
	if first["reporting_status"] != "evidence_unavailable" || first["evidence_checks"] != "not_checked" {
		t.Fatalf("selected projection = %#v", first)
	}
	fixtureBytes, err := os.ReadFile("../../test/conformance/testdata/python-v0.0.2/handoff-report-digests.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Report map[string]any `json:"report"`
	}
	if err := json.Unmarshal(fixtureBytes, &fixture); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload, fixture.Report) {
		t.Fatalf("Go report JSON differs from frozen Python report\nGo: %#v\nPython: %#v", payload, fixture.Report)
	}
	markdown, err := handoffreport.RenderMarkdown(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown, "# PowerContext 项目交接报告") ||
		!strings.Contains(markdown, "PowerContext &lt;script&gt;") || strings.Contains(markdown, "<script>") ||
		!strings.Contains(markdown, `activity_cursor: 7`) {
		t.Fatalf("unexpected Markdown projection:\n%s", markdown)
	}
}

func TestServiceProjectsRevisionHistoryOnlyThroughFrozenSelection(t *testing.T) {
	t.Parallel()
	first := reportHandoffRevision(t, 1, "Implement the report.", "Add the API.")
	selected := reportHandoffRevision(t, 2, "Second objective", "Review revision two")
	later := reportHandoffRevision(t, 3, "Concurrent third objective", "Do not include yet")
	reader := &reportReader{
		values:    map[string]*handoff.Handoff{"scope-a": &selected},
		histories: map[string][]handoff.Handoff{"scope-a": {first, selected, later}},
	}
	service, err := handoffreport.NewService(reader)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Generate(context.Background(), handoffreport.GenerateInput{
		Project: domainProject(t), Workstreams: []handoffreport.WorkstreamDescriptor{domainWorkstream(t, "scope-a")},
		GeneratedAt: time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	item := report.Workstreams()[0]
	history := item.HandoffHistory()
	if item.HandoffRevisionCount() != 2 || item.HandoffHistoryTruncated() || len(history) != 2 {
		t.Fatalf("revision projection = count %d, truncated %t, history %#v",
			item.HandoffRevisionCount(), item.HandoffHistoryTruncated(), history)
	}
	if history[0].Reference().Revision() != 1 || history[1].Reference().Revision() != 2 ||
		history[1].ObjectiveExcerpt() != "Second objective" {
		t.Fatalf("frozen history = %#v", history)
	}
	markdown, err := handoffreport.RenderMarkdown(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(markdown, "Concurrent third objective") {
		t.Fatal("report included a revision committed after its frozen selection")
	}
}

func TestServiceBoundsRevisionHistoryAndPreservesTotalCount(t *testing.T) {
	t.Parallel()
	revisions := make([]handoff.Handoff, 22)
	for index := range revisions {
		revision := int64(index + 1)
		revisions[index] = reportHandoffRevision(t, revision, "Objective "+decimal(int(revision)), "Next")
	}
	selected := revisions[len(revisions)-1]
	reader := &reportReader{
		values:    map[string]*handoff.Handoff{"scope-a": &selected},
		histories: map[string][]handoff.Handoff{"scope-a": revisions},
	}
	service, err := handoffreport.NewService(reader)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Generate(context.Background(), handoffreport.GenerateInput{
		Project: domainProject(t), Workstreams: []handoffreport.WorkstreamDescriptor{domainWorkstream(t, "scope-a")},
		GeneratedAt: time.Date(2026, time.August, 5, 0, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	item := report.Workstreams()[0]
	history := item.HandoffHistory()
	if item.HandoffRevisionCount() != 22 || !item.HandoffHistoryTruncated() || len(history) != handoffreport.MaxReportHandoffHistory {
		t.Fatalf("bounded projection = count %d, truncated %t, history length %d",
			item.HandoffRevisionCount(), item.HandoffHistoryTruncated(), len(history))
	}
	for index, summary := range history {
		if want := int64(index + 3); summary.Reference().Revision() != want {
			t.Fatalf("history[%d] revision = %d, want %d", index, summary.Reference().Revision(), want)
		}
	}
}

func TestSelectionDigestIsLocaleIndependentAndCanonicalRejectsFloats(t *testing.T) {
	t.Parallel()
	canonicalA, err := handoffreport.CanonicalJSONBytes(map[string]any{"text": "Cafe\u0301"})
	if err != nil {
		t.Fatal(err)
	}
	canonicalB, err := handoffreport.CanonicalJSONBytes(map[string]any{"text": "Café"})
	if err != nil {
		t.Fatal(err)
	}
	if string(canonicalA) != string(canonicalB) {
		t.Fatalf("NFC differs: %s != %s", canonicalA, canonicalB)
	}
	_, err = handoffreport.CanonicalJSONBytes(map[string]any{"value": 1.5})
	var invalid *handoffreport.CanonicalizationError
	if !errors.As(err, &invalid) || invalid.Code != "float" {
		t.Fatalf("expected float rejection, got %v", err)
	}
	selected := reportHandoff(t, 1)
	generate := func(locale handoffreport.Locale) handoffreport.Report {
		reader := &reportReader{values: map[string]*handoff.Handoff{"scope-a": &selected}}
		service, _ := handoffreport.NewService(reader)
		value, err := service.Generate(context.Background(), handoffreport.GenerateInput{Project: domainProject(t), Workstreams: []handoffreport.WorkstreamDescriptor{domainWorkstream(t, "scope-a")}, Locale: &locale, GeneratedAt: time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC), Format: handoffreport.FormatJSON, ReportKind: handoffreport.ReportHandoff, NormalizedFilters: map[string]any{}, ActivityCoverage: handoffreport.ActivityNotConfigured})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	chinese, english := generate(handoffreport.LocaleChinese), generate(handoffreport.LocaleEnglish)
	if chinese.SelectionDigest() != english.SelectionDigest() {
		t.Fatalf("locale changed selection digest")
	}
	if chinese.ReportDigest() == english.ReportDigest() {
		t.Fatalf("locale did not change report digest")
	}
}

func TestDigestFieldsStayStableWhenReportIsRevalidated(t *testing.T) {
	t.Parallel()
	selected := reportHandoff(t, 1)
	reader := &reportReader{values: map[string]*handoff.Handoff{"scope-a": &selected}}
	service, err := handoffreport.NewService(reader)
	if err != nil {
		t.Fatal(err)
	}
	report, err := service.Generate(context.Background(), handoffreport.GenerateInput{
		Project:           domainProject(t),
		Workstreams:       []handoffreport.WorkstreamDescriptor{domainWorkstream(t, "scope-a")},
		GeneratedAt:       time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC),
		Format:            handoffreport.FormatJSON,
		ReportKind:        handoffreport.ReportHandoff,
		NormalizedFilters: map[string]any{},
		ActivityCoverage:  handoffreport.ActivityNotConfigured,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantSelection, wantReport := report.SelectionDigest(), report.ReportDigest()
	if err := report.Validate(); err != nil {
		t.Fatal(err)
	}
	selection, err := handoffreport.SelectionDigest(report)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := handoffreport.ReportDigest(report)
	if err != nil {
		t.Fatal(err)
	}
	if selection != wantSelection || digest != wantReport {
		t.Fatalf("revalidated digests = (%q, %q), want (%q, %q)", selection, digest, wantSelection, wantReport)
	}
}

func TestCanonicalJSONTreatsTypedNilMarshalerAsNull(t *testing.T) {
	t.Parallel()
	var vcs *handoffreport.ActivityVCSContext
	encoded, err := handoffreport.CanonicalJSONBytes(map[string]any{"vcs_context": vcs})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"vcs_context":null}` {
		t.Fatalf("canonical typed nil = %s", encoded)
	}
}

func TestOptimisticSelectionFailsAfterBoundedInstability(t *testing.T) {
	t.Parallel()
	one, two := reportHandoff(t, 1), reportHandoff(t, 2)
	reader := &alternatingReader{one: one, two: two}
	_, err := handoffreport.SelectOptimisticStable(context.Background(), reader, []handoffreport.WorkstreamDescriptor{domainWorkstream(t, "scope-a")}, 3)
	var busy *handoffreport.BusyError
	if !errors.As(err, &busy) || busy.Attempts != 3 {
		t.Fatalf("expected bounded busy error, got %v", err)
	}
	if reader.reads != 6 {
		t.Fatalf("head reads = %d", reader.reads)
	}
}

type reportReader struct {
	values                  map[string]*handoff.Handoff
	histories               map[string][]handoff.Handoff
	latestReads, exactReads int
	evidenceReads           int
	evidenceUnavailable     bool
}

func (r *reportReader) Latest(_ context.Context, scope string) (handoff.Handoff, bool, error) {
	r.latestReads++
	value := r.values[scope]
	if value == nil {
		return handoff.Handoff{}, false, nil
	}
	return *value, true, nil
}

func (r *reportReader) Get(_ context.Context, scope string, ref artifact.Ref) (handoff.Handoff, error) {
	r.exactReads++
	value := r.values[scope]
	if value == nil {
		return handoff.Handoff{}, &artifact.NotFoundError{Ref: ref}
	}
	return *value, nil
}

func (r *reportReader) Revisions(_ context.Context, scope string) ([]handoff.Handoff, error) {
	if values, ok := r.histories[scope]; ok {
		return append([]handoff.Handoff(nil), values...), nil
	}
	value := r.values[scope]
	if value == nil {
		return nil, nil
	}
	return []handoff.Handoff{*value}, nil
}

func (r *reportReader) CheckEvidence(context.Context, string, artifact.Ref) ([]handoff.EvidenceCheck, error) {
	r.evidenceReads++
	if r.evidenceUnavailable {
		return nil, &handoffreport.EvidenceCheckUnavailableError{}
	}
	return []handoff.EvidenceCheck{}, nil
}

type alternatingReader struct {
	one, two handoff.Handoff
	reads    int
}

func (r *alternatingReader) Latest(context.Context, string) (handoff.Handoff, bool, error) {
	r.reads++
	if r.reads%2 == 1 {
		return r.one, true, nil
	}
	return r.two, true, nil
}

func (*alternatingReader) Get(context.Context, string, artifact.Ref) (handoff.Handoff, error) {
	panic("unexpected exact read")
}

func (*alternatingReader) Revisions(context.Context, string) ([]handoff.Handoff, error) {
	return nil, nil
}

func (*alternatingReader) CheckEvidence(context.Context, string, artifact.Ref) ([]handoff.EvidenceCheck, error) {
	return nil, nil
}

func domainProject(t *testing.T) handoffreport.ProjectDescriptor {
	t.Helper()
	value, err := handoffreport.NewProjectDescriptor("prj-1", "powercontext", "PowerContext <script>", nil, handoffreport.LocaleChinese, "Asia/Shanghai", handoffreport.CatalogIncluded, 1)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func domainWorkstream(t *testing.T, scope string) handoffreport.WorkstreamDescriptor {
	t.Helper()
	value, err := handoffreport.NewWorkstreamDescriptor(scope, "prj-1", nil, "Report", handoffreport.WorkstreamFeature, handoffreport.CatalogIncluded, nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func domainActivity(t *testing.T, id, scope string, occurred time.Time) handoffreport.ActivityEvent {
	t.Helper()
	value, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{EventID: id, ProjectID: "prj-1", ScopeID: &scope, Source: handoffreport.ActivityCodingSession, SourceEventID: "source-" + id, OccurredAt: &occurred, ObservedAt: occurred.Add(time.Hour).UTC(), TimeBasis: handoffreport.TimeSourceReported})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func reportHandoff(t *testing.T, revision int64) handoff.Handoff {
	return reportHandoffRevision(t, revision, "Implement the report.", "Add the API.")
}

func reportHandoffRevision(t *testing.T, revision int64, objective, nextAction string) handoff.Handoff {
	t.Helper()
	ref, _ := source.NewRef("content", "source-1")
	citation, _ := handoff.NewSourceCitation(ref)
	statement, _ := handoff.NewStatement("The model exists.", []handoff.Citation{citation})
	next, _ := handoff.NewStatement(nextAction, []handoff.Citation{citation})
	content, err := handoff.NewContent(objective, []handoff.Statement{statement}, handoff.Continuable, &next, nil)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := handoff.NewArtifactDraft(content, []source.Ref{ref}, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := artifact.New("handoff", revision, draft)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func reportBool(value bool) *bool { return &value }
