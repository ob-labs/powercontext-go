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

package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory/prompts"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/source"
)

type ExtractionProfile string

const (
	CodingProfile       ExtractionProfile = "coding"
	ConversationProfile ExtractionProfile = "conversation"
)

func ExtractionInstructions(profile ExtractionProfile) (string, error) {
	switch profile {
	case CodingProfile:
		return prompts.Coding(), nil
	case ConversationProfile:
		return prompts.Conversation(), nil
	default:
		return "", fmt.Errorf("unsupported Memory extraction profile %q", profile)
	}
}

func ExtractionInstructionsVersion(profile ExtractionProfile) (string, error) {
	switch profile {
	case CodingProfile:
		return prompts.CodingVersion, nil
	case ConversationProfile:
		return prompts.ConversationVersion, nil
	default:
		return "", fmt.Errorf("unsupported Memory extraction profile %q", profile)
	}
}

type CandidateRequest struct {
	sources        []source.Value
	artifacts      []artifact.Snapshot
	currentEntries []EntryVersion
}

func NewCandidateRequest(
	sources []source.Value,
	artifacts []artifact.Snapshot,
	currentEntries []EntryVersion,
) (CandidateRequest, error) {
	for _, value := range sources {
		if value == nil {
			return CandidateRequest{}, fmt.Errorf("Memory candidate Source must not be nil")
		}
		if _, err := source.NewRef("source", value.SourceName()); err != nil {
			return CandidateRequest{}, err
		}
		if value.SourceMaterialization() != source.Captured && value.SourceMaterialization() != source.Referenced {
			return CandidateRequest{}, fmt.Errorf("invalid Source materialization %q", value.SourceMaterialization())
		}
	}
	for _, value := range artifacts {
		if value == nil {
			return CandidateRequest{}, fmt.Errorf("Memory candidate Artifact must not be nil")
		}
		if err := value.Ref().Validate(); err != nil {
			return CandidateRequest{}, err
		}
	}
	entries := cloneEntryVersions(currentEntries)
	return CandidateRequest{
		sources: slices.Clone(sources), artifacts: slices.Clone(artifacts), currentEntries: entries,
	}, nil
}

func (r CandidateRequest) Sources() []source.Value        { return slices.Clone(r.sources) }
func (r CandidateRequest) Artifacts() []artifact.Snapshot { return slices.Clone(r.artifacts) }
func (r CandidateRequest) CurrentEntries() []EntryVersion {
	return cloneEntryVersions(r.currentEntries)
}

type EntryInput struct {
	entry     *EntryVersion
	kind      string
	text      string
	sources   []source.Value
	artifacts []artifact.Snapshot
	reason    *string
}

func NewEntryInput(
	entry *EntryVersion,
	kind, text string,
	sources []source.Value,
	artifacts []artifact.Snapshot,
	reason *string,
) EntryInput {
	return EntryInput{
		entry: cloneEntryVersion(entry), kind: kind, text: text,
		sources: slices.Clone(sources), artifacts: slices.Clone(artifacts), reason: cloneString(reason),
	}
}

func (i EntryInput) Entry() *EntryVersion           { return cloneEntryVersion(i.entry) }
func (i EntryInput) Kind() string                   { return i.kind }
func (i EntryInput) Text() string                   { return i.text }
func (i EntryInput) Sources() []source.Value        { return slices.Clone(i.sources) }
func (i EntryInput) Artifacts() []artifact.Snapshot { return slices.Clone(i.artifacts) }
func (i EntryInput) Reason() *string                { return cloneString(i.reason) }

type EvidenceProjector interface {
	ProjectSource(source.Value) (any, error)
	ProjectArtifact(artifact.Snapshot) (any, error)
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

// ContentEvidenceProjector exposes captured Content Source bodies while
// retaining the default projection for every other exact Source type.
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

type ExtractionEvidence struct {
	evidenceID   string
	evidenceType artifact.EvidenceKind
	content      json.RawMessage
}

func (e ExtractionEvidence) EvidenceID() string                  { return e.evidenceID }
func (e ExtractionEvidence) EvidenceType() artifact.EvidenceKind { return e.evidenceType }
func (e ExtractionEvidence) Content() any {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(e.content))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	return value
}

type ExtractionCurrentEntry struct {
	entryID string
	kind    string
	text    string
}

func (e ExtractionCurrentEntry) EntryID() string { return e.entryID }
func (e ExtractionCurrentEntry) Kind() string    { return e.kind }
func (e ExtractionCurrentEntry) Text() string    { return e.text }

