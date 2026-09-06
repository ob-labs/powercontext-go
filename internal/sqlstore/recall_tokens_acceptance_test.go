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

package sqlstore

import (
	"encoding/json/jsontext"
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/source"
)

func TestRecallSourceTextRejectsRawStandardTextEvidence(t *testing.T) {
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
		`{"source_type":"remote.note","source_id":"item-1","content":"recall this"}`,
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
	if _, recallErr := recallSourceText(ref, raw); recallErr == nil {
		t.Fatal("raw worker observation became recall text")
	} else if _, ok := errors.AsType[*RecallTokenProjectionError](recallErr); !ok {
		t.Fatalf("raw recall error = %T %v", recallErr, recallErr)
	}
}
