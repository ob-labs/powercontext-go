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
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/source"
)

// CandidateRepository stores family-neutral immutable proposal versions and
// mutable lifecycle heads.
type CandidateRepository struct {
	dialect  Dialect
	byFamily map[string]ArtifactCodec
}

func NewCandidateRepository(dialect Dialect, proposalCodecs ...ArtifactCodec) (*CandidateRepository, error) {
	if dialect != SQLiteDialect && dialect != MySQLDialect {
		return nil, fmt.Errorf("sqlstore: unsupported dialect %q", dialect)
	}
	if len(proposalCodecs) == 0 {
		return nil, errors.New("sqlstore: at least one Candidate proposal codec is required")
	}
	repository := &CandidateRepository{dialect: dialect, byFamily: make(map[string]ArtifactCodec)}
	for _, codec := range proposalCodecs {
		if _, exists := repository.byFamily[codec.family]; exists {
			return nil, &CodecConflictError{Route: "candidate family", Value: codec.family}
		}
		repository.byFamily[codec.family] = codec
	}
	return repository, nil
}

func (r *CandidateRepository) Create(
	ctx context.Context,
	db DBTX,
	scopeID, candidateID, family string,
	proposal any,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (review.Snapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if err := r.requireProposal(family, proposal); err != nil {
		return nil, err
	}
	candidate, err := review.NewCandidate(
		candidateID, 1, family, review.Pending, proposal, sources, artifacts,
		target, reason, nil, nil,
	)
	if err != nil {
		return nil, err
	}
	current, found, err := r.findCurrent(ctx, db, scopeID, candidateID, false)
	if err != nil {
		return nil, err
	}
	if found {
		return nil, &review.CandidateConflictError{
			CandidateID: candidateID, ExpectedVersion: 1, CurrentVersion: current.Version(),
		}
	}
	if err := r.insertVersion(ctx, db, scopeID, candidate); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO pc_artifact_candidate_heads
        (scope_id, candidate_id, family, version, status)
        VALUES (?, ?, ?, 1, ?)`, scopeID, candidateID, family, string(review.Pending)); err != nil {
		return nil, err
	}
	return candidate, nil
}

func (r *CandidateRepository) Get(
	ctx context.Context,
	db DBTX,
	scopeID, candidateID string,
) (review.Snapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	candidate, found, err := r.findCurrent(ctx, db, scopeID, candidateID, false)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &review.CandidateNotFoundError{CandidateID: candidateID}
	}
	return candidate, nil
}

func (r *CandidateRepository) List(
	ctx context.Context,
	db DBTX,
	scopeID string,
	status review.Status,
	family, cursor *string,
	limit int,
) (page review.Page, returnErr error) {
	if err := requireScope(scopeID); err != nil {
		return review.Page{}, err
	}
	if status != review.Pending && status != review.Approved && status != review.Rejected {
		return review.Page{}, &review.InvalidCandidateError{Field: "status", Detail: string(status)}
	}
	if limit < 1 || limit > review.MaxPageSize {
		return review.Page{}, &InvalidRepositoryArgumentError{
			Field: "limit", Detail: fmt.Sprintf("must be between 1 and %d", review.MaxPageSize),
		}
	}
	if family != nil {
		if _, ok := r.byFamily[*family]; !ok {
			return review.Page{}, &review.InvalidCandidateError{Field: "family", Detail: *family}
		}
	}
	query := candidateSelect + ` WHERE h.scope_id = ? AND h.status = ?`
	arguments := []any{scopeID, string(status)}
	if family != nil {
		query += " AND h.family = ?"
		arguments = append(arguments, *family)
	}
	if cursor != nil {
		query += " AND h.candidate_id > ?"
		arguments = append(arguments, *cursor)
	}
	query += " ORDER BY h.candidate_id LIMIT ?"
	arguments = append(arguments, limit+1)
	rows, err := db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return review.Page{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	values := make([]review.Snapshot, 0, limit+1)
	for rows.Next() {
		candidate, err := r.scanAndDecode(rows)
		if err != nil {
			return review.Page{}, err
		}
		values = append(values, candidate)
	}
	if err := rows.Err(); err != nil {
		return review.Page{}, err
	}
	hasMore := len(values) > limit
	if hasMore {
		values = values[:limit]
	}
	var next *string
	if hasMore && len(values) > 0 {
		value := values[len(values)-1].ID()
		next = &value
	}
	page = review.Page{Candidates: values, NextCursor: next}
	return page, nil
}

func (r *CandidateRepository) Revise(
	ctx context.Context,
	db DBTX,
	scopeID, candidateID string,
	expectedVersion int64,
	proposal any,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (review.Snapshot, error) {
	current, err := r.LockPending(ctx, db, scopeID, candidateID, expectedVersion)
	if err != nil {
		return nil, err
	}
	if err := r.requireProposal(current.Family(), proposal); err != nil {
		return nil, err
	}
	revised, err := review.NewCandidate(
		candidateID, current.Version()+1, current.Family(), review.Pending, proposal,
		sources, artifacts, target, reason, nil, nil,
	)
	if err != nil {
		return nil, err
	}
	if err := r.insertVersion(ctx, db, scopeID, revised); err != nil {
		return nil, err
	}
	result, err := db.ExecContext(ctx, `UPDATE pc_artifact_candidate_heads SET version = ?
        WHERE scope_id = ? AND candidate_id = ? AND version = ? AND status = ?`,
		revised.Version(), scopeID, candidateID, expectedVersion, string(review.Pending))
	if err != nil {
		return nil, err
	}
	if err := r.requireOneCAS(ctx, db, scopeID, candidateID, expectedVersion, result); err != nil {
		return nil, err
	}
	return revised, nil
}

func (r *CandidateRepository) Reject(
	ctx context.Context,
	db DBTX,
	scopeID, candidateID string,
	expectedVersion int64,
	reason string,
) (review.Snapshot, error) {
	current, err := r.LockPending(ctx, db, scopeID, candidateID, expectedVersion)
	if err != nil {
		return nil, err
	}
	reasonPointer := &reason
	rejected, err := review.NewCandidate(
		current.ID(), current.Version(), current.Family(), review.Rejected, current.ProposalValue(),
		current.Sources(), current.Artifacts(), current.Target(), current.Reason(), nil, reasonPointer,
	)
	if err != nil {
		return nil, err
	}
	result, err := db.ExecContext(ctx, `UPDATE pc_artifact_candidate_heads
        SET status = ?, decision_reason = ?
        WHERE scope_id = ? AND candidate_id = ? AND version = ? AND status = ?`,
		string(review.Rejected), reason, scopeID, candidateID, expectedVersion, string(review.Pending))
	if err != nil {
		return nil, err
	}
	if err := r.requireOneCAS(ctx, db, scopeID, candidateID, expectedVersion, result); err != nil {
		return nil, err
	}
	return rejected, nil
}

func (r *CandidateRepository) LockPending(
	ctx context.Context,
	db DBTX,
	scopeID, candidateID string,
	expectedVersion int64,
) (review.Snapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE pc_artifact_candidate_heads SET version = version
        WHERE scope_id = ? AND candidate_id = ? AND version = ? AND status = ?`,
		scopeID, candidateID, expectedVersion, string(review.Pending)); err != nil {
		return nil, err
	}
	current, found, err := r.findCurrent(ctx, db, scopeID, candidateID, true)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &review.CandidateNotFoundError{CandidateID: candidateID}
	}
	if current.Status() != review.Pending {
		return nil, &review.CandidateTerminalError{CandidateID: candidateID, Status: current.Status()}
	}
	if current.Version() != expectedVersion {
		return nil, &review.CandidateConflictError{
			CandidateID: candidateID, ExpectedVersion: expectedVersion, CurrentVersion: current.Version(),
		}
	}
	return current, nil
}

