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

package experience

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/artifact/experience/prompts"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/source"
)

const (
	TaskOutcomeSourceKind    = "task-outcome"
	IncubationCursorName     = "experience-incubation"
	IncubationWindowLimit    = 32
	MaxIncubationSources     = 100
	MaxIncubationSourceChars = 64_000
	MaxCandidateEvidence     = 32
	IncubationReason         = "Incubated from bounded task-outcome evidence by the configured Experience pipeline."
)

type IncubationEvidence struct {
	evidenceID string
	content    string
}

func (e IncubationEvidence) EvidenceID() string { return e.evidenceID }
func (e IncubationEvidence) Content() string    { return e.content }

type IncubationInput struct{ evidence []IncubationEvidence }

func (i IncubationInput) Evidence() []IncubationEvidence { return slices.Clone(i.evidence) }

func (i IncubationInput) MarshalJSON() ([]byte, error) {
	type evidenceDTO struct {
		EvidenceID string `json:"evidence_id"`
		Content    string `json:"content"`
	}
	projected := make([]evidenceDTO, len(i.evidence))
	for index, evidence := range i.evidence {
		projected[index] = evidenceDTO{EvidenceID: evidence.evidenceID, Content: evidence.content}
	}
	return marshalJSON(struct {
		Evidence []evidenceDTO `json:"evidence"`
	}{Evidence: projected})
}

type IncubationCandidate struct {
	proposal    Content
	evidenceIDs []string
}

func NewIncubationCandidate(proposal Content, evidenceIDs []string) (IncubationCandidate, error) {
	if len(evidenceIDs) < 1 || len(evidenceIDs) > MaxCandidateEvidence {
		return IncubationCandidate{}, fmt.Errorf("Experience candidate evidence must contain 1..%d identifiers", MaxCandidateEvidence)
	}
	return IncubationCandidate{proposal: proposal, evidenceIDs: slices.Clone(evidenceIDs)}, nil
}

func (c IncubationCandidate) Proposal() Content     { return c.proposal }
func (c IncubationCandidate) EvidenceIDs() []string { return slices.Clone(c.evidenceIDs) }

type IncubationOutput struct{ candidates []IncubationCandidate }

func NewIncubationOutput(candidates []IncubationCandidate) IncubationOutput {
	return IncubationOutput{candidates: cloneIncubationCandidates(candidates)}
}

func (o IncubationOutput) Candidates() []IncubationCandidate {
	return cloneIncubationCandidates(o.candidates)
}

type CandidateInput struct {
	proposal Content
	sources  []source.Ref
	reason   string
}

func NewCandidateInput(proposal Content, sources []source.Ref) (CandidateInput, error) {
	if len(sources) < 1 || len(sources) > MaxCandidateEvidence {
		return CandidateInput{}, fmt.Errorf("Experience candidate sources must contain 1..%d references", MaxCandidateEvidence)
	}
	for _, ref := range sources {
		if _, err := source.NewRef(ref.Type(), ref.ID()); err != nil {
			return CandidateInput{}, err
		}
	}
	return CandidateInput{proposal: proposal, sources: slices.Clone(sources), reason: IncubationReason}, nil
}

func (i CandidateInput) Proposal() Content     { return i.proposal }
func (i CandidateInput) Sources() []source.Ref { return slices.Clone(i.sources) }
func (i CandidateInput) Reason() string        { return i.reason }

type CandidatePipeline interface {
	Incubate(context.Context, []source.Value) ([]CandidateInput, error)
}

type LLMCandidatePipeline struct {
	generator inference.StructuredGenerator[IncubationInput, IncubationOutput]
}

func NewLLMCandidatePipeline(
	generator inference.StructuredGenerator[IncubationInput, IncubationOutput],
) (*LLMCandidatePipeline, error) {
	if generator == nil {
		return nil, fmt.Errorf("Experience incubation generator must not be nil")
	}
	return &LLMCandidatePipeline{generator: generator}, nil
}

func NewIncubationPromptedGenerator(
	model inference.TextModel,
	limits *inference.Limits,
	settings inference.GenerationSettings,
) (*inference.PromptedGenerator[IncubationInput, IncubationOutput], error) {
	codec, err := inference.NewJSONCodec[IncubationInput, IncubationOutput](
		prompts.IncubationSchema(), nil, decodeIncubationOutput,
	)
	if err != nil {
		return nil, err
	}
	return inference.NewPromptedGenerator(model, prompts.Incubation(), codec, limits, settings)
}

func (p *LLMCandidatePipeline) Incubate(ctx context.Context, values []source.Value) ([]CandidateInput, error) {
	input, evidence, ok, err := projectIncubation(values)
	if err != nil || !ok {
		return nil, err
	}
	result, err := p.generator.Generate(ctx, input)
	if err != nil {
		return nil, err
	}

	candidates := make([]CandidateInput, 0, len(result.Output.candidates))
	seen := make(map[string]struct{})
	for _, candidate := range result.Output.candidates {
		selected, err := selectSources(candidate.evidenceIDs, evidence)
		if err != nil {
			return nil, err
		}
		keyBytes, _ := json.Marshal(struct {
			Proposal contentKeyDTO     `json:"proposal"`
			Sources  []sourceRefKeyDTO `json:"sources"`
		}{
			Proposal: contentKey(candidate.proposal),
			Sources:  sourceRefKeys(selected),
		})
		key := string(keyBytes)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		input, err := NewCandidateInput(candidate.proposal, selected)
		if err != nil {
			return nil, inference.NewInvalidOutputError("experience-incubate", "generator returned an invalid output tree")
		}
		candidates = append(candidates, input)
	}
	return candidates, nil
}

