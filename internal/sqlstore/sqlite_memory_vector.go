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
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
)

const requiredSQLiteVecVersion = "v0.1.9"

// SQLiteMemoryVectorIndex maintains the Python-compatible sqlite-vec vec0
// active-head projection for one exact embedding profile. OpenSQLite embeds
// and initializes the extension on every connection.
type SQLiteMemoryVectorIndex struct {
	profile memory.EmbeddingProfile
}

func NewSQLiteMemoryVectorIndex(profile memory.EmbeddingProfile) (*SQLiteMemoryVectorIndex, error) {
	if profile.Dimension < 1 || profile.Distance != "l2" || profile.Normalization != "unit" {
		return nil, &memory.CapabilityNotSupportedError{
			Capability: "vector",
			Detail:     "sqlite-vec requires a positive unit-normalized L2 embedding profile",
		}
	}
	return &SQLiteMemoryVectorIndex{profile: profile}, nil
}

func (i *SQLiteMemoryVectorIndex) Capabilities() memory.Capabilities {
	profile := i.profile
	return memory.Capabilities{Vector: true, EmbeddingProfile: &profile}
}

func (i *SQLiteMemoryVectorIndex) Initialize(ctx context.Context, db DBTX) error {
	var version any
	if err := db.QueryRowContext(ctx, "SELECT vec_version()").Scan(&version); err != nil {
		return sqliteVecCapabilityError("sqlite-vec probe failed")
	}
	if err := validateSQLiteVecVersion(version); err != nil {
		return err
	}
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS pc_memory_vector_entries (
            vector_id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
            scope_id VARCHAR(256) NOT NULL,
            memory_artifact_id VARCHAR(128) NOT NULL,
            head_revision INTEGER NOT NULL,
            entry_id VARCHAR(128) NOT NULL,
            entry_version_id VARCHAR(128) NOT NULL,
            entry_content_hash VARCHAR(64) NOT NULL,
            embedding_content_hash VARCHAR(64) NOT NULL,
            CONSTRAINT uq_pc_memory_vector_entries_head UNIQUE (
                scope_id, memory_artifact_id, entry_id
            ),
            CONSTRAINT fk_pc_memory_vector_entries_head FOREIGN KEY (
                scope_id, memory_artifact_id, entry_id
            ) REFERENCES pc_memory_entry_heads (
                scope_id, memory_artifact_id, entry_id
            ) ON DELETE CASCADE,
            CONSTRAINT ck_pc_memory_vector_entries_revision_positive CHECK (head_revision > 0)
        )`,
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS pc_memory_entry_vec USING vec0(embedding float[%d])`, i.profile.Dimension),
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return sqliteVecCapabilityError("sqlite-vec probe failed")
		}
	}

	probe := make([]float64, i.profile.Dimension)
	packed, err := packSQLiteVector(probe)
	if err != nil {
		return sqliteVecCapabilityError("sqlite-vec probe failed")
	}
	if _, err := db.ExecContext(ctx,
		"INSERT INTO pc_memory_entry_vec (rowid, embedding) VALUES (?, ?)", -1, packed,
	); err != nil {
		return sqliteVecCapabilityError("sqlite-vec probe failed")
	}
	var rowID int64
	queryErr := db.QueryRowContext(ctx,
		"SELECT rowid FROM pc_memory_entry_vec WHERE embedding MATCH ? AND k = 1", packed,
	).Scan(&rowID)
	_, deleteErr := db.ExecContext(ctx, "DELETE FROM pc_memory_entry_vec WHERE rowid = ?", -1)
	if queryErr != nil || deleteErr != nil {
		return sqliteVecCapabilityError("sqlite-vec probe failed")
	}
	if rowID != -1 {
		return sqliteVecCapabilityError("sqlite-vec probe returned an invalid row")
	}
	return nil
}

