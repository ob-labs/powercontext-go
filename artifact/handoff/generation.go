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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff/prompts"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/source"
)

type EvidenceProjector interface {
	ProjectSource(source.Value) (any, error)
	ProjectArtifact(artifact.Snapshot) (any, error)
	ProjectMemoryEntry(memory.EntryVersion) (any, error)
}

type DefaultEvidenceProjector struct{}

func (DefaultEvidenceProjector) ProjectSource(value source.Value) (any, error) {
	description, hasDescription := value.SourceDescription()
	var projectedDescription *string
	if hasDescription {
		projectedDescription = &description
	}
	return struct {
		Name            string                 `json:"name"`
		Materialization source.Materialization `json:"materialization"`
		Description     *string                `json:"description"`
	}{value.SourceName(), value.SourceMaterialization(), projectedDescription}, nil
}

func (DefaultEvidenceProjector) ProjectArtifact(value artifact.Snapshot) (any, error) {
	ref := value.Ref()
	return struct {
		ArtifactID string `json:"artifact_id"`
		Revision   int64  `json:"revision"`
		Family     string `json:"family"`
		Content    any    `json:"content"`
	}{ref.ID(), ref.Revision(), ref.Family(), value.ContentValue()}, nil
}

func (DefaultEvidenceProjector) ProjectMemoryEntry(value memory.EntryVersion) (any, error) {
	return struct {
		EntryID        string `json:"entry_id"`
		EntryVersionID string `json:"entry_version_id"`
		Kind           string `json:"kind"`
		Text           string `json:"text"`
	}{value.EntryID, value.EntryVersionID, value.Kind, value.Text}, nil
}

type ContentEvidenceProjector struct{ fallback EvidenceProjector }

func NewContentEvidenceProjector(fallback EvidenceProjector) ContentEvidenceProjector {
	if fallback == nil {
		fallback = DefaultEvidenceProjector{}
	}
	return ContentEvidenceProjector{fallback: fallback}
}

func (p ContentEvidenceProjector) ProjectSource(value source.Value) (any, error) {
	if content, ok := value.(source.ContentSource); ok {
		return struct {
			SourceType string         `json:"source_type"`
			SourceID   string         `json:"source_id"`
			Content    string         `json:"content"`
			Metadata   map[string]any `json:"metadata"`
		}{source.ContentType, content.SourceName(), content.Content(), content.Metadata()}, nil
	}
	return p.fallback.ProjectSource(value)
}

func (p ContentEvidenceProjector) ProjectArtifact(value artifact.Snapshot) (any, error) {
	return p.fallback.ProjectArtifact(value)
}

func (p ContentEvidenceProjector) ProjectMemoryEntry(value memory.EntryVersion) (any, error) {
	return p.fallback.ProjectMemoryEntry(value)
}

type GenerationEvidenceInput struct {
	evidenceID   string
	evidenceType CitationKind
	content      json.RawMessage
}

func (e GenerationEvidenceInput) EvidenceID() string         { return e.evidenceID }
func (e GenerationEvidenceInput) EvidenceType() CitationKind { return e.evidenceType }
func (e GenerationEvidenceInput) Content() any {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(e.content))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	return value
}

type GenerationInput struct {
	objective string
	evidence  []GenerationEvidenceInput
	maxBytes  int
}

func (i GenerationInput) Objective() string { return i.objective }
func (i GenerationInput) MaxBytes() int     { return i.maxBytes }
func (i GenerationInput) Evidence() []GenerationEvidenceInput {
	result := make([]GenerationEvidenceInput, len(i.evidence))
	for index, value := range i.evidence {
		result[index] = value
		result[index].content = slices.Clone(value.content)
	}
	return result
}

func (i GenerationInput) MarshalJSON() ([]byte, error) {
	type evidenceDTO struct {
		EvidenceID   string          `json:"evidence_id"`
		EvidenceType CitationKind    `json:"evidence_type"`
		Content      json.RawMessage `json:"content"`
	}
	values := make([]evidenceDTO, len(i.evidence))
	for index, evidence := range i.evidence {
		values[index] = evidenceDTO{evidence.evidenceID, evidence.evidenceType, evidence.content}
	}
	return marshalGenerationJSON(struct {
		Objective string        `json:"objective"`
		Evidence  []evidenceDTO `json:"evidence"`
		MaxBytes  int           `json:"max_bytes"`
	}{i.objective, values, i.maxBytes})
}

