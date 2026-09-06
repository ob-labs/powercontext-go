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

package sqlstore

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"reflect"

	"github.com/ob-labs/powercontext-go/source"
)

const (
	sourceEnvelopeEncoding            = "powercontext-source-v1"
	sourceNativeRepresentation        = "native"
	sourceObservationRepresentation   = "observation"
	redactedSourceObservationIdentity = "<redacted>"
	sourceObservationPayloadKind      = "source"
)

type sourceObservationEnvelope struct {
	Encoding       string         `json:"encoding"`
	Representation string         `json:"representation"`
	Value          jsontext.Value `json:"value"`
}

type sourceEnvelope struct {
	Encoding       string         `json:"encoding"`
	Representation string         `json:"representation"`
	Value          jsontext.Value `json:"value"`
}

type parsedSourceEnvelope struct {
	representation string
	value          jsontext.Value
	observation    source.SourceObservation
}

// SourceCodec is an exact concrete Source type route for the Python storage
// payload. It is immutable after construction.
type SourceCodec struct {
	name      string
	valueType reflect.Type
	encode    func(source.Value) ([]byte, error)
	decode    func([]byte) (source.Value, error)
}

// NewSourceCodec constructs a schema-specific exact-type Source codec.
func NewSourceCodec[S source.Value](
	name string,
	encode func(S) ([]byte, error),
	decode func([]byte) (S, error),
) (SourceCodec, error) {
	if _, err := source.NewRef(name, "codec-validation"); err != nil {
		return SourceCodec{}, err
	}
	valueType := reflect.TypeFor[S]()
	if valueType.Kind() == reflect.Interface {
		return SourceCodec{}, fmt.Errorf("sqlstore: Source codec type must be concrete")
	}
	if encode == nil || decode == nil {
		return SourceCodec{}, fmt.Errorf("sqlstore: Source codec functions must not be nil")
	}
	return SourceCodec{
		name:      name,
		valueType: valueType,
		encode: func(value source.Value) ([]byte, error) {
			if reflect.TypeOf(value) != valueType {
				return nil, fmt.Errorf("sqlstore: Source codec %q expected %s, got %T", name, valueType, value)
			}
			return encode(value.(S))
		},
		decode: func(payload []byte) (source.Value, error) {
			value, err := decode(payload)
			if err != nil {
				return nil, err
			}
			return value, nil
		},
	}, nil
}

// ContentSourceCodec returns the built-in content payload route.
func ContentSourceCodec() SourceCodec {
	codec, err := NewSourceCodec(source.ContentType, encodeContentSource, decodeContentSource)
	if err != nil {
		panic(err)
	}
	return codec
}

type contentSourceJSON struct {
	Name            string                 `json:"name"`
	Materialization source.Materialization `json:"materialization"`
	Description     *string                `json:"description"`
	Content         string                 `json:"content"`
	Metadata        map[string]any         `json:"metadata"`
}

func encodeContentSource(value source.ContentSource) ([]byte, error) {
	description, present := value.SourceDescription()
	var optional *string
	if present {
		optional = &description
	}
	return marshalJSON(contentSourceJSON{
		Name:            value.SourceName(),
		Materialization: value.SourceMaterialization(),
		Description:     optional,
		Content:         value.Content(),
		Metadata:        value.Metadata(),
	})
}

func decodeContentSource(payload []byte) (source.ContentSource, error) {
	var fields map[string]json.RawMessage
	if err := unmarshalJSON(payload, &fields); err != nil {
		return source.ContentSource{}, err
	}
	required := func(name string, destination any) error {
		raw, ok := fields[name]
		if !ok {
			return fmt.Errorf("required field %q is missing", name)
		}
		return unmarshalJSON(raw, destination)
	}
	var name string
	var materialization source.Materialization
	var content string
	if err := required("name", &name); err != nil {
		return source.ContentSource{}, err
	}
	if err := required("materialization", &materialization); err != nil {
		return source.ContentSource{}, err
	}
	if err := required("content", &content); err != nil {
		return source.ContentSource{}, err
	}
	var description *string
	if raw, ok := fields["description"]; ok && string(raw) != "null" {
		var decoded string
		if err := unmarshalJSON(raw, &decoded); err != nil {
			return source.ContentSource{}, err
		}
		description = &decoded
	}
	metadata := map[string]any{}
	if raw, ok := fields["metadata"]; ok {
		if string(raw) == "null" {
			return source.ContentSource{}, fmt.Errorf("metadata must be an object")
		}
		if err := unmarshalJSON(raw, &metadata); err != nil {
			return source.ContentSource{}, err
		}
	}
	return source.RestoreContentSource(name, materialization, description, content, metadata)
}

