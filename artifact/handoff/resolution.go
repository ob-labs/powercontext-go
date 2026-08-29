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
	"fmt"
	"slices"
	"strings"

	"github.com/ob-labs/powercontext-go/artifact"
)

// Prepared is an uncommitted handoff bound to the exact Scope and base head
// against which it was generated.
type Prepared struct {
	scopeID string
	base    *artifact.Ref
	content Content
}

func NewPrepared(scopeID string, base *artifact.Ref, content Content) (Prepared, error) {
	value := Prepared{scopeID: scopeID, base: cloneArtifactRef(base), content: cloneContent(content)}
	if err := value.Validate(); err != nil {
		return Prepared{}, err
	}
	return value, nil
}
func (p Prepared) Schema() string      { return PreparedSchemaVersion }
func (p Prepared) ScopeID() string     { return p.scopeID }
func (p Prepared) Base() *artifact.Ref { return cloneArtifactRef(p.base) }
func (p Prepared) Content() Content    { return cloneContent(p.content) }
func (p Prepared) Validate() error {
	if strings.TrimSpace(p.scopeID) == "" {
		return fmt.Errorf("scope_id must contain non-whitespace content")
	}
	if p.base != nil {
		if err := p.base.Validate(); err != nil {
			return err
		}
	}
	return p.content.Validate()
}

type EvidenceStatus string

const (
	EvidenceAvailable   EvidenceStatus = "available"
	EvidenceUnavailable EvidenceStatus = "unavailable"
)

type Claim string

const (
	StateClaim      Claim = "state"
	NextActionClaim Claim = "next_action"
)

type EvidenceCheck struct {
	claim               Claim
	stateIndex          *int
	status              EvidenceStatus
	unavailableEvidence []Citation
}

func NewEvidenceCheck(
	claim Claim,
	stateIndex *int,
	status EvidenceStatus,
	unavailableEvidence []Citation,
) (EvidenceCheck, error) {
	value := EvidenceCheck{
		claim: claim, stateIndex: cloneInt(stateIndex), status: status,
		unavailableEvidence: slices.Clone(unavailableEvidence),
	}
	if err := value.Validate(); err != nil {
		return EvidenceCheck{}, err
	}
	return value, nil
}

func (c EvidenceCheck) Claim() Claim                    { return c.claim }
func (c EvidenceCheck) StateIndex() *int                { return cloneInt(c.stateIndex) }
func (c EvidenceCheck) Status() EvidenceStatus          { return c.status }
func (c EvidenceCheck) UnavailableEvidence() []Citation { return slices.Clone(c.unavailableEvidence) }

func (c EvidenceCheck) Validate() error {
	if c.claim != StateClaim && c.claim != NextActionClaim {
		return fmt.Errorf("invalid Handoff evidence claim %q", c.claim)
	}
	if c.claim == StateClaim && c.stateIndex == nil {
		return fmt.Errorf("state evidence check requires a state index")
	}
	if c.claim == NextActionClaim && c.stateIndex != nil {
		return fmt.Errorf("next-action evidence check cannot contain a state index")
	}
	if c.stateIndex != nil && *c.stateIndex < 0 {
		return fmt.Errorf("Handoff evidence state index must not be negative")
	}
	if c.status != EvidenceAvailable && c.status != EvidenceUnavailable {
		return fmt.Errorf("invalid Handoff evidence status %q", c.status)
	}
	if len(c.unavailableEvidence) > MaxCitations {
		return fmt.Errorf("Handoff unavailable evidence exceeds %d citations", MaxCitations)
	}
	if err := validateCitations(c.unavailableEvidence, false); err != nil {
		return err
	}
	if c.status == EvidenceAvailable && len(c.unavailableEvidence) != 0 {
		return fmt.Errorf("available evidence check cannot identify unavailable evidence")
	}
	if c.status == EvidenceUnavailable && len(c.unavailableEvidence) == 0 {
		return fmt.Errorf("unavailable evidence check must identify unavailable evidence")
	}
	return nil
}

type Selection string

const (
	PreparedSelection Selection = "prepared"
	ExactSelection    Selection = "exact"
	LatestSelection   Selection = "latest"
)

type ResolutionStatus string

const (
	EmptyResolution    ResolutionStatus = "empty"
	ResolvedResolution ResolutionStatus = "resolved"
)

type Resolution struct {
	status           ResolutionStatus
	scopeID          string
	content          *Content
	selection        *Selection
	selectedRevision *artifact.Ref
	currentRevision  *artifact.Ref
	evidenceChecks   []EvidenceCheck
}

func Empty(scopeID string) Resolution {
	value, err := NewResolution(EmptyResolution, scopeID, nil, nil, nil, nil, nil)
	if err != nil {
		panic(err)
	}
	return value
}

func NewResolved(
	scopeID string,
	content Content,
	selection Selection,
	selectedRevision *artifact.Ref,
	currentRevision *artifact.Ref,
	evidenceChecks []EvidenceCheck,
) (Resolution, error) {
	return NewResolution(
		ResolvedResolution,
		scopeID,
		&content,
		&selection,
		selectedRevision,
		currentRevision,
		evidenceChecks,
	)
}

