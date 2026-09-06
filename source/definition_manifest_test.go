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
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/source"
)

func TestDefinitionManifestMatchesUpstreamFingerprint(t *testing.T) {
	manifest := newManifest(t, "content", "1", jsontext.Value(`{"type":"object"}`), nil)

	const want = "sha256:3c9c8f195eb3ba1f747db8a09a6ee4a152faf46a5ffd62f167efc0d1014f9b2f"
	if manifest.Fingerprint() != want {
		t.Fatalf("Fingerprint() = %q, want %q", manifest.Fingerprint(), want)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := source.ParseDefinitionManifest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Fingerprint() != want || parsed.Name() != "content" || parsed.Version() != "1" {
		t.Fatalf("parsed manifest = name %q, version %q, fingerprint %q", parsed.Name(), parsed.Version(), parsed.Fingerprint())
	}
}

func TestDefinitionManifestFingerprintUsesJCSWithoutUnicodeNormalization(t *testing.T) {
	ordered := newManifest(t, "content", "1", jsontext.Value(`{"z":1,"a":{"b":2,"a":1}}`), nil)
	reordered := newManifest(t, "content", "1", jsontext.Value(`{"a":{"a":1,"b":2},"z":1}`), nil)
	if ordered.Fingerprint() != reordered.Fingerprint() {
		t.Fatalf("object key order changed fingerprint: %q != %q", ordered.Fingerprint(), reordered.Fingerprint())
	}

	nfc := newManifest(t, "content", "1", jsontext.Value("{\"title\":\"\u00e9\"}"), nil)
	nfd := newManifest(t, "content", "1", jsontext.Value("{\"title\":\"e\u0301\"}"), nil)
	if nfc.Fingerprint() == nfd.Fingerprint() {
		t.Fatal("NFC and NFD declarations received the same fingerprint")
	}
}

func TestDefinitionManifestFingerprintPreservesProjectionOrder(t *testing.T) {
	first := newProjection(t, "text", "1", jsontext.Value(`{"type":"string"}`))
	second := newProjection(t, "tags", "1", jsontext.Value(`{"type":"array"}`))

	forward := newManifest(t, "content", "1", jsontext.Value(`{"type":"object"}`), []source.ProjectionManifest{first, second})
	reverse := newManifest(t, "content", "1", jsontext.Value(`{"type":"object"}`), []source.ProjectionManifest{second, first})
	if forward.Fingerprint() == reverse.Fingerprint() {
		t.Fatal("projection order did not affect the fingerprint")
	}
}

func TestDefinitionManifestOwnsAllMutableJSON(t *testing.T) {
	sourceSchema := jsontext.Value(`{"type":"object"}`)
	projectionSchema := jsontext.Value(`{"type":"string"}`)
	projection := newProjection(t, "text", "1", projectionSchema)
	projections := []source.ProjectionManifest{projection}
	manifest := newManifest(t, "content", "1", sourceSchema, projections)
	fingerprint := manifest.Fingerprint()

	copy(sourceSchema, `{"evil":"value"}`)
	copy(projectionSchema, `{"evil":"value"}`)
	projections[0] = source.ProjectionManifest{}
	returnedSource := manifest.SourceSchema()
	copy(returnedSource, `{"evil":"value"}`)
	returnedProjections := manifest.Projections()
	returnedProjectionSchema := returnedProjections[0].Schema()
	copy(returnedProjectionSchema, `{"evil":"value"}`)
	returnedProjections[0] = source.ProjectionManifest{}

	if manifest.Fingerprint() != fingerprint {
		t.Fatalf("mutation changed fingerprint from %q to %q", fingerprint, manifest.Fingerprint())
	}
	if got := string(manifest.SourceSchema()); got != `{"type":"object"}` {
		t.Fatalf("SourceSchema() = %s", got)
	}
	if got := string(manifest.Projections()[0].Schema()); got != `{"type":"string"}` {
		t.Fatalf("projection Schema() = %s", got)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionManifestRejectsUnsafeJSONIntegers(t *testing.T) {
	identity := newIdentity(t, "content", "1")
	key := newProjectionKey(t, "text", "1")
	for _, test := range []struct {
		name   string
		value  jsontext.Value
		build  func(jsontext.Value) error
		secret string
	}{
		{
			name:  "positive source integer",
			value: jsontext.Value(`{"maximum":9007199254740992}`),
			build: func(value jsontext.Value) error {
				_, err := source.NewDefinitionManifest(identity, value, nil)
				return err
			},
			secret: "9007199254740992",
		},
		{
			name:  "negative source integer",
			value: jsontext.Value(`{"minimum":-9007199254740992}`),
			build: func(value jsontext.Value) error {
				_, err := source.NewDefinitionManifest(identity, value, nil)
				return err
			},
			secret: "-9007199254740992",
		},
		{
			name:   "projection integer",
			value:  jsontext.Value(`{"const":9007199254740992}`),
			build:  func(value jsontext.Value) error { _, err := source.NewProjectionManifest(key, value); return err },
			secret: "9007199254740992",
		},
		{
			name: "Go integer",
			build: func(jsontext.Value) error {
				_, err := source.NewDefinitionManifest(identity, map[string]any{"maximum": int64(9007199254740992)}, nil)
				return err
			},
			secret: "9007199254740992",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.build(test.value)
			_ = assertInvalidManifest(t, err)
			if strings.Contains(err.Error(), test.secret) {
				t.Fatalf("error disclosed rejected JSON value: %v", err)
			}
		})
	}

	for _, value := range []jsontext.Value{
		jsontext.Value(`{"minimum":-9007199254740991}`),
		jsontext.Value(`{"maximum":9007199254740991}`),
		jsontext.Value(`{"multipleOf":9.007199254740992e+15}`),
	} {
		if _, err := source.NewDefinitionManifest(identity, value, nil); err != nil {
			t.Fatalf("safe or floating-point JSON number %s rejected: %v", value, err)
		}
	}
}

func TestDefinitionManifestAcceptsPythonJCSFloatBoundary(t *testing.T) {
	const want = "sha256:034496481be1e18cc7517c80f55bc13fcaf60c2af34d8cb65a9a7145e4ee9ddf"
	for _, schema := range []any{
		map[string]any{"maximum": float64(9007199254740992)},
		jsontext.Value(`{"maximum":9.007199254740992e+15}`),
		jsontext.Value(`{"maximum":9007199254740992.0}`),
	} {
		manifest := newManifest(t, "content", "1", schema, nil)
		if manifest.Fingerprint() != want {
			t.Fatalf("Fingerprint() = %q, want %q", manifest.Fingerprint(), want)
		}
		if err := manifest.Validate(); err != nil {
			t.Fatalf("Validate() rejected the constructed manifest: %v", err)
		}
		payload, err := json.Marshal(manifest)
		if err != nil {
			t.Fatalf("MarshalJSON() rejected the constructed manifest: %v", err)
		}
		parsed, err := source.ParseDefinitionManifest(payload)
		if err != nil {
			t.Fatalf("ParseDefinitionManifest() rejected marshaled output: %v", err)
		}
		if parsed.Fingerprint() != want {
			t.Fatalf("round-trip Fingerprint() = %q, want %q", parsed.Fingerprint(), want)
		}
	}
}

func TestProjectionManifestPreservesPythonJCSFloatBoundary(t *testing.T) {
	projection := newProjection(t, "score", "1", map[string]any{"maximum": float64(9007199254740992)})
	if err := projection.Validate(); err != nil {
		t.Fatalf("projection Validate() rejected its constructed value: %v", err)
	}
	manifest := newManifest(t, "content", "1", jsontext.Value(`{}`), []source.ProjectionManifest{projection})
	if err := manifest.Validate(); err != nil {
		t.Fatalf("definition Validate() rejected the projection: %v", err)
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("MarshalJSON() rejected the projection: %v", err)
	}
	parsed, err := source.ParseDefinitionManifest(payload)
	if err != nil {
		t.Fatalf("ParseDefinitionManifest() rejected the projection: %v", err)
	}
	if parsed.Fingerprint() != manifest.Fingerprint() || len(parsed.Projections()) != 1 {
		t.Fatalf("round-trip manifest = fingerprint %q, projections %d", parsed.Fingerprint(), len(parsed.Projections()))
	}
}

func TestDefinitionManifestRejectsCyclicGoValues(t *testing.T) {
	if cycleKind := os.Getenv("POWERCONTEXT_MANIFEST_CYCLE_TEST"); cycleKind != "" {
		var schema map[string]any
		switch cycleKind {
		case "map":
			schema = map[string]any{}
			schema["self"] = schema
		case "slice":
			cycle := []any{nil}
			cycle[0] = cycle
			schema = map[string]any{"self": cycle}
		default:
			t.Fatalf("unknown cycle kind %q", cycleKind)
		}
		_, err := source.NewDefinitionManifest(newIdentity(t, "content", "1"), schema, nil)
		_ = assertInvalidManifest(t, err)
		return
	}

	for _, cycleKind := range []string{"map", "slice"} {
		t.Run(cycleKind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDefinitionManifestRejectsCyclicGoValues$")
			command.Env = append(os.Environ(), "POWERCONTEXT_MANIFEST_CYCLE_TEST="+cycleKind)
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatalf("cycle helper exceeded its deadline: %v", ctx.Err())
			}
			if err != nil {
				t.Fatalf("cycle helper failed: %v\n%s", err, output)
			}
		})
	}
}

