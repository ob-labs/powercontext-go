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

	managedskills "github.com/ob-labs/powercontext-go/api/canonical/managedskills"
	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/source"
)

// ManagedSkillOperations is the Runtime-owned exact package-read and bounded
// usage-write surface used by the canonical managed-Skill sidecar.
type ManagedSkillOperations interface {
	ReadSkillPackage(context.Context, string, artifact.Ref) (skill.PackageSnapshot, error)
	Record(context.Context, string, source.SkillUsageCapture) (runtime.SourceReceipt, error)
}

// CanonicalManagedSkillHandler projects verified immutable Skill package
// snapshots and bounded immutable usage Source evidence onto the generated
// managed-Skill sidecar contract.
type CanonicalManagedSkillHandler struct {
	operations ManagedSkillOperations
}

var _ managedskills.Handler = (*CanonicalManagedSkillHandler)(nil)

func NewCanonicalManagedSkillHandler(operations ManagedSkillOperations) *CanonicalManagedSkillHandler {
	return &CanonicalManagedSkillHandler{operations: operations}
}

func (h *CanonicalManagedSkillHandler) RecordSkillUsage(
	ctx context.Context,
	request *managedskills.RecordSkillUsageRequest,
) (managedskills.RecordSkillUsageRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return nil, &InvalidRequestError{}
	}
	skillRef, err := source.NewSkillUsageArtifactReference(
		request.SkillRef.Family,
		request.SkillRef.ArtifactID,
		int64(request.SkillRef.Revision),
	)
	if err != nil {
		return nil, &InvalidRequestError{}
	}
	var taskSource *source.Ref
	if value, found := request.TaskSource.Get(); found {
		ref, refErr := source.NewRef(value.Name, value.SourceID)
		if refErr != nil {
			return nil, &InvalidRequestError{}
		}
		taskSource = &ref
	}
	var environmentFingerprint *string
	if value, found := request.EnvironmentFingerprint.Get(); found {
		environmentFingerprint = &value
	}
	capture, err := source.NewSkillUsageCapture(
		request.ObservationID,
		skillRef,
		request.PackageDigest,
		request.TargetID,
		request.Selected,
		source.ObservedInvocation(request.Invoked),
		source.ObservedValidation(request.Validation),
		source.ObservedOutcome(request.Outcome),
		taskSource,
		environmentFingerprint,
	)
	if err != nil {
		return nil, &InvalidRequestError{}
	}
	receipt, err := h.operations.Record(ctx, request.ScopeID, capture)
	if err != nil {
		return nil, err
	}
	position := int(receipt.Sequence)
	if receipt.Sequence < 1 || int64(position) != receipt.Sequence {
		return nil, &RuntimeNotReadyError{}
	}
	return &managedskills.CaptureContentSourceResponse{
		Status:   managedskills.CaptureStatusAccepted,
		Source:   managedskills.SourceReference{Name: receipt.Ref.Type(), SourceID: receipt.Ref.ID()},
		Position: position,
	}, nil
}

func (h *CanonicalManagedSkillHandler) GetSkillPackageManifest(
	ctx context.Context,
	request *managedskills.GetSkillPackageRequest,
) (managedskills.GetSkillPackageManifestRes, error) {
	snapshot, err := h.read(ctx, request)
	if err != nil {
		return nil, err
	}
	manifest, err := canonicalManagedSkillManifest(snapshot)
	if err != nil {
		return nil, err
	}
	return &manifest, nil
}

func (h *CanonicalManagedSkillHandler) DownloadSkillPackage(
	ctx context.Context,
	request *managedskills.GetSkillPackageRequest,
) (managedskills.DownloadSkillPackageRes, error) {
	snapshot, err := h.read(ctx, request)
	if err != nil {
		return nil, err
	}
	packageRef, err := canonicalManagedSkillPackageReference(snapshot.Reference())
	if err != nil {
		return nil, err
	}
	result := managedskills.SkillPackageDownload{
		ArchiveBase64: base64.StdEncoding.EncodeToString(snapshot.Archive()),
		Package:       packageRef,
	}
	if err := result.Validate(); err != nil {
		return nil, errors.New("managed Skill package snapshot does not match the download contract")
	}
	return &result, nil
}

func (h *CanonicalManagedSkillHandler) read(ctx context.Context, request *managedskills.GetSkillPackageRequest) (skill.PackageSnapshot, error) {
	if h == nil || h.operations == nil {
		return skill.PackageSnapshot{}, &RuntimeNotReadyError{}
	}
	if request == nil || request.Artifact.Revision < 1 {
		return skill.PackageSnapshot{}, &InvalidRequestError{}
	}
	ref, err := artifact.NewRef(request.Artifact.Family, request.Artifact.ArtifactID, int64(request.Artifact.Revision))
	if err != nil {
		return skill.PackageSnapshot{}, &InvalidRequestError{}
	}
	snapshot, err := h.operations.ReadSkillPackage(ctx, request.ScopeID, ref)
	if err != nil {
		if _, missing := errors.AsType[*artifact.NotFoundError](err); missing {
			return skill.PackageSnapshot{}, &managedSkillPackageNotFoundError{}
		}
		return skill.PackageSnapshot{}, err
	}
	return snapshot, nil
}

func canonicalManagedSkillManifest(snapshot skill.PackageSnapshot) (managedskills.SkillPackageManifest, error) {
	packageRef, err := canonicalManagedSkillPackageReference(snapshot.Reference())
	if err != nil {
		return managedskills.SkillPackageManifest{}, err
	}
	metadata := snapshot.Metadata()
	entries := snapshot.Entries()
	files := make([]managedskills.SkillPackageFile, len(entries))
	for index, entry := range entries {
		files[index] = managedskills.SkillPackageFile{
			Path: entry.Path(), Digest: entry.Digest(), Size: entry.Size(), MediaType: entry.MediaType(),
			Executable: entry.Mode()&0o111 != 0,
		}
	}
	result := managedskills.SkillPackageManifest{
		Description: metadata.Description(), Files: files, Metadata: managedskills.SkillPackageManifestMetadata(metadata.Metadata()),
		Name: metadata.Name(), Package: packageRef,
	}
	result.License.SetToNull()
	if value := metadata.License(); value != "" {
		result.License.SetTo(value)
	}
	result.Compatibility.SetToNull()
	if value := metadata.Compatibility(); value != "" {
		result.Compatibility.SetTo(value)
	}
	result.AllowedTools.SetToNull()
	if value := metadata.AllowedTools(); value != "" {
		result.AllowedTools.SetTo(value)
	}
	if err := result.Validate(); err != nil {
		return managedskills.SkillPackageManifest{}, errors.New("managed Skill package snapshot does not match the manifest contract")
	}
	return result, nil
}

func canonicalManagedSkillPackageReference(ref skill.PackageRef) (managedskills.SkillPackageReference, error) {
	if err := ref.Validate(); err != nil {
		return managedskills.SkillPackageReference{}, errors.New("managed Skill package reference is unavailable")
	}
	result := managedskills.SkillPackageReference{
		TreeDigest: ref.TreeDigest(), ArchiveDigest: ref.ArchiveDigest(), FileCount: ref.FileCount(),
		UncompressedSize: ref.UncompressedSize(), ArchiveSize: ref.ArchiveSize(),
	}
	if err := result.Validate(); err != nil {
		return managedskills.SkillPackageReference{}, errors.New("managed Skill package reference is not representable")
	}
	return result, nil
}

type managedSkillPackageNotFoundError struct{}

func (*managedSkillPackageNotFoundError) Error() string { return "managed Skill package was not found" }
