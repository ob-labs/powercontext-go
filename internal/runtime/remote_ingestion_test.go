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
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/source"
)

func TestRemoteIngestionApplicationRegistersOnlyValidatedNonNativeDefinitions(t *testing.T) {
	valid := remoteManifest(t, "remote.note", jsontext.Value(`{"type":"object"}`), nil)
	for _, test := range []struct {
		name     string
		manifest source.DefinitionManifest
		native   bool
	}{
		{name: "native definition shadow", manifest: valid, native: true},
		{name: "invalid Draft 2020-12 schema", manifest: remoteManifest(t, "remote.invalid-schema", jsontext.Value(`{"type":5}`), nil)},
		{name: "different text evidence schema", manifest: remoteManifest(t, "remote.invalid-evidence", jsontext.Value(`{"type":"object"}`), []source.ProjectionManifest{
			remoteProjection(t, source.TextEvidenceProjectionKey(), jsontext.Value(`{"type":"object"}`)),
		})},
		{name: "manifest over 64 KiB", manifest: remoteManifest(t, "remote.large", jsontext.Value(`{"description":"`+strings.Repeat("x", 64*1024)+`"}`), nil)},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := newRemoteIngestionBackend()
			backend.native[test.manifest.Name()] = test.native
			application, err := NewRemoteIngestionApplication(New(), backend)
			if err != nil {
				t.Fatal(err)
			}
			_, err = application.Register(t.Context(), test.manifest)
			if _, ok := errors.AsType[*source.InvalidDefinitionManifestError](err); !ok {
				t.Fatalf("Register() error = %T %v", err, err)
			}
			if backend.registered != 0 {
				t.Fatalf("Register() reached storage %d times", backend.registered)
			}
			for _, secret := range []string{test.manifest.Name(), "remote", "xxxxx"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Register() error exposed %q: %v", secret, err)
				}
			}
		})
	}

	backend := newRemoteIngestionBackend()
	application, err := NewRemoteIngestionApplication(New(), backend)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := application.Register(t.Context(), valid)
	if err != nil {
		t.Fatal(err)
	}
	if backend.registered != 1 || registered.Fingerprint() != valid.Fingerprint() {
		t.Fatalf("Register() = %#v after %d store calls", registered, backend.registered)
	}
}

