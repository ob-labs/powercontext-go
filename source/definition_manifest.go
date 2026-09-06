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
	"crypto/sha256"
	"encoding/hex"
	stdjson "encoding/json"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxDefinitionProjections = 16

var maxJSONSafeInteger = func() *big.Int {
	value, ok := new(big.Int).SetString("9007199254740991", 10)
	if !ok {
		panic("invalid JSON safe integer limit")
	}
	return value
}()

// InvalidDefinitionManifestError reports an invalid declarative Source
// Definition without disclosing the rejected identity or schema contents.
type InvalidDefinitionManifestError struct {
	Field  string
	Detail string
}

func (e *InvalidDefinitionManifestError) Error() string {
	return fmt.Sprintf("invalid Source Definition manifest %s: %s", e.Field, e.Detail)
}

// DefinitionIdentity identifies one immutable version of a Source Definition.
type DefinitionIdentity struct {
	name    string
	version string
}

// NewDefinitionIdentity validates and constructs a Definition identity.
func NewDefinitionIdentity(name, version string) (DefinitionIdentity, error) {
	identity := DefinitionIdentity{name: name, version: version}
	if err := identity.Validate(); err != nil {
		return DefinitionIdentity{}, err
	}
	return identity, nil
}

func (i DefinitionIdentity) Name() string    { return i.name }
func (i DefinitionIdentity) Version() string { return i.version }

// Validate rejects zero-value and otherwise invalid Definition identities.
func (i DefinitionIdentity) Validate() error {
	if err := validateManifestIdentity("name", i.name, MaxTypeLength); err != nil {
		return err
	}
	return validateManifestIdentity("version", i.version, MaxTypeLength)
}

// ProjectionKey identifies one independently versioned Source projection.
type ProjectionKey struct {
	name    string
	version string
}

// NewProjectionKey validates and constructs a projection key.
func NewProjectionKey(name, version string) (ProjectionKey, error) {
	key := ProjectionKey{name: name, version: version}
	if err := key.Validate(); err != nil {
		return ProjectionKey{}, err
	}
	return key, nil
}

func (k ProjectionKey) Name() string    { return k.name }
func (k ProjectionKey) Version() string { return k.version }

// Validate rejects zero-value and otherwise invalid projection keys.
func (k ProjectionKey) Validate() error {
	if err := validateManifestIdentity("name", k.name, MaxIDLength); err != nil {
		return err
	}
	return validateManifestIdentity("version", k.version, MaxIDLength)
}

// ProjectionManifest declares the schema for one worker-owned projection.
type ProjectionManifest struct {
	key    ProjectionKey
	schema jsontext.Value
}

// NewProjectionManifest validates and freezes one projection declaration.
// schema must encode a JSON object; jsontext.Value preserves raw JSON numbers.
func NewProjectionManifest(key ProjectionKey, schema any) (ProjectionManifest, error) {
	if err := key.Validate(); err != nil {
		return ProjectionManifest{}, manifestError("key", "must contain a valid name and version")
	}
	encoded, err := immutableSchema(schema, "schema")
	if err != nil {
		return ProjectionManifest{}, err
	}
	return ProjectionManifest{key: key, schema: encoded}, nil
}

func (m ProjectionManifest) Key() ProjectionKey { return m.key }

// Schema returns an isolated copy of the projection JSON Schema.
func (m ProjectionManifest) Schema() jsontext.Value {
	return jsontext.Value(bytes.Clone(m.schema))
}

// Validate rejects zero-value and otherwise invalid projection declarations.
func (m ProjectionManifest) Validate() error {
	if err := m.key.Validate(); err != nil {
		return manifestError("key", "must contain a valid name and version")
	}
	_, err := immutableSchema(m.schema, "schema")
	return err
}

// DefinitionManifest is an immutable, content-addressed Source Definition
// declaration that can be stored without loading worker implementation code.
type DefinitionManifest struct {
	identity     DefinitionIdentity
	fingerprint  string
	sourceSchema jsontext.Value
	projections  []ProjectionManifest
}

