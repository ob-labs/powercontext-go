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

package runtime

import (
	"context"
	"errors"
	"slices"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/internal/work"
	"github.com/ob-labs/powercontext-go/source"
)

type WorkSourceBackend interface {
	Capture(context.Context, string, source.ContentCapture) (source.Ref, int64, error)
	Entries(context.Context, string) ([]source.JournalEntry, error)
}

// WorkApplication orchestrates the delegation, Handoff, acknowledgement, and
// outcome loop over the existing Source journal and Handoff authority.
type WorkApplication struct {
	runtime  *Runtime
	sources  WorkSourceBackend
	handoffs HandoffServiceFactory
}

func NewWorkApplication(runtime *Runtime, sources WorkSourceBackend, handoffs HandoffServiceFactory) (*WorkApplication, error) {
	if runtime == nil || sources == nil || handoffs == nil {
		return nil, errors.New("runtime: Work application dependencies must not be nil")
	}
	return &WorkApplication{runtime: runtime, sources: sources, handoffs: handoffs}, nil
}

func (a *WorkApplication) CreateContract(ctx context.Context, scopeID string, request work.CreateContract) (work.SourceReceipt, error) {
	var result work.SourceReceipt
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		if err := request.Contract.Validate(); err != nil {
			return err
		}
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		if validationErr := service.ValidateEvidence(ctx, claimsEvidence(request.Contract.Facts())); validationErr != nil {
			return validationErr
		}
		result, err = a.capture(ctx, scope, work.WorkContractSourceKind, request.SourceID, request.Contract)
		return err
	})
	return result, err
}

func (a *WorkApplication) HandoffCurrent(ctx context.Context, scopeID string, request work.HandoffCurrent) (work.PreparedHandoff, error) {
	var result work.PreparedHandoff
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		if err := request.Handoff.Validate(); err != nil {
			return err
		}
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		claims := request.Handoff.State()
		if next := request.Handoff.NextAction(); next != nil {
			claims = append(claims, *next)
		}
		if validationErr := service.ValidateEvidence(ctx, claimsEvidence(claims)); validationErr != nil {
			return validationErr
		}
		boundary, err := a.capture(ctx, scope, work.HandoffBoundarySourceKind, request.SourceID, request.Handoff)
		if err != nil {
			return err
		}
		boundaryCitation, err := handoff.NewSourceCitation(boundary.SourceRef)
		if err != nil {
			return err
		}
		state := request.Handoff.State()
		statements := make([]handoff.Statement, len(state))
		for index, claim := range state {
			statements[index], err = statementFromClaim(claim, boundaryCitation)
			if err != nil {
				return err
			}
		}
		var next *handoff.Statement
		if claim := request.Handoff.NextAction(); claim != nil {
			statement, statementErr := statementFromClaim(*claim, boundaryCitation)
			if statementErr != nil {
				return statementErr
			}
			next = &statement
		}
		omissionTexts := request.Handoff.Omissions()
		omissions := make([]handoff.Omission, len(omissionTexts))
		for index, text := range omissionTexts {
			omissions[index], err = handoff.NewOmission(text, nil)
			if err != nil {
				return err
			}
		}
		draft, err := handoff.NewDraft(
			request.Handoff.Objective(), statements, request.Handoff.Disposition(), next, omissions,
		)
		if err != nil {
			return err
		}
		prepared, err := service.Finalize(ctx, draft)
		if err != nil {
			return err
		}
		result = work.PreparedHandoff{Boundary: boundary, Handoff: prepared}
		return nil
	})
	return result, err
}

