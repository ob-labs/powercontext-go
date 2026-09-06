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
	"cmp"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"unicode/utf8"
)

// InvalidSourceObservationError reports invalid captured worker data without
// disclosing its identity, fingerprint, description, or payload.
type InvalidSourceObservationError struct {
	Field  string
	Detail string
}

func (e *InvalidSourceObservationError) Error() string {
	return fmt.Sprintf("invalid Source observation %s: %s", e.Field, e.Detail)
}

// InvalidSourceProjectionError reports invalid or absent worker projection data
// without disclosing the projection key or its contents.
type InvalidSourceProjectionError struct {
	Field  string
	Detail string
}

func (e *InvalidSourceProjectionError) Error() string {
	return fmt.Sprintf("invalid Source projection %s: %s", e.Field, e.Detail)
}

// SourceProjectionValue retains one immutable named result computed by a worker.
type SourceProjectionValue struct {
	key   ProjectionKey
	value jsontext.Value
}

// NewSourceProjectionValue freezes any valid JSON value, including JSON null.
// Data is not canonicalized or subjected to JSON Schema reference restrictions.
func NewSourceProjectionValue(key ProjectionKey, value jsontext.Value) (SourceProjectionValue, error) {
	projection := SourceProjectionValue{key: key, value: value.Clone()}
	if err := projection.Validate(); err != nil {
		return SourceProjectionValue{}, err
	}
	return projection, nil
}

func (p SourceProjectionValue) Key() ProjectionKey { return p.key }

// Value returns an isolated copy of the worker's JSON value.
func (p SourceProjectionValue) Value() jsontext.Value { return p.value.Clone() }

// Validate rejects an invalid key or JSON value, including zero values.
func (p SourceProjectionValue) Validate() error {
	if err := p.key.Validate(); err != nil {
		return &InvalidSourceProjectionError{Field: "key", Detail: "must contain a valid name and version"}
	}
	if !p.value.IsValid() {
		return &InvalidSourceProjectionError{Field: "value", Detail: "must be valid JSON"}
	}
	return nil
}

// MarshalJSON emits the worker projection wire shape without changing numbers.
func (p SourceProjectionValue) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Key   projectionKeyWire `json:"key"`
		Value jsontext.Value    `json:"value"`
	}{Key: projectionKeyWire{Name: p.key.Name(), Version: p.key.Version()}, Value: p.value})
}

// ParseSourceProjectionValue strictly parses a transported worker projection.
func ParseSourceProjectionValue(payload []byte) (SourceProjectionValue, error) {
	var wire struct {
		Key   projectionKeyWire `json:"key"`
		Value jsontext.Value    `json:"value"`
	}
	if err := json.Unmarshal(payload, &wire, json.RejectUnknownMembers(true)); err != nil {
		return SourceProjectionValue{}, &InvalidSourceProjectionError{Field: "json", Detail: "must contain only valid projection fields"}
	}
	key, err := NewProjectionKey(wire.Key.Name, wire.Key.Version)
	if err != nil {
		return SourceProjectionValue{}, &InvalidSourceProjectionError{Field: "key", Detail: "must contain a valid name and version"}
	}
	return NewSourceProjectionValue(key, wire.Value)
}

// SourceObservation is an immutable captured Source and worker-owned projections.
// Its fingerprint is opaque; a registered Definition validates it separately.
type SourceObservation struct {
	ref                   Ref
	definitionVersion     string
	definitionFingerprint string
	description           *string
	payload               jsontext.Value
	projections           []SourceProjectionValue
}

// NewSourceObservation freezes a captured worker result with a matching payload
// envelope. An empty definitionVersion selects the default version "1".
func NewSourceObservation(
	ref Ref,
	definitionVersion, definitionFingerprint string,
	description *string,
	payload jsontext.Value,
	projections []SourceProjectionValue,
) (SourceObservation, error) {
	observation := SourceObservation{
		ref:                   ref,
		definitionVersion:     cmp.Or(definitionVersion, "1"),
		definitionFingerprint: definitionFingerprint,
		description:           cloneOptionalText(description),
		payload:               payload.Clone(),
		projections:           make([]SourceProjectionValue, len(projections)),
	}
	for index, projection := range projections {
		observation.projections[index] = SourceProjectionValue{key: projection.key, value: projection.value.Clone()}
	}
	if err := observation.Validate(); err != nil {
		return SourceObservation{}, err
	}
	return observation, nil
}

