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
	"errors"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	admissionSchemaResourceURL = "https://powercontext.invalid/source-admission-schema"
	draft202012SchemaURL       = "https://json-schema.org/draft/2020-12/schema"
)

// AdmittedObservation is a worker observation which has passed the
// definition, schema, and projection checks at an owning runtime boundary.
// It deliberately does not implement Value. A persistence adapter must
// re-admit it against the Definition stored in its write transaction and write
// a durable acceptance marker before it can become model evidence.
type AdmittedObservation struct{ observation SourceObservation }

// AdmitObservation validates a worker result against its exact immutable
// Definition before making it eligible for durable evidence admission. The
// returned value still requires a persistence adapter to verify the same
// Definition inside the transaction that writes its acceptance marker.
func AdmitObservation(manifest DefinitionManifest, observation SourceObservation) (*AdmittedObservation, error) {
	compiled, err := compileAdmissionManifest(manifest)
	if err != nil {
		return nil, err
	}
	if err := validateAdmissionObservation(manifest, compiled, observation); err != nil {
		return nil, err
	}
	return &AdmittedObservation{observation: observation}, nil
}

// ValidateDefinitionManifestForAdmission verifies that a Definition can be
// used to admit worker data under the supported JSON Schema dialect. Durable
// registries use this before storing a remote Definition.
func ValidateDefinitionManifestForAdmission(manifest DefinitionManifest) error {
	_, err := compileAdmissionManifest(manifest)
	return err
}

// Observation returns the immutable worker payload which was admitted.
func (o *AdmittedObservation) Observation() SourceObservation {
	if o == nil {
		return SourceObservation{}
	}
	return o.observation
}

// Validate checks the wrapped raw observation. It does not repeat Definition
// admission, which belongs to the current runtime boundary.
func (o *AdmittedObservation) Validate() error {
	if o == nil {
		return observationError("admission", "must not be nil")
	}
	return o.observation.Validate()
}

type admissionManifest struct {
	source      *jsonschema.Schema
	projections map[ProjectionKey]*jsonschema.Schema
}

func compileAdmissionManifest(manifest DefinitionManifest) (admissionManifest, error) {
	if err := manifest.Validate(); err != nil {
		return admissionManifest{}, err
	}
	sourceSchema, err := compileAdmissionDraft202012(manifest.SourceSchema())
	if err != nil {
		return admissionManifest{}, manifestError("schema", "must use valid JSON Schema Draft 2020-12")
	}
	projections := make(map[ProjectionKey]*jsonschema.Schema, len(manifest.Projections()))
	for _, projection := range manifest.Projections() {
		if IsTextEvidenceProjection(projection.Key()) {
			if err := ValidateTextEvidenceSchema(projection.Schema()); err != nil {
				return admissionManifest{}, manifestError("projection", "must use the standard text-evidence schema")
			}
		}
		compiled, compileErr := compileAdmissionDraft202012(projection.Schema())
		if compileErr != nil {
			return admissionManifest{}, manifestError("projection", "must use valid JSON Schema Draft 2020-12")
		}
		projections[projection.Key()] = compiled
	}
	return admissionManifest{source: sourceSchema, projections: projections}, nil
}

func validateAdmissionObservation(
	manifest DefinitionManifest,
	compiled admissionManifest,
	observation SourceObservation,
) error {
	if err := observation.Validate(); err != nil {
		return err
	}
	if observation.Ref().Type() != manifest.Name() || observation.DefinitionVersion() != manifest.Version() {
		return observationError("definition", "does not match the registered manifest identity")
	}
	if observation.DefinitionFingerprint() != manifest.Fingerprint() {
		return observationError("fingerprint", "does not match the registered manifest")
	}
	if err := validateAdmissionValue(compiled.source, observation.Payload()); err != nil {
		return observationError("schema", "does not match the registered Source schema")
	}
	projections := observation.Projections()
	if len(compiled.projections) != len(projections) {
		return observationError("projections", "must exactly match the registered manifest")
	}
	for _, projection := range projections {
		schema, found := compiled.projections[projection.Key()]
		if !found {
			return observationError("projections", "must exactly match the registered manifest")
		}
		if err := validateAdmissionValue(schema, projection.Value()); err != nil {
			return observationError("projection", "does not match the registered schema")
		}
		if IsTextEvidenceProjection(projection.Key()) {
			if err := ValidateTextEvidence(projection.Value(), observation.Ref()); err != nil {
				return observationError("text-evidence", "does not match the observation envelope")
			}
		}
	}
	return nil
}

func compileAdmissionDraft202012(schema jsontext.Value) (*jsonschema.Schema, error) {
	if err := requireAdmissionDraft202012(schema); err != nil {
		return nil, err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(admissionSchemaLoader{})
	if err := compiler.AddResource(admissionSchemaResourceURL, document); err != nil {
		return nil, err
	}
	return compiler.Compile(admissionSchemaResourceURL)
}

func validateAdmissionValue(schema *jsonschema.Schema, value jsontext.Value) error {
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(value))
	if err != nil {
		return err
	}
	return schema.Validate(instance)
}

// requireAdmissionDraft202012 prevents a nested schema resource from changing
// the Draft selected by the manifest admission contract. Annotation payloads
// remain ordinary data and are not recursively treated as schemas.
func requireAdmissionDraft202012(schema jsontext.Value) error {
	var value any
	if err := json.Unmarshal(schema, &value); err != nil {
		return err
	}
	return requireAdmissionDraft202012Value(value, true)
}

func requireAdmissionDraft202012Value(value any, resourceRoot bool) error {
	schema, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if resourceRoot {
		if dialect, exists := schema["$schema"]; exists {
			name, stringValue := dialect.(string)
			if !stringValue || !isAdmissionDraft202012SchemaURL(name) {
				return errors.New("JSON Schema must use Draft 2020-12")
			}
		}
	}
	for keyword, nested := range schema {
		var err error
		switch keyword {
		case "not", "additionalProperties", "additionalItems", "propertyNames", "contains",
			"if", "then", "else", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
			err = requireAdmissionDraft202012Subschema(nested)
		case "definitions", "properties", "patternProperties", "dependencies", "$defs", "dependentSchemas":
			err = requireAdmissionDraft202012SubschemaMap(nested)
		case "allOf", "anyOf", "oneOf", "prefixItems":
			err = requireAdmissionDraft202012SubschemaArray(nested)
		case "items":
			err = requireAdmissionDraft202012Subschema(nested)
			if err == nil {
				err = requireAdmissionDraft202012SubschemaArray(nested)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func requireAdmissionDraft202012Subschema(value any) error {
	schema, ok := value.(map[string]any)
	return requireAdmissionDraft202012Value(value, ok && hasAdmissionSchemaResourceID(schema))
}

func requireAdmissionDraft202012SubschemaMap(value any) error {
	entries, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for _, schema := range entries {
		if err := requireAdmissionDraft202012Subschema(schema); err != nil {
			return err
		}
	}
	return nil
}

func requireAdmissionDraft202012SubschemaArray(value any) error {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, schema := range entries {
		if err := requireAdmissionDraft202012Subschema(schema); err != nil {
			return err
		}
	}
	return nil
}

func hasAdmissionSchemaResourceID(value map[string]any) bool {
	id, ok := value["$id"].(string)
	return ok && id != ""
}

func isAdmissionDraft202012SchemaURL(value string) bool {
	return value == draft202012SchemaURL || value == "http://json-schema.org/draft/2020-12/schema"
}

type admissionSchemaLoader struct{}

func (admissionSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("remote JSON Schema loading is disabled")
}
