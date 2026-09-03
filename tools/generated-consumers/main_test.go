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

package generatedconsumers

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const generatedAPIConsumerTest = `package generatedconsumer

import (
	"testing"

	v1 "example.com/powercontext-generated-api/api/v1"
)

func TestGeneratedClientConstruction(t *testing.T) {
	client, err := v1.NewClient("https://127.0.0.1:8080", nil)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		t.Fatal("NewClient returned nil")
	}
}
`

const generatedMCPSchemaConsumerTest = `package mcpapi

import (
	"encoding/json"
	"testing"
)

func TestGeneratedRememberMemorySchema(t *testing.T) {
	schema, ok := generatedToolSchemas["remember_memory"]
	if !ok {
		t.Fatal("remember_memory schema is missing")
	}
	if !json.Valid(schema.Input) || !json.Valid(schema.Output) {
		t.Fatal("remember_memory schemas must contain valid JSON")
	}
}
`

const generatedTraceabilityConsumerTest = `package generatedconsumer

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"slices"
	"testing"
)

type traceabilityTable struct {
	SchemaVersion               int                 ` + "`json:\"schema_version\"`" + `
	OracleCommit                string              ` + "`json:\"oracle_commit\"`" + `
	PythonTestFileCount         int                 ` + "`json:\"python_test_file_count\"`" + `
	PythonTestCaseCount         int                 ` + "`json:\"python_test_case_count\"`" + `
	CaseSpecificEvidenceCount   int                 ` + "`json:\"case_specific_evidence_count\"`" + `
	FileSupportingEvidenceCount int                 ` + "`json:\"file_supporting_evidence_count\"`" + `
	CaseSpecificEvidenceMinimum int                 ` + "`json:\"case_specific_evidence_minimum\"`" + `
	Entries                     []traceabilityEntry ` + "`json:\"entries\"`" + `
}

type traceabilityEntry struct {
	Python        pythonIdentity         ` + "`json:\"python\"`" + `
	Mode          string                 ` + "`json:\"mode\"`" + `
	EvidenceLevel string                 ` + "`json:\"evidence_level\"`" + `
	Evidence      []traceabilityEvidence ` + "`json:\"evidence\"`" + `
}

type pythonIdentity struct {
	File string ` + "`json:\"file\"`" + `
	Name string ` + "`json:\"name\"`" + `
	Line int    ` + "`json:\"line\"`" + `
}

type traceabilityEvidence struct {
	Kind string ` + "`json:\"kind\"`" + `
	File string ` + "`json:\"file\"`" + `
	Test string ` + "`json:\"test\"`" + `
}

func TestGeneratedTraceabilityTable(t *testing.T) {
	payload, readErr := os.ReadFile("traceability.json")
	if readErr != nil {
		t.Fatal(readErr)
	}
	table, decodeErr := decodeTraceabilityTable(payload)
	if decodeErr != nil {
		t.Fatalf("decode traceability table: %v", decodeErr)
	}
	if validateErr := validateTraceabilityTable(table); validateErr != nil {
		t.Fatal(validateErr)
	}
}

func TestTraceabilityEntryValidationRejectsIncompleteValues(t *testing.T) {
	valid := traceabilityEntry{
		Python:        pythonIdentity{File: "tests/test_example.py", Name: "test_example", Line: 10},
		Mode:          "go-port",
		EvidenceLevel: "case-specific",
		Evidence:      []traceabilityEvidence{{Kind: "go", File: "example_test.go", Test: "TestExample"}},
	}
	tests := []struct {
		name   string
		mutate func(*traceabilityEntry)
	}{
		{name: "missing Python identity", mutate: func(entry *traceabilityEntry) { entry.Python.Name = "" }},
		{name: "invalid mode", mutate: func(entry *traceabilityEntry) { entry.Mode = "unknown" }},
		{name: "invalid evidence level", mutate: func(entry *traceabilityEntry) { entry.EvidenceLevel = "file-supporting" }},
		{name: "missing evidence", mutate: func(entry *traceabilityEntry) { entry.Evidence = nil }},
		{name: "incomplete evidence", mutate: func(entry *traceabilityEntry) { entry.Evidence[0].File = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := valid
			entry.Evidence = slices.Clone(valid.Evidence)
			test.mutate(&entry)
			if validateErr := validateTraceabilityEntry(entry); validateErr == nil {
				t.Fatal("validation unexpectedly succeeded")
			}
		})
	}
}

func decodeTraceabilityTable(payload []byte) (traceabilityTable, error) {
	var table traceabilityTable
	if decodeErr := json.Unmarshal(payload, &table, json.RejectUnknownMembers(true)); decodeErr != nil {
		return traceabilityTable{}, decodeErr
	}
	return table, nil
}

func validateTraceabilityTable(table traceabilityTable) error {
	if table.SchemaVersion != 2 {
		return fmt.Errorf("schema version = %d, want 2", table.SchemaVersion)
	}
	if table.OracleCommit != "3a6cb0151670eaff7dc0293466edd673124e80da" {
		return fmt.Errorf("oracle commit = %q", table.OracleCommit)
	}
	if table.PythonTestFileCount != 109 || table.PythonTestCaseCount != 622 ||
		table.CaseSpecificEvidenceCount != 622 || table.FileSupportingEvidenceCount != 0 ||
		table.CaseSpecificEvidenceMinimum != 622 {
		return fmt.Errorf("traceability summary is inconsistent")
	}
	if len(table.Entries) != table.PythonTestCaseCount {
		return fmt.Errorf("traceability entries = %d, want %d", len(table.Entries), table.PythonTestCaseCount)
	}
	for index, entry := range table.Entries {
		if entryErr := validateTraceabilityEntry(entry); entryErr != nil {
			return fmt.Errorf("entry %d: %w", index, entryErr)
		}
	}
	return nil
}

func validateTraceabilityEntry(entry traceabilityEntry) error {
	if entry.Python.File == "" || entry.Python.Name == "" || entry.Python.Line <= 0 {
		return fmt.Errorf("Python identity is incomplete")
	}
	if !slices.Contains([]string{"cross-layer", "go-port", "retained-host"}, entry.Mode) {
		return fmt.Errorf("mode = %q", entry.Mode)
	}
	if entry.EvidenceLevel != "case-specific" {
		return fmt.Errorf("evidence level = %q", entry.EvidenceLevel)
	}
	if len(entry.Evidence) == 0 {
		return fmt.Errorf("evidence is empty")
	}
	for index, evidence := range entry.Evidence {
		if !slices.Contains([]string{"go", "py", "ts"}, evidence.Kind) {
			return fmt.Errorf("evidence %d kind = %q", index, evidence.Kind)
		}
		if evidence.File == "" || evidence.Test == "" {
			return fmt.Errorf("evidence %d is incomplete", index)
		}
	}
	return nil
}
`

