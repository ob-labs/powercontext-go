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

package source

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"slices"
	"strings"
	"sync"
)

// ConnectorRunStatus reports whether a Connector exhausted the provider work
// represented by a run. Only complete runs without unsafe item outcomes may
// advance a durable checkpoint.
type ConnectorRunStatus string

const (
	ConnectorRunComplete   ConnectorRunStatus = "complete"
	ConnectorRunIncomplete ConnectorRunStatus = "incomplete"
)

// ConnectorSubmissionStatus is the visible durable outcome for one provider
// item. Rejected and failed outcomes make a complete run unsafe to checkpoint.
type ConnectorSubmissionStatus string

const (
	ConnectorSubmissionAccepted ConnectorSubmissionStatus = "accepted"
	ConnectorSubmissionRejected ConnectorSubmissionStatus = "rejected"
	ConnectorSubmissionFailed   ConnectorSubmissionStatus = "failed"
)

// InvalidConnectorError reports an invalid Connector declaration without
// exposing provider-owned values.
type InvalidConnectorError struct {
	Field  string
	Detail string
}

func (e *InvalidConnectorError) Error() string {
	return fmt.Sprintf("invalid Connector %s: %s", e.Field, e.Detail)
}

// InvalidConnectorRunError reports an invalid Connector lifecycle action
// without exposing provider item content or stable Source identities.
type InvalidConnectorRunError struct {
	Field  string
	Detail string
}

func (e *InvalidConnectorRunError) Error() string {
	return fmt.Sprintf("invalid Connector run %s: %s", e.Field, e.Detail)
}

// ConnectorSubmissionRejectedError is returned by a durable ingestion sink
// when one item is rejected as a normal, visible lifecycle outcome. Other
// errors abort the Connector run and never advance its checkpoint.
type ConnectorSubmissionRejectedError struct{ detail string }

func (e *ConnectorSubmissionRejectedError) Error() string {
	if e == nil {
		return "Connector submission was rejected"
	}
	return "Connector submission was rejected: " + e.detail
}

// NewConnectorSubmissionRejectedError constructs a bounded, display-safe
// rejection detail. Callers must not put captured Source payloads in detail.
func NewConnectorSubmissionRejectedError(detail string) (*ConnectorSubmissionRejectedError, error) {
	if err := validateConnectorRunText("detail", detail, MaxIDLength); err != nil {
		return nil, err
	}
	return &ConnectorSubmissionRejectedError{detail: detail}, nil
}

// Detail returns the bounded reason suitable for a visible rejected outcome.
func (e *ConnectorSubmissionRejectedError) Detail() string {
	if e == nil {
		return ""
	}
	return e.detail
}

// ConnectorCheckpoint distinguishes an absent checkpoint row from a stored
// JSON null. Value returns false only for an absent row.
type ConnectorCheckpoint struct {
	found bool
	value jsontext.Value
}

// NoConnectorCheckpoint represents a binding with no durable checkpoint row.
func NoConnectorCheckpoint() ConnectorCheckpoint { return ConnectorCheckpoint{} }

// NewConnectorCheckpoint freezes one stored or proposed checkpoint. The value
// must be complete valid JSON; JSON null is a valid stored checkpoint.
func NewConnectorCheckpoint(value jsontext.Value) (ConnectorCheckpoint, error) {
	cloned, err := immutableConnectorCheckpoint(value)
	if err != nil {
		return ConnectorCheckpoint{}, err
	}
	return ConnectorCheckpoint{found: true, value: cloned}, nil
}

// Found reports whether a durable checkpoint row exists.
func (c ConnectorCheckpoint) Found() bool { return c.found }

// Value returns an immutable JSON checkpoint and whether a durable row exists.
func (c ConnectorCheckpoint) Value() (jsontext.Value, bool) {
	if !c.found {
		return nil, false
	}
	return jsontext.Value(bytes.Clone(c.value)), true
}

// Validate rejects malformed zero-value mutations at persistence boundaries.
func (c ConnectorCheckpoint) Validate() error {
	if !c.found {
		if len(c.value) != 0 {
			return connectorRunError("checkpoint", "must not carry a value when absent")
		}
		return nil
	}
	_, err := immutableConnectorCheckpoint(c.value)
	return err
}

