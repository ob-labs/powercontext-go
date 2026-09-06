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
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ob-labs/powercontext-go/source"
)

const (
	maxDefinitionManifestBytes = 64 * 1024
	maxSourceObservationBytes  = 4 * 1024 * 1024
	schemaResourceURL          = "https://powercontext.invalid/remote-source-schema"
	draft202012SchemaURL       = "https://json-schema.org/draft/2020-12/schema"
)

// RemoteIngestionBackend is the transaction-owning persistence contract for
// declarative worker Source ingestion. Implementations must not hold a SQL
// transaction while RemoteIngestionApplication validates a manifest or value.
type RemoteIngestionBackend interface {
	Register(context.Context, source.DefinitionManifest) (source.DefinitionManifest, error)
	Find(context.Context, source.DefinitionIdentity) (source.DefinitionManifest, bool, error)
	Add(context.Context, string, source.SourceObservation) (source.Ref, int64, error)
	HasNativeDefinition(string) bool
}

// RemoteIngestionApplication validates remote Source declarations and
// observations before delegating their short durable writes to a backend.
type RemoteIngestionApplication struct {
	runtime *Runtime
	backend RemoteIngestionBackend
}

// NewRemoteIngestionApplication binds remote Source ingestion to Runtime
// lifecycle admission and per-Scope serialization.
func NewRemoteIngestionApplication(
	runtime *Runtime,
	backend RemoteIngestionBackend,
) (*RemoteIngestionApplication, error) {
	if runtime == nil || backend == nil {
		return nil, errors.New("runtime: Remote ingestion dependencies must not be nil")
	}
	return &RemoteIngestionApplication{runtime: runtime, backend: backend}, nil
}

// Register validates one immutable worker Definition before beginning its
// backend-owned transaction.
func (a *RemoteIngestionApplication) Register(
	ctx context.Context,
	manifest source.DefinitionManifest,
) (result source.DefinitionManifest, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		if validationErr := validateRemoteDefinition(manifest, a.backend); validationErr != nil {
			return validationErr
		}
		var registerErr error
		result, registerErr = a.backend.Register(ctx, manifest)
		return registerErr
	})
	return result, err
}

// Submit validates a worker observation against its durable Definition before
// asking the backend to append it in a short transaction.
func (a *RemoteIngestionApplication) Submit(
	ctx context.Context,
	scopeID string,
	observation source.SourceObservation,
) (result SourceReceipt, err error) {
	err = a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		if validationErr := validateRemoteObservationEnvelope(observation); validationErr != nil {
			return validationErr
		}
		identity, identityErr := source.NewDefinitionIdentity(observation.Ref().Type(), observation.DefinitionVersion())
		if identityErr != nil {
			return sourceObservationError("definition", "must identify a valid Source Definition")
		}
		manifest, found, findErr := a.backend.Find(ctx, identity)
		if findErr != nil {
			return findErr
		}
		if !found {
			return &source.DefinitionNotFoundError{}
		}
		if a.backend.HasNativeDefinition(observation.Ref().Type()) {
			return &source.DefinitionConflictError{}
		}
		if validationErr := validateRemoteObservation(manifest, observation); validationErr != nil {
			return validationErr
		}
		ref, sequence, addErr := a.backend.Add(ctx, scope, observation)
		if addErr != nil {
			return addErr
		}
		result = SourceReceipt{Ref: ref, Sequence: sequence}
		return nil
	})
	return result, err
}

func validateRemoteDefinition(manifest source.DefinitionManifest, backend RemoteIngestionBackend) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return definitionManifestError("manifest", "must be serializable")
	}
	if len(payload) > maxDefinitionManifestBytes {
		return definitionManifestError("manifest", "must not exceed 64 KiB")
	}
	if backend.HasNativeDefinition(manifest.Name()) {
		return definitionManifestError("name", "must not replace an active Source Definition")
	}
	if _, err := compileDraft202012(manifest.SourceSchema()); err != nil {
		return definitionManifestError("schema", "must be valid JSON Schema Draft 2020-12")
	}
	for _, projection := range manifest.Projections() {
		if source.IsTextEvidenceProjection(projection.Key()) {
			if err := source.ValidateTextEvidenceSchema(projection.Schema()); err != nil {
				return definitionManifestError("projection", "must use the standard text-evidence schema")
			}
		}
		if _, err := compileDraft202012(projection.Schema()); err != nil {
			return definitionManifestError("projection", "must use valid JSON Schema Draft 2020-12")
		}
	}
	return nil
}

