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
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestExperienceCandidatesAndCursorCASAreOneTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	sources, _ := repositories(t)
	candidates, err := sqlstore.NewCandidateRepository(sqlstore.SQLiteDialect, sqlstore.ExperienceArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	stored := addFlushSource(t, database, sources, "scope-incubation", "task-1", "task outcome")
	store, err := sqlstore.NewExperienceIncubationStore(database, "scope-incubation", sources, candidates)
	if err != nil {
		t.Fatal(err)
	}
	previous, next, generation, high, values, refs, err := store.ObserveWindow(
		ctx, experience.IncubationCursorName, experience.IncubationWindowLimit,
	)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Sequence() != 0 || next.Sequence() != 1 || generation != nil || high != 1 ||
		len(values) != 1 || len(refs) != 1 || refs[0] != stored.Ref {
		t.Fatalf("window = previous:%d next:%d generation:%v high:%d values:%d refs:%v",
			previous.Sequence(), next.Sequence(), generation, high, len(values), refs)
	}
	content, _ := experience.NewContent("situation", "action", "outcome", "lesson")
	plan, _ := experience.NewCandidateInput(content, []source.Ref{stored.Ref})

	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, saveErr := (sqlstore.SourceCursorRepository{}).Save(
			ctx, tx, "scope-incubation", experience.IncubationCursorName, source.NewCursor(0), nil,
		)
		return saveErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	err = store.ApplyWindow(
		ctx, experience.IncubationCursorName, []string{"cand-1"}, []experience.CandidateInput{plan}, next, generation,
	)
	var conflict *sqlstore.GenerationConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ApplyWindow() error = %T %v", err, err)
	}
	assertIncubationState(t, database, 0, 0, 1)

	_, next, generation, _, _, _, err = store.ObserveWindow(
		ctx, experience.IncubationCursorName, experience.IncubationWindowLimit,
	)
	if err != nil || generation == nil || *generation != 1 {
		t.Fatalf("retry generation = %v, err=%v", generation, err)
	}
	if err := store.ApplyWindow(
		ctx, experience.IncubationCursorName, []string{"cand-1"}, []experience.CandidateInput{plan}, next, generation,
	); err != nil {
		t.Fatal(err)
	}
	assertIncubationState(t, database, 1, 1, 2)
}

func assertIncubationState(t *testing.T, database *sqlstore.Database, candidates, sequence, generation int64) {
	t.Helper()
	ctx := context.Background()
	var candidateCount int64
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pc_artifact_candidate_heads
        WHERE scope_id = ?`, "scope-incubation").Scan(&candidateCount); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	var storedGeneration int64
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT cursor, generation FROM pc_source_cursors
        WHERE scope_id = ? AND binding_name = ?`, "scope-incubation", experience.IncubationCursorName).Scan(
		&payload, &storedGeneration,
	); err != nil {
		t.Fatal(err)
	}
	wantPayload := fmt.Sprintf(`{"sequence":%d}`, sequence)
	if candidateCount != candidates || string(payload) != wantPayload || storedGeneration != generation {
		t.Fatalf("state = candidates:%d cursor:%s generation:%d", candidateCount, payload, storedGeneration)
	}
}