// EqualConnectorCheckpoints compares checkpoint JSON by value, including
// arbitrary-size integers. It does not parse JSON numbers through float64.
func EqualConnectorCheckpoints(left, right ConnectorCheckpoint) (bool, error) {
	if err := left.Validate(); err != nil {
		return false, err
	}
	if err := right.Validate(); err != nil {
		return false, err
	}
	if left.found != right.found {
		return false, nil
	}
	if !left.found {
		return true, nil
	}
	return equalConnectorJSON(left.value, right.value)
}

// ConnectorItemOutcome records exactly one claimed provider item.
type ConnectorItemOutcome struct {
	itemID         string
	definitionName string
	status         ConnectorSubmissionStatus
	sourceRef      *Ref
	detail         *string
}

func (o ConnectorItemOutcome) ItemID() string                    { return o.itemID }
func (o ConnectorItemOutcome) DefinitionName() string            { return o.definitionName }
func (o ConnectorItemOutcome) Status() ConnectorSubmissionStatus { return o.status }

// SourceRef returns the durable Source accepted for this item.
func (o ConnectorItemOutcome) SourceRef() (Ref, bool) {
	if o.sourceRef == nil {
		return Ref{}, false
	}
	return *o.sourceRef, true
}

// Detail returns provider or admission detail for an unsafe item outcome.
func (o ConnectorItemOutcome) Detail() (string, bool) {
	if o.detail == nil {
		return "", false
	}
	return *o.detail, true
}

// ConnectorRunCompletion is the Connector-owned terminal provider signal and
// proposed next checkpoint. A complete status alone is insufficient to commit.
type ConnectorRunCompletion struct {
	status     ConnectorRunStatus
	checkpoint ConnectorCheckpoint
}

func NewConnectorRunCompletion(status ConnectorRunStatus, checkpoint jsontext.Value) (ConnectorRunCompletion, error) {
	if !validConnectorRunStatus(status) {
		return ConnectorRunCompletion{}, connectorRunError("status", "must be complete or incomplete")
	}
	value, err := NewConnectorCheckpoint(checkpoint)
	if err != nil {
		return ConnectorRunCompletion{}, err
	}
	return ConnectorRunCompletion{status: status, checkpoint: value}, nil
}

func (c ConnectorRunCompletion) Status() ConnectorRunStatus { return c.status }

// Checkpoint is valid JSON even when it is JSON null.
func (c ConnectorRunCompletion) Checkpoint() jsontext.Value {
	value, _ := c.checkpoint.Value()
	return value
}

func (c ConnectorRunCompletion) Validate() error {
	if !validConnectorRunStatus(c.status) {
		return connectorRunError("status", "must be complete or incomplete")
	}
	if !c.checkpoint.Found() {
		return connectorRunError("checkpoint", "must contain a JSON value")
	}
	return c.checkpoint.Validate()
}

// ConnectorRunResult exposes the outcome after any permitted checkpoint CAS.
type ConnectorRunResult struct {
	binding   ConnectorBinding
	status    ConnectorRunStatus
	previous  ConnectorCheckpoint
	proposed  ConnectorCheckpoint
	committed ConnectorCheckpoint
	items     []ConnectorItemOutcome
}

func NewConnectorRunResult(
	binding ConnectorBinding,
	status ConnectorRunStatus,
	previous, proposed, committed ConnectorCheckpoint,
	items []ConnectorItemOutcome,
) (ConnectorRunResult, error) {
	if err := binding.Validate(); err != nil {
		return ConnectorRunResult{}, err
	}
	if !validConnectorRunStatus(status) {
		return ConnectorRunResult{}, connectorRunError("status", "must be complete or incomplete")
	}
	for _, checkpoint := range []ConnectorCheckpoint{previous, proposed, committed} {
		if err := checkpoint.Validate(); err != nil {
			return ConnectorRunResult{}, err
		}
	}
	if !proposed.Found() {
		return ConnectorRunResult{}, connectorRunError("proposed_checkpoint", "must contain a JSON value")
	}
	cloned := make([]ConnectorItemOutcome, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		if err := item.validate(); err != nil {
			return ConnectorRunResult{}, err
		}
		if _, exists := seen[item.itemID]; exists {
			return ConnectorRunResult{}, connectorRunError("items", "must not contain duplicate item identifiers")
		}
		seen[item.itemID] = struct{}{}
		cloned[index] = cloneConnectorOutcome(item)
	}
	return ConnectorRunResult{
		binding: binding, status: status, previous: cloneConnectorCheckpoint(previous),
		proposed: cloneConnectorCheckpoint(proposed), committed: cloneConnectorCheckpoint(committed), items: cloned,
	}, nil
}

