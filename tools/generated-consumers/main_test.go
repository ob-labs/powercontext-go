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
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	runGo(t, consumer, "test", "-count=1", "./...")
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
