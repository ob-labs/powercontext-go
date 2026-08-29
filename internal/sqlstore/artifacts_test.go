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
	"sync"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestArtifactRepositoryPreservesPayloadRevisionAndOrderedLineage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	sources, artifacts := repositories(t)
	evidence := contentSource(t, "evidence", "source evidence", nil)
	var storedSource sqlstore.StoredSource
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var err error
		storedSource, err = sources.Add(ctx, tx, "scope-a", evidence)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	skillContent, err := skill.NewContent("check", "Check work", "Run the checks.", []string{"tests pass"})
	if err != nil {
		t.Fatal(err)
	}
	skillDraft, err := skill.NewDraft(skillContent, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var upstream artifact.Snapshot
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var createErr error
		upstream, createErr = artifacts.Create(ctx, tx, "scope-a", "skill-1", skillDraft)
		return createErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}

	firstContent, err := experience.NewContent("situation", "action", "outcome", "lesson")
	if err != nil {
		t.Fatal(err)
	}
	firstDraft, err := experience.NewDraft(
		firstContent,
		[]source.Ref{storedSource.Ref},
		[]artifact.Ref{upstream.Ref()},
	)
	if err != nil {
		t.Fatal(err)
	}
	var first artifact.Snapshot
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var createErr error
		first, createErr = artifacts.Create(ctx, tx, "scope-a", "experience-1", firstDraft)
		return createErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if first.Ref().Revision() != 1 {
		t.Fatalf("revision = %d", first.Ref().Revision())
	}
	var payload []byte
	if queryErr := database.SQLDB().QueryRowContext(ctx, `SELECT content FROM pc_artifacts
        WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = 1`,
		"scope-a", experience.Family, "experience-1").Scan(&payload); queryErr != nil {
		t.Fatal(queryErr)
	}
	want := `{"situation":"situation","action":"action","outcome":"outcome","lesson":"lesson"}`
	if string(payload) != want {
		t.Fatalf("payload mismatch\n got: %s\nwant: %s", payload, want)
	}

	secondContent, err := experience.NewContent("situation 2", "action 2", "outcome 2", "lesson 2")
	if err != nil {
		t.Fatal(err)
	}
	secondDraft, err := experience.NewDraft(secondContent, []source.Ref{storedSource.Ref}, []artifact.Ref{first.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	var second artifact.Snapshot
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var reviseErr error
		second, reviseErr = artifacts.Revise(ctx, tx, "scope-a", first, secondDraft)
		return reviseErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if second.Ref().Revision() != 2 {
		t.Fatalf("revision = %d", second.Ref().Revision())
	}

	var loaded artifact.Snapshot
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var getErr error
		loaded, getErr = artifacts.Get(ctx, tx, "scope-a", second.Ref())
		return getErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	lineage := loaded.(artifact.Artifact[experience.Content]).Lineage()
	if got := lineage.Sources(); len(got) != 1 || got[0] != storedSource.Ref {
		t.Fatalf("source lineage = %#v", got)
	}
	if got := lineage.Artifacts(); len(got) != 1 || got[0] != first.Ref() {
		t.Fatalf("artifact lineage = %#v", got)
	}

	err = database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, reviseErr := artifacts.Revise(ctx, tx, "scope-a", first, secondDraft)
		return reviseErr
	})
	var conflict *artifact.RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Current != second.Ref() {
		t.Fatalf("expected current revision conflict, got %v", err)
	}
	var revisions []artifact.Snapshot
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var listErr error
		revisions, listErr = artifacts.Revisions(ctx, tx, "scope-a", experience.Family, "experience-1")
		return listErr
	}); err != nil {
		t.Fatal(err)
	}
	if len(revisions) != 2 {
		t.Fatalf("revisions = %d", len(revisions))
	}
}

func TestArtifactRepositoryConcurrentCASHasOneWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	_, repository := repositories(t)
	content, err := experience.NewContent("s", "a", "o", "l")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := experience.NewDraft(content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var current artifact.Snapshot
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var createErr error
		current, createErr = repository.Create(ctx, tx, "scope-cas", "experience-1", draft)
		return createErr
	}); err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	results := make(chan error, 2)
	for index := range 2 {
		group.Go(func() {
			value, createErr := experience.NewContent("s", "a", "o", string(rune('1'+index)))
			if createErr != nil {
				results <- createErr
				return
			}
			next, createErr := experience.NewDraft(value, nil, nil)
			if createErr != nil {
				results <- createErr
				return
			}
			results <- database.Transaction(ctx, func(tx sqlstore.DBTX) error {
				_, reviseErr := repository.Revise(ctx, tx, "scope-cas", current, next)
				return reviseErr
			})
		})
	}
	group.Wait()
	close(results)
	winners, conflicts := 0, 0
	for result := range results {
		if result == nil {
			winners++
			continue
		}
		var conflict *artifact.RevisionConflictError
		if errors.As(result, &conflict) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected CAS result: %v", result)
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
}

