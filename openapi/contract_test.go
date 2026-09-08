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

const activeOpenAPISHA256 = "ab78caf229a61568675dbc9176ad0e1a48d6d48aa860fc7f3b5993ea69268ccb"

func TestREADMEStatesManagedSkillPackageArchiveBoundary(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := managedSkillPackageArchiveBoundaryError(string(contents)); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSkillPackageArchiveBoundaryHandlesInteroperabilityClaimPolarity(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		claim  string
		reject bool
	}{
		{"Cross-language ZIP canonicalization is implemented.", true},
		{"Cross-language ZIP canonicalization is supported.", true},
		{"Python Receiver archive-reference interoperability is implemented.", true},
		{"Python Receiver archive-reference interoperability is supported.", true},
		{"Python CLI archive-reference interoperability is implemented.", true},
		{"Python CLI archive-reference interoperability is supported.", true},
		{"Cross-language ZIP canonicalization is not implemented.", false},
		{"Python Receiver archive-reference interoperability is not implemented.", false},
		{"Python CLI archive-reference interoperability is not supported.", false},
	} {
		err := managedSkillPackageArchiveBoundaryError(string(contents) + "\n" + test.claim)
		if test.reject != (err != nil) {
			t.Fatalf("managed Skill package archive boundary claim %q reject=%t, error=%v", test.claim, test.reject, err)
		}
	}
}

func managedSkillPackageArchiveBoundaryError(contents string) error {
	text := strings.Join(strings.Fields(contents), " ")
	for _, required := range []string{
		"This rebaseline does not implement Artifact writes, managed Skill generation, remote Skills, or native personal services.",
		"Go-persisted immutable package snapshot",
		"`archive_base64` decodes to the exact stored archive bytes",
		"Python Receiver/CLI archive-reference interoperability remains unimplemented",
		"do not define a shared cross-language ZIP canonicalization",
	} {
		if !strings.Contains(text, required) {
			return fmt.Errorf("README.md does not state managed Skill package archive boundary %q", required)
		}
	}
	for _, prohibited := range []string{
		"this rebaseline does not implement artifact writes, managed skills, remote skills, or native personal services.",
		"cross-language zip canonicalization is implemented",
		"cross-language zip canonicalization is supported",
		"python receiver archive-reference interoperability is implemented",
		"python receiver archive-reference interoperability is supported",
		"python cli archive-reference interoperability is implemented",
		"python cli archive-reference interoperability is supported",
	} {
		if strings.Contains(strings.ToLower(text), prohibited) {
			return fmt.Errorf("README.md falsely claims managed Skill package interoperability %q", prohibited)
		}
	}
	return nil
}

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
