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
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/source"
)

func TestHandoffDraftPreservesInspectedContentDuringFinalization(t *testing.T) {
	t.Parallel()
	citation := modelSourceCitation(t, "turn-1")
	state := modelStatement(t, "Implementation is ready.", citation)
	next := modelStatement(t, "Run regression tests.", citation)
	draft, err := NewDraft(
		"Complete parser error handling.",
		[]Statement{state},
		Continuable,
		&next,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	content := draft.AsContent()
	want, err := NewContent(draft.Objective(), []Statement{state}, Continuable, &next, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !content.Equal(want) {
		t.Fatalf("finalized content = %#v, want %#v", content, want)
	}
	if content.Schema() != ContentSchemaVersion {
		t.Fatalf("schema = %q, want %q", content.Schema(), ContentSchemaVersion)
	}
}

func TestHandoffContentContractRejectsIncompleteHandoffs(t *testing.T) {
	t.Parallel()
	statement := modelStatement(t, "claim", modelSourceCitation(t, "turn-1"))
	tests := []struct {
		name        string
		objective   string
		state       []Statement
		disposition Disposition
	}{
		{name: "empty objective", state: []Statement{statement}, disposition: Continuable},
		{name: "blank objective", objective: "   ", state: []Statement{statement}, disposition: Continuable},
		{name: "empty state", objective: "objective", disposition: Continuable},
		{name: "unknown disposition", objective: "objective", state: []Statement{statement}, disposition: "unknown"},
		{name: "unvalidated nested statement", objective: "objective", state: []Statement{{}}, disposition: Continuable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewDraft(test.objective, test.state, test.disposition, nil, nil); err == nil {
				t.Fatal("NewDraft accepted an incomplete Handoff")
			}
		})
	}
	if _, err := NewArtifactDraft(Content{}, nil, nil); err == nil {
		t.Fatal("NewArtifactDraft accepted unvalidated zero Content")
	}
	if _, err := NewPrepared("project", nil, Content{}); err == nil {
		t.Fatal("NewPrepared accepted unvalidated zero Content")
	}
	if _, err := NewGenerationRequest("objective", []Evidence{SourceEvidence{}}, DefaultMaxBytes); err == nil {
		t.Fatal("NewGenerationRequest accepted unvalidated zero evidence")
	}
}

func TestHandoffValuesAreImmutableAfterValidation(t *testing.T) {
	t.Parallel()
	citation := modelSourceCitation(t, "turn-1")
	citations := []Citation{citation}
	statement, err := NewStatement("claim", citations)
	if err != nil {
		t.Fatal(err)
	}
	citations[0] = nil
	state := []Statement{statement}
	content, err := NewContent("objective", state, Complete, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	state[0] = Statement{}

	returnedState := content.State()
	returnedState[0] = Statement{}
	returnedCitations := content.State()[0].Citations()
	returnedCitations[0] = nil
	if validationErr := content.Validate(); validationErr != nil || content.State()[0].Text() != "claim" || len(content.State()[0].Citations()) != 1 {
		t.Fatalf("Content changed through an input or accessor copy: %v", validationErr)
	}

	base, err := artifact.NewRef(Family, "handoff", 1)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := NewPrepared("project", &base, content)
	if err != nil {
		t.Fatal(err)
	}
	returnedBase := prepared.Base()
	*returnedBase = artifact.Ref{}
	if prepared.Base() == nil || *prepared.Base() != base {
		t.Fatal("Prepared base changed through its accessor")
	}

	index := 0
	check, err := NewEvidenceCheck(StateClaim, &index, EvidenceAvailable, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolved("project", content, PreparedSelection, nil, &base, []EvidenceCheck{check})
	if err != nil {
		t.Fatal(err)
	}
	returnedChecks := resolution.EvidenceChecks()
	returnedChecks[0] = EvidenceCheck{}
	returnedIndex := resolution.EvidenceChecks()[0].StateIndex()
	*returnedIndex = 9
	returnedContent := resolution.Content()
	*returnedContent = Content{}
	if err := resolution.Validate(); err != nil || *resolution.EvidenceChecks()[0].StateIndex() != 0 || resolution.Content() == nil {
		t.Fatalf("Resolution changed through an accessor copy: %v", err)
	}
}

func TestHandoffUnknownContentAndEnvelopeVersionsAreRejected(t *testing.T) {
	t.Parallel()
	if err := ValidateContentSchema(ContentSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePreparedSchema(PreparedSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := ValidateContentSchema("powercontext.handoff.v2"); err == nil {
		t.Fatal("unknown Handoff content schema was accepted")
	}
	if err := ValidatePreparedSchema("powercontext.prepared-handoff.v2"); err == nil {
		t.Fatal("unknown Prepared Handoff schema was accepted")
	}
}

func TestHandoffContentIsBounded(t *testing.T) {
	t.Parallel()
	citation := modelSourceCitation(t, "turn-1")
	statement := modelStatement(t, "claim", citation)
	if _, err := NewDraft(strings.Repeat("x", MaxTextLength+1), []Statement{statement}, Continuable, nil, nil); err == nil {
		t.Fatal("Handoff objective above the character limit was accepted")
	}
	state := make([]Statement, MaxStateStatements+1)
	for index := range state {
		state[index] = modelStatement(t, string(rune('a'+index%26)), citation)
	}
	if _, err := NewDraft("objective", state, Continuable, nil, nil); err == nil {
		t.Fatal("Handoff state above the statement limit was accepted")
	}
}

func TestHandoffPrepareRequiresUniqueBoundedEvidence(t *testing.T) {
	t.Parallel()
	citation := modelSourceCitation(t, "turn-1")
	if _, err := NewPrepare("Complete parser error handling.", []Citation{citation, citation}, DefaultMaxBytes); err == nil {
		t.Fatal("duplicate preparation evidence was accepted")
	}
	if _, err := NewPrepare("Complete parser error handling.", []Citation{citation}, MaxBytes+1); err == nil {
		t.Fatal("preparation byte budget above the maximum was accepted")
	}
}

func TestHandoffActivateAddsBoundaryOnceAndRejectsInvalidEvidence(t *testing.T) {
	t.Parallel()
	boundary := modelSourceRef(t, "turn-1")
	boundaryCitation := modelSourceCitation(t, "turn-1")
	activation, err := NewActivate(
		boundary,
		"Complete parser error handling.",
		[]Citation{boundaryCitation},
		DefaultMaxBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence := activation.ActionEvidence()
	if len(evidence) != 1 || evidence[0].citationKey() != boundaryCitation.citationKey() {
		t.Fatalf("action evidence = %#v, want boundary once", evidence)
	}

	other := modelSourceCitation(t, "turn-2")
	if _, err := NewActivate(boundary, "objective", []Citation{other, other}, DefaultMaxBytes); err == nil {
		t.Fatal("duplicate activation evidence was accepted")
	}
	full := make([]Citation, MaxCitations)
	for index := range full {
		full[index] = modelSourceCitation(t, "evidence-"+string(rune('A'+index)))
	}
	if _, err := NewActivate(boundary, "objective", full, DefaultMaxBytes); err == nil {
		t.Fatal("activation accepted 32 explicit citations plus its implicit boundary citation")
	}
	if _, err := NewActivate(boundary, "objective", []Citation{nil}, DefaultMaxBytes); err == nil {
		t.Fatal("nil activation evidence was accepted")
	}
}

func TestHandoffActivationResultEnforcesBoundaryTransition(t *testing.T) {
	t.Parallel()
	boundary := modelSourceRef(t, "turn-1")
	draft := modelDraft(t)
	if _, err := NewActivation(ActivationIgnored, boundary, 4, 4, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := NewActivation(ActivationGenerated, boundary, 4, 5, &draft); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		status   ActivationStatus
		previous int64
		current  int64
		draft    *Draft
	}{
		{name: "moves backwards", status: ActivationIgnored, previous: 5, current: 4},
		{name: "generated without draft", status: ActivationGenerated, previous: 4, current: 5},
		{name: "generated without advance", status: ActivationGenerated, previous: 4, current: 4, draft: &draft},
		{name: "ignored with advance", status: ActivationIgnored, previous: 4, current: 5},
		{name: "ignored with draft", status: ActivationIgnored, previous: 4, current: 4, draft: &draft},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewActivation(test.status, boundary, test.previous, test.current, test.draft); err == nil {
				t.Fatal("invalid activation result was accepted")
			}
		})
	}
}

func TestHandoffResolutionRequiresOneEvidenceCheckPerStatement(t *testing.T) {
	t.Parallel()
	citation := modelSourceCitation(t, "turn-1")
	statement := modelStatement(t, "Implementation is ready.", citation)
	content, err := NewContent("objective", []Statement{statement}, Continuable, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, resolveErr := NewResolved("project", content, PreparedSelection, nil, nil, nil); resolveErr == nil {
		t.Fatal("resolved Handoff without its statement evidence check was accepted")
	}
	index := 0
	available, err := NewEvidenceCheck(StateClaim, &index, EvidenceAvailable, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := NewResolved(
		"project", content, PreparedSelection, nil, nil, []EvidenceCheck{available},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status() != ResolvedResolution {
		t.Fatalf("status = %q, want resolved", resolution.Status())
	}

	unrelated := modelSourceCitation(t, "unrelated")
	unavailable, err := NewEvidenceCheck(StateClaim, &index, EvidenceUnavailable, []Citation{unrelated})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewResolved(
		"project", content, PreparedSelection, nil, nil, []EvidenceCheck{unavailable},
	); err == nil {
		t.Fatal("resolution accepted unavailable evidence outside the checked statement")
	}
	if _, err := NewEvidenceCheck(StateClaim, nil, EvidenceAvailable, nil); err == nil {
		t.Fatal("state evidence check without an index was accepted")
	}
	if _, err := NewEvidenceCheck(StateClaim, &index, EvidenceAvailable, []Citation{citation}); err == nil {
		t.Fatal("available check containing unavailable evidence was accepted")
	}
}

func modelSourceRef(t *testing.T, id string) source.Ref {
	t.Helper()
	ref, err := source.NewRef(source.ContentType, id)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func modelSourceCitation(t *testing.T, id string) SourceCitation {
	t.Helper()
	citation, err := NewSourceCitation(modelSourceRef(t, id))
	if err != nil {
		t.Fatal(err)
	}
	return citation
}

func modelStatement(t *testing.T, text string, citations ...Citation) Statement {
	t.Helper()
	statement, err := NewStatement(text, citations)
	if err != nil {
		t.Fatal(err)
	}
	return statement
}

func modelDraft(t *testing.T) Draft {
	t.Helper()
	draft, err := NewDraft(
		"objective",
		[]Statement{modelStatement(t, "state", modelSourceCitation(t, "turn-1"))},
		Continuable,
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return draft
}
