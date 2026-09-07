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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghodss/yaml"
)

const (
	scopeSidecarSchemaVersion = 1
	upstreamScopeRepository   = "oceanbase/powercontext"
	upstreamScopeCommit       = "74b961fbb07165595314726715d412a3d0d90589"
	upstreamScopePath         = "openapi/powercontext.yaml"
)

var scopeSidecarOperations = []scopeSidecarOperation{
	{OperationID: "list_scopes", Method: "get", Path: "/v1/scopes"},
	{OperationID: "get_scope", Method: "get", Path: "/v1/scopes/{scope_id}"},
	{OperationID: "get_default_scope", Method: "get", Path: "/v1/scopes/default"},
	{OperationID: "resolve_scope_selection", Method: "post", Path: "/v1/scopes/selection/resolve"},
	{OperationID: "resolve_scope_binding", Method: "post", Path: "/v1/scope-bindings/resolve"},
}

type scopeSidecarManifest struct {
	SchemaVersion int                         `json:"schema_version"`
	Upstream      scopeSidecarUpstream        `json:"upstream"`
	Operations    []scopeSidecarOperation     `json:"operations"`
	Overlays      scopeSidecarManifestOverlay `json:"overlays"`
}

type scopeSidecarUpstream struct {
	Repository string `json:"repository"`
	Commit     string `json:"commit"`
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
}

type scopeSidecarOperation struct {
	OperationID string `json:"operation_id"`
	Method      string `json:"method"`
	Path        string `json:"path"`
}

type scopeSidecarManifestOverlay struct {
	ScopeBindingKey scopeSidecarBindingKeyOverlay `json:"scope_binding_key"`
}

type scopeSidecarBindingKeyOverlay struct {
	IntegrationEnum []string `json:"integration_enum"`
}

func loadScopeSidecarManifest(path string) (scopeSidecarManifest, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return scopeSidecarManifest{}, err
	}
	var manifest scopeSidecarManifest
	if err := json.Unmarshal(contents, &manifest, json.RejectUnknownMembers(true)); err != nil {
		return scopeSidecarManifest{}, fmt.Errorf("decode Scope sidecar manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return scopeSidecarManifest{}, err
	}
	return manifest, nil
}

func (manifest scopeSidecarManifest) validate() error {
	if manifest.SchemaVersion != scopeSidecarSchemaVersion {
		return fmt.Errorf("Scope sidecar manifest schema_version = %d, want %d", manifest.SchemaVersion, scopeSidecarSchemaVersion)
	}
	if manifest.Upstream.Repository != upstreamScopeRepository ||
		manifest.Upstream.Commit != upstreamScopeCommit ||
		manifest.Upstream.Path != upstreamScopePath {
		return fmt.Errorf("unexpected Scope sidecar upstream %q at %q:%q", manifest.Upstream.Repository, manifest.Upstream.Commit, manifest.Upstream.Path)
	}
	if len(manifest.Upstream.SHA256) != sha256.Size*2 {
		return errors.New("Scope sidecar upstream SHA-256 is not a complete digest")
	}
	if _, err := hex.DecodeString(manifest.Upstream.SHA256); err != nil {
		return fmt.Errorf("decode Scope sidecar upstream SHA-256: %w", err)
	}
	if len(manifest.Operations) != len(scopeSidecarOperations) {
		return fmt.Errorf("Scope sidecar operation count = %d, want %d", len(manifest.Operations), len(scopeSidecarOperations))
	}
	for index, want := range scopeSidecarOperations {
		got := manifest.Operations[index]
		if got != want {
			return fmt.Errorf("Scope sidecar operation %d = %#v, want %#v", index, got, want)
		}
	}
	if got := manifest.Overlays.ScopeBindingKey.IntegrationEnum; len(got) != 2 || got[0] != "codex" || got[1] != "workbuddy" {
		return fmt.Errorf("ScopeBindingKey integration overlay = %#v, want [codex workbuddy]", got)
	}
	return nil
}

