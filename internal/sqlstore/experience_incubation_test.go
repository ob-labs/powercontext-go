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
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/runtime"
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

func TestExperienceIncubationRejectsRawObservationCitationFromMixedWindow(t *testing.T) {
	ctx := t.Context()
	database := openTestDatabase(t)
	sources, _ := repositories(t)
	stored := addFlushSource(t, database, sources, "scope-filtered-window", "task-1", "safe task outcome")
	raw := observationSource(t, "worker.raw", "item-1", `{"name":"item-1","definition_version":"1","materialization":"captured"}`)
	payload, err := observationEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, insertErr := database.SQLDB().ExecContext(ctx, `INSERT INTO pc_sources
        (scope_id, source_type, source_id, payload, journal_position) VALUES (?, ?, ?, ?, ?)`,
		"scope-filtered-window", raw.Ref().Type(), raw.Ref().ID(), payload, 2,
	); insertErr != nil {
		t.Fatal(insertErr)
	}
	candidates, err := sqlstore.NewCandidateRepository(sqlstore.SQLiteDialect, sqlstore.ExperienceArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlstore.NewExperienceIncubationStore(database, "scope-filtered-window", sources, candidates)
	if err != nil {
		t.Fatal(err)
	}
	var seen []source.Value
	application, err := runtime.NewExperienceIncubationApplication(
		runtime.New(), func(string) (runtime.ExperienceIncubationBackend, error) { return store, nil },
		experiencePipelineFunc(func(_ context.Context, values []source.Value) ([]experience.CandidateInput, error) {
			seen = append([]source.Value(nil), values...)
			content, contentErr := experience.NewContent("situation", "action", "outcome", "lesson")
			if contentErr != nil {
				return nil, contentErr
			}
			plan, planErr := experience.NewCandidateInput(content, []source.Ref{raw.Ref()})
			return []experience.CandidateInput{plan}, planErr
		}),
		func(string) (string, error) { return "candidate-raw", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := application.Incubate(ctx, "scope-filtered-window", experience.IncubationWindowLimit)
	var invalid *inference.InvalidOutputError
	if !errors.As(err, &invalid) {
		t.Fatalf("Incubate() error = %T %v", err, err)
	}
	if result.CurrentCursor != 2 || result.HighWatermark != 2 || result.ProcessedSourceCount != 1 || len(seen) != 1 {
		t.Fatalf("result=%#v pipeline values=%#v", result, seen)
	}
	if value, ok := seen[0].(source.ContentSource); !ok || value.SourceName() != stored.Ref.ID() {
		t.Fatalf("pipeline received %T %#v, want captured ContentSource", seen[0], seen[0])
	}
	var candidateCount, cursorCount int
	if queryErr := database.SQLDB().QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM pc_artifact_candidate_heads WHERE scope_id = ?),
        (SELECT COUNT(*) FROM pc_source_cursors WHERE scope_id = ? AND binding_name = ?)`,
		"scope-filtered-window", "scope-filtered-window", experience.IncubationCursorName,
	).Scan(&candidateCount, &cursorCount); queryErr != nil {
		t.Fatal(queryErr)
	}
	if candidateCount != 0 || cursorCount != 0 {
		t.Fatalf("rejected raw citation persisted candidates=%d cursors=%d", candidateCount, cursorCount)
	}
}

func TestExperienceIncubationConsumesRawOnlyWindowWithoutPipeline(t *testing.T) {
	ctx := t.Context()
	database := openTestDatabase(t)
	sources, _ := repositories(t)
	raw := observationSource(t, "worker.raw", "item-1", `{"name":"item-1","definition_version":"1","materialization":"captured"}`)
	payload, err := observationEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, insertErr := database.SQLDB().ExecContext(ctx, `INSERT INTO pc_sources
        (scope_id, source_type, source_id, payload, journal_position) VALUES (?, ?, ?, ?, ?)`,
		"scope-raw-only", raw.Ref().Type(), raw.Ref().ID(), payload, 1,
	); insertErr != nil {
		t.Fatal(insertErr)
	}
	candidates, err := sqlstore.NewCandidateRepository(sqlstore.SQLiteDialect, sqlstore.ExperienceArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlstore.NewExperienceIncubationStore(database, "scope-raw-only", sources, candidates)
	if err != nil {
		t.Fatal(err)
	}
	application, err := runtime.NewExperienceIncubationApplication(
		runtime.New(), func(string) (runtime.ExperienceIncubationBackend, error) { return store, nil },
		experiencePipelineFunc(func(context.Context, []source.Value) ([]experience.CandidateInput, error) {
			t.Fatal("raw-only window invoked the Experience candidate pipeline")
			return nil, nil
		}),
		func(string) (string, error) { return "unused", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := application.Incubate(ctx, "scope-raw-only", experience.IncubationWindowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if result.CurrentCursor != 1 || result.HighWatermark != 1 || result.ProcessedSourceCount != 0 || result.CandidateCount != 0 {
		t.Fatalf("result=%#v", result)
	}
	assertIncubationCursor(t, database, "scope-raw-only", 1)
}

func TestExperienceIncubationApplyWindowRejectsRawCitationAndRollsBack(t *testing.T) {
	ctx := t.Context()
	database := openTestDatabase(t)
	sources, _ := repositories(t)
	raw := observationSource(t, "worker.raw", "item-apply", `{"name":"item-apply","definition_version":"1","materialization":"captured"}`)
	payload, err := observationEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, insertErr := database.SQLDB().ExecContext(ctx, `INSERT INTO pc_sources
        (scope_id, source_type, source_id, payload, journal_position) VALUES (?, ?, ?, ?, ?)`,
		"scope-raw-apply", raw.Ref().Type(), raw.Ref().ID(), payload, 1,
	); insertErr != nil {
		t.Fatal(insertErr)
	}
	candidates, err := sqlstore.NewCandidateRepository(sqlstore.SQLiteDialect, sqlstore.ExperienceArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlstore.NewExperienceIncubationStore(database, "scope-raw-apply", sources, candidates)
	if err != nil {
		t.Fatal(err)
	}
	content, err := experience.NewContent("situation", "action", "outcome", "lesson")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := experience.NewCandidateInput(content, []source.Ref{raw.Ref()})
	if err != nil {
		t.Fatal(err)
	}
	err = store.ApplyWindow(ctx, experience.IncubationCursorName, []string{"candidate-raw"}, []experience.CandidateInput{plan}, source.NewCursor(1), nil)
	if _, ok := errors.AsType[*source.UnacceptedObservationError](err); !ok {
		t.Fatalf("ApplyWindow() error = %T %v", err, err)
	}
	var candidateCount, cursorCount int
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM pc_artifact_candidate_heads WHERE scope_id = ?),
        (SELECT COUNT(*) FROM pc_source_cursors WHERE scope_id = ? AND binding_name = ?)`,
		"scope-raw-apply", "scope-raw-apply", experience.IncubationCursorName,
	).Scan(&candidateCount, &cursorCount); err != nil {
		t.Fatal(err)
	}
	if candidateCount != 0 || cursorCount != 0 {
		t.Fatalf("raw candidate changed state: candidates=%d cursors=%d", candidateCount, cursorCount)
	}
}

type experiencePipelineFunc func(context.Context, []source.Value) ([]experience.CandidateInput, error)

func (f experiencePipelineFunc) Incubate(ctx context.Context, values []source.Value) ([]experience.CandidateInput, error) {
	return f(ctx, values)
}

func assertIncubationCursor(t *testing.T, database *sqlstore.Database, scopeID string, sequence int64) {
	t.Helper()
	var cursor []byte
	if err := database.SQLDB().QueryRowContext(t.Context(), `SELECT cursor FROM pc_source_cursors
        WHERE scope_id = ? AND binding_name = ?`, scopeID, experience.IncubationCursorName).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`{"sequence":%d}`, sequence)
	if string(cursor) != want {
		t.Fatalf("cursor=%s, want %s", cursor, want)
	}
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
