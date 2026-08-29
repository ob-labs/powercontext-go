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
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestSourceRepositoryPythonPayloadAndIdempotence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	value := contentSource(t, "capture-1", "hello <world>", map[string]any{
		"nested": map[string]any{"x": "y"},
		"a":      1,
	})

	var first sqlstore.StoredSource
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var addErr error
		first, addErr = repository.Add(ctx, tx, "scope-a", value)
		return addErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if first.JournalPosition != 1 {
		t.Fatalf("journal position = %d", first.JournalPosition)
	}
	var payload []byte
	if queryErr := database.SQLDB().QueryRowContext(ctx,
		"SELECT payload FROM pc_sources WHERE scope_id = ? AND source_type = ? AND source_id = ?",
		"scope-a", "content", "capture-1",
	).Scan(&payload); queryErr != nil {
		t.Fatal(queryErr)
	}
	want := `{"name":"capture-1","materialization":"captured","description":null,"content":"hello <world>","metadata":{"a":1,"nested":{"x":"y"}}}`
	if string(payload) != want {
		t.Fatalf("payload mismatch\n got: %s\nwant: %s", payload, want)
	}

	var second sqlstore.StoredSource
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var addErr error
		second, addErr = repository.Add(ctx, tx, "scope-a", value)
		return addErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if second.JournalPosition != 1 {
		t.Fatalf("idempotent position = %d", second.JournalPosition)
	}

	conflicting := contentSource(t, "capture-1", "different", nil)
	err = database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(ctx, tx, "scope-a", conflicting)
		return addErr
	})
	var conflict *sqlstore.StoredPayloadConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

