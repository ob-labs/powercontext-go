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

package sqlstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestReviewRevisionAppendsImmutableFamilyTypedCandidateVersion(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, "project", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "task-1", "bounded evidence")
	originalProposal := reviewExperience(t, "Initial lesson.")
	original, err := fixture.service.ProposeExperience(
		fixture.ctx, originalProposal, []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	wrongProposal := reviewSkill(t, "Use the wrong family.")
	_, err = fixture.service.Revise(
		fixture.ctx, original.ID(), 1, wrongProposal, []source.Ref{evidence}, nil, nil, nil,
	)
	assertReviewInvalidField(t, err, "family")
	current, err := fixture.service.Get(fixture.ctx, original.ID())
	if err != nil {
		t.Fatal(err)
	}
	if current.Version() != 1 || current.ProposalValue() != originalProposal {
		t.Fatalf("failed family revision changed Candidate: %#v", current)
	}

	revisedProposal := reviewExperience(t, "Reviewed lesson.")
	revised, err := fixture.service.Revise(
		fixture.ctx, original.ID(), 1, revisedProposal,
		[]source.Ref{evidence, evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if revised.Version() != 2 || len(revised.Sources()) != 1 {
		t.Fatalf("revised Candidate = %#v", revised)
	}

	rows, err := fixture.database.SQLDB().QueryContext(fixture.ctx, `SELECT version, proposal
        FROM pc_artifact_candidate_versions
        WHERE scope_id = ? AND candidate_id = ? ORDER BY version`, fixture.scope, original.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close Candidate version rows: %v", err)
		}
	}()
	var versions []int64
	var proposals []string
	for rows.Next() {
		var version int64
		var proposal []byte
		if err := rows.Scan(&version, &proposal); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
		proposals = append(proposals, string(proposal))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 || proposals[0] == proposals[1] {
		t.Fatalf("immutable versions=%v proposals=%q", versions, proposals)
	}
}

func TestReviewRejectIsTerminalAndEvidenceIsScopeIsolated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	sources, artifacts := repositories(t)
	candidates := reviewCandidateRepository(t)
	ids := &sequenceIDs{}
	scopeA := newReviewService(t, database, "scope-a", sources, artifacts, candidates, ids.New, nil)
	scopeB := newReviewService(t, database, "scope-b", sources, artifacts, candidates, ids.New, nil)

	value := contentSource(t, "task-1", "bounded evidence", nil)
	var evidence source.Ref
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		stored, addErr := sources.Add(ctx, tx, "scope-a", value)
		evidence = stored.Ref
		return addErr
	}); err != nil {
		t.Fatal(err)
	}
	proposal := reviewExperience(t, "Keep exact scope boundaries.")
	_, err := scopeB.ProposeExperience(ctx, proposal, []source.Ref{evidence}, nil, nil, nil)
	assertReviewInvalidField(t, err, "evidence")

	candidate, err := scopeA.ProposeExperience(ctx, proposal, []source.Ref{evidence}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := scopeA.Reject(ctx, candidate.ID(), 1, "The evidence does not support the lesson.")
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Status() != review.Rejected || rejected.ResultArtifact() != nil ||
		rejected.DecisionReason() == nil || *rejected.DecisionReason() != "The evidence does not support the lesson." {
		t.Fatalf("rejected Candidate = %#v", rejected)
	}
	_, err = scopeA.Revise(
		ctx, candidate.ID(), 1, reviewExperience(t, "replacement"), []source.Ref{evidence}, nil, nil, nil,
	)
	var terminal *review.CandidateTerminalError
	if !errors.As(err, &terminal) || terminal.Status != review.Rejected {
		t.Fatalf("terminal revision error = %v", err)
	}
	assertArtifactCount(t, database, "scope-a", experience.Family, 0)
}

