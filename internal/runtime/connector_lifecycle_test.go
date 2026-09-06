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

package runtime

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"sync"
	"testing"

	"github.com/ob-labs/powercontext-go/source"
)

func TestConnectorLifecycleCommitsOnlyAfterDurableAcceptedSource(t *testing.T) {
	binding := lifecycleBinding(t)
	accepted := lifecycleAcceptedObservation(t, "remote.note", "item-1")
	previous, err := source.NewConnectorCheckpoint(jsontext.Value(`{"offset":1}`))
	if err != nil {
		t.Fatal(err)
	}
	store := &lifecycleCheckpointStore{value: previous}
	ingestion := &lifecycleIngestion{receipt: SourceReceipt{Ref: accepted.Observation().Ref(), Sequence: 4}}
	application, err := NewConnectorLifecycleApplication(New(), ingestion, store)
	if err != nil {
		t.Fatal(err)
	}
	connector := lifecycleConnector{
		name: "connector", version: "v1", definitions: []string{"remote.note"},
		run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			if _, submitErr := session.Submit(ctx, "provider-item", "remote.note", accepted); submitErr != nil {
				return source.ConnectorRunCompletion{}, submitErr
			}
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":2}`))
		},
	}
	result, err := application.Run(t.Context(), connector, binding)
	if err != nil {
		t.Fatal(err)
	}
	if ingestion.calls != 1 || store.saves != 1 {
		t.Fatalf("ingestion calls=%d checkpoint saves=%d", ingestion.calls, store.saves)
	}
	if len(store.events) != 2 || store.events[0] != "load" || store.events[1] != "save" {
		t.Fatalf("checkpoint events = %#v", store.events)
	}
	if result.Status() != source.ConnectorRunComplete || len(result.Items()) != 1 || result.Items()[0].Status() != source.ConnectorSubmissionAccepted {
		t.Fatalf("result = %#v", result)
	}
	committed, found := result.CommittedCheckpoint().Value()
	if !found || string(committed) != `{"offset":2}` {
		t.Fatalf("committed checkpoint = %q, found=%t", committed, found)
	}
}

func TestConnectorLifecycleNeverAdvancesUnsafeOrUnchangedCheckpoint(t *testing.T) {
	binding := lifecycleBinding(t)
	accepted := lifecycleAcceptedObservation(t, "remote.note", "item-1")
	previous, err := source.NewConnectorCheckpoint(jsontext.Value(`{"offset":9007199254740993,"tag":"same"}`))
	if err != nil {
		t.Fatal(err)
	}
	rejection, err := source.NewConnectorSubmissionRejectedError("provider rejected item")
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name       string
		completion source.ConnectorRunStatus
		checkpoint jsontext.Value
		run        func(context.Context, *source.ConnectorRunSession) error
		wantStatus source.ConnectorRunStatus
		wantSaves  int
	}{
		{
			name: "value equivalent checkpoint", completion: source.ConnectorRunComplete,
			checkpoint: jsontext.Value(`{"tag":"same","offset":9007199254740993.0}`), wantStatus: source.ConnectorRunComplete,
		},
		{
			name: "incomplete run", completion: source.ConnectorRunIncomplete,
			checkpoint: jsontext.Value(`{"offset":2}`), wantStatus: source.ConnectorRunIncomplete,
		},
		{
			name: "provider rejection", completion: source.ConnectorRunComplete,
			checkpoint: jsontext.Value(`{"offset":2}`), wantStatus: source.ConnectorRunIncomplete,
			run: func(_ context.Context, session *source.ConnectorRunSession) error {
				_, runErr := session.Reject("provider-item", "remote.note", "provider rejected item")
				return runErr
			},
		},
		{
			name: "provider failure", completion: source.ConnectorRunComplete,
			checkpoint: jsontext.Value(`{"offset":2}`), wantStatus: source.ConnectorRunIncomplete,
			run: func(_ context.Context, session *source.ConnectorRunSession) error {
				_, runErr := session.Fail("provider-item", "remote.note", "provider unavailable")
				return runErr
			},
		},
		{
			name: "typed durable rejection", completion: source.ConnectorRunComplete,
			checkpoint: jsontext.Value(`{"offset":2}`), wantStatus: source.ConnectorRunIncomplete,
			run: func(ctx context.Context, session *source.ConnectorRunSession) error {
				_, runErr := session.Submit(ctx, "provider-item", "remote.note", accepted)
				return runErr
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &lifecycleCheckpointStore{value: previous}
			ingestion := &lifecycleIngestion{receipt: SourceReceipt{Ref: accepted.Observation().Ref(), Sequence: 1}}
			if test.name == "typed durable rejection" {
				ingestion.err = rejection
			}
			application, newErr := NewConnectorLifecycleApplication(New(), ingestion, store)
			if newErr != nil {
				t.Fatal(newErr)
			}
			connector := lifecycleConnector{
				name: "connector", version: "v1", definitions: []string{"remote.note"},
				run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
					if test.run != nil {
						if runErr := test.run(ctx, session); runErr != nil {
							return source.ConnectorRunCompletion{}, runErr
						}
					}
					return source.NewConnectorRunCompletion(test.completion, test.checkpoint)
				},
			}
			result, runErr := application.Run(t.Context(), connector, binding)
			if runErr != nil {
				t.Fatal(runErr)
			}
			if result.Status() != test.wantStatus || store.saves != test.wantSaves {
				t.Fatalf("status=%q saves=%d", result.Status(), store.saves)
			}
			committed, found := result.CommittedCheckpoint().Value()
			before, beforeFound := previous.Value()
			if !found || !beforeFound || string(committed) != string(before) {
				t.Fatalf("unsafe result committed=%q found=%t, want previous=%q", committed, found, before)
			}
		})
	}
}