// NewDefinitionManifest validates and freezes a Source Definition declaration.
// sourceSchema must encode a JSON object; projection order is significant.
func NewDefinitionManifest(
	identity DefinitionIdentity,
	sourceSchema any,
	projections []ProjectionManifest,
) (DefinitionManifest, error) {
	if err := identity.Validate(); err != nil {
		return DefinitionManifest{}, manifestError("identity", "must contain a valid name and version")
	}
	if len(projections) > maxDefinitionProjections {
		return DefinitionManifest{}, manifestError("projections", "must not contain more than 16 entries")
	}

	frozenProjections := make([]ProjectionManifest, len(projections))
	seen := make(map[ProjectionKey]struct{}, len(projections))
	for index, projection := range projections {
		if err := projection.Validate(); err != nil {
			return DefinitionManifest{}, manifestError("projections", "must contain valid projection declarations")
		}
		if _, exists := seen[projection.key]; exists {
			return DefinitionManifest{}, manifestError("projections", "keys must be unique")
		}
		seen[projection.key] = struct{}{}
		frozenProjections[index] = cloneProjectionManifest(projection)
	}

	encodedSchema, err := immutableSchema(sourceSchema, "source_schema")
	if err != nil {
		return DefinitionManifest{}, err
	}
	manifest := DefinitionManifest{
		identity:     identity,
		sourceSchema: encodedSchema,
		projections:  frozenProjections,
	}
	fingerprint, err := definitionFingerprint(manifest)
	if err != nil {
		return DefinitionManifest{}, err
	}
	manifest.fingerprint = fingerprint
	return manifest, nil
}

func (m DefinitionManifest) Identity() DefinitionIdentity { return m.identity }
func (m DefinitionManifest) Name() string                 { return m.identity.name }
func (m DefinitionManifest) Version() string              { return m.identity.version }
func (m DefinitionManifest) Fingerprint() string          { return m.fingerprint }

// SourceSchema returns an isolated copy of the Source JSON Schema.
func (m DefinitionManifest) SourceSchema() jsontext.Value {
	return jsontext.Value(bytes.Clone(m.sourceSchema))
}

// Projections returns isolated copies in declaration order.
func (m DefinitionManifest) Projections() []ProjectionManifest {
	result := make([]ProjectionManifest, len(m.projections))
	for index, projection := range m.projections {
		result[index] = cloneProjectionManifest(projection)
	}
	return result
}

// Validate rejects zero-value, mutated, and otherwise invalid manifests.
func (m DefinitionManifest) Validate() error {
	if err := m.identity.Validate(); err != nil {
		return manifestError("identity", "must contain a valid name and version")
	}
	if !validFingerprint(m.fingerprint) {
		return manifestError("fingerprint", "must use sha256 followed by lowercase hexadecimal")
	}
	if len(m.projections) > maxDefinitionProjections {
		return manifestError("projections", "must not contain more than 16 entries")
	}
	seen := make(map[ProjectionKey]struct{}, len(m.projections))
	for _, projection := range m.projections {
		if err := projection.Validate(); err != nil {
			return manifestError("projections", "must contain valid projection declarations")
		}
		if _, exists := seen[projection.key]; exists {
			return manifestError("projections", "keys must be unique")
		}
		seen[projection.key] = struct{}{}
	}
	if _, err := immutableSchema(m.sourceSchema, "source_schema"); err != nil {
		return err
	}
	expected, err := definitionFingerprint(m)
	if err != nil {
		return err
	}
	if m.fingerprint != expected {
		return manifestError("fingerprint", "does not match the declaration")
	}
	return nil
}

// MarshalJSON emits the Python-compatible Source Definition manifest shape.
func (m DefinitionManifest) MarshalJSON() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(definitionWire(m, true))
}