func TestDefinitionManifestRejectsRemoteSchemaReferences(t *testing.T) {
	identity := newIdentity(t, "content", "1")
	key := newProjectionKey(t, "text", "1")
	for _, test := range []struct {
		name  string
		build func() error
	}{
		{
			name: "nested source ref",
			build: func() error {
				_, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"items":[{"$ref":"https://example.invalid/schema"}]}`), nil)
				return err
			},
		},
		{
			name: "projection dynamic ref",
			build: func() error {
				_, err := source.NewProjectionManifest(key, jsontext.Value(`{"allOf":[{"$dynamicRef":"other.json"}]}`))
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_ = assertInvalidManifest(t, test.build())
		})
	}

	local := newProjection(t, "text", "1", jsontext.Value(`{"$dynamicRef":"#anchor"}`))
	if _, err := source.NewDefinitionManifest(
		identity,
		jsontext.Value(`{"$ref":"#/definitions/content"}`),
		[]source.ProjectionManifest{local},
	); err != nil {
		t.Fatalf("local fragment references rejected: %v", err)
	}
}

func TestDefinitionManifestRequiresJSONObjects(t *testing.T) {
	identity := newIdentity(t, "content", "1")
	key := newProjectionKey(t, "text", "1")
	for _, test := range []struct {
		name  string
		build func() error
	}{
		{name: "source array", build: func() error { _, err := source.NewDefinitionManifest(identity, jsontext.Value(`[]`), nil); return err }},
		{name: "source invalid JSON", build: func() error {
			_, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"type":`), nil)
			return err
		}},
		{name: "source duplicate member", build: func() error {
			_, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"type":"object","type":"array"}`), nil)
			return err
		}},
		{name: "projection null", build: func() error { _, err := source.NewProjectionManifest(key, jsontext.Value(`null`)); return err }},
		{name: "non-JSON Go value", build: func() error {
			_, err := source.NewDefinitionManifest(identity, map[string]any{"callback": func() {}}, nil)
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_ = assertInvalidManifest(t, test.build())
		})
	}
}

func TestDefinitionAndProjectionIdentitiesAreBoundedAndRedacted(t *testing.T) {
	maximumProjectionPart := strings.Repeat("p", source.MaxIDLength)
	key, err := source.NewProjectionKey(maximumProjectionPart, maximumProjectionPart)
	if err != nil {
		t.Fatalf("256-character projection key rejected: %v", err)
	}
	if key.Name() != maximumProjectionPart || key.Version() != maximumProjectionPart {
		t.Fatal("projection key did not retain its maximum-length identity")
	}

	invalidUTF8 := string([]byte{0xff})
	for _, test := range []struct {
		name   string
		field  string
		value  string
		secret string
		build  func(string) error
	}{
		{name: "empty definition name", field: "name", value: "", build: func(value string) error { _, err := source.NewDefinitionIdentity(value, "1"); return err }},
		{name: "definition Python whitespace", field: "name", value: "\u001csecret-name", secret: "secret-name", build: func(value string) error { _, err := source.NewDefinitionIdentity(value, "1"); return err }},
		{name: "definition invalid UTF-8", field: "version", value: invalidUTF8, build: func(value string) error { _, err := source.NewDefinitionIdentity("content", value); return err }},
		{name: "definition oversize", field: "name", value: strings.Repeat("d", source.MaxTypeLength+1), build: func(value string) error { _, err := source.NewDefinitionIdentity(value, "1"); return err }},
		{name: "projection name oversize", field: "name", value: strings.Repeat("p", source.MaxIDLength+1), build: func(value string) error { _, err := source.NewProjectionKey(value, "1"); return err }},
		{name: "projection version oversize", field: "version", value: strings.Repeat("v", source.MaxIDLength+1), build: func(value string) error { _, err := source.NewProjectionKey("text", value); return err }},
		{name: "projection trailing whitespace", field: "version", value: "1\u001f", build: func(value string) error { _, err := source.NewProjectionKey("text", value); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.build(test.value)
			invalid := assertInvalidManifest(t, err)
			if invalid.Field != test.field {
				t.Fatalf("Field = %q, want %q", invalid.Field, test.field)
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("error disclosed rejected identity: %v", err)
			}
		})
	}

	if err := (source.DefinitionIdentity{}).Validate(); err == nil {
		t.Fatal("zero DefinitionIdentity was accepted")
	}
	if err := (source.ProjectionKey{}).Validate(); err == nil {
		t.Fatal("zero ProjectionKey was accepted")
	}
	if err := (source.ProjectionManifest{}).Validate(); err == nil {
		t.Fatal("zero ProjectionManifest was accepted")
	}
	if err := (source.DefinitionManifest{}).Validate(); err == nil {
		t.Fatal("zero DefinitionManifest was accepted")
	}
}

func TestDefinitionManifestLimitsProjectionCountAndRequiresUniqueKeys(t *testing.T) {
	identity := newIdentity(t, "content", "1")
	projection := newProjection(t, "text", "1", jsontext.Value(`{"type":"string"}`))
	duplicate := []source.ProjectionManifest{projection, projection}
	if _, err := source.NewDefinitionManifest(identity, jsontext.Value(`{}`), duplicate); err == nil {
		t.Fatal("duplicate projection key was accepted")
	} else {
		_ = assertInvalidManifest(t, err)
	}

	projections := make([]source.ProjectionManifest, 17)
	for index := range projections {
		projections[index] = newProjection(t, "projection-"+string(rune('a'+index)), "1", jsontext.Value(`{}`))
	}
	if _, err := source.NewDefinitionManifest(identity, jsontext.Value(`{}`), projections); err == nil {
		t.Fatal("17 projections were accepted")
	} else {
		_ = assertInvalidManifest(t, err)
	}
}

func TestParseDefinitionManifestIsStrict(t *testing.T) {
	const fingerprint = "sha256:3c9c8f195eb3ba1f747db8a09a6ee4a152faf46a5ffd62f167efc0d1014f9b2f"
	valid := []byte(`{"name":"content","version":"1","fingerprint":"` + fingerprint + `","source_schema":{"type":"object"},"projections":[]}`)
	if _, err := source.ParseDefinitionManifest(valid); err != nil {
		t.Fatal(err)
	}

	invalidPayloads := map[string][]byte{
		"unknown field":        bytes.Replace(valid, []byte(`"name":`), []byte(`"unknown":true,"name":`), 1),
		"duplicate field":      bytes.Replace(valid, []byte(`"name":"content"`), []byte(`"name":"other","name":"content"`), 1),
		"missing field":        bytes.Replace(valid, []byte(`,"source_schema":{"type":"object"}`), nil, 1),
		"trailing value":       append(bytes.Clone(valid), []byte(` {}`)...),
		"fingerprint mismatch": bytes.Replace(valid, []byte(fingerprint), []byte("sha256:"+strings.Repeat("0", 64)), 1),
	}
	for name, payload := range invalidPayloads {
		t.Run(name, func(t *testing.T) {
			_, err := source.ParseDefinitionManifest(payload)
			_ = assertInvalidManifest(t, err)
		})
	}
}

func TestParseDefinitionManifestDefaultsOmittedProjectionsToEmpty(t *testing.T) {
	const fingerprint = "sha256:3c9c8f195eb3ba1f747db8a09a6ee4a152faf46a5ffd62f167efc0d1014f9b2f"
	payload := []byte(`{"name":"content","version":"1","fingerprint":"` + fingerprint + `","source_schema":{"type":"object"}}`)

	manifest, err := source.ParseDefinitionManifest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if projections := manifest.Projections(); len(projections) != 0 {
		t.Fatalf("Projections() has %d entries, want 0", len(projections))
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"projections":[]`)) {
		t.Fatalf("MarshalJSON() = %s, want explicit empty projections", encoded)
	}
}

func newIdentity(t *testing.T, name, version string) source.DefinitionIdentity {
	t.Helper()
	identity, err := source.NewDefinitionIdentity(name, version)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func newProjectionKey(t *testing.T, name, version string) source.ProjectionKey {
	t.Helper()
	key, err := source.NewProjectionKey(name, version)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func newProjection(t *testing.T, name, version string, schema any) source.ProjectionManifest {
	t.Helper()
	manifest, err := source.NewProjectionManifest(newProjectionKey(t, name, version), schema)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func newManifest(
	t *testing.T,
	name, version string,
	schema any,
	projections []source.ProjectionManifest,
) source.DefinitionManifest {
	t.Helper()
	manifest, err := source.NewDefinitionManifest(newIdentity(t, name, version), schema, projections)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func assertInvalidManifest(t *testing.T, err error) *source.InvalidDefinitionManifestError {
	t.Helper()
	invalid, ok := errors.AsType[*source.InvalidDefinitionManifestError](err)
	if !ok {
		t.Fatalf("error = %T %v, want *source.InvalidDefinitionManifestError", err, err)
	}
	return invalid
}