type ExtractionInput struct {
	evidence       []ExtractionEvidence
	currentEntries []ExtractionCurrentEntry
}

func (i ExtractionInput) Evidence() []ExtractionEvidence {
	result := make([]ExtractionEvidence, len(i.evidence))
	for index, evidence := range i.evidence {
		result[index] = evidence
		result[index].content = slices.Clone(evidence.content)
	}
	return result
}

func (i ExtractionInput) CurrentEntries() []ExtractionCurrentEntry {
	return slices.Clone(i.currentEntries)
}

func (i ExtractionInput) MarshalJSON() ([]byte, error) {
	type evidenceDTO struct {
		EvidenceID   string                `json:"evidence_id"`
		EvidenceType artifact.EvidenceKind `json:"evidence_type"`
		Content      json.RawMessage       `json:"content"`
	}
	type entryDTO struct {
		EntryID string `json:"entry_id"`
		Kind    string `json:"kind"`
		Text    string `json:"text"`
	}
	evidence := make([]evidenceDTO, len(i.evidence))
	for index, value := range i.evidence {
		evidence[index] = evidenceDTO{value.evidenceID, value.evidenceType, value.content}
	}
	entries := make([]entryDTO, len(i.currentEntries))
	for index, value := range i.currentEntries {
		entries[index] = entryDTO{value.entryID, value.kind, value.text}
	}
	return marshalExtractionJSON(struct {
		Evidence       []evidenceDTO `json:"evidence"`
		CurrentEntries []entryDTO    `json:"current_entries"`
	}{evidence, entries})
}

type ExtractionIntent string

const (
	ExtractionAdd    ExtractionIntent = "add"
	ExtractionRevise ExtractionIntent = "revise"
)

type ExtractionCandidate struct {
	intent      ExtractionIntent
	kind        string
	text        string
	evidenceIDs []string
	entryID     *string
	reason      *string
}

func NewExtractionCandidate(
	intent ExtractionIntent,
	kind, text string,
	evidenceIDs []string,
	entryID, reason *string,
) (ExtractionCandidate, error) {
	if intent != ExtractionAdd && intent != ExtractionRevise {
		return ExtractionCandidate{}, fmt.Errorf("unsupported Memory extraction intent %q", intent)
	}
	if !knownExtractionKind(kind) {
		return ExtractionCandidate{}, fmt.Errorf("unsupported Memory extraction kind %q", kind)
	}
	return ExtractionCandidate{
		intent: intent, kind: kind, text: text, evidenceIDs: slices.Clone(evidenceIDs),
		entryID: cloneString(entryID), reason: cloneString(reason),
	}, nil
}

func (c ExtractionCandidate) Intent() ExtractionIntent { return c.intent }
func (c ExtractionCandidate) Kind() string             { return c.kind }
func (c ExtractionCandidate) Text() string             { return c.text }
func (c ExtractionCandidate) EvidenceIDs() []string    { return slices.Clone(c.evidenceIDs) }
func (c ExtractionCandidate) EntryID() *string         { return cloneString(c.entryID) }
func (c ExtractionCandidate) Reason() *string          { return cloneString(c.reason) }

type ExtractionOutput struct{ candidates []ExtractionCandidate }

func NewExtractionOutput(candidates []ExtractionCandidate) ExtractionOutput {
	return ExtractionOutput{candidates: cloneExtractionCandidates(candidates)}
}

func (o ExtractionOutput) Candidates() []ExtractionCandidate {
	return cloneExtractionCandidates(o.candidates)
}

type CandidatePipeline interface {
	Extract(context.Context, CandidateRequest) ([]EntryInput, error)
}

type LLMCandidatePipeline struct {
	generator inference.StructuredGenerator[ExtractionInput, ExtractionOutput]
	projector EvidenceProjector
}

func NewLLMCandidatePipeline(
	generator inference.StructuredGenerator[ExtractionInput, ExtractionOutput],
	projector EvidenceProjector,
) (*LLMCandidatePipeline, error) {
	if generator == nil {
		return nil, fmt.Errorf("Memory extraction generator must not be nil")
	}
	if projector == nil {
		projector = DefaultEvidenceProjector{}
	}
	return &LLMCandidatePipeline{generator: generator, projector: projector}, nil
}