type GenerationStatement struct {
	text        string
	evidenceIDs []string
}

func NewGenerationStatement(text string, evidenceIDs []string) GenerationStatement {
	return GenerationStatement{text: text, evidenceIDs: slices.Clone(evidenceIDs)}
}

func (s GenerationStatement) Text() string          { return s.text }
func (s GenerationStatement) EvidenceIDs() []string { return slices.Clone(s.evidenceIDs) }

type GenerationOmission struct {
	text       string
	evidenceID *string
}

func NewGenerationOmission(text string, evidenceID *string) GenerationOmission {
	return GenerationOmission{text: text, evidenceID: cloneText(evidenceID)}
}

func (o GenerationOmission) Text() string        { return o.text }
func (o GenerationOmission) EvidenceID() *string { return cloneText(o.evidenceID) }

type GenerationOutput struct {
	state       []GenerationStatement
	disposition Disposition
	nextAction  *GenerationStatement
	omissions   []GenerationOmission
}

func NewGenerationOutput(
	state []GenerationStatement,
	disposition Disposition,
	nextAction *GenerationStatement,
	omissions []GenerationOmission,
) (GenerationOutput, error) {
	value := GenerationOutput{
		state: cloneGenerationStatements(state), disposition: disposition,
		nextAction: cloneGenerationStatement(nextAction), omissions: cloneGenerationOmissions(omissions),
	}
	if err := value.Validate(); err != nil {
		return GenerationOutput{}, err
	}
	return value, nil
}

func (o GenerationOutput) State() []GenerationStatement { return cloneGenerationStatements(o.state) }
func (o GenerationOutput) Disposition() Disposition     { return o.disposition }
func (o GenerationOutput) NextAction() *GenerationStatement {
	return cloneGenerationStatement(o.nextAction)
}

func (o GenerationOutput) Omissions() []GenerationOmission {
	return cloneGenerationOmissions(o.omissions)
}

func (o GenerationOutput) Validate() error {
	if o.disposition != Continuable && o.disposition != Blocked && o.disposition != Complete {
		return fmt.Errorf("invalid Handoff generation disposition %q", o.disposition)
	}
	return nil
}

type LLMGenerationPipeline struct {
	generator inference.StructuredGenerator[GenerationInput, GenerationOutput]
	projector EvidenceProjector
}

func NewLLMGenerationPipeline(
	generator inference.StructuredGenerator[GenerationInput, GenerationOutput],
	projector EvidenceProjector,
) (*LLMGenerationPipeline, error) {
	if generator == nil {
		return nil, fmt.Errorf("Handoff generator must not be nil")
	}
	if projector == nil {
		projector = DefaultEvidenceProjector{}
	}
	return &LLMGenerationPipeline{generator: generator, projector: projector}, nil
}

func NewPromptedGenerator(
	model inference.TextModel,
	limits *inference.Limits,
	settings inference.GenerationSettings,
) (*inference.PromptedGenerator[GenerationInput, GenerationOutput], error) {
	codec, err := inference.NewJSONCodec[GenerationInput, GenerationOutput](
		prompts.GenerationSchema(), nil, decodeGenerationOutput,
	)
	if err != nil {
		return nil, err
	}
	return inference.NewPromptedGenerator(model, prompts.Generation(), codec, limits, settings)
}