func projectScopeSidecar(
	source []byte,
	manifest scopeSidecarManifest,
	legacySpecification []byte,
	compatibilityContents []byte,
) ([]byte, error) {
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	if scopeSidecarSHA256(source) != manifest.Upstream.SHA256 {
		return nil, errors.New("pinned Scope sidecar upstream OpenAPI digest differs")
	}
	if err := validatePinnedScopeSidecarSource(source, legacySpecification, compatibilityContents); err != nil {
		return nil, err
	}
	document, err := decodeScopeSidecarDocument(source)
	if err != nil {
		return nil, err
	}
	paths, err := scopeSidecarObject(document, "paths")
	if err != nil {
		return nil, err
	}
	projectedPaths, err := projectScopeSidecarPaths(paths, manifest.Operations)
	if err != nil {
		return nil, err
	}
	projected := map[string]any{
		"openapi": document["openapi"],
		"info":    document["info"],
		"paths":   projectedPaths,
	}
	if security, found := document["security"]; found {
		projected["security"] = security
	}
	components, err := projectScopeSidecarComponents(document, projectedPaths)
	if err != nil {
		return nil, err
	}
	if overlayErr := applyScopeBindingKeyOverlay(components, manifest.Overlays.ScopeBindingKey); overlayErr != nil {
		return nil, overlayErr
	}
	projected["components"] = components
	encoded, err := json.Marshal(projected, json.Deterministic(true))
	if err != nil {
		return nil, fmt.Errorf("encode Scope sidecar OpenAPI: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := validateProjectedScopeSidecar(encoded, manifest.Operations); err != nil {
		return nil, err
	}
	return encoded, nil
}

func validatePinnedScopeSidecarSource(source, legacySpecification, compatibilityContents []byte) error {
	surface, err := decodeCompatibilitySurface(compatibilityContents)
	if err != nil {
		return err
	}
	legacy, err := parseOpenAPIOperations(legacySpecification)
	if err != nil {
		return fmt.Errorf("parse legacy OpenAPI: %w", err)
	}
	if validateErr := surface.validate(legacy); validateErr != nil {
		return fmt.Errorf("validate compatibility surface: %w", validateErr)
	}
	want, err := surface.projectCanonical(legacy)
	if err != nil {
		return fmt.Errorf("project canonical operation ledger: %w", err)
	}
	got, err := parseOpenAPIOperations(source)
	if err != nil {
		return fmt.Errorf("parse pinned upstream OpenAPI: %w", err)
	}
	if len(got) != len(want) {
		return fmt.Errorf("pinned upstream operation count = %d, want %d", len(got), len(want))
	}
	for operationID, endpoint := range want {
		if got[operationID] != endpoint {
			return fmt.Errorf("pinned upstream operation %q = %#v, want %#v", operationID, got[operationID], endpoint)
		}
	}
	for operationID := range got {
		if _, found := want[operationID]; !found {
			return fmt.Errorf("pinned upstream OpenAPI contains untracked operation %q", operationID)
		}
	}
	return nil
}

func decodeScopeSidecarDocument(source []byte) (map[string]any, error) {
	encoded, err := yaml.YAMLToJSON(source)
	if err != nil {
		return nil, fmt.Errorf("convert pinned upstream OpenAPI YAML: %w", err)
	}
	var document map[string]any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, fmt.Errorf("decode pinned upstream OpenAPI JSON: %w", err)
	}
	if _, err := scopeSidecarObject(document, "paths"); err != nil {
		return nil, err
	}
	if _, err := scopeSidecarObject(document, "components"); err != nil {
		return nil, err
	}
	return document, nil
}

func scopeSidecarObject(value map[string]any, key string) (map[string]any, error) {
	object, found := value[key].(map[string]any)
	if !found {
		return nil, fmt.Errorf("Scope sidecar OpenAPI %q is not an object", key)
	}
	return object, nil
}

