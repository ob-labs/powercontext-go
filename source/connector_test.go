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
	"errors"
	"strings"
	"testing"
)

func TestConnectorBindingRequiresStableBoundedIdentity(t *testing.T) {
	binding, err := NewConnectorBinding("scope-a", "github:repository", "github", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Validate(); err != nil {
		t.Fatal(err)
	}
	if binding.ScopeID() != "scope-a" || binding.ID() != "github:repository" ||
		binding.ConnectorName() != "github" || binding.ConnectorVersion() != "v1" {
		t.Fatalf("binding = %#v", binding)
	}

	invalidUTF8 := string([]byte{0xff})
	for _, test := range []struct {
		name  string
		input [4]string
		field string
	}{
		{name: "empty scope", input: [4]string{"", "binding", "connector", "v1"}, field: "scope_id"},
		{name: "leading binding whitespace", input: [4]string{"scope", " binding", "connector", "v1"}, field: "binding_id"},
		{name: "trailing version whitespace", input: [4]string{"scope", "binding", "connector", "v1 "}, field: "connector_version"},
		{name: "invalid UTF-8 name", input: [4]string{"scope", "binding", invalidUTF8, "v1"}, field: "connector_name"},
		{name: "oversize scope", input: [4]string{strings.Repeat("s", MaxIDLength+1), "binding", "connector", "v1"}, field: "scope_id"},
		{name: "oversize binding", input: [4]string{"scope", strings.Repeat("b", MaxIDLength+1), "connector", "v1"}, field: "binding_id"},
		{name: "oversize connector name", input: [4]string{"scope", "binding", strings.Repeat("c", MaxTypeLength+1), "v1"}, field: "connector_name"},
		{name: "oversize connector version", input: [4]string{"scope", "binding", "connector", strings.Repeat("v", MaxTypeLength+1)}, field: "connector_version"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewConnectorBinding(test.input[0], test.input[1], test.input[2], test.input[3])
			invalid, ok := errors.AsType[*InvalidConnectorBindingError](err)
			if !ok || invalid.Field != test.field {
				t.Fatalf("invalid connector binding error = %T %v", err, err)
			}
		})
	}

	if err := (ConnectorBinding{}).Validate(); err == nil {
		t.Fatal("zero Connector binding was accepted")
	} else if _, ok := errors.AsType[*InvalidConnectorBindingError](err); !ok {
		t.Fatalf("zero Connector binding error = %T %v", err, err)
	}
}

func TestConnectorBindingUsesPythonWhitespaceSemantics(t *testing.T) {
	for character := rune('\u001c'); character <= '\u001f'; character++ {
		for _, value := range []string{string(character) + "scope-secret", "scope-secret" + string(character)} {
			_, err := NewConnectorBinding(value, "binding", "connector", "v1")
			invalid, ok := errors.AsType[*InvalidConnectorBindingError](err)
			if !ok || invalid.Field != "scope_id" {
				t.Fatalf("NewConnectorBinding(%U) error = %T %v", character, err, err)
			}
			if strings.Contains(err.Error(), "scope-secret") {
				t.Errorf("NewConnectorBinding(%U) disclosed connector identity: %v", character, err)
			}
		}
	}
}
