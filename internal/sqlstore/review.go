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

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/source"
)

// ReviewBackend keeps Candidate CAS, Artifact CAS, projection replacement,
// and terminal head update inside one database transaction.
type ReviewBackend struct {
	database        *Database
	scopeID         string
	candidates      *CandidateRepository
	artifacts       *ArtifactRepository
	sources         *SourceRepository
	experienceIndex ExperienceIndex
}

func NewReviewBackend(
	database *Database,
	scopeID string,
	candidates *CandidateRepository,
	artifacts *ArtifactRepository,
	sources *SourceRepository,
	experienceIndex ExperienceIndex,
) (*ReviewBackend, error) {
	if database == nil || candidates == nil || artifacts == nil || sources == nil {
		return nil, errors.New("sqlstore: Review dependencies must not be nil")
	}
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if experienceIndex == nil {
		experienceIndex = NoExperienceIndex{}
	}
	return &ReviewBackend{
		database: database, scopeID: scopeID, candidates: candidates,
		artifacts: artifacts, sources: sources, experienceIndex: experienceIndex,
	}, nil
}

func (r *ReviewBackend) Initialize(ctx context.Context) error {
	return r.database.Transaction(ctx, func(tx DBTX) error {
		return r.experienceIndex.Initialize(ctx, tx)
	})
}

func (r *ReviewBackend) Propose(
	ctx context.Context,
	candidateID, family string,
	proposal any,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (review.Snapshot, error) {
	var result review.Snapshot
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		if err := r.validateEvidence(ctx, tx, sources, artifacts); err != nil {
			return err
		}
		if err := r.validateTarget(ctx, tx, family, target, artifacts); err != nil {
			return err
		}
		var err error
		result, err = r.candidates.Create(
			ctx, tx, r.scopeID, candidateID, family, proposal, sources, artifacts, target, reason,
		)
		return err
	})
	return result, err
}

func (r *ReviewBackend) Get(ctx context.Context, candidateID string) (review.Snapshot, error) {
	var result review.Snapshot
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = r.candidates.Get(ctx, tx, r.scopeID, candidateID)
		return err
	})
	return result, err
}

func (r *ReviewBackend) GetArtifact(ctx context.Context, ref artifact.Ref) (artifact.Snapshot, error) {
	var result artifact.Snapshot
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var getErr error
		result, getErr = r.artifacts.Get(ctx, tx, r.scopeID, ref)
		return getErr
	})
	if err != nil {
		var missing *RepositoryNotFoundError
		if errors.As(err, &missing) {
			return nil, &artifact.NotFoundError{Ref: ref}
		}
	}
	return result, err
}

func (r *ReviewBackend) List(
	ctx context.Context,
	status review.Status,
	family, cursor *string,
	limit int,
) (review.Page, error) {
	var result review.Page
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = r.candidates.List(ctx, tx, r.scopeID, status, family, cursor, limit)
		return err
	})
	return result, err
}

func (r *ReviewBackend) Revise(
	ctx context.Context,
	candidateID string,
	expectedVersion int64,
	proposal any,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (review.Snapshot, error) {
	var result review.Snapshot
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		current, err := r.candidates.LockPending(ctx, tx, r.scopeID, candidateID, expectedVersion)
		if err != nil {
			return err
		}
		if proposalErr := validateReviewedProposal(current.Family(), proposal); proposalErr != nil {
			return proposalErr
		}
		if !equalOptionalRef(target, current.Target()) {
			return &review.InvalidCandidateError{Field: "target", Detail: "cannot change across Candidate versions"}
		}
		if evidenceErr := r.validateEvidence(ctx, tx, sources, artifacts); evidenceErr != nil {
			return evidenceErr
		}
		if targetErr := r.validateTarget(ctx, tx, current.Family(), target, artifacts); targetErr != nil {
			return targetErr
		}
		result, err = r.candidates.Revise(
			ctx, tx, r.scopeID, candidateID, expectedVersion, proposal,
			sources, artifacts, target, reason,
		)
		return err
	})
	return result, err
}

func (r *ReviewBackend) Reject(
	ctx context.Context,
	candidateID string,
	expectedVersion int64,
	reason string,
) (review.Snapshot, error) {
	var result review.Snapshot
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = r.candidates.Reject(ctx, tx, r.scopeID, candidateID, expectedVersion, reason)
		return err
	})
	return result, err
}

func (r *ReviewBackend) Approve(
	ctx context.Context,
	candidateID string,
	expectedVersion int64,
	idFactory review.IDFactory,
) (review.Snapshot, error) {
	var approved review.Snapshot
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		candidate, err := r.candidates.LockPending(ctx, tx, r.scopeID, candidateID, expectedVersion)
		if err != nil {
			return err
		}
		if lineageErr := validateApprovalLineage(candidate); lineageErr != nil {
			return lineageErr
		}
		draft, err := candidateDraft(candidate)
		if err != nil {
			return err
		}
		var stored artifact.Snapshot
		target := candidate.Target()
		if target == nil {
			artifactID, idErr := idFactory(candidate.Family())
			if idErr != nil {
				return idErr
			}
			stored, err = r.artifacts.Create(ctx, tx, r.scopeID, artifactID, draft)
			if err != nil {
				return err
			}
		} else {
			current, currentErr := r.artifacts.Get(ctx, tx, r.scopeID, *target)
			if currentErr != nil {
				return currentErr
			}
			stored, err = r.artifacts.Revise(ctx, tx, r.scopeID, current, draft)
			if err != nil {
				var conflict *artifact.RevisionConflictError
				if errors.As(err, &conflict) {
					return &review.ArtifactTargetConflictError{Target: *target, Current: conflict.Current}
				}
				return err
			}
		}
		if value, ok := stored.(artifact.Artifact[experience.Content]); ok {
			if indexErr := r.experienceIndex.Replace(ctx, tx, r.scopeID, value); indexErr != nil {
				return indexErr
			}
		}
		approved, err = r.candidates.MarkApproved(
			ctx, tx, r.scopeID, candidateID, expectedVersion, stored.Ref(),
		)
		return err
	})
	return approved, err
}