func (o SourceObservation) Ref() Ref                      { return o.ref }
func (o SourceObservation) DefinitionVersion() string     { return o.definitionVersion }
func (o SourceObservation) DefinitionFingerprint() string { return o.definitionFingerprint }
func (o SourceObservation) SourceName() string            { return o.ref.ID() }
func (o SourceObservation) SourceMaterialization() Materialization {
	return Captured
}

func (o SourceObservation) SourceDescription() (string, bool) {
	if o.description == nil {
		return "", false
	}
	return *o.description, true
}

// Payload returns an isolated copy of the captured worker payload.
func (o SourceObservation) Payload() jsontext.Value { return o.payload.Clone() }

// Projections returns isolated values in worker declaration order.
func (o SourceObservation) Projections() []SourceProjectionValue {
	projections := make([]SourceProjectionValue, len(o.projections))
	for index, projection := range o.projections {
		projections[index] = SourceProjectionValue{key: projection.key, value: projection.value.Clone()}
	}
	return projections
}

// Projection returns an isolated worker result or a typed missing-key error.
func (o SourceObservation) Projection(key ProjectionKey) (jsontext.Value, error) {
	for _, projection := range o.projections {
		if projection.key == key {
			return projection.value.Clone(), nil
		}
	}
	return nil, &InvalidSourceProjectionError{Field: "key", Detail: "was not supplied by the worker"}
}

// Validate checks identity, payload envelope, and projection key uniqueness.
// Definition fingerprint, schema, and declared projection-set checks are owned
// by the component accepting the observation against a registered Definition.
func (o SourceObservation) Validate() error {
	if !utf8.ValidString(o.ref.Type()) || !utf8.ValidString(o.ref.ID()) {
		return observationError("ref", "must contain valid UTF-8")
	}
	if _, err := NewRef(o.ref.Type(), o.ref.ID()); err != nil {
		return observationError("ref", "must contain a valid Source type and identifier")
	}
	if !utf8.ValidString(o.definitionVersion) {
		return observationError("definition_version", "must contain valid UTF-8")
	}
	if err := validateReferencePart("definition_version", o.definitionVersion, MaxIDLength); err != nil {
		return observationError("definition_version", "must be a bounded non-empty trimmed string")
	}
	if !utf8.ValidString(o.definitionFingerprint) {
		return observationError("definition_fingerprint", "must contain valid UTF-8")
	}
	if o.description != nil && !utf8.ValidString(*o.description) {
		return observationError("description", "must contain valid UTF-8")
	}
	if err := o.validatePayload(); err != nil {
		return err
	}
	seen := make(map[ProjectionKey]struct{}, len(o.projections))
	for _, projection := range o.projections {
		if err := projection.Validate(); err != nil {
			return observationError("projections", "must contain valid worker projections")
		}
		if _, exists := seen[projection.key]; exists {
			return observationError("projections", "keys must be unique")
		}
		seen[projection.key] = struct{}{}
	}
	return nil
}

func (o SourceObservation) validatePayload() error {
	if o.payload.Kind() != '{' {
		return observationError("payload", "must be a JSON object")
	}
	var payload map[string]jsontext.Value
	if err := json.Unmarshal(o.payload, &payload); err != nil {
		return observationError("payload", "must be a valid JSON object")
	}
	for _, expected := range []struct{ field, value string }{
		{field: "name", value: o.ref.ID()},
		{field: "definition_version", value: o.definitionVersion},
		{field: "materialization", value: string(Captured)},
	} {
		value, ok := sourceDataString(payload[expected.field])
		if !ok || value != expected.value {
			return observationError(expected.field, "payload does not match its envelope")
		}
	}
	if o.description == nil {
		if description, exists := payload["description"]; exists && description.Kind() != 'n' {
			return observationError("description", "payload does not match its envelope")
		}
		return nil
	}
	description, ok := sourceDataString(payload["description"])
	if !ok || description != *o.description {
		return observationError("description", "payload does not match its envelope")
	}
	return nil
}

