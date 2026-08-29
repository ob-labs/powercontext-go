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
	"strings"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/source"
)

type EvidenceResolver interface {
	Resolve(context.Context, Citation) (Evidence, error)
	Validate(context.Context, Citation) error
}

type GenerationPipeline interface {
	Generate(context.Context, GenerationRequest) (Draft, error)
}

type Backend interface {
	Create(context.Context, string, ArtifactDraft) (Handoff, error)
	Revise(context.Context, Handoff, ArtifactDraft) (Handoff, error)
	Get(context.Context, artifact.Ref) (Handoff, error)
	Latest(context.Context, string) (Handoff, bool, error)
	Revisions(context.Context, string) ([]Handoff, error)
}

type Service struct {
	scopeID    string
	artifactID string
	backend    Backend
	resolver   EvidenceResolver
	pipeline   GenerationPipeline
}

func NewService(scopeID, artifactID string, backend Backend, resolver EvidenceResolver, pipeline GenerationPipeline) (*Service, error) {
	if strings.TrimSpace(scopeID) == "" {
		return nil, fmt.Errorf("scope_id must not be empty")
	}
	if _, err := artifact.NewRef(Family, artifactID, 1); err != nil {
		return nil, err
	}
	if backend == nil || resolver == nil {
		return nil, fmt.Errorf("Handoff backend and evidence resolver must be configured")
	}
	return &Service{scopeID: scopeID, artifactID: artifactID, backend: backend, resolver: resolver, pipeline: pipeline}, nil
}

func (s *Service) Prepare(ctx context.Context, action Prepare) (Draft, error) {
	if err := action.Validate(); err != nil {
		return Draft{}, err
	}
	if s.pipeline == nil {
		return Draft{}, &GenerationUnavailableError{}
	}
	citations := action.Evidence()
	evidence := make([]Evidence, 0, len(citations))
	for _, citation := range citations {
		resolved, err := s.resolver.Resolve(ctx, citation)
		if err != nil {
			return Draft{}, err
		}
		evidence = append(evidence, resolved)
	}
	request, err := NewGenerationRequest(action.objective, evidence, action.maxBytes)
	if err != nil {
		return Draft{}, err
	}
	draft, err := s.pipeline.Generate(ctx, request)
	if err != nil {
		return Draft{}, err
	}
	if err := validateGeneratedDraft(action, draft); err != nil {
		return Draft{}, err
	}
	return draft, nil
}

func (s *Service) Finalize(ctx context.Context, draft Draft) (Prepared, error) {
	if err := draft.Validate(); err != nil {
		return Prepared{}, err
	}
	content := draft.AsContent()
	if err := s.validateEvidence(ctx, content); err != nil {
		return Prepared{}, err
	}
	current, found, err := s.backend.Latest(ctx, s.artifactID)
	if err != nil {
		return Prepared{}, err
	}
	var base *artifact.Ref
	if found {
		ref := current.Ref()
		base = &ref
	}
	return NewPrepared(s.scopeID, base, content)
}

func (s *Service) Commit(ctx context.Context, prepared Prepared) (Handoff, error) {
	if err := prepared.Validate(); err != nil {
		return Handoff{}, err
	}
	if err := s.requirePrepared(prepared); err != nil {
		return Handoff{}, err
	}
	current, found, err := s.backend.Latest(ctx, s.artifactID)
	if err != nil {
		return Handoff{}, err
	}
	if found && current.Content().Equal(prepared.content) {
		return current, nil
	}
	if !sameBase(prepared.base, current, found) {
		requested := artifact.Ref{}
		if prepared.base != nil {
			requested = *prepared.base
		}
		currentRef := artifact.Ref{}
		if found {
			currentRef = current.Ref()
		}
		return Handoff{}, &artifact.RevisionConflictError{Requested: requested, Current: currentRef}
	}
	if validationErr := s.validateEvidence(ctx, prepared.content); validationErr != nil {
		return Handoff{}, validationErr
	}
	draft, err := NewArtifactDraft(prepared.content, sourceLineage(prepared.content), artifactLineage(prepared.content))
	if err != nil {
		return Handoff{}, err
	}
	if !found {
		return s.backend.Create(ctx, s.artifactID, draft)
	}
	return s.backend.Revise(ctx, current, draft)
}

func (s *Service) Latest(ctx context.Context) (Handoff, bool, error) {
	return s.backend.Latest(ctx, s.artifactID)
}

func (s *Service) Revision(ctx context.Context, ref artifact.Ref) (Handoff, error) {
	if err := s.requireReference(ref); err != nil {
		return Handoff{}, err
	}
	return s.backend.Get(ctx, ref)
}

func (s *Service) Revisions(ctx context.Context) ([]Handoff, error) {
	return s.backend.Revisions(ctx, s.artifactID)
}