func (r ConnectorRunResult) Binding() ConnectorBinding  { return r.binding }
func (r ConnectorRunResult) Status() ConnectorRunStatus { return r.status }

func (r ConnectorRunResult) PreviousCheckpoint() ConnectorCheckpoint {
	return cloneConnectorCheckpoint(r.previous)
}

func (r ConnectorRunResult) ProposedCheckpoint() ConnectorCheckpoint {
	return cloneConnectorCheckpoint(r.proposed)
}

func (r ConnectorRunResult) CommittedCheckpoint() ConnectorCheckpoint {
	return cloneConnectorCheckpoint(r.committed)
}

// Items returns immutable, ordered item outcomes.
func (r ConnectorRunResult) Items() []ConnectorItemOutcome {
	result := make([]ConnectorItemOutcome, len(r.items))
	for index, item := range r.items {
		result[index] = cloneConnectorOutcome(item)
	}
	return result
}

// Connector is the provider-neutral external boundary. Its Run implementation
// is deliberately invoked outside SQL transactions and Runtime scope locks.
type Connector interface {
	Name() string
	Version() string
	SourceDefinitions() []string
	Run(context.Context, *ConnectorRunSession) (ConnectorRunCompletion, error)
}

// ConnectorSourceSink durably accepts already-admitted worker observations.
// ConnectorRunSession translates only ConnectorSubmissionRejectedError into a
// visible item result; every other error aborts the run.
type ConnectorSourceSink interface {
	Submit(context.Context, ConnectorBinding, string, string, *AdmittedObservation) (Ref, error)
}

// ConnectorRunSession constrains one external Connector run to its declared
// Definitions and records each item exactly once.
type ConnectorRunSession struct {
	binding     ConnectorBinding
	checkpoint  ConnectorCheckpoint
	definitions map[string]struct{}
	sink        ConnectorSourceSink

	mu           sync.Mutex
	outcomes     map[uint64]ConnectorItemOutcome
	itemIDs      map[string]uint64
	nextSequence uint64
	failure      error
	closed       bool
	inFlight     int
	drained      chan struct{}
	drainClosed  bool
}

func NewConnectorRunSession(
	binding ConnectorBinding,
	checkpoint ConnectorCheckpoint,
	definitions []string,
	sink ConnectorSourceSink,
) (*ConnectorRunSession, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if err := checkpoint.Validate(); err != nil {
		return nil, err
	}
	if sink == nil || isNil(sink) {
		return nil, connectorRunError("sink", "must not be nil")
	}
	declared, err := declaredConnectorDefinitions(definitions)
	if err != nil {
		return nil, err
	}
	return &ConnectorRunSession{
		binding: binding, checkpoint: cloneConnectorCheckpoint(checkpoint), definitions: declared,
		sink: sink, outcomes: make(map[uint64]ConnectorItemOutcome), itemIDs: make(map[string]uint64),
		drained: make(chan struct{}),
	}, nil
}

func (s *ConnectorRunSession) Binding() ConnectorBinding {
	if s == nil {
		return ConnectorBinding{}
	}
	return s.binding
}

func (s *ConnectorRunSession) Checkpoint() ConnectorCheckpoint {
	if s == nil {
		return NoConnectorCheckpoint()
	}
	return cloneConnectorCheckpoint(s.checkpoint)
}

// Outcomes returns immutable results in item-claim order.
func (s *ConnectorRunSession) Outcomes() []ConnectorItemOutcome {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sequences := slices.Sorted(maps.Keys(s.outcomes))
	result := make([]ConnectorItemOutcome, len(sequences))
	for index, sequence := range sequences {
		result[index] = cloneConnectorOutcome(s.outcomes[sequence])
	}
	return result
}

