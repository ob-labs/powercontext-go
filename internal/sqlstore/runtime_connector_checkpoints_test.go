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
	"time"

	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestRuntimeConnectorCheckpointStoreDistinguishesAbsentNullAndTwoDatabaseCAS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector-lifecycle.db")
	firstDatabase := openCheckpointDatabase(t, path)
	secondDatabase := openCheckpointDatabase(t, path)
	first, err := sqlstore.NewRuntimeConnectorCheckpointStore(firstDatabase, sqlstore.ConnectorCheckpointRepository{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := sqlstore.NewRuntimeConnectorCheckpointStore(secondDatabase, sqlstore.ConnectorCheckpointRepository{})
	if err != nil {
		t.Fatal(err)
	}
	binding := checkpointBinding(t, "runtime-store")
	missing, err := first.Load(t.Context(), binding)
	if err != nil || missing.Found() {
		t.Fatalf("missing checkpoint = %#v, %v", missing, err)
	}
	nullCheckpoint, err := source.NewConnectorCheckpoint(jsontext.Value("null"))
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := first.Save(t.Context(), binding, nullCheckpoint, missing); saveErr != nil {
		t.Fatal(saveErr)
	}
	loaded, err := first.Load(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if value, found := loaded.Value(); !found || string(value) != "null" {
		t.Fatalf("stored null = %q, found=%t", value, found)
	}

	stale, err := second.Load(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	firstNext, err := source.NewConnectorCheckpoint(jsontext.Value(`{"offset":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if saveErr := first.Save(t.Context(), binding, firstNext, loaded); saveErr != nil {
		t.Fatal(saveErr)
	}
	secondNext, err := source.NewConnectorCheckpoint(jsontext.Value(`{"offset":9007199254740994}`))
	if err != nil {
		t.Fatal(err)
	}
	err = second.Save(t.Context(), binding, secondNext, stale)
	if _, ok := errors.AsType[*sqlstore.CheckpointConflictError](err); !ok {
		t.Fatalf("stale two-database save error = %T %v", err, err)
	}
	final, err := first.Load(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	value, found := final.Value()
	if !found || string(value) != `{"offset":9007199254740993}` {
		t.Fatalf("final checkpoint = %q, found=%t", value, found)
	}
}

func TestConnectorLifecycleSQLiteCommitsSourceBeforeCheckpointAndRetriesIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connector-source-before-checkpoint.db")
	database := openCheckpointDatabase(t, path)
	secondDatabase := openCheckpointDatabase(t, path)
	sources, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := sqlstore.NewRuntimeRemoteIngestionBackend(database, sqlstore.DefinitionManifestRepository{}, sources)
	if err != nil {
		t.Fatal(err)
	}
	runtime := pcruntime.New()
	ingestion, err := pcruntime.NewRemoteIngestionApplication(runtime, backend)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlstore.NewRuntimeConnectorCheckpointStore(database, sqlstore.ConnectorCheckpointRepository{})
	if err != nil {
		t.Fatal(err)
	}
	secondStore, err := sqlstore.NewRuntimeConnectorCheckpointStore(secondDatabase, sqlstore.ConnectorCheckpointRepository{})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := pcruntime.NewConnectorLifecycleApplication(runtime, ingestion, store)
	if err != nil {
		t.Fatal(err)
	}
	manifest := lifecycleStoredManifest(t, "remote.connector")
	if _, registerErr := ingestion.Register(t.Context(), manifest); registerErr != nil {
		t.Fatal(registerErr)
	}
	accepted := lifecycleStoredObservation(t, manifest, "item-1", "one")
	binding, err := source.NewConnectorBinding("scope-connector", "binding-connector", "connector", "v1")
	if err != nil {
		t.Fatal(err)
	}
	connector := sqliteLifecycleConnector{
		name: "connector", version: "v1", definitions: []string{manifest.Name()},
		run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			if _, submitErr := session.Submit(ctx, "provider-item", manifest.Name(), accepted); submitErr != nil {
				return source.ConnectorRunCompletion{}, submitErr
			}
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":1}`))
		},
	}
	if _, triggerErr := database.SQLDB().ExecContext(t.Context(), `CREATE TRIGGER fail_connector_checkpoint
        BEFORE INSERT ON pc_connector_checkpoints BEGIN SELECT RAISE(ABORT, 'checkpoint write blocked'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, runErr := lifecycle.Run(ctx, connector, binding); runErr == nil {
		t.Fatal("checkpoint trigger failure was swallowed")
	}
	assertConnectorStoredSources(t, database, binding.ScopeID(), 1, 1)
	assertConnectorCheckpointRows(t, database, binding, 0)
	if _, dropErr := database.SQLDB().ExecContext(t.Context(), "DROP TRIGGER fail_connector_checkpoint"); dropErr != nil {
		t.Fatal(dropErr)
	}

	result, err := lifecycle.Run(t.Context(), connector, binding)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != source.ConnectorRunComplete || len(result.Items()) != 1 || result.Items()[0].Status() != source.ConnectorSubmissionAccepted {
		t.Fatalf("retry result = %#v", result)
	}
	assertConnectorStoredSources(t, database, binding.ScopeID(), 1, 1)
	assertConnectorCheckpointRows(t, database, binding, 1)
	checkpoint, err := store.Load(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if value, found := checkpoint.Value(); !found || string(value) != `{"offset":1}` {
		t.Fatalf("retry checkpoint = %q, found=%t", value, found)
	}

	conflictSource := lifecycleStoredObservation(t, manifest, "item-2", "two")
	competingCheckpoint, err := source.NewConnectorCheckpoint(jsontext.Value(`{"offset":3}`))
	if err != nil {
		t.Fatal(err)
	}
	conflictingStore := sqliteCheckpointConflictStore{
		primary: store, competing: secondStore, next: competingCheckpoint,
	}
	conflictLifecycle, err := pcruntime.NewConnectorLifecycleApplication(runtime, ingestion, &conflictingStore)
	if err != nil {
		t.Fatal(err)
	}
	casConflictConnector := sqliteLifecycleConnector{
		name: "connector", version: "v1", definitions: []string{manifest.Name()},
		run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			if _, submitErr := session.Submit(ctx, "provider-cas-conflict", manifest.Name(), conflictSource); submitErr != nil {
				return source.ConnectorRunCompletion{}, submitErr
			}
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":2}`))
		},
	}
	_, err = conflictLifecycle.Run(t.Context(), casConflictConnector, binding)
	if _, ok := errors.AsType[*sqlstore.CheckpointConflictError](err); !ok {
		t.Fatalf("lifecycle CAS conflict = %T %v", err, err)
	}
	assertConnectorStoredSources(t, database, binding.ScopeID(), 2, 2)
	checkpoint, err = store.Load(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if value, found := checkpoint.Value(); !found || string(value) != `{"offset":3}` {
		t.Fatalf("CAS conflict checkpoint = %q, found=%t", value, found)
	}

	conflicting := lifecycleStoredObservation(t, manifest, "item-1", "different")
	conflictConnector := sqliteLifecycleConnector{
		name: "connector", version: "v1", definitions: []string{manifest.Name()},
		run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			if _, submitErr := session.Submit(ctx, "provider-conflict", manifest.Name(), conflicting); submitErr != nil {
				return source.ConnectorRunCompletion{}, submitErr
			}
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":2}`))
		},
	}
	conflictResult, err := lifecycle.Run(t.Context(), conflictConnector, binding)
	if err != nil {
		t.Fatal(err)
	}
	if conflictResult.Status() != source.ConnectorRunIncomplete || len(conflictResult.Items()) != 1 || conflictResult.Items()[0].Status() != source.ConnectorSubmissionRejected {
		t.Fatalf("conflict result = %#v", conflictResult)
	}
	checkpoint, err = store.Load(t.Context(), binding)
	if err != nil {
		t.Fatal(err)
	}
	if value, found := checkpoint.Value(); !found || string(value) != `{"offset":3}` {
		t.Fatalf("rejected item advanced checkpoint to %q, found=%t", value, found)
	}
}