// ValidateEvidence verifies exact same-scope evidence for a higher-level Work
// record without manufacturing a Handoff Content value. It intentionally has
// no batch-size policy; the owning Work schema supplies those bounds.
func (s *Service) ValidateEvidence(ctx context.Context, citations []Citation) error {
	for _, citation := range citations {
		if err := validateCitation(citation); err != nil {
			return err
		}
		if err := s.resolver.Validate(ctx, citation); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ContinueFromPrepared(ctx context.Context, prepared Prepared) (Resolution, error) {
	if err := prepared.Validate(); err != nil {
		return Resolution{}, err
	}
	if err := s.requirePrepared(prepared); err != nil {
		return Resolution{}, err
	}
	current, found, err := s.backend.Latest(ctx, s.artifactID)
	if err != nil {
		return Resolution{}, err
	}
	return s.resolve(ctx, prepared.content, PreparedSelection, nil, current, found)
}

func (s *Service) ContinueFromRevision(ctx context.Context, ref artifact.Ref) (Resolution, error) {
	selected, err := s.Revision(ctx, ref)
	if err != nil {
		return Resolution{}, err
	}
	current, found, err := s.backend.Latest(ctx, s.artifactID)
	if err != nil {
		return Resolution{}, err
	}
	selectedRef := selected.Ref()
	return s.resolve(ctx, selected.Content(), ExactSelection, &selectedRef, current, found)
}

func (s *Service) ContinueLatest(ctx context.Context) (Resolution, error) {
	current, found, err := s.backend.Latest(ctx, s.artifactID)
	if err != nil {
		return Resolution{}, err
	}
	if !found {
		return Empty(s.scopeID), nil
	}
	ref := current.Ref()
	return s.resolve(ctx, current.Content(), LatestSelection, &ref, current, true)
}

func (s *Service) resolve(
	ctx context.Context,
	content Content,
	selection Selection,
	selectedRef *artifact.Ref,
	current Handoff,
	found bool,
) (Resolution, error) {
	checks, err := s.evidenceChecks(ctx, content)
	if err != nil {
		return Resolution{}, err
	}
	var currentRef *artifact.Ref
	if found {
		ref := current.Ref()
		currentRef = &ref
	}
	return NewResolved(s.scopeID, content, selection, selectedRef, currentRef, checks)
}

func (s *Service) requirePrepared(prepared Prepared) error {
	if prepared.scopeID != s.scopeID {
		return &ScopeMismatchError{Expected: s.scopeID, Actual: prepared.scopeID}
	}
	if prepared.base != nil {
		return s.requireReference(*prepared.base)
	}
	return nil
}

func (s *Service) requireReference(ref artifact.Ref) error {
	if err := ref.Validate(); err != nil || ref.Family() != Family || ref.ID() != s.artifactID {
		return &InvalidReferenceError{}
	}
	return nil
}

func (s *Service) validateEvidence(ctx context.Context, content Content) error {
	for _, citation := range directCitations(content) {
		if err := s.resolver.Validate(ctx, citation); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) evidenceChecks(ctx context.Context, content Content) ([]EvidenceCheck, error) {
	checks := make([]EvidenceCheck, 0, len(content.state)+1)
	for index, statement := range content.state {
		stateIndex := index
		check, err := s.checkEvidence(ctx, statement.citations, StateClaim, &stateIndex)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	if content.nextAction != nil {
		check, err := s.checkEvidence(ctx, content.nextAction.citations, NextActionClaim, nil)
		if err != nil {
			return nil, err
		}
		checks = append(checks, check)
	}
	return checks, nil
}

func (s *Service) checkEvidence(ctx context.Context, citations []Citation, claim Claim, stateIndex *int) (EvidenceCheck, error) {
	var unavailable []Citation
	seen := make(map[string]struct{})
	for _, citation := range citations {
		err := s.resolver.Validate(ctx, citation)
		if err == nil {
			continue
		}
		var unavailableError *EvidenceUnavailableError
		if !errors.As(err, &unavailableError) {
			return EvidenceCheck{}, err
		}
		if _, exists := seen[citation.citationKey()]; !exists {
			seen[citation.citationKey()] = struct{}{}
			unavailable = append(unavailable, citation)
		}
	}
	status := EvidenceAvailable
	if len(unavailable) > 0 {
		status = EvidenceUnavailable
	}
	return NewEvidenceCheck(claim, stateIndex, status, unavailable)
}

func validateGeneratedDraft(action Prepare, draft Draft) error {
	content := draft.AsContent()
	if content.objective != action.objective {
		return &InvalidGenerationError{Code: "objective"}
	}
	allowed := make(map[string]struct{}, len(action.evidence))
	for _, citation := range action.evidence {
		allowed[citation.citationKey()] = struct{}{}
	}
	for _, citation := range allCitations(content) {
		if _, exists := allowed[citation.citationKey()]; !exists {
			return &InvalidGenerationError{Code: "evidence"}
		}
	}
	rendered, err := RenderContent(content)
	if err != nil {
		return err
	}
	if len(rendered) > action.maxBytes {
		return &InvalidGenerationError{Code: "budget"}
	}
	return nil
}

func sameBase(base *artifact.Ref, current Handoff, found bool) bool {
	if !found {
		return base == nil
	}
	return base != nil && *base == current.Ref()
}

func directCitations(content Content) []Citation {
	var result []Citation
	for _, statement := range content.state {
		result = append(result, statement.citations...)
	}
	if content.nextAction != nil {
		result = append(result, content.nextAction.citations...)
	}
	return result
}

func allCitations(content Content) []Citation {
	result := directCitations(content)
	for _, omission := range content.omissions {
		if omission.citation != nil {
			result = append(result, omission.citation)
		}
	}
	return result
}

func sourceLineage(content Content) []source.Ref {
	var result []source.Ref
	seen := make(map[source.Ref]struct{})
	for _, citation := range directCitations(content) {
		value, ok := citation.(SourceCitation)
		if !ok {
			continue
		}
		if _, exists := seen[value.ref]; !exists {
			seen[value.ref] = struct{}{}
			result = append(result, value.ref)
		}
	}
	return result
}

func artifactLineage(content Content) []artifact.Ref {
	var result []artifact.Ref
	seen := make(map[artifact.Ref]struct{})
	for _, citation := range directCitations(content) {
		var ref artifact.Ref
		switch value := citation.(type) {
		case ArtifactCitation:
			ref = value.ref
		case MemoryCitation:
			ref = value.citation.MemoryRef
		default:
			continue
		}
		if _, exists := seen[ref]; !exists {
			seen[ref] = struct{}{}
			result = append(result, ref)
		}
	}
	return result
}