type generatorInventory struct {
	SchemaVersion int              `json:"schema_version"`
	Generators    []generatorEntry `json:"generators"`
}

type generatorEntry struct {
	Name     string   `json:"name"`
	Command  string   `json:"command"`
	Inputs   []string `json:"inputs"`
	Outputs  []string `json:"outputs"`
	Evidence []string `json:"evidence"`
}

func TestGeneratorInventoryListsCheckedContracts(t *testing.T) {
	repository := repositoryRoot(t)
	payload, err := os.ReadFile(filepath.Join(repository, "test", "generator-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory generatorInventory
	if err := json.Unmarshal(payload, &inventory, json.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	if inventory.SchemaVersion != 1 || len(inventory.Generators) == 0 {
		t.Fatalf("generator inventory = %#v", inventory)
	}
	seen := map[string]bool{}
	for _, generator := range inventory.Generators {
		if generator.Name == "" || generator.Command == "" || seen[generator.Name] || len(generator.Inputs) == 0 || len(generator.Outputs) == 0 || len(generator.Evidence) == 0 {
			t.Fatalf("generator entry = %#v", generator)
		}
		seen[generator.Name] = true
		for _, path := range append(slices.Clone(generator.Inputs), generator.Outputs...) {
			if _, err := os.Stat(filepath.Join(repository, filepath.FromSlash(path))); err != nil {
				t.Fatalf("%s declared path %q: %v", generator.Name, path, err)
			}
		}
		for _, evidence := range generator.Evidence {
			path, needle, ok := strings.Cut(evidence, "#")
			if !ok || path == "" || needle == "" {
				t.Fatalf("%s evidence %q is invalid", generator.Name, evidence)
			}
			contents, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(path)))
			if err != nil || !bytes.Contains(contents, []byte(needle)) {
				t.Fatalf("%s evidence %q is not a real test: %v", generator.Name, evidence, err)
			}
		}
	}
}

func TestGeneratorInventoryListsEveryOpenAPIGeneratorOutput(t *testing.T) {
	repository := repositoryRoot(t)
	payload, err := os.ReadFile(filepath.Join(repository, "test", "generator-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory generatorInventory
	if err := json.Unmarshal(payload, &inventory, json.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	if err := validateOpenAPIGeneratorOutputs(inventory, openAPIGeneratorOutputs(t, repository)); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAPIInventoryRejectsMissingGeneratedOutput(t *testing.T) {
	repository := repositoryRoot(t)
	payload, err := os.ReadFile(filepath.Join(repository, "test", "generator-inventory.json"))
	if err != nil {
		t.Fatal(err)
	}
	var inventory generatorInventory
	if err := json.Unmarshal(payload, &inventory, json.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	mutated := generatorInventory{SchemaVersion: inventory.SchemaVersion, Generators: slices.Clone(inventory.Generators)}
	for index := range mutated.Generators {
		if mutated.Generators[index].Name != "openapi-go-client" {
			continue
		}
		outputs := slices.Clone(mutated.Generators[index].Outputs)
		for outputIndex, output := range outputs {
			if output == "api/v1/time.go" {
				mutated.Generators[index].Outputs = slices.Delete(outputs, outputIndex, outputIndex+1)
				if err := validateOpenAPIGeneratorOutputs(mutated, openAPIGeneratorOutputs(t, repository)); err == nil {
					t.Fatal("OpenAPI generator inventory accepted missing time support output")
				}
				return
			}
		}
		t.Fatal("OpenAPI generator inventory has no time support output")
	}
	t.Fatal("generator inventory has no openapi-go-client entry")
}

func openAPIGeneratorOutputs(t *testing.T, repository string) []string {
	t.Helper()
	outputs := []string{"client/invoker_gen.go"}
	for _, path := range generatedTreeFiles(t, filepath.Join(repository, "api", "v1")) {
		outputs = append(outputs, filepath.ToSlash(filepath.Join("api", "v1", path)))
	}
	slices.Sort(outputs)
	return outputs
}

func validateOpenAPIGeneratorOutputs(inventory generatorInventory, want []string) error {
	for _, generator := range inventory.Generators {
		if generator.Name != "openapi-go-client" {
			continue
		}
		got := slices.Clone(generator.Outputs)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			return fmt.Errorf("OpenAPI generator outputs = %v, want %v", got, want)
		}
		return nil
	}
	return errors.New("generator inventory has no openapi-go-client entry")
}

func TestRetainedAdapterOperationMirrorsMatchDSHGeneratorOutput(t *testing.T) {
	repository := repositoryRoot(t)
	paths := []string{
		"integrations/dsh/plugins/powercontext/src/operations.generated.ts",
		"integrations/pi/plugins/powercontext/src/operations.generated.ts",
		"integrations/opencode/plugins/powercontext/src/operations.generated.ts",
	}
	if err := validateOperationMirrors(repository, paths); err != nil {
		t.Fatal(err)
	}
}

func TestOperationMirrorValidationRejectsDrift(t *testing.T) {
	directory := t.TempDir()
	paths := []string{"dsh.ts", "pi.ts", "opencode.ts"}
	for _, path := range paths {
		writeFile(t, filepath.Join(directory, path), "export const OPERATIONS = {}\n")
	}
	writeFile(t, filepath.Join(directory, "opencode.ts"), "export const OPERATIONS = { drift: true }\n")
	if err := validateOperationMirrors(directory, paths); err == nil {
		t.Fatal("operation mirror drift was accepted")
	}
}

func validateOperationMirrors(root string, paths []string) error {
	if len(paths) < 2 {
		return fmt.Errorf("operation mirror count = %d, want at least 2", len(paths))
	}
	reference, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(paths[0])))
	if err != nil {
		return err
	}
	reference = bytes.ReplaceAll(reference, []byte("\r\n"), []byte("\n"))
	for _, path := range paths[1:] {
		payload, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return err
		}
		if !bytes.Equal(reference, bytes.ReplaceAll(payload, []byte("\r\n"), []byte("\n"))) {
			return fmt.Errorf("operation mirror %q differs from %q", path, paths[0])
		}
	}
	return nil
}

func TestOpenAPIGeneratorProducesGoldenBuildableConsumer(t *testing.T) {
	repository := repositoryRoot(t)
	consumer := t.TempDir()
	generatedAPI := filepath.Join(consumer, "api", "v1")
	invoker := filepath.Join(t.TempDir(), "invoker_gen.go")

	runGo(t, repository,
		"run", "./tools/api-generate",
		"-spec", filepath.Join(repository, "openapi", "powercontext.yaml"),
		"-target", generatedAPI,
		"-package", "v1",
		"-client-invoker", invoker,
	)

	compareTree(t, filepath.Join(repository, "api", "v1"), generatedAPI)
	compareFile(t, filepath.Join(repository, "client", "invoker_gen.go"), invoker)
	writeFile(t, filepath.Join(consumer, "go.mod"), "module example.com/powercontext-generated-api\n\ngo 1.27.0\n")
	writeFile(t, filepath.Join(consumer, "consumer_test.go"), generatedAPIConsumerTest)

	runGo(t, consumer, "mod", "tidy")
	runGo(t, consumer, "mod", "verify")
	runGo(t, consumer, "build", "./...")
	runGo(t, consumer, "test", "-count=1", "./...")
}

func TestMCPSchemaGeneratorProducesGoldenBuildableConsumer(t *testing.T) {
	repository := repositoryRoot(t)
	consumer := t.TempDir()
	generated := filepath.Join(consumer, "schemas_gen.go")

	runGo(t, repository,
		"run", "./tools/mcp-schema-generate",
		"-spec", filepath.Join(repository, "openapi", "powercontext.yaml"),
		"-output", generated,
	)

	compareFile(t, filepath.Join(repository, "internal", "mcpapi", "schemas_gen.go"), generated)
	writeFile(t, filepath.Join(consumer, "go.mod"), "module example.com/powercontext-generated-mcp\n\ngo 1.27.0\n")
	writeFile(t, filepath.Join(consumer, "schemas_gen_test.go"), generatedMCPSchemaConsumerTest)

	runGo(t, consumer, "mod", "tidy")
	runGo(t, consumer, "mod", "verify")
	runGo(t, consumer, "build", "./...")
	runGo(t, consumer, "test", "-count=1", "./...")
}

func TestTraceabilityGeneratorProducesGoldenConsumableTable(t *testing.T) {
	repository := repositoryRoot(t)
	consumer := t.TempDir()
	generated := filepath.Join(consumer, "traceability.json")

	runGo(t, repository,
		"run", "./tools/traceability-generate",
		"-root", repository,
		"-manifest", filepath.Join(repository, "test", "conformance", "testdata", "python-v0.0.2", "manifest.json"),
		"-rules", filepath.Join(repository, "test", "conformance", "traceability-rules.json"),
		"-output", generated,
	)

	compareFile(t, filepath.Join(repository, "test", "conformance", "traceability.json"), generated)
	prepareTraceabilityConsumer(t, consumer)
	runGo(t, consumer, "build", "./...")
	runGo(t, consumer, "test", "-count=1", "./...")
	runGo(t, consumer, "vet", "./...")
	runConsumerLint(t, repository, consumer)
}

func TestTraceabilityConsumerRejectsSchemaMutants(t *testing.T) {
	repository := repositoryRoot(t)
	payload, readErr := os.ReadFile(filepath.Join(repository, "test", "conformance", "traceability.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	tests := []struct {
		name       string
		mutate     func([]byte) []byte
		wantOutput string
	}{
		{
			name: "unknown top-level field",
			mutate: func(payload []byte) []byte {
				return replaceOnce(t, payload, []byte("{"), []byte(`{"unexpected":true,`))
			},
			wantOutput: "decode traceability table",
		},
		{
			name: "duplicate top-level field",
			mutate: func(payload []byte) []byte {
				return replaceOnce(t, payload, []byte(`"schema_version": 2,`), []byte(`"schema_version": 2, "schema_version": 2,`))
			},
			wantOutput: "decode traceability table",
		},
		{
			name: "unknown entry field",
			mutate: func(payload []byte) []byte {
				return replaceOnce(t, payload, []byte(`"python": {`), []byte(`"unexpected": true, "python": {`))
			},
			wantOutput: "decode traceability table",
		},
		{
			name: "missing evidence field",
			mutate: func(payload []byte) []byte {
				return replaceOnce(t, payload, []byte(`"evidence": [`), []byte(`"missing_evidence": [`))
			},
			wantOutput: "decode traceability table",
		},
		{
			name: "empty evidence object",
			mutate: func(payload []byte) []byte {
				return clearFirstJSONStringValue(t, payload, "kind")
			},
			wantOutput: "evidence 0 kind",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			consumer := t.TempDir()
			mutated := test.mutate(slices.Clone(payload))
			generated := filepath.Join(consumer, "traceability.json")
			writeFile(t, generated, string(mutated))
			prepareTraceabilityConsumer(t, consumer)
			runGoExpectFailure(t, consumer, test.wantOutput, "test", "-count=1", "./...")
		})
	}
}

func prepareTraceabilityConsumer(t *testing.T, consumer string) {
	t.Helper()
	writeFile(t, filepath.Join(consumer, "go.mod"), "module example.com/powercontext-traceability-consumer\n\ngo 1.27.0\n")
	writeFile(t, filepath.Join(consumer, "consumer_test.go"), generatedTraceabilityConsumerTest)
	runGo(t, consumer, "mod", "tidy")
	runGo(t, consumer, "mod", "verify")
}

func replaceOnce(t *testing.T, payload, old, replacement []byte) []byte {
	t.Helper()
	mutated := bytes.Replace(payload, old, replacement, 1)
	if bytes.Equal(mutated, payload) {
		t.Fatalf("traceability payload does not contain %q", old)
	}
	return mutated
}

func clearFirstJSONStringValue(t *testing.T, payload []byte, key string) []byte {
	t.Helper()
	prefix := []byte(`"` + key + `": "`)
	start := bytes.Index(payload, prefix)
	if start < 0 {
		t.Fatalf("traceability payload does not contain string field %q", key)
	}
	valueStart := start + len(prefix)
	valueEnd := bytes.IndexByte(payload[valueStart:], '"')
	if valueEnd < 0 {
		t.Fatalf("traceability field %q has no closing quote", key)
	}
	mutated := slices.Clone(payload[:valueStart])
	return append(mutated, payload[valueStart+valueEnd:]...)
}

func runGoExpectFailure(t *testing.T, directory, wantOutput string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("Go command unexpectedly succeeded:\n%s", output)
	}
	if !bytes.Contains(output, []byte(wantOutput)) {
		t.Fatalf("Go command output is missing %q:\n%s", wantOutput, output)
	}
}

func runConsumerLint(t *testing.T, repository, consumer string) {
	t.Helper()
	linter := os.Getenv("POWERCONTEXT_GOLANGCI_LINT")
	if linter == "" {
		return
	}
	config := filepath.Join(repository, ".golangci.yml")
	for _, arguments := range [][]string{
		{"fmt", "--diff", "--config", config},
		{"run", "--config", config},
	} {
		command := exec.CommandContext(t.Context(), linter, arguments...)
		command.Dir = consumer
		command.Env = append(os.Environ(), "GOWORK=off")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("check generated consumer with %v: %v\n%s", arguments, err, output)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if fileExists(filepath.Join(directory, "go.mod")) && fileExists(filepath.Join(directory, "openapi", "powercontext.yaml")) {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("could not find the repository root")
		}
		directory = parent
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func runGo(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run Go command: %v\n%s", err, output)
	}
}

func compareTree(t *testing.T, expectedRoot, actualRoot string) {
	t.Helper()
	expected := generatedTreeFiles(t, expectedRoot)
	actual := treeFiles(t, actualRoot)
	if !slices.Equal(expected, actual) {
		t.Fatalf("generated file inventory = %v, want %v", actual, expected)
	}
	for _, relative := range expected {
		compareFile(t, filepath.Join(expectedRoot, relative), filepath.Join(actualRoot, relative))
	}
}

func treeFiles(t *testing.T, root string) []string {
	t.Helper()
	var result []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result = append(result, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(result)
	return result
}

func generatedTreeFiles(t *testing.T, root string) []string {
	t.Helper()
	var result []string
	for _, relative := range treeFiles(t, root) {
		contents, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(contents, []byte("Code generated")) {
			result = append(result, relative)
		}
	}
	return result
}

func compareFile(t *testing.T, expectedPath, actualPath string) {
	t.Helper()
	expected, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(actualPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected, actual) {
		t.Fatalf("generated output %q differs from its reviewed golden", filepath.Base(actualPath))
	}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