func TestManagedSkillApprovalValidatesLineageBeforeArtifactWrite(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, "project", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "skill-source", "reviewed Skill source")
	initial, err := fixture.service.ProposeSkill(
		fixture.ctx, reviewSkill(t, "Run the checked workflow."), []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	initialApproval, err := fixture.service.Approve(fixture.ctx, initial.ID(), 1)
	if err != nil {
		t.Fatal(err)
	}
	initialRef := initialApproval.ResultArtifact()
	if initialRef == nil {
		t.Fatal("initial Skill approval has no result Artifact")
	}

	unsupportedCreate, err := fixture.service.ProposeSkill(
		fixture.ctx, reviewSkill(t, "Use another managed Skill as evidence."),
		nil, []artifact.Ref{*initialRef}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.Approve(fixture.ctx, unsupportedCreate.ID(), 1)
	assertReviewInvalidField(t, err, "artifacts")
	assertPendingCandidate(t, fixture.service, unsupportedCreate.ID())

	unsupportedReplacement, err := fixture.service.ProposeSkill(
		fixture.ctx, reviewSkill(t, "Replace without usage evidence."),
		nil, []artifact.Ref{*initialRef}, initialRef, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.Approve(fixture.ctx, unsupportedReplacement.ID(), 1)
	assertReviewInvalidField(t, err, "sources")
	assertPendingCandidate(t, fixture.service, unsupportedReplacement.ID())
	assertArtifactCount(t, fixture.database, fixture.scope, skill.Family, 1)

	replacement, err := fixture.service.ProposeSkill(
		fixture.ctx, reviewSkill(t, "Replace using bounded usage evidence."),
		[]source.Ref{evidence}, []artifact.Ref{*initialRef}, initialRef, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	replacementApproval, err := fixture.service.Approve(fixture.ctx, replacement.ID(), 1)
	if err != nil {
		t.Fatal(err)
	}
	resultRef := replacementApproval.ResultArtifact()
	if resultRef == nil || resultRef.ID() != initialRef.ID() || resultRef.Revision() != 2 {
		t.Fatalf("replacement result = %#v", resultRef)
	}
	stored, err := fixture.service.GetSkill(fixture.ctx, *resultRef)
	if err != nil {
		t.Fatal(err)
	}
	lineage := stored.Lineage()
	if got := lineage.Sources(); len(got) != 1 || got[0] != evidence {
		t.Fatalf("replacement Source lineage = %#v", got)
	}
	if got := lineage.Artifacts(); len(got) != 1 || got[0] != *initialRef {
		t.Fatalf("replacement Artifact lineage = %#v", got)
	}
}

func TestReviewStaleExperienceTargetKeepsCandidatePending(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, "project", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "task-1", "first result")
	initial, err := fixture.service.ProposeExperience(
		fixture.ctx, reviewExperience(t, "initial"), []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	initialApproval, err := fixture.service.Approve(fixture.ctx, initial.ID(), 1)
	if err != nil {
		t.Fatal(err)
	}
	initialRef := initialApproval.ResultArtifact()
	if initialRef == nil {
		t.Fatal("initial approval has no result Artifact")
	}

	_, err = fixture.service.ProposeExperience(
		fixture.ctx, reviewExperience(t, "missing predecessor"),
		[]source.Ref{evidence}, nil, initialRef, nil,
	)
	assertReviewInvalidField(t, err, "artifacts")

	winner, err := fixture.service.ProposeExperience(
		fixture.ctx, reviewExperience(t, "winner"),
		[]source.Ref{evidence}, []artifact.Ref{*initialRef}, initialRef, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := fixture.service.ProposeExperience(
		fixture.ctx, reviewExperience(t, "stale"),
		[]source.Ref{evidence}, []artifact.Ref{*initialRef}, initialRef, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	winnerApproval, err := fixture.service.Approve(fixture.ctx, winner.ID(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if winnerApproval.ResultArtifact() == nil || winnerApproval.ResultArtifact().Revision() != 2 {
		t.Fatalf("winner approval = %#v", winnerApproval)
	}
	_, err = fixture.service.Approve(fixture.ctx, stale.ID(), 1)
	var conflict *review.ArtifactTargetConflictError
	if !errors.As(err, &conflict) || conflict.Target != *initialRef || conflict.Current.Revision() != 2 {
		t.Fatalf("stale target error = %v", err)
	}
	assertPendingCandidate(t, fixture.service, stale.ID())
	assertArtifactCount(t, fixture.database, fixture.scope, experience.Family, 2)
}

func TestReviewApprovalRollsBackWhenCandidateStatusUpdateFails(t *testing.T) {
	t.Parallel()
	fixedID := func(kind string) (string, error) { return kind + "-fixed", nil }
	fixture := newReviewFixture(t, "project", fixedID)
	evidence := fixture.capture(t, "task-1", "bounded evidence")
	candidate, err := fixture.service.ProposeExperience(
		fixture.ctx, reviewExperience(t, "atomic approval"), []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, createTriggerErr := fixture.database.SQLDB().ExecContext(fixture.ctx, `CREATE TRIGGER pc_test_fail_candidate_approval
        BEFORE UPDATE OF status ON pc_artifact_candidate_heads
        WHEN NEW.status = 'approved'
		BEGIN SELECT RAISE(ABORT, 'injected Candidate status failure'); END`); createTriggerErr != nil {
		t.Fatal(createTriggerErr)
	}
	if _, approveErr := fixture.service.Approve(fixture.ctx, candidate.ID(), 1); approveErr == nil {
		t.Fatal("approval unexpectedly survived injected status failure")
	}
	assertPendingCandidate(t, fixture.service, candidate.ID())
	assertArtifactCount(t, fixture.database, fixture.scope, experience.Family, 0)

	if _, dropTriggerErr := fixture.database.SQLDB().ExecContext(fixture.ctx, `DROP TRIGGER pc_test_fail_candidate_approval`); dropTriggerErr != nil {
		t.Fatal(dropTriggerErr)
	}
	approved, err := fixture.service.Approve(fixture.ctx, candidate.ID(), 1)
	if err != nil {
		t.Fatal(err)
	}
	wantRef, err := artifact.NewRef(experience.Family, "experience-fixed", 1)
	if err != nil {
		t.Fatal(err)
	}
	if approved.ResultArtifact() == nil || *approved.ResultArtifact() != wantRef {
		t.Fatalf("approval after retry = %#v", approved)
	}
}

func TestReviewEvidenceValidationPreservesOperationalStorageErrors(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, "project", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "task-1", "bounded evidence")
	if _, err := fixture.database.SQLDB().ExecContext(fixture.ctx, `UPDATE pc_sources SET payload = ?
        WHERE scope_id = ? AND source_type = ? AND source_id = ?`,
		[]byte(`{"name":`), fixture.scope, evidence.Type(), evidence.ID()); err != nil {
		t.Fatal(err)
	}

	_, err := fixture.service.ProposeExperience(
		fixture.ctx, reviewExperience(t, "do not mask corruption"), []source.Ref{evidence}, nil, nil, nil,
	)
	var storedPayload *sqlstore.InvalidStoredPayloadError
	if !errors.As(err, &storedPayload) {
		t.Fatalf("expected stored payload error, got %v", err)
	}
	var invalid *review.InvalidCandidateError
	if errors.As(err, &invalid) {
		t.Fatalf("stored payload error was masked as invalid Candidate: %v", err)
	}

	canceled, cancel := context.WithCancel(fixture.ctx)
	cancel()
	_, err = fixture.service.ProposeExperience(
		canceled, reviewExperience(t, "preserve cancellation"), []source.Ref{evidence}, nil, nil, nil,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled proposal error = %v", err)
	}
}

func TestCandidateListUsesStableCursorAndStatusFamilyFilters(t *testing.T) {
	t.Parallel()
	fixture := newReviewFixture(t, "project", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "task-1", "bounded evidence")
	for index := range 3 {
		candidate, err := fixture.service.ProposeExperience(
			fixture.ctx, reviewExperience(t, string(rune('a'+index))), []source.Ref{evidence}, nil, nil, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			if _, err := fixture.service.Reject(fixture.ctx, candidate.ID(), 1, "not reusable"); err != nil {
				t.Fatal(err)
			}
		}
	}
	family := experience.Family
	first, err := fixture.service.List(fixture.ctx, review.Pending, &family, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Candidates) != 1 || first.Candidates[0].ID() != "candidate-2" ||
		first.NextCursor == nil || *first.NextCursor != "candidate-2" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := fixture.service.List(fixture.ctx, review.Pending, &family, first.NextCursor, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Candidates) != 1 || second.Candidates[0].ID() != "candidate-3" || second.NextCursor != nil {
		t.Fatalf("second page = %#v", second)
	}
	rejected, err := fixture.service.List(fixture.ctx, review.Rejected, &family, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected.Candidates) != 1 || rejected.Candidates[0].ID() != "candidate-1" {
		t.Fatalf("rejected page = %#v", rejected)
	}
}

type reviewFixture struct {
	ctx       context.Context
	database  *sqlstore.Database
	sources   *sqlstore.SourceRepository
	artifacts *sqlstore.ArtifactRepository
	service   *review.Service
	scope     string
}

func newReviewFixture(t *testing.T, scope string, ids review.IDFactory) reviewFixture {
	t.Helper()
	database := openTestDatabase(t)
	sources, artifacts := repositories(t)
	candidates := reviewCandidateRepository(t)
	service := newReviewService(t, database, scope, sources, artifacts, candidates, ids, nil)
	return reviewFixture{
		ctx: context.Background(), database: database, sources: sources,
		artifacts: artifacts, service: service, scope: scope,
	}
}

func (f reviewFixture) capture(t *testing.T, id, content string) source.Ref {
	t.Helper()
	value := contentSource(t, id, content, nil)
	var result sqlstore.StoredSource
	if err := f.database.Transaction(f.ctx, func(tx sqlstore.DBTX) error {
		var err error
		result, err = f.sources.Add(f.ctx, tx, f.scope, value)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result.Ref
}

func reviewCandidateRepository(t *testing.T) *sqlstore.CandidateRepository {
	t.Helper()
	value, err := sqlstore.NewCandidateRepository(
		sqlstore.SQLiteDialect, sqlstore.ExperienceArtifactCodec(), sqlstore.SkillArtifactCodec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func newReviewService(
	t *testing.T,
	database *sqlstore.Database,
	scope string,
	sources *sqlstore.SourceRepository,
	artifacts *sqlstore.ArtifactRepository,
	candidates *sqlstore.CandidateRepository,
	ids review.IDFactory,
	index sqlstore.ExperienceIndex,
) *review.Service {
	t.Helper()
	backend, err := sqlstore.NewReviewBackend(database, scope, candidates, artifacts, sources, index)
	if err != nil {
		t.Fatal(err)
	}
	service, err := review.NewService(backend, ids)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func reviewExperience(t *testing.T, lesson string) experience.Content {
	t.Helper()
	value, err := experience.NewContent("situation", "action", "outcome", lesson)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func reviewSkill(t *testing.T, instructions string) skill.Content {
	t.Helper()
	value, err := skill.NewContent(
		"powercontext-review", "Use for reviewed changes.", instructions, []string{"tests pass"},
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func assertReviewInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	var invalid *review.InvalidCandidateError
	if !errors.As(err, &invalid) || invalid.Field != field {
		t.Fatalf("expected invalid Candidate field %q, got %v", field, err)
	}
}

func assertPendingCandidate(t *testing.T, service *review.Service, candidateID string) {
	t.Helper()
	value, err := service.Get(context.Background(), candidateID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status() != review.Pending || value.ResultArtifact() != nil {
		t.Fatalf("Candidate after rolled-back approval = %#v", value)
	}
}

func assertArtifactCount(
	t *testing.T,
	database *sqlstore.Database,
	scope, family string,
	want int,
) {
	t.Helper()
	var got int
	if err := database.SQLDB().QueryRowContext(context.Background(), `SELECT COUNT(*) FROM pc_artifacts
        WHERE scope_id = ? AND family = ?`, scope, family).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s Artifact count = %d, want %d", family, got, want)
	}
}