func (i *SQLiteMemoryVectorIndex) Replace(
	ctx context.Context,
	db DBTX,
	scopeID string,
	memoryRef artifact.Ref,
	projections []memory.Projection,
) error {
	rows, err := db.QueryContext(ctx, `SELECT vector_id FROM pc_memory_vector_entries
        WHERE scope_id = ? AND memory_artifact_id = ? ORDER BY vector_id`, scopeID, memoryRef.ID())
	if err != nil {
		return err
	}
	vectorIDs := make([]int64, 0)
	for rows.Next() {
		var vectorID int64
		if err := rows.Scan(&vectorID); err != nil {
			return errors.Join(err, closeRows(rows))
		}
		vectorIDs = append(vectorIDs, vectorID)
	}
	if err := closeRows(rows); err != nil {
		return err
	}
	// vec0 is not a relational child table, so its rows must be removed before
	// metadata. This ordering is part of the Python on-disk contract.
	for _, vectorID := range vectorIDs {
		if _, err := db.ExecContext(ctx,
			"DELETE FROM pc_memory_entry_vec WHERE rowid = ?", vectorID,
		); err != nil {
			return err
		}
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM pc_memory_vector_entries
        WHERE scope_id = ? AND memory_artifact_id = ?`, scopeID, memoryRef.ID()); err != nil {
		return err
	}

	for _, projection := range projections {
		embedding := projection.Embedding()
		embeddingHash := projection.EmbeddingContentHash()
		if len(embedding) == 0 || embeddingHash == nil {
			continue
		}
		vector, err := memory.ValidateEmbedding(embedding, i.profile.Dimension)
		if err != nil {
			return err
		}
		packed, err := packSQLiteVector(vector)
		if err != nil {
			return err
		}
		entry := projection.EntryVersion()
		result, err := db.ExecContext(ctx, `INSERT INTO pc_memory_vector_entries (
            scope_id, memory_artifact_id, head_revision, entry_id, entry_version_id,
            entry_content_hash, embedding_content_hash
        ) VALUES (?, ?, ?, ?, ?, ?, ?)`, scopeID, memoryRef.ID(), memoryRef.Revision(),
			entry.EntryID, entry.EntryVersionID, entry.EntryContentHash, *embeddingHash)
		if err != nil {
			return err
		}
		vectorID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx,
			"INSERT INTO pc_memory_entry_vec (rowid, embedding) VALUES (?, ?)", vectorID, packed,
		); err != nil {
			return err
		}
	}
	return nil
}

func (i *SQLiteMemoryVectorIndex) Search(
	ctx context.Context,
	db DBTX,
	scopeID string,
	request memory.SearchRequest,
) (channels memory.SearchChannels, returnErr error) {
	request = request.Clone()
	if request.Mode != memory.SearchVector && request.Mode != memory.SearchHybrid {
		return memory.SearchChannels{}, nil
	}
	if request.EmbeddingProfile == nil || *request.EmbeddingProfile != i.profile || len(request.QueryVector) == 0 {
		return memory.SearchChannels{}, &memory.CapabilityNotSupportedError{Capability: "embedding-profile"}
	}
	complete, err := i.VectorComplete(ctx, db, scopeID, request.Memories, i.profile)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	if !complete {
		return memory.SearchChannels{}, &memory.CapabilityNotSupportedError{Capability: "vector"}
	}
	queryVector, err := memory.ValidateEmbedding(request.QueryVector, i.profile.Dimension)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	packed, err := packSQLiteVector(queryVector)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	var total int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pc_memory_vector_entries").Scan(&total); err != nil {
		return memory.SearchChannels{}, err
	}
	if total == 0 || len(request.Memories) == 0 {
		return memory.SearchChannels{}, nil
	}
	memoryIDs := make([]string, len(request.Memories))
	requested := make(map[string]int64, len(request.Memories))
	for index, ref := range request.Memories {
		memoryIDs[index] = ref.ID()
		requested[ref.ID()] = ref.Revision()
	}
	encodedMemoryIDs, err := json.Marshal(memoryIDs)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	rows, err := db.QueryContext(ctx, `WITH nearest AS (
			SELECT rowid, distance
			FROM pc_memory_entry_vec
			WHERE embedding MATCH ? AND k = ?
		)
		SELECT m.memory_artifact_id, m.head_revision, m.entry_id, m.entry_version_id,
			   v.text, nearest.distance
		FROM nearest
		JOIN pc_memory_vector_entries AS m ON m.vector_id = nearest.rowid
        JOIN pc_memory_entry_versions AS v
          ON v.scope_id = m.scope_id
         AND v.memory_artifact_id = m.memory_artifact_id
         AND v.entry_version_id = m.entry_version_id
        WHERE m.scope_id = ?
          AND m.memory_artifact_id IN (SELECT value FROM json_each(?))
		ORDER BY nearest.distance,
				 m.memory_artifact_id, m.entry_id, m.entry_version_id
		LIMIT ?`, packed, total, scopeID, string(encodedMemoryIDs), request.CandidateLimit)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	hits := make([]memory.ChannelHit, 0)
	for rows.Next() {
		var artifactID, entryID, versionID, text string
		var revision, rawDistance any
		if err := rows.Scan(&artifactID, &revision, &entryID, &versionID, &text, &rawDistance); err != nil {
			return memory.SearchChannels{}, err
		}
		decodedRevision, ok := integer(revision)
		if !ok || requested[artifactID] != decodedRevision {
			continue
		}
		distance, err := sqliteVectorDistance(rawDistance)
		if err != nil {
			return memory.SearchChannels{}, err
		}
		ref, err := artifact.NewRef(memory.Family, artifactID, decodedRevision)
		if err != nil {
			return memory.SearchChannels{}, err
		}
		hit, err := memory.NewChannelHit(ref, entryID, versionID, text, &distance)
		if err != nil {
			return memory.SearchChannels{}, err
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return memory.SearchChannels{}, err
	}
	channels = memory.SearchChannels{Vector: hits}
	return channels, nil
}

func (i *SQLiteMemoryVectorIndex) VectorComplete(
	ctx context.Context,
	db DBTX,
	scopeID string,
	memories []artifact.Ref,
	profile memory.EmbeddingProfile,
) (bool, error) {
	if profile != i.profile {
		return false, nil
	}
	for _, memoryRef := range memories {
		expectedRows, err := db.QueryContext(ctx, `SELECT entry_id, entry_version_id, entry_content_hash
            FROM pc_memory_entry_heads
            WHERE scope_id = ? AND memory_artifact_id = ? AND head_revision = ?`,
			scopeID, memoryRef.ID(), memoryRef.Revision())
		if err != nil {
			return false, err
		}
		expected, err := scanSQLiteHeadIdentities(expectedRows)
		if err != nil {
			return false, err
		}
		metadataRows, err := db.QueryContext(ctx, `SELECT vector_id, entry_id, entry_version_id,
                entry_content_hash, embedding_content_hash
            FROM pc_memory_vector_entries
            WHERE scope_id = ? AND memory_artifact_id = ? AND head_revision = ?`,
			scopeID, memoryRef.ID(), memoryRef.Revision())
		if err != nil {
			return false, err
		}
		type metadataValue struct {
			vectorID      int64
			embeddingHash string
		}
		actual := make(map[sqliteHeadIdentity]metadataValue)
		for metadataRows.Next() {
			var identity sqliteHeadIdentity
			var value metadataValue
			if err := metadataRows.Scan(
				&value.vectorID, &identity.entryID, &identity.versionID,
				&identity.contentHash, &value.embeddingHash,
			); err != nil {
				return false, errors.Join(err, closeRows(metadataRows))
			}
			actual[identity] = value
		}
		if err := closeRows(metadataRows); err != nil {
			return false, err
		}
		if len(actual) != len(expected) {
			return false, nil
		}
		for identity := range expected {
			value, found := actual[identity]
			if !found {
				return false, nil
			}
			expectedHash, err := memory.EmbeddingContentHash(i.profile, identity.contentHash)
			if err != nil {
				return false, err
			}
			if value.embeddingHash != expectedHash {
				return false, nil
			}
			var packed any
			err = db.QueryRowContext(ctx,
				"SELECT embedding FROM pc_memory_entry_vec WHERE rowid = ?", value.vectorID,
			).Scan(&packed)
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

func (i *SQLiteMemoryVectorIndex) Hydrate(
	ctx context.Context,
	db DBTX,
	scopeID string,
	projections []memory.Projection,
) ([]memory.Projection, error) {
	result := make([]memory.Projection, 0, len(projections))
	for _, projection := range projections {
		entry := projection.EntryVersion()
		var vectorID int64
		var contentHash, embeddingHash string
		err := db.QueryRowContext(ctx, `SELECT vector_id, entry_content_hash, embedding_content_hash
            FROM pc_memory_vector_entries
            WHERE scope_id = ? AND memory_artifact_id = ? AND entry_id = ? AND entry_version_id = ?`,
			scopeID, entry.MemoryArtifactID, entry.EntryID, entry.EntryVersionID,
		).Scan(&vectorID, &contentHash, &embeddingHash)
		if errors.Is(err, sql.ErrNoRows) {
			result = append(result, projection)
			continue
		}
		if err != nil {
			return nil, err
		}
		expectedHash, err := memory.EmbeddingContentHash(i.profile, entry.EntryContentHash)
		if err != nil {
			return nil, err
		}
		if contentHash != entry.EntryContentHash || embeddingHash != expectedHash {
			result = append(result, projection)
			continue
		}
		var raw any
		err = db.QueryRowContext(ctx,
			"SELECT embedding FROM pc_memory_entry_vec WHERE rowid = ?", vectorID,
		).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			result = append(result, projection)
			continue
		}
		if err != nil {
			return nil, err
		}
		vector, err := unpackSQLiteVector(raw, i.profile.Dimension)
		if err != nil {
			return nil, err
		}
		hydrated, err := memory.NewProjection(entry, projection.SearchableText(), vector, &embeddingHash)
		if err != nil {
			return nil, err
		}
		result = append(result, hydrated)
	}
	return result, nil
}

type sqliteHeadIdentity struct{ entryID, versionID, contentHash string }

func scanSQLiteHeadIdentities(rows *sql.Rows) (map[sqliteHeadIdentity]struct{}, error) {
	result := make(map[sqliteHeadIdentity]struct{})
	for rows.Next() {
		var identity sqliteHeadIdentity
		if err := rows.Scan(&identity.entryID, &identity.versionID, &identity.contentHash); err != nil {
			return nil, errors.Join(err, closeRows(rows))
		}
		result[identity] = struct{}{}
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	return result, nil
}

func packSQLiteVector(vector []float64) ([]byte, error) {
	packed := make([]byte, len(vector)*4)
	for index, value := range vector {
		converted := float32(value)
		if math.IsNaN(float64(converted)) || math.IsInf(float64(converted), 0) {
			return nil, &memory.CapabilityNotSupportedError{
				Capability: "vector", Detail: "sqlite-vec vector cannot be represented as float32",
			}
		}
		binary.LittleEndian.PutUint32(packed[index*4:], math.Float32bits(converted))
	}
	return packed, nil
}

func unpackSQLiteVector(value any, dimension int) ([]float64, error) {
	packed, ok := value.([]byte)
	if !ok {
		return nil, sqliteVecCapabilityError("sqlite-vec returned an invalid vector")
	}
	if len(packed) != dimension*4 {
		return nil, sqliteVecCapabilityError("sqlite-vec returned the wrong vector dimension")
	}
	vector := make([]float64, dimension)
	for index := range vector {
		vector[index] = float64(math.Float32frombits(binary.LittleEndian.Uint32(packed[index*4:])))
	}
	return memory.ValidateEmbedding(vector, dimension)
}

func validateSQLiteVecVersion(value any) error {
	var version string
	switch typed := value.(type) {
	case string:
		version = typed
	case []byte:
		version = string(typed)
	case nil:
		version = ""
	default:
		version = fmt.Sprint(typed)
	}
	if version != requiredSQLiteVecVersion {
		return sqliteVecCapabilityError("sqlite-vec v0.1.9 is required")
	}
	return nil
}

func sqliteVectorDistance(value any) (float64, error) {
	var result float64
	switch typed := value.(type) {
	case float64:
		result = typed
	case float32:
		result = float64(typed)
	case int64:
		result = float64(typed)
	case []byte:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err != nil {
			return 0, sqliteVecCapabilityError("sqlite-vec returned an invalid distance")
		}
		result = parsed
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0, sqliteVecCapabilityError("sqlite-vec returned an invalid distance")
		}
		result = parsed
	default:
		return 0, sqliteVecCapabilityError("sqlite-vec returned an invalid distance")
	}
	if result < 0 || math.IsNaN(result) || math.IsInf(result, 0) {
		return 0, sqliteVecCapabilityError("sqlite-vec returned an invalid distance")
	}
	return result, nil
}

func sqliteVecCapabilityError(detail string) *memory.CapabilityNotSupportedError {
	return &memory.CapabilityNotSupportedError{Capability: "vector", Detail: detail}
}

var _ MemoryIndex = (*SQLiteMemoryVectorIndex)(nil)
