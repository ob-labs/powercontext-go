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
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestConnectorCheckpointRepositoryUsesPythonEnvelopeAndValueCAS(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.ConnectorCheckpointRepository{}
	binding, err := source.NewConnectorBinding("scope-a", "repository", "github", "v1")
	if err != nil {
		t.Fatal(err)
	}
	first := []byte(`{"value":{"right":2,"left":9007199254740993}}`)
	expected := []byte(`{"left":9007199254740993,"right":2}`)
	second := []byte(`{"cursor":2}`)
	if _, err := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_connector_checkpoints
        (scope_id, binding_id, connector_name, connector_version, checkpoint) VALUES (?, ?, ?, ?, ?)`,
		binding.ScopeID(), binding.ID(), binding.ConnectorName(), binding.ConnectorVersion(), first); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		loaded, found, loadErr := repository.Load(t.Context(), tx, binding)
		if loadErr != nil || !found || string(loaded) != string(expected) {
			t.Fatalf("checkpoint = %q, %t, %v", loaded, found, loadErr)
		}
		return repository.Save(t.Context(), tx, binding, second, expected, true)
	}); err != nil {
		t.Fatal(err)
	}
	var stored any
	if err := database.SQLDB().QueryRowContext(t.Context(), `SELECT checkpoint FROM pc_connector_checkpoints
        WHERE scope_id = ? AND binding_id = ?`, binding.ScopeID(), binding.ID()).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	storedBytes, ok := stored.([]byte)
	if !ok || string(storedBytes) != `{"value":{"cursor":2}}` {
		t.Fatalf("stored checkpoint = %T %q", stored, stored)
	}
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		saveErr := repository.Save(t.Context(), tx, binding, first, expected, true)
		if _, ok := errors.AsType[*sqlstore.CheckpointConflictError](saveErr); !ok {
			t.Fatalf("stale checkpoint error = %T %v", saveErr, saveErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorCheckpointRepositoryRoundTripsNull(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.ConnectorCheckpointRepository{}
	binding := checkpointBinding(t, "null")
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		return repository.Save(t.Context(), tx, binding, []byte("null"), nil, false)
	}); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := database.SQLDB().QueryRowContext(t.Context(), `SELECT checkpoint FROM pc_connector_checkpoints
        WHERE scope_id = ? AND binding_id = ?`, binding.ScopeID(), binding.ID()).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) != `{"value":null}` {
		t.Fatalf("stored checkpoint = %q", stored)
	}
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		loaded, found, err := repository.Load(t.Context(), tx, binding)
		if err != nil || !found || string(loaded) != "null" {
			t.Fatalf("checkpoint = %q, %t, %v", loaded, found, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestConnectorCheckpointRepositoryRejectsInvalidBindingAtPersistenceBoundary(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.ConnectorCheckpointRepository{}
	for _, operation := range []struct {
		name string
		run  func(context.Context, sqlstore.DBTX) error
	}{
		{name: "load", run: func(ctx context.Context, tx sqlstore.DBTX) error {
			_, _, err := repository.Load(ctx, tx, source.ConnectorBinding{})
			return err
		}},
		{name: "save", run: func(ctx context.Context, tx sqlstore.DBTX) error {
			return repository.Save(ctx, tx, source.ConnectorBinding{}, []byte("null"), nil, false)
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
				return operation.run(t.Context(), tx)
			})
			if _, ok := errors.AsType[*source.InvalidConnectorBindingError](err); !ok {
				t.Fatalf("invalid binding error = %T %v", err, err)
			}
		})
	}
}

func TestConnectorCheckpointRepositoryRejectsInvalidStoredEnvelope(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload []byte
	}{
		{name: "not JSON", payload: []byte("not-json")},
		{name: "missing value", payload: []byte(`{"cursor":1}`)},
		{name: "unknown member", payload: []byte(`{"value":1,"extra":2}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openTestDatabase(t)
			binding := checkpointBinding(t, test.name)
			if _, err := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_connector_checkpoints
                (scope_id, binding_id, connector_name, connector_version, checkpoint) VALUES (?, ?, ?, ?, ?)`,
				binding.ScopeID(), binding.ID(), binding.ConnectorName(), binding.ConnectorVersion(), test.payload); err != nil {
				t.Fatal(err)
			}
			err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
				_, _, err := (sqlstore.ConnectorCheckpointRepository{}).Load(t.Context(), tx, binding)
				return err
			})
			if _, ok := errors.AsType[*sqlstore.InvalidStoredPayloadError](err); !ok {
				t.Fatalf("invalid envelope error = %T %v", err, err)
			}
			if strings.Contains(err.Error(), binding.ScopeID()) || strings.Contains(err.Error(), binding.ID()) {
				t.Fatalf("invalid envelope error disclosed binding identity: %v", err)
			}
		})
	}
}

func TestConnectorCheckpointRepositoryRejectsStoredBindingMismatchWithoutDisclosure(t *testing.T) {
	database := openTestDatabase(t)
	binding := checkpointBinding(t, "requested-secret")
	if _, err := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_connector_checkpoints
        (scope_id, binding_id, connector_name, connector_version, checkpoint) VALUES (?, ?, ?, ?, ?)`,
		binding.ScopeID(), binding.ID(), "stored-secret", "stored-version-secret", []byte(`{"value":null}`)); err != nil {
		t.Fatal(err)
	}
	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, _, err := (sqlstore.ConnectorCheckpointRepository{}).Load(t.Context(), tx, binding)
		return err
	})
	if _, ok := errors.AsType[*sqlstore.InvalidStoredPayloadError](err); !ok {
		t.Fatalf("binding mismatch error = %T %v", err, err)
	}
	for _, secret := range []string{binding.ScopeID(), binding.ID(), binding.ConnectorName(), binding.ConnectorVersion(), "stored-secret", "stored-version-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("binding mismatch error disclosed identity: %v", err)
		}
	}
}