// Failure returns the first generic submission failure. Connectors may choose
// to observe and recover from an error, but the owning lifecycle must still
// prevent checkpoint advancement for that run.
func (s *ConnectorRunSession) Failure() error {
	if s == nil {
		return connectorRunError("session", "must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failure
}

// Finalize prevents further item claims and waits for every previously
// claimed Submit call to settle its durable sink I/O. It never holds the
// session lock while waiting. A canceled Context closes the session before
// returning its cancellation error; callers may call Finalize again to wait
// for settlement with a live Context.
func (s *ConnectorRunSession) Finalize(ctx context.Context) error {
	if s == nil {
		return connectorRunError("session", "must not be nil")
	}
	s.mu.Lock()
	s.closed = true
	s.finishDrainLocked()
	drained := s.drained
	s.mu.Unlock()

	if contextErr := context.Cause(ctx); contextErr != nil {
		return contextErr
	}
	select {
	case <-drained:
		if contextErr := context.Cause(ctx); contextErr != nil {
			return contextErr
		}
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// Submit durably accepts one pre-admitted observation before recording an
// accepted outcome. A typed rejection remains visible; a generic failure is
// returned to the Connector and prevents completion/checkpoint handling.
func (s *ConnectorRunSession) Submit(
	ctx context.Context,
	itemID, definitionName string,
	observation *AdmittedObservation,
) (ConnectorItemOutcome, error) {
	if s == nil {
		return ConnectorItemOutcome{}, connectorRunError("session", "must not be nil")
	}
	sequence, claimErr := s.claim(itemID, definitionName, true)
	if claimErr != nil {
		return ConnectorItemOutcome{}, s.recordFailure(claimErr)
	}
	defer s.settleSubmission()
	if err := observation.Validate(); err != nil {
		return ConnectorItemOutcome{}, s.recordFailure(connectorRunError("observation", "must be admitted and valid"))
	}
	accepted := observation.Observation()
	if accepted.Ref().Type() != definitionName {
		return ConnectorItemOutcome{}, s.recordFailure(connectorRunError("source_ref", "must match the declared Source Definition"))
	}
	ref, err := s.sink.Submit(ctx, s.binding, itemID, definitionName, observation)
	if rejected, ok := errors.AsType[*ConnectorSubmissionRejectedError](err); ok {
		outcome := rejectedConnectorOutcome(itemID, definitionName, rejected.Detail())
		s.recordOutcome(sequence, outcome)
		return cloneConnectorOutcome(outcome), nil
	}
	if err != nil {
		return ConnectorItemOutcome{}, s.recordFailure(err)
	}
	if validateErr := validateConnectorReturnedRef(ref, accepted.Ref(), definitionName); validateErr != nil {
		return ConnectorItemOutcome{}, s.recordFailure(validateErr)
	}
	outcome := acceptedConnectorOutcome(itemID, definitionName, ref)
	s.recordOutcome(sequence, outcome)
	return cloneConnectorOutcome(outcome), nil
}

// Reject records one provider item that cannot satisfy its Source Definition.
func (s *ConnectorRunSession) Reject(itemID, definitionName, detail string) (ConnectorItemOutcome, error) {
	return s.recordUnsafe(itemID, definitionName, ConnectorSubmissionRejected, detail)
}

// Fail records one provider item that could not be acquired safely.
func (s *ConnectorRunSession) Fail(itemID, definitionName, detail string) (ConnectorItemOutcome, error) {
	return s.recordUnsafe(itemID, definitionName, ConnectorSubmissionFailed, detail)
}

func (s *ConnectorRunSession) recordUnsafe(
	itemID, definitionName string,
	status ConnectorSubmissionStatus,
	detail string,
) (ConnectorItemOutcome, error) {
	if s == nil {
		return ConnectorItemOutcome{}, connectorRunError("session", "must not be nil")
	}
	if status != ConnectorSubmissionRejected && status != ConnectorSubmissionFailed {
		return ConnectorItemOutcome{}, connectorRunError("status", "must be rejected or failed")
	}
	sequence, claimErr := s.claim(itemID, definitionName, false)
	if claimErr != nil {
		return ConnectorItemOutcome{}, s.recordFailure(claimErr)
	}
	if err := validateConnectorRunText("detail", detail, MaxIDLength); err != nil {
		return ConnectorItemOutcome{}, s.recordFailure(err)
	}
	outcome := unsafeConnectorOutcome(itemID, definitionName, status, detail)
	s.recordOutcome(sequence, outcome)
	return cloneConnectorOutcome(outcome), nil
}

func (s *ConnectorRunSession) recordFailure(err error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failure == nil {
		s.failure = err
	}
	return err
}

func (s *ConnectorRunSession) recordOutcome(sequence uint64, outcome ConnectorItemOutcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes[sequence] = cloneConnectorOutcome(outcome)
}

func (s *ConnectorRunSession) claim(itemID, definitionName string, tracksSettlement bool) (uint64, error) {
	if err := validateConnectorRunText("item_id", itemID, MaxIDLength); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, connectorRunError("session", "is closed")
	}
	if _, exists := s.itemIDs[itemID]; exists {
		return 0, connectorRunError("duplicate_item", "must not be submitted more than once")
	}
	if _, exists := s.definitions[definitionName]; !exists {
		return 0, connectorRunError("definition", "was not declared by the Connector")
	}
	sequence := s.nextSequence
	s.nextSequence++
	s.itemIDs[itemID] = sequence
	if tracksSettlement {
		s.inFlight++
	}
	return sequence, nil
}

func (s *ConnectorRunSession) settleSubmission() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight--
	s.finishDrainLocked()
}

func (s *ConnectorRunSession) finishDrainLocked() {
	if s.closed && s.inFlight == 0 && !s.drainClosed {
		close(s.drained)
		s.drainClosed = true
	}
}

// ValidateConnector binds one Connector declaration to the exact durable
// binding it will execute and returns an immutable declaration snapshot.
func ValidateConnector(connector Connector, binding ConnectorBinding) ([]string, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if connector == nil || isNil(connector) {
		return nil, &InvalidConnectorError{Field: "connector", Detail: "must not be nil"}
	}
	if connector.Name() != binding.ConnectorName() {
		return nil, &InvalidConnectorError{Field: "name", Detail: "must match the binding"}
	}
	if connector.Version() != binding.ConnectorVersion() {
		return nil, &InvalidConnectorError{Field: "version", Detail: "must match the binding"}
	}
	definitions := connector.SourceDefinitions()
	if _, err := declaredConnectorDefinitions(definitions); err != nil {
		return nil, &InvalidConnectorError{Field: "source_definitions", Detail: "must contain unique, non-empty trimmed names"}
	}
	return slices.Clone(definitions), nil
}

func declaredConnectorDefinitions(values []string) (map[string]struct{}, error) {
	if len(values) == 0 {
		return nil, connectorRunError("source_definitions", "must not be empty")
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateConnectorRunText("definition", value, MaxTypeLength); err != nil {
			return nil, err
		}
		if _, exists := result[value]; exists {
			return nil, connectorRunError("source_definitions", "must not contain duplicates")
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func validConnectorRunStatus(status ConnectorRunStatus) bool {
	return status == ConnectorRunComplete || status == ConnectorRunIncomplete
}

func validateConnectorRunText(field, value string, maximum int) error {
	if err := connectorIdentity(field, value, maximum); err != nil {
		return connectorRunError(field, "must be a non-empty trimmed valid UTF-8 string")
	}
	return nil
}

func immutableConnectorCheckpoint(value jsontext.Value) (jsontext.Value, error) {
	cloned := jsontext.Value(bytes.Clone(value))
	if len(cloned) == 0 || !cloned.IsValid() {
		return nil, connectorRunError("checkpoint", "must be valid JSON")
	}
	return cloned, nil
}

func cloneConnectorCheckpoint(value ConnectorCheckpoint) ConnectorCheckpoint {
	if !value.found {
		return ConnectorCheckpoint{}
	}
	return ConnectorCheckpoint{found: true, value: jsontext.Value(bytes.Clone(value.value))}
}

func acceptedConnectorOutcome(itemID, definitionName string, ref Ref) ConnectorItemOutcome {
	cloned := ref
	return ConnectorItemOutcome{itemID: itemID, definitionName: definitionName, status: ConnectorSubmissionAccepted, sourceRef: &cloned}
}

func rejectedConnectorOutcome(itemID, definitionName, detail string) ConnectorItemOutcome {
	return unsafeConnectorOutcome(itemID, definitionName, ConnectorSubmissionRejected, detail)
}

func unsafeConnectorOutcome(itemID, definitionName string, status ConnectorSubmissionStatus, detail string) ConnectorItemOutcome {
	cloned := strings.Clone(detail)
	return ConnectorItemOutcome{itemID: itemID, definitionName: definitionName, status: status, detail: &cloned}
}

func cloneConnectorOutcome(value ConnectorItemOutcome) ConnectorItemOutcome {
	result := value
	if value.sourceRef != nil {
		ref := *value.sourceRef
		result.sourceRef = &ref
	}
	if value.detail != nil {
		detail := strings.Clone(*value.detail)
		result.detail = &detail
	}
	return result
}

func (o ConnectorItemOutcome) validate() error {
	if err := validateConnectorRunText("item_id", o.itemID, MaxIDLength); err != nil {
		return err
	}
	if err := validateConnectorRunText("definition", o.definitionName, MaxTypeLength); err != nil {
		return err
	}
	switch o.status {
	case ConnectorSubmissionAccepted:
		if o.sourceRef == nil || o.detail != nil {
			return connectorRunError("item", "accepted outcomes must contain exactly one Source reference")
		}
		if _, err := NewRef(o.sourceRef.Type(), o.sourceRef.ID()); err != nil {
			return connectorRunError("source_ref", "must be valid")
		}
		if o.sourceRef.Type() != o.definitionName {
			return connectorRunError("source_ref", "must match the declared Source Definition")
		}
	case ConnectorSubmissionRejected, ConnectorSubmissionFailed:
		if o.sourceRef != nil || o.detail == nil {
			return connectorRunError("item", "unsafe outcomes must contain one detail and no Source reference")
		}
		if err := validateConnectorRunText("detail", *o.detail, MaxIDLength); err != nil {
			return err
		}
	default:
		return connectorRunError("status", "must be accepted, rejected, or failed")
	}
	return nil
}

func validateConnectorReturnedRef(actual, expected Ref, definitionName string) error {
	if _, err := NewRef(actual.Type(), actual.ID()); err != nil || actual.Type() != definitionName || actual != expected {
		return connectorRunError("source_ref", "must match the accepted observation")
	}
	return nil
}

func connectorRunError(field, detail string) *InvalidConnectorRunError {
	return &InvalidConnectorRunError{Field: field, Detail: detail}
}

func equalConnectorJSON(left, right jsontext.Value) (bool, error) {
	if !left.IsValid() || !right.IsValid() {
		return false, connectorRunError("checkpoint", "must be valid JSON")
	}
	if left.Kind() != right.Kind() {
		return false, nil
	}
	switch left.Kind() {
	case '{':
		return equalConnectorObjects(left, right)
	case '[':
		return equalConnectorArrays(left, right)
	case '"':
		var leftString, rightString string
		if err := json.Unmarshal(left, &leftString); err != nil {
			return false, connectorRunError("checkpoint", "must be valid JSON")
		}
		if err := json.Unmarshal(right, &rightString); err != nil {
			return false, connectorRunError("checkpoint", "must be valid JSON")
		}
		return leftString == rightString, nil
	case '0':
		return equalConnectorNumbers(left, right)
	default:
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right)), nil
	}
}

func equalConnectorObjects(left, right jsontext.Value) (bool, error) {
	leftMembers, err := connectorObjectMembers(left)
	if err != nil {
		return false, err
	}
	rightMembers, err := connectorObjectMembers(right)
	if err != nil {
		return false, err
	}
	if len(leftMembers) != len(rightMembers) {
		return false, nil
	}
	for name, leftValue := range leftMembers {
		rightValue, found := rightMembers[name]
		if !found {
			return false, nil
		}
		equal, compareErr := equalConnectorJSON(leftValue, rightValue)
		if compareErr != nil || !equal {
			return equal, compareErr
		}
	}
	return true, nil
}

func equalConnectorArrays(left, right jsontext.Value) (bool, error) {
	leftValues, err := connectorArrayValues(left)
	if err != nil {
		return false, err
	}
	rightValues, err := connectorArrayValues(right)
	if err != nil {
		return false, err
	}
	if len(leftValues) != len(rightValues) {
		return false, nil
	}
	for index := range leftValues {
		equal, compareErr := equalConnectorJSON(leftValues[index], rightValues[index])
		if compareErr != nil || !equal {
			return equal, compareErr
		}
	}
	return true, nil
}

func connectorObjectMembers(value jsontext.Value) (map[string]jsontext.Value, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(value))
	start, err := decoder.ReadToken()
	if err != nil || start.Kind() != '{' {
		return nil, connectorRunError("checkpoint", "must be valid JSON")
	}
	members := make(map[string]jsontext.Value)
	for decoder.PeekKind() != '}' {
		name, tokenErr := decoder.ReadToken()
		if tokenErr != nil || name.Kind() != '"' {
			return nil, connectorRunError("checkpoint", "must be valid JSON")
		}
		memberName := name.Clone().String()
		member, valueErr := decoder.ReadValue()
		if valueErr != nil {
			return nil, connectorRunError("checkpoint", "must be valid JSON")
		}
		if _, exists := members[memberName]; exists {
			return nil, connectorRunError("checkpoint", "must not contain duplicate object members")
		}
		members[memberName] = member.Clone()
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, connectorRunError("checkpoint", "must be valid JSON")
	}
	return members, nil
}

func connectorArrayValues(value jsontext.Value) ([]jsontext.Value, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(value))
	start, err := decoder.ReadToken()
	if err != nil || start.Kind() != '[' {
		return nil, connectorRunError("checkpoint", "must be valid JSON")
	}
	values := make([]jsontext.Value, 0)
	for decoder.PeekKind() != ']' {
		entry, valueErr := decoder.ReadValue()
		if valueErr != nil {
			return nil, connectorRunError("checkpoint", "must be valid JSON")
		}
		values = append(values, entry.Clone())
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, connectorRunError("checkpoint", "must be valid JSON")
	}
	return values, nil
}

func equalConnectorNumbers(left, right jsontext.Value) (bool, error) {
	leftNumber, err := parseConnectorNumber(left)
	if err != nil {
		return false, err
	}
	rightNumber, err := parseConnectorNumber(right)
	if err != nil {
		return false, err
	}
	if leftNumber.zero || rightNumber.zero {
		return leftNumber.zero == rightNumber.zero, nil
	}
	return leftNumber.negative == rightNumber.negative &&
		leftNumber.significand == rightNumber.significand &&
		leftNumber.exponent.Cmp(rightNumber.exponent) == 0, nil
}

type connectorNumber struct {
	negative    bool
	zero        bool
	significand string
	exponent    *big.Int
}

func parseConnectorNumber(value jsontext.Value) (connectorNumber, error) {
	raw := bytes.TrimSpace(value)
	if len(raw) == 0 {
		return connectorNumber{}, connectorRunError("checkpoint", "must be valid JSON")
	}
	negative := false
	if raw[0] == '-' {
		negative = true
		raw = raw[1:]
	}
	mantissa, exponentText, hasExponent := bytes.Cut(raw, []byte("e"))
	if !hasExponent {
		mantissa, exponentText, _ = bytes.Cut(raw, []byte("E"))
	}
	fractionDigits := 0
	if whole, fraction, found := bytes.Cut(mantissa, []byte(".")); found {
		fractionDigits = len(fraction)
		mantissa = append(bytes.Clone(whole), fraction...)
	}
	mantissa = bytes.TrimLeft(mantissa, "0")
	if len(mantissa) == 0 {
		return connectorNumber{zero: true}, nil
	}
	trailingZeros := len(mantissa) - len(bytes.TrimRight(mantissa, "0"))
	mantissa = mantissa[:len(mantissa)-trailingZeros]
	exponent := new(big.Int).Neg(big.NewInt(int64(fractionDigits)))
	if len(exponentText) > 0 {
		if exponentText[0] == '+' {
			exponentText = exponentText[1:]
		}
		parsed, ok := new(big.Int).SetString(string(exponentText), 10)
		if !ok {
			return connectorNumber{}, connectorRunError("checkpoint", "must be valid JSON")
		}
		exponent.Add(exponent, parsed)
	}
	exponent.Add(exponent, big.NewInt(int64(trailingZeros)))
	return connectorNumber{negative: negative, significand: string(mantissa), exponent: exponent}, nil
}