func (a *WorkApplication) Acknowledge(ctx context.Context, scopeID string, request work.Acknowledge) (work.Acknowledgement, error) {
	var result work.Acknowledgement
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		if err := request.Validate(); err != nil {
			return err
		}
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		var resolution handoff.Resolution
		if request.Selection() == handoff.PreparedSelection {
			resolution, err = service.ContinueFromPrepared(ctx, *request.Prepared())
		} else {
			resolution, err = service.ContinueFromRevision(ctx, *request.Revision())
		}
		if err != nil {
			return err
		}
		if resolution.Status() == handoff.EmptyResolution {
			return &work.InvalidRequestError{Code: "handoff-empty"}
		}
		unavailable, err := unavailableEvidence(resolution)
		if err != nil {
			return err
		}
		if request.Status() == work.ReceiptAccepted && len(unavailable) != 0 {
			return &work.InvalidRequestError{Code: "handoff-evidence-unavailable"}
		}
		var preparedDigest *string
		if prepared := request.Prepared(); prepared != nil {
			digest, digestErr := work.PreparedDigest(*prepared)
			if digestErr != nil {
				return digestErr
			}
			preparedDigest = &digest
		}
		receiptRecord, err := work.NewHandoffReceipt(
			request.Receiver(), request.Status(), request.Selection(), resolution.SelectedRevision(),
			preparedDigest, request.ReceiverChecks(), evidenceStatus(unavailable), unavailable, request.Message(),
		)
		if err != nil {
			return err
		}
		receipt, err := a.capture(ctx, scope, work.HandoffReceiptSourceKind, request.SourceID(), receiptRecord)
		if err != nil {
			return err
		}
		result = work.Acknowledgement{Resolution: resolution, Receipt: receipt}
		return nil
	})
	return result, err
}

func (a *WorkApplication) RecordOutcome(ctx context.Context, scopeID string, request work.RecordOutcome) (work.SourceReceipt, error) {
	var result work.SourceReceipt
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		if err := request.Outcome.Validate(); err != nil {
			return err
		}
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		citations, err := outcomeEvidence(request.Outcome)
		if err != nil {
			return err
		}
		if validationErr := service.ValidateEvidence(ctx, citations); validationErr != nil {
			return validationErr
		}
		if receipt := request.Outcome.HandoffReceiptRef(); receipt != nil {
			if receiptErr := a.validateOutcomeReceipt(ctx, scope, *receipt); receiptErr != nil {
				return receiptErr
			}
		}
		result, err = a.capture(ctx, scope, work.TaskOutcomeSourceKind, request.SourceID, request.Outcome)
		return err
	})
	return result, err
}

func (a *WorkApplication) Continuity(ctx context.Context, scopeID string, selected *artifact.Ref) (work.Continuity, error) {
	var result work.Continuity
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		entries, err := a.sources.Entries(ctx, scope)
		if err != nil {
			return err
		}
		if selected == nil {
			service, serviceErr := a.service(scope)
			if serviceErr != nil {
				return serviceErr
			}
			latest, found, latestErr := service.Latest(ctx)
			if latestErr != nil {
				return latestErr
			}
			if found {
				ref := latest.Ref()
				selected = &ref
			}
		}
		result, err = work.ProjectContinuity(scope, entries, selected)
		return err
	})
	return result, err
}

func (a *WorkApplication) capture(ctx context.Context, scope string, kind work.Kind, sourceID string, record any) (work.SourceReceipt, error) {
	payload, err := work.EncodeRecord(record, true)
	if err != nil {
		return work.SourceReceipt{}, err
	}
	digest, err := work.ContentDigest(record)
	if err != nil {
		return work.SourceReceipt{}, err
	}
	var schema string
	switch value := record.(type) {
	case work.Contract:
		schema = value.Schema()
	case work.CurrentHandoff:
		schema = value.Schema()
	case work.HandoffReceipt:
		schema = value.Schema()
	case work.TaskOutcome:
		schema = value.Schema()
	default:
		return work.SourceReceipt{}, &work.InvalidError{Field: "record", Detail: "unsupported capture type"}
	}
	capture, err := source.NewContentCapture(sourceID, string(payload), map[string]any{"kind": string(kind), "schema": schema})
	if err != nil {
		return work.SourceReceipt{}, err
	}
	ref, position, err := a.sources.Capture(ctx, scope, capture)
	if err != nil {
		return work.SourceReceipt{}, err
	}
	return work.SourceReceipt{Kind: kind, SourceRef: ref, Position: position, ContentDigest: digest}, nil
}