func projectScopeSidecarPaths(
	paths map[string]any,
	operations []scopeSidecarOperation,
) (map[string]any, error) {
	projected := make(map[string]any, len(operations))
	for _, expected := range operations {
		pathItem, found := paths[expected.Path].(map[string]any)
		if !found {
			return nil, fmt.Errorf("pinned upstream Scope path %q is missing", expected.Path)
		}
		operation, found := pathItem[expected.Method]
		if !found {
			return nil, fmt.Errorf("pinned upstream Scope operation %s %s is missing", expected.Method, expected.Path)
		}
		copied, err := cloneScopeSidecarValue(operation)
		if err != nil {
			return nil, err
		}
		projectedPath, found := projected[expected.Path].(map[string]any)
		if !found {
			projectedPath = make(map[string]any, 1)
			projected[expected.Path] = projectedPath
		}
		projectedPath[expected.Method] = copied
	}
	return projected, nil
}

func projectScopeSidecarComponents(document, paths map[string]any) (map[string]any, error) {
	components, err := scopeSidecarObject(document, "components")
	if err != nil {
		return nil, err
	}
	references := make(map[string]map[string]struct{})
	if err := collectScopeSidecarReferences(paths, references); err != nil {
		return nil, err
	}
	for name := range scopeSidecarSecuritySchemes(document["security"]) {
		addScopeSidecarReference(references, "securitySchemes", name)
	}
	projected := make(map[string]any)
	copied := make(map[string]map[string]struct{})
	for {
		category, name, found := nextScopeSidecarReference(references, copied)
		if !found {
			break
		}
		sourceCategory, categoryFound := components[category].(map[string]any)
		if !categoryFound {
			return nil, fmt.Errorf("pinned upstream components.%s is not an object", category)
		}
		value, valueFound := sourceCategory[name]
		if !valueFound {
			return nil, fmt.Errorf("pinned upstream component %s/%s is missing", category, name)
		}
		cloned, cloneErr := cloneScopeSidecarValue(value)
		if cloneErr != nil {
			return nil, cloneErr
		}
		projectedCategory, categoryFound := projected[category].(map[string]any)
		if !categoryFound {
			projectedCategory = make(map[string]any)
			projected[category] = projectedCategory
		}
		projectedCategory[name] = cloned
		addScopeSidecarReference(copied, category, name)
		if collectErr := collectScopeSidecarReferences(cloned, references); collectErr != nil {
			return nil, collectErr
		}
	}
	return projected, nil
}

func collectScopeSidecarReferences(value any, references map[string]map[string]struct{}) error {
	switch value := value.(type) {
	case map[string]any:
		if reference, found := value["$ref"].(string); found {
			if err := addScopeSidecarComponentReference(references, reference); err != nil {
				return err
			}
		}
		for _, child := range value {
			if err := collectScopeSidecarReferences(child, references); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := collectScopeSidecarReferences(child, references); err != nil {
				return err
			}
		}
	}
	return nil
}

func addScopeSidecarComponentReference(references map[string]map[string]struct{}, reference string) error {
	if !strings.HasPrefix(reference, "#") {
		return fmt.Errorf("Scope sidecar does not permit external reference %q", reference)
	}
	parts := strings.Split(reference, "/")
	if len(parts) != 4 || parts[0] != "#" || parts[1] != "components" || parts[2] == "" || parts[3] == "" {
		return fmt.Errorf("Scope sidecar reference %q is not a component reference", reference)
	}
	addScopeSidecarReference(references, parts[2], parts[3])
	return nil
}

func addScopeSidecarReference(references map[string]map[string]struct{}, category, name string) {
	entries := references[category]
	if entries == nil {
		entries = make(map[string]struct{})
		references[category] = entries
	}
	entries[name] = struct{}{}
}

func nextScopeSidecarReference(
	references, copied map[string]map[string]struct{},
) (category, name string, found bool) {
	for category, entries := range references {
		for name := range entries {
			if _, copiedAlready := copied[category][name]; !copiedAlready {
				return category, name, true
			}
		}
	}
	return "", "", false
}

