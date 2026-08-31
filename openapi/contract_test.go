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

package openapi

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

const activeOpenAPISHA256 = "08b9c71cf286307b4e4573fad7198fec37f963192bfa70f15bac33b228283798"

func TestFrozenOpenAPIAndGeneratedHandlerStayInSync(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("powercontext.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(contents)); got != activeOpenAPISHA256 {
		t.Fatalf("OpenAPI SHA-256 = %s, want active contract %s", got, activeOpenAPISHA256)
	}
	if !bytes.Contains(contents, []byte("\n  version: 0.1.0\n")) {
		t.Fatal("OpenAPI info.version must match the v0.1.0 release")
	}

	operationIDs := make(map[string]struct{})
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "operationId:") {
			continue
		}
		id := strings.TrimSpace(strings.TrimPrefix(line, "operationId:"))
		if id == "" {
			t.Fatal("OpenAPI contains a blank operationId")
		}
		if _, duplicate := operationIDs[id]; duplicate {
			t.Fatalf("duplicate operationId %q", id)
		}
		operationIDs[id] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if got := len(operationIDs); got != 53 {
		t.Fatalf("OpenAPI operations = %d, want 53", got)
	}
	handler := reflect.TypeOf((*v1.Handler)(nil)).Elem()
	if got := handler.NumMethod(); got != len(operationIDs) {
		t.Fatalf("generated Handler methods = %d, OpenAPI operations = %d", got, len(operationIDs))
	}
}

func TestBundledDSHOpenAPIMatchesAuthority(t *testing.T) {
	t.Parallel()
	authority, err := os.ReadFile("powercontext.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bundled, err := os.ReadFile("../integrations/dsh/plugins/powercontext/openapi/powercontext.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bundled, authority) {
		t.Fatal("DSH bundled OpenAPI drifted from openapi/powercontext.yaml")
	}
}
