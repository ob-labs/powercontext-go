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

// Command mcp-schema-generate derives the curated MCP tool schemas from the
// authoritative OpenAPI document. The generated file is checked in so runtime
// binaries do not parse YAML and schema drift remains reviewable.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"sort"
	"strings"

	"github.com/ghodss/yaml"
)

var selectedOperations = map[string]bool{
	"acknowledge_handoff":              true,
	"activate_handoff":                 true,
	"approve_artifact_candidate":       true,
	"capture_content_source":           true,
	"commit_handoff":                   true,
	"continue_handoff":                 true,
	"create_work_contract":             true,
	"finalize_handoff":                 true,
	"get_artifact_candidate":           true,
	"get_memory_entry":                 true,
	"handoff_current_work":             true,
	"list_artifact_candidates":         true,
	"list_memory_entries":              true,
	"list_handoff_report_known_scopes": true,
	"record_task_outcome":              true,
	"reject_artifact_candidate":        true,
	"remember_memory":                  true,
	"retire_memory_entry":              true,
	"revise_artifact_candidate":        true,
	"revise_memory_entry":              true,
	"search_memory":                    true,
	"get_handoff_report":               true,
	"get_handoff_report_workspace":     true,
}

type definition struct {
	name, description string
	input, output     []byte
	report            bool
}

func main() {
	specPath := flag.String("spec", "openapi/powercontext.yaml", "OpenAPI input")
	outputPath := flag.String("output", "internal/mcpapi/schemas_gen.go", "generated Go output")
	flag.Parse()
	if err := run(*specPath, *outputPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(specPath, outputPath string) error {
	yamlBytes, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	jsonBytes, err := yaml.YAMLToJSON(yamlBytes)
	if err != nil {
		return err
	}
	var root map[string]any
	if unmarshalErr := json.Unmarshal(jsonBytes, &root); unmarshalErr != nil {
		return unmarshalErr
	}
	definitions, err := collect(root)
	if err != nil {
		return err
	}
	generated, err := render(definitions)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, generated, 0o644)
}

func collect(root map[string]any) ([]definition, error) {
	paths, ok := root["paths"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("OpenAPI paths are missing")
	}
	var result []definition
	for _, rawPath := range paths {
		path, _ := rawPath.(map[string]any)
		for _, method := range []string{"get", "post", "put", "patch", "delete"} {
			operation, _ := path[method].(map[string]any)
			name, _ := operation["operationId"].(string)
			if !selectedOperations[name] {
				continue
			}
			input, err := requestSchema(root, operation)
			if err != nil {
				return nil, fmt.Errorf("%s input: %w", name, err)
			}
			output, err := responseSchema(root, operation)
			if err != nil {
				return nil, fmt.Errorf("%s output: %w", name, err)
			}
			description, _ := operation["description"].(string)
			if description == "" {
				description, _ = operation["summary"].(string)
			}
			result = append(result, definition{
				name: name, description: strings.TrimSpace(description), input: input, output: output,
				report: strings.HasPrefix(name, "get_handoff_report") || name == "list_handoff_report_known_scopes",
			})
		}
	}
	if len(result) != len(selectedOperations) {
		return nil, fmt.Errorf("found %d curated operations, want %d", len(result), len(selectedOperations))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].name < result[j].name })
	return result, nil
}

func requestSchema(root map[string]any, operation map[string]any) ([]byte, error) {
	body, _ := operation["requestBody"].(map[string]any)
	content, _ := body["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, ok := media["schema"]
	if !ok {
		return nil, fmt.Errorf("application/json request schema is missing")
	}
	return encodeResolved(root, schema)
}

func responseSchema(root map[string]any, operation map[string]any) ([]byte, error) {
	responses, _ := operation["responses"].(map[string]any)
	var status string
	for candidate := range responses {
		if strings.HasPrefix(candidate, "2") && (status == "" || candidate < status) {
			status = candidate
		}
	}
	response, _ := responses[status].(map[string]any)
	content, _ := response["content"].(map[string]any)
	media, _ := content["application/json"].(map[string]any)
	schema, ok := media["schema"]
	if !ok {
		return nil, fmt.Errorf("application/json success schema is missing")
	}
	return encodeResolved(root, schema)
}

func encodeResolved(root map[string]any, schema any) ([]byte, error) {
	resolved, err := resolve(root, schema, 0)
	if err != nil {
		return nil, err
	}
	return json.Marshal(resolved)
}

func resolve(root map[string]any, value any, depth int) (any, error) {
	if depth > 128 {
		return nil, fmt.Errorf("schema reference depth exceeds 128")
	}
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			resolved, err := resolve(root, item, depth+1)
			if err != nil {
				return nil, err
			}
			result[i] = resolved
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(value))
		if reference, ok := value["$ref"].(string); ok {
			target, err := localReference(root, reference)
			if err != nil {
				return nil, err
			}
			resolved, err := resolve(root, target, depth+1)
			if err != nil {
				return nil, err
			}
			for key, item := range resolved.(map[string]any) {
				result[key] = item
			}
		}
		for key, item := range value {
			if key == "$ref" || key == "nullable" || key == "discriminator" || key == "example" {
				continue
			}
			resolved, err := resolve(root, item, depth+1)
			if err != nil {
				return nil, err
			}
			result[key] = resolved
		}
		if nullable, _ := value["nullable"].(bool); nullable {
			switch kind := result["type"].(type) {
			case string:
				result["type"] = []any{kind, "null"}
			case []any:
				result["type"] = append(kind, "null")
			default:
				copy := make(map[string]any, len(result))
				for key, item := range result {
					copy[key] = item
				}
				result = map[string]any{"anyOf": []any{copy, map[string]any{"type": "null"}}}
			}
		}
		return result, nil
	default:
		return value, nil
	}
}

func localReference(root map[string]any, reference string) (any, error) {
	if !strings.HasPrefix(reference, "#/") {
		return nil, fmt.Errorf("external schema reference %q is not supported", reference)
	}
	var current any = root
	for _, component := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		component = strings.ReplaceAll(strings.ReplaceAll(component, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("schema reference %q does not resolve to an object", reference)
		}
		current, ok = object[component]
		if !ok {
			return nil, fmt.Errorf("schema reference %q is missing", reference)
		}
	}
	return current, nil
}

func render(definitions []definition) ([]byte, error) {
	var output bytes.Buffer
	output.WriteString("// Code generated by mcp-schema-generate; DO NOT EDIT.\n\npackage mcpapi\n\nimport \"encoding/json\"\n\n")
	output.WriteString("type toolSchemaDefinition struct {\n\tDescription string\n\tInput json.RawMessage\n\tOutput json.RawMessage\n\tHandoffReport bool\n}\n\n")
	output.WriteString("var generatedToolSchemas = map[string]toolSchemaDefinition{\n")
	for _, item := range definitions {
		fmt.Fprintf(&output, "\t%q: {Description: %q, Input: json.RawMessage(%q), Output: json.RawMessage(%q), HandoffReport: %t},\n", item.name, item.description, item.input, item.output, item.report)
	}
	output.WriteString("}\n")
	return format.Source(output.Bytes())
}