func TestRemoteIngestionApplicationEnforcesDraft202012OnlyAtSchemaResourceRoots(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema jsontext.Value
		secret string
	}{
		{
			name:   "Draft 7 root",
			schema: jsontext.Value(`{"$schema":"http://json-schema.org/draft-07/schema","type":"object"}`),
			secret: "draft-07",
		},
		{
			name:   "Draft 2019 root",
			schema: jsontext.Value(`{"$schema":"https://json-schema.org/draft/2019-09/schema","type":"object"}`),
			secret: "2019-09",
		},
		{
			name: "Draft 2019 nested resource",
			schema: jsontext.Value(`{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"$defs":{"embedded":{"$id":"https://powercontext.invalid/embedded","$schema":"https://json-schema.org/draft/2019-09/schema","type":"object"}}
			}`),
			secret: "embedded",
		},
		{
			name: "Draft 7 nested property resource",
			schema: jsontext.Value(`{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"properties":{"embedded":{"$id":"https://powercontext.invalid/property","$schema":"http://json-schema.org/draft-07/schema","type":"string"}}
			}`),
			secret: "property",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := remoteManifest(t, "remote.dialect", test.schema, nil)
			backend := newRemoteIngestionBackend()
			application, err := NewRemoteIngestionApplication(New(), backend)
			if err != nil {
				t.Fatal(err)
			}

			_, err = application.Register(t.Context(), manifest)
			if _, ok := errors.AsType[*source.InvalidDefinitionManifestError](err); !ok {
				t.Fatalf("Register() error = %T %v", err, err)
			}
			if backend.registered != 0 {
				t.Fatalf("Register() reached storage %d times", backend.registered)
			}
			for _, secret := range []string{"remote.dialect", test.secret, "json-schema.org"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Register() error exposed %q: %v", secret, err)
				}
			}
		})
	}

	manifest := remoteManifest(t, "remote.explicit-dialect", jsontext.Value(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}`), nil)
	backend := newRemoteIngestionBackend()
	application, err := NewRemoteIngestionApplication(New(), backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, registerErr := application.Register(t.Context(), manifest); registerErr != nil {
		t.Fatalf("explicit Draft 2020-12 Register() = %v", registerErr)
	}
	if backend.registered != 1 {
		t.Fatalf("explicit Draft 2020-12 Register() reached storage %d times", backend.registered)
	}

	for _, test := range []struct {
		name   string
		schema jsontext.Value
	}{
		{
			name: "inline non-resource dialect",
			schema: jsontext.Value(`{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"$defs":{"inline":{"$schema":"https://json-schema.org/draft/2019-09/schema","type":"object"}}
			}`),
		},
		{
			name: "examples annotation",
			schema: jsontext.Value(`{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"value":{"examples":[{"$id":"example-record","$schema":"http://json-schema.org/draft-07/schema"}]}}
			}`),
		},
		{
			name: "custom annotation",
			schema: jsontext.Value(`{
				"$schema":"https://json-schema.org/draft/2020-12/schema",
				"type":"object",
				"properties":{"value":{"x-powercontext":{"$id":"custom-record","$schema":"https://json-schema.org/draft/2019-09/schema"}}}
			}`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidateBackend := newRemoteIngestionBackend()
			candidate, candidateErr := NewRemoteIngestionApplication(New(), candidateBackend)
			if candidateErr != nil {
				t.Fatal(candidateErr)
			}
			candidateManifest := remoteManifest(t, "remote.annotation", test.schema, nil)

			if _, registerErr := candidate.Register(t.Context(), candidateManifest); registerErr != nil {
				t.Fatalf("annotation Register() = %v", registerErr)
			}
			if candidateBackend.registered != 1 {
				t.Fatalf("annotation Register() reached storage %d times", candidateBackend.registered)
			}
		})
	}
}

func TestRemoteIngestionApplicationValidatesObservationBeforeStore(t *testing.T) {
	manifest := remoteManifest(t, "remote.note", remoteObservationSchema, []source.ProjectionManifest{
		remoteProjection(t, source.TextEvidenceProjectionKey(), source.TextEvidenceSchema()),
	})
	valid := remoteObservation(t, manifest, `{"name":"item-1","definition_version":"1","materialization":"captured","large":9007199254740993}`, `{"source_type":"remote.note","source_id":"item-1","content":"preserve this","metadata":{"large":9007199254740993}}`, true)

	backend := newRemoteIngestionBackend()
	backend.manifests[manifest.Identity()] = manifest
	application, err := NewRemoteIngestionApplication(New(), backend)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := application.Submit(t.Context(), "scope-remote", valid)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Ref != valid.Ref() || receipt.Sequence != 1 || backend.added != 1 {
		t.Fatalf("Submit() = %#v after %d stores", receipt, backend.added)
	}
	if !strings.Contains(string(backend.last.Payload()), "9007199254740993") {
		t.Fatalf("Submit() lost the large JSON integer: %s", backend.last.Payload())
	}

	for _, test := range []struct {
		name        string
		observation source.SourceObservation
		missing     bool
	}{
		{name: "missing manifest", observation: valid, missing: true},
		{name: "mismatched fingerprint", observation: remoteObservation(t, manifest, `{"name":"item-1","definition_version":"1","materialization":"captured","large":1}`, `{"source_type":"remote.note","source_id":"item-1","content":"preserve this"}`, true, "wrong-fingerprint")},
		{name: "schema mismatch", observation: remoteObservation(t, manifest, `{"name":"item-1","definition_version":"1","materialization":"captured","large":"wrong"}`, `{"source_type":"remote.note","source_id":"item-1","content":"preserve this"}`, true)},
		{name: "projection set mismatch", observation: remoteObservation(t, manifest, `{"name":"item-1","definition_version":"1","materialization":"captured","large":1}`, "", false)},
		{name: "text evidence identity mismatch", observation: remoteObservation(t, manifest, `{"name":"item-1","definition_version":"1","materialization":"captured","large":1}`, `{"source_type":"remote.note","source_id":"other","content":"preserve this"}`, true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidateBackend := newRemoteIngestionBackend()
			if !test.missing {
				candidateBackend.manifests[manifest.Identity()] = manifest
			}
			candidate, newErr := NewRemoteIngestionApplication(New(), candidateBackend)
			if newErr != nil {
				t.Fatal(newErr)
			}
			_, submitErr := candidate.Submit(t.Context(), "scope-remote", test.observation)
			if test.missing {
				if _, ok := errors.AsType[*source.DefinitionNotFoundError](submitErr); !ok {
					t.Fatalf("missing manifest error = %T %v", submitErr, submitErr)
				}
			} else if _, ok := errors.AsType[*source.InvalidSourceObservationError](submitErr); !ok {
				t.Fatalf("Submit() error = %T %v", submitErr, submitErr)
			}
			if candidateBackend.added != 0 {
				t.Fatalf("Submit() stored invalid observation %d times", candidateBackend.added)
			}
			for _, secret := range []string{"remote.note", "item-1", "wrong-fingerprint", "preserve this", "other"} {
				if strings.Contains(submitErr.Error(), secret) {
					t.Fatalf("Submit() error exposed %q: %v", secret, submitErr)
				}
			}
		})
	}
}

func TestRemoteIngestionApplicationRejectsPersistedManifestShadowedByNativeDefinition(t *testing.T) {
	manifest := remoteManifest(t, source.ContentType, remoteObservationSchema, []source.ProjectionManifest{
		remoteProjection(t, source.TextEvidenceProjectionKey(), source.TextEvidenceSchema()),
	})
	observation := remoteObservation(t, manifest,
		`{"name":"item-1","definition_version":"1","materialization":"captured","large":1}`,
		`{"source_type":"content","source_id":"item-1","content":"durable text"}`,
		true,
	)
	backend := newRemoteIngestionBackend()
	// Model a remote manifest which was persisted before the local content codec
	// became active. Submit must not route that state through the native codec.
	backend.manifests[manifest.Identity()] = manifest
	backend.native[source.ContentType] = true
	application, err := NewRemoteIngestionApplication(New(), backend)
	if err != nil {
		t.Fatal(err)
	}

	_, err = application.Submit(t.Context(), "scope-remote", observation)
	if _, ok := errors.AsType[*source.DefinitionConflictError](err); !ok {
		t.Fatalf("Submit() error = %T %v", err, err)
	}
	if backend.finds != 1 || backend.added != 0 {
		t.Fatalf("Submit() performed %d manifest reads and %d Source writes", backend.finds, backend.added)
	}
	for _, secret := range []string{source.ContentType, "item-1", manifest.Fingerprint(), "durable text"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("Submit() error exposed %q: %v", secret, err)
		}
	}
}

func TestRemoteIngestionApplicationPreCanceledContextSkipsBackend(t *testing.T) {
	manifest := remoteManifest(t, "remote.canceled", remoteObservationSchema, nil)
	observation := remoteObservation(t, manifest,
		`{"name":"item-1","definition_version":"1","materialization":"captured","large":1}`,
		"",
		false,
	)

	t.Run("Register", func(t *testing.T) {
		backend := newRemoteIngestionBackend()
		application, err := NewRemoteIngestionApplication(New(), backend)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = application.Register(ctx, manifest)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Register() = %T %v", err, err)
		}
		if backend.registered != 0 {
			t.Fatalf("canceled Register() reached storage %d times", backend.registered)
		}
	})

	t.Run("Submit", func(t *testing.T) {
		backend := newRemoteIngestionBackend()
		backend.manifests[manifest.Identity()] = manifest
		application, err := NewRemoteIngestionApplication(New(), backend)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = application.Submit(ctx, "scope-remote", observation)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled Submit() = %T %v", err, err)
		}
		if backend.finds != 0 || backend.added != 0 {
			t.Fatalf("canceled Submit() performed %d manifest reads and %d Source writes", backend.finds, backend.added)
		}
	})
}

func TestRemoteIngestionApplicationRejectsObservationOverFourMiBBeforeStore(t *testing.T) {
	manifest := remoteManifest(t, "remote.large", remoteObservationSchema, []source.ProjectionManifest{
		remoteProjection(t, source.TextEvidenceProjectionKey(), source.TextEvidenceSchema()),
	})
	observation := remoteObservation(t, manifest,
		`{"name":"item-1","definition_version":"1","materialization":"captured","large":1,"extra":"`+strings.Repeat("x", 4*1024*1024)+`"}`,
		`{"source_type":"remote.large","source_id":"item-1","content":"bounded"}`, true,
	)
	backend := newRemoteIngestionBackend()
	backend.manifests[manifest.Identity()] = manifest
	application, err := NewRemoteIngestionApplication(New(), backend)
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.Submit(t.Context(), "scope-large", observation)
	if _, ok := errors.AsType[*source.InvalidSourceObservationError](err); !ok {
		t.Fatalf("large observation error = %T %v", err, err)
	}
	if backend.finds != 0 || backend.added != 0 {
		t.Fatalf("large observation reached storage %d reads and %d writes", backend.finds, backend.added)
	}
}

var remoteObservationSchema = jsontext.Value(`{"type":"object","properties":{"name":{"type":"string"},"definition_version":{"type":"string"},"materialization":{"const":"captured"},"large":{"type":"integer"}},"required":["name","definition_version","materialization","large"]}`)

type remoteIngestionBackend struct {
	manifests  map[source.DefinitionIdentity]source.DefinitionManifest
	native     map[string]bool
	registered int
	finds      int
	added      int
	last       source.SourceObservation
}

func newRemoteIngestionBackend() *remoteIngestionBackend {
	return &remoteIngestionBackend{
		manifests: make(map[source.DefinitionIdentity]source.DefinitionManifest),
		native:    make(map[string]bool),
	}
}

func (b *remoteIngestionBackend) Register(_ context.Context, manifest source.DefinitionManifest) (source.DefinitionManifest, error) {
	b.registered++
	b.manifests[manifest.Identity()] = manifest
	return manifest, nil
}

func (b *remoteIngestionBackend) Find(_ context.Context, identity source.DefinitionIdentity) (source.DefinitionManifest, bool, error) {
	b.finds++
	manifest, found := b.manifests[identity]
	return manifest, found, nil
}

func (b *remoteIngestionBackend) Add(_ context.Context, _ string, observation source.SourceObservation) (source.Ref, int64, error) {
	b.added++
	b.last = observation
	return observation.Ref(), int64(b.added), nil
}

func (b *remoteIngestionBackend) HasNativeDefinition(name string) bool { return b.native[name] }

func remoteManifest(
	t *testing.T,
	name string,
	schema jsontext.Value,
	projections []source.ProjectionManifest,
) source.DefinitionManifest {
	t.Helper()
	identity, err := source.NewDefinitionIdentity(name, "1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, schema, projections)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func remoteProjection(t *testing.T, key source.ProjectionKey, schema jsontext.Value) source.ProjectionManifest {
	t.Helper()
	projection, err := source.NewProjectionManifest(key, schema)
	if err != nil {
		t.Fatal(err)
	}
	return projection
}

func remoteObservation(
	t *testing.T,
	manifest source.DefinitionManifest,
	payload, evidence string,
	withProjection bool,
	fingerprint ...string,
) source.SourceObservation {
	t.Helper()
	valueFingerprint := manifest.Fingerprint()
	if len(fingerprint) > 0 {
		valueFingerprint = fingerprint[0]
	}
	ref, err := source.NewRef(manifest.Name(), "item-1")
	if err != nil {
		t.Fatal(err)
	}
	projections := []source.SourceProjectionValue(nil)
	if withProjection {
		projection, projectionErr := source.NewSourceProjectionValue(source.TextEvidenceProjectionKey(), jsontext.Value(evidence))
		if projectionErr != nil {
			t.Fatal(projectionErr)
		}
		projections = []source.SourceProjectionValue{projection}
	}
	observation, err := source.NewSourceObservation(ref, manifest.Version(), valueFingerprint, nil, jsontext.Value(payload), projections)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}
