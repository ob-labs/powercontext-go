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

package endpoint

import (
	"context"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/source"
)

type HandoffOperations interface {
	Activate(context.Context, string, handoff.Activate) (runtime.HandoffActivationResult, error)
	Prepare(context.Context, string, handoff.Prepare) (handoff.Draft, error)
	Finalize(context.Context, string, handoff.Draft) (handoff.Prepared, error)
	Commit(context.Context, string, handoff.Prepared) (handoff.Handoff, error)
	ContinuePrepared(context.Context, string, handoff.Prepared) (handoff.Resolution, error)
	ContinueRevision(context.Context, string, artifact.Ref) (handoff.Resolution, error)
	ContinueLatest(context.Context, string) (handoff.Resolution, error)
}

func (h *Handler) ActivateHandoff(
	ctx context.Context,
	req *v1.ActivateHandoffRequest,
) (v1.ActivateHandoffRes, error) {
	if h.handoff == nil {
		return nil, &RuntimeNotReadyError{}
	}
	boundary, err := source.NewRef(req.BoundarySource.Name, req.BoundarySource.SourceID)
	if err != nil {
		return nil, invalidHandoffRequest("boundary_source")
	}
	evidence, err := runtimeHandoffCitations(req.Evidence)
	if err != nil {
		return nil, err
	}
	activation, err := handoff.NewActivate(
		boundary, req.Objective, evidence, req.MaxBytes.Or(handoff.DefaultMaxBytes),
	)
	if err != nil {
		return nil, invalidHandoffRequest("handoff")
	}
	result, err := h.handoff.Activate(ctx, req.ScopeID, activation)
	if err != nil {
		return nil, err
	}
	response := v1.HandoffActivation{
		Status: v1.HandoffActivationStatus(result.Status()), BoundarySource: sourceReference(result.BoundarySource()),
		PreviousPosition: int(result.PreviousPosition()), CurrentPosition: int(result.CurrentPosition()),
	}
	if draft := result.Draft(); draft != nil {
		mapped, mapErr := wireHandoffDraft(*draft)
		err = mapErr
		if err != nil {
			return nil, err
		}
		response.Draft = v1.NewNilHandoffDraft(mapped)
	} else {
		response.Draft.SetToNull()
	}
	return &v1.HandoffActivationHeaders{XPowerContextRequestID: requestID(ctx), Response: response}, nil
}

func (h *Handler) PrepareHandoff(
	ctx context.Context,
	req *v1.PrepareHandoffRequest,
) (v1.PrepareHandoffRes, error) {
	if h.handoff == nil {
		return nil, &RuntimeNotReadyError{}
	}
	evidence, err := runtimeHandoffCitations(req.Evidence)
	if err != nil {
		return nil, err
	}
	action, err := handoff.NewPrepare(req.Objective, evidence, req.MaxBytes.Or(handoff.DefaultMaxBytes))
	if err != nil {
		return nil, invalidHandoffRequest("handoff")
	}
	draft, err := h.handoff.Prepare(ctx, req.ScopeID, action)
	if err != nil {
		return nil, err
	}
	response, err := wireHandoffDraft(draft)
	if err != nil {
		return nil, err
	}
	return &v1.HandoffDraftHeaders{XPowerContextRequestID: requestID(ctx), Response: response}, nil
}

func (h *Handler) FinalizeHandoff(
	ctx context.Context,
	req *v1.FinalizeHandoffRequest,
) (v1.FinalizeHandoffRes, error) {
	if h.handoff == nil {
		return nil, &RuntimeNotReadyError{}
	}
	draft, err := runtimeHandoffDraft(req.Draft)
	if err != nil {
		return nil, err
	}
	prepared, err := h.handoff.Finalize(ctx, req.ScopeID, draft)
	if err != nil {
		return nil, err
	}
	response, err := wirePreparedHandoff(prepared)
	if err != nil {
		return nil, err
	}
	return &v1.PreparedHandoffHeaders{XPowerContextRequestID: requestID(ctx), Response: response}, nil
}