func (r *ReviewBackend) SearchExperiences(
	ctx context.Context,
	query string,
	limit int,
) ([]experience.SearchHit, error) {
	var result []experience.SearchHit
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = r.experienceIndex.Search(ctx, tx, r.scopeID, query, limit)
		return err
	})
	return result, err
}

func (r *ReviewBackend) validateEvidence(
	ctx context.Context,
	tx DBTX,
	sources []source.Ref,
	artifacts []artifact.Ref,
) error {
	if len(sources)+len(artifacts) < 1 {
		return &review.InvalidCandidateError{Field: "evidence", Detail: "at least one exact reference is required"}
	}
	if len(sources)+len(artifacts) > review.MaxEvidence {
		return &review.InvalidCandidateError{
			Field: "evidence", Detail: fmt.Sprintf("must not exceed %d exact references", review.MaxEvidence),
		}
	}
	for _, ref := range sources {
		if _, err := r.sources.Get(ctx, tx, r.scopeID, ref); err != nil {
			var missing *RepositoryNotFoundError
			if errors.As(err, &missing) {
				return &review.InvalidCandidateError{Field: "evidence", Detail: "reference is not available in this scope"}
			}
			return err
		}
	}
	for _, ref := range artifacts {
		if _, err := r.artifacts.Get(ctx, tx, r.scopeID, ref); err != nil {
			var missing *RepositoryNotFoundError
			if errors.As(err, &missing) {
				return &review.InvalidCandidateError{Field: "evidence", Detail: "reference is not available in this scope"}
			}
			return err
		}
	}
	return nil
}

func (r *ReviewBackend) validateTarget(
	ctx context.Context,
	tx DBTX,
	family string,
	target *artifact.Ref,
	artifacts []artifact.Ref,
) error {
	if target == nil {
		return nil
	}
	if target.Family() != family {
		return &review.InvalidCandidateError{Field: "target", Detail: "must identify a " + family + " Artifact"}
	}
	if !slicesContainsRef(artifacts, *target) {
		return &review.InvalidCandidateError{
			Field: "artifacts", Detail: "must include the exact target " + family + " Artifact",
		}
	}
	latest, err := r.artifacts.Latest(ctx, tx, r.scopeID, target.Family(), target.ID())
	if err != nil {
		var missing *RepositoryNotFoundError
		if errors.As(err, &missing) {
			return &review.InvalidCandidateError{Field: "target", Detail: "Artifact is not available in this scope"}
		}
		return err
	}
	if latest.Ref() != *target {
		return &review.ArtifactTargetConflictError{Target: *target, Current: latest.Ref()}
	}
	return nil
}

func validateReviewedProposal(family string, proposal any) error {
	expected := map[string]reflect.Type{
		experience.Family: reflect.TypeFor[experience.Content](),
		skill.Family:      reflect.TypeFor[skill.Content](),
	}[family]
	if expected == nil || reflect.TypeOf(proposal) != expected {
		return &review.InvalidCandidateError{Field: "family", Detail: family}
	}
	return nil
}

func validateApprovalLineage(candidate review.Snapshot) error {
	if candidate.Family() != skill.Family {
		return nil
	}
	target := candidate.Target()
	artifacts := candidate.Artifacts()
	if target == nil {
		for _, ref := range artifacts {
			if ref.Family() != experience.Family {
				return &review.InvalidCandidateError{
					Field: "artifacts", Detail: "new managed Skill lineage may reference only Experience Artifacts",
				}
			}
		}
		return nil
	}
	if !slicesContainsRef(artifacts, *target) {
		return &review.InvalidCandidateError{Field: "artifacts", Detail: "managed Skill lineage must include its exact target"}
	}
	if len(candidate.Sources()) == 0 {
		return &review.InvalidCandidateError{Field: "sources", Detail: "managed Skill replacement requires bounded Source evidence"}
	}
	return nil
}

func candidateDraft(candidate review.Snapshot) (artifact.DraftSnapshot, error) {
	switch proposal := candidate.ProposalValue().(type) {
	case experience.Content:
		if candidate.Family() != experience.Family {
			break
		}
		return experience.NewDraft(proposal, candidate.Sources(), candidate.Artifacts())
	case skill.Content:
		if candidate.Family() != skill.Family {
			break
		}
		return skill.NewDraft(proposal, candidate.Sources(), candidate.Artifacts())
	}
	return nil, &review.InvalidCandidateError{Field: "family", Detail: candidate.Family()}
}

func equalOptionalRef(left, right *artifact.Ref) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func slicesContainsRef(values []artifact.Ref, target artifact.Ref) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
