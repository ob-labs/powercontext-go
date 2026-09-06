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
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/source"
)

func TestTextEvidenceContractRequiresTheStandardSchemaAndMatchingSource(t *testing.T) {
	key := source.TextEvidenceProjectionKey()
	if key.Name() != "powercontext.text-evidence" || key.Version() != "1" {
		t.Fatalf("TextEvidenceProjectionKey() = %s@%s", key.Name(), key.Version())
	}
	if err := source.ValidateTextEvidenceSchema(source.TextEvidenceSchema()); err != nil {
		t.Fatalf("standard TextEvidence schema rejected: %v", err)
	}
	if err := source.ValidateTextEvidenceSchema(jsontext.Value(`{"type":"object"}`)); err == nil {
		t.Fatal("schema drift was accepted")
	} else if _, ok := errors.AsType[*source.InvalidTextEvidenceError](err); !ok {
		t.Fatalf("schema drift error = %T %v", err, err)
	}

	ref, err := source.NewRef("remote.note", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	valid := jsontext.Value(`{"source_type":"remote.note","source_id":"item-1","content":"bounded evidence","metadata":{"large":9007199254740993}}`)
	if err := source.ValidateTextEvidence(valid, ref); err != nil {
		t.Fatalf("valid TextEvidence rejected: %v", err)
	}
	for name, value := range map[string]jsontext.Value{
		"missing required text":  jsontext.Value(`{"source_type":"remote.note","source_id":"item-1"}`),
		"wrong source identity":  jsontext.Value(`{"source_type":"remote.note","source_id":"other","content":"bounded evidence"}`),
		"metadata is not object": jsontext.Value(`{"source_type":"remote.note","source_id":"item-1","content":"bounded evidence","metadata":[]}`),
	} {
		t.Run(name, func(t *testing.T) {
			err := source.ValidateTextEvidence(value, ref)
			if _, ok := errors.AsType[*source.InvalidTextEvidenceError](err); !ok {
				t.Fatalf("ValidateTextEvidence() error = %T %v", err, err)
			}
			for _, secret := range []string{"remote.note", "item-1", "other", "bounded evidence"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("TextEvidence error exposed %q: %v", secret, err)
				}
			}
		})
	}
}
