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
	"encoding/json/jsontext"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestConnectorCheckpointApplicationSQLiteWireNullAndSemanticCAS(t *testing.T) {
	for _, test := range []struct {
		name, stored, expected, next, want string
		conflict                           bool
	}{
		{name: "absent to null", expected: "null", next: "null", want: "null"},
		{name: "absent to object", expected: "null", next: `{"offset":1}`, want: `{"offset":1}`},
		{name: "stored null to object", stored: "null", expected: " null ", next: `{"offset":1}`, want: `{"offset":1}`},
		{name: "stored null to null", stored: "null", expected: "null", next: "null", want: "null"},
		{name: "object to null", stored: `{"offset":1}`, expected: `{"offset":1}`, next: "null", want: "null"},
		{name: "ordered object and exact numeric equality", stored: `{"z":true,"offset":9007199254740993}`, expected: `{"offset":9007199254740993.0,"z":true}`, next: `{"offset":9007199254740994}`, want: `{"offset":9007199254740994}`},
		{name: "large integer mismatch", stored: `{"offset":9007199254740993}`, expected: `{"offset":9007199254740992}`, next: "null", want: `{"offset":9007199254740993}`, conflict: true},
		{name: "null cannot overwrite object", stored: `{"offset":1}`, expected: "null", next: "null", want: `{"offset":1}`, conflict: true},
		{name: "object cannot match absent", expected: `{"offset":1}`, next: "null", conflict: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "wire-checkpoint.db")
			first := openCheckpointDatabase(t, path)
			second := openCheckpointDatabase(t, path)
			binding := checkpointBinding(t, "wire")
			createCheckpointScope(t, first, binding.ScopeID())
			if test.stored != "" {
				insertCheckpointPayload(t, first, binding, []byte(`{"value":`+test.stored+`}`))
			}
			application := sqliteCheckpointApplication(t, first, nil)
			reader := sqliteCheckpointApplication(t, second, nil)
			before, err := reader.Get(t.Context(), binding)
			if err != nil || before.Found() != (test.stored != "") {
				t.Fatalf("Get lost absent/null distinction: found=%t err=%v", before.Found(), err)
			}
			result, err := application.Commit(t.Context(), binding, jsontext.Value(test.next), jsontext.Value(test.expected))
			_, conflict := errors.AsType[*pcruntime.ConnectorCheckpointConflictError](err)
			if conflict != test.conflict || (!test.conflict && err != nil) {
				t.Fatalf("Commit error = %T %v, want conflict=%t", err, err, test.conflict)
			}
			if test.conflict && result.Found() {
				t.Fatal("failed Commit returned a successful checkpoint")
			}
			if !test.conflict {
				value, found := result.Value()
				if !found || string(value) != test.want {
					t.Fatalf("Commit result = %q, found=%t", value, found)
				}
			}
			final, err := reader.Get(t.Context(), binding)
			if err != nil {
				t.Fatal(err)
			}
			value, found := final.Value()
			if found != (test.want != "") || string(value) != test.want {
				t.Fatalf("durable checkpoint = %q, found=%t; want %q", value, found, test.want)
			}
		})
	}
}

func TestConnectorCheckpointApplicationSQLiteRejectsStaleConcurrentCommit(t *testing.T) {
	for _, test := range []struct {
		name, initial string
		otherBinding  bool
	}{
		{name: "concurrent create"},
		{name: "concurrent update", initial: `{"offset":1}`},
		{name: "concurrent binding claim", otherBinding: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "concurrent-checkpoint.db")
			first := openCheckpointDatabase(t, path)
			second := openCheckpointDatabase(t, path)
			binding := checkpointBinding(t, "stale")
			createCheckpointScope(t, first, binding.ScopeID())
			expected := "null"
			if test.initial != "" {
				insertCheckpointPayload(t, first, binding, []byte(`{"value":`+test.initial+`}`))
				expected = test.initial
			}
			store, err := sqlstore.NewRuntimeConnectorCheckpointStore(first, sqlstore.ConnectorCheckpointRepository{})
			if err != nil {
				t.Fatal(err)
			}
			loaded, release := make(chan struct{}), make(chan struct{})
			releaseLoad := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseLoad)
			paused := &checkpointPausedLoad{delegate: store, loaded: loaded, release: release}
			stale := sqliteCheckpointApplication(t, first, paused)
			winner := sqliteCheckpointApplication(t, second, nil)
			winnerBinding := binding
			if test.otherBinding {
				winnerBinding, err = source.NewConnectorBinding(binding.ScopeID(), binding.ID(), "other-connector", binding.ConnectorVersion())
				if err != nil {
					t.Fatal(err)
				}
			}
			finished := make(chan error, 1)
			go func() {
				_, commitErr := stale.Commit(t.Context(), binding, jsontext.Value(`{"offset":3}`), jsontext.Value(expected))
				finished <- commitErr
			}()
			<-loaded
			if _, commitErr := winner.Commit(t.Context(), winnerBinding, jsontext.Value(`{"offset":2}`), jsontext.Value(expected)); commitErr != nil {
				t.Fatal(commitErr)
			}
			releaseLoad()
			if commitErr := <-finished; commitErr == nil {
				t.Fatal("stale Runtime overwrote a competing commit")
			} else if _, ok := errors.AsType[*pcruntime.ConnectorCheckpointConflictError](commitErr); !ok {
				t.Fatalf("stale commit = %T %v", commitErr, commitErr)
			}
			final, err := winner.Get(t.Context(), winnerBinding)
			if err != nil {
				t.Fatal(err)
			}
			if value, found := final.Value(); !found || string(value) != `{"offset":2}` {
				t.Fatalf("competing commit lost: %q, found=%t", value, found)
			}
		})
	}
}