func TestConnectorCheckpointRepositoryConcurrentCreateHasOneTypedConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint-race.db")
	first := openCheckpointDatabase(t, path)
	second := openCheckpointDatabase(t, path)
	binding := checkpointBinding(t, "race")
	repository := sqlstore.ConnectorCheckpointRepository{}
	start := make(chan struct{})
	results := make([]error, 2)
	var wg sync.WaitGroup
	checkpoints := [][]byte{[]byte(`{"writer":1}`), []byte(`{"writer":2}`)}
	ctx := t.Context()
	for index, database := range []*sqlstore.Database{first, second} {
		wg.Go(func() {
			<-start
			results[index] = database.Transaction(ctx, func(tx sqlstore.DBTX) error {
				return repository.Save(ctx, tx, binding, checkpoints[index], nil, false)
			})
		})
	}
	close(start)
	wg.Wait()

	var succeeded, conflicted int
	for _, err := range results {
		_, conflict := errors.AsType[*sqlstore.CheckpointConflictError](err)
		switch {
		case err == nil:
			succeeded++
		case conflict:
			conflicted++
			message := strings.ToLower(err.Error())
			for _, detail := range []string{"sqlite", "constraint", "locked", "busy"} {
				if strings.Contains(message, detail) {
					t.Fatalf("checkpoint conflict disclosed driver detail: %v", err)
				}
			}
		default:
			t.Fatalf("concurrent create error = %T %v", err, err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent create outcomes = success:%d conflict:%d", succeeded, conflicted)
	}
}

func TestConnectorCheckpointRepositoryPreservesCancellationDuringSave(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.ConnectorCheckpointRepository{}
	binding := checkpointBinding(t, "cancel")
	baseContext, cancel := context.WithCancel(t.Context())
	enteredDatabase := make(chan struct{})
	continueCancellation := make(chan struct{})
	ctx := &checkpointCancellationContext{
		Context: baseContext,
		beforeDone: sync.OnceFunc(func() {
			close(enteredDatabase)
			<-continueCancellation
		}),
	}
	transactionContext := t.Context()
	result := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		result <- database.Transaction(transactionContext, func(tx sqlstore.DBTX) error {
			return repository.Save(ctx, tx, binding, []byte("null"), nil, false)
		})
	})
	<-enteredDatabase
	cancel()
	close(continueCancellation)
	saveErr := <-result
	wg.Wait()

	if !errors.Is(saveErr, context.Canceled) {
		t.Fatalf("canceled checkpoint save error = %T %v", saveErr, saveErr)
	}
	if _, conflict := errors.AsType[*sqlstore.CheckpointConflictError](saveErr); conflict {
		t.Fatalf("canceled checkpoint save became a conflict: %v", saveErr)
	}
}

type checkpointCancellationContext struct {
	context.Context
	beforeDone func()
}

func (c *checkpointCancellationContext) Done() <-chan struct{} {
	c.beforeDone()
	return c.Context.Done()
}

func checkpointBinding(t *testing.T, suffix string) source.ConnectorBinding {
	t.Helper()
	binding, err := source.NewConnectorBinding("scope-secret-"+suffix, "binding-secret-"+suffix, "connector-secret", "version-secret")
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func openCheckpointDatabase(t *testing.T, path string) *sqlstore.Database {
	t.Helper()
	database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
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