func NewResolution(
	status ResolutionStatus,
	scopeID string,
	content *Content,
	selection *Selection,
	selectedRevision *artifact.Ref,
	currentRevision *artifact.Ref,
	evidenceChecks []EvidenceCheck,
) (Resolution, error) {
	value := Resolution{
		status: status, scopeID: scopeID, content: cloneContentPointer(content),
		selection: cloneSelection(selection), selectedRevision: cloneArtifactRef(selectedRevision),
		currentRevision: cloneArtifactRef(currentRevision), evidenceChecks: cloneEvidenceChecks(evidenceChecks),
	}
	if err := value.Validate(); err != nil {
		return Resolution{}, err
	}
	return value, nil
}

func (r Resolution) Trust() string                   { return ResolutionTrust }
func (r Resolution) Status() ResolutionStatus        { return r.status }
func (r Resolution) ScopeID() string                 { return r.scopeID }
func (r Resolution) Content() *Content               { return cloneContentPointer(r.content) }
func (r Resolution) Selection() *Selection           { return cloneSelection(r.selection) }
func (r Resolution) SelectedRevision() *artifact.Ref { return cloneArtifactRef(r.selectedRevision) }
func (r Resolution) CurrentRevision() *artifact.Ref  { return cloneArtifactRef(r.currentRevision) }
func (r Resolution) EvidenceChecks() []EvidenceCheck { return cloneEvidenceChecks(r.evidenceChecks) }

func (r Resolution) Validate() error {
	if r.status != EmptyResolution && r.status != ResolvedResolution {
		return fmt.Errorf("invalid Handoff resolution status %q", r.status)
	}
	if r.status == EmptyResolution {
		if r.content != nil || r.selection != nil || r.selectedRevision != nil ||
			r.currentRevision != nil || len(r.evidenceChecks) != 0 {
			return fmt.Errorf("empty resolution cannot contain Handoff state")
		}
		return nil
	}
	if r.content == nil || r.selection == nil {
		return fmt.Errorf("resolved Handoff must contain content and selection")
	}
	if err := r.content.Validate(); err != nil {
		return err
	}
	if err := validateResolutionSelection(*r.selection, r.selectedRevision, r.currentRevision); err != nil {
		return err
	}
	return validateResolutionEvidence(*r.content, r.evidenceChecks)
}

func validateResolutionSelection(selection Selection, selected, current *artifact.Ref) error {
	if selection != PreparedSelection && selection != ExactSelection && selection != LatestSelection {
		return fmt.Errorf("invalid Handoff resolution selection %q", selection)
	}
	for _, ref := range []*artifact.Ref{selected, current} {
		if ref != nil {
			if err := ref.Validate(); err != nil {
				return err
			}
		}
	}
	if selection == PreparedSelection && selected != nil {
		return fmt.Errorf("prepared selection cannot identify a committed Revision")
	}
	if selection != PreparedSelection && selected == nil {
		return fmt.Errorf("committed selection must identify its exact Revision")
	}
	if selection != PreparedSelection && current == nil {
		return fmt.Errorf("committed selection must identify the current Revision")
	}
	if selection == LatestSelection && *selected != *current {
		return fmt.Errorf("latest selection must select the current Revision")
	}
	if selected != nil && current != nil &&
		(selected.Family() != current.Family() || selected.ID() != current.ID()) {
		return fmt.Errorf("selected and current Revisions must share one Artifact identity")
	}
	return nil
}

func validateResolutionEvidence(content Content, checks []EvidenceCheck) error {
	expected := len(content.state)
	if content.nextAction != nil {
		expected++
	}
	if len(checks) != expected {
		return fmt.Errorf("evidence checks must match Handoff statements in order")
	}
	for index, check := range checks {
		if err := check.Validate(); err != nil {
			return err
		}
		var statement Statement
		if index < len(content.state) {
			if check.claim != StateClaim || check.stateIndex == nil || *check.stateIndex != index {
				return fmt.Errorf("evidence checks must match Handoff statements in order")
			}
			statement = content.state[index]
		} else {
			if check.claim != NextActionClaim || check.stateIndex != nil {
				return fmt.Errorf("evidence checks must match Handoff statements in order")
			}
			statement = *content.nextAction
		}
		for _, unavailable := range check.unavailableEvidence {
			if !statementHasCitation(statement, unavailable) {
				return fmt.Errorf("unavailable evidence must belong to the checked statement")
			}
		}
	}
	return nil
}

func statementHasCitation(statement Statement, citation Citation) bool {
	key := citation.citationKey()
	for _, candidate := range statement.citations {
		if candidate.citationKey() == key {
			return true
		}
	}
	return false
}

func cloneSelection(value *Selection) *Selection {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneEvidenceChecks(values []EvidenceCheck) []EvidenceCheck {
	result := make([]EvidenceCheck, len(values))
	for index, value := range values {
		result[index] = EvidenceCheck{
			claim: value.claim, stateIndex: cloneInt(value.stateIndex), status: value.status,
			unavailableEvidence: slices.Clone(value.unavailableEvidence),
		}
	}
	return result
}

func cloneArtifactRef(value *artifact.Ref) *artifact.Ref {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
