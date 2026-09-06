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
	"encoding/json/jsontext"
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

func TestSourceRepositoryPersistsObservationEnvelopeAlongsideNativeSources(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	observation := observationSource(t, "worker.capture", "worker-item", `{"name":"worker-item","definition_version":"1","materialization":"captured","payload":{"nested":true}}`)

	ref, err := repository.Ref(observation)
	if err != nil {
		t.Fatal(err)
	}
	if ref != observation.Ref() {
		t.Fatalf("observation Ref() = %s, want %s", ref, observation.Ref())
	}

	var native, stored sqlstore.StoredSource
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var addErr error
		native, addErr = repository.Add(ctx, tx, "scope-observation", contentSource(t, "native-item", "native", nil))
		if addErr != nil {
			return addErr
		}
		stored, addErr = repository.Add(ctx, tx, "scope-observation", observation)
		return addErr
	}); err != nil {
		t.Fatal(err)
	}
	if native.JournalPosition != 1 || stored.JournalPosition != 2 {
		t.Fatalf("journal positions = %d/%d, want 1/2", native.JournalPosition, stored.JournalPosition)
	}
	decoded, ok := stored.Value.(source.SourceObservation)
	if !ok || decoded.Ref() != observation.Ref() || string(decoded.Payload()) != string(observation.Payload()) {
		t.Fatalf("stored observation = %#v", stored.Value)
	}

	var nativePayload, observationPayload []byte
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT payload FROM pc_sources
        WHERE scope_id = ? AND source_type = ? AND source_id = ?`,
		"scope-observation", "content", "native-item",
	).Scan(&nativePayload); err != nil {
		t.Fatal(err)
	}
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT payload FROM pc_sources
        WHERE scope_id = ? AND source_type = ? AND source_id = ?`,
		"scope-observation", observation.Ref().Type(), observation.Ref().ID(),
	).Scan(&observationPayload); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(nativePayload, []byte(`"encoding":"powercontext-source-v1"`)) {
		t.Fatalf("native payload changed to an envelope: %s", nativePayload)
	}
	for _, member := range []string{
		`"encoding":"powercontext-source-v1"`,
		`"representation":"observation"`,
		`"source_type":"worker.capture"`,
	} {
		if !bytes.Contains(observationPayload, []byte(member)) {
			t.Fatalf("observation envelope %s does not contain %s", observationPayload, member)
		}
	}

	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		got, getErr := repository.Get(ctx, tx, "scope-observation", observation.Ref())
		if getErr != nil {
			return getErr
		}
		if got.JournalPosition != 2 {
			t.Fatalf("observation Get() position = %d, want 2", got.JournalPosition)
		}
		listed, listErr := repository.List(ctx, tx, "scope-observation", 0, nil)
		if listErr != nil {
			return listErr
		}
		if len(listed) != 2 {
			t.Fatalf("List() returned %d Sources, want 2", len(listed))
		}
		_, observationFound := listed[1].Value.(source.SourceObservation)
		if !observationFound {
			t.Fatalf("List() observation type = %T", listed[1].Value)
		}
		return nil
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
}