func TestConnectorLifecyclePreservesSourceWhenCheckpointConflictsOrRunFails(t *testing.T) {
	binding := lifecycleBinding(t)
	accepted := lifecycleAcceptedObservation(t, "remote.note", "item-1")
	previous := source.NoConnectorCheckpoint()
	for _, test := range []struct {
		name     string
		storeErr error
		runErr   error
		wantErr  error
	}{
		{name: "checkpoint conflict", storeErr: errors.New("checkpoint conflict")},
		{name: "canceled provider", runErr: context.Canceled, wantErr: context.Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &lifecycleCheckpointStore{value: previous, saveErr: test.storeErr}
			ingestion := &lifecycleIngestion{receipt: SourceReceipt{Ref: accepted.Observation().Ref(), Sequence: 3}}
			application, err := NewConnectorLifecycleApplication(New(), ingestion, store)
			if err != nil {
				t.Fatal(err)
			}
			connector := lifecycleConnector{
				name: "connector", version: "v1", definitions: []string{"remote.note"},
				run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
					if _, submitErr := session.Submit(ctx, "provider-item", "remote.note", accepted); submitErr != nil {
						return source.ConnectorRunCompletion{}, submitErr
					}
					if test.runErr != nil {
						return source.ConnectorRunCompletion{}, test.runErr
					}
					return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":1}`))
				},
			}
			_, runErr := application.Run(t.Context(), connector, binding)
			if runErr == nil {
				t.Fatal("unsafe lifecycle error was swallowed")
			}
			if test.wantErr != nil && !errors.Is(runErr, test.wantErr) {
				t.Fatalf("Run() error = %T %v, want %v", runErr, runErr, test.wantErr)
			}
			if ingestion.calls != 1 {
				t.Fatalf("durable source was not submitted before lifecycle failure: %d", ingestion.calls)
			}
			if test.storeErr != nil && store.saves != 1 {
				t.Fatalf("checkpoint save attempts = %d", store.saves)
			}
			if test.runErr != nil && store.saves != 0 {
				t.Fatalf("checkpoint save attempts after provider failure = %d", store.saves)
			}
		})
	}
}