func NewExtractionPromptedGenerator(
	model inference.TextModel,
	profile ExtractionProfile,
	limits *inference.Limits,
	settings inference.GenerationSettings,
) (*inference.PromptedGenerator[ExtractionInput, ExtractionOutput], error) {
	instructions, err := ExtractionInstructions(profile)
	if err != nil {
		return nil, err
	}
	codec, err := inference.NewJSONCodec[ExtractionInput, ExtractionOutput](
		prompts.ExtractionSchema(), nil, decodeExtractionOutput,
	)
	if err != nil {
		return nil, err
	}
	return inference.NewPromptedGenerator(model, instructions, codec, limits, settings)
}

func (p *LLMCandidatePipeline) Extract(ctx context.Context, request CandidateRequest) ([]EntryInput, error) {
	input, evidence, err := extractionInput(request, p.projector)
	if err != nil {
		return nil, err
	}
	result, err := p.generator.Generate(ctx, input)
	if err != nil {
		return nil, err
	}
	current := make(map[string]EntryVersion, len(request.currentEntries))
	for _, entry := range request.currentEntries {
		current[entry.EntryID] = entry.Clone()
	}
	revised := make(map[string]struct{})
	candidates := make([]EntryInput, 0, len(result.Output.candidates))
	for _, proposal := range result.Output.candidates {
		sources, artifacts, err := selectedExtractionEvidence(proposal, evidence)
		if err != nil {
			return nil, err
		}
		entry, err := extractionRevisionTarget(proposal, current, revised)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, NewEntryInput(
			entry, proposal.kind, proposal.text, sources, artifacts, proposal.reason,
		))
	}
	return candidates, nil
}

type extractionEvidenceValue struct {
	source   source.Value
	artifact artifact.Snapshot
}

func extractionInput(
	request CandidateRequest,
	projector EvidenceProjector,
) (ExtractionInput, map[string]extractionEvidenceValue, error) {
	values := make([]ExtractionEvidence, 0, len(request.sources)+len(request.artifacts))
	evidence := make(map[string]extractionEvidenceValue, cap(values))
	for index, value := range request.sources {
		id := fmt.Sprintf("source:%d", index)
		projected, err := projector.ProjectSource(value)
		if err != nil {
			return ExtractionInput{}, nil, &EvidenceProjectionError{Kind: "source"}
		}
		encoded, err := marshalExtractionJSON(projected)
		if err != nil {
			return ExtractionInput{}, nil, &EvidenceProjectionError{Kind: "source"}
		}
		values = append(values, ExtractionEvidence{id, artifact.SourceEvidence, encoded})
		evidence[id] = extractionEvidenceValue{source: value}
	}
	for index, value := range request.artifacts {
		id := fmt.Sprintf("artifact:%d", index)
		projected, err := projector.ProjectArtifact(value)
		if err != nil {
			return ExtractionInput{}, nil, &EvidenceProjectionError{Kind: "artifact"}
		}
		encoded, err := marshalExtractionJSON(projected)
		if err != nil {
			return ExtractionInput{}, nil, &EvidenceProjectionError{Kind: "artifact"}
		}
		values = append(values, ExtractionEvidence{id, artifact.ArtifactEvidence, encoded})
		evidence[id] = extractionEvidenceValue{artifact: value}
	}
	entries := make([]ExtractionCurrentEntry, len(request.currentEntries))
	for index, entry := range request.currentEntries {
		entries[index] = ExtractionCurrentEntry{entry.EntryID, entry.Kind, entry.Text}
	}
	return ExtractionInput{evidence: values, currentEntries: entries}, evidence, nil
}

func selectedExtractionEvidence(
	proposal ExtractionCandidate,
	evidence map[string]extractionEvidenceValue,
) ([]source.Value, []artifact.Snapshot, error) {
	if len(proposal.evidenceIDs) == 0 {
		return nil, nil, inference.NewInvalidOutputError("memory-extract", "candidate does not cite evidence")
	}
	selectedSources := make([]source.Value, 0)
	selectedArtifacts := make([]artifact.Snapshot, 0)
	seen := make(map[string]struct{}, len(proposal.evidenceIDs))
	for _, id := range proposal.evidenceIDs {
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		value, exists := evidence[id]
		if !exists {
			return nil, nil, inference.NewInvalidOutputError("memory-extract", "candidate cites evidence outside the request")
		}
		if value.source != nil {
			selectedSources = append(selectedSources, value.source)
		} else {
			selectedArtifacts = append(selectedArtifacts, value.artifact)
		}
	}
	return selectedSources, selectedArtifacts, nil
}