func TestSourceRepositoryObservationEnvelopeWinsOverCollidingContentCodec(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	observation := observationSource(t, source.ContentType, "observation-item", `{"name":"observation-item","definition_version":"1","materialization":"captured","payload":{"content":"worker-owned"}}`)

	var added sqlstore.StoredSource
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var addErr error
		added, addErr = repository.Add(ctx, tx, "scope-observation-content", observation)
		return addErr
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := added.Value.(source.SourceObservation); !ok {
		t.Fatalf("Add() value type = %T, want source.SourceObservation", added.Value)
	}

	var payload []byte
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT payload FROM pc_sources
        WHERE scope_id = ? AND source_type = ? AND source_id = ?`,
		"scope-observation-content", source.ContentType, observation.Ref().ID(),
	).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{
		`"encoding":"powercontext-source-v1"`,
		`"representation":"observation"`,
		`"source_type":"content"`,
	} {
		if !bytes.Contains(payload, []byte(member)) {
			t.Fatalf("observation payload %s does not contain %s", payload, member)
		}
	}

	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		stored, getErr := repository.Get(ctx, tx, "scope-observation-content", observation.Ref())
		if getErr != nil {
			return getErr
		}
		if _, ok := stored.Value.(source.SourceObservation); !ok {
			t.Fatalf("Get() value type = %T, want source.SourceObservation", stored.Value)
		}
		listed, listErr := repository.List(ctx, tx, "scope-observation-content", 0, nil)
		if listErr != nil {
			return listErr
		}
		if len(listed) != 1 {
			t.Fatalf("List() returned %d Sources, want 1", len(listed))
		}
		if _, ok := listed[0].Value.(source.SourceObservation); !ok {
			t.Fatalf("List() value type = %T, want source.SourceObservation", listed[0].Value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSourceRepositoryObservationUsesJSONSemanticIdempotence(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	first := observationSource(t, "worker.capture", "item", `{"name":"item","definition_version":"1","materialization":"captured","payload":{"\u0061":1.0,"b":[true,null],"large":123456789012345678901234567890}}`)
	reordered := observationSource(t, "worker.capture", "item", `{"payload":{"large":123456789012345678901234567890,"b":[true,null],"a":1e0},"materialization":"captured","definition_version":"1","name":"item"}`)
	conflicting := observationSource(t, "worker.capture", "item", `{"name":"item","definition_version":"1","materialization":"captured","payload":{"a":2,"b":[true,null],"large":123456789012345678901234567890}}`)

	var initial, replay sqlstore.StoredSource
	if initialTransactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var addErr error
		initial, addErr = repository.Add(ctx, tx, "scope-observation", first)
		if addErr != nil {
			return addErr
		}
		replay, addErr = repository.Add(ctx, tx, "scope-observation", reordered)
		return addErr
	}); initialTransactionErr != nil {
		t.Fatal(initialTransactionErr)
	}
	if initial.JournalPosition != 1 || replay.JournalPosition != 1 {
		t.Fatalf("replayed positions = %d/%d, want 1/1", initial.JournalPosition, replay.JournalPosition)
	}
	largeIntegerConflict := observationSource(t, "worker.capture", "item", `{"name":"item","definition_version":"1","materialization":"captured","payload":{"a":1e0,"b":[true,null],"large":123456789012345678901234567891}}`)
	err = database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(ctx, tx, "scope-observation", largeIntegerConflict)
		return addErr
	})
	if _, ok := errors.AsType[*sqlstore.StoredPayloadConflictError](err); !ok {
		t.Fatalf("large integer conflict = %T %v", err, err)
	}
	assertObservationRepositoryErrorRedacted(t, err, "worker.capture", "item", "fingerprint-secret")
	err = database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, addErr := repository.Add(ctx, tx, "scope-observation", conflicting)
		return addErr
	})
	if _, ok := errors.AsType[*sqlstore.StoredPayloadConflictError](err); !ok {
		t.Fatalf("conflicting observation = %T %v", err, err)
	}
	assertObservationRepositoryErrorRedacted(t, err, "worker.capture", "item", "fingerprint-secret")
	var position int64
	if positionTransactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var positionErr error
		position, positionErr = repository.JournalPosition(ctx, tx, "scope-observation")
		return positionErr
	}); positionTransactionErr != nil {
		t.Fatal(positionTransactionErr)
	}
	if position != 1 {
		t.Fatalf("journal position after conflicting replay = %d, want 1", position)
	}
}

func TestSourceRepositoryDecodesCurrentNativeEnvelopeWithoutHijackingLegacyEncoding(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}

	current := contentSource(t, "current-v1-native", "current native source", nil)
	legacy := contentSource(t, "legacy-encoding", "legacy native source", nil)
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		if _, addErr := repository.Add(ctx, tx, "seed-native-envelope", current); addErr != nil {
			return addErr
		}
		_, addErr := repository.Add(ctx, tx, "seed-native-envelope", legacy)
		return addErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}

	var currentPayload, legacyPayload []byte
	currentRef, err := source.NewRef(source.ContentType, current.SourceName())
	if err != nil {
		t.Fatal(err)
	}
	legacyRef, err := source.NewRef(source.ContentType, legacy.SourceName())
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT payload FROM pc_sources
        WHERE scope_id = ? AND source_type = ? AND source_id = ?`,
		"seed-native-envelope", currentRef.Type(), currentRef.ID(),
	).Scan(&currentPayload); err != nil {
		t.Fatal(err)
	}
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT payload FROM pc_sources
        WHERE scope_id = ? AND source_type = ? AND source_id = ?`,
		"seed-native-envelope", legacyRef.Type(), legacyRef.ID(),
	).Scan(&legacyPayload); err != nil {
		t.Fatal(err)
	}
	currentPayload = []byte(`{"encoding":"powercontext-source-v1","representation":"native","value":` + string(currentPayload) + `}`)
	legacyPayload = append(legacyPayload[:len(legacyPayload)-1], []byte(`,"encoding":"powercontext-source-v1"}`)...)

	for _, entry := range []struct {
		name     string
		ref      source.Ref
		payload  []byte
		position int
	}{
		{name: "current native envelope", ref: currentRef, payload: currentPayload, position: 1},
		{name: "legacy native encoding field", ref: legacyRef, payload: legacyPayload, position: 2},
	} {
		t.Run(entry.name, func(t *testing.T) {
			t.Parallel()
			if _, execErr := database.SQLDB().ExecContext(ctx, `INSERT INTO pc_sources
                (scope_id, source_type, source_id, payload, journal_position) VALUES (?, ?, ?, ?, ?)`,
				"native-envelope", entry.ref.Type(), entry.ref.ID(), entry.payload, entry.position,
			); execErr != nil {
				t.Fatal(execErr)
			}
			err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
				stored, getErr := repository.Get(ctx, tx, "native-envelope", entry.ref)
				if getErr != nil {
					return getErr
				}
				if stored.Value.SourceName() != entry.ref.ID() {
					t.Fatalf("decoded native SourceName() = %q, want %q", stored.Value.SourceName(), entry.ref.ID())
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSourceRepositoryRejectsDamagedCurrentV1EnvelopeBeforeCodecLookup(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		payload []byte
	}{
		{
			name:    "malformed outer envelope",
			payload: []byte(`{"encoding":"powercontext-source-v1","representation":"observation","value":`),
		},
		{
			name:    "damaged outer envelope",
			payload: []byte(`{"encoding":"powercontext-source-v1","representation":"observation"}`),
		},
		{
			name:    "duplicate outer marker",
			payload: []byte(`{"encoding":"powercontext-source-v1","encoding":"powercontext-source-v1","representation":"observation","value":{}}`),
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			database := openTestDatabase(t)
			repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
			if err != nil {
				t.Fatal(err)
			}
			const typeName = "dynamic-type-secret"
			const sourceID = "dynamic-id-secret"
			if _, execErr := database.SQLDB().ExecContext(ctx, `INSERT INTO pc_sources
                (scope_id, source_type, source_id, payload, journal_position) VALUES (?, ?, ?, ?, ?)`,
				"scope-dynamic-envelope", typeName, sourceID, scenario.payload, 1,
			); execErr != nil {
				t.Fatal(execErr)
			}
			ref, err := source.NewRef(typeName, sourceID)
			if err != nil {
				t.Fatal(err)
			}
			err = database.Transaction(ctx, func(tx sqlstore.DBTX) error {
				_, getErr := repository.Get(ctx, tx, "scope-dynamic-envelope", ref)
				return getErr
			})
			if _, ok := errors.AsType[*sqlstore.InvalidStoredPayloadError](err); !ok {
				t.Fatalf("damaged dynamic envelope = %T %v", err, err)
			}
			assertObservationRepositoryErrorRedacted(t, err, typeName, sourceID, "powercontext-source-v1")
		})
	}
}

func TestSourceRepositoryClassifiesObservationStorageCorruptionBeforeCodecLookup(t *testing.T) {
	for _, scenario := range []string{"malformed envelope", "unknown envelope member", "indexed identity mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			database := openTestDatabase(t)
			repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
			if err != nil {
				t.Fatal(err)
			}
			observation := observationSource(t, "decoded-type-secret", "decoded-id-secret", `{"name":"decoded-id-secret","definition_version":"1","materialization":"captured"}`)
			payload, err := observationEnvelope(observation)
			if err != nil {
				t.Fatal(err)
			}
			indexedType, indexedID := observation.Ref().Type(), observation.Ref().ID()
			switch scenario {
			case "malformed envelope":
				payload = []byte(`{"encoding":"powercontext-source-v1","representation":"observation","value":{"source_type":"stored-secret"}}`)
			case "unknown envelope member":
				payload = append(payload[:len(payload)-1], []byte(`,"stored-secret":true}`)...)
			case "indexed identity mismatch":
				indexedType = "indexed-type-secret"
				indexedID = "indexed-id-secret"
			}
			if _, execErr := database.SQLDB().ExecContext(ctx, `INSERT INTO pc_sources
                (scope_id, source_type, source_id, payload, journal_position) VALUES (?, ?, ?, ?, ?)`,
				"scope-observation", indexedType, indexedID, payload, 1,
			); execErr != nil {
				t.Fatal(execErr)
			}
			ref, err := source.NewRef(indexedType, indexedID)
			if err != nil {
				t.Fatal(err)
			}
			err = database.Transaction(ctx, func(tx sqlstore.DBTX) error {
				_, getErr := repository.Get(ctx, tx, "scope-observation", ref)
				return getErr
			})
			switch scenario {
			case "malformed envelope", "unknown envelope member":
				if _, ok := errors.AsType[*sqlstore.InvalidStoredPayloadError](err); !ok {
					t.Fatalf("malformed observation error = %T %v", err, err)
				}
			case "indexed identity mismatch":
				if _, ok := errors.AsType[*sqlstore.IdentityMismatchError](err); !ok {
					t.Fatalf("indexed observation error = %T %v", err, err)
				}
			}
			assertObservationRepositoryErrorRedacted(t, err,
				"decoded-type-secret", "decoded-id-secret", "indexed-type-secret", "indexed-id-secret", "stored-secret", "fingerprint-secret",
			)
		})
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

func observationSource(t *testing.T, sourceType, sourceID, payload string) source.SourceObservation {
	t.Helper()
	ref, err := source.NewRef(sourceType, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(ref, "1", "fingerprint-secret", nil, jsontext.Value(payload), nil)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func observationEnvelope(observation source.SourceObservation) ([]byte, error) {
	value, err := observation.MarshalJSON()
	if err != nil {
		return nil, err
	}
	return []byte(`{"encoding":"powercontext-source-v1","representation":"observation","value":` + string(value) + `}`), nil
}

func assertObservationRepositoryErrorRedacted(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an observation repository error")
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("observation repository error exposed %q: %v", secret, err)
		}
	}
}
