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

package handoff

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/source"
)

func TestHandoffServicePrepareGeneratesDraftFromExactBoundedEvidence(t *testing.T) {
	t.Parallel()
	citation := serviceSourceCitation(t, "turn-1")
	generated := serviceDraft(t, "Error mapping changed.")
	pipeline := &serviceGenerationPipeline{draft: generated}
	service, backend, _ := newHandoffService(t, pipeline)
	action := servicePrepare(t, generated.Objective(), []Citation{citation}, 4096)

	draft, err := service.Prepare(context.Background(), action)
	if err != nil {
		t.Fatal(err)
	}
	if !draft.AsContent().Equal(generated.AsContent()) {
		t.Fatalf("prepared Draft = %#v, want generated Draft", draft.AsContent())
	}
	if pipeline.request.Objective() != action.Objective() || pipeline.request.MaxBytes() != 4096 ||
		len(pipeline.request.Evidence()) != 1 || pipeline.request.Evidence()[0].Citation().citationKey() != citation.citationKey() {
		t.Fatalf("generation request did not preserve exact bounded action: %#v", pipeline.request)
	}
	if _, found, err := backend.Latest(context.Background(), "handoff"); err != nil || found {
		t.Fatalf("latest after prepare = found:%t err:%v, want empty", found, err)
	}
}

func TestHandoffServicePrepareRequiresConfiguredGenerationPipeline(t *testing.T) {
	t.Parallel()
	service, _, _ := newHandoffService(t, nil)
	action := servicePrepare(t, "Complete parser error handling.", []Citation{serviceSourceCitation(t, "turn-1")}, DefaultMaxBytes)

	_, err := service.Prepare(context.Background(), action)
	var unavailable *GenerationUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %#v, want GenerationUnavailableError", err)
	}
}

func TestHandoffServicePrepareRejectsPipelineContractViolations(t *testing.T) {
	t.Parallel()
	allowed := serviceSourceCitation(t, "turn-1")
	missing := serviceSourceCitation(t, "missing")
	changedObjective := serviceDraftValue(t, "Changed objective.", []Statement{
		serviceStatement(t, "Current state.", allowed),
	}, Continuable, nil, nil)
	outsideEvidence := serviceDraftValue(t, "Complete parser error handling.", []Statement{
		serviceStatement(t, "Unsupported state.", missing),
	}, Continuable, nil, nil)
	overBudget := serviceDraft(t, strings.Repeat("x", 1000))

	for _, test := range []struct {
		name     string
		draft    Draft
		maxBytes int
		code     string
	}{
		{name: "objective", draft: changedObjective, maxBytes: 4096, code: "objective"},
		{name: "evidence", draft: outsideEvidence, maxBytes: 4096, code: "evidence"},
		{name: "budget", draft: overBudget, maxBytes: 512, code: "budget"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pipeline := &serviceGenerationPipeline{draft: test.draft}
			service, _, _ := newHandoffService(t, pipeline)
			action := servicePrepare(t, "Complete parser error handling.", []Citation{allowed}, test.maxBytes)
			_, err := service.Prepare(context.Background(), action)
			var invalid *InvalidGenerationError
			if !errors.As(err, &invalid) || invalid.Code != test.code {
				t.Fatalf("error = %#v, want InvalidGenerationError(%q)", err, test.code)
			}
		})
	}
}

