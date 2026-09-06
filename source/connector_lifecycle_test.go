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

package source_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/source"
)

func TestConnectorCheckpointKeepsAbsentDistinctFromJSONNullAndComparesLosslessly(t *testing.T) {
	absent := source.NoConnectorCheckpoint()
	nullCheckpoint, err := source.NewConnectorCheckpoint(jsontext.Value("null"))
	if err != nil {
		t.Fatal(err)
	}
	if equal, compareErr := source.EqualConnectorCheckpoints(absent, nullCheckpoint); compareErr != nil || equal {
		t.Fatalf("absent and null equal = %t, %v", equal, compareErr)
	}
	if _, found := absent.Value(); found {
		t.Fatal("absent checkpoint exposed a JSON value")
	}
	if value, found := nullCheckpoint.Value(); !found || string(value) != "null" {
		t.Fatalf("null checkpoint = %q, found=%t", value, found)
	}

	left, err := source.NewConnectorCheckpoint(jsontext.Value(`{"offset":9007199254740993,"flags":[true,1.0]}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := source.NewConnectorCheckpoint(jsontext.Value(`{"flags":[true,1e0],"offset":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	if equal, compareErr := source.EqualConnectorCheckpoints(left, right); compareErr != nil || !equal {
		t.Fatalf("value-equivalent checkpoints equal = %t, %v", equal, compareErr)
	}
	changed, err := source.NewConnectorCheckpoint(jsontext.Value(`{"offset":9007199254740994,"flags":[true,1]}`))
	if err != nil {
		t.Fatal(err)
	}
	if equal, compareErr := source.EqualConnectorCheckpoints(left, changed); compareErr != nil || equal {
		t.Fatalf("large integer change equal = %t, %v", equal, compareErr)
	}
}

func TestConnectorRunSessionRecordsOnlyDurableAcceptedAndTypedRejectedItems(t *testing.T) {
	binding := connectorLifecycleBinding(t)
	accepted := connectorLifecycleObservation(t, "remote.note", "item-1")
	rejection, err := source.NewConnectorSubmissionRejectedError("schema rejected")
	if err != nil {
		t.Fatal(err)
	}
	sink := &connectorLifecycleSink{results: []connectorLifecycleSinkResult{
		{ref: accepted.Observation().Ref()},
		{err: rejection},
	}}
	session, err := source.NewConnectorRunSession(
		binding, source.NoConnectorCheckpoint(), []string{"remote.note"}, sink,
	)
	if err != nil {
		t.Fatal(err)
	}

	first, err := session.Submit(t.Context(), "provider-1", "remote.note", accepted)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status() != source.ConnectorSubmissionAccepted {
		t.Fatalf("first status = %q", first.Status())
	}
	if ref, found := first.SourceRef(); !found || ref != accepted.Observation().Ref() {
		t.Fatalf("accepted ref = %#v, found=%t", ref, found)
	}

	second, err := session.Submit(t.Context(), "provider-2", "remote.note", accepted)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status() != source.ConnectorSubmissionRejected {
		t.Fatalf("rejected status = %q", second.Status())
	}
	if detail, found := second.Detail(); !found || detail != "schema rejected" {
		t.Fatalf("rejection detail = %q, found=%t", detail, found)
	}
	if _, found := second.SourceRef(); found {
		t.Fatal("rejected item carried a durable Source reference")
	}
	if len(session.Outcomes()) != 2 || sink.calls != 2 {
		t.Fatalf("outcomes=%d sink calls=%d", len(session.Outcomes()), sink.calls)
	}
}

func TestConnectorRunSessionRejectsDuplicateUndeclaredAndWrongSourceRef(t *testing.T) {
	binding := connectorLifecycleBinding(t)
	accepted := connectorLifecycleObservation(t, "remote.note", "item-1")
	wrong := connectorLifecycleObservation(t, "remote.other", "item-1")
	session, err := source.NewConnectorRunSession(
		binding, source.NoConnectorCheckpoint(), []string{"remote.note"}, &connectorLifecycleSink{results: []connectorLifecycleSinkResult{{ref: accepted.Observation().Ref()}}},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := session.Submit(t.Context(), "item", "remote.other", accepted); err == nil {
		t.Fatal("undeclared definition was accepted")
	} else if _, ok := errors.AsType[*source.InvalidConnectorRunError](err); !ok {
		t.Fatalf("undeclared definition error = %T %v", err, err)
	}
	if _, err := session.Submit(t.Context(), "item", "remote.note", accepted); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Submit(t.Context(), "item", "remote.note", accepted); err == nil {
		t.Fatal("duplicate item was accepted")
	} else if _, ok := errors.AsType[*source.InvalidConnectorRunError](err); !ok {
		t.Fatalf("duplicate item error = %T %v", err, err)
	}
	if _, err := session.Submit(t.Context(), "wrong", "remote.note", wrong); err == nil {
		t.Fatal("wrong Source reference was accepted")
	} else if _, ok := errors.AsType[*source.InvalidConnectorRunError](err); !ok {
		t.Fatalf("wrong source reference error = %T %v", err, err)
	}

	connector := connectorLifecycleFake{name: "other", version: binding.ConnectorVersion(), definitions: []string{"remote.note"}}
	if _, err := source.ValidateConnector(connector, binding); err == nil {
		t.Fatal("binding mismatch was accepted")
	} else if _, ok := errors.AsType[*source.InvalidConnectorError](err); !ok {
		t.Fatalf("binding mismatch error = %T %v", err, err)
	}
}

func TestConnectorRunSessionConcurrentClaimsPreserveReservationOrder(t *testing.T) {
	binding := connectorLifecycleBinding(t)
	accepted := connectorLifecycleObservation(t, "remote.note", "item-1")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseFirst) })
	t.Cleanup(release)
	sink := &concurrentConnectorLifecycleSink{
		firstStarted: firstStarted,
		releaseFirst: releaseFirst,
		ref:          accepted.Observation().Ref(),
		calls:        make(map[string]int),
	}
	session, err := source.NewConnectorRunSession(
		binding, source.NoConnectorCheckpoint(), []string{"remote.note"}, sink,
	)
	if err != nil {
		t.Fatal(err)
	}

	doneReading := make(chan struct{})
	stopReaders := sync.OnceFunc(func() { close(doneReading) })
	defer stopReaders()
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for {
				select {
				case <-doneReading:
					return
				default:
					_ = session.Outcomes()
					_ = session.Failure()
				}
			}
		})
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	type submitResult struct {
		outcome source.ConnectorItemOutcome
		err     error
	}
	firstResult := make(chan submitResult, 1)
	secondResult := make(chan submitResult, 1)
	var submissions sync.WaitGroup
	submissions.Go(func() {
		outcome, submitErr := session.Submit(ctx, "first", "remote.note", accepted)
		firstResult <- submitResult{outcome: outcome, err: submitErr}
	})
	<-firstStarted

	submissions.Go(func() {
		outcome, submitErr := session.Submit(ctx, "second", "remote.note", accepted)
		secondResult <- submitResult{outcome: outcome, err: submitErr}
	})
	var second submitResult
	select {
	case second = <-secondResult:
	case <-ctx.Done():
		release()
		first := <-firstResult
		second = <-secondResult
		submissions.Wait()
		t.Fatalf("second Submit() did not finish while the first sink call waited: first=%v second=%v", first.err, second.err)
	}
	if second.err != nil || second.outcome.Status() != source.ConnectorSubmissionAccepted {
		t.Fatalf("second Submit() = %#v, %v", second.outcome, second.err)
	}
	var writes sync.WaitGroup
	writeErrors := make(chan error, 2)
	writes.Go(func() {
		_, rejectErr := session.Reject("rejected", "remote.note", "provider rejected item")
		writeErrors <- rejectErr
	})
	writes.Go(func() {
		_, failErr := session.Fail("failed", "remote.note", "provider unavailable")
		writeErrors <- failErr
	})
	writes.Wait()
	for range 2 {
		if writeErr := <-writeErrors; writeErr != nil {
			t.Fatalf("concurrent unsafe outcome error = %v", writeErr)
		}
	}
	if _, duplicateErr := session.Submit(t.Context(), "first", "remote.note", accepted); duplicateErr == nil {
		t.Fatal("concurrent duplicate item was accepted")
	} else if _, ok := errors.AsType[*source.InvalidConnectorRunError](duplicateErr); !ok {
		t.Fatalf("concurrent duplicate error = %T %v", duplicateErr, duplicateErr)
	}
	if outcomes := session.Outcomes(); len(outcomes) != 3 {
		t.Fatalf("outcomes before first sink release = %#v", outcomes)
	}

	release()
	first := <-firstResult
	submissions.Wait()
	if first.err != nil {
		t.Fatalf("first Submit() = %v", first.err)
	}
	stopReaders()
	readers.Wait()

	outcomes := session.Outcomes()
	if len(outcomes) != 4 || outcomes[0].ItemID() != "first" || outcomes[1].ItemID() != "second" {
		t.Fatalf("outcomes ordered by sink completion instead of reservation = %#v", outcomes)
	}
	if sink.CallCount("first") != 1 || sink.CallCount("second") != 1 {
		t.Fatalf("sink calls first=%d second=%d", sink.CallCount("first"), sink.CallCount("second"))
	}
	if _, ok := errors.AsType[*source.InvalidConnectorRunError](session.Failure()); !ok {
		t.Fatalf("Failure() = %T %v", session.Failure(), session.Failure())
	}
}