func TestSourceRepositoryKeepsCaseAndAccentVariantIdentitiesDistinct(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	var second sqlstore.StoredSource
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		if _, addErr := repository.Add(ctx, tx, "PC-Alpha", contentSource(t, "Turn-1", "upper", nil)); addErr != nil {
			return addErr
		}
		var addErr error
		second, addErr = repository.Add(ctx, tx, "PC-Alpha", contentSource(t, "turn-1", "lower", nil))
		return addErr
	}); err != nil {
		t.Fatal(err)
	}
	if second.JournalPosition != 2 {
		t.Fatalf("case-variant Source position = %d, want 2", second.JournalPosition)
	}
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		if _, addErr := repository.Add(ctx, tx, "project-café", contentSource(t, "turn", "accent", nil)); addErr != nil {
			return addErr
		}
		for _, scope := range []string{"pc-alpha", "project-cafe"} {
			items, listErr := repository.List(ctx, tx, scope, 0, nil)
			if listErr != nil {
				return listErr
			}
			if len(items) != 0 {
				return fmt.Errorf("identity-variant scope %q leaked %d Sources", scope, len(items))
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRepositorySerializesConcurrentJournalPositions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}

	const count = 16
	positions := make([]int, 0, count)
	var positionsMu sync.Mutex
	var group sync.WaitGroup
	errorsFound := make(chan error, count)
	for index := range count {
		group.Go(func() {
			value := contentSource(t, fmt.Sprintf("capture-%02d", index), "content", nil)
			var stored sqlstore.StoredSource
			err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
				var addErr error
				stored, addErr = repository.Add(ctx, tx, "scope-concurrent", value)
				return addErr
			})
			if err != nil {
				errorsFound <- err
				return
			}
			positionsMu.Lock()
			positions = append(positions, int(stored.JournalPosition))
			positionsMu.Unlock()
		})
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	sort.Ints(positions)
	for index, position := range positions {
		if position != index+1 {
			t.Fatalf("positions = %v", positions)
		}
	}
	var highWatermark int64
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var positionErr error
		highWatermark, positionErr = repository.JournalPosition(ctx, tx, "scope-concurrent")
		return positionErr
	}); err != nil {
		t.Fatal(err)
	}
	if highWatermark != count {
		t.Fatalf("high watermark = %d", highWatermark)
	}

	repeated := contentSource(t, "same-source", "same-content", nil)
	positions = positions[:0]
	secondErrors := make(chan error, 8)
	for range 8 {
		group.Go(func() {
			var stored sqlstore.StoredSource
			err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
				var addErr error
				stored, addErr = repository.Add(ctx, tx, "scope-idempotent", repeated)
				return addErr
			})
			if err != nil {
				secondErrors <- err
				return
			}
			positionsMu.Lock()
			positions = append(positions, int(stored.JournalPosition))
			positionsMu.Unlock()
		})
	}
	group.Wait()
	close(secondErrors)
	for err := range secondErrors {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	for _, position := range positions {
		if position != 1 {
			t.Fatalf("idempotent positions = %v", positions)
		}
	}
	following := contentSource(t, "following-source", "following-content", nil)
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		stored, addErr := repository.Add(ctx, tx, "scope-idempotent", following)
		if addErr == nil && stored.JournalPosition != 2 {
			t.Fatalf("following position = %d", stored.JournalPosition)
		}
		return addErr
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTwoSourceAdaptersShareRepositoryAndJournal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(
		sqlstore.SQLiteDialect,
		sqlstore.ContentSourceCodec(),
		sqlstore.ExternalSkillSnapshotSourceCodec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	registration, err := skill.NewRegistration(
		"codex:project:repository/friendly-python", "codex", "codex", "workstation-1",
		skill.ProjectScope, "/workspace/.agents/skills/friendly-python", strings.Repeat("a", 64),
		"friendly-python", "Use when writing Python.",
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := skill.NewSnapshot(registration, "---\nname: friendly-python\n---\n\nKeep boundaries explicit.\n")
	if err != nil {
		t.Fatal(err)
	}
	external, err := (skill.SnapshotSourceAdapter{}).Resolve(ctx, skill.SnapshotCapture{
		Snapshot: snapshot, Mode: skill.ImportModeImport,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := contentSource(t, "note-1", "Review the repository boundary.", nil)
	var first, second, repeated sqlstore.StoredSource
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var addErr error
		first, addErr = repository.Add(ctx, tx, "scope-a", content)
		if addErr != nil {
			return addErr
		}
		second, addErr = repository.Add(ctx, tx, "scope-a", external)
		if addErr != nil {
			return addErr
		}
		repeated, addErr = repository.Add(ctx, tx, "scope-a", content)
		return addErr
	}); err != nil {
		t.Fatal(err)
	}
	if first.JournalPosition != 1 || second.JournalPosition != 2 || repeated.JournalPosition != 1 {
		t.Fatalf("positions = %d/%d/%d", first.JournalPosition, second.JournalPosition, repeated.JournalPosition)
	}
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		listed, listErr := repository.List(ctx, tx, "scope-a", 1, nil)
		if listErr != nil {
			return listErr
		}
		if len(listed) != 1 {
			t.Fatalf("listed = %#v", listed)
		}
		_, ok := listed[0].Value.(skill.SnapshotSource)
		if !ok {
			t.Fatalf("decoded Source = %T", listed[0].Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRepositoryRejectsScopeOutsideRelationalIdentityBaseline(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(context.Background(), func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(
			context.Background(), tx, strings.Repeat("x", 257), contentSource(t, "note", "body", nil),
		)
		return addErr
	})
	var invalid *sqlstore.InvalidRepositoryArgumentError
	if !errors.As(err, &invalid) || invalid.Field != "scope_id" {
		t.Fatalf("scope error = %v", err)
	}
}

func TestSourceRepositoryRejectsIndexedIdentityMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	value := contentSource(t, "indexed", "content", nil)
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(ctx, tx, "scope-a", value)
		return addErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if _, execErr := database.SQLDB().ExecContext(ctx,
		`UPDATE pc_sources SET payload = ? WHERE scope_id = ? AND source_type = ? AND source_id = ?`,
		[]byte(`{"name":"decoded","materialization":"captured","description":null,"content":"content","metadata":{}}`),
		"scope-a", "content", "indexed",
	); execErr != nil {
		t.Fatal(execErr)
	}
	ref, err := source.NewRef("content", "indexed")
	if err != nil {
		t.Fatal(err)
	}
	err = database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, getErr := repository.Get(ctx, tx, "scope-a", ref)
		return getErr
	})
	var mismatch *sqlstore.IdentityMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected identity mismatch, got %v", err)
	}
}

func openTestDatabase(t *testing.T) *sqlstore.Database {
	t.Helper()
	config := sqlstore.DefaultSQLiteConfig(filepath.Join(t.TempDir(), "powercontext.db"))
	database, err := sqlstore.OpenSQLite(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return database
}

func contentSource(t *testing.T, id, content string, metadata map[string]any) source.ContentSource {
	t.Helper()
	capture, err := source.NewContentCapture(id, content, metadata)
	if err != nil {
		t.Fatal(err)
	}
	value, err := (source.ContentAdapter{}).Resolve(context.Background(), capture)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