// ParseDefinitionManifest strictly parses and verifies a transported manifest.
func ParseDefinitionManifest(payload []byte) (DefinitionManifest, error) {
	root, err := definitionManifestObject(payload)
	if err != nil {
		return DefinitionManifest{}, err
	}
	name, err := requiredString(root["name"], "name")
	if err != nil {
		return DefinitionManifest{}, err
	}
	version, err := requiredString(root["version"], "version")
	if err != nil {
		return DefinitionManifest{}, err
	}
	providedFingerprint, err := requiredString(root["fingerprint"], "fingerprint")
	if err != nil || !validFingerprint(providedFingerprint) {
		return DefinitionManifest{}, manifestError("fingerprint", "must use sha256 followed by lowercase hexadecimal")
	}
	identity, err := NewDefinitionIdentity(name, version)
	if err != nil {
		return DefinitionManifest{}, err
	}

	projectionValues := []jsontext.Value{}
	if encodedProjections, exists := root["projections"]; exists {
		projectionValues, err = requiredArray(encodedProjections, "projections")
		if err != nil {
			return DefinitionManifest{}, err
		}
	}
	projections := make([]ProjectionManifest, len(projectionValues))
	for index, value := range projectionValues {
		projection, parseErr := parseProjectionManifest(value)
		if parseErr != nil {
			return DefinitionManifest{}, manifestError("projections", "must contain valid projection declarations")
		}
		projections[index] = projection
	}

	manifest, err := NewDefinitionManifest(identity, root["source_schema"], projections)
	if err != nil {
		return DefinitionManifest{}, err
	}
	if manifest.fingerprint != providedFingerprint {
		return DefinitionManifest{}, manifestError("fingerprint", "does not match the declaration")
	}
	return manifest, nil
}

type projectionKeyWire struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type projectionManifestWire struct {
	Key    projectionKeyWire `json:"key"`
	Schema jsontext.Value    `json:"schema"`
}

type definitionManifestWire struct {
	Name         string                   `json:"name"`
	Version      string                   `json:"version"`
	Fingerprint  string                   `json:"fingerprint,omitempty"`
	SourceSchema jsontext.Value           `json:"source_schema"`
	Projections  []projectionManifestWire `json:"projections"`
}

func definitionWire(manifest DefinitionManifest, includeFingerprint bool) definitionManifestWire {
	projections := make([]projectionManifestWire, len(manifest.projections))
	for index, projection := range manifest.projections {
		projections[index] = projectionManifestWire{
			Key: projectionKeyWire{
				Name:    projection.key.name,
				Version: projection.key.version,
			},
			Schema: jsontext.Value(bytes.Clone(projection.schema)),
		}
	}
	fingerprint := ""
	if includeFingerprint {
		fingerprint = manifest.fingerprint
	}
	return definitionManifestWire{
		Name:         manifest.identity.name,
		Version:      manifest.identity.version,
		Fingerprint:  fingerprint,
		SourceSchema: jsontext.Value(bytes.Clone(manifest.sourceSchema)),
		Projections:  projections,
	}
}

