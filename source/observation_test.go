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
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/source"
)

const (
	observationPayload = `{"name":"item","definition_version":"1","materialization":"captured"}`
	observationJSON    = `{"name":"item","source_type":"worker","definition_fingerprint":"","payload":` + observationPayload + `}`
)

func TestSourceObservationDefaultsRoundTripAsCapturedValue(t *testing.T) {
	observation, err := source.NewSourceObservation(observationRef(t), "", "", nil, jsontext.Value(observationPayload), nil)
	if err != nil {
		t.Fatal(err)
	}
	var value source.Value = observation
	if value.SourceName() != "item" || value.SourceMaterialization() != source.Captured || observation.DefinitionVersion() != "1" {
		t.Fatal("default observation does not retain the captured Source identity")
	}
	if description, present := value.SourceDescription(); description != "" || present {
		t.Fatal("absent description did not retain its absence")
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []string{`"definition_version":"1"`, `"materialization":"captured"`, `"description":null`, `"projections":[]`} {
		if !bytes.Contains(encoded, []byte(member)) {
			t.Fatalf("encoded observation %s does not contain %s", encoded, member)
		}
	}
	parsed, err := source.ParseSourceObservation(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Ref() != observation.Ref() || parsed.DefinitionFingerprint() != "" || string(parsed.Payload()) != observationPayload {
		t.Fatal("round trip changed Source identity, opaque fingerprint, or payload")
	}
}

func TestSourceObservationPreservesOpaqueDataWithoutManifestRestrictions(t *testing.T) {
	const payload = `{"name":"item","definition_version":"1","materialization":"captured","$ref":"https://example.invalid/data","count":123456789012345678901234567890}`
	values := []string{`null`, `true`, `"https://example.invalid/data"`, `123456789012345678901234567890`, `{"$ref":"remote.json","$dynamicRef":"https://example.invalid/ref"}`, `[1,"two",null]`}
	projections := make([]source.SourceProjectionValue, len(values))
	for index, raw := range values {
		projection, err := source.NewSourceProjectionValue(newProjectionKey(t, string(rune('a'+index)), "1"), jsontext.Value(raw))
		if err != nil {
			t.Fatal(err)
		}
		projections[index] = projection
	}
	observation, err := source.NewSourceObservation(observationRef(t), "1", " opaque fingerprint ", nil, jsontext.Value(payload), projections)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := source.ParseSourceObservation(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.DefinitionFingerprint() != " opaque fingerprint " || string(parsed.Payload()) != payload {
		t.Fatal("raw data or fingerprint was normalized")
	}
	for index, want := range values {
		got, projectionErr := parsed.Projection(projections[index].Key())
		if projectionErr != nil || string(got) != want {
			t.Fatalf("projection %d = %s, %v; want %s", index, got, projectionErr, want)
		}
	}
}

func TestSourceObservationOwnsPayloadDescriptionAndProjectionValues(t *testing.T) {
	const payloadText = `{"name":"item","definition_version":"1","materialization":"captured","description":"original"}`
	payload := jsontext.Value(payloadText)
	rawProjection := jsontext.Value(`{"nested":["original"]}`)
	key := newProjectionKey(t, "text", "1")
	projection, err := source.NewSourceProjectionValue(key, rawProjection)
	if err != nil {
		t.Fatal(err)
	}
	projections := []source.SourceProjectionValue{projection}
	description := new("original")
	observation, err := source.NewSourceObservation(observationRef(t), "1", "opaque", description, payload, projections)
	if err != nil {
		t.Fatal(err)
	}
	clear(payload)
	clear(rawProjection)
	clear(projection.Value())
	projections[0] = source.SourceProjectionValue{}
	*description = "changed"
	clear(observation.Payload())
	returned := observation.Projections()
	clear(returned[0].Value())
	returned[0] = source.SourceProjectionValue{}
	lookup, err := observation.Projection(key)
	if err != nil {
		t.Fatal(err)
	}
	clear(lookup)
	if got, present := observation.SourceDescription(); got != "original" || !present {
		t.Fatalf("description = %q, present %v", got, present)
	}
	if string(observation.Payload()) != payloadText || string(observation.Projections()[0].Value()) != `{"nested":["original"]}` {
		t.Fatal("external mutation changed the observation")
	}
	if validateErr := observation.Validate(); validateErr != nil {
		t.Fatal(validateErr)
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := source.ParseSourceObservation(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got, present := parsed.SourceDescription(); got != "original" || !present {
		t.Fatalf("round-trip description = %q, present %v", got, present)
	}
}

func TestSourceObservationRequiresMatchingEnvelope(t *testing.T) {
	for name, payload := range map[string]string{
		"name mismatch":        strings.Replace(observationPayload, `"item"`, `"other"`, 1),
		"version mismatch":     strings.Replace(observationPayload, `"1"`, `"2"`, 1),
		"referenced payload":   strings.Replace(observationPayload, `"captured"`, `"referenced"`, 1),
		"missing identity":     `{"name":"item"}`,
		"description mismatch": strings.TrimSuffix(observationPayload, `}`) + `,"description":"private description"}`,
		"array payload":        `[]`,
		"null payload":         `null`,
		"duplicate data":       strings.TrimSuffix(observationPayload, `}`) + `,"private":"one","private":"two"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := source.NewSourceObservation(observationRef(t), "1", "", nil, jsontext.Value(payload), nil)
			assertInvalidObservation(t, err)
		})
	}
	_, err := source.NewSourceObservation(observationRef(t), "1", "", new("private description"), jsontext.Value(observationPayload), nil)
	assertInvalidObservation(t, err)
	if strings.Contains(err.Error(), "private description") {
		t.Fatal("validation error disclosed description")
	}
}

func TestSourceObservationVersionUsesSourceIdentityLimit(t *testing.T) {
	for _, version := range []string{strings.Repeat("v", 256), strings.Repeat("\u00e9", 256)} {
		payload := jsontext.Value(strings.Replace(observationPayload, `"definition_version":"1"`, `"definition_version":"`+version+`"`, 1))
		observation, err := source.NewSourceObservation(observationRef(t), version, "", nil, payload, nil)
		if err != nil {
			t.Fatalf("256-code-point version rejected: %v", err)
		}
		if err := observation.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, version := range []string{strings.Repeat("v", 257), " 1", "1\u001f", string([]byte{0xff})} {
		_, err := source.NewSourceObservation(observationRef(t), version, "", nil, jsontext.Value(observationPayload), nil)
		assertInvalidObservation(t, err)
	}
}

func TestSourceObservationRequiresUniqueOrderedProjectionKeys(t *testing.T) {
	projections := make([]source.SourceProjectionValue, 17)
	for index := range projections {
		projection, err := source.NewSourceProjectionValue(newProjectionKey(t, string(rune('a'+index)), "1"), jsontext.Value(`null`))
		if err != nil {
			t.Fatal(err)
		}
		projections[index] = projection
	}
	observation, err := source.NewSourceObservation(observationRef(t), "1", "", nil, jsontext.Value(observationPayload), projections)
	if err != nil {
		t.Fatalf("observation incorrectly inherited manifest projection limit: %v", err)
	}
	for index, got := range observation.Projections() {
		if got.Key() != projections[index].Key() {
			t.Fatal("projection declaration order changed")
		}
	}
	projections[1] = projections[0]
	_, err = source.NewSourceObservation(observationRef(t), "1", "", nil, jsontext.Value(observationPayload), projections)
	assertInvalidObservation(t, err)
	_, err = observation.Projection(newProjectionKey(t, "private-projection-name", "1"))
	if _, ok := errors.AsType[*source.InvalidSourceProjectionError](err); !ok {
		t.Fatalf("missing projection = %T %v, want typed projection error", err, err)
	}
	if strings.Contains(err.Error(), "private-projection-name") {
		t.Fatal("missing projection error disclosed the key")
	}
}

func TestParseSourceObservationAcceptsOnlyUpstreamDefaults(t *testing.T) {
	for _, optional := range []string{"", `,"definition_version":"1"`, `,"materialization":"captured"`, `,"description":null`, `,"projections":[]`} {
		payload := []byte(strings.TrimSuffix(observationJSON, `}`) + optional + `}`)
		observation, err := source.ParseSourceObservation(payload)
		if err != nil {
			t.Fatalf("valid omitted defaults rejected: %v", err)
		}
		if observation.DefinitionVersion() != "1" || len(observation.Projections()) != 0 {
			t.Fatal("omitted fields did not receive their declared defaults")
		}
	}
	for name, change := range map[string]string{
		"null version":         `,"definition_version":null`,
		"empty version":        `,"definition_version":""`,
		"null materialization": `,"materialization":null`,
		"referenced":           `,"materialization":"referenced"`,
		"numeric description":  `,"description":1`,
		"null projections":     `,"projections":null`,
		"object projections":   `,"projections":{}`,
		"unknown field":        `,"secret-field":"secret-value"`,
		"duplicate name":       `,"name":"item"`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := source.ParseSourceObservation([]byte(strings.TrimSuffix(observationJSON, `}`) + change + `}`))
			assertInvalidObservation(t, err)
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("parse error disclosed input")
			}
		})
	}
}

func TestParseSourceObservationRejectsMalformedRequiredFields(t *testing.T) {
	for name, payload := range map[string]string{
		"missing name":        strings.Replace(observationJSON, `"name":"item",`, "", 1),
		"missing type":        strings.Replace(observationJSON, `"source_type":"worker",`, "", 1),
		"missing fingerprint": strings.Replace(observationJSON, `"definition_fingerprint":"",`, "", 1),
		"null fingerprint":    strings.Replace(observationJSON, `"definition_fingerprint":""`, `"definition_fingerprint":null`, 1),
		"missing payload":     `{"name":"item","source_type":"worker","definition_fingerprint":""}`,
		"trailing value":      observationJSON + `{}`,
		"not object":          `[]`,
		"invalid UTF-8":       strings.Replace(observationJSON, "worker", string([]byte{0xff}), 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := source.ParseSourceObservation([]byte(payload))
			assertInvalidObservation(t, err)
		})
	}
}

func TestSourceProjectionValueParsingRetainsNullAndRejectsMalformedValues(t *testing.T) {
	const valid = `{"key":{"name":"text","version":"1"},"value":null}`
	projection, err := source.ParseSourceProjectionValue([]byte(valid))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(projection)
	if err != nil || string(encoded) != valid {
		t.Fatalf("null projection round trip = %s, %v", encoded, err)
	}
	boundary := strings.Replace(valid, `"text"`, `"`+strings.Repeat("k", 256)+`"`, 1)
	if _, err := source.ParseSourceProjectionValue([]byte(boundary)); err != nil {
		t.Fatalf("256-character projection key rejected: %v", err)
	}
	for name, payload := range map[string]string{
		"missing value":     `{"key":{"name":"text","version":"1"}}`,
		"missing key":       `{"value":null}`,
		"missing version":   `{"key":{"name":"text"},"value":null}`,
		"unknown key field": strings.Replace(valid, `"version":"1"`, `"version":"1","other":true`, 1),
		"duplicate key":     strings.Replace(valid, `"name":"text"`, `"name":"text","name":"text"`, 1),
		"unknown field":     strings.TrimSuffix(valid, `}`) + `,"other":true}`,
		"trailing value":    valid + ` null`,
		"257-character key": strings.Replace(valid, `"text"`, `"`+strings.Repeat("k", 257)+`"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := source.ParseSourceProjectionValue([]byte(payload))
			if _, ok := errors.AsType[*source.InvalidSourceProjectionError](err); !ok {
				t.Fatalf("projection parse error = %T %v", err, err)
			}
		})
	}
	for _, raw := range []jsontext.Value{nil, jsontext.Value(`NaN`), jsontext.Value(`{"x":1,"x":2}`)} {
		_, err := source.NewSourceProjectionValue(newProjectionKey(t, "text", "1"), raw)
		if _, ok := errors.AsType[*source.InvalidSourceProjectionError](err); !ok {
			t.Fatalf("projection construction error = %T %v", err, err)
		}
	}
}