func TestConnectorLifecycleSQLiteDrainsInFlightSubmitBeforeCheckpoint(t *testing.T) {
	genericSubmissionError := errors.New("durable source write failed")
	for _, test := range []struct {
		name              string
		submissionError   error
		wantRunError      error
		wantStatus        source.ConnectorRunStatus
		wantSources       int
		wantCheckpointRow int
	}{
		{
			name:              "accepted source settles before checkpoint",
			wantStatus:        source.ConnectorRunComplete,
			wantSources:       1,
			wantCheckpointRow: 1,
		},
		{
			name:              "typed rejection after drain leaves checkpoint absent",
			submissionError:   &source.ObservationConflictError{},
			wantStatus:        source.ConnectorRunIncomplete,
			wantSources:       0,
			wantCheckpointRow: 0,
		},
		{
			name:              "generic failure after drain leaves checkpoint absent",
			submissionError:   genericSubmissionError,
			wantRunError:      genericSubmissionError,
			wantSources:       0,
			wantCheckpointRow: 0,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openCheckpointDatabase(t, filepath.Join(t.TempDir(), "connector-drain.db"))
			sources, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect)
			if err != nil {
				t.Fatal(err)
			}
			backend, err := sqlstore.NewRuntimeRemoteIngestionBackend(database, sqlstore.DefinitionManifestRepository{}, sources)
			if err != nil {
				t.Fatal(err)
			}
			runtime := pcruntime.New()
			ingestion, err := pcruntime.NewRemoteIngestionApplication(runtime, backend)
			if err != nil {
				t.Fatal(err)
			}
			manifest := lifecycleStoredManifest(t, "remote.connector.drain")
			if _, registerErr := ingestion.Register(t.Context(), manifest); registerErr != nil {
				t.Fatal(registerErr)
			}
			store, err := sqlstore.NewRuntimeConnectorCheckpointStore(database, sqlstore.ConnectorCheckpointRepository{})
			if err != nil {
				t.Fatal(err)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			releaseSubmit := sync.OnceFunc(func() { close(release) })
			t.Cleanup(releaseSubmit)
			gatedIngestion := &checkpointDrainIngestion{
				delegate: ingestion, entered: entered, release: release, err: test.submissionError,
			}
			observedStore := &checkpointDrainStore{delegate: store, saveStarted: make(chan struct{})}
			lifecycle, err := pcruntime.NewConnectorLifecycleApplication(runtime, gatedIngestion, observedStore)
			if err != nil {
				t.Fatal(err)
			}
			binding, err := source.NewConnectorBinding("scope-connector-drain", "binding-connector-drain", "connector", "v1")
			if err != nil {
				t.Fatal(err)
			}
			accepted := lifecycleStoredObservation(t, manifest, "item-1", "one")
			submitted := make(chan error, 1)
			connectorReturned := make(chan struct{})
			connector := sqliteLifecycleConnector{
				name: "connector", version: "v1", definitions: []string{manifest.Name()},
				run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
					go func() {
						_, submitErr := session.Submit(ctx, "provider-item", manifest.Name(), accepted)
						submitted <- submitErr
					}()
					<-entered
					close(connectorReturned)
					return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":1}`))
				},
			}
			type lifecycleResult struct {
				result source.ConnectorRunResult
				err    error
			}
			finished := make(chan lifecycleResult, 1)
			go func() {
				result, runErr := lifecycle.Run(t.Context(), connector, binding)
				finished <- lifecycleResult{result: result, err: runErr}
			}()
			<-connectorReturned

			select {
			case completed := <-finished:
				t.Fatalf("lifecycle completed before blocked Submit settled: result=%#v err=%v", completed.result, completed.err)
			case <-time.After(100 * time.Millisecond):
			}
			assertConnectorCheckpointRows(t, database, binding, 0)
			select {
			case <-observedStore.saveStarted:
				t.Fatal("checkpoint Save began before blocked Submit settled")
			default:
			}

			releaseSubmit()
			completed := <-finished
			submitErr := <-submitted
			if test.wantRunError != nil {
				if !errors.Is(completed.err, test.wantRunError) {
					t.Fatalf("Run() error = %T %v, want %v", completed.err, completed.err, test.wantRunError)
				}
				if !errors.Is(submitErr, test.wantRunError) {
					t.Fatalf("Submit() error = %T %v, want %v", submitErr, submitErr, test.wantRunError)
				}
			} else {
				if completed.err != nil {
					t.Fatal(completed.err)
				}
				if submitErr != nil {
					t.Fatal(submitErr)
				}
				if completed.result.Status() != test.wantStatus {
					t.Fatalf("status = %q, want %q", completed.result.Status(), test.wantStatus)
				}
			}
			assertConnectorStoredSources(t, database, binding.ScopeID(), test.wantSources, test.wantSources)
			assertConnectorCheckpointRows(t, database, binding, test.wantCheckpointRow)
			if test.wantCheckpointRow == 1 {
				select {
				case <-observedStore.saveStarted:
				default:
					t.Fatal("accepted Source settlement did not lead to checkpoint Save")
				}
			} else {
				select {
				case <-observedStore.saveStarted:
					t.Fatal("unsafe settled Submit began checkpoint Save")
				default:
				}
			}
		})
	}
}

