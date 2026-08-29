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
	"time"

	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
	"github.com/ob-labs/powercontext-go/trigger"
)

func TestMemoryFlushArtifactAndCursorCASAreOneTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	sources, artifacts := repositories(t)
	stored := addFlushSource(t, database, sources, "scope-flush", "source-1", "Remember the atomic window.")
	repository, _ := sqlstore.NewMemoryRepository(database, "scope-flush", artifacts, nil)
	resolver, _ := sqlstore.NewMemorySourceResolver(database, "scope-flush", sources)
	service, err := memory.NewService(repository, memory.ServiceOptions{
		CandidatePipeline: echoMemoryPipeline{}, SourceResolver: resolver, IDFactory: sequentialMemoryIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlstore.NewMemoryFlushStore(database, "scope-flush", sources, repository)
	if err != nil {
		t.Fatal(err)
	}
	previous, next, generation, high, values, err := store.ObserveWindow(ctx, trigger.SourceWindowName, 10)
	if err != nil {
		t.Fatal(err)
	}
	if previous.Sequence() != 0 || next.Sequence() != 1 || generation != nil || high != 1 || len(values) != 1 {
		t.Fatalf("observed window = previous:%d next:%d generation:%v high:%d values:%d",
			previous.Sequence(), next.Sequence(), generation, high, len(values))
	}
	if ref, _ := resolver.Ref(values[0]); ref != stored.Ref {
		t.Fatalf("observed Source = %v, want %v", ref, stored.Ref)
	}
	plan, err := service.PlanRemember(ctx, nil, values, nil, nil, memory.RememberExtract)
	if err != nil {
		t.Fatal(err)
	}

	// Install a competing generation after observation. Apply writes Memory
	// first, then fails cursor CAS; the surrounding transaction must roll both
	// authoritative Memory and projections back.
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, saveErr := (sqlstore.SourceCursorRepository{}).Save(
			ctx, tx, "scope-flush", trigger.SourceWindowName, source.NewCursor(0), nil,
		)
		return saveErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	_, err = store.ApplyWindow(ctx, trigger.SourceWindowName, plan, next, generation)
	var conflict *sqlstore.GenerationConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ApplyWindow error = %T %v, want generation conflict", err, err)
	}
	assertFlushState(t, database, 0, 0, 1)

	previous, next, generation, _, values, err = store.ObserveWindow(ctx, trigger.SourceWindowName, 10)
	if err != nil {
		t.Fatal(err)
	}
	if generation == nil || *generation != 1 {
		t.Fatalf("retry generation = %v", generation)
	}
	plan, err = service.PlanRemember(ctx, nil, values, nil, nil, memory.RememberExtract)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.ApplyWindow(ctx, trigger.SourceWindowName, plan, next, generation)
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.Revision() != 1 || previous.Sequence() != 0 {
		t.Fatalf("retry result = %#v", updated)
	}
	assertFlushState(t, database, 1, 1, 2)
}

func TestMemoryFlushPlanningDoesNotHoldDatabaseTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	sources, artifacts := repositories(t)
	addFlushSource(t, database, sources, "scope-model", "source-1", "first")
	repository, _ := sqlstore.NewMemoryRepository(database, "scope-model", artifacts, nil)
	resolver, _ := sqlstore.NewMemorySourceResolver(database, "scope-model", sources)
	pipeline := &blockingMemoryPipeline{started: make(chan struct{}), release: make(chan struct{})}
	service, err := memory.NewService(repository, memory.ServiceOptions{
		CandidatePipeline: pipeline, SourceResolver: resolver, IDFactory: sequentialMemoryIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store, _ := sqlstore.NewMemoryFlushStore(database, "scope-model", sources, repository)
	_, _, _, _, values, err := store.ObserveWindow(ctx, trigger.SourceWindowName, 10)
	if err != nil {
		t.Fatal(err)
	}
	planned := make(chan error, 1)
	go func() {
		_, planErr := service.PlanRemember(ctx, nil, values, nil, nil, memory.RememberExtract)
		planned <- planErr
	}()
	select {
	case <-pipeline.started:
	case <-time.After(time.Second):
		t.Fatal("Memory candidate pipeline did not start")
	}

	second := contentSource(t, "source-2", "second", nil)
	captured := make(chan error, 1)
	go func() {
		captured <- database.Transaction(ctx, func(tx sqlstore.DBTX) error {
			_, addErr := sources.Add(ctx, tx, "scope-model", second)
			return addErr
		})
	}()
	select {
	case err := <-captured:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Source capture was blocked while model planning ran")
	}
	close(pipeline.release)
	if err := <-planned; err != nil {
		t.Fatal(err)
	}
}

type echoMemoryPipeline struct{}

func (echoMemoryPipeline) Extract(_ context.Context, request memory.CandidateRequest) ([]memory.EntryInput, error) {
	result := make([]memory.EntryInput, 0, len(request.Sources()))
	for _, value := range request.Sources() {
		content, ok := value.(source.ContentSource)
		if !ok {
			continue
		}
		result = append(result, memory.NewEntryInput(nil, "fact", content.Content(), []source.Value{value}, nil, nil))
	}
	return result, nil
}

type blockingMemoryPipeline struct {
	started chan struct{}
	release chan struct{}
}

func (p *blockingMemoryPipeline) Extract(ctx context.Context, request memory.CandidateRequest) ([]memory.EntryInput, error) {
	close(p.started)
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-p.release:
		return (echoMemoryPipeline{}).Extract(ctx, request)
	}
}

func addFlushSource(
	t *testing.T,
	database *sqlstore.Database,
	repository *sqlstore.SourceRepository,
	scopeID, sourceID, text string,
) sqlstore.StoredSource {
	t.Helper()
	value := contentSource(t, sourceID, text, nil)
	var stored sqlstore.StoredSource
	if err := database.Transaction(context.Background(), func(tx sqlstore.DBTX) error {
		var err error
		stored, err = repository.Add(context.Background(), tx, scopeID, value)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return stored
}

func assertFlushState(t *testing.T, database *sqlstore.Database, artifacts, cursorSequence, generation int64) {
	t.Helper()
	ctx := context.Background()
	var artifactCount int64
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pc_artifacts
        WHERE scope_id = ? AND family = ?`, "scope-flush", memory.Family).Scan(&artifactCount); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	var storedGeneration int64
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT cursor, generation FROM pc_source_cursors
        WHERE scope_id = ? AND binding_name = ?`, "scope-flush", trigger.SourceWindowName).Scan(
		&payload, &storedGeneration,
	); err != nil {
		t.Fatal(err)
	}
	wantPayload := fmt.Sprintf(`{"sequence":%d}`, cursorSequence)
	if artifactCount != artifacts || string(payload) != wantPayload || storedGeneration != generation {
		t.Fatalf("flush state = artifacts:%d cursor:%s generation:%d", artifactCount, payload, storedGeneration)
	}
}
