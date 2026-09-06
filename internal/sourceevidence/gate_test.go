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

package sourceevidence

import (
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/source"
)

func TestRequireUsesClosedProductTypeAllowlist(t *testing.T) {
	content, err := source.RestoreContentSource("content-1", source.Captured, nil, "content", nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest, raw := acceptedObservation(t)
	if _, admissionErr := source.AdmitObservation(manifest, raw); admissionErr != nil {
		t.Fatal(admissionErr)
	}
	accepted, err := NewAcceptedObservation(raw)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotSource(t)

	for _, value := range []source.Value{content, accepted, snapshot} {
		if err := Require(value); err != nil {
			t.Fatalf("Require(%T) = %v", value, err)
		}
	}
	for _, value := range []source.Value{raw, &raw, forgedEvidenceSource{SourceObservation: raw}, &accepted} {
		err := Require(value)
		if _, ok := errors.AsType[*source.UnacceptedObservationError](err); !ok {
			t.Fatalf("Require(%T) error = %T %v", value, err, err)
		}
	}
}

func TestDirectAdmissionCannotMintModelEvidenceWithoutDurableAcceptance(t *testing.T) {
	manifest, raw := acceptedObservation(t)
	admitted, err := source.AdmitObservation(manifest, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, isModelValue := any(admitted).(source.Value); isModelValue {
		t.Fatal("direct source admission minted a model-capable Source without a durable acceptance marker")
	}
}

type forgedEvidenceSource struct{ source.SourceObservation }

func (forgedEvidenceSource) EvidenceSource() {}

func acceptedObservation(t *testing.T) (source.DefinitionManifest, source.SourceObservation) {
	t.Helper()
	identity, err := source.NewDefinitionIdentity("remote.closed", "1")
	if err != nil {
		t.Fatal(err)
	}
	projectionManifest, err := source.NewProjectionManifest(
		source.TextEvidenceProjectionKey(), source.TextEvidenceSchema(),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(
		identity, jsontext.Value(`{"type":"object"}`), []source.ProjectionManifest{projectionManifest},
	)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := source.NewRef(identity.Name(), "item-1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := source.NewSourceProjectionValue(
		source.TextEvidenceProjectionKey(),
		jsontext.Value(`{"source_type":"remote.closed","source_id":"item-1","content":"accepted"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := source.NewSourceObservation(
		ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured"}`),
		[]source.SourceProjectionValue{projection},
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, raw
}

func snapshotSource(t *testing.T) skill.SnapshotSource {
	t.Helper()
	registration, err := skill.NewRegistration(
		"codex:project:repository/evidence-skill", "codex", "codex", "workstation-1",
		skill.ProjectScope, "/workspace/.agents/skills/evidence-skill", strings.Repeat("a", 64),
		"evidence-skill", "Provides an immutable test snapshot.",
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := skill.NewSnapshot(registration, "---\nname: evidence-skill\n---\n")
	if err != nil {
		t.Fatal(err)
	}
	value, err := (skill.SnapshotSourceAdapter{}).Resolve(t.Context(), skill.SnapshotCapture{
		Snapshot: snapshot, Mode: skill.ImportModeImport,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
