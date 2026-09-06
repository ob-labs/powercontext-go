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

package handoff

import (
	"encoding/json/jsontext"
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/source"
)

func TestHandoffEvidenceRejectsRawObservationBeforeDurableAcceptance(t *testing.T) {
	ref, raw, admitted := handoffAcceptedObservation(t)
	if _, isValue := any(admitted).(source.Value); isValue {
		t.Fatal("direct source admission became Handoff evidence")
	}
	citation, err := NewSourceCitation(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, sourceEvidenceErr := NewSourceEvidence(citation, raw); sourceEvidenceErr == nil {
		t.Fatal("raw observation became Handoff evidence")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](sourceEvidenceErr); !ok {
		t.Fatalf("raw evidence error = %T %v", sourceEvidenceErr, sourceEvidenceErr)
	}
	if _, sourceEvidenceErr := NewSourceEvidence(citation, &raw); sourceEvidenceErr == nil {
		t.Fatal("raw observation pointer became Handoff evidence")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](sourceEvidenceErr); !ok {
		t.Fatalf("raw pointer evidence error = %T %v", sourceEvidenceErr, sourceEvidenceErr)
	}
	wrapped := wrappedRawObservation{SourceObservation: raw}
	if _, sourceEvidenceErr := NewSourceEvidence(citation, wrapped); sourceEvidenceErr == nil {
		t.Fatal("wrapped raw observation became Handoff evidence")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](sourceEvidenceErr); !ok {
		t.Fatalf("wrapped evidence error = %T %v", sourceEvidenceErr, sourceEvidenceErr)
	}
	if _, projectorErr := NewContentEvidenceProjector(nil).ProjectSource(wrapped); projectorErr == nil {
		t.Fatal("wrapped raw observation reached Handoff evidence projector")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](projectorErr); !ok {
		t.Fatalf("wrapped projector error = %T %v", projectorErr, projectorErr)
	}
	if _, projectorErr := (DefaultEvidenceProjector{}).ProjectSource(wrapped); projectorErr == nil {
		t.Fatal("wrapped raw observation reached Handoff default evidence projector")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](projectorErr); !ok {
		t.Fatalf("wrapped default projector error = %T %v", projectorErr, projectorErr)
	}
}

type wrappedRawObservation struct{ source.SourceObservation }

func (wrappedRawObservation) EvidenceSource() {}

func handoffAcceptedObservation(t *testing.T) (source.Ref, source.SourceObservation, *source.AdmittedObservation) {
	t.Helper()
	identity, err := source.NewDefinitionIdentity("remote.handoff", "1")
	if err != nil {
		t.Fatal(err)
	}
	projectionManifest, err := source.NewProjectionManifest(source.TextEvidenceProjectionKey(), source.TextEvidenceSchema())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity,
		jsontext.Value(`{"type":"object","properties":{"name":{"type":"string"},"definition_version":{"type":"string"},"materialization":{"const":"captured"}},"required":["name","definition_version","materialization"]}`),
		[]source.ProjectionManifest{projectionManifest})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := source.NewRef("remote.handoff", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := source.NewSourceProjectionValue(source.TextEvidenceProjectionKey(), jsontext.Value(
		`{"source_type":"remote.handoff","source_id":"item-1","content":"handoff evidence"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := source.NewSourceObservation(ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured"}`),
		[]source.SourceProjectionValue{projection},
	)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := source.AdmitObservation(manifest, raw)
	if err != nil {
		t.Fatal(err)
	}
	return ref, raw, accepted
}
