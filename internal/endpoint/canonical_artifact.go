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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"strconv"

	webpkijcs "github.com/gowebpki/jcs"

	canonicalartifact "github.com/ob-labs/powercontext-go/api/canonical/artifacts"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/runtime"
)

type ArtifactResourceOperations interface {
	GetArtifact(context.Context, string, string, string, int64) (runtime.ArtifactRecord, error)
}

type CanonicalArtifactHandler struct{ operations ArtifactResourceOperations }

var _ canonicalartifact.Handler = (*CanonicalArtifactHandler)(nil)

func NewCanonicalArtifactHandler(operations ArtifactResourceOperations) *CanonicalArtifactHandler {
	return &CanonicalArtifactHandler{operations: operations}
}

func (h *CanonicalArtifactHandler) GetArtifact(ctx context.Context, params canonicalartifact.GetArtifactParams) (canonicalartifact.GetArtifactRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	result, err := h.operations.GetArtifact(ctx, params.ScopeID, string(params.Family), params.ArtifactID, 0)
	if err != nil {
		return nil, err
	}
	record, err := canonicalArtifactRecord(result)
	if err != nil {
		return nil, err
	}
	etag := canonicalartifact.NewOptString(`"revision:` + strconv.FormatInt(result.Value.Ref().Revision(), 10) + `"`)
	if validator, exists := params.IfNoneMatch.Get(); exists && validator == etag.Value {
		return &canonicalartifact.GetArtifactNotModified{ETag: etag, XPowerContextRequestID: canonicalArtifactRequestID(ctx)}, nil
	}
	return &canonicalartifact.ArtifactRevisionHeaders{Response: record, ETag: etag, XPowerContextRequestID: canonicalArtifactRequestID(ctx)}, nil
}

func (h *CanonicalArtifactHandler) GetArtifactRevision(ctx context.Context, params canonicalartifact.GetArtifactRevisionParams) (canonicalartifact.GetArtifactRevisionRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if params.Revision < 1 {
		return nil, &InvalidRequestError{}
	}
	result, err := h.operations.GetArtifact(ctx, params.ScopeID, string(params.Family), params.ArtifactID, int64(params.Revision))
	if err != nil {
		return nil, err
	}
	record, err := canonicalArtifactRecord(result)
	if err != nil {
		return nil, err
	}
	return &canonicalartifact.GetArtifactRevisionOKHeaders{Response: record, XPowerContextRequestID: canonicalArtifactRequestID(ctx)}, nil
}

func canonicalArtifactRecord(result runtime.ArtifactRecord) (canonicalartifact.ArtifactRevision, error) {
	if result.Value == nil {
		return canonicalartifact.ArtifactRevision{}, errors.New("Artifact snapshot is unavailable")
	}
	value := result.Value
	ref := value.Ref()
	record := canonicalartifact.ArtifactRevision{ScopeID: result.ScopeID, ArtifactID: ref.ID(), Revision: int(ref.Revision())}
	if int64(record.Revision) != ref.Revision() || ref.Revision() < 1 {
		return record, errors.New("Artifact revision is not representable")
	}
	switch ref.Family() {
	case memory.Family:
		record.Family = canonicalartifact.BaseArtifactFamilyMemory
	case experience.Family:
		record.Family = canonicalartifact.BaseArtifactFamilyExperience
	case skill.Family:
		record.Family = canonicalartifact.BaseArtifactFamilySkill
	case handoff.Family:
		record.Family = canonicalartifact.BaseArtifactFamilyHandoff
	default:
		return record, errors.New("Artifact family is unavailable")
	}
	content, err := canonicalArtifactContent(value.ContentValue())
	if err != nil {
		return record, err
	}
	if decodeErr := json.Unmarshal(content, &record.Content); decodeErr != nil {
		return record, decodeErr
	}
	// The resource digest follows upstream RFC 8785 without NFC normalization.
	canonical, err := webpkijcs.Transform(content)
	if err != nil {
		return record, err
	}
	digest := sha256.Sum256(canonical)
	record.ContentDigest = "sha256:" + hex.EncodeToString(digest[:])
	sources := value.Lineage().Sources()
	record.Sources = make([]canonicalartifact.SourceTypeReference, len(sources))
	for index, sourceRef := range sources {
		var sourceType canonicalartifact.SourceTypeReferenceSourceType
		switch sourceRef.Type() {
		case "content":
			sourceType = canonicalartifact.SourceTypeReferenceSourceTypeContent
		case "external-skill-snapshot":
			sourceType = canonicalartifact.SourceTypeReferenceSourceTypeExternalSkillSnapshot
		case "accepted-observation":
			sourceType = canonicalartifact.SourceTypeReferenceSourceTypeAcceptedObservation
		default:
			return record, errors.New("Artifact lineage Source type is unavailable")
		}
		record.Sources[index] = canonicalartifact.SourceTypeReference{SourceType: sourceType, SourceID: sourceRef.ID()}
	}
	artifacts := value.Lineage().Artifacts()
	record.Artifacts = make([]canonicalartifact.ArtifactReference, len(artifacts))
	for index, artifactRef := range artifacts {
		revision := int(artifactRef.Revision())
		if int64(revision) != artifactRef.Revision() {
			return record, errors.New("Artifact lineage revision is not representable")
		}
		record.Artifacts[index] = canonicalartifact.ArtifactReference{Family: artifactRef.Family(), ArtifactID: artifactRef.ID(), Revision: revision}
	}
	if validationErr := record.Validate(); validationErr != nil {
		return canonicalartifact.ArtifactRevision{}, errors.New("Artifact snapshot does not match the resource contract")
	}
	return record, nil
}