func (h *Handler) CommitHandoff(
	ctx context.Context,
	req *v1.CommitHandoffRequest,
) (v1.CommitHandoffRes, error) {
	if h.handoff == nil {
		return nil, &RuntimeNotReadyError{}
	}
	prepared, err := runtimePreparedHandoff(req.Handoff)
	if err != nil {
		return nil, err
	}
	committed, err := h.handoff.Commit(ctx, req.ScopeID, prepared)
	if err != nil {
		return nil, err
	}
	content, err := wireHandoffContent(committed.Content())
	if err != nil {
		return nil, err
	}
	lineage := committed.Lineage()
	return &v1.CommittedHandoffHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response: v1.CommittedHandoff{
			Reference: artifactReference(committed.Ref()), Content: content,
			SourceRefs:   wireSourceReferences(lineage.Sources()),
			ArtifactRefs: wireArtifactReferences(lineage.Artifacts()),
		},
	}, nil
}

func (h *Handler) ContinueHandoff(
	ctx context.Context,
	req *v1.ContinueHandoffRequest,
) (v1.ContinueHandoffRes, error) {
	if h.handoff == nil {
		return nil, &RuntimeNotReadyError{}
	}
	preparedValue, hasPrepared := req.Prepared.Get()
	revisionValue, hasRevision := req.Revision.Get()
	var resolution handoff.Resolution
	var err error
	switch req.Selection {
	case v1.HandoffSelectionLatest:
		if hasPrepared || hasRevision {
			return nil, invalidHandoffRequest("handoff-selection")
		}
		resolution, err = h.handoff.ContinueLatest(ctx, req.ScopeID)
	case v1.HandoffSelectionPrepared:
		if !hasPrepared || hasRevision {
			return nil, invalidHandoffRequest("handoff-selection")
		}
		var prepared handoff.Prepared
		prepared, err = runtimePreparedHandoff(preparedValue)
		if err == nil {
			resolution, err = h.handoff.ContinuePrepared(ctx, req.ScopeID, prepared)
		}
	case v1.HandoffSelectionExact:
		if hasPrepared || !hasRevision {
			return nil, invalidHandoffRequest("handoff-selection")
		}
		var ref artifact.Ref
		ref, err = runtimeArtifactReference(revisionValue)
		if err == nil {
			resolution, err = h.handoff.ContinueRevision(ctx, req.ScopeID, ref)
		}
	default:
		return nil, invalidHandoffRequest("handoff-selection")
	}
	if err != nil {
		return nil, err
	}
	response, err := wireHandoffResolution(resolution)
	if err != nil {
		return nil, err
	}
	return &v1.HandoffResolutionHeaders{XPowerContextRequestID: requestID(ctx), Response: response}, nil
}

func runtimeHandoffCitations(values []v1.HandoffCitation) ([]handoff.Citation, error) {
	result := make([]handoff.Citation, len(values))
	for index, value := range values {
		citation, err := runtimeHandoffCitation(value)
		if err != nil {
			return nil, err
		}
		result[index] = citation
	}
	return result, nil
}

func runtimeHandoffCitation(value v1.HandoffCitation) (handoff.Citation, error) {
	if citation, ok := value.GetHandoffSourceCitation(); ok {
		ref, err := source.NewRef(citation.SourceRef.Name, citation.SourceRef.SourceID)
		if err != nil {
			return nil, invalidHandoffRequest("citation")
		}
		result, err := handoff.NewSourceCitation(ref)
		if err != nil {
			return nil, invalidHandoffRequest("citation")
		}
		return result, nil
	}
	if citation, ok := value.GetHandoffArtifactCitation(); ok {
		ref, err := runtimeArtifactReference(citation.ArtifactRef)
		if err != nil {
			return nil, invalidHandoffRequest("citation")
		}
		result, err := handoff.NewArtifactCitation(ref)
		if err != nil {
			return nil, invalidHandoffRequest("citation")
		}
		return result, nil
	}
	if citation, ok := value.GetHandoffMemoryCitation(); ok {
		memoryValue, err := runtimeCitation(citation.MemoryCitation)
		if err != nil {
			return nil, invalidHandoffRequest("citation")
		}
		result, err := handoff.NewMemoryCitation(memoryValue)
		if err != nil {
			return nil, invalidHandoffRequest("citation")
		}
		return result, nil
	}
	return nil, invalidHandoffRequest("citation")
}