func validateRemoteObservation(
	manifest source.DefinitionManifest,
	observation source.SourceObservation,
) error {
	if observation.Ref().Type() != manifest.Name() || observation.DefinitionVersion() != manifest.Version() {
		return sourceObservationError("definition", "does not match the registered manifest identity")
	}
	if observation.DefinitionFingerprint() != manifest.Fingerprint() {
		return sourceObservationError("fingerprint", "does not match the registered manifest")
	}
	if err := validateDraft202012Value(manifest.SourceSchema(), observation.Payload()); err != nil {
		return sourceObservationError("schema", "does not match the registered Source schema")
	}
	declared := make(map[source.ProjectionKey]source.ProjectionManifest, len(manifest.Projections()))
	for _, projection := range manifest.Projections() {
		declared[projection.Key()] = projection
	}
	supplied := observation.Projections()
	if len(declared) != len(supplied) {
		return sourceObservationError("projections", "must exactly match the registered manifest")
	}
	for _, projection := range supplied {
		declaration, exists := declared[projection.Key()]
		if !exists {
			return sourceObservationError("projections", "must exactly match the registered manifest")
		}
		if err := validateDraft202012Value(declaration.Schema(), projection.Value()); err != nil {
			return sourceObservationError("projection", "does not match the registered schema")
		}
		if source.IsTextEvidenceProjection(projection.Key()) {
			if err := source.ValidateTextEvidence(projection.Value(), observation.Ref()); err != nil {
				return sourceObservationError("text-evidence", "does not match the observation envelope")
			}
		}
	}
	return nil
}

func validateRemoteObservationEnvelope(observation source.SourceObservation) error {
	if err := observation.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(observation)
	if err != nil {
		return sourceObservationError("value", "must be serializable")
	}
	if len(payload) > maxSourceObservationBytes {
		return sourceObservationError("size", "must not exceed 4 MiB")
	}
	return nil
}

func compileDraft202012(schema jsontext.Value) (*jsonschema.Schema, error) {
	if err := requireDraft202012(schema); err != nil {
		return nil, err
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(localSchemaLoader{})
	if err := compiler.AddResource(schemaResourceURL, document); err != nil {
		return nil, err
	}
	return compiler.Compile(schemaResourceURL)
}

// requireDraft202012 prevents explicit dialect declarations from overriding
// the compiler default. Embedded schema resources with their own $id may
// independently declare a dialect, so each resource is checked before
// compilation.
func requireDraft202012(schema jsontext.Value) error {
	var value any
	if err := json.Unmarshal(schema, &value); err != nil {
		return err
	}
	return requireDraft202012Value(value, true)
}

func requireDraft202012Value(value any, resourceRoot bool) error {
	schema, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if resourceRoot {
		if dialect, exists := schema["$schema"]; exists {
			name, ok := dialect.(string)
			if !ok || !isDraft202012SchemaURL(name) {
				return errors.New("JSON Schema must use Draft 2020-12")
			}
		}
	}

	for keyword, nested := range schema {
		var err error
		switch keyword {
		case "not", "additionalProperties", "additionalItems", "propertyNames", "contains",
			"if", "then", "else", "unevaluatedProperties", "unevaluatedItems", "contentSchema":
			err = requireDraft202012Subschema(nested)
		case "definitions", "properties", "patternProperties", "dependencies", "$defs", "dependentSchemas":
			err = requireDraft202012SubschemaMap(nested)
		case "allOf", "anyOf", "oneOf", "prefixItems":
			err = requireDraft202012SubschemaArray(nested)
		case "items":
			err = requireDraft202012Subschema(nested)
			if err == nil {
				err = requireDraft202012SubschemaArray(nested)
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func requireDraft202012Subschema(value any) error {
	schema, ok := value.(map[string]any)
	return requireDraft202012Value(value, ok && hasSchemaResourceID(schema))
}

func requireDraft202012SubschemaMap(value any) error {
	entries, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for _, schema := range entries {
		if err := requireDraft202012Subschema(schema); err != nil {
			return err
		}
	}
	return nil
}

func requireDraft202012SubschemaArray(value any) error {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, schema := range entries {
		if err := requireDraft202012Subschema(schema); err != nil {
			return err
		}
	}
	return nil
}

func hasSchemaResourceID(value map[string]any) bool {
	id, ok := value["$id"].(string)
	return ok && id != ""
}

func isDraft202012SchemaURL(value string) bool {
	return value == draft202012SchemaURL ||
		value == "http://json-schema.org/draft/2020-12/schema"
}

func validateDraft202012Value(schema, value jsontext.Value) error {
	compiled, err := compileDraft202012(schema)
	if err != nil {
		return err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(value))
	if err != nil {
		return err
	}
	return compiled.Validate(instance)
}

type localSchemaLoader struct{}

func (localSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("remote JSON Schema loading is disabled")
}

func definitionManifestError(field, detail string) *source.InvalidDefinitionManifestError {
	return &source.InvalidDefinitionManifestError{Field: field, Detail: detail}
}

func sourceObservationError(field, detail string) *source.InvalidSourceObservationError {
	return &source.InvalidSourceObservationError{Field: field, Detail: detail}
}
