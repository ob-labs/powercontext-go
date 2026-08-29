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

package sqlstore

import (
	"context"
	"database/sql"
	"errors"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/source"
)

func insertMemoryEntry(
	ctx context.Context,
	db DBTX,
	scopeID string,
	entry memory.EntryVersion,
) error {
	sourceRefs, err := encodeSourceRefs(entry.Sources)
	if err != nil {
		return &InvalidStoredPayloadError{Kind: "memory-entry", Name: entry.EntryVersionID, Issue: "value is not JSON serializable"}
	}
	artifactRefs, err := encodeArtifactRefs(entry.Artifacts)
	if err != nil {
		return &InvalidStoredPayloadError{Kind: "memory-entry", Name: entry.EntryVersionID, Issue: "value is not JSON serializable"}
	}
	_, err = db.ExecContext(ctx, `INSERT INTO pc_memory_entry_versions (
        scope_id, family, memory_artifact_id, entry_id, entry_version_id, version,
        previous_version_id, kind, text, source_refs, artifact_refs,
        entry_content_hash, created_in_revision
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		scopeID, memory.Family, entry.MemoryArtifactID, entry.EntryID, entry.EntryVersionID,
		entry.Version, nullableText(entry.PreviousVersionID), entry.Kind, entry.Text, sourceRefs,
		artifactRefs, entry.EntryContentHash, entry.CreatedInRevision)
	return err
}

func findMemoryEntry(
	ctx context.Context,
	db DBTX,
	scopeID, memoryID, entryID, versionID string,
) (memory.EntryVersion, bool, error) {
	query := `SELECT memory_artifact_id, entry_id, entry_version_id, version,
        previous_version_id, kind, text, source_refs, artifact_refs,
        entry_content_hash, created_in_revision
        FROM pc_memory_entry_versions
        WHERE scope_id = ? AND memory_artifact_id = ? AND entry_id = ? AND entry_version_id = ?`
	entry, err := scanMemoryEntry(db.QueryRowContext(ctx, query, scopeID, memoryID, entryID, versionID))
	if errors.Is(err, sql.ErrNoRows) {
		return memory.EntryVersion{}, false, nil
	}
	return entry, err == nil, err
}

func scanMemoryEntry(value scanner) (memory.EntryVersion, error) {
	var memoryID, entryID, versionID, kind, text, contentHash string
	var version, previous, sourcePayload, artifactPayload, revision any
	if err := value.Scan(
		&memoryID,
		&entryID,
		&versionID,
		&version,
		&previous,
		&kind,
		&text,
		&sourcePayload,
		&artifactPayload,
		&contentHash,
		&revision,
	); err != nil {
		return memory.EntryVersion{}, err
	}
	return decodeMemoryEntryColumns(
		memoryID, entryID, versionID, kind, text, contentHash,
		version, previous, sourcePayload, artifactPayload, revision,
	)
}

func decodeMemoryEntryColumns(
	memoryID, entryID, versionID, kind, text, contentHash string,
	version, previous, sourcePayload, artifactPayload, revision any,
) (memory.EntryVersion, error) {
	decodedVersion, ok := integer(version)
	if !ok {
		return memory.EntryVersion{}, &InvalidStoredColumnError{Column: "version", Expected: "an integer"}
	}
	decodedRevision, ok := integer(revision)
	if !ok {
		return memory.EntryVersion{}, &InvalidStoredColumnError{Column: "created_in_revision", Expected: "an integer"}
	}
	var previousID *string
	if previous != nil {
		previousText, previousOK := previous.(string)
		if !previousOK {
			return memory.EntryVersion{}, &InvalidStoredColumnError{Column: "previous_version_id", Expected: "a string or null"}
		}
		previousID = &previousText
	}
	sourceBytes, err := storedBytes(sourcePayload, "memory payload")
	if err != nil {
		return memory.EntryVersion{}, err
	}
	artifactBytes, err := storedBytes(artifactPayload, "memory payload")
	if err != nil {
		return memory.EntryVersion{}, err
	}
	sources, err := decodeSourceRefs(sourceBytes)
	if err != nil {
		return memory.EntryVersion{}, &InvalidStoredPayloadError{Kind: "memory-entry", Name: versionID, Issue: "payload does not match the model"}
	}
	artifacts, err := decodeArtifactRefs(artifactBytes)
	if err != nil {
		return memory.EntryVersion{}, &InvalidStoredPayloadError{Kind: "memory-entry", Name: versionID, Issue: "payload does not match the model"}
	}
	return memory.EntryVersion{
		MemoryArtifactID:  memoryID,
		EntryID:           entryID,
		EntryVersionID:    versionID,
		Version:           decodedVersion,
		PreviousVersionID: previousID,
		Kind:              kind,
		Text:              text,
		Sources:           sources,
		Artifacts:         artifacts,
		EntryContentHash:  contentHash,
		CreatedInRevision: decodedRevision,
	}, nil
}

func encodeSourceRefs(values []source.Ref) ([]byte, error) {
	encoded := make([]sourceRefJSON, len(values))
	for index, value := range values {
		if _, err := source.NewRef(value.Type(), value.ID()); err != nil {
			return nil, err
		}
		encoded[index] = sourceRefJSON{SourceType: value.Type(), SourceID: value.ID()}
	}
	return marshalJSON(encoded)
}

func decodeSourceRefs(payload []byte) ([]source.Ref, error) {
	var encoded []sourceRefJSON
	if err := unmarshalJSON(payload, &encoded); err != nil {
		return nil, err
	}
	result := make([]source.Ref, len(encoded))
	for index, value := range encoded {
		ref, err := source.NewRef(value.SourceType, value.SourceID)
		if err != nil {
			return nil, err
		}
		result[index] = ref
	}
	return result, nil
}

func encodeArtifactRefs(values []artifact.Ref) ([]byte, error) {
	encoded := make([]artifactRefJSON, len(values))
	for index, value := range values {
		if err := value.Validate(); err != nil {
			return nil, err
		}
		encoded[index] = encodeArtifactRef(value)
	}
	return marshalJSON(encoded)
}

func decodeArtifactRefs(payload []byte) ([]artifact.Ref, error) {
	var encoded []artifactRefJSON
	if err := unmarshalJSON(payload, &encoded); err != nil {
		return nil, err
	}
	result := make([]artifact.Ref, len(encoded))
	for index, value := range encoded {
		ref, err := artifact.NewRef(value.Family, value.ArtifactID, value.Revision)
		if err != nil {
			return nil, err
		}
		result[index] = ref
	}
	return result, nil
}

func nullableText(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
