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
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/ghodss/yaml"
)

var statsSidecarOperations = []scopeSidecarOperation{
	{OperationID: "get_stats", Method: "post", Path: "/v1/stats"},
}

type statsSidecarManifest struct {
	SchemaVersion int                     `json:"schema_version"`
	Upstream      scopeSidecarUpstream    `json:"upstream"`
	Operations    []scopeSidecarOperation `json:"operations"`
}

func loadStatsSidecarManifest(path string) (statsSidecarManifest, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return statsSidecarManifest{}, err
	}
	var manifest statsSidecarManifest
	if err := json.Unmarshal(contents, &manifest, json.RejectUnknownMembers(true)); err != nil {
		return statsSidecarManifest{}, err
	}
	return manifest, manifest.validate()
}

func (m statsSidecarManifest) validate() error {
	if m.SchemaVersion != 1 || m.Upstream.Repository != upstreamScopeRepository ||
		m.Upstream.Commit != upstreamScopeCommit || m.Upstream.Path != upstreamScopePath ||
		m.Upstream.SHA256 != "aca2a9dedfc4733176df3f7e27861deedcc751e0042515e3014ee9c36a017ec3" ||
		!slices.Equal(m.Operations, statsSidecarOperations) {
		return errors.New("invalid pinned Stats sidecar manifest")
	}
	return nil
}

func projectStatsSidecar(source []byte, manifest statsSidecarManifest, legacy, compatibility []byte) ([]byte, error) {
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	if scopeSidecarSHA256(source) != manifest.Upstream.SHA256 {
		return nil, errors.New("pinned Stats sidecar upstream digest differs")
	}
	if err := validatePinnedScopeSidecarSource(source, manifest.Operations, legacy, compatibility); err != nil {
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
	components, err := projectScopeSidecarComponents(document, projectedPaths)
	if err != nil {
		return nil, err
	}
	projected := map[string]any{
		"openapi": document["openapi"], "info": document["info"], "paths": projectedPaths, "components": components,
	}
	if security, found := document["security"]; found {
		projected["security"] = security
	}
	encoded, err := json.Marshal(projected, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if err := validateProjectedScopeSidecar(encoded, manifest.Operations); err != nil {
		return nil, err
	}
	return encoded, nil
}

func runStatsSidecar(sourcePath, manifestPath, target, packageName, clientInvoker, compatibilityPath, legacyPath string) error {
	if packageName != "stats" || filepath.Base(filepath.Clean(target)) != "stats" ||
		filepath.Base(filepath.Dir(filepath.Clean(target))) != "canonical" || clientInvoker != "" {
		return errors.New("Stats sidecar must target api/canonical/stats without a Client Invoker")
	}
	manifest, err := loadStatsSidecarManifest(manifestPath)
	if err != nil {
		return err
	}
	upstream, err := os.ReadFile(sourcePath)
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
	projected, err := projectStatsSidecar(upstream, manifest, legacy, compatibility)
	if err != nil {
		return err
	}
	if writeErr := writeScopeSidecarDocument(filepath.Join(filepath.Dir(manifestPath), "stats.json"), projected); writeErr != nil {
		return writeErr
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	generatedSource, err := yaml.JSONToYAML(projected)
	if err != nil {
		return err
	}
	generatedInput, err := normalizeNullableReferences(generatedSource)
	if err != nil {
		return err
	}
	return runOgen(generatedInput, absolute, packageName, true)
}