func runtimeHandoffDraft(value v1.HandoffDraft) (handoff.Draft, error) {
	state, err := runtimeHandoffStatements(value.State)
	if err != nil {
		return handoff.Draft{}, err
	}
	next, err := runtimeOptionalHandoffStatement(value.NextAction)
	if err != nil {
		return handoff.Draft{}, err
	}
	omissions, err := runtimeHandoffOmissions(value.Omissions)
	if err != nil {
		return handoff.Draft{}, err
	}
	result, err := handoff.NewDraft(
		value.Objective, state, handoff.Disposition(value.Disposition), next, omissions,
	)
	if err != nil {
		return handoff.Draft{}, invalidHandoffRequest("draft")
	}
	return result, nil
}

func runtimePreparedHandoff(value v1.PreparedHandoff) (handoff.Prepared, error) {
	if err := handoff.ValidatePreparedSchema(string(value.Schema)); err != nil {
		return handoff.Prepared{}, invalidHandoffRequest("handoff.schema")
	}
	content, err := runtimeHandoffContent(value.Content)
	if err != nil {
		return handoff.Prepared{}, err
	}
	var base *artifact.Ref
	if baseValue, ok := value.Base.Get(); ok {
		ref, referenceErr := runtimeArtifactReference(baseValue)
		if referenceErr != nil {
			return handoff.Prepared{}, invalidHandoffRequest("handoff.base")
		}
		base = &ref
	}
	result, err := handoff.NewPrepared(value.ScopeID, base, content)
	if err != nil {
		return handoff.Prepared{}, invalidHandoffRequest("handoff")
	}
	return result, nil
}

func runtimeHandoffContent(value v1.HandoffContent) (handoff.Content, error) {
	if err := handoff.ValidateContentSchema(string(value.Schema)); err != nil {
		return handoff.Content{}, invalidHandoffRequest("content.schema")
	}
	state, err := runtimeHandoffStatements(value.State)
	if err != nil {
		return handoff.Content{}, err
	}
	next, err := runtimeOptionalHandoffStatement(value.NextAction)
	if err != nil {
		return handoff.Content{}, err
	}
	omissions, err := runtimeHandoffOmissions(value.Omissions)
	if err != nil {
		return handoff.Content{}, err
	}
	result, err := handoff.NewContent(
		value.Objective, state, handoff.Disposition(value.Disposition), next, omissions,
	)
	if err != nil {
		return handoff.Content{}, invalidHandoffRequest("content")
	}
	return result, nil
}

func runtimeHandoffStatements(values []v1.HandoffStatement) ([]handoff.Statement, error) {
	result := make([]handoff.Statement, len(values))
	for index, value := range values {
		statement, err := runtimeHandoffStatement(value)
		if err != nil {
			return nil, err
		}
		result[index] = statement
	}
	return result, nil
}

func runtimeHandoffStatement(value v1.HandoffStatement) (handoff.Statement, error) {
	citations, err := runtimeHandoffCitations(value.Citations)
	if err != nil {
		return handoff.Statement{}, err
	}
	result, err := handoff.NewStatement(value.Text, citations)
	if err != nil {
		return handoff.Statement{}, invalidHandoffRequest("statement")
	}
	return result, nil
}

