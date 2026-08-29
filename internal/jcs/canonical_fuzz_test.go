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

package jcs

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func FuzzMarshalCanonicalJSONIsIdempotent(f *testing.F) {
	f.Add([]byte(`{"z":"e\u0301","a":[2,true,null]}`))
	f.Add([]byte(`{"é":1,"e\u0301":2}`))
	f.Add([]byte(`[-0.0,1e30,"\u2028"]`))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 16*1024 {
			t.Skip()
		}
		decoder := json.NewDecoder(bytes.NewReader(input))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			return
		}
		first, err := Marshal(value)
		if err != nil {
			return
		}
		if !json.Valid(first) {
			t.Fatalf("canonical output is not JSON: %q", first)
		}
		decoder = json.NewDecoder(bytes.NewReader(first))
		decoder.UseNumber()
		var canonicalValue any
		if decodeErr := decoder.Decode(&canonicalValue); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		second, err := Marshal(canonicalValue)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("canonicalization is not idempotent:\nfirst:  %s\nsecond: %s", first, second)
		}
	})
}
