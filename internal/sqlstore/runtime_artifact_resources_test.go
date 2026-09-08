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
	"encoding/json/v2"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestRuntimeArtifactPageUsesCurrentCaseSensitiveHeads(t *testing.T) {
	database := openTestDatabase(t)
	database.SQLDB().SetMaxOpenConns(1)
	sources, repository := repositories(t)
	reader, err := sqlstore.NewRuntimeArtifactReader(database, repository)
	if err != nil {
		t.Fatal(err)
	}
	content, err := experience.NewContent("s", "a", "o", "l")
	if err != nil {
		t.Fatal(err)
	}
	var sourceRefs []source.Ref
	for _, id := range []string{"z", "a"} {
		value := contentSource(t, id, id, nil)
		if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
			stored, addErr := sources.Add(t.Context(), tx, "scope-a", value)
			sourceRefs = append(sourceRefs, stored.Ref)
			return addErr
		}); transactionErr != nil {
			t.Fatal(transactionErr)
		}
	}
	draft, err := experience.NewDraft(content, sourceRefs, nil)
	if err != nil {
		t.Fatal(err)
	}
	created := map[string]artifact.Snapshot{}
	for _, id := range []string{"a", "Z", "B", "A"} {
		if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
			var createErr error
			created[id], createErr = repository.Create(t.Context(), tx, "scope-a", id, draft)
			return createErr
		}); transactionErr != nil {
			t.Fatal(transactionErr)
		}
	}
	first, more, err := reader.ReadArtifactPage(t.Context(), "scope-a", "experience", "", 2)
	if err != nil || !more || !slices.Equal(artifactPageIDs(first), []string{"A", "B"}) {
		t.Fatalf("first page = %v more=%v err=%v", artifactPageIDs(first), more, err)
	}
	upstreams := []artifact.Ref{created["B"].Ref(), created["A"].Ref()}
	nextDraft, err := experience.NewDraft(content, sourceRefs, upstreams)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "Z"} {
		if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
			_, reviseErr := repository.Revise(t.Context(), tx, "scope-a", created[id], nextDraft)
			return reviseErr
		}); transactionErr != nil {
			t.Fatal(transactionErr)
		}
	}
	if _, queryErr := database.SQLDB().ExecContext(t.Context(), "PRAGMA query_only = ON"); queryErr != nil {
		t.Fatal(queryErr)
	}
	second, more, err := reader.ReadArtifactPage(t.Context(), "scope-a", "experience", "B", 100)
	if err != nil || more || !slices.Equal(artifactPageIDs(second), []string{"Z", "a"}) {
		t.Fatalf("second page = %v more=%v err=%v", artifactPageIDs(second), more, err)
	}
	if second[0].Ref().Revision() != 2 || !slices.Equal(second[0].Lineage().Sources(), sourceRefs) || !slices.Equal(second[0].Lineage().Artifacts(), upstreams) {
		t.Fatalf("current revision or ordered lineage lost: %+v", second[0])
	}
	for _, request := range [][3]string{{"scope-a", "experience", "a"}, {"scope-b", "experience", ""}, {"scope-a", "skill", ""}} {
		page, hasMore, listErr := reader.ReadArtifactPage(t.Context(), request[0], request[1], request[2], 1)
		if listErr != nil || page == nil || len(page) != 0 || hasMore {
			t.Fatalf("empty page = %+v more=%v err=%v", page, hasMore, listErr)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, listErr := reader.ReadArtifactPage(ctx, "scope-a", "experience", "", 1); !errors.Is(listErr, context.Canceled) {
		t.Fatalf("canceled page = %v", listErr)
	}
}

func TestRuntimeArtifactPageRehydratesSkillPackage(t *testing.T) {
	database := openTestDatabase(t)
	database.SQLDB().SetMaxOpenConns(1)
	_, repository := repositories(t)
	reader, err := sqlstore.NewRuntimeArtifactReader(database, repository)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := skillPackageSnapshot(t, "listed-package")
	content, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := skill.NewPackageDraft(content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		if _, addErr := (sqlstore.SkillPackageRepository{}).Add(t.Context(), tx, "scope-a", snapshot); addErr != nil {
			return addErr
		}
		_, createErr := repository.Create(t.Context(), tx, "scope-a", "skill", draft)
		return createErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if _, queryErr := database.SQLDB().ExecContext(t.Context(), "PRAGMA query_only = ON"); queryErr != nil {
		t.Fatal(queryErr)
	}
	page, more, err := reader.ReadArtifactPage(t.Context(), "scope-a", "skill", "", 1)
	if err != nil || more || len(page) != 1 {
		t.Fatalf("package page = %+v more=%v err=%v", page, more, err)
	}
	value, ok := page[0].(skill.PackageSkill)
	if !ok || value.Content().Reference() != snapshot.Reference() || value.Content().Snapshot().Instructions() != snapshot.Instructions() {
		t.Fatal("listed Skill did not rehydrate its persisted package")
	}
}

func artifactPageIDs(page []artifact.Snapshot) []string {
	result := make([]string, len(page))
	for index, value := range page {
		result[index] = value.Ref().ID()
	}
	return result
}

func TestRuntimeArtifactPageKeepsSnapshotDuringConcurrentRevision(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	config := sqlstore.DefaultSQLiteConfig(filepath.Join(t.TempDir(), "page-snapshot.db"))
	database, err := sqlstore.OpenSQLite(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	writer, err := sqlstore.OpenSQLite(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := writer.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	entered, release := make(chan struct{}), make(chan struct{})
	var enabled atomic.Bool
	var pause sync.Once
	codec, err := sqlstore.NewArtifactCodec("experience",
		func(value string) ([]byte, error) { return json.Marshal(value) },
		func(payload []byte) (string, error) {
			var value string
			decodeErr := json.Unmarshal(payload, &value)
			if enabled.Load() && value == "before" {
				pause.Do(func() {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				})
			}
			return value, decodeErr
		})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := sqlstore.NewArtifactRepository(sqlstore.SQLiteDialect, codec)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := artifact.NewDraft("experience", "before", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var second artifact.Snapshot
	if transactionErr := writer.Transaction(ctx, func(tx sqlstore.DBTX) error {
		if _, createErr := repository.Create(ctx, tx, "scope", "A", draft); createErr != nil {
			return createErr
		}
		var createErr error
		second, createErr = repository.Create(ctx, tx, "scope", "B", draft)
		return createErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	reader, err := sqlstore.NewRuntimeArtifactReader(database, repository)
	if err != nil {
		t.Fatal(err)
	}
	enabled.Store(true)
	type pageResult struct {
		values []artifact.Snapshot
		err    error
	}
	completed := make(chan pageResult, 1)
	go func() {
		values, _, listErr := reader.ReadArtifactPage(ctx, "scope", "experience", "", 2)
		completed <- pageResult{values: values, err: listErr}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("page did not enter decoding")
	}
	next, err := artifact.NewDraft("experience", "after", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if transactionErr := writer.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, reviseErr := repository.Revise(ctx, tx, "scope", second, next)
		if reviseErr != nil {
			return reviseErr
		}
		// Removing an old revision must not invalidate a page that already owns a read snapshot.
		_, deleteErr := tx.ExecContext(ctx, `DELETE FROM pc_artifacts WHERE scope_id = ? AND family = ? AND artifact_id = ? AND revision = 1`, "scope", "experience", "B")
		return deleteErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	close(release)
	select {
	case result := <-completed:
		if result.err != nil || len(result.values) != 2 || result.values[1].Ref().Revision() != 1 || result.values[1].ContentValue() != "before" {
			t.Fatalf("page mixed database snapshots: %+v %v", result.values, result.err)
		}
	case <-ctx.Done():
		t.Fatal("page did not complete")
	}
	after, _, err := reader.ReadArtifactPage(ctx, "scope", "experience", "A", 1)
	if err != nil || len(after) != 1 || after[0].Ref().Revision() != 2 || after[0].ContentValue() != "after" {
		t.Fatalf("later page did not observe committed head: %+v %v", after, err)
	}
}