func TestSourceObservationRejectsZeroValuesBeforeEncoding(t *testing.T) {
	assertInvalidObservation(t, (source.SourceObservation{}).Validate())
	_, err := json.Marshal(source.SourceObservation{})
	assertInvalidObservation(t, err)
	_, err = source.NewSourceObservation(source.Ref{}, "1", "", nil, jsontext.Value(observationPayload), nil)
	assertInvalidObservation(t, err)
	_, err = source.NewSourceObservation(observationRef(t), "1", "", nil, jsontext.Value(observationPayload), []source.SourceProjectionValue{{}})
	assertInvalidObservation(t, err)
	if _, ok := errors.AsType[*source.InvalidSourceProjectionError]((source.SourceProjectionValue{}).Validate()); !ok {
		t.Fatal("zero projection was not rejected")
	}
	if _, err := json.Marshal(source.SourceProjectionValue{}); err == nil {
		t.Fatal("zero projection was encoded")
	}
}

func observationRef(t *testing.T) source.Ref {
	t.Helper()
	ref, err := source.NewRef("worker", "item")
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func assertInvalidObservation(t *testing.T, err error) {
	t.Helper()
	if _, ok := errors.AsType[*source.InvalidSourceObservationError](err); !ok {
		t.Fatalf("error = %T %v, want *source.InvalidSourceObservationError", err, err)
	}
}
