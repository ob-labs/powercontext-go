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

package source

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
)

func TestContentResourcePreservesJSONAndCanonicalDigest(t *testing.T) {
	for _, tc := range []struct{ input, canonical, text string }{
		{`null`, `null`, `null`},
		{`"null"`, `"null"`, "null"},
		{`""`, `""`, ""},
		{`"  "`, `"  "`, "  "},
		{`true`, `true`, `true`},
		{`[null, 1.0, false]`, `[null,1,false]`, `[null,1,false]`},
		{`{"z":0,"a":2}`, `{"a":2,"z":0}`, `{"a":2,"z":0}`},
		{`"\u00e9"`, "\"\u00e9\"", "\u00e9"},
		{`"e\u0301"`, "\"e\u0301\"", "e\u0301"},
		{`9007199254740991`, `9007199254740991`, `9007199254740991`},
		{`1e30`, `1e+30`, `1e+30`},
		{`9007199254740992.0`, `9007199254740992`, `9007199254740992`},
	} {
		t.Run(tc.input, func(t *testing.T) {
			raw := []byte(tc.input)
			value, err := NewContentResource("resource", raw)
			if err != nil {
				t.Fatal(err)
			}
			raw[0] = '!'
			got, err := value.ContentJSON()
			if err != nil || string(got) != tc.input || value.Content() != tc.text {
				t.Fatalf("content JSON = %s, text = %q, error = %v", got, value.Content(), err)
			}
			digest := sha256.Sum256([]byte(tc.canonical))
			if got, err := value.ContentDigest(); err != nil || got != "sha256:"+hex.EncodeToString(digest[:]) {
				t.Fatalf("digest = %s, error = %v", got, err)
			}
			got[0] = '!'
			again, _ := value.ContentJSON()
			if string(again) != tc.input {
				t.Fatal("caller mutated resource content")
			}
		})
	}
}

func TestContentResourceRejectsValuesOutsideCanonicalDomain(t *testing.T) {
	for _, input := range []string{`9007199254740992`, `9007199254740993`, `-9007199254740992`, `{"n":18446744073709551615}`, `1e400`, `{"a":1,"a":2}`, `"\ud800"`, `NaN`, ``} {
		_, err := NewContentResource("private-source", []byte(input))
		if _, ok := errors.AsType[*InvalidContentResourceError](err); !ok {
			t.Fatalf("%q: error = %v", input, err)
		}
		if fmt.Sprintf("%#v", err) != "invalid Source JSON content" {
			t.Fatalf("unredacted error representation: %#v", err)
		}
	}
}

func TestLegacyContentResourceDoesNotParseText(t *testing.T) {
	value, err := RestoreContentSource("legacy", Captured, nil, `{"n":1}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := value.ContentJSON()
	if err != nil || string(got) != `"{\"n\":1}"` {
		t.Fatalf("legacy JSON = %s, error = %v", got, err)
	}
}