type contentKeyDTO struct {
	Situation string `json:"situation"`
	Action    string `json:"action"`
	Outcome   string `json:"outcome"`
	Lesson    string `json:"lesson"`
}

func contentKey(value Content) contentKeyDTO {
	return contentKeyDTO{value.Situation(), value.Action(), value.Outcome(), value.Lesson()}
}

func projectIncubation(values []source.Value) (IncubationInput, map[string]source.Ref, bool, error) {
	projected := make([]IncubationEvidence, 0)
	evidence := make(map[string]source.Ref)
	for _, value := range values {
		content, ok := value.(source.ContentSource)
		if !ok {
			continue
		}
		kind, _ := content.Metadata()["kind"].(string)
		if kind != TaskOutcomeSourceKind {
			continue
		}
		text := truncateRunes(content.Content(), MaxIncubationSourceChars)
		if text == "" {
			return IncubationInput{}, nil, false, inference.NewInvalidOutputError(
				"experience-incubate", "eligible Task Outcome evidence exceeded the bounded input contract",
			)
		}
		id := "source:" + source.ContentType + "/" + content.SourceName()
		ref, err := source.NewRef(source.ContentType, content.SourceName())
		if err != nil {
			return IncubationInput{}, nil, false, err
		}
		projected = append(projected, IncubationEvidence{evidenceID: id, content: text})
		evidence[id] = ref
	}
	if len(projected) == 0 {
		return IncubationInput{}, nil, false, nil
	}
	if len(projected) > MaxIncubationSources {
		return IncubationInput{}, nil, false, inference.NewInvalidOutputError(
			"experience-incubate", "eligible Task Outcome evidence exceeded the bounded input contract",
		)
	}
	return IncubationInput{evidence: projected}, evidence, true, nil
}

func selectSources(ids []string, evidence map[string]source.Ref) ([]source.Ref, error) {
	selected := make([]source.Ref, 0, len(ids))
	seen := make(map[string]struct{})
	for _, id := range ids {
		ref, exists := evidence[id]
		if !exists {
			return nil, inference.NewInvalidOutputError(
				"experience-incubate", "candidate cited evidence outside the current Task Outcome window",
			)
		}
		key := ref.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		selected = append(selected, ref)
	}
	return selected, nil
}

type sourceRefKeyDTO struct {
	Type string `json:"source_type"`
	ID   string `json:"source_id"`
}

func sourceRefKeys(refs []source.Ref) []sourceRefKeyDTO {
	result := make([]sourceRefKeyDTO, len(refs))
	for index, ref := range refs {
		result[index] = sourceRefKeyDTO{Type: ref.Type(), ID: ref.ID()}
	}
	return result
}

func truncateRunes(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum])
}

func decodeIncubationOutput(encoded []byte) (IncubationOutput, error) {
	fields, err := decodeObject(encoded)
	if err != nil {
		return IncubationOutput{}, err
	}
	raw, exists := fields["candidates"]
	if !exists {
		return IncubationOutput{}, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return IncubationOutput{}, fmt.Errorf("Experience candidates must be an array")
	}
	candidates := make([]IncubationCandidate, 0, len(values))
	for _, encodedCandidate := range values {
		candidateFields, err := decodeObject(encodedCandidate)
		if err != nil {
			return IncubationOutput{}, err
		}
		proposalRaw, proposalExists := candidateFields["proposal"]
		evidenceRaw, evidenceExists := candidateFields["evidence_ids"]
		if !proposalExists || !evidenceExists {
			return IncubationOutput{}, fmt.Errorf("Experience candidate is incomplete")
		}
		proposal, err := decodeContent(proposalRaw)
		if err != nil {
			return IncubationOutput{}, err
		}
		var evidenceIDs []string
		if unmarshalErr := json.Unmarshal(evidenceRaw, &evidenceIDs); unmarshalErr != nil || evidenceIDs == nil {
			return IncubationOutput{}, fmt.Errorf("Experience candidate evidence is invalid")
		}
		candidate, err := NewIncubationCandidate(proposal, evidenceIDs)
		if err != nil {
			return IncubationOutput{}, err
		}
		candidates = append(candidates, candidate)
	}
	return NewIncubationOutput(candidates), nil
}

func cloneIncubationCandidates(values []IncubationCandidate) []IncubationCandidate {
	result := make([]IncubationCandidate, len(values))
	for index, value := range values {
		result[index] = value
		result[index].evidenceIDs = slices.Clone(value.evidenceIDs)
	}
	return result
}
