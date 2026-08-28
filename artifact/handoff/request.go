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

	"github.com/ob-labs/powercontext-go/source"
)

type Prepare struct {
	objective string
	evidence  []Citation
	maxBytes  int
}

func NewPrepare(objective string, evidence []Citation, maxBytes int) (Prepare, error) {
	value := Prepare{objective: objective, evidence: slices.Clone(evidence), maxBytes: maxBytes}
	if err := value.Validate(); err != nil {
		return Prepare{}, err
	}
	return value, nil
}

func (p Prepare) Objective() string    { return p.objective }
func (p Prepare) Evidence() []Citation { return slices.Clone(p.evidence) }
func (p Prepare) MaxBytes() int        { return p.maxBytes }
func (p Prepare) Validate() error {
	if err := validateText("objective", p.objective); err != nil {
		return err
	}
	if len(p.evidence) < 1 || len(p.evidence) > MaxCitations {
		return fmt.Errorf("Handoff preparation evidence must contain 1..%d citations", MaxCitations)
	}
	if err := validateCitations(p.evidence, true); err != nil {
		return err
	}
	return validateBudget(p.maxBytes)
}

type Activate struct {
	boundarySource source.Ref
	objective      string
	evidence       []Citation
	maxBytes       int
}

func NewActivate(boundary source.Ref, objective string, evidence []Citation, maxBytes int) (Activate, error) {
	value := Activate{boundarySource: boundary, objective: objective, evidence: slices.Clone(evidence), maxBytes: maxBytes}
	if err := value.Validate(); err != nil {
		return Activate{}, err
	}
	return value, nil
}

func (a Activate) BoundarySource() source.Ref { return a.boundarySource }
func (a Activate) Objective() string          { return a.objective }
func (a Activate) Evidence() []Citation       { return slices.Clone(a.evidence) }
func (a Activate) MaxBytes() int              { return a.maxBytes }
func (a Activate) Clone() Activate {
	a.evidence = slices.Clone(a.evidence)
	return a
}

func (a Activate) Validate() error {
	if _, err := source.NewRef(a.boundarySource.Type(), a.boundarySource.ID()); err != nil {
		return err
	}
	if err := validateText("objective", a.objective); err != nil {
		return err
	}
	if len(a.evidence) > MaxCitations {
		return fmt.Errorf("Handoff activation evidence exceeds the citation limit")
	}
	if err := validateBudget(a.maxBytes); err != nil {
		return err
	}
	if err := validateCitations(a.evidence, false); err != nil {
		return err
	}
	actionEvidence := a.ActionEvidence()
	if len(actionEvidence) > MaxCitations {
		return fmt.Errorf("Handoff activation evidence exceeds the citation limit")
	}
	if err := validateCitations(actionEvidence, true); err != nil {
		return fmt.Errorf("Handoff activation evidence must be unique: %w", err)
	}
	return nil
}

func (a Activate) ActionEvidence() []Citation {
	boundary := SourceCitation{ref: a.boundarySource}
	result := []Citation{boundary}
	for _, citation := range a.evidence {
		if validateCitation(citation) != nil || citation.citationKey() != boundary.citationKey() {
			result = append(result, citation)
		}
	}
	return result
}

type GenerationRequest struct {
	objective string
	evidence  []Evidence
	maxBytes  int
}

func NewGenerationRequest(objective string, evidence []Evidence, maxBytes int) (GenerationRequest, error) {
	value := GenerationRequest{objective: objective, evidence: slices.Clone(evidence), maxBytes: maxBytes}
	if err := value.Validate(); err != nil {
		return GenerationRequest{}, err
	}
	return value, nil
}
func (r GenerationRequest) Objective() string    { return r.objective }
func (r GenerationRequest) Evidence() []Evidence { return slices.Clone(r.evidence) }
func (r GenerationRequest) MaxBytes() int        { return r.maxBytes }
func (r GenerationRequest) Validate() error {
	if err := validateText("objective", r.objective); err != nil {
		return err
	}
	if len(r.evidence) < 1 || len(r.evidence) > MaxCitations {
		return fmt.Errorf("Handoff generation evidence must contain 1..%d values", MaxCitations)
	}
	for _, evidence := range r.evidence {
		if err := validateEvidence(evidence); err != nil {
			return err
		}
	}
	return validateBudget(r.maxBytes)
}
