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

package endpoint

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	managedskills "github.com/ob-labs/powercontext-go/api/canonical/managedskills"
	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
)

type managedSkillPackageOperationsFunc func(context.Context, string, artifact.Ref) (skill.PackageSnapshot, error)

func (function managedSkillPackageOperationsFunc) ReadSkillPackage(ctx context.Context, scopeID string, ref artifact.Ref) (skill.PackageSnapshot, error) {
	return function(ctx, scopeID, ref)
}

func TestCanonicalManagedSkillHandlerProjectsVerifiedSnapshot(t *testing.T) {
	snapshot := endpointManagedSkillSnapshot(t)
	called := false
	handler := NewCanonicalManagedSkillHandler(managedSkillPackageOperationsFunc(func(_ context.Context, scopeID string, ref artifact.Ref) (skill.PackageSnapshot, error) {
		called = true
		if scopeID != "scope" || ref.Family() != skill.Family || ref.ID() != "package" || ref.Revision() != 2 {
			t.Fatalf("read arguments = %q %#v", scopeID, ref)
		}
		return snapshot, nil
	}))
	request := &managedskills.GetSkillPackageRequest{ScopeID: "scope", Artifact: managedskills.ArtifactReference{Family: skill.Family, ArtifactID: "package", Revision: 2}}
	manifestResult, err := handler.GetSkillPackageManifest(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	manifest, ok := manifestResult.(*managedskills.SkillPackageManifest)
	if !ok || !called {
		t.Fatalf("manifest = %#v, called=%t", manifestResult, called)
	}
	ref := snapshot.Reference()
	if manifest.Name != "endpoint-skill" || manifest.Description != "Endpoint projection" || manifest.Package.TreeDigest != ref.TreeDigest() || len(manifest.Files) != len(snapshot.Entries()) || manifest.License.Null != true || manifest.Compatibility.Null != true || manifest.AllowedTools.Null != true {
		t.Fatalf("manifest = %#v", manifest)
	}
	downloadResult, err := handler.DownloadSkillPackage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	download, ok := downloadResult.(*managedskills.SkillPackageDownload)
	if !ok {
		t.Fatalf("download = %#v", downloadResult)
	}
	archive, err := base64.StdEncoding.DecodeString(download.ArchiveBase64)
	if err != nil || string(archive) != string(snapshot.Archive()) || download.Package != manifest.Package {
		t.Fatalf("download did not preserve the verified snapshot")
	}
}

func TestCanonicalManagedSkillHandlerClassifiesInvalidAndMissingReads(t *testing.T) {
	var readRef artifact.Ref
	handler := NewCanonicalManagedSkillHandler(managedSkillPackageOperationsFunc(func(_ context.Context, _ string, ref artifact.Ref) (skill.PackageSnapshot, error) {
		readRef = ref
		return skill.PackageSnapshot{}, &artifact.NotFoundError{}
	}))
	missing, err := handler.GetSkillPackageManifest(t.Context(), &managedskills.GetSkillPackageRequest{ScopeID: "scope", Artifact: managedskills.ArtifactReference{Family: "future.family", ArtifactID: "package", Revision: 1}})
	if err == nil || missing != nil || MapError(err).Code != "not_found" {
		t.Fatalf("missing result=%#v error=%v mapping=%#v", missing, err, MapError(err))
	}
	if readRef.Family() != "future.family" {
		t.Fatalf("missing arbitrary family was not delegated: %#v", readRef)
	}
	invalid, invalidErr := handler.GetSkillPackageManifest(t.Context(), &managedskills.GetSkillPackageRequest{ScopeID: "scope", Artifact: managedskills.ArtifactReference{Family: skill.Family, ArtifactID: "package", Revision: 0}})
	if invalidErr == nil || invalid != nil || MapError(invalidErr).Code != "invalid_request" {
		t.Fatalf("invalid result=%#v error=%v mapping=%#v", invalid, invalidErr, MapError(invalidErr))
	}
	if _, unavailableErr := (*CanonicalManagedSkillHandler)(nil).DownloadSkillPackage(t.Context(), nil); MapError(unavailableErr).Code != "runtime_not_ready" {
		t.Fatalf("nil handler error = %v", unavailableErr)
	}
}

func endpointManagedSkillSnapshot(t *testing.T) skill.PackageSnapshot {
	t.Helper()
	root := filepath.Join(t.TempDir(), "endpoint-skill")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: endpoint-skill\ndescription: Endpoint projection\nmetadata: {}\n---\ninspect package\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := skill.CapturePackageDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