func TestConnectorCheckpointApplicationSQLiteClassifiesBindingAndCorruption(t *testing.T) {
	for _, test := range []struct {
		name, connector, version, payload string
		conflict                          bool
	}{
		{name: "binding identity", connector: "other-connector", payload: `{"value":null}`, conflict: true},
		{name: "binding version", connector: "connector-secret", version: "other-version", payload: `{"value":null}`, conflict: true},
		{name: "corrupt envelope", connector: "connector-secret", payload: `{"wrong":null}`},
		{name: "corrupt mismatched binding", connector: "other-connector", payload: `{"wrong":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openCheckpointDatabase(t, filepath.Join(t.TempDir(), "invalid-checkpoint.db"))
			binding := checkpointBinding(t, "invalid")
			createCheckpointScope(t, database, binding.ScopeID())
			version := binding.ConnectorVersion()
			if test.version != "" {
				version = test.version
			}
			storedBinding, err := source.NewConnectorBinding(binding.ScopeID(), binding.ID(), test.connector, version)
			if err != nil {
				t.Fatal(err)
			}
			insertCheckpointPayload(t, database, storedBinding, []byte(test.payload))
			application := sqliteCheckpointApplication(t, database, nil)
			for _, operation := range []string{"get", "commit"} {
				var result source.ConnectorCheckpoint
				if operation == "get" {
					result, err = application.Get(t.Context(), binding)
				} else {
					result, err = application.Commit(t.Context(), binding, jsontext.Value("null"), jsontext.Value("null"))
				}
				_, conflict := errors.AsType[*pcruntime.ConnectorCheckpointConflictError](err)
				_, corrupt := errors.AsType[*sqlstore.InvalidStoredPayloadError](err)
				if result.Found() || conflict != test.conflict || corrupt == test.conflict {
					t.Fatalf("%s returned found=%t err=%T %v", operation, result.Found(), err, err)
				}
			}
			var final []byte
			if queryErr := database.SQLDB().QueryRowContext(t.Context(), "SELECT checkpoint FROM pc_connector_checkpoints").Scan(&final); queryErr != nil {
				t.Fatal(queryErr)
			}
			if string(final) != test.payload {
				t.Fatalf("invalid row changed to %q", final)
			}
		})
	}
}

func TestConnectorCheckpointApplicationSQLiteUnknownScopeDoesNotTouchCheckpoint(t *testing.T) {
	database := openCheckpointDatabase(t, filepath.Join(t.TempDir(), "unknown-scope.db"))
	binding := checkpointBinding(t, "unknown")
	// A corrupt checkpoint under an unknown Scope makes any premature load visible.
	insertCheckpointPayload(t, database, binding, []byte("broken"))
	application := sqliteCheckpointApplication(t, database, nil)
	_, getErr := application.Get(t.Context(), binding)
	_, commitErr := application.Commit(t.Context(), binding, jsontext.Value("null"), jsontext.Value("null"))
	for _, err := range []error{getErr, commitErr} {
		if _, ok := errors.AsType[*scope.NotFoundError](err); !ok {
			t.Fatalf("unknown Scope = %T %v", err, err)
		}
	}
}

func sqliteCheckpointApplication(t *testing.T, database *sqlstore.Database, store pcruntime.ConnectorCheckpointStore) *pcruntime.ConnectorCheckpointApplication {
	t.Helper()
	reader, err := sqlstore.NewRuntimeScopeReader(database, sqlstore.ScopeRepository{})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := pcruntime.NewConfigured(pcruntime.RuntimeOptions{ScopeReader: reader}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		store, err = sqlstore.NewRuntimeConnectorCheckpointStore(database, sqlstore.ConnectorCheckpointRepository{})
		if err != nil {
			t.Fatal(err)
		}
	}
	application, err := pcruntime.NewConnectorCheckpointApplication(lifecycle, store)
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func createCheckpointScope(t *testing.T, database *sqlstore.Database, id string) {
	t.Helper()
	draft, err := scope.NewDraft("Checkpoint Scope", "Checkpoint test scope", "", nil, nil, id)
	if err != nil {
		t.Fatal(err)
	}
	if createErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, operationErr := (sqlstore.ScopeRepository{}).Create(t.Context(), tx, id, draft)
		return operationErr
	}); createErr != nil {
		t.Fatal(createErr)
	}
}

func insertCheckpointPayload(t *testing.T, database *sqlstore.Database, binding source.ConnectorBinding, payload []byte) {
	t.Helper()
	if _, err := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_connector_checkpoints
		(scope_id, binding_id, connector_name, connector_version, checkpoint) VALUES (?, ?, ?, ?, ?)`,
		binding.ScopeID(), binding.ID(), binding.ConnectorName(), binding.ConnectorVersion(), payload); err != nil {
		t.Fatal(err)
	}
}

type checkpointPausedLoad struct {
	delegate pcruntime.ConnectorCheckpointStore
	loaded   chan<- struct{}
	release  <-chan struct{}
}

func (s *checkpointPausedLoad) Load(ctx context.Context, binding source.ConnectorBinding) (source.ConnectorCheckpoint, error) {
	value, err := s.delegate.Load(ctx, binding)
	close(s.loaded)
	select {
	case <-s.release:
	case <-ctx.Done():
		return source.NoConnectorCheckpoint(), ctx.Err()
	}
	return value, err
}

func (s *checkpointPausedLoad) Save(ctx context.Context, binding source.ConnectorBinding, next, expected source.ConnectorCheckpoint) error {
	return s.delegate.Save(ctx, binding, next, expected)
}