func extractionRevisionTarget(
	proposal ExtractionCandidate,
	current map[string]EntryVersion,
	revised map[string]struct{},
) (*EntryVersion, error) {
	if proposal.intent == ExtractionAdd {
		if proposal.entryID != nil {
			return nil, inference.NewInvalidOutputError("memory-extract", "add candidate must not identify an existing entry")
		}
		return nil, nil
	}
	if proposal.intent != ExtractionRevise {
		return nil, inference.NewInvalidOutputError("memory-extract", "candidate has an unsupported intent")
	}
	if proposal.entryID == nil {
		return nil, inference.NewInvalidOutputError("memory-extract", "revise candidate must identify an active entry")
	}
	entry, exists := current[*proposal.entryID]
	if !exists {
		return nil, inference.NewInvalidOutputError("memory-extract", "revise candidate does not identify an active entry")
	}
	if _, exists := revised[*proposal.entryID]; exists {
		return nil, inference.NewInvalidOutputError("memory-extract", "an active entry can only be revised once per extraction")
	}
	revised[*proposal.entryID] = struct{}{}
	copy := entry.Clone()
	return &copy, nil
}

func decodeExtractionOutput(encoded []byte) (ExtractionOutput, error) {
	fields, err := decodeExtractionObject(encoded)
	if err != nil {
		return ExtractionOutput{}, err
	}
	raw, exists := fields["candidates"]
	if !exists {
		return ExtractionOutput{}, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return ExtractionOutput{}, fmt.Errorf("Memory extraction candidates must be an array")
	}
	result := make([]ExtractionCandidate, 0, len(values))
	for _, value := range values {
		candidate, err := decodeExtractionCandidate(value)
		if err != nil {
			return ExtractionOutput{}, err
		}
		result = append(result, candidate)
	}
	return NewExtractionOutput(result), nil
}

func decodeExtractionCandidate(encoded []byte) (ExtractionCandidate, error) {
	fields, err := decodeExtractionObject(encoded)
	if err != nil {
		return ExtractionCandidate{}, err
	}
	var intent ExtractionIntent
	var kind, text string
	for name, target := range map[string]any{
		"intent": &intent, "kind": &kind, "text": &text,
	} {
		raw, exists := fields[name]
		if !exists || json.Unmarshal(raw, target) != nil {
			return ExtractionCandidate{}, fmt.Errorf("Memory extraction candidate field %s is invalid", name)
		}
	}
	evidenceRaw, exists := fields["evidence_ids"]
	if !exists {
		return ExtractionCandidate{}, fmt.Errorf("Memory extraction candidate evidence is missing")
	}
	var evidenceIDs []string
	if unmarshalErr := json.Unmarshal(evidenceRaw, &evidenceIDs); unmarshalErr != nil || evidenceIDs == nil {
		return ExtractionCandidate{}, fmt.Errorf("Memory extraction candidate evidence is invalid")
	}
	entryID, err := decodeOptionalString(fields, "entry_id")
	if err != nil {
		return ExtractionCandidate{}, err
	}
	reason, err := decodeOptionalString(fields, "reason")
	if err != nil {
		return ExtractionCandidate{}, err
	}
	return NewExtractionCandidate(intent, kind, text, evidenceIDs, entryID, reason)
}

func decodeOptionalString(fields map[string]json.RawMessage, name string) (*string, error) {
	raw, exists := fields[name]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("Memory extraction candidate field %s is invalid", name)
	}
	return &value, nil
}

func decodeExtractionObject(encoded []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var value map[string]json.RawMessage
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("JSON value has trailing data")
	}
	return value, nil
}

func marshalExtractionJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func knownExtractionKind(value string) bool {
	switch value {
	case "fact", "preference", "decision", "constraint", "working_note":
		return true
	default:
		return false
	}
}

func cloneEntryVersion(value *EntryVersion) *EntryVersion {
	if value == nil {
		return nil
	}
	copy := value.Clone()
	return &copy
}

func cloneExtractionCandidates(values []ExtractionCandidate) []ExtractionCandidate {
	result := make([]ExtractionCandidate, len(values))
	for index, value := range values {
		result[index] = value
		result[index].evidenceIDs = slices.Clone(value.evidenceIDs)
		result[index].entryID = cloneString(value.entryID)
		result[index].reason = cloneString(value.reason)
	}
	return result
}

type EvidenceProjectionError struct{ Kind string }

func (e *EvidenceProjectionError) Error() string {
	return "Memory " + strings.TrimSpace(e.Kind) + " evidence projection is not JSON-compatible"
}