func definitionFingerprint(manifest DefinitionManifest) (string, error) {
	declaration, err := json.Marshal(definitionWire(manifest, false))
	if err != nil {
		return "", manifestError("declaration", "must be valid JSON")
	}
	canonical := jsontext.Value(declaration)
	if err := canonical.Canonicalize(); err != nil {
		return "", manifestError("declaration", "must satisfy RFC 8785")
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func parseProjectionManifest(payload jsontext.Value) (ProjectionManifest, error) {
	root, err := exactObject(payload, "projection", "key", "schema")
	if err != nil {
		return ProjectionManifest{}, err
	}
	keyObject, err := exactObject(root["key"], "key", "name", "version")
	if err != nil {
		return ProjectionManifest{}, err
	}
	name, err := requiredString(keyObject["name"], "name")
	if err != nil {
		return ProjectionManifest{}, err
	}
	version, err := requiredString(keyObject["version"], "version")
	if err != nil {
		return ProjectionManifest{}, err
	}
	key, err := NewProjectionKey(name, version)
	if err != nil {
		return ProjectionManifest{}, err
	}
	return NewProjectionManifest(key, root["schema"])
}

func immutableSchema(value any, field string) (jsontext.Value, error) {
	var encoded jsontext.Value
	if raw, ok := value.(jsontext.Value); ok {
		encoded = jsontext.Value(bytes.Clone(raw))
		if err := validateJSONNumbers(encoded); err != nil {
			return nil, manifestError(field, "must contain only interoperable JSON numbers")
		}
	} else {
		// Let the standard encoder reject cycles before recursively preserving
		// the lexical distinction between Go floating-point and integer values.
		if _, err := json.Marshal(value); err != nil {
			return nil, manifestError(field, "must contain only JSON-compatible values")
		}
		normalized, err := normalizeGoJSONValue(value)
		if err != nil {
			return nil, manifestError(field, "must contain only JSON-compatible values")
		}
		payload, err := json.Marshal(normalized)
		if err != nil {
			return nil, manifestError(field, "must be a JSON object")
		}
		encoded = jsontext.Value(payload)
	}
	if encoded.Kind() != '{' {
		return nil, manifestError(field, "must be a JSON object")
	}
	var schema map[string]any
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return nil, manifestError(field, "must be a valid JSON object")
	}
	if hasRemoteSchemaReference(schema) {
		return nil, manifestError(field, "must not contain remote schema references")
	}
	return encoded, nil
}

func normalizeGoJSONValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil, bool, string:
		return typed, nil
	case float32:
		return normalizeGoFloat(float64(typed), 32)
	case float64:
		return normalizeGoFloat(typed, 64)
	case int:
		return typed, validateGoSignedInteger(int64(typed))
	case int8:
		return typed, nil
	case int16:
		return typed, nil
	case int32:
		return typed, nil
	case int64:
		return typed, validateGoSignedInteger(typed)
	case uint:
		return typed, validateGoUnsignedInteger(uint64(typed))
	case uint8:
		return typed, nil
	case uint16:
		return typed, nil
	case uint32:
		return typed, nil
	case uint64:
		return typed, validateGoUnsignedInteger(typed)
	case stdjson.Number:
		raw := jsontext.Value(typed.String())
		if err := validateJSONNumbers(raw); err != nil {
			return nil, err
		}
		return raw, nil
	case jsontext.Value:
		if err := validateJSONNumbers(typed); err != nil {
			return nil, err
		}
		return jsontext.Value(bytes.Clone(typed)), nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for name, nested := range typed {
			normalized, err := normalizeGoJSONValue(nested)
			if err != nil {
				return nil, err
			}
			result[name] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for index, nested := range typed {
			normalized, err := normalizeGoJSONValue(nested)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	default:
		return nil, manifestError("value", "must be JSON-compatible")
	}
}

func normalizeGoFloat(value float64, bits int) (jsontext.Value, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, manifestError("number", "must be finite")
	}
	encoded := strconv.FormatFloat(value, 'g', -1, bits)
	if !strings.ContainsAny(encoded, ".eE") {
		encoded += ".0"
	}
	return jsontext.Value(encoded), nil
}

func validateGoSignedInteger(value int64) error {
	if value < -9007199254740991 || value > 9007199254740991 {
		return manifestError("number", "must be within the IEEE-754 safe integer range")
	}
	return nil
}

func validateGoUnsignedInteger(value uint64) error {
	if value > 9007199254740991 {
		return manifestError("number", "must be within the IEEE-754 safe integer range")
	}
	return nil
}