func TestConnectorLifecycleSQLiteRejectsShadowedNativeDefinitionWithoutAdvancingCheckpoint(t *testing.T) {
	database := openCheckpointDatabase(t, filepath.Join(t.TempDir(), "connector-shadowed-native.db"))
	sources, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	backend, err := sqlstore.NewRuntimeRemoteIngestionBackend(database, sqlstore.DefinitionManifestRepository{}, sources)
	if err != nil {
		t.Fatal(err)
	}
	// Persist directly to model a remote Definition that predates the native codec.
	manifest := lifecycleStoredManifest(t, source.ContentType)
	if _, registerErr := backend.Register(t.Context(), manifest); registerErr != nil {
		t.Fatal(registerErr)
	}
	ingestion, err := pcruntime.NewRemoteIngestionApplication(pcruntime.New(), backend)
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlstore.NewRuntimeConnectorCheckpointStore(database, sqlstore.ConnectorCheckpointRepository{})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := pcruntime.NewConnectorLifecycleApplication(pcruntime.New(), ingestion, store)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := source.NewConnectorBinding("scope-shadowed-native", "binding-shadowed-native", "connector", "v1")
	if err != nil {
		t.Fatal(err)
	}
	accepted := lifecycleStoredObservation(t, manifest, "item-1", "value")
	connector := sqliteLifecycleConnector{
		name: "connector", version: "v1", definitions: []string{source.ContentType},
		run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			if _, submitErr := session.Submit(ctx, "provider-item", source.ContentType, accepted); submitErr != nil {
				return source.ConnectorRunCompletion{}, submitErr
			}
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":1}`))
		},
	}

	result, err := lifecycle.Run(t.Context(), connector, binding)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != source.ConnectorRunIncomplete || len(result.Items()) != 1 || result.Items()[0].Status() != source.ConnectorSubmissionRejected {
		t.Fatalf("shadowed native lifecycle result = %#v", result)
	}
	assertConnectorStoredSources(t, database, binding.ScopeID(), 0, 0)
	assertConnectorCheckpointRows(t, database, binding, 0)
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		position, positionErr := sources.JournalPosition(t.Context(), tx, binding.ScopeID())
		if positionErr != nil {
			return positionErr
		}
		if position != 0 {
			t.Fatalf("shadowed lifecycle journal position = %d, want 0", position)
		}
		return nil
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
}