// MarshalJSON emits the Python-compatible captured Source Observation shape.
func (o SourceObservation) MarshalJSON() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Name                  string                  `json:"name"`
		DefinitionVersion     string                  `json:"definition_version"`
		Materialization       Materialization         `json:"materialization"`
		Description           *string                 `json:"description"`
		SourceType            string                  `json:"source_type"`
		DefinitionFingerprint string                  `json:"definition_fingerprint"`
		Payload               jsontext.Value          `json:"payload"`
		Projections           []SourceProjectionValue `json:"projections"`
	}{
		Name:                  o.ref.ID(),
		DefinitionVersion:     o.definitionVersion,
		Materialization:       Captured,
		Description:           o.description,
		SourceType:            o.ref.Type(),
		DefinitionFingerprint: o.definitionFingerprint,
		Payload:               o.payload,
		Projections:           o.projections,
	})
}

// ParseSourceObservation strictly parses a worker result, applying defaults only
// to omitted fields. Explicit empty versions and null collections are invalid.
func ParseSourceObservation(payload []byte) (SourceObservation, error) {
	var wire struct {
		Name                  jsontext.Value `json:"name"`
		DefinitionVersion     jsontext.Value `json:"definition_version"`
		Materialization       jsontext.Value `json:"materialization"`
		Description           jsontext.Value `json:"description"`
		SourceType            jsontext.Value `json:"source_type"`
		DefinitionFingerprint jsontext.Value `json:"definition_fingerprint"`
		Payload               jsontext.Value `json:"payload"`
		Projections           jsontext.Value `json:"projections"`
	}
	if err := json.Unmarshal(payload, &wire, json.RejectUnknownMembers(true)); err != nil {
		return SourceObservation{}, observationError("json", "must contain only valid observation fields")
	}
	name, nameOK := sourceDataString(wire.Name)
	sourceType, typeOK := sourceDataString(wire.SourceType)
	fingerprint, fingerprintOK := sourceDataString(wire.DefinitionFingerprint)
	if !nameOK || !typeOK || !fingerprintOK {
		return SourceObservation{}, observationError("identity", "name, source_type, and definition_fingerprint must be strings")
	}
	ref, err := NewRef(sourceType, name)
	if err != nil {
		return SourceObservation{}, observationError("ref", "must contain a valid Source type and identifier")
	}
	version := "1"
	if wire.DefinitionVersion != nil {
		value, ok := sourceDataString(wire.DefinitionVersion)
		if !ok || value == "" {
			return SourceObservation{}, observationError("definition_version", "must be a non-empty string")
		}
		version = value
	}
	if wire.Materialization != nil {
		value, ok := sourceDataString(wire.Materialization)
		if !ok || value != string(Captured) {
			return SourceObservation{}, observationError("materialization", "must be captured")
		}
	}
	var description *string
	if wire.Description != nil && wire.Description.Kind() != 'n' {
		value, ok := sourceDataString(wire.Description)
		if !ok {
			return SourceObservation{}, observationError("description", "must be a string or null")
		}
		description = new(value)
	}
	var projections []SourceProjectionValue
	if wire.Projections != nil {
		if wire.Projections.Kind() != '[' {
			return SourceObservation{}, observationError("projections", "must be an array")
		}
		var entries []jsontext.Value
		if err := json.Unmarshal(wire.Projections, &entries); err != nil {
			return SourceObservation{}, observationError("projections", "must be a valid array")
		}
		projections = make([]SourceProjectionValue, len(entries))
		for index, entry := range entries {
			projection, parseErr := ParseSourceProjectionValue(entry)
			if parseErr != nil {
				return SourceObservation{}, observationError("projections", "must contain valid worker projections")
			}
			projections[index] = projection
		}
	}
	return NewSourceObservation(ref, version, fingerprint, description, wire.Payload, projections)
}

func sourceDataString(raw jsontext.Value) (string, bool) {
	if raw.Kind() != '"' {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func observationError(field, detail string) *InvalidSourceObservationError {
	return &InvalidSourceObservationError{Field: field, Detail: detail}
}