func TestArtifactLineageAndHeadForeignKeysAreEnforced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	if _, err := database.SQLDB().ExecContext(ctx, `INSERT INTO pc_artifacts
        (scope_id, family, artifact_id, revision, content) VALUES (?, ?, ?, ?, ?)`,
		"scope-fk", "experience", "experience-1", 1, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `INSERT INTO pc_artifact_lineage_sources
                (scope_id, family, artifact_id, revision, ordinal, source_type, source_id)
                VALUES (?, ?, ?, ?, ?, ?, ?)`,
			args: []any{"scope-fk", "experience", "experience-1", 1, 0, "content", "missing"},
		},
		{
			query: `INSERT INTO pc_artifact_lineage_artifacts
                (scope_id, family, artifact_id, revision, ordinal, upstream_family, upstream_artifact_id, upstream_revision)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			args: []any{"scope-fk", "experience", "experience-1", 1, 0, "skill", "missing", 1},
		},
		{
			query: `INSERT INTO pc_artifact_heads
                (scope_id, family, artifact_id, revision) VALUES (?, ?, ?, ?)`,
			args: []any{"scope-fk", "experience", "missing", 1},
		},
	}
	for index, statement := range statements {
		if _, err := database.SQLDB().ExecContext(ctx, statement.query, statement.args...); err == nil {
			t.Fatalf("foreign-key violation %d was accepted", index)
		}
	}
}

func TestMemoryAndHandoffPayloadsMatchPythonFieldOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	sources, repository := repositories(t)
	evidence := contentSource(t, "evidence", "content", nil)
	var stored sqlstore.StoredSource
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var addErr error
		stored, addErr = sources.Add(ctx, tx, "scope-a", evidence)
		return addErr
	}); err != nil {
		t.Fatal(err)
	}

	memoryDraft, err := memory.NewDraft(memory.NewContent(memory.NewManifest(nil), nil), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, createErr := repository.Create(ctx, tx, "scope-a", "memory-1", memoryDraft)
		return createErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	assertArtifactPayload(t, database, "memory", "memory-1",
		`{"manifest":{"entries":[],"format":"flat-v1"},"changes":[],"schema":"powercontext.memory.v1"}`)

	citation, err := handoff.NewSourceCitation(stored.Ref)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := handoff.NewStatement("Ready", []handoff.Citation{citation})
	if err != nil {
		t.Fatal(err)
	}
	handoffContent, err := handoff.NewContent("Ship", []handoff.Statement{statement}, handoff.Continuable, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handoffDraft, err := handoff.NewArtifactDraft(handoffContent, []source.Ref{stored.Ref}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, createErr := repository.Create(ctx, tx, "scope-a", "handoff-1", handoffDraft)
		return createErr
	}); err != nil {
		t.Fatal(err)
	}
	assertArtifactPayload(t, database, "handoff", "handoff-1",
		`{"schema":"powercontext.handoff.v1","objective":"Ship","state":[{"text":"Ready","citations":[{"kind":"source","source_ref":{"source_type":"content","source_id":"evidence"}}]}],"disposition":"continuable","next_action":null,"omissions":[]}`)
}

func repositories(t *testing.T) (*sqlstore.SourceRepository, *sqlstore.ArtifactRepository) {
	t.Helper()
	sources, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := sqlstore.NewArtifactRepository(
		sqlstore.SQLiteDialect,
		sqlstore.ExperienceArtifactCodec(),
		sqlstore.SkillArtifactCodec(),
		sqlstore.MemoryArtifactCodec(),
		sqlstore.HandoffArtifactCodec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return sources, artifacts
}

func assertArtifactPayload(t *testing.T, database *sqlstore.Database, family, id, want string) {
	t.Helper()
	var payload []byte
	if err := database.SQLDB().QueryRowContext(context.Background(), `SELECT content FROM pc_artifacts
        WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = 1`,
		"scope-a", family, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != want {
		t.Fatalf("%s payload mismatch\n got: %s\nwant: %s", family, payload, want)
	}
}