type sqliteCheckpointConflictStore struct {
	primary   *sqlstore.RuntimeConnectorCheckpointStore
	competing *sqlstore.RuntimeConnectorCheckpointStore
	next      source.ConnectorCheckpoint
}

func (s *sqliteCheckpointConflictStore) Load(
	ctx context.Context,
	binding source.ConnectorBinding,
) (source.ConnectorCheckpoint, error) {
	return s.primary.Load(ctx, binding)
}

func (s *sqliteCheckpointConflictStore) Save(
	ctx context.Context,
	binding source.ConnectorBinding,
	next, expected source.ConnectorCheckpoint,
) error {
	if err := s.competing.Save(ctx, binding, s.next, expected); err != nil {
		return err
	}
	return s.primary.Save(ctx, binding, next, expected)
}

type checkpointDrainIngestion struct {
	delegate *pcruntime.RemoteIngestionApplication
	entered  chan<- struct{}
	release  <-chan struct{}
	err      error
	once     sync.Once
}

func (s *checkpointDrainIngestion) SubmitAccepted(
	ctx context.Context,
	scopeID string,
	accepted *source.AdmittedObservation,
) (pcruntime.SourceReceipt, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return pcruntime.SourceReceipt{}, ctx.Err()
	}
	if s.err != nil {
		return pcruntime.SourceReceipt{}, s.err
	}
	return s.delegate.SubmitAccepted(ctx, scopeID, accepted)
}