func (p *LLMGenerationPipeline) Generate(ctx context.Context, request GenerationRequest) (Draft, error) {
	if err := request.Validate(); err != nil {
		return Draft{}, err
	}
	input, citations, err := projectGenerationInput(request, p.projector)
	if err != nil {
		return Draft{}, err
	}
	result, err := p.generator.Generate(ctx, input)
	if err != nil {
		return Draft{}, err
	}
	if validationErr := result.Output.Validate(); validationErr != nil {
		return Draft{}, inference.NewInvalidOutputError(
			"handoff-generate", "generator returned an invalid output tree",
		)
	}
	state := make([]Statement, 0, len(result.Output.state))
	for _, value := range result.Output.state {
		statement, mapErr := mapGenerationStatement(value, citations)
		if mapErr != nil {
			return Draft{}, mapErr
		}
		state = append(state, statement)
	}
	var nextAction *Statement
	if result.Output.nextAction != nil {
		mapped, mapErr := mapGenerationStatement(*result.Output.nextAction, citations)
		if mapErr != nil {
			return Draft{}, mapErr
		}
		nextAction = &mapped
	}
	omissions := make([]Omission, 0, len(result.Output.omissions))
	for _, value := range result.Output.omissions {
		mapped, mapErr := mapGenerationOmission(value, citations)
		if mapErr != nil {
			return Draft{}, mapErr
		}
		omissions = append(omissions, mapped)
	}
	draft, err := NewDraft(request.objective, state, result.Output.disposition, nextAction, omissions)
	if err != nil {
		return Draft{}, inference.NewInvalidOutputError(
			"handoff-generate", "generated content violates the Handoff Draft contract",
		)
	}
	return draft, nil
}

func projectGenerationInput(
	request GenerationRequest,
	projector EvidenceProjector,
) (GenerationInput, map[string]Citation, error) {
	values := make([]GenerationEvidenceInput, 0, len(request.evidence))
	citations := make(map[string]Citation, len(request.evidence))
	for index, evidence := range request.evidence {
		citation := evidence.Citation()
		id := fmt.Sprintf("%s:%d", citation.Kind(), index)
		projected, err := projectGenerationEvidence(evidence, projector)
		if err != nil {
			return GenerationInput{}, nil, inference.NewInvalidOutputError(
				"handoff-generate", "Handoff evidence projection is not JSON-compatible",
			)
		}
		encoded, err := marshalGenerationJSON(projected)
		if err != nil {
			return GenerationInput{}, nil, inference.NewInvalidOutputError(
				"handoff-generate", "Handoff evidence projection is not JSON-compatible",
			)
		}
		values = append(values, GenerationEvidenceInput{id, citation.Kind(), encoded})
		citations[id] = citation
	}
	return GenerationInput{request.objective, values, request.maxBytes}, citations, nil
}

func projectGenerationEvidence(evidence Evidence, projector EvidenceProjector) (any, error) {
	switch value := evidence.(type) {
	case SourceEvidence:
		return projector.ProjectSource(value.Source())
	case ArtifactEvidence:
		return projector.ProjectArtifact(value.Artifact())
	case MemoryEvidence:
		return projector.ProjectMemoryEntry(value.Entry())
	default:
		return nil, fmt.Errorf("unsupported Handoff evidence %T", evidence)
	}
}

func mapGenerationStatement(value GenerationStatement, citations map[string]Citation) (Statement, error) {
	if len(value.evidenceIDs) == 0 {
		return Statement{}, inference.NewInvalidOutputError("handoff-generate", "statement does not cite evidence")
	}
	selected := make([]Citation, 0, len(value.evidenceIDs))
	seen := make(map[string]struct{}, len(value.evidenceIDs))
	for _, id := range value.evidenceIDs {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		citation, exists := citations[id]
		if !exists {
			return Statement{}, inference.NewInvalidOutputError(
				"handoff-generate", "generated content cites evidence outside the request",
			)
		}
		selected = append(selected, citation)
	}
	statement, err := NewStatement(value.text, selected)
	if err != nil {
		return Statement{}, inference.NewInvalidOutputError(
			"handoff-generate", "generated content violates the Handoff Draft contract",
		)
	}
	return statement, nil
}

func mapGenerationOmission(value GenerationOmission, citations map[string]Citation) (Omission, error) {
	var citation Citation
	if value.evidenceID != nil {
		var exists bool
		citation, exists = citations[*value.evidenceID]
		if !exists {
			return Omission{}, inference.NewInvalidOutputError(
				"handoff-generate", "generated content cites evidence outside the request",
			)
		}
	}
	omission, err := NewOmission(value.text, citation)
	if err != nil {
		return Omission{}, inference.NewInvalidOutputError(
			"handoff-generate", "generated content violates the Handoff Draft contract",
		)
	}
	return omission, nil
}

