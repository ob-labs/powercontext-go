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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeNullableReferencesPreservesCanonicalOrder(t *testing.T) {
	t.Parallel()
	input := []byte("properties:\n" +
		"  before:\n" +
		"    type: string\n" +
		"  value:\n" +
		"    $ref: \"#/components/schemas/Value\"\n" +
		"    nullable: true\n" +
		"  primitive:\n" +
		"    type: string\n" +
		"    nullable: true\n" +
		"  after:\n" +
		"    type: integer\n")
	got, err := normalizeNullableReferences(input)
	if err != nil {
		t.Fatal(err)
	}
	want := "properties:\n" +
		"  before:\n" +
		"    type: string\n" +
		"  value:\n" +
		"    oneOf:\n" +
		"      - $ref: \"#/components/schemas/Value\"\n" +
		"      - type: \"null\"\n" +
		"  primitive:\n" +
		"    type: string\n" +
		"    nullable: true\n" +
		"  after:\n" +
		"    type: integer\n"
	if string(got) != want {
		t.Fatalf("normalized OpenAPI:\n%s\nwant:\n%s", got, want)
	}
	if strings.Index(string(got), "before:") > strings.Index(string(got), "after:") {
		t.Fatal("schema property order changed")
	}
}

func TestNormalizeNullableReferencesRequiresWork(t *testing.T) {
	t.Parallel()
	if _, err := normalizeNullableReferences([]byte("openapi: 3.0.3\n")); err == nil {
		t.Fatal("expected missing nullable reference error")
	}
}

func TestRewriteDateTimeEncodersIsDeterministicAndRequiresGeneratedCalls(t *testing.T) {
	t.Parallel()
	target := t.TempDir()
	path := filepath.Join(target, "oas_json_gen.go")
	contents := "package v1\nfunc encode() { json.EncodeDateTime(e, first); json.EncodeDateTime(e, second) }\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewriteDateTimeEncoders(target); err != nil {
		t.Fatal(err)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "package v1\nfunc encode() { encodeDateTime(e, first); encodeDateTime(e, second) }\n"
	if string(rewritten) != want {
		t.Fatalf("rewritten generated source = %q, want %q", rewritten, want)
	}
	if err := rewriteDateTimeEncoders(target); err == nil {
		t.Fatal("second rewrite unexpectedly accepted output without generated calls")
	}
}

func TestGenerateClientInvokerCoversEveryOpenAPIOperation(t *testing.T) {
	t.Parallel()
	specification := []byte(`openapi: 3.0.3
paths:
  /health/live:
    get:
      operationId: get_liveness
      responses:
        "200": {description: ok}
  /v1/sources/content:
    post:
      operationId: capture_content_source
      responses:
        "202": {description: accepted}
        "422": {description: invalid}
  /v1/stats:
    get:
      operationId: get_stats
      responses:
        "200": {description: ok}
`)
	generatedClient := `package v1

import "context"

type Invoker interface {
	GetLiveness(ctx context.Context) (*HealthResponseHeaders, error)
	CaptureContentSource(ctx context.Context, request *CaptureContentSourceRequest) (CaptureContentSourceRes, error)
	GetStats(ctx context.Context, params GetStatsParams) (GetStatsRes, error)
}
`
	directory := t.TempDir()
	clientPath := filepath.Join(directory, "oas_client_gen.go")
	if err := os.WriteFile(clientPath, []byte(generatedClient), 0o600); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(directory, "invoker_gen.go")
	if err := generateClientInvoker(specification, clientPath, outputPath); err != nil {
		t.Fatal(err)
	}
	generated, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, expected := range []string{
		"var _ v1.Invoker = normalizedInvoker{}",
		"func (i normalizedInvoker) CaptureContentSource",
		`operationDescriptor{path: "/v1/sources/content", successStatus: 202}`,
		`validateOperationRequest("/v1/sources/content", request)`,
		"request *v1.CaptureContentSourceRequest",
		"func (i normalizedInvoker) GetLiveness",
		"func (i normalizedInvoker) GetStats",
		"params v1.GetStatsParams",
		`validateOperationRequest("/v1/stats", &params)`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("generated Client Invoker does not contain %q:\n%s", expected, text)
		}
	}

	missing := []byte(strings.Replace(string(specification), "  /v1/stats:", "  /v1/untracked:\n    get:\n      operationId: untracked\n      responses:\n        \"200\": {description: ok}\n  /v1/stats:", 1))
	if err := generateClientInvoker(missing, clientPath, outputPath); err == nil {
		t.Fatal("OpenAPI operation missing from Invoker was accepted")
	}
}

func TestGenerateContractValidationDerivesCombinedEvidenceModels(t *testing.T) {
	t.Parallel()
	specification := []byte(`openapi: 3.0.3
components:
  schemas:
    Candidate:
      type: object
      properties:
        source_refs: {type: array, maxItems: 32}
        artifact_refs: {type: array, maxItems: 32}
    UnboundedLineage:
      type: object
      properties:
        source_refs: {type: array}
        artifact_refs: {type: array}
`)
	models, err := candidateEvidenceModels(specification)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0] != "Candidate" {
		t.Fatalf("combined evidence models = %#v, want [Candidate]", models)
	}

	outputPath := filepath.Join(t.TempDir(), "powercontext_contract_validation_gen.go")
	if generateErr := generateContractValidation(specification, outputPath); generateErr != nil {
		t.Fatal(generateErr)
	}
	generated, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, expected := range []string{
		"case Candidate:",
		"case *Candidate:",
		"source_refs and artifact_refs together must not exceed 32 references",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("generated contract validation does not contain %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "UnboundedLineage") {
		t.Fatalf("generated contract validation unexpectedly contains unbounded schema:\n%s", text)
	}
}