func TestConnectorRunSessionFinalizeClosesClaimsBeforeCanceledDrainCompletes(t *testing.T) {
	binding := connectorLifecycleBinding(t)
	accepted := connectorLifecycleObservation(t, "remote.note", "item-1")
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	sink := &concurrentConnectorLifecycleSink{
		firstStarted: firstStarted,
		releaseFirst: releaseFirst,
		ref:          accepted.Observation().Ref(),
		calls:        make(map[string]int),
	}
	session, err := source.NewConnectorRunSession(
		binding, source.NoConnectorCheckpoint(), []string{"remote.note"}, sink,
	)
	if err != nil {
		t.Fatal(err)
	}

	submitted := make(chan error, 1)
	go func() {
		_, submitErr := session.Submit(t.Context(), "first", "remote.note", accepted)
		submitted <- submitErr
	}()
	<-firstStarted

	drainContext, cancelDrain := context.WithCancel(t.Context())
	cancelDrain()
	if finalizeErr := session.Finalize(drainContext); !errors.Is(finalizeErr, context.Canceled) {
		t.Fatalf("Finalize() error = %T %v, want context cancellation", finalizeErr, finalizeErr)
	}
	if _, submitErr := session.Submit(t.Context(), "late", "remote.note", accepted); submitErr == nil {
		t.Fatal("Submit() accepted a claim after Finalize closed the session")
	} else if _, ok := errors.AsType[*source.InvalidConnectorRunError](submitErr); !ok {
		t.Fatalf("late Submit() error = %T %v", submitErr, submitErr)
	}
	if calls := sink.CallCount("late"); calls != 0 {
		t.Fatalf("late sink calls = %d, want 0", calls)
	}

	close(releaseFirst)
	if submitErr := <-submitted; submitErr != nil {
		t.Fatalf("in-flight Submit() error = %v", submitErr)
	}
	if finalizeErr := session.Finalize(t.Context()); finalizeErr != nil {
		t.Fatalf("Finalize() after settlement = %v", finalizeErr)
	}
}

