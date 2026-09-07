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
)

var artifactSidecarOperations = []scopeSidecarOperation{
	{OperationID: "list_artifacts", Method: "get", Path: "/v1/scopes/{scope_id}/artifacts/{family}"},
	{OperationID: "get_artifact", Method: "get", Path: "/v1/scopes/{scope_id}/artifacts/{family}/{artifact_id}"},
	{OperationID: "get_artifact_revision", Method: "get", Path: "/v1/scopes/{scope_id}/artifacts/{family}/{artifact_id}/revisions/{revision}"},
}

type artifactSidecarManifest struct {
	SchemaVersion int                     `json:"schema_version"`
	Upstream      scopeSidecarUpstream    `json:"upstream"`
	Operations    []scopeSidecarOperation `json:"operations"`
	Overlays      artifactSidecarOverlay  `json:"overlays"`
}

type artifactSidecarOverlay struct {
	Policy      string   `json:"policy"`
	SourceTypes []string `json:"artifact_revision_source_types"`
}

func loadArtifactSidecarManifest(path string) (artifactSidecarManifest, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return artifactSidecarManifest{}, err
	}
	var manifest artifactSidecarManifest
	if err := json.Unmarshal(contents, &manifest, json.RejectUnknownMembers(true)); err != nil {
		return artifactSidecarManifest{}, err
	}
	return manifest, manifest.validate()
}

func (m artifactSidecarManifest) validate() error {
	if m.SchemaVersion != 1 || m.Upstream.Repository != upstreamScopeRepository ||
		m.Upstream.Commit != upstreamScopeCommit || m.Upstream.Path != upstreamScopePath ||
		m.Upstream.SHA256 != "aca2a9dedfc4733176df3f7e27861deedcc751e0042515e3014ee9c36a017ec3" ||
		!slices.Equal(m.Operations, artifactSidecarOperations) ||
		m.Overlays.Policy != "https://github.com/ob-labs/powercontext-go/issues/202#issuecomment-5575546837" ||
		!slices.Equal(m.Overlays.SourceTypes, []string{"content", "external-skill-snapshot", "accepted-observation"}) {
		return errors.New("invalid pinned Artifact sidecar manifest")
	}
	return nil
}

func projectArtifactSidecar(source []byte, manifest artifactSidecarManifest, legacy, compatibility []byte) ([]byte, error) {
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	if scopeSidecarSHA256(source) != manifest.Upstream.SHA256 {
		return nil, errors.New("pinned Artifact sidecar upstream digest differs")
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
	// The cloned component is reachable only from ArtifactRevision and
	// ArtifactCollectionItem in this sidecar. The pinned upstream document and
	// other sidecars stay immutable.
	schemas, err := scopeSidecarObject(components, "schemas")
	if err != nil {
		return nil, err
	}
	reference, err := scopeSidecarObject(schemas, "SourceTypeReference")
	if err != nil {
		return nil, err
	}
	properties, err := scopeSidecarObject(reference, "properties")
	if err != nil {
		return nil, err
	}
	sourceType, err := scopeSidecarObject(properties, "source_type")
	if err != nil {
		return nil, err
	}
	sourceType["enum"] = slices.Clone(manifest.Overlays.SourceTypes)
	projected := map[string]any{"openapi": document["openapi"], "info": document["info"], "paths": projectedPaths, "components": components}
	if security, found := document["security"]; found {
		projected["security"] = security
	}
	encoded, err := json.Marshal(projected, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	if err := validateProjectedScopeSidecar(encoded, manifest.Operations); err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func runArtifactSidecar(sourcePath, manifestPath, target, packageName, clientInvoker, compatibilityPath, legacyPath string) error {
	if packageName != "artifacts" || filepath.Base(filepath.Clean(target)) != "artifacts" ||
		filepath.Base(filepath.Dir(filepath.Clean(target))) != "canonical" || clientInvoker != "" {
		return errors.New("Artifact sidecar must target api/canonical/artifacts without a Client Invoker")
	}
	manifest, err := loadArtifactSidecarManifest(manifestPath)
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
	projected, err := projectArtifactSidecar(upstream, manifest, legacy, compatibility)
	if err != nil {
		return err
	}
	if writeErr := writeScopeSidecarDocument(filepath.Join(filepath.Dir(manifestPath), "artifacts.json"), projected); writeErr != nil {
		return writeErr
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	return runOgen(projected, absolute, packageName, true)
}