type checkpointDrainStore struct {
	delegate    *sqlstore.RuntimeConnectorCheckpointStore
	saveStarted chan struct{}
	once        sync.Once
}

func (s *checkpointDrainStore) Load(
	ctx context.Context,
	binding source.ConnectorBinding,
) (source.ConnectorCheckpoint, error) {
	return s.delegate.Load(ctx, binding)
}

func (s *checkpointDrainStore) Save(
	ctx context.Context,
	binding source.ConnectorBinding,
	next, expected source.ConnectorCheckpoint,
) error {
	s.once.Do(func() { close(s.saveStarted) })
	return s.delegate.Save(ctx, binding, next, expected)
}

type sqliteLifecycleConnector struct {
	name        string
	version     string
	definitions []string
	run         func(context.Context, *source.ConnectorRunSession) (source.ConnectorRunCompletion, error)
}

func (c sqliteLifecycleConnector) Name() string                { return c.name }
func (c sqliteLifecycleConnector) Version() string             { return c.version }
func (c sqliteLifecycleConnector) SourceDefinitions() []string { return c.definitions }
func (c sqliteLifecycleConnector) Run(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
	return c.run(ctx, session)
}

func lifecycleStoredManifest(t *testing.T, name string) source.DefinitionManifest {
	t.Helper()
	identity, err := source.NewDefinitionIdentity(name, "1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"type":"object","required":["name","definition_version","materialization","value"],"properties":{"name":{"type":"string"},"definition_version":{"type":"string"},"materialization":{"const":"captured"},"value":{"type":"string"}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func lifecycleStoredObservation(
	t *testing.T,
	manifest source.DefinitionManifest,
	identifier, value string,
) *source.AdmittedObservation {
	t.Helper()
	ref, err := source.NewRef(manifest.Name(), identifier)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"`+identifier+`","definition_version":"1","materialization":"captured","value":"`+value+`"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := source.AdmitObservation(manifest, observation)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}

func assertConnectorStoredSources(t *testing.T, database *sqlstore.Database, scopeID string, sources, acceptances int) {
	t.Helper()
	var sourceCount, acceptanceCount, highestPosition int
	if err := database.SQLDB().QueryRowContext(t.Context(), "SELECT count(*) FROM pc_sources WHERE scope_id = ?", scopeID).Scan(&sourceCount); err != nil {
		t.Fatal(err)
	}
	if err := database.SQLDB().QueryRowContext(t.Context(), "SELECT count(*) FROM pc_source_observation_acceptances WHERE scope_id = ?", scopeID).Scan(&acceptanceCount); err != nil {
		t.Fatal(err)
	}
	if err := database.SQLDB().QueryRowContext(t.Context(), "SELECT COALESCE(MAX(journal_position), 0) FROM pc_sources WHERE scope_id = ?", scopeID).Scan(&highestPosition); err != nil {
		t.Fatal(err)
	}
	if sourceCount != sources || acceptanceCount != acceptances {
		t.Fatalf("stored sources=%d accepted=%d, want sources=%d accepted=%d", sourceCount, acceptanceCount, sources, acceptances)
	}
	if highestPosition != sources {
		t.Fatalf("highest journal position=%d, want %d after idempotent source writes", highestPosition, sources)
	}
}

func assertConnectorCheckpointRows(t *testing.T, database *sqlstore.Database, binding source.ConnectorBinding, want int) {
	t.Helper()
	var count int
	if err := database.SQLDB().QueryRowContext(t.Context(), `SELECT count(*) FROM pc_connector_checkpoints
        WHERE scope_id = ? AND binding_id = ?`, binding.ScopeID(), binding.ID()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("checkpoint rows=%d, want %d", count, want)
	}
}
