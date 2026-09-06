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
	"encoding/json/jsontext"
	json "encoding/json/v2"
)

var (
	textEvidenceProjectionKey = ProjectionKey{name: "powercontext.text-evidence", version: "1"}
	textEvidenceSchema        = jsontext.Value(`{"$defs":{"JsonValue":{}},"description":"Canonical JSON shape consumed as textual Artifact evidence.","properties":{"content":{"title":"Content","type":"string"},"metadata":{"additionalProperties":{"$ref":"#/$defs/JsonValue"},"title":"Metadata","type":"object"},"source_id":{"title":"Source Id","type":"string"},"source_type":{"title":"Source Type","type":"string"}},"required":["source_type","source_id","content"],"title":"TextEvidence","type":"object"}`)
)

// InvalidTextEvidenceError reports a malformed standard text-evidence
// declaration or value without disclosing its source identity or content.
type InvalidTextEvidenceError struct {
	Field  string
	Detail string
}

func (e *InvalidTextEvidenceError) Error() string {
	return "invalid Source text evidence " + e.Field + ": " + e.Detail
}

// TextEvidenceProjectionKey identifies the stable standard projection consumed
// as textual Artifact evidence.
func TextEvidenceProjectionKey() ProjectionKey { return textEvidenceProjectionKey }

// TextEvidenceSchema returns the exact Python-compatible JSON Schema for the
// standard text-evidence projection.
func TextEvidenceSchema() jsontext.Value { return textEvidenceSchema.Clone() }

// IsTextEvidenceProjection reports whether key is the standard text-evidence
// projection without accepting an unversioned or similarly named projection.
func IsTextEvidenceProjection(key ProjectionKey) bool { return key == textEvidenceProjectionKey }

// ValidateTextEvidenceSchema requires the exact standard schema declaration.
// Object member order and whitespace do not affect equality.
func ValidateTextEvidenceSchema(schema jsontext.Value) error {
	if !schema.IsValid() || schema.Kind() != '{' {
		return textEvidenceError("schema", "must be the standard JSON object schema")
	}
	actual := schema.Clone()
	expected := textEvidenceSchema.Clone()
	if err := actual.Canonicalize(); err != nil {
		return textEvidenceError("schema", "must be valid JSON")
	}
	if err := expected.Canonicalize(); err != nil {
		panic("invalid built-in TextEvidence schema")
	}
	if !bytes.Equal(actual, expected) {
		return textEvidenceError("schema", "must be the standard schema")
	}
	return nil
}

// ValidateTextEvidence verifies a standard projection value belongs to ref.
// Extra fields are retained for Python compatibility; required fields and
// metadata retain the standard model's JSON shape.
func ValidateTextEvidence(value jsontext.Value, ref Ref) error {
	if _, err := NewRef(ref.Type(), ref.ID()); err != nil {
		return textEvidenceError("source", "must have a valid observation identity")
	}
	if !value.IsValid() || value.Kind() != '{' {
		return textEvidenceError("value", "must be a JSON object")
	}
	var fields map[string]jsontext.Value
	if err := json.Unmarshal(value, &fields); err != nil {
		return textEvidenceError("value", "must be valid JSON")
	}
	sourceType, ok := textEvidenceString(fields["source_type"])
	if !ok || sourceType != ref.Type() {
		return textEvidenceError("source_type", "must match the observation")
	}
	sourceID, ok := textEvidenceString(fields["source_id"])
	if !ok || sourceID != ref.ID() {
		return textEvidenceError("source_id", "must match the observation")
	}
	if _, ok := textEvidenceString(fields["content"]); !ok {
		return textEvidenceError("content", "must be a string")
	}
	if metadata, exists := fields["metadata"]; exists && metadata.Kind() != '{' {
		return textEvidenceError("metadata", "must be a JSON object")
	}
	return nil
}

func textEvidenceString(value jsontext.Value) (string, bool) {
	if value.Kind() != '"' {
		return "", false
	}
	var result string
	if err := json.Unmarshal(value, &result); err != nil {
		return "", false
	}
	return result, true
}

func textEvidenceError(field, detail string) *InvalidTextEvidenceError {
	return &InvalidTextEvidenceError{Field: field, Detail: detail}
}
