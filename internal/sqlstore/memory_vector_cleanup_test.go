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
	"fmt"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestMemoryRepositoryRevisionRemovesSQLiteVecOrphans(t *testing.T) {
	ctx := t.Context()
	database := openTestDatabase(t)
	profile, index := newMemoryVectorCleanupIndex(t)
	artifacts, err := sqlstore.NewArtifactRepository(sqlstore.SQLiteDialect, sqlstore.MemoryArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	first, err := sqlstore.NewMemoryRepository(database, "scope-one", artifacts, index)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sqlstore.NewMemoryRepository(database, "scope-two", artifacts, index)
	if err != nil {
		t.Fatal(err)
	}
	if initializeErr := first.Initialize(ctx); initializeErr != nil {
		t.Fatal(initializeErr)
	}

	firstRevision, firstCommitErr := first.Commit(ctx, memoryVectorCleanupCommit(t, nil, "memory-1", "entry-one", "first", []float64{1, 0, 0}, profile))
	if firstCommitErr != nil {
		t.Fatal(firstCommitErr)
	}
	if _, secondCommitErr := second.Commit(ctx, memoryVectorCleanupCommit(t, nil, "memory-1", "entry-two", "second", []float64{0, 1, 0}, profile)); secondCommitErr != nil {
		t.Fatal(secondCommitErr)
	}
	if _, revisionCommitErr := first.Commit(ctx, memoryVectorCleanupCommit(t, &firstRevision, "memory-1", "entry-one", "revised", []float64{0, 0, 1}, profile)); revisionCommitErr != nil {
		t.Fatal(revisionCommitErr)
	}

	assertMemoryVectorCleanupState(t, database, 0, map[string]int{
		"scope-one": 1,
		"scope-two": 1,
	})
}

func TestSQLiteVecInitializeRemovesPreexistingOrphans(t *testing.T) {
	ctx := t.Context()
	database := openTestDatabase(t)
	profile, index := newMemoryVectorCleanupIndex(t)
	artifacts, err := sqlstore.NewArtifactRepository(sqlstore.SQLiteDialect, sqlstore.MemoryArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlstore.NewMemoryRepository(database, "scope-initialize", artifacts, index)
	if err != nil {
		t.Fatal(err)
	}
	if initializeErr := repository.Initialize(ctx); initializeErr != nil {
		t.Fatal(initializeErr)
	}
	if _, commitErr := repository.Commit(ctx, memoryVectorCleanupCommit(t, nil, "memory-1", "entry-one", "current", []float64{1, 0, 0}, profile)); commitErr != nil {
		t.Fatal(commitErr)
	}
	var packed []byte
	if queryErr := database.SQLDB().QueryRowContext(ctx,
		"SELECT embedding FROM pc_memory_entry_vec LIMIT 1",
	).Scan(&packed); queryErr != nil {
		t.Fatal(queryErr)
	}
	if _, insertErr := database.SQLDB().ExecContext(ctx,
		"INSERT INTO pc_memory_entry_vec (rowid, embedding) VALUES (?, ?)", -2, packed,
	); insertErr != nil {
		t.Fatal(insertErr)
	}

	if initializeErr := repository.Initialize(ctx); initializeErr != nil {
		t.Fatal(initializeErr)
	}
	assertMemoryVectorCleanupState(t, database, 0, map[string]int{"scope-initialize": 1})
}

func TestMemoryRepositoryVectorReplacementRollsBackProjectionCleanup(t *testing.T) {
	ctx := t.Context()
	database := openTestDatabase(t)
	profile, index := newMemoryVectorCleanupIndex(t)
	artifacts, err := sqlstore.NewArtifactRepository(sqlstore.SQLiteDialect, sqlstore.MemoryArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlstore.NewMemoryRepository(database, "scope-rollback", artifacts, index)
	if err != nil {
		t.Fatal(err)
	}
	if initializeErr := repository.Initialize(ctx); initializeErr != nil {
		t.Fatal(initializeErr)
	}
	first, firstCommitErr := repository.Commit(ctx, memoryVectorCleanupCommit(t, nil, "memory-1", "entry-one", "current", []float64{1, 0, 0}, profile))
	if firstCommitErr != nil {
		t.Fatal(firstCommitErr)
	}
	if _, invalidCommitErr := repository.Commit(ctx, memoryVectorCleanupCommit(t, &first, "memory-1", "entry-one", "invalid", []float64{1, 0}, profile)); invalidCommitErr == nil {
		t.Fatal("invalid embedding dimension committed")
	}

	assertMemoryVectorCleanupState(t, database, 0, map[string]int{"scope-rollback": 1})
	var revision int64
	if queryErr := database.SQLDB().QueryRowContext(ctx, `SELECT head_revision
        FROM pc_memory_entry_heads
        WHERE scope_id = ? AND memory_artifact_id = ? AND entry_id = ?`,
		"scope-rollback", "memory-1", "entry-one",
	).Scan(&revision); queryErr != nil {
		t.Fatal(queryErr)
	}
	if revision != first.Revision() {
		t.Fatalf("rolled-back head revision = %d, want %d", revision, first.Revision())
	}
}

func newMemoryVectorCleanupIndex(t *testing.T) (memory.EmbeddingProfile, *sqlstore.SQLiteMemoryVectorIndex) {
	t.Helper()
	profile, err := memory.NewEmbeddingProfile("profile", "model", 3, "unit")
	if err != nil {
		t.Fatal(err)
	}
	index, err := sqlstore.NewSQLiteMemoryVectorIndex(profile)
	if err != nil {
		t.Fatal(err)
	}
	return profile, index
}

func memoryVectorCleanupCommit(
	t *testing.T,
	base *memory.Memory,
	memoryID, entryID, text string,
	vector []float64,
	profile memory.EmbeddingProfile,
) memory.Commit {
	t.Helper()
	revision := int64(1)
	if base != nil {
		revision = base.Revision() + 1
	}
	entryVersionID := fmt.Sprintf("%s-v%d", entryID, revision)
	contentHash, err := memory.EntryContentHash("fact", text, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := memory.EntryVersion{
		MemoryArtifactID:  memoryID,
		EntryID:           entryID,
		EntryVersionID:    entryVersionID,
		Version:           revision,
		Kind:              "fact",
		Text:              text,
		EntryContentHash:  contentHash,
		CreatedInRevision: revision,
	}
	var change memory.Change
	if base == nil {
		change, err = memory.NewChange(memory.Add, entryID, nil, &entryVersionID, nil)
	} else {
		previous := base.Content().Manifest().Entries()[0].EntryVersionID()
		entry.PreviousVersionID = &previous
		change, err = memory.NewChange(memory.Revise, entryID, &previous, &entryVersionID, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	manifestEntry, err := memory.NewManifestEntry(entryID, entryVersionID, contentHash, memory.Active)
	if err != nil {
		t.Fatal(err)
	}
	content := memory.NewContent(memory.NewManifest([]memory.ManifestEntry{manifestEntry}), []memory.Change{change})
	draft, err := memory.NewDraft(content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := artifact.New(memoryID, revision, draft)
	if err != nil {
		t.Fatal(err)
	}
	embeddingHash, err := memory.EmbeddingContentHash(profile, contentHash)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := memory.NewProjection(entry, memory.AnalyzeText(text), vector, &embeddingHash)
	if err != nil {
		t.Fatal(err)
	}
	memoryHash, err := memory.ContentHash(content)
	if err != nil {
		t.Fatal(err)
	}
	return memory.NewCommit(base, next, memoryHash, []memory.EntryVersion{entry}, []memory.Projection{projection})
}

func assertMemoryVectorCleanupState(t *testing.T, database *sqlstore.Database, wantOrphans int, wantScopes map[string]int) {
	t.Helper()
	ctx := t.Context()
	var orphanCount int
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*)
        FROM pc_memory_entry_vec
        WHERE rowid NOT IN (SELECT vector_id FROM pc_memory_vector_entries)`).Scan(&orphanCount); err != nil {
		t.Fatal(err)
	}
	if orphanCount != wantOrphans {
		t.Fatalf("orphan vec0 rows = %d, want %d", orphanCount, wantOrphans)
	}
	for scopeID, want := range wantScopes {
		var got int
		if err := database.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pc_memory_vector_entries
            WHERE scope_id = ?`, scopeID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("scope %q vector metadata rows = %d, want %d", scopeID, got, want)
		}
	}
}