func TestConnectorLifecycleRejectsWrongDurableSourceReference(t *testing.T) {
	binding := lifecycleBinding(t)
	accepted := lifecycleAcceptedObservation(t, "remote.note", "item-1")
	wrongRef, err := source.NewRef("remote.note", "other")
	if err != nil {
		t.Fatal(err)
	}
	store := &lifecycleCheckpointStore{value: source.NoConnectorCheckpoint()}
	ingestion := &lifecycleIngestion{receipt: SourceReceipt{Ref: wrongRef, Sequence: 1}}
	application, err := NewConnectorLifecycleApplication(New(), ingestion, store)
	if err != nil {
		t.Fatal(err)
	}
	connector := lifecycleConnector{
		name: "connector", version: "v1", definitions: []string{"remote.note"},
		run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			if _, submitErr := session.Submit(ctx, "provider-item", "remote.note", accepted); submitErr != nil {
				return source.ConnectorRunCompletion{}, submitErr
			}
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value("null"))
		},
	}
	_, err = application.Run(t.Context(), connector, binding)
	if _, ok := errors.AsType[*source.InvalidConnectorRunError](err); !ok {
		t.Fatalf("wrong Source reference error = %T %v", err, err)
	}
	if store.saves != 0 {
		t.Fatalf("checkpoint advanced after wrong Source reference: %d", store.saves)
	}
}

func TestConnectorLifecycleDoesNotAdvanceWhenConnectorSwallowsGenericSubmissionError(t *testing.T) {
	binding := lifecycleBinding(t)
	accepted := lifecycleAcceptedObservation(t, "remote.note", "item-1")
	submissionErr := errors.New("durable database unavailable")
	store := &lifecycleCheckpointStore{value: source.NoConnectorCheckpoint()}
	ingestion := &lifecycleIngestion{err: submissionErr}
	application, err := NewConnectorLifecycleApplication(New(), ingestion, store)
	if err != nil {
		t.Fatal(err)
	}
	connector := lifecycleConnector{
		name: "connector", version: "v1", definitions: []string{"remote.note"},
		run: func(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			_, _ = session.Submit(ctx, "provider-item", "remote.note", accepted)
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":1}`))
		},
	}
	_, err = application.Run(t.Context(), connector, binding)
	if !errors.Is(err, submissionErr) {
		t.Fatalf("Run() error = %T %v, want submission error", err, err)
	}
	if store.saves != 0 {
		t.Fatalf("checkpoint advanced after swallowed generic submission error: %d", store.saves)
	}
}

func TestConnectorLifecycleCancellationAfterIgnoredRunStopsSuccessfulResult(t *testing.T) {
	binding := lifecycleBinding(t)
	previous, err := source.NewConnectorCheckpoint(jsontext.Value(`{"offset":1}`))
	if err != nil {
		t.Fatal(err)
	}
	store := &lifecycleCheckpointStore{value: previous}
	application, err := NewConnectorLifecycleApplication(New(), &lifecycleIngestion{}, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	connector := lifecycleConnector{
		name: "connector", version: "v1", definitions: []string{"remote.note"},
		run: func(_ context.Context, _ *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			cancel()
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":1}`))
		},
	}

	result, runErr := application.Run(ctx, connector, binding)
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run() error = %T %v, want context cancellation", runErr, runErr)
	}
	if result.Status() == source.ConnectorRunComplete {
		t.Fatalf("Run() returned successful result after cancellation: %#v", result)
	}
	if store.saves != 0 {
		t.Fatalf("checkpoint save attempts after ignored cancellation = %d", store.saves)
	}
}

func TestConnectorLifecycleCancellationWhileDrainingDoesNotAdvanceCheckpoint(t *testing.T) {
	binding := lifecycleBinding(t)
	accepted := lifecycleAcceptedObservation(t, "remote.note", "item-1")
	store := &lifecycleCheckpointStore{value: source.NoConnectorCheckpoint()}
	entered := make(chan struct{})
	drainIngestion := &lifecycleDrainIngestion{
		receipt: SourceReceipt{Ref: accepted.Observation().Ref(), Sequence: 1},
		entered: entered,
	}
	application, err := NewConnectorLifecycleApplication(New(), drainIngestion, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	submitted := make(chan error, 1)
	connectorReturned := make(chan struct{})
	connector := lifecycleConnector{
		name: "connector", version: "v1", definitions: []string{"remote.note"},
		run: func(runContext context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			go func() {
				_, submitErr := session.Submit(runContext, "provider-item", "remote.note", accepted)
				submitted <- submitErr
			}()
			<-entered
			close(connectorReturned)
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value(`{"offset":1}`))
		},
	}
	finished := make(chan error, 1)
	go func() {
		_, runErr := application.Run(ctx, connector, binding)
		finished <- runErr
	}()
	<-connectorReturned
	cancel()
	if runErr := <-finished; !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run() error = %T %v, want context cancellation", runErr, runErr)
	}
	if submitErr := <-submitted; !errors.Is(submitErr, context.Canceled) {
		t.Fatalf("in-flight Submit() error = %T %v, want context cancellation", submitErr, submitErr)
	}
	if store.saves != 0 {
		t.Fatalf("checkpoint save attempts after cancellation while draining = %d", store.saves)
	}
}