func decodeGenerationOutput(encoded []byte) (GenerationOutput, error) {
	fields, err := decodeGenerationObject(encoded)
	if err != nil {
		return GenerationOutput{}, err
	}
	stateRaw, stateExists := fields["state"]
	dispositionRaw, dispositionExists := fields["disposition"]
	if !stateExists || !dispositionExists {
		return GenerationOutput{}, fmt.Errorf("Handoff generation output is incomplete")
	}
	state, err := decodeGenerationStatements(stateRaw)
	if err != nil {
		return GenerationOutput{}, err
	}
	var disposition Disposition
	if unmarshalErr := json.Unmarshal(dispositionRaw, &disposition); unmarshalErr != nil {
		return GenerationOutput{}, fmt.Errorf("Handoff generation disposition is invalid")
	}
	var nextAction *GenerationStatement
	if raw, exists := fields["next_action"]; exists && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		value, decodeErr := decodeGenerationStatement(raw)
		if decodeErr != nil {
			return GenerationOutput{}, decodeErr
		}
		nextAction = &value
	}
	var omissions []GenerationOmission
	if raw, exists := fields["omissions"]; exists {
		omissions, err = decodeGenerationOmissions(raw)
		if err != nil {
			return GenerationOutput{}, err
		}
	}
	return NewGenerationOutput(state, disposition, nextAction, omissions)
}

func decodeGenerationStatements(encoded []byte) ([]GenerationStatement, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(encoded, &values); err != nil || values == nil {
		return nil, fmt.Errorf("Handoff generation statements must be an array")
	}
	result := make([]GenerationStatement, 0, len(values))
	for _, raw := range values {
		value, err := decodeGenerationStatement(raw)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func decodeGenerationStatement(encoded []byte) (GenerationStatement, error) {
	fields, err := decodeGenerationObject(encoded)
	if err != nil {
		return GenerationStatement{}, err
	}
	textRaw, textExists := fields["text"]
	evidenceRaw, evidenceExists := fields["evidence_ids"]
	if !textExists || !evidenceExists {
		return GenerationStatement{}, fmt.Errorf("Handoff generation statement is incomplete")
	}
	var text string
	var ids []string
	if json.Unmarshal(textRaw, &text) != nil || json.Unmarshal(evidenceRaw, &ids) != nil || ids == nil {
		return GenerationStatement{}, fmt.Errorf("Handoff generation statement is invalid")
	}
	return NewGenerationStatement(text, ids), nil
}

func decodeGenerationOmissions(encoded []byte) ([]GenerationOmission, error) {
	var values []json.RawMessage
	if err := json.Unmarshal(encoded, &values); err != nil || values == nil {
		return nil, fmt.Errorf("Handoff generation omissions must be an array")
	}
	result := make([]GenerationOmission, 0, len(values))
	for _, raw := range values {
		fields, err := decodeGenerationObject(raw)
		if err != nil {
			return nil, err
		}
		textRaw, exists := fields["text"]
		if !exists {
			return nil, fmt.Errorf("Handoff generation omission text is missing")
		}
		var text string
		if json.Unmarshal(textRaw, &text) != nil {
			return nil, fmt.Errorf("Handoff generation omission text is invalid")
		}
		id, err := decodeGenerationOptionalString(fields, "evidence_id")
		if err != nil {
			return nil, err
		}
		result = append(result, NewGenerationOmission(text, id))
	}
	return result, nil
}

func decodeGenerationOptionalString(fields map[string]json.RawMessage, name string) (*string, error) {
	raw, exists := fields[name]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("Handoff generation field %s is invalid", name)
	}
	return &value, nil
}

func decodeGenerationObject(encoded []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("JSON value has trailing data")
	}
	return fields, nil
}

func marshalGenerationJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func cloneGenerationStatement(value *GenerationStatement) *GenerationStatement {
	if value == nil {
		return nil
	}
	copy := *value
	copy.evidenceIDs = slices.Clone(value.evidenceIDs)
	return &copy
}

func cloneGenerationStatements(values []GenerationStatement) []GenerationStatement {
	result := make([]GenerationStatement, len(values))
	for index, value := range values {
		result[index] = value
		result[index].evidenceIDs = slices.Clone(value.evidenceIDs)
	}
	return result
}

func cloneGenerationOmissions(values []GenerationOmission) []GenerationOmission {
	result := make([]GenerationOmission, len(values))
	for index, value := range values {
		result[index] = value
		result[index].evidenceID = cloneText(value.evidenceID)
	}
	return result
}

func cloneText(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
