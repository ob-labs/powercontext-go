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
	"encoding/json/jsontext"
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/source"
)

func TestAdmittedObservationRetainsValidatedWorkerPayloadWithoutModelCapability(t *testing.T) {
	manifest := acceptedObservationManifest(t, "remote.note")
	ref, err := source.NewRef("remote.note", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := source.NewSourceProjectionValue(
		source.TextEvidenceProjectionKey(),
		jsontext.Value(`{"source_type":"remote.note","source_id":"item-1","content":"admitted","metadata":{"stable":true}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := source.NewSourceObservation(
		ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured","priority":1}`),
		[]source.SourceProjectionValue{projection},
	)
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := source.AdmitObservation(manifest, raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := admitted.Observation(); got.Ref() != raw.Ref() || string(got.Payload()) != string(raw.Payload()) {
		t.Fatalf("Observation() = %#v, want equivalent worker payload", got)
	}
	if _, isValue := any(admitted).(source.Value); isValue {
		t.Fatal("admitted worker value implements source.Value")
	}
}

func TestAdmitObservationRejectsForgedDefinitionAndWorkerValues(t *testing.T) {
	manifest := acceptedObservationManifest(t, "remote.note")
	valid := acceptedObservationValue(t, manifest, "item-1", `{"name":"item-1","definition_version":"1","materialization":"captured","priority":1}`,
		`{"source_type":"remote.note","source_id":"item-1","content":"admitted"}`)

	for _, test := range []struct {
		name        string
		manifest    source.DefinitionManifest
		observation source.SourceObservation
	}{
		{
			name:        "wrong fingerprint",
			manifest:    manifest,
			observation: acceptedObservationValue(t, manifest, "item-1", `{"name":"item-1","definition_version":"1","materialization":"captured","priority":1}`, `{"source_type":"remote.note","source_id":"item-1","content":"admitted"}`, "wrong"),
		},
		{
			name:        "payload schema",
			manifest:    manifest,
			observation: acceptedObservationValue(t, manifest, "item-1", `{"name":"item-1","definition_version":"1","materialization":"captured","priority":"wrong"}`, `{"source_type":"remote.note","source_id":"item-1","content":"admitted"}`),
		},
		{
			name:        "text evidence identity",
			manifest:    manifest,
			observation: acceptedObservationValue(t, manifest, "item-1", `{"name":"item-1","definition_version":"1","materialization":"captured","priority":1}`, `{"source_type":"remote.note","source_id":"other","content":"admitted"}`),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := source.AdmitObservation(test.manifest, test.observation); err == nil {
				t.Fatal("forged observation was admitted")
			} else if _, ok := errors.AsType[*source.InvalidSourceObservationError](err); !ok {
				t.Fatalf("AdmitObservation() error = %T %v", err, err)
			}
		})
	}

	if _, err := source.AdmitObservation(manifest, valid); err != nil {
		t.Fatalf("AdmitObservation(valid) = %v", err)
	}
	ref, err := source.NewRef(manifest.Name(), "item-1")
	if err != nil {
		t.Fatal(err)
	}
	withoutProjection, err := source.NewSourceObservation(ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured","priority":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, admissionErr := source.AdmitObservation(manifest, withoutProjection); admissionErr == nil {
		t.Fatal("observation missing a declared projection was admitted")
	}
	wrongVersion, err := source.NewSourceObservation(ref, "2", manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"item-1","definition_version":"2","materialization":"captured","priority":1}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, admissionErr := source.AdmitObservation(manifest, wrongVersion); admissionErr == nil {
		t.Fatal("observation with a mismatched definition version was admitted")
	}
	undeclaredKey, err := source.NewProjectionKey("remote.undeclared", "1")
	if err != nil {
		t.Fatal(err)
	}
	undeclaredProjection, err := source.NewSourceProjectionValue(undeclaredKey, jsontext.Value(`true`))
	if err != nil {
		t.Fatal(err)
	}
	undeclaredProjectionObservation, err := source.NewSourceObservation(ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured","priority":1}`),
		[]source.SourceProjectionValue{undeclaredProjection})
	if err != nil {
		t.Fatal(err)
	}
	if _, admissionErr := source.AdmitObservation(manifest, undeclaredProjectionObservation); admissionErr == nil {
		t.Fatal("observation with an undeclared projection was admitted")
	}

	projectionKey, err := source.NewProjectionKey("remote.priority", "1")
	if err != nil {
		t.Fatal(err)
	}
	customProjection, err := source.NewProjectionManifest(projectionKey, jsontext.Value(`{"type":"integer"}`))
	if err != nil {
		t.Fatal(err)
	}
	manifestWithCustom, err := source.NewDefinitionManifest(manifest.Identity(), manifest.SourceSchema(), append(manifest.Projections(), customProjection))
	if err != nil {
		t.Fatal(err)
	}
	textProjection, err := source.NewSourceProjectionValue(source.TextEvidenceProjectionKey(), jsontext.Value(`{"source_type":"remote.note","source_id":"item-1","content":"admitted"}`))
	if err != nil {
		t.Fatal(err)
	}
	wrongProjection, err := source.NewSourceProjectionValue(projectionKey, jsontext.Value(`"wrong"`))
	if err != nil {
		t.Fatal(err)
	}
	projectionSchemaMismatch, err := source.NewSourceObservation(ref, manifestWithCustom.Version(), manifestWithCustom.Fingerprint(), nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured","priority":1}`),
		[]source.SourceProjectionValue{textProjection, wrongProjection})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.AdmitObservation(manifestWithCustom, projectionSchemaMismatch); err == nil {
		t.Fatal("observation with a schema-invalid projection was admitted")
	}
}

func TestAdmitObservationRejectsNonstandardTextEvidenceManifest(t *testing.T) {
	identity, err := source.NewDefinitionIdentity("remote.text", "1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := source.NewProjectionManifest(source.TextEvidenceProjectionKey(), jsontext.Value(`{"type":"object"}`))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"type":"object"}`), []source.ProjectionManifest{projection})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := source.NewRef(manifest.Name(), "item-1")
	if err != nil {
		t.Fatal(err)
	}
	value, err := source.NewSourceProjectionValue(source.TextEvidenceProjectionKey(), jsontext.Value(`{"source_type":"remote.text","source_id":"item-1","content":"admitted"}`))
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured"}`), []source.SourceProjectionValue{value})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.AdmitObservation(manifest, observation); err == nil {
		t.Fatal("nonstandard text-evidence manifest admitted an observation")
	} else if _, ok := errors.AsType[*source.InvalidDefinitionManifestError](err); !ok {
		t.Fatalf("AdmitObservation() error = %T %v", err, err)
	}
}

func acceptedObservationManifest(t *testing.T, name string) source.DefinitionManifest {
	t.Helper()
	identity, err := source.NewDefinitionIdentity(name, "1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := source.NewProjectionManifest(source.TextEvidenceProjectionKey(), source.TextEvidenceSchema())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"type":"object","properties":{"name":{"type":"string"},"definition_version":{"type":"string"},"materialization":{"const":"captured"},"priority":{"type":"integer"}},"required":["name","definition_version","materialization","priority"]}`), []source.ProjectionManifest{projection})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func acceptedObservationValue(
	t *testing.T,
	manifest source.DefinitionManifest,
	id, payload, evidence string,
	fingerprint ...string,
) source.SourceObservation {
	t.Helper()
	ref, err := source.NewRef(manifest.Name(), id)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := source.NewSourceProjectionValue(source.TextEvidenceProjectionKey(), jsontext.Value(evidence))
	if err != nil {
		t.Fatal(err)
	}
	valueFingerprint := manifest.Fingerprint()
	if len(fingerprint) != 0 {
		valueFingerprint = fingerprint[0]
	}
	observation, err := source.NewSourceObservation(ref, manifest.Version(), valueFingerprint, nil, jsontext.Value(payload), []source.SourceProjectionValue{projection})
	if err != nil {
		t.Fatal(err)
	}
	return observation
}