func TestConnectorLifecyclePreservesConnectorErrorAfterCancellation(t *testing.T) {
	binding := lifecycleBinding(t)
	store := &lifecycleCheckpointStore{value: source.NoConnectorCheckpoint()}
	application, err := NewConnectorLifecycleApplication(New(), &lifecycleIngestion{}, store)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	providerErr := errors.New("provider failed")
	connector := lifecycleConnector{
		name: "connector", version: "v1", definitions: []string{"remote.note"},
		run: func(_ context.Context, _ *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			cancel()
			return source.ConnectorRunCompletion{}, providerErr
		},
	}

	_, runErr := application.Run(ctx, connector, binding)
	if !errors.Is(runErr, providerErr) {
		t.Fatalf("Run() error = %T %v, want provider error", runErr, runErr)
	}
	if errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run() replaced connector error with cancellation: %v", runErr)
	}
	if store.saves != 0 {
		t.Fatalf("checkpoint save attempts after connector error = %d", store.saves)
	}
}

type lifecycleCheckpointStore struct {
	value   source.ConnectorCheckpoint
	events  []string
	saves   int
	saveErr error
}

func (s *lifecycleCheckpointStore) Load(_ context.Context, _ source.ConnectorBinding) (source.ConnectorCheckpoint, error) {
	s.events = append(s.events, "load")
	return s.value, nil
}

func (s *lifecycleCheckpointStore) Save(
	_ context.Context,
	_ source.ConnectorBinding,
	next, _ source.ConnectorCheckpoint,
) error {
	s.events = append(s.events, "save")
	s.saves++
	if s.saveErr != nil {
		return s.saveErr
	}
	s.value = next
	return nil
}

type lifecycleIngestion struct {
	receipt SourceReceipt
	err     error
	calls   int
}

func (i *lifecycleIngestion) SubmitAccepted(
	_ context.Context,
	_ string,
	_ *source.AdmittedObservation,
) (SourceReceipt, error) {
	i.calls++
	return i.receipt, i.err
}

type lifecycleDrainIngestion struct {
	receipt SourceReceipt
	entered chan<- struct{}
	once    sync.Once
}

func (i *lifecycleDrainIngestion) SubmitAccepted(
	ctx context.Context,
	_ string,
	_ *source.AdmittedObservation,
) (SourceReceipt, error) {
	i.once.Do(func() { close(i.entered) })
	<-ctx.Done()
	return i.receipt, ctx.Err()
}

type lifecycleConnector struct {
	name        string
	version     string
	definitions []string
	run         func(context.Context, *source.ConnectorRunSession) (source.ConnectorRunCompletion, error)
}

func (c lifecycleConnector) Name() string                { return c.name }
func (c lifecycleConnector) Version() string             { return c.version }
func (c lifecycleConnector) SourceDefinitions() []string { return c.definitions }
func (c lifecycleConnector) Run(ctx context.Context, session *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
	return c.run(ctx, session)
}

func lifecycleBinding(t *testing.T) source.ConnectorBinding {
	t.Helper()
	binding, err := source.NewConnectorBinding("scope", "binding", "connector", "v1")
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func lifecycleAcceptedObservation(t *testing.T, definition, identifier string) *source.AdmittedObservation {
	t.Helper()
	identity, err := source.NewDefinitionIdentity(definition, "1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"type":"object","required":["name","definition_version","materialization"],"properties":{"name":{"type":"string"},"definition_version":{"type":"string"},"materialization":{"const":"captured"}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := source.NewRef(definition, identifier)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(ref, "1", manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"`+identifier+`","definition_version":"1","materialization":"captured"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := source.AdmitObservation(manifest, observation)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
