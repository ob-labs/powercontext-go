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
	"bytes"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
)

var remoteSkillSidecarOperations = []scopeSidecarOperation{
	{OperationID: "list_remote_skill_targets", Method: "post", Path: "/v1/skill/remote/targets"},
	{OperationID: "create_remote_skill_target", Method: "post", Path: "/v1/skill/remote/target/create"},
	{OperationID: "enroll_remote_skill_target", Method: "post", Path: "/v1/skill/remote/target/enroll"},
	{OperationID: "rename_remote_skill_target", Method: "post", Path: "/v1/skill/remote/target/rename"},
	{OperationID: "revoke_remote_skill_target", Method: "post", Path: "/v1/skill/remote/target/revoke"},
}

var remoteSkillSidecarAgentKinds = []string{"codex", "workbuddy"}

type remoteSkillSidecarManifest struct {
	SchemaVersion int                     `json:"schema_version"`
	Upstream      scopeSidecarUpstream    `json:"upstream"`
	Operations    []scopeSidecarOperation `json:"operations"`
	Overlays      struct {
		RemoteAgentKind struct {
			Enum []string `json:"enum"`
		} `json:"remote_agent_kind"`
	} `json:"overlays"`
}

func loadRemoteSkillSidecarManifest(path string) (remoteSkillSidecarManifest, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return remoteSkillSidecarManifest{}, err
	}
	var manifest remoteSkillSidecarManifest
	if err := json.Unmarshal(contents, &manifest, json.RejectUnknownMembers(true)); err != nil {
		return remoteSkillSidecarManifest{}, err
	}
	return manifest, manifest.validate()
}

func (m remoteSkillSidecarManifest) validate() error {
	if m.SchemaVersion != 1 || m.Upstream.Repository != upstreamScopeRepository ||
		m.Upstream.Commit != upstreamScopeCommit || m.Upstream.Path != upstreamScopePath ||
		m.Upstream.SHA256 != "aca2a9dedfc4733176df3f7e27861deedcc751e0042515e3014ee9c36a017ec3" ||
		!slices.Equal(m.Operations, remoteSkillSidecarOperations) ||
		!slices.Equal(m.Overlays.RemoteAgentKind.Enum, remoteSkillSidecarAgentKinds) {
		return errors.New("invalid pinned remote Skill sidecar manifest")
	}
	return nil
}

func projectRemoteSkillSidecar(source []byte, manifest remoteSkillSidecarManifest, legacy, compatibility []byte) ([]byte, error) {
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	if scopeSidecarSHA256(source) != manifest.Upstream.SHA256 {
		return nil, errors.New("pinned remote Skill sidecar upstream digest differs")
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
	if err := applyRemoteSkillSidecarOverlays(components, manifest.Overlays.RemoteAgentKind.Enum); err != nil {
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
	if bytes.Contains(encoded, []byte("claude_code")) {
		return nil, errors.New("remote Skill sidecar retains unsupported remote agent kind")
	}
	if err := validateProjectedScopeSidecar(encoded, manifest.Operations); err != nil {
		return nil, err
	}
	return encoded, nil
}

func applyRemoteSkillSidecarOverlays(components map[string]any, agentKinds []string) error {
	if !slices.Equal(agentKinds, remoteSkillSidecarAgentKinds) {
		return errors.New("invalid remote Skill sidecar remote agent kind overlay")
	}
	schemas, err := scopeSidecarObject(components, "schemas")
	if err != nil {
		return err
	}
	remoteAgentKind, err := scopeSidecarObject(schemas, "RemoteAgentKind")
	if err != nil {
		return err
	}
	if remoteAgentKind["type"] != "string" {
		return errors.New("remote Skill sidecar RemoteAgentKind is not a string enum")
	}
	remoteAgentKind["enum"] = []any{agentKinds[0], agentKinds[1]}
	return nil
}

func runRemoteSkillSidecar(sourcePath, manifestPath, target, packageName, clientInvoker, compatibilityPath, legacyPath string) error {
	if packageName != "remoteskills" || filepath.Base(filepath.Clean(target)) != "remoteskills" ||
		filepath.Base(filepath.Dir(filepath.Clean(target))) != "canonical" ||
		filepath.Base(filepath.Dir(filepath.Dir(filepath.Clean(target)))) != "api" || clientInvoker != "" {
		return errors.New("remote Skill sidecar must target api/canonical/remoteskills without a Client Invoker")
	}
	manifest, err := loadRemoteSkillSidecarManifest(manifestPath)
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
	projected, err := projectRemoteSkillSidecar(upstream, manifest, legacy, compatibility)
	if err != nil {
		return err
	}
	if err := writeScopeSidecarDocument(filepath.Join(filepath.Dir(manifestPath), "remote-skills.json"), projected); err != nil {
		return err
	}
	absolute, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	return runOgen(projected, absolute, packageName, true)
}