func (r *CandidateRepository) MarkApproved(
	ctx context.Context,
	db DBTX,
	scopeID, candidateID string,
	expectedVersion int64,
	resultRef artifact.Ref,
) (review.Snapshot, error) {
	result, err := db.ExecContext(ctx, `UPDATE pc_artifact_candidate_heads
        SET status = ?, result_family = ?, result_artifact_id = ?, result_revision = ?
        WHERE scope_id = ? AND candidate_id = ? AND version = ? AND status = ?`,
		string(review.Approved), resultRef.Family(), resultRef.ID(), resultRef.Revision(),
		scopeID, candidateID, expectedVersion, string(review.Pending))
	if err != nil {
		return nil, err
	}
	if err := r.requireOneCAS(ctx, db, scopeID, candidateID, expectedVersion, result); err != nil {
		return nil, err
	}
	return r.Get(ctx, db, scopeID, candidateID)
}

func (r *CandidateRepository) insertVersion(
	ctx context.Context,
	db DBTX,
	scopeID string,
	candidate review.Snapshot,
) error {
	codec := r.byFamily[candidate.Family()]
	proposal, err := codec.encode(candidate.ProposalValue())
	if err != nil {
		return &InvalidStoredPayloadError{Kind: "candidate-proposal", Name: candidate.Family(), Issue: "value is not JSON serializable"}
	}
	sourceRefs, err := encodeSourceRefs(candidate.Sources())
	if err != nil {
		return err
	}
	artifactRefs, err := encodeArtifactRefs(candidate.Artifacts())
	if err != nil {
		return err
	}
	target := candidate.Target()
	var targetFamily, targetID, targetRevision any
	if target != nil {
		targetFamily, targetID, targetRevision = target.Family(), target.ID(), target.Revision()
	}
	_, err = db.ExecContext(ctx, `INSERT INTO pc_artifact_candidate_versions (
        scope_id, candidate_id, version, family, proposal, source_refs, artifact_refs,
        target_family, target_artifact_id, target_revision, reason
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, scopeID, candidate.ID(), candidate.Version(),
		candidate.Family(), proposal, sourceRefs, artifactRefs, targetFamily, targetID, targetRevision,
		nullableText(candidate.Reason()))
	return err
}

const candidateSelect = `SELECT h.candidate_id, h.family, h.version, h.status,
    h.result_family, h.result_artifact_id, h.result_revision, h.decision_reason,
    v.proposal, v.source_refs, v.artifact_refs,
    v.target_family, v.target_artifact_id, v.target_revision, v.reason
    FROM pc_artifact_candidate_heads AS h
    JOIN pc_artifact_candidate_versions AS v
      ON v.scope_id = h.scope_id
     AND v.candidate_id = h.candidate_id
     AND v.version = h.version`

func (r *CandidateRepository) findCurrent(
	ctx context.Context,
	db DBTX,
	scopeID, candidateID string,
	locked bool,
) (review.Snapshot, bool, error) {
	query := candidateSelect + " WHERE h.scope_id = ? AND h.candidate_id = ?"
	if locked && r.dialect == MySQLDialect {
		query += " FOR UPDATE"
	}
	candidate, err := r.scanAndDecode(db.QueryRowContext(ctx, query, scopeID, candidateID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return candidate, err == nil, err
}

func (r *CandidateRepository) scanAndDecode(value scanner) (review.Snapshot, error) {
	var candidateID, family, status string
	var version, resultFamily, resultID, resultRevision, decisionReason any
	var proposalPayload, sourcePayload, artifactPayload any
	var targetFamily, targetID, targetRevision, reason any
	if err := value.Scan(
		&candidateID, &family, &version, &status,
		&resultFamily, &resultID, &resultRevision, &decisionReason,
		&proposalPayload, &sourcePayload, &artifactPayload,
		&targetFamily, &targetID, &targetRevision, &reason,
	); err != nil {
		return nil, err
	}
	decodedVersion, ok := integer(version)
	if !ok {
		return nil, &InvalidStoredColumnError{Column: "version", Expected: "an integer"}
	}
	codec, ok := r.byFamily[family]
	if !ok {
		return nil, &review.InvalidCandidateError{Field: "family", Detail: family}
	}
	proposalBytes, err := storedBytes(proposalPayload, "proposal")
	if err != nil {
		return nil, err
	}
	proposal, err := codec.decodeContent(proposalBytes)
	if err != nil {
		return nil, &InvalidStoredPayloadError{Kind: "candidate-proposal", Name: family, Issue: "payload does not match the model"}
	}
	sourceBytes, err := storedBytes(sourcePayload, "source_refs")
	if err != nil {
		return nil, err
	}
	sources, err := decodeSourceRefs(sourceBytes)
	if err != nil {
		return nil, &InvalidStoredPayloadError{Kind: "candidate", Name: "source-refs", Issue: "payload does not match the model"}
	}
	artifactBytes, err := storedBytes(artifactPayload, "artifact_refs")
	if err != nil {
		return nil, err
	}
	artifacts, err := decodeArtifactRefs(artifactBytes)
	if err != nil {
		return nil, &InvalidStoredPayloadError{Kind: "candidate", Name: "artifact-refs", Issue: "payload does not match the model"}
	}
	target, err := nullableArtifactRef(targetFamily, targetID, targetRevision)
	if err != nil {
		return nil, err
	}
	result, err := nullableArtifactRef(resultFamily, resultID, resultRevision)
	if err != nil {
		return nil, err
	}
	decodedReason, err := nullableString(reason, "reason")
	if err != nil {
		return nil, err
	}
	decodedDecision, err := nullableString(decisionReason, "decision_reason")
	if err != nil {
		return nil, err
	}
	return review.NewCandidate(
		candidateID, decodedVersion, family, review.Status(status), proposal,
		sources, artifacts, target, decodedReason, result, decodedDecision,
	)
}

func (r *CandidateRepository) requireProposal(family string, proposal any) error {
	codec, ok := r.byFamily[family]
	if !ok {
		return &review.InvalidCandidateError{Field: "family", Detail: family}
	}
	if reflect.TypeOf(proposal) != codec.contentType {
		return &review.InvalidCandidateError{
			Field: "proposal", Detail: fmt.Sprintf("expected %s", codec.contentType.Name()),
		}
	}
	return nil
}

func (r *CandidateRepository) requireOneCAS(
	ctx context.Context,
	db DBTX,
	scopeID, candidateID string,
	expectedVersion int64,
	result sql.Result,
) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	current, getErr := r.Get(ctx, db, scopeID, candidateID)
	if getErr != nil {
		return getErr
	}
	if current.Status() != review.Pending {
		return &review.CandidateTerminalError{CandidateID: candidateID, Status: current.Status()}
	}
	return &review.CandidateConflictError{
		CandidateID: candidateID, ExpectedVersion: expectedVersion, CurrentVersion: current.Version(),
	}
}

func nullableArtifactRef(family, artifactID, revision any) (*artifact.Ref, error) {
	if family == nil && artifactID == nil && revision == nil {
		return nil, nil
	}
	familyText, familyOK := family.(string)
	idText, idOK := artifactID.(string)
	revisionValue, revisionOK := integer(revision)
	if !familyOK || !idOK || !revisionOK {
		return nil, &InvalidStoredColumnError{Column: "artifact reference", Expected: "a complete reference or null"}
	}
	ref, err := artifact.NewRef(familyText, idText, revisionValue)
	if err != nil {
		return nil, err
	}
	return &ref, nil
}

func nullableString(value any, column string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, &InvalidStoredColumnError{Column: column, Expected: "a string or null"}
	}
	return &text, nil
}
