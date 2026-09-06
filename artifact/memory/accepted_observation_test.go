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

package memory

import (
	"encoding/json/jsontext"
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/source"
)

func TestMemoryEvidenceRejectsRawObservationBeforeCandidateModelInput(t *testing.T) {
	raw, admitted := memoryAcceptedObservation(t)
	if _, isValue := any(admitted).(source.Value); isValue {
		t.Fatal("direct source admission became Memory model evidence")
	}
	if _, err := NewCandidateRequest([]source.Value{raw}, nil, nil); err == nil {
		t.Fatal("raw observation reached CandidateRequest")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](err); !ok {
		t.Fatalf("raw CandidateRequest error = %T %v", err, err)
	}
	if _, err := NewContentEvidenceProjector(nil).ProjectSource(raw); err == nil {
		t.Fatal("raw observation reached Memory evidence projector")
	}
	if _, err := NewCandidateRequest([]source.Value{&raw}, nil, nil); err == nil {
		t.Fatal("raw observation pointer reached CandidateRequest")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](err); !ok {
		t.Fatalf("raw pointer CandidateRequest error = %T %v", err, err)
	}
	if _, err := NewContentEvidenceProjector(nil).ProjectSource(&raw); err == nil {
		t.Fatal("raw observation pointer reached Memory evidence projector")
	}
	wrapped := wrappedRawObservation{SourceObservation: raw}
	if _, err := NewCandidateRequest([]source.Value{wrapped}, nil, nil); err == nil {
		t.Fatal("wrapped raw observation reached Memory candidate input")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](err); !ok {
		t.Fatalf("wrapped CandidateRequest error = %T %v", err, err)
	}
	if _, err := NewContentEvidenceProjector(nil).ProjectSource(wrapped); err == nil {
		t.Fatal("wrapped raw observation reached Memory evidence projector")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](err); !ok {
		t.Fatalf("wrapped projector error = %T %v", err, err)
	}
	if _, err := (DefaultEvidenceProjector{}).ProjectSource(wrapped); err == nil {
		t.Fatal("wrapped raw observation reached Memory default evidence projector")
	} else if _, ok := errors.AsType[*source.UnacceptedObservationError](err); !ok {
		t.Fatalf("wrapped default projector error = %T %v", err, err)
	}
}

type wrappedRawObservation struct{ source.SourceObservation }

func (wrappedRawObservation) EvidenceSource() {}

func memoryAcceptedObservation(t *testing.T) (source.SourceObservation, *source.AdmittedObservation) {
	t.Helper()
	identity, err := source.NewDefinitionIdentity("remote.note", "1")
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
	ref, err := source.NewRef("remote.note", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := source.NewSourceProjectionValue(source.TextEvidenceProjectionKey(), jsontext.Value(
		`{"source_type":"remote.note","source_id":"item-1","content":"admitted evidence"}`,
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
	return raw, accepted
}