func canonicalArtifactContent(value any) ([]byte, error) {
	switch content := value.(type) {
	case experience.Content:
		return json.Marshal(map[string]any{"situation": content.Situation(), "action": content.Action(), "outcome": content.Outcome(), "lesson": content.Lesson()})
	case skill.Content:
		return json.Marshal(map[string]any{
			"name": content.Name(), "description": content.Description(), "instructions": content.Instructions(), "validation": content.Validation(),
			"package": nil, "license": nil, "compatibility": nil, "metadata": map[string]string{}, "allowed_tools": nil,
		})
	case skill.PackageContent:
		snapshot := content.Snapshot()
		metadata := snapshot.Metadata()
		ref := snapshot.Reference()
		var license, compatibility, allowedTools *string
		if value := metadata.License(); value != "" {
			license = new(value)
		}
		if value := metadata.Compatibility(); value != "" {
			compatibility = new(value)
		}
		if value := metadata.AllowedTools(); value != "" {
			allowedTools = new(value)
		}
		return json.Marshal(map[string]any{
			"name": metadata.Name(), "description": metadata.Description(), "instructions": snapshot.Instructions(), "validation": []string{},
			"license": license, "compatibility": compatibility, "metadata": metadata.Metadata(), "allowed_tools": allowedTools,
			"package": map[string]any{"tree_digest": ref.TreeDigest(), "archive_digest": ref.ArchiveDigest(), "file_count": ref.FileCount(), "uncompressed_size": ref.UncompressedSize(), "archive_size": ref.ArchiveSize()},
		})
	case memory.Content:
		return canonicalArtifactMemoryContent(content)
	case handoff.Content:
		return handoff.RenderContent(content)
	default:
		return nil, errors.New("Artifact content is unavailable")
	}
}

func canonicalArtifactMemoryContent(content memory.Content) ([]byte, error) {
	entries := content.Manifest().Entries()
	wireEntries := make([]map[string]any, len(entries))
	for index, entry := range entries {
		wireEntries[index] = map[string]any{
			"entry_id": entry.EntryID(), "entry_version_id": entry.EntryVersionID(),
			"entry_content_hash": entry.EntryContentHash(), "state": string(entry.State()),
		}
	}
	changes := content.Changes()
	wireChanges := make([]map[string]any, len(changes))
	for index, change := range changes {
		wireChanges[index] = map[string]any{
			"op": string(change.Op()), "entry_id": change.EntryID(),
			"from_entry_version_id": change.FromEntryVersionID(), "to_entry_version_id": change.ToEntryVersionID(), "reason": change.Reason(),
		}
	}
	return json.Marshal(map[string]any{
		"schema": content.Schema(), "manifest": map[string]any{"format": content.Manifest().Format(), "entries": wireEntries}, "changes": wireChanges,
	})
}

func canonicalArtifactRequestID(ctx context.Context) canonicalartifact.OptString {
	if id, exists := requestID(ctx).Get(); exists {
		return canonicalartifact.NewOptString(id)
	}
	return canonicalartifact.OptString{}
}
