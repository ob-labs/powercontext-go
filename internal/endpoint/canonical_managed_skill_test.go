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
	"errors"
	"os"
	"path/filepath"
	"testing"

	managedskills "github.com/ob-labs/powercontext-go/api/canonical/managedskills"
	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/source"
)

type managedSkillPackageOperationsFunc func(context.Context, string, artifact.Ref) (skill.PackageSnapshot, error)

func (function managedSkillPackageOperationsFunc) ReadSkillPackage(ctx context.Context, scopeID string, ref artifact.Ref) (skill.PackageSnapshot, error) {
	return function(ctx, scopeID, ref)
}

func (managedSkillPackageOperationsFunc) Record(context.Context, string, source.SkillUsageCapture) (runtime.SourceReceipt, error) {
	return runtime.SourceReceipt{}, errors.New("usage record was not expected")
}

type managedSkillUsageOperations struct {
	record func(context.Context, string, source.SkillUsageCapture) (runtime.SourceReceipt, error)
}

type managedSkillProposalOperationsStub struct {
	propose func(context.Context, string, []byte, []artifact.Ref, *artifact.Ref, *string) (review.Snapshot, error)
}

func (managedSkillProposalOperationsStub) ReadSkillPackage(context.Context, string, artifact.Ref) (skill.PackageSnapshot, error) {
	return skill.PackageSnapshot{}, errors.New("package read was not expected")
}

func (managedSkillProposalOperationsStub) Record(context.Context, string, source.SkillUsageCapture) (runtime.SourceReceipt, error) {
	return runtime.SourceReceipt{}, errors.New("usage record was not expected")
}