func TestHandoffServiceDraftCanBeCorrectedBeforeTemporaryHandoffFinalized(t *testing.T) {
	t.Parallel()
	service, backend, _ := newHandoffService(t, nil)
	corrected := serviceDraft(t, "Error mapping changed.")

	prepared, err := service.Finalize(context.Background(), corrected)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ScopeID() != "project" || prepared.Base() != nil ||
		prepared.Content().State()[0].Text() != "Error mapping changed." {
		t.Fatalf("prepared Handoff = %#v", prepared)
	}
	if _, found, latestErr := backend.Latest(context.Background(), "handoff"); latestErr != nil || found {
		t.Fatalf("latest after finalize = found:%t err:%v, want empty", found, latestErr)
	}
	draftHuman, err := Render(corrected, Human)
	if err != nil {
		t.Fatal(err)
	}
	preparedHuman, err := Render(prepared, Human)
	if err != nil {
		t.Fatal(err)
	}
	preparedAgent, err := Render(prepared, Agent)
	if err != nil {
		t.Fatal(err)
	}
	if draftHuman != preparedHuman || preparedHuman != preparedAgent {
		t.Fatal("Draft, human Prepared, and agent Prepared renderings differ")
	}

	continued, err := service.ContinueFromPrepared(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	selection := continued.Selection()
	content := continued.Content()
	if continued.Status() != ResolvedResolution || continued.Trust() != ResolutionTrust ||
		selection == nil || *selection != PreparedSelection || continued.SelectedRevision() != nil ||
		content == nil || !content.Equal(prepared.Content()) {
		t.Fatalf("continued Prepared Handoff = %#v", continued)
	}
	for _, check := range continued.EvidenceChecks() {
		if check.Status() != EvidenceAvailable {
			t.Fatalf("evidence check = %#v, want available", check)
		}
	}
}

func TestHandoffServiceCommitIsIdempotentAndOmissionsAreNotLineage(t *testing.T) {
	t.Parallel()
	service, backend, _ := newHandoffService(t, nil)
	omission, err := NewOmission("Latest test output is unavailable.", serviceSourceCitation(t, "missing"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Finalize(context.Background(), serviceDraftWith(t, "Error mapping changed.", Continuable, textPointer("Run tests."), []Omission{omission}))
	if err != nil {
		t.Fatal(err)
	}

	committed, err := service.Commit(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	retried, err := service.Commit(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Ref() != committed.Ref() || !retried.Content().Equal(committed.Content()) {
		t.Fatalf("idempotent retry = %s, want %s", retried.Ref(), committed.Ref())
	}
	if len(backend.history) != 1 {
		t.Fatalf("committed revisions = %d, want 1", len(backend.history))
	}
	want := []source.Ref{serviceSourceRef(t, "turn-1")}
	if got := committed.Lineage().Sources(); !slices.Equal(got, want) {
		t.Fatalf("source lineage = %v, want %v", got, want)
	}
}

func TestHandoffServiceStalePreparedCannotReplaceNewerMilestone(t *testing.T) {
	t.Parallel()
	service, _, _ := newHandoffService(t, nil)
	first, err := service.Finalize(context.Background(), serviceDraft(t, "Initial state."))
	if err != nil {
		t.Fatal(err)
	}
	if _, commitErr := service.Commit(context.Background(), first); commitErr != nil {
		t.Fatal(commitErr)
	}
	sessionA, err := service.Finalize(context.Background(), serviceDraft(t, "Session A state."))
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := service.Finalize(context.Background(), serviceDraft(t, "Session B state."))
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.Commit(context.Background(), sessionA)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Commit(context.Background(), sessionB)
	var conflict *artifact.RevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("error = %#v, want RevisionConflictError", err)
	}
	if sessionB.Base() == nil || conflict.Requested != *sessionB.Base() || conflict.Current != current.Ref() {
		t.Fatalf("conflict = requested:%s current:%s", conflict.Requested, conflict.Current)
	}
}

func TestHandoffServiceContinueReportsEvidenceAvailabilityPerStatement(t *testing.T) {
	t.Parallel()
	service, _, resolver := newHandoffService(t, nil)
	available := serviceSourceCitation(t, "turn-1")
	missing := serviceSourceCitation(t, "missing")
	draft := serviceDraftValue(t, "Complete parser error handling.", []Statement{
		serviceStatement(t, "Verified state.", available),
		serviceStatement(t, "Unverified state.", missing),
	}, Continuable, statementPointer(serviceStatement(t, "Run tests.", available)), nil)
	prepared, err := service.Finalize(context.Background(), draft)
	if err != nil {
		t.Fatal(err)
	}
	resolver.markUnavailable(missing)

	continued, err := service.ContinueFromPrepared(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	wantStatuses := []EvidenceStatus{EvidenceAvailable, EvidenceUnavailable, EvidenceAvailable}
	checks := continued.EvidenceChecks()
	gotStatuses := make([]EvidenceStatus, len(checks))
	for index, check := range checks {
		gotStatuses[index] = check.Status()
	}
	if !slices.Equal(gotStatuses, wantStatuses) {
		t.Fatalf("evidence statuses = %v, want %v", gotStatuses, wantStatuses)
	}
	unavailable := checks[1].UnavailableEvidence()
	if len(unavailable) != 1 || unavailable[0].citationKey() != missing.citationKey() {
		t.Fatalf("unavailable evidence = %#v", unavailable)
	}
	if continued.Trust() != ResolutionTrust {
		t.Fatalf("trust = %q", continued.Trust())
	}
}

func TestHandoffServiceCommitRevalidatesEvidenceAfterPreparation(t *testing.T) {
	t.Parallel()
	service, backend, resolver := newHandoffService(t, nil)
	prepared, err := service.Finalize(context.Background(), serviceDraft(t, "Current state."))
	if err != nil {
		t.Fatal(err)
	}
	resolver.markUnavailable(serviceSourceCitation(t, "turn-1"))

	_, err = service.Commit(context.Background(), prepared)
	var unavailable *EvidenceUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %#v, want EvidenceUnavailableError", err)
	}
	if _, found, latestErr := backend.Latest(context.Background(), "handoff"); latestErr != nil || found {
		t.Fatalf("latest after failed commit = found:%t err:%v", found, latestErr)
	}
}

func TestHandoffServiceExactOldRevisionRemainsHistoricalInput(t *testing.T) {
	t.Parallel()
	service, _, _ := newHandoffService(t, nil)
	firstPrepared, err := service.Finalize(context.Background(), serviceDraft(t, "First state."))
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Commit(context.Background(), firstPrepared)
	if err != nil {
		t.Fatal(err)
	}
	secondDraft := serviceDraftWith(t, "Second state.", Complete, nil, nil)
	secondPrepared, err := service.Finalize(context.Background(), secondDraft)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Commit(context.Background(), secondPrepared)
	if err != nil {
		t.Fatal(err)
	}

	continued, err := service.ContinueFromRevision(context.Background(), first.Ref())
	if err != nil {
		t.Fatal(err)
	}
	latest, err := service.ContinueLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	continuedSelection, selectedRevision, currentRevision := continued.Selection(), continued.SelectedRevision(), continued.CurrentRevision()
	if continuedSelection == nil || *continuedSelection != ExactSelection || selectedRevision == nil ||
		*selectedRevision != first.Ref() || currentRevision == nil || *currentRevision != second.Ref() {
		t.Fatalf("exact resolution = %#v", continued)
	}
	latestSelection, latestSelected, latestContent := latest.Selection(), latest.SelectedRevision(), latest.Content()
	if latestSelection == nil || *latestSelection != LatestSelection || latestSelected == nil ||
		*latestSelected != second.Ref() || latestContent == nil || latestContent.Disposition() != Complete {
		t.Fatalf("latest resolution = %#v", latest)
	}
	revisions, err := service.Revisions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 || revisions[0].Ref() != first.Ref() || revisions[1].Ref() != second.Ref() {
		t.Fatalf("revisions = %#v", revisions)
	}
}

func TestHandoffServiceRejectsCrossScopePreparedHandoff(t *testing.T) {
	t.Parallel()
	service, _, _ := newHandoffService(t, nil)
	prepared, err := service.Finalize(context.Background(), serviceDraft(t, "Current state."))
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := NewPrepared("other", prepared.Base(), prepared.Content())
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.ContinueFromPrepared(context.Background(), foreign)
	var mismatch *ScopeMismatchError
	if !errors.As(err, &mismatch) || mismatch.Expected != "project" || mismatch.Actual != "other" {
		t.Fatalf("error = %#v, want project/other ScopeMismatchError", err)
	}
}

func TestHandoffServiceContinueLatestReportsEmptyWithoutMilestone(t *testing.T) {
	t.Parallel()
	service, _, _ := newHandoffService(t, nil)

	continued, err := service.ContinueLatest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if continued.Status() != EmptyResolution || continued.Trust() != ResolutionTrust || continued.ScopeID() != "project" ||
		continued.Content() != nil || continued.Selection() != nil || continued.SelectedRevision() != nil ||
		continued.CurrentRevision() != nil || len(continued.EvidenceChecks()) != 0 {
		t.Fatalf("empty resolution = %#v", continued)
	}
}

type serviceEvidenceResolver struct {
	unavailable map[string]struct{}
}

func newServiceEvidenceResolver() *serviceEvidenceResolver {
	return &serviceEvidenceResolver{unavailable: make(map[string]struct{})}
}

func (r *serviceEvidenceResolver) Resolve(ctx context.Context, citation Citation) (Evidence, error) {
	if err := r.Validate(ctx, citation); err != nil {
		return nil, err
	}
	sourceCitation, ok := citation.(SourceCitation)
	if !ok {
		return nil, fmt.Errorf("unsupported test citation %T", citation)
	}
	value, err := source.RestoreContentSource(sourceCitation.Ref().ID(), source.Captured, nil, "", nil)
	if err != nil {
		return nil, err
	}
	return NewSourceEvidence(sourceCitation, value)
}

func (r *serviceEvidenceResolver) Validate(_ context.Context, citation Citation) error {
	if _, unavailable := r.unavailable[citation.citationKey()]; unavailable {
		return &EvidenceUnavailableError{Citation: citation}
	}
	return nil
}

func (r *serviceEvidenceResolver) markUnavailable(citation Citation) {
	r.unavailable[citation.citationKey()] = struct{}{}
}

type serviceGenerationPipeline struct {
	draft   Draft
	request GenerationRequest
}

func (p *serviceGenerationPipeline) Generate(_ context.Context, request GenerationRequest) (Draft, error) {
	p.request = request
	return p.draft, nil
}

type serviceBackend struct {
	history []Handoff
}

func (b *serviceBackend) Create(_ context.Context, artifactID string, draft ArtifactDraft) (Handoff, error) {
	if len(b.history) != 0 {
		return Handoff{}, &artifact.RevisionConflictError{Current: b.history[len(b.history)-1].Ref()}
	}
	return b.append(artifactID, draft)
}

func (b *serviceBackend) Revise(_ context.Context, base Handoff, draft ArtifactDraft) (Handoff, error) {
	if len(b.history) == 0 || b.history[len(b.history)-1].Ref() != base.Ref() {
		var current artifact.Ref
		if len(b.history) != 0 {
			current = b.history[len(b.history)-1].Ref()
		}
		return Handoff{}, &artifact.RevisionConflictError{Requested: base.Ref(), Current: current}
	}
	return b.append(base.ID(), draft)
}

func (b *serviceBackend) Get(_ context.Context, ref artifact.Ref) (Handoff, error) {
	index := int(ref.Revision() - 1)
	if index < 0 || index >= len(b.history) || b.history[index].Ref() != ref {
		return Handoff{}, &artifact.NotFoundError{Ref: ref}
	}
	return b.history[index], nil
}

func (b *serviceBackend) Latest(_ context.Context, artifactID string) (Handoff, bool, error) {
	if len(b.history) == 0 {
		return Handoff{}, false, nil
	}
	latest := b.history[len(b.history)-1]
	if latest.ID() != artifactID {
		return Handoff{}, false, fmt.Errorf("unexpected Artifact identity %q", artifactID)
	}
	return latest, true, nil
}

func (b *serviceBackend) Revisions(_ context.Context, artifactID string) ([]Handoff, error) {
	if len(b.history) != 0 && b.history[len(b.history)-1].ID() != artifactID {
		return nil, fmt.Errorf("unexpected Artifact identity %q", artifactID)
	}
	return slices.Clone(b.history), nil
}

func (b *serviceBackend) append(artifactID string, draft ArtifactDraft) (Handoff, error) {
	value, err := artifact.New(artifactID, int64(len(b.history)+1), draft)
	if err != nil {
		return Handoff{}, err
	}
	b.history = append(b.history, value)
	return value, nil
}

func newHandoffService(t *testing.T, pipeline GenerationPipeline) (*Service, *serviceBackend, *serviceEvidenceResolver) {
	t.Helper()
	backend := new(serviceBackend)
	resolver := newServiceEvidenceResolver()
	service, err := NewService("project", "handoff", backend, resolver, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	return service, backend, resolver
}

func serviceSourceRef(t *testing.T, id string) source.Ref {
	t.Helper()
	ref, err := source.NewRef("content", id)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func serviceSourceCitation(t *testing.T, id string) SourceCitation {
	t.Helper()
	citation, err := NewSourceCitation(serviceSourceRef(t, id))
	if err != nil {
		t.Fatal(err)
	}
	return citation
}

func servicePrepare(t *testing.T, objective string, evidence []Citation, maxBytes int) Prepare {
	t.Helper()
	action, err := NewPrepare(objective, evidence, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return action
}

func serviceStatement(t *testing.T, text string, citations ...Citation) Statement {
	t.Helper()
	statement, err := NewStatement(text, citations)
	if err != nil {
		t.Fatal(err)
	}
	return statement
}

func serviceDraft(t *testing.T, state string) Draft {
	t.Helper()
	return serviceDraftWith(t, state, Continuable, textPointer("Run tests."), nil)
}

func serviceDraftWith(t *testing.T, state string, disposition Disposition, nextText *string, omissions []Omission) Draft {
	t.Helper()
	citation := serviceSourceCitation(t, "turn-1")
	var nextAction *Statement
	if nextText != nil {
		nextAction = statementPointer(serviceStatement(t, *nextText, citation))
	}
	return serviceDraftValue(t, "Complete parser error handling.", []Statement{
		serviceStatement(t, state, citation),
	}, disposition, nextAction, omissions)
}

func serviceDraftValue(
	t *testing.T,
	objective string,
	state []Statement,
	disposition Disposition,
	nextAction *Statement,
	omissions []Omission,
) Draft {
	t.Helper()
	draft, err := NewDraft(objective, state, disposition, nextAction, omissions)
	if err != nil {
		t.Fatal(err)
	}
	return draft
}

func statementPointer(value Statement) *Statement { return &value }
func textPointer(value string) *string            { return &value }