func runtimeOptionalHandoffStatement(value v1.NilHandoffStatement) (*handoff.Statement, error) {
	statement, ok := value.Get()
	if !ok {
		return nil, nil
	}
	result, err := runtimeHandoffStatement(statement)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func runtimeHandoffOmissions(values []v1.HandoffOmission) ([]handoff.Omission, error) {
	result := make([]handoff.Omission, len(values))
	for index, value := range values {
		var citation handoff.Citation
		var err error
		if citationValue, ok := value.Citation.Get(); ok {
			citation, err = runtimeHandoffCitation(citationValue)
			if err != nil {
				return nil, err
			}
		}
		result[index], err = handoff.NewOmission(value.Text, citation)
		if err != nil {
			return nil, invalidHandoffRequest("omission")
		}
	}
	return result, nil
}

func wireHandoffDraft(value handoff.Draft) (v1.HandoffDraft, error) {
	content := value.AsContent()
	state, err := wireHandoffStatements(content.State())
	if err != nil {
		return v1.HandoffDraft{}, err
	}
	next, err := wireOptionalHandoffStatement(content.NextAction())
	if err != nil {
		return v1.HandoffDraft{}, err
	}
	omissions, err := wireHandoffOmissions(content.Omissions())
	if err != nil {
		return v1.HandoffDraft{}, err
	}
	return v1.HandoffDraft{
		Objective: content.Objective(), State: state,
		Disposition: v1.HandoffDisposition(content.Disposition()), NextAction: next, Omissions: omissions,
	}, nil
}

func wirePreparedHandoff(value handoff.Prepared) (v1.PreparedHandoff, error) {
	content, err := wireHandoffContent(value.Content())
	if err != nil {
		return v1.PreparedHandoff{}, err
	}
	result := v1.PreparedHandoff{
		Schema:  v1.PreparedHandoffSchemaPowercontextPreparedHandoffV1,
		ScopeID: value.ScopeID(), Content: content,
	}
	if base := value.Base(); base != nil {
		result.Base = v1.NewNilArtifactReference(artifactReference(*base))
	} else {
		result.Base.SetToNull()
	}
	return result, nil
}

func wireHandoffContent(value handoff.Content) (v1.HandoffContent, error) {
	state, err := wireHandoffStatements(value.State())
	if err != nil {
		return v1.HandoffContent{}, err
	}
	next, err := wireOptionalHandoffStatement(value.NextAction())
	if err != nil {
		return v1.HandoffContent{}, err
	}
	omissions, err := wireHandoffOmissions(value.Omissions())
	if err != nil {
		return v1.HandoffContent{}, err
	}
	return v1.HandoffContent{
		Schema: v1.HandoffSchemaPowercontextHandoffV1, Objective: value.Objective(), State: state,
		Disposition: v1.HandoffDisposition(value.Disposition()), NextAction: next, Omissions: omissions,
	}, nil
}

func wireHandoffStatements(values []handoff.Statement) ([]v1.HandoffStatement, error) {
	result := make([]v1.HandoffStatement, len(values))
	for index, value := range values {
		citations, err := wireHandoffCitations(value.Citations())
		if err != nil {
			return nil, err
		}
		result[index] = v1.HandoffStatement{Text: value.Text(), Citations: citations}
	}
	return result, nil
}

func wireOptionalHandoffStatement(value *handoff.Statement) (v1.NilHandoffStatement, error) {
	if value == nil {
		result := v1.NilHandoffStatement{}
		result.SetToNull()
		return result, nil
	}
	values, err := wireHandoffStatements([]handoff.Statement{*value})
	if err != nil {
		return v1.NilHandoffStatement{}, err
	}
	return v1.NewNilHandoffStatement(values[0]), nil
}

func wireHandoffCitations(values []handoff.Citation) ([]v1.HandoffCitation, error) {
	result := make([]v1.HandoffCitation, len(values))
	for index, value := range values {
		citation, err := wireHandoffCitation(value)
		if err != nil {
			return nil, err
		}
		result[index] = citation
	}
	return result, nil
}

func wireHandoffCitation(value handoff.Citation) (v1.HandoffCitation, error) {
	switch citation := value.(type) {
	case handoff.SourceCitation:
		return v1.NewHandoffSourceCitationHandoffCitation(v1.HandoffSourceCitation{
			Kind: v1.HandoffSourceCitationKindSource, SourceRef: sourceReference(citation.Ref()),
		}), nil
	case handoff.ArtifactCitation:
		return v1.NewHandoffArtifactCitationHandoffCitation(v1.HandoffArtifactCitation{
			Kind: v1.HandoffArtifactCitationKindArtifact, ArtifactRef: artifactReference(citation.Ref()),
		}), nil
	case handoff.MemoryCitation:
		return v1.NewHandoffMemoryCitationHandoffCitation(v1.HandoffMemoryCitation{
			Kind: v1.HandoffMemoryCitationKindMemory, MemoryCitation: memoryCitation(citation.Citation()),
		}), nil
	default:
		return v1.HandoffCitation{}, invalidHandoffRequest("citation")
	}
}

func wireHandoffOmissions(values []handoff.Omission) ([]v1.HandoffOmission, error) {
	result := make([]v1.HandoffOmission, len(values))
	for index, value := range values {
		result[index].Text = value.Text()
		if citation := value.Citation(); citation != nil {
			mapped, err := wireHandoffCitation(citation)
			if err != nil {
				return nil, err
			}
			result[index].Citation = v1.NewNilHandoffCitation(mapped)
		} else {
			result[index].Citation.SetToNull()
		}
	}
	return result, nil
}

func wireHandoffResolution(value handoff.Resolution) (v1.HandoffResolution, error) {
	if err := value.Validate(); err != nil {
		return v1.HandoffResolution{}, err
	}
	checks := value.EvidenceChecks()
	result := v1.HandoffResolution{
		Trust:  v1.HandoffResolutionTrustUntrustedHistory,
		Status: v1.HandoffResolutionStatus(value.Status()), ScopeID: value.ScopeID(),
		EvidenceChecks: make([]v1.HandoffEvidenceCheck, len(checks)),
	}
	if resolvedContent := value.Content(); resolvedContent != nil {
		content, err := wireHandoffContent(*resolvedContent)
		if err != nil {
			return v1.HandoffResolution{}, err
		}
		result.Content = v1.NewNilHandoffContent(content)
	} else {
		result.Content.SetToNull()
	}
	if selection := value.Selection(); selection != nil {
		result.Selection = v1.NewNilHandoffSelection(v1.HandoffSelection(*selection))
	} else {
		result.Selection.SetToNull()
	}
	if selected := value.SelectedRevision(); selected != nil {
		result.SelectedRevision = v1.NewNilArtifactReference(artifactReference(*selected))
	} else {
		result.SelectedRevision.SetToNull()
	}
	if current := value.CurrentRevision(); current != nil {
		result.CurrentRevision = v1.NewNilArtifactReference(artifactReference(*current))
	} else {
		result.CurrentRevision.SetToNull()
	}
	for index, check := range checks {
		citations, err := wireHandoffCitations(check.UnavailableEvidence())
		if err != nil {
			return v1.HandoffResolution{}, err
		}
		stateIndex := v1.NilInt{}
		stateIndexValue := check.StateIndex()
		if stateIndexValue == nil {
			stateIndex.SetToNull()
		} else {
			stateIndex = v1.NewNilInt(*stateIndexValue)
		}
		result.EvidenceChecks[index] = v1.HandoffEvidenceCheck{
			Claim: v1.HandoffClaim(check.Claim()), StateIndex: stateIndex,
			Status: v1.HandoffEvidenceStatus(check.Status()), UnavailableEvidence: citations,
		}
	}
	return result, nil
}

func invalidHandoffRequest(field string) error { return &InvalidRequestError{Field: field} }

var _ HandoffOperations = (*runtime.HandoffApplication)(nil)