func (a *WorkApplication) validateOutcomeReceipt(ctx context.Context, scope string, receiptRef source.Ref) error {
	entries, err := a.sources.Entries(ctx, scope)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Ref() != receiptRef {
			continue
		}
		content, ok := entry.Value().(source.ContentSource)
		if !ok || content.Metadata()["kind"] != string(work.HandoffReceiptSourceKind) {
			break
		}
		record, decodeErr := work.DecodeRecord(work.HandoffReceiptSourceKind, []byte(content.Content()))
		if decodeErr != nil {
			break
		}
		receipt, ok := record.(work.HandoffReceipt)
		if ok && receipt.Status() == work.ReceiptAccepted && receipt.Selection() == handoff.ExactSelection && receipt.SelectedRevision() != nil {
			return nil
		}
		break
	}
	return &work.InvalidRequestError{Code: "task-outcome-handoff-receipt"}
}

func (a *WorkApplication) service(scope string) (*handoff.Service, error) {
	service, err := a.handoffs(scope)
	if err != nil {
		return nil, err
	}
	if service == nil {
		return nil, &StateError{Code: "handoff"}
	}
	return service, nil
}

func claimsEvidence(claims []work.Claim) []handoff.Citation {
	var citations []handoff.Citation
	for _, claim := range claims {
		citations = append(citations, claim.Evidence()...)
	}
	return uniqueCitations(citations)
}

func outcomeEvidence(outcome work.TaskOutcome) ([]handoff.Citation, error) {
	citations := claimsEvidence(outcome.Observations())
	for _, check := range outcome.Checks() {
		citations = append(citations, check.Evidence()...)
	}
	for _, ref := range outcome.ProducedArtifacts() {
		citation, err := handoff.NewArtifactCitation(ref)
		if err != nil {
			return nil, err
		}
		citations = append(citations, citation)
	}
	return uniqueCitations(citations), nil
}

func statementFromClaim(claim work.Claim, boundary handoff.SourceCitation) (handoff.Statement, error) {
	citations := append([]handoff.Citation{boundary}, claim.Evidence()...)
	return handoff.NewStatement(claim.Text(), uniqueCitations(citations))
}

func unavailableEvidence(resolution handoff.Resolution) ([]handoff.Citation, error) {
	var result []handoff.Citation
	for _, check := range resolution.EvidenceChecks() {
		result = append(result, check.UnavailableEvidence()...)
	}
	return uniqueCitations(result), nil
}

func uniqueCitations(values []handoff.Citation) []handoff.Citation {
	result := make([]handoff.Citation, 0, len(values))
	for _, candidate := range values {
		if !slices.ContainsFunc(result, func(current handoff.Citation) bool {
			return citationsEqual(current, candidate)
		}) {
			result = append(result, candidate)
		}
	}
	return result
}

func citationsEqual(left, right handoff.Citation) bool {
	if left == nil || right == nil || left.Kind() != right.Kind() {
		return false
	}
	switch leftValue := left.(type) {
	case handoff.SourceCitation:
		rightValue, ok := right.(handoff.SourceCitation)
		return ok && leftValue.Ref() == rightValue.Ref()
	case handoff.ArtifactCitation:
		rightValue, ok := right.(handoff.ArtifactCitation)
		return ok && leftValue.Ref() == rightValue.Ref()
	case handoff.MemoryCitation:
		rightValue, ok := right.(handoff.MemoryCitation)
		return ok && leftValue.Citation() == rightValue.Citation()
	default:
		return false
	}
}

func evidenceStatus(values []handoff.Citation) work.EvidenceStatus {
	if len(values) == 0 {
		return work.EvidenceAvailable
	}
	return work.EvidenceUnavailable
}