func scopeSidecarSecuritySchemes(value any) map[string]struct{} {
	result := make(map[string]struct{})
	security, ok := value.([]any)
	if !ok {
		return result
	}
	for _, requirement := range security {
		object, objectOK := requirement.(map[string]any)
		if !objectOK {
			continue
		}
		for name := range object {
			result[name] = struct{}{}
		}
	}
	return result
}

func applyScopeBindingKeyOverlay(
	components map[string]any,
	overlay scopeSidecarBindingKeyOverlay,
) error {
	schemas, found := components["schemas"].(map[string]any)
	if !found {
		return errors.New("Scope sidecar lacks the ScopeBindingKey schema")
	}
	bindingKey, found := schemas["ScopeBindingKey"].(map[string]any)
	if !found {
		return errors.New("Scope sidecar lacks the ScopeBindingKey component")
	}
	properties, found := bindingKey["properties"].(map[string]any)
	if !found {
		return errors.New("ScopeBindingKey properties are not an object")
	}
	integration, found := properties["integration"].(map[string]any)
	if !found {
		return errors.New("ScopeBindingKey integration is not an object")
	}
	enum := make([]any, len(overlay.IntegrationEnum))
	for index, value := range overlay.IntegrationEnum {
		enum[index] = value
	}
	integration["enum"] = enum
	return nil
}

func validateProjectedScopeSidecar(
	projected []byte,
	operations []scopeSidecarOperation,
) error {
	got, err := parseOpenAPIOperations(projected)
	if err != nil {
		return fmt.Errorf("parse projected Scope sidecar: %w", err)
	}
	if len(got) != len(operations) {
		return fmt.Errorf("projected Scope sidecar operation count = %d, want %d", len(got), len(operations))
	}
	for _, expected := range operations {
		if got[expected.OperationID] != (compatibilityEndpoint{Method: expected.Method, Path: expected.Path}) {
			return fmt.Errorf("projected Scope sidecar operation %q = %#v, want %s %s", expected.OperationID, got[expected.OperationID], expected.Method, expected.Path)
		}
	}
	return nil
}

func runScopeSidecar(
	sourcePath, manifestPath, target, packageName, clientInvoker, compatibilityPath, legacyPath string,
) error {
	if packageName != "scopes" || filepath.Base(filepath.Clean(target)) != "scopes" ||
		filepath.Base(filepath.Dir(filepath.Clean(target))) != "canonical" {
		return errors.New("Scope sidecar generation must target api/canonical/scopes package scopes")
	}
	if clientInvoker != "" {
		return errors.New("Scope sidecar generation cannot write a Client Invoker")
	}
	if compatibilityPath == "" || legacyPath == "" {
		return errors.New("Scope sidecar generation requires compatibility and legacy-spec")
	}
	manifest, err := loadScopeSidecarManifest(manifestPath)
	if err != nil {
		return err
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	legacy, err := os.ReadFile(legacyPath)
	if err != nil {
		return err
	}
	compatibility, err := os.ReadFile(compatibilityPath)
	if err != nil {
		return err
	}
	projected, err := projectScopeSidecar(source, manifest, legacy, compatibility)
	if err != nil {
		return err
	}
	if writeErr := writeScopeSidecarDocument(filepath.Join(filepath.Dir(manifestPath), "scopes.json"), projected); writeErr != nil {
		return writeErr
	}
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	return runOgen(projected, absoluteTarget, packageName, true)
}

func writeScopeSidecarDocument(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, contents, 0o644)
}

func cloneScopeSidecarValue(value any) (any, error) {
	contents, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var copy any
	if err := json.Unmarshal(contents, &copy); err != nil {
		return nil, err
	}
	return copy, nil
}

func scopeSidecarSHA256(source []byte) string {
	digest := sha256.Sum256(source)
	return hex.EncodeToString(digest[:])
}
