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
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
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

func TestPackageBackedSkillReviewPersistsOnlyCanonicalReference(t *testing.T) {
	fixture := newReviewFixture(t, "package-review", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "package-evidence", "reviewed package evidence")
	snapshot := skillPackageSnapshot(t, "package-review-skill")
	proposal, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := fixture.service.ProposePackageSkill(
		fixture.ctx, proposal, []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Proposal().Reference() != snapshot.Reference() {
		t.Fatalf("candidate package reference = %#v, want %#v", candidate.Proposal().Reference(), snapshot.Reference())
	}
	assertPackageReviewPayload(t, fixture, candidate.ID(), "proposal", snapshot)
	assertPackageReviewRow(t, fixture, snapshot, 1)

	approved, err := fixture.service.Approve(fixture.ctx, candidate.ID(), candidate.Version())
	if err != nil {
		t.Fatal(err)
	}
	if approved.ResultArtifact() == nil {
		t.Fatal("approved package Candidate has no Artifact")
	}
	stored, err := fixture.service.GetPackageSkill(fixture.ctx, *approved.ResultArtifact())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Content().Reference() != snapshot.Reference() {
		t.Fatalf("stored package Artifact reference = %#v, want %#v", stored.Content().Reference(), snapshot.Reference())
	}
	assertPackageArtifactPayload(t, fixture, *approved.ResultArtifact(), snapshot)

	legacy := reviewSkill(t, "Keep legacy skill JSON unchanged.")
	legacyCandidate, err := fixture.service.ProposeSkill(
		fixture.ctx, legacy, []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	var legacyPayload []byte
	if err := fixture.database.SQLDB().QueryRowContext(fixture.ctx, `SELECT proposal
        FROM pc_artifact_candidate_versions WHERE scope_id = ? AND candidate_id = ?`,
		fixture.scope, legacyCandidate.ID()).Scan(&legacyPayload); err != nil {
		t.Fatal(err)
	}
	wantLegacy := `{"name":"powercontext-review","description":"Use for reviewed changes.","instructions":"Keep legacy skill JSON unchanged.","validation":["tests pass"]}`
	if string(legacyPayload) != wantLegacy {
		t.Fatalf("legacy Skill proposal JSON changed: %s", legacyPayload)
	}
}

func TestPackageSkillProposalRollsBackPackageWhenCandidateWriteFails(t *testing.T) {
	fixedID := func(kind string) (string, error) { return kind + "-package", nil }
	fixture := newReviewFixture(t, "package-rollback", fixedID)
	evidence := fixture.capture(t, "package-evidence", "reviewed package evidence")
	snapshot := skillPackageSnapshot(t, "package-rollback-skill")
	proposal, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, triggerErr := fixture.database.SQLDB().ExecContext(fixture.ctx, `CREATE TRIGGER pc_test_fail_package_candidate
        BEFORE INSERT ON pc_artifact_candidate_heads
		BEGIN SELECT RAISE(ABORT, 'injected package Candidate failure'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}

	_, err = fixture.service.ProposePackageSkill(fixture.ctx, proposal, []source.Ref{evidence}, nil, nil, nil)
	if err == nil {
		t.Fatal("package proposal unexpectedly survived Candidate failure")
	}
	assertPackageReviewRow(t, fixture, snapshot, 0)
}

func TestPackageSkillRevisionPersistsExactReferenceAndRollsBackOnCandidateFailure(t *testing.T) {
	fixture := newReviewFixture(t, "package-revision", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "package-evidence", "reviewed package evidence")
	initialSnapshot := skillPackageSnapshot(t, "package-revision-initial")
	initialContent, err := skill.NewPackageContent(initialSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := fixture.service.ProposePackageSkill(
		fixture.ctx, initialContent, []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	initialApproval, err := fixture.service.Approve(fixture.ctx, initial.ID(), initial.Version())
	if err != nil {
		t.Fatal(err)
	}
	initialRef := initialApproval.ResultArtifact()
	if initialRef == nil {
		t.Fatal("initial package Skill has no Artifact")
	}

	revisedSnapshot := skillPackageSnapshot(t, "package-revision-revised")
	revisedContent, err := skill.NewPackageContent(revisedSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := fixture.service.ProposePackageSkill(
		fixture.ctx, revisedContent, []source.Ref{evidence}, []artifact.Ref{*initialRef}, initialRef, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	replacementApproval, err := fixture.service.Approve(fixture.ctx, replacement.ID(), replacement.Version())
	if err != nil {
		t.Fatal(err)
	}
	revisedRef := replacementApproval.ResultArtifact()
	if revisedRef == nil || revisedRef.ID() != initialRef.ID() || revisedRef.Revision() != 2 {
		t.Fatalf("revised package Artifact = %#v, want revision 2 of %#v", revisedRef, initialRef)
	}
	stored, err := fixture.service.GetPackageSkill(fixture.ctx, *revisedRef)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Content().Reference() != revisedSnapshot.Reference() {
		t.Fatalf("revised package reference = %#v, want %#v", stored.Content().Reference(), revisedSnapshot.Reference())
	}
	if lineage := stored.Lineage(); len(lineage.Sources()) != 1 || lineage.Sources()[0] != evidence ||
		len(lineage.Artifacts()) != 1 || lineage.Artifacts()[0] != *initialRef {
		t.Fatalf("revised package lineage = %#v", lineage)
	}
	assertPackageArtifactPayload(t, fixture, *revisedRef, revisedSnapshot)

	rollbackSnapshot := skillPackageSnapshot(t, "package-revision-rollback")
	rollbackContent, err := skill.NewPackageContent(rollbackSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := fixture.service.ProposePackageSkill(
		fixture.ctx, initialContent, []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, triggerErr := fixture.database.SQLDB().ExecContext(fixture.ctx, `CREATE TRIGGER pc_test_fail_package_revision
        BEFORE UPDATE OF version ON pc_artifact_candidate_heads
		BEGIN SELECT RAISE(ABORT, 'injected package Candidate revision failure'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	_, err = fixture.service.Revise(
		fixture.ctx, pending.ID(), pending.Version(), rollbackContent, []source.Ref{evidence}, nil, nil, nil,
	)
	if err == nil {
		t.Fatal("package revision unexpectedly survived Candidate failure")
	}
	assertPackageReviewRow(t, fixture, rollbackSnapshot, 0)
	assertPendingCandidate(t, fixture.service, pending.ID())
}

func TestUploadedPackageSkillCreatesOnePackageUploadSourceAndPendingCandidate(t *testing.T) {
	fixture := newReviewFixture(t, "package-upload", (&sequenceIDs{}).New)
	snapshot := skillPackageSnapshot(t, "uploaded-package")
	candidate, proposeErr := fixture.service.ProposeUploadedPackage(fixture.ctx, snapshot.Archive(), nil, nil, nil)
	if proposeErr != nil {
		t.Fatal(proposeErr)
	}
	if candidate.Family() != skill.Family || candidate.Status() != review.Pending || len(candidate.Sources()) != 1 || len(candidate.Artifacts()) != 0 {
		t.Fatalf("uploaded package Candidate = %#v", candidate)
	}
	uploadRef := candidate.Sources()[0]
	if uploadRef.Type() != source.SkillPackageUploadType || uploadRef.ID() != "skill_pkg_"+snapshot.Reference().TreeDigest() {
		t.Fatalf("upload evidence ref = %#v", uploadRef)
	}
	assertPackageReviewRow(t, fixture, snapshot, 1)
	var stored sqlstore.StoredSource
	if transactionErr := fixture.database.Transaction(fixture.ctx, func(tx sqlstore.DBTX) error {
		var getErr error
		stored, getErr = fixture.sources.Get(fixture.ctx, tx, fixture.scope, uploadRef)
		return getErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	upload, ok := stored.Value.(source.SkillPackageUploadSource)
	if !ok || upload.Capture().Package().TreeDigest() != snapshot.Reference().TreeDigest() ||
		upload.Capture().SkillName() != snapshot.Metadata().Name() || upload.Capture().SkillDescription() != snapshot.Metadata().Description() {
		t.Fatalf("stored package upload = %#v", stored.Value)
	}
	var payload []byte
	if queryErr := fixture.database.SQLDB().QueryRowContext(fixture.ctx, `SELECT payload FROM pc_sources
        WHERE scope_id = ? AND source_type = ? AND source_id = ?`, fixture.scope, uploadRef.Type(), uploadRef.ID()).Scan(&payload); queryErr != nil {
		t.Fatal(queryErr)
	}
	if bytes.Contains(payload, snapshot.Archive()) {
		t.Fatal("package upload Source duplicated archive bytes")
	}

	second, err := fixture.service.ProposeUploadedPackage(fixture.ctx, snapshot.Archive(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID() == candidate.ID() || second.Sources()[0] != uploadRef || countPackageUploadSources(t, fixture) != 1 {
		t.Fatalf("repeated package upload = %#v", second)
	}
}

func TestUploadedPackageSkillRollsBackPackageAndUploadSourceOnCandidateFailure(t *testing.T) {
	fixture := newReviewFixture(t, "package-upload-rollback", (&sequenceIDs{}).New)
	snapshot := skillPackageSnapshot(t, "uploaded-package-rollback")
	if _, err := fixture.database.SQLDB().ExecContext(fixture.ctx, `CREATE TRIGGER pc_test_fail_uploaded_package_candidate
        BEFORE INSERT ON pc_artifact_candidate_heads
        BEGIN SELECT RAISE(ABORT, 'injected package Candidate failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.ProposeUploadedPackage(fixture.ctx, snapshot.Archive(), nil, nil, nil); err == nil {
		t.Fatal("package upload survived Candidate failure")
	}
	assertPackageReviewRow(t, fixture, snapshot, 0)
	if count := countPackageUploadSources(t, fixture); count != 0 {
		t.Fatalf("rolled back package upload Sources = %d", count)
	}
}

func countPackageUploadSources(t *testing.T, fixture reviewFixture) int {
	t.Helper()
	var count int
	if err := fixture.database.SQLDB().QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM pc_sources
        WHERE scope_id = ? AND source_type = ?`, fixture.scope, source.SkillPackageUploadType).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestPackageSkillCandidateRevisionRejectsCrossVariant(t *testing.T) {
	fixture := newReviewFixture(t, "package-cross-variant", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "package-evidence", "reviewed package evidence")
	snapshot := skillPackageSnapshot(t, "package-cross-variant-skill")
	packageContent, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := fixture.service.ProposePackageSkill(
		fixture.ctx, packageContent, []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	legacy := reviewSkill(t, "Do not cross Skill content variants.")
	_, err = fixture.service.Revise(
		fixture.ctx, candidate.ID(), candidate.Version(), legacy, []source.Ref{evidence}, nil, nil, nil,
	)
	assertReviewInvalidField(t, err, "family")
	current, err := fixture.service.Get(fixture.ctx, candidate.ID())
	if err != nil {
		t.Fatal(err)
	}
	if current.Version() != candidate.Version() {
		t.Fatalf("cross-variant Candidate changed to version %d", current.Version())
	}
}

func TestPackageSkillApprovalRollsBackArtifactAndCandidateForEveryWriteFailure(t *testing.T) {
	tests := []struct {
		name       string
		triggerSQL string
	}{
		{
			name: "artifact insert",
			triggerSQL: `CREATE TRIGGER pc_test_fail_package_artifact
                BEFORE INSERT ON pc_artifacts
                BEGIN SELECT RAISE(ABORT, 'injected package Artifact failure'); END`,
		},
		{
			name: "source lineage insert",
			triggerSQL: `CREATE TRIGGER pc_test_fail_package_lineage
                BEFORE INSERT ON pc_artifact_lineage_sources
                BEGIN SELECT RAISE(ABORT, 'injected package lineage failure'); END`,
		},
		{
			name: "candidate terminal update",
			triggerSQL: `CREATE TRIGGER pc_test_fail_package_terminal
                BEFORE UPDATE OF status ON pc_artifact_candidate_heads
                WHEN NEW.status = 'approved'
                BEGIN SELECT RAISE(ABORT, 'injected package terminal failure'); END`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewFixture(t, "package-approval-rollback", (&sequenceIDs{}).New)
			evidence := fixture.capture(t, "package-evidence", "reviewed package evidence")
			snapshot := skillPackageSnapshot(t, "package-approval-rollback-skill")
			proposal, err := skill.NewPackageContent(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := fixture.service.ProposePackageSkill(
				fixture.ctx, proposal, []source.Ref{evidence}, nil, nil, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.database.SQLDB().ExecContext(fixture.ctx, test.triggerSQL); err != nil {
				t.Fatal(err)
			}

			if _, err := fixture.service.Approve(fixture.ctx, candidate.ID(), candidate.Version()); err == nil {
				t.Fatal("package approval unexpectedly survived injected write failure")
			}
			assertPendingCandidate(t, fixture.service, candidate.ID())
			assertArtifactCount(t, fixture.database, fixture.scope, skill.Family, 0)
			assertPackageReviewRow(t, fixture, snapshot, 1)
		})
	}
}

func TestPackageSkillApprovalRejectsCorruptCanonicalArchiveBeforeArtifactWrite(t *testing.T) {
	fixture := newReviewFixture(t, "package-approval-corrupt", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "package-evidence", "reviewed package evidence")
	snapshot := skillPackageSnapshot(t, "package-approval-corrupt-skill")
	proposal, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := fixture.service.ProposePackageSkill(
		fixture.ctx, proposal, []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, corruptErr := fixture.database.SQLDB().ExecContext(fixture.ctx, `UPDATE pc_skill_packages SET archive = ?
        WHERE scope_id = ? AND tree_digest = ?`, []byte("approval-corrupt-package-secret"), fixture.scope, snapshot.Reference().TreeDigest()); corruptErr != nil {
		t.Fatal(corruptErr)
	}

	_, err = fixture.service.Approve(fixture.ctx, candidate.ID(), candidate.Version())
	assertPackageReadFailureRedacted(t, err, fixture, snapshot, "approval-corrupt-package-secret")
	assertPendingCandidateHead(t, fixture.database, fixture.scope, candidate.ID())
	assertArtifactCount(t, fixture.database, fixture.scope, skill.Family, 0)
}

func TestPackageSkillArtifactReadRevalidatesCanonicalArchive(t *testing.T) {
	fixture := newReviewFixture(t, "package-artifact-corrupt", (&sequenceIDs{}).New)
	evidence := fixture.capture(t, "package-evidence", "reviewed package evidence")
	snapshot := skillPackageSnapshot(t, "package-artifact-corrupt-skill")
	proposal, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := fixture.service.ProposePackageSkill(
		fixture.ctx, proposal, []source.Ref{evidence}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := fixture.service.Approve(fixture.ctx, candidate.ID(), candidate.Version())
	if err != nil {
		t.Fatal(err)
	}
	ref := approved.ResultArtifact()
	if ref == nil {
		t.Fatal("approved package Skill has no Artifact")
	}
	if _, corruptErr := fixture.database.SQLDB().ExecContext(fixture.ctx, `UPDATE pc_skill_packages SET archive = ?
        WHERE scope_id = ? AND tree_digest = ?`, []byte("artifact-read-corrupt-package-secret"), fixture.scope, snapshot.Reference().TreeDigest()); corruptErr != nil {
		t.Fatal(corruptErr)
	}

	_, err = fixture.service.GetPackageSkill(fixture.ctx, *ref)
	assertPackageReadFailureRedacted(t, err, fixture, snapshot, "artifact-read-corrupt-package-secret")
}

func TestPackageSkillReadRejectsMissingOrCorruptCanonicalPackageWithoutLeakingIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, reviewFixture, skill.PackageSnapshot, string)
	}{
		{
			name: "scope mismatch",
			mutate: func(t *testing.T, fixture reviewFixture, snapshot skill.PackageSnapshot, candidateID string) {
				t.Helper()
				var proposal, sourceRefs, artifactRefs []byte
				if err := fixture.database.SQLDB().QueryRowContext(fixture.ctx, `SELECT proposal, source_refs, artifact_refs
                    FROM pc_artifact_candidate_versions WHERE scope_id = ? AND candidate_id = ?`, fixture.scope, candidateID).Scan(
					&proposal, &sourceRefs, &artifactRefs,
				); err != nil {
					t.Fatal(err)
				}
				const otherScope = "package-other-scope-secret"
				if _, err := fixture.database.SQLDB().ExecContext(fixture.ctx, `INSERT INTO pc_artifact_candidate_versions
                    (scope_id, candidate_id, version, family, proposal, source_refs, artifact_refs, reason)
                    VALUES (?, ?, 1, ?, ?, ?, ?, NULL)`, otherScope, "package-mismatched-candidate", skill.Family,
					proposal, sourceRefs, artifactRefs,
				); err != nil {
					t.Fatal(err)
				}
				if _, err := fixture.database.SQLDB().ExecContext(fixture.ctx, `INSERT INTO pc_artifact_candidate_heads
                    (scope_id, candidate_id, family, version, status)
                    VALUES (?, ?, ?, 1, 'pending')`, otherScope, "package-mismatched-candidate", skill.Family); err != nil {
					t.Fatal(err)
				}
				candidateRepository := reviewCandidateRepository(t)
				err := fixture.database.Transaction(fixture.ctx, func(tx sqlstore.DBTX) error {
					_, getErr := candidateRepository.Get(fixture.ctx, tx, otherScope, "package-mismatched-candidate")
					return getErr
				})
				assertPackageReadFailureRedacted(t, err, fixture, snapshot, otherScope)
			},
		},
		{
			name: "corrupt archive",
			mutate: func(t *testing.T, fixture reviewFixture, snapshot skill.PackageSnapshot, candidateID string) {
				t.Helper()
				if _, err := fixture.database.SQLDB().ExecContext(fixture.ctx, `UPDATE pc_skill_packages SET archive = ?
                    WHERE scope_id = ? AND tree_digest = ?`, []byte("stored-package-secret"), fixture.scope, snapshot.Reference().TreeDigest()); err != nil {
					t.Fatal(err)
				}
				_, err := fixture.service.Get(fixture.ctx, candidateID)
				assertPackageReadFailureRedacted(t, err, fixture, snapshot, "stored-package-secret")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewFixture(t, "package-read-secret", (&sequenceIDs{}).New)
			evidence := fixture.capture(t, "package-evidence", "reviewed package evidence")
			snapshot := skillPackageSnapshot(t, "package-read-skill")
			proposal, err := skill.NewPackageContent(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := fixture.service.ProposePackageSkill(
				fixture.ctx, proposal, []source.Ref{evidence}, nil, nil, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture, snapshot, candidate.ID())
		})
	}
}

func assertPackageReadFailureRedacted(
	t *testing.T,
	err error,
	fixture reviewFixture,
	snapshot skill.PackageSnapshot,
	extraSecret string,
) {
	t.Helper()
	if _, invalid := errors.AsType[*sqlstore.InvalidStoredPayloadError](err); !invalid {
		t.Fatalf("package read error = %T %v, want InvalidStoredPayloadError", err, err)
	}
	for _, secret := range []string{fixture.scope, snapshot.Metadata().Name(), snapshot.Reference().TreeDigest(), snapshot.Reference().ArchiveDigest(), extraSecret} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("package read error leaked %q: %v", secret, err)
		}
	}
}

func assertPendingCandidateHead(t *testing.T, database *sqlstore.Database, scopeID, candidateID string) {
	t.Helper()
	var status string
	if err := database.SQLDB().QueryRowContext(t.Context(), `SELECT status
        FROM pc_artifact_candidate_heads WHERE scope_id = ? AND candidate_id = ?`, scopeID, candidateID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != string(review.Pending) {
		t.Fatalf("Candidate head status = %q, want pending", status)
	}
}

func assertPackageReviewPayload(t *testing.T, fixture reviewFixture, candidateID, column string, snapshot skill.PackageSnapshot) {
	t.Helper()
	var payload []byte
	query := fmt.Sprintf(`SELECT %s FROM pc_artifact_candidate_versions WHERE scope_id = ? AND candidate_id = ?`, column)
	if err := fixture.database.SQLDB().QueryRowContext(fixture.ctx, query, fixture.scope, candidateID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	assertPackageReferencePayload(t, payload, snapshot)
}

func assertPackageArtifactPayload(t *testing.T, fixture reviewFixture, ref artifact.Ref, snapshot skill.PackageSnapshot) {
	t.Helper()
	var payload []byte
	if err := fixture.database.SQLDB().QueryRowContext(fixture.ctx, `SELECT content FROM pc_artifacts
        WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = ?`,
		fixture.scope, ref.Family(), ref.ID(), ref.Revision()).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	assertPackageReferencePayload(t, payload, snapshot)
}

func assertPackageReferencePayload(t *testing.T, payload []byte, snapshot skill.PackageSnapshot) {
	t.Helper()
	ref := snapshot.Reference()
	want := fmt.Sprintf(`{"package_ref":{"tree_digest":"%s","archive_digest":"%s","file_count":%d,"uncompressed_size":%d,"archive_size":%d}}`,
		ref.TreeDigest(), ref.ArchiveDigest(), ref.FileCount(), ref.UncompressedSize(), ref.ArchiveSize())
	if string(payload) != want {
		t.Fatalf("package reference payload = %s, want %s", payload, want)
	}
	for _, forbidden := range [][]byte{snapshot.Archive(), snapshot.Manifest(), []byte(snapshot.Instructions())} {
		if bytes.Contains(payload, forbidden) {
			t.Fatalf("package reference payload duplicated package bytes: %q", payload)
		}
	}
}

func assertPackageReviewRow(t *testing.T, fixture reviewFixture, snapshot skill.PackageSnapshot, want int) {
	t.Helper()
	var count int
	if err := fixture.database.SQLDB().QueryRowContext(fixture.ctx, `SELECT COUNT(*) FROM pc_skill_packages
        WHERE scope_id = ? AND tree_digest = ?`, fixture.scope, snapshot.Reference().TreeDigest()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("stored package rows = %d, want %d", count, want)
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