func (operations managedSkillProposalOperationsStub) ProposeUploadedPackage(
	ctx context.Context,
	scopeID string,
	archive []byte,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (review.Snapshot, error) {
	return operations.propose(ctx, scopeID, archive, artifacts, target, reason)
}

func (managedSkillUsageOperations) ReadSkillPackage(context.Context, string, artifact.Ref) (skill.PackageSnapshot, error) {
	return skill.PackageSnapshot{}, errors.New("package read was not expected")
}

func (operations managedSkillUsageOperations) Record(
	ctx context.Context,
	scopeID string,
	capture source.SkillUsageCapture,
) (runtime.SourceReceipt, error) {
	return operations.record(ctx, scopeID, capture)
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

func TestCanonicalManagedSkillHandlerRecordsBoundedUsage(t *testing.T) {
	observed := false
	handler := NewCanonicalManagedSkillHandler(managedSkillUsageOperations{
		record: func(_ context.Context, scopeID string, capture source.SkillUsageCapture) (runtime.SourceReceipt, error) {
			observed = true
			if scopeID != "scope" || capture.ObservationID() != "usage-1" || capture.SkillRef().Family() != skill.Family ||
				capture.SkillRef().ID() != "package" || capture.SkillRef().Revision() != 2 ||
				capture.PackageDigest() != "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
				capture.TargetID() != "workbuddy-target" || !capture.Selected() ||
				capture.Invoked() != source.ObservedInvocationTrue || capture.Validation() != source.ObservedValidationPassed ||
				capture.Outcome() != source.ObservedOutcomeSuccess {
				t.Fatalf("usage capture = %#v", capture)
			}
			taskSource, found := capture.TaskSource()
			if !found || taskSource.Type() != "content" || taskSource.ID() != "task-1" {
				t.Fatalf("task Source = %#v, found=%t", taskSource, found)
			}
			fingerprint, found := capture.EnvironmentFingerprint()
			if !found || fingerprint != "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
				t.Fatalf("environment fingerprint = %q, found=%t", fingerprint, found)
			}
			ref, err := source.NewRef(source.SkillUsageType, "usage-1")
			if err != nil {
				t.Fatal(err)
			}
			return runtime.SourceReceipt{Ref: ref, Sequence: 4}, nil
		},
	})
	request := &managedskills.RecordSkillUsageRequest{
		ScopeID: "scope", ObservationID: "usage-1",
		SkillRef:      managedskills.ArtifactReference{Family: skill.Family, ArtifactID: "package", Revision: 2},
		PackageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TargetID:      "workbuddy-target", Selected: true,
		Invoked:                managedskills.RecordSkillUsageRequestInvokedTrue,
		Validation:             managedskills.RecordSkillUsageRequestValidationPassed,
		Outcome:                managedskills.RecordSkillUsageRequestOutcomeSuccess,
		TaskSource:             managedskills.NewOptSourceReference(managedskills.SourceReference{Name: "content", SourceID: "task-1"}),
		EnvironmentFingerprint: managedskills.NewOptNilString("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	}
	result, err := handler.RecordSkillUsage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := result.(*managedskills.CaptureContentSourceResponse)
	if !ok || !observed || response.Status != managedskills.CaptureStatusAccepted || response.Position != 4 ||
		response.Source.Name != source.SkillUsageType || response.Source.SourceID != "usage-1" {
		t.Fatalf("usage result = %#v, observed=%t", result, observed)
	}
}

func TestCanonicalManagedSkillHandlerProposesExactPackageCandidate(t *testing.T) {
	snapshot := endpointManagedSkillSnapshot(t)
	content, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := source.NewRef(source.SkillPackageUploadType, "skill_pkg_"+snapshot.Reference().TreeDigest())
	if err != nil {
		t.Fatal(err)
	}
	reason := "Package review"
	candidate, err := review.NewCandidate(
		"candidate-1", 1, skill.Family, review.Pending, content, []source.Ref{ref}, nil, nil, &reason, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	handler := NewCanonicalManagedSkillHandler(managedSkillProposalOperationsStub{
		propose: func(_ context.Context, scopeID string, archive []byte, artifacts []artifact.Ref, target *artifact.Ref, receivedReason *string) (review.Snapshot, error) {
			called = true
			if scopeID != "scope" || string(archive) != string(snapshot.Archive()) || len(artifacts) != 0 || target != nil || receivedReason == nil || *receivedReason != reason {
				t.Fatalf("proposal inputs: scope=%q archive=%d artifacts=%#v target=%#v reason=%#v", scopeID, len(archive), artifacts, target, receivedReason)
			}
			return candidate, nil
		},
	})
	request := &managedskills.ProposeSkillPackageRequest{
		ScopeID: "scope", ArchiveBase64: base64.StdEncoding.EncodeToString(snapshot.Archive()), Reason: managedskills.NewOptNilString(reason),
	}
	result, err := handler.ProposeSkillPackage(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := result.(*managedskills.ArtifactCandidate)
	if !ok || !called {
		t.Fatalf("proposal result = %#v, called=%t", result, called)
	}
	proposal, found := response.Proposal.GetSkillProposal()
	if !found || response.CandidateID != "candidate-1" || response.Status != managedskills.CandidateStatusPending ||
		response.Target.IsNull() != true || response.ResultArtifact.IsNull() != true || response.Reason.Or("") != reason ||
		len(response.SourceRefs) != 1 || response.SourceRefs[0].Name != source.SkillPackageUploadType ||
		proposal.Package.IsSet() != true || proposal.Validation == nil || len(proposal.Validation) != 0 {
		t.Fatalf("proposal response = %#v", response)
	}
}

func TestCanonicalManagedSkillHandlerRejectsInvalidPackageEncoding(t *testing.T) {
	called := false
	handler := NewCanonicalManagedSkillHandler(managedSkillProposalOperationsStub{
		propose: func(context.Context, string, []byte, []artifact.Ref, *artifact.Ref, *string) (review.Snapshot, error) {
			called = true
			return nil, nil
		},
	})
	result, err := handler.ProposeSkillPackage(t.Context(), &managedskills.ProposeSkillPackageRequest{ScopeID: "scope", ArchiveBase64: "%%%"})
	if err == nil || result != nil || MapError(err).Code != "invalid_request" || called {
		t.Fatalf("invalid archive result=%#v error=%v called=%t", result, err, called)
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
