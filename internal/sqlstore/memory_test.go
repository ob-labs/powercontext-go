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
	"reflect"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestMemoryRepositoryCommitsAuthorityAndProjectionAtomically(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	_, artifacts := repositories(t)
	repository, err := sqlstore.NewMemoryRepository(database, "scope-memory", artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	commit := initialMemoryCommit(t, "memory-1")
	committed, err := repository.Commit(ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	if committed.Ref() != commit.Memory().Ref() {
		t.Fatalf("committed ref = %s", committed.Ref())
	}

	loaded, err := repository.Get(ctx, committed.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, committed) {
		t.Fatal("loaded Memory differs from committed authority")
	}
	entries, err := repository.Entries(ctx, committed.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].EntryID != "entry-1" {
		t.Fatalf("entries = %#v", entries)
	}
	projections, err := repository.Projections(ctx, committed.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 1 || projections[0].SearchableText() != memory.AnalyzeText("Remember this") {
		t.Fatalf("projections = %#v", projections)
	}

	var sourceRefs, artifactRefs []byte
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT source_refs, artifact_refs
        FROM pc_memory_entry_versions WHERE scope_id = ? AND memory_artifact_id = ? AND entry_id = ?`,
		"scope-memory", "memory-1", "entry-1").Scan(&sourceRefs, &artifactRefs); err != nil {
		t.Fatal(err)
	}
	if string(sourceRefs) != "[]" || string(artifactRefs) != "[]" {
		t.Fatalf("reference payloads source=%s artifact=%s", sourceRefs, artifactRefs)
	}
}

func TestMemoryRepositoryRollsBackArtifactWhenIndexReplaceFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	_, artifacts := repositories(t)
	repository, err := sqlstore.NewMemoryRepository(
		database,
		"scope-memory",
		artifacts,
		failingMemoryIndex{failure: errors.New("projection failed")},
	)
	if err != nil {
		t.Fatal(err)
	}
	commit := initialMemoryCommit(t, "memory-rollback")
	if _, err := repository.Commit(ctx, commit); err == nil || err.Error() != "projection failed" {
		t.Fatalf("commit error = %v", err)
	}
	var count int
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pc_artifacts
        WHERE scope_id = ? AND family = ? AND artifact_id = ?`,
		"scope-memory", memory.Family, "memory-rollback").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back Artifact rows = %d", count)
	}
}

func TestMemoryRepositoryRejectsProjectionManifestMismatchBeforeWriting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	_, artifacts := repositories(t)
	repository, err := sqlstore.NewMemoryRepository(database, "scope-memory", artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := initialMemoryCommit(t, "memory-invalid")
	invalid := memory.NewCommit(valid.Base(), valid.Memory(), valid.ContentHash(), valid.EntryVersions(), nil)
	_, err = repository.Commit(ctx, invalid)
	var configuration *memory.BackendConfigurationError
	if !errors.As(err, &configuration) {
		t.Fatalf("expected configuration error, got %v", err)
	}
}

func TestMemoryRepositoriesAreScopeIsolated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	_, artifacts := repositories(t)
	first, err := sqlstore.NewMemoryRepository(database, "scope-one", artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sqlstore.NewMemoryRepository(database, "scope-two", artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := first.Commit(ctx, initialMemoryCommit(t, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = second.Get(ctx, committed.Ref())
	var missing *artifact.NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("cross-scope Get error = %v", err)
	}
}

func initialMemoryCommit(t *testing.T, artifactID string) memory.Commit {
	t.Helper()
	contentHash, err := memory.EntryContentHash("fact", "Remember this", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := memory.EntryVersion{
		MemoryArtifactID:  artifactID,
		EntryID:           "entry-1",
		EntryVersionID:    "version-1",
		Version:           1,
		Kind:              "fact",
		Text:              "Remember this",
		EntryContentHash:  contentHash,
		CreatedInRevision: 1,
	}
	manifestEntry, err := memory.NewManifestEntry("entry-1", "version-1", contentHash, memory.Active)
	if err != nil {
		t.Fatal(err)
	}
	versionID := "version-1"
	change, err := memory.NewChange(memory.Add, "entry-1", nil, &versionID, nil)
	if err != nil {
		t.Fatal(err)
	}
	content := memory.NewContent(memory.NewManifest([]memory.ManifestEntry{manifestEntry}), []memory.Change{change})
	draft, err := memory.NewDraft(content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	next, err := artifact.New(artifactID, 1, draft)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := memory.NewProjection(entry, memory.AnalyzeText(entry.Text), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	memoryHash, err := memory.ContentHash(content)
	if err != nil {
		t.Fatal(err)
	}
	return memory.NewCommit(nil, next, memoryHash, []memory.EntryVersion{entry}, []memory.Projection{projection})
}

type failingMemoryIndex struct{ failure error }

func (f failingMemoryIndex) Capabilities() memory.Capabilities { return memory.Capabilities{FTS: true} }

func (f failingMemoryIndex) Initialize(context.Context, sqlstore.DBTX) error {
	return nil
}

func (f failingMemoryIndex) Replace(
	context.Context,
	sqlstore.DBTX,
	string,
	artifact.Ref,
	[]memory.Projection,
) error {
	return f.failure
}

func (f failingMemoryIndex) Search(
	context.Context,
	sqlstore.DBTX,
	string,
	memory.SearchRequest,
) (memory.SearchChannels, error) {
	return memory.SearchChannels{}, f.failure
}

func (f failingMemoryIndex) VectorComplete(
	context.Context,
	sqlstore.DBTX,
	string,
	[]artifact.Ref,
	memory.EmbeddingProfile,
) (bool, error) {
	return false, f.failure
}

func (f failingMemoryIndex) Hydrate(
	_ context.Context,
	_ sqlstore.DBTX,
	_ string,
	projections []memory.Projection,
) ([]memory.Projection, error) {
	return projections, nil
}