func encodeSourceObservation(value source.SourceObservation) ([]byte, error) {
	observation, err := jsonv2.Marshal(value)
	if err != nil {
		return nil, err
	}
	return jsonv2.Marshal(sourceObservationEnvelope{
		Encoding:       sourceEnvelopeEncoding,
		Representation: sourceObservationRepresentation,
		Value:          jsontext.Value(observation),
	})
}

// parseSourceEnvelope recognizes the current durable Source envelope. A
// legacy native payload with only an incidental encoding member remains owned
// by its exact codec; current envelopes require both the encoding and
// representation members.
func parseSourceEnvelope(payload []byte) (parsedSourceEnvelope, bool, error) {
	var marker map[string]jsontext.Value
	if err := jsonv2.Unmarshal(payload, &marker); err != nil {
		if looksLikeCurrentSourceEnvelope(payload) {
			return parsedSourceEnvelope{}, true, err
		}
		return parsedSourceEnvelope{}, false, nil
	}
	encoding, exists := marker["encoding"]
	if !exists {
		return parsedSourceEnvelope{}, false, nil
	}
	var encodingValue string
	if err := jsonv2.Unmarshal(encoding, &encodingValue); err != nil || encodingValue != sourceEnvelopeEncoding {
		return parsedSourceEnvelope{}, false, nil
	}
	if _, exists := marker["representation"]; !exists {
		return parsedSourceEnvelope{}, false, nil
	}
	var envelope sourceEnvelope
	if err := jsonv2.Unmarshal(payload, &envelope, jsonv2.RejectUnknownMembers(true)); err != nil {
		return parsedSourceEnvelope{}, true, err
	}
	if envelope.Value == nil {
		return parsedSourceEnvelope{}, true, fmt.Errorf("invalid Source envelope")
	}
	parsed := parsedSourceEnvelope{
		representation: envelope.Representation,
		value:          envelope.Value.Clone(),
	}
	switch envelope.Representation {
	case sourceNativeRepresentation:
		return parsed, true, nil
	case sourceObservationRepresentation:
		observation, err := source.ParseSourceObservation(envelope.Value)
		if err != nil {
			return parsedSourceEnvelope{}, true, err
		}
		parsed.observation = observation
		return parsed, true, nil
	default:
		return parsedSourceEnvelope{}, true, fmt.Errorf("invalid Source envelope representation")
	}
}

// looksLikeCurrentSourceEnvelope permits damaged and duplicate current
// envelopes to be classified before codec lookup while deliberately leaving a
// legacy native payload with only an encoding member to its registered codec.
func looksLikeCurrentSourceEnvelope(payload []byte) bool {
	decoder := jsontext.NewDecoder(bytes.NewReader(payload), jsontext.AllowDuplicateNames(true))
	start, err := decoder.ReadToken()
	if err != nil || start.Kind() != '{' {
		return false
	}
	var matchingEncoding, representation bool
	for decoder.PeekKind() != '}' {
		name, err := decoder.ReadToken()
		if err != nil || name.Kind() != '"' {
			return matchingEncoding && representation
		}
		switch name.String() {
		case "encoding":
			value, readErr := decoder.ReadValue()
			if readErr != nil {
				return matchingEncoding && representation
			}
			var encoding string
			if jsonv2.Unmarshal(value, &encoding) == nil && encoding == sourceEnvelopeEncoding {
				matchingEncoding = true
			}
		case "representation":
			representation = true
			if err := decoder.SkipValue(); err != nil {
				return matchingEncoding && representation
			}
		default:
			if err := decoder.SkipValue(); err != nil {
				return matchingEncoding && representation
			}
		}
	}
	return matchingEncoding && representation
}