func validateJSONNumbers(payload jsontext.Value) error {
	decoder := jsontext.NewDecoder(bytes.NewReader(payload))
	for decoder.PeekKind() != 0 {
		token, err := decoder.ReadToken()
		if err != nil {
			return err
		}
		if token.Kind() != '0' {
			continue
		}
		value := token.String()
		if strings.ContainsAny(value, ".eE") {
			continue
		}
		integer, ok := new(big.Int).SetString(value, 10)
		if !ok || new(big.Int).Abs(integer).Cmp(maxJSONSafeInteger) > 0 {
			return manifestError("number", "must be within the IEEE-754 safe integer range")
		}
	}
	return nil
}

func hasRemoteSchemaReference(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for name, nested := range typed {
			if name == "$ref" || name == "$dynamicRef" {
				if reference, ok := nested.(string); ok && !strings.HasPrefix(reference, "#") {
					return true
				}
			}
			if hasRemoteSchemaReference(nested) {
				return true
			}
		}
	case []any:
		for _, nested := range typed {
			if hasRemoteSchemaReference(nested) {
				return true
			}
		}
	}
	return false
}

func exactObject(payload []byte, field string, required ...string) (map[string]jsontext.Value, error) {
	if jsontext.Value(payload).Kind() != '{' {
		return nil, manifestError(field, "must be a JSON object")
	}
	var object map[string]jsontext.Value
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, manifestError(field, "must be valid JSON without duplicate members or trailing data")
	}
	if len(object) != len(required) {
		return nil, manifestError(field, "must contain exactly the required members")
	}
	for _, name := range required {
		if _, exists := object[name]; !exists {
			return nil, manifestError(field, "must contain exactly the required members")
		}
	}
	return object, nil
}

func definitionManifestObject(payload []byte) (map[string]jsontext.Value, error) {
	if jsontext.Value(payload).Kind() != '{' {
		return nil, manifestError("payload", "must be a JSON object")
	}
	var object map[string]jsontext.Value
	if err := json.Unmarshal(payload, &object); err != nil {
		return nil, manifestError("payload", "must be valid JSON without duplicate members or trailing data")
	}
	required := [...]string{"name", "version", "fingerprint", "source_schema"}
	for _, name := range required {
		if _, exists := object[name]; !exists {
			return nil, manifestError("payload", "must contain every required member")
		}
	}
	for name := range object {
		switch name {
		case "name", "version", "fingerprint", "source_schema", "projections":
		default:
			return nil, manifestError("payload", "must not contain unknown members")
		}
	}
	return object, nil
}

func requiredString(payload jsontext.Value, field string) (string, error) {
	if payload.Kind() != '"' {
		return "", manifestError(field, "must be a string")
	}
	var value string
	if err := json.Unmarshal(payload, &value); err != nil {
		return "", manifestError(field, "must be a valid string")
	}
	return value, nil
}

func requiredArray(payload jsontext.Value, field string) ([]jsontext.Value, error) {
	if payload.Kind() != '[' {
		return nil, manifestError(field, "must be an array")
	}
	var values []jsontext.Value
	if err := json.Unmarshal(payload, &values); err != nil {
		return nil, manifestError(field, "must be a valid array")
	}
	return values, nil
}

func validateManifestIdentity(field, value string, maximum int) error {
	if !utf8.ValidString(value) {
		return manifestError(field, "must be valid UTF-8")
	}
	trimmed := strings.TrimFunc(value, isPythonWhitespace)
	if trimmed == "" {
		return manifestError(field, "must be a non-empty string")
	}
	if trimmed != value {
		return manifestError(field, "must not contain leading or trailing whitespace")
	}
	if utf8.RuneCountInString(value) > maximum {
		return manifestError(field, fmt.Sprintf("must not exceed %d characters", maximum))
	}
	return nil
}

func validFingerprint(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	digest := value[len("sha256:"):]
	for _, character := range digest {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func cloneProjectionManifest(value ProjectionManifest) ProjectionManifest {
	return ProjectionManifest{key: value.key, schema: jsontext.Value(bytes.Clone(value.schema))}
}

func manifestError(field, detail string) *InvalidDefinitionManifestError {
	return &InvalidDefinitionManifestError{Field: field, Detail: detail}
}
