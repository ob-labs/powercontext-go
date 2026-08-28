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
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestMemoryServiceAppendReviseAndStateTransitionsUseAtomicRepository(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	_, artifacts := repositories(t)
	repository, err := sqlstore.NewMemoryRepository(database, "scope-service", artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := memory.NewService(repository, memory.ServiceOptions{IDFactory: sequentialMemoryIDs()})
	if err != nil {
		t.Fatal(err)
	}

	firstInput := memory.NewEntryInput(nil, " fact ", " Remember this. ", nil, nil, nil)
	first, err := service.Remember(ctx, nil, nil, nil, []memory.EntryInput{firstInput}, memory.RememberAppend)
	if err != nil {
		t.Fatal(err)
	}
	if first == nil || first.Ref().String() != "memory:mem_art_1@1" {
		t.Fatalf("first Memory = %#v", first)
	}
	entries, err := service.Entries(ctx, *first)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Kind != "fact" || entries[0].Text != "Remember this." {
		t.Fatalf("entries = %#v", entries)
	}

	// An exact duplicate is a no-op and must not allocate a Revision.
	duplicate, err := service.Remember(ctx, first, nil, nil, []memory.EntryInput{firstInput}, memory.RememberAppend)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate == nil || duplicate.Ref() != first.Ref() {
		t.Fatalf("duplicate result = %#v", duplicate)
	}

	reason := "new observation"
	revisionInput := memory.NewEntryInput(&entries[0], "fact", "Remember this exactly.", nil, nil, &reason)
	second, err := service.Remember(ctx, first, nil, nil, []memory.EntryInput{revisionInput}, memory.RememberAppend)
	if err != nil {
		t.Fatal(err)
	}
	if second == nil || second.Revision() != 2 {
		t.Fatalf("second Memory = %#v", second)
	}
	secondEntries, err := service.Entries(ctx, *second)
	if err != nil {
		t.Fatal(err)
	}
	if secondEntries[0].Version != 2 || secondEntries[0].PreviousVersionID == nil ||
		*secondEntries[0].PreviousVersionID != entries[0].EntryVersionID {
		t.Fatalf("revised entry = %#v", secondEntries[0])
	}

	paused := "paused"
	forgotten, err := service.Forget(ctx, *second, secondEntries, &paused)
	if err != nil {
		t.Fatal(err)
	}
	forgottenManifest := forgotten.Content().Manifest().Entries()
	forgottenChanges := forgotten.Content().Changes()
	if forgotten.Revision() != 3 || forgottenManifest[0].State() != memory.Inactive ||
		forgottenManifest[0].EntryVersionID() != secondEntries[0].EntryVersionID ||
		len(forgottenChanges) != 1 || forgottenChanges[0].Op() != memory.Deactivate ||
		forgottenChanges[0].Reason() == nil || *forgottenChanges[0].Reason() != paused {
		t.Fatalf("forgotten Memory = %#v", forgotten)
	}
	resumed := "resumed"
	reactivated, err := service.Reactivate(ctx, forgotten, secondEntries, &resumed)
	if err != nil {
		t.Fatal(err)
	}
	reactivatedManifest := reactivated.Content().Manifest().Entries()
	reactivatedChanges := reactivated.Content().Changes()
	if reactivated.Revision() != 4 || reactivatedManifest[0].State() != memory.Active ||
		reactivatedManifest[0].EntryVersionID() != secondEntries[0].EntryVersionID ||
		len(reactivatedChanges) != 1 || reactivatedChanges[0].Op() != memory.Reactivate ||
		reactivatedChanges[0].Reason() == nil || *reactivatedChanges[0].Reason() != resumed {
		t.Fatalf("reactivated Memory = %#v", reactivated)
	}
	reactivatedEntries, err := service.Entries(ctx, reactivated)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reactivatedEntries, secondEntries) {
		t.Fatalf("state transition rewrote immutable entry versions: got %#v, want %#v", reactivatedEntries, secondEntries)
	}

	var revisions int
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pc_artifacts
        WHERE scope_id = ? AND family = ? AND artifact_id = ?`,
		"scope-service", memory.Family, first.ID()).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 4 {
		t.Fatalf("stored revisions = %d", revisions)
	}
}

func TestMemoryOrganizeDeduplicatesAndNormalizesExistingEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	_, artifacts := repositories(t)
	repository, err := sqlstore.NewMemoryRepository(database, "scope-organize", artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	contentHash, err := memory.EntryContentHash("fact", "Duplicate.", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	versions := []memory.EntryVersion{
		{
			MemoryArtifactID: "memory", EntryID: "entry-a", EntryVersionID: "entry-a-v1",
			Version: 1, Kind: " fact ", Text: "  Duplicate.  ", EntryContentHash: contentHash,
			CreatedInRevision: 1,
		},
		{
			MemoryArtifactID: "memory", EntryID: "entry-b", EntryVersionID: "entry-b-v1",
			Version: 1, Kind: " fact ", Text: "  Duplicate.  ", EntryContentHash: contentHash,
			CreatedInRevision: 1,
		},
	}
	manifestEntries := make([]memory.ManifestEntry, len(versions))
	projections := make([]memory.Projection, len(versions))
	for index, version := range versions {
		manifestEntries[index], err = memory.NewManifestEntry(
			version.EntryID, version.EntryVersionID, version.EntryContentHash, memory.Active,
		)
		if err != nil {
			t.Fatal(err)
		}
		projections[index], err = memory.NewProjection(version, "duplicate.", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	content := memory.NewContent(memory.NewManifest(manifestEntries), nil)
	draft, err := memory.NewDraft(content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := artifact.New("memory", 1, draft)
	if err != nil {
		t.Fatal(err)
	}
	memoryHash, err := memory.ContentHash(content)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := repository.Commit(ctx, memory.NewCommit(nil, value, memoryHash, versions, projections))
	if err != nil {
		t.Fatal(err)
	}
	service, err := memory.NewService(repository, memory.ServiceOptions{IDFactory: sequentialMemoryIDs()})
	if err != nil {
		t.Fatal(err)
	}

	organized, err := service.Organize(ctx, stored, memory.OrganizeDefault)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := service.Entries(ctx, organized)
	if err != nil {
		t.Fatal(err)
	}
	manifest := organized.Content().Manifest().Entries()
	changes := organized.Content().Changes()
	if organized.Revision() != 2 || len(manifest) != 2 || manifest[0].State() != memory.Active ||
		manifest[1].State() != memory.Inactive || len(changes) != 2 ||
		changes[0].Op() != memory.Revise || changes[1].Op() != memory.Deactivate {
		t.Fatalf("organized Memory = %#v", organized)
	}
	if len(entries) != 2 {
		t.Fatalf("organized entries = %#v", entries)
	}
	unchanged := entries[1]
	if entries[0].Kind != "fact" || entries[0].Text != "Duplicate." ||
		entries[0].Version != 2 || entries[0].PreviousVersionID == nil ||
		*entries[0].PreviousVersionID != "entry-a-v1" ||
		unchanged.MemoryArtifactID != versions[1].MemoryArtifactID || unchanged.EntryID != versions[1].EntryID ||
		unchanged.EntryVersionID != versions[1].EntryVersionID || unchanged.Version != versions[1].Version ||
		unchanged.PreviousVersionID != nil || unchanged.Kind != versions[1].Kind || unchanged.Text != versions[1].Text ||
		unchanged.EntryContentHash != versions[1].EntryContentHash ||
		unchanged.CreatedInRevision != versions[1].CreatedInRevision ||
		!slices.Equal(unchanged.Sources, versions[1].Sources) || !slices.Equal(unchanged.Artifacts, versions[1].Artifacts) {
		t.Fatalf("organized entries = %#v", entries)
	}
}

func TestMemoryServiceRejectsStaleBaseBeforePreparingAWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	_, artifacts := repositories(t)
	repository, _ := sqlstore.NewMemoryRepository(database, "scope-cas-service", artifacts, nil)
	service, _ := memory.NewService(repository, memory.ServiceOptions{IDFactory: sequentialMemoryIDs()})
	input := memory.NewEntryInput(nil, "fact", "one", nil, nil, nil)
	first, err := service.Remember(ctx, nil, nil, nil, []memory.EntryInput{input}, memory.RememberAppend)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := service.Entries(ctx, *first)
	revision := memory.NewEntryInput(&entries[0], "fact", "two", nil, nil, nil)
	if _, rememberErr := service.Remember(ctx, first, nil, nil, []memory.EntryInput{revision}, memory.RememberAppend); rememberErr != nil {
		t.Fatal(rememberErr)
	}
	_, err = service.PlanRemember(ctx, first, nil, nil, []memory.EntryInput{input}, memory.RememberAppend)
	var conflict *artifact.RevisionConflictError
	if !errors.As(err, &conflict) || conflict.Requested != first.Ref() || conflict.Current.Revision() != 2 {
		t.Fatalf("error = %T %v", err, err)
	}
}

func TestMemoryWriteRemainsDirectAndDoesNotCreateReviewCandidate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	_, artifacts := repositories(t)
	repository, err := sqlstore.NewMemoryRepository(database, "scope-direct", artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := memory.NewService(repository, memory.ServiceOptions{IDFactory: sequentialMemoryIDs()})
	if err != nil {
		t.Fatal(err)
	}
	input := memory.NewEntryInput(nil, "decision", "Keep Memory writes direct.", nil, nil, nil)
	remembered, err := service.Remember(ctx, nil, nil, nil, []memory.EntryInput{input}, memory.RememberAppend)
	if err != nil {
		t.Fatal(err)
	}
	if remembered == nil || remembered.Family() != memory.Family || remembered.Revision() != 1 {
		t.Fatalf("remembered Memory = %#v", remembered)
	}

	candidates, err := sqlstore.NewCandidateRepository(
		sqlstore.SQLiteDialect, sqlstore.ExperienceArtifactCodec(), sqlstore.SkillArtifactCodec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	var inbox review.Page
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var listErr error
		inbox, listErr = candidates.List(ctx, tx, "scope-direct", review.Pending, nil, nil, review.DefaultPageSize)
		return listErr
	}); err != nil {
		t.Fatal(err)
	}
	if len(inbox.Candidates) != 0 || inbox.NextCursor != nil {
		t.Fatalf("direct Memory write created Review work: %#v", inbox)
	}
}

func sequentialMemoryIDs() memory.IDFactory {
	counters := map[string]int{}
	return func(kind string) (string, error) {
		prefixes := map[string]string{"memory": "mem_art", "entry": "mem_ent", "version": "mem_ver"}
		prefix, exists := prefixes[kind]
		if !exists {
			return "", fmt.Errorf("unexpected identity kind %q", kind)
		}
		counters[kind]++
		return fmt.Sprintf("%s_%d", prefix, counters[kind]), nil
	}
}