type connectorLifecycleSinkResult struct {
	ref source.Ref
	err error
}

type connectorLifecycleSink struct {
	results []connectorLifecycleSinkResult
	calls   int
}

type concurrentConnectorLifecycleSink struct {
	mu           sync.Mutex
	firstStarted chan<- struct{}
	releaseFirst <-chan struct{}
	ref          source.Ref
	calls        map[string]int
}

func (s *concurrentConnectorLifecycleSink) Submit(
	ctx context.Context,
	_ source.ConnectorBinding,
	itemID, _ string,
	_ *source.AdmittedObservation,
) (source.Ref, error) {
	s.mu.Lock()
	s.calls[itemID]++
	s.mu.Unlock()
	if itemID == "first" {
		s.firstStarted <- struct{}{}
		select {
		case <-s.releaseFirst:
		case <-ctx.Done():
			return source.Ref{}, ctx.Err()
		}
	}
	return s.ref, nil
}

func (s *concurrentConnectorLifecycleSink) CallCount(itemID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[itemID]
}

func (s *connectorLifecycleSink) Submit(
	_ context.Context,
	_ source.ConnectorBinding,
	_, _ string,
	_ *source.AdmittedObservation,
) (source.Ref, error) {
	result := s.results[s.calls]
	s.calls++
	return result.ref, result.err
}

type connectorLifecycleFake struct {
	name        string
	version     string
	definitions []string
}

func (f connectorLifecycleFake) Name() string                { return f.name }
func (f connectorLifecycleFake) Version() string             { return f.version }
func (f connectorLifecycleFake) SourceDefinitions() []string { return f.definitions }
func (connectorLifecycleFake) Run(context.Context, *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
	return source.ConnectorRunCompletion{}, nil
}

func connectorLifecycleBinding(t *testing.T) source.ConnectorBinding {
	t.Helper()
	binding, err := source.NewConnectorBinding("scope", "binding", "connector", "v1")
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func connectorLifecycleObservation(t *testing.T, definition, identifier string) *source.AdmittedObservation {
	t.Helper()
	identity, err := source.NewDefinitionIdentity(definition, "1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"type":"object","required":["identifier","name","definition_version","materialization"],"properties":{"identifier":{"type":"string"},"name":{"type":"string"},"definition_version":{"type":"string"},"materialization":{"const":"captured"}}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := source.NewRef(definition, identifier)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"identifier":"`+identifier+`","name":"`+identifier+`","definition_version":"1","materialization":"captured"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := source.AdmitObservation(manifest, observation)
	if err != nil {
		t.Fatal(err)
	}
	return accepted
}
