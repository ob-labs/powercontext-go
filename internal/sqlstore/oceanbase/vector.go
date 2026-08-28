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

package oceanbase

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

const (
	memoryVectorTableName = "pc_memory_vector_entries"
	memoryVectorIndexName = "ix_pc_memory_vector_entries_embedding"
)

// MemoryVectorIndex maintains the Python-compatible OceanBase VECTOR/HNSW
// active-head projection for one exact embedding profile.
type MemoryVectorIndex struct{ profile memory.EmbeddingProfile }

func NewMemoryVectorIndex(profile memory.EmbeddingProfile) (*MemoryVectorIndex, error) {
	if profile.Dimension < 1 || profile.Distance != "l2" || profile.Normalization != "unit" {
		return nil, &memory.CapabilityNotSupportedError{
			Capability: "vector", Detail: "OceanBase requires a positive unit-normalized L2 embedding profile",
		}
	}
	return &MemoryVectorIndex{profile: profile}, nil
}

func (i *MemoryVectorIndex) Capabilities() memory.Capabilities {
	profile := i.profile
	return memory.Capabilities{Vector: true, EmbeddingProfile: &profile}
}

func (i *MemoryVectorIndex) Initialize(ctx context.Context, db sqlstore.DBTX) error {
	statement := memoryVectorDDL(i.profile.Dimension)
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return &memory.CapabilityNotSupportedError{Capability: "oceanbase-vector", Detail: err.Error()}
	}
	var columnType sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT data_type FROM information_schema.columns
        WHERE table_schema = DATABASE() AND table_name = ? AND column_name = 'embedding'`,
		memoryVectorTableName).Scan(&columnType); err != nil {
		return &memory.CapabilityNotSupportedError{Capability: "oceanbase-vector", Detail: err.Error()}
	}
	expected := fmt.Sprintf("VECTOR(%d)", i.profile.Dimension)
	if !columnType.Valid || strings.ToUpper(columnType.String) != expected {
		return &memory.CapabilityNotSupportedError{
			Capability: "vector", Detail: fmt.Sprintf("OceanBase projection uses %q; expected %s", columnType.String, expected),
		}
	}
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics
        WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`,
		memoryVectorTableName, memoryVectorIndexName).Scan(&count); err != nil {
		return &memory.CapabilityNotSupportedError{Capability: "oceanbase-vector", Detail: err.Error()}
	}
	if count == 0 {
		if _, err := db.ExecContext(ctx, fmt.Sprintf(
			"CREATE VECTOR INDEX %s ON %s (embedding) WITH (distance=l2,type=hnsw)",
			memoryVectorIndexName, memoryVectorTableName,
		)); err != nil {
			return &memory.CapabilityNotSupportedError{Capability: "oceanbase-vector", Detail: err.Error()}
		}
	}
	return nil
}

func memoryVectorDDL(dimension int) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
        vector_id BIGINT NOT NULL AUTO_INCREMENT,
		scope_id %s NOT NULL,
		memory_artifact_id %s NOT NULL,
        head_revision BIGINT NOT NULL,
		entry_id %s NOT NULL,
		entry_version_id %s NOT NULL,
		entry_content_hash %s NOT NULL,
		embedding_content_hash %s NOT NULL,
        embedding VECTOR(%d) NOT NULL,
        PRIMARY KEY (vector_id),
        CONSTRAINT uq_pc_memory_vector_entries_head UNIQUE (scope_id, memory_artifact_id, entry_id)
	)`, memoryVectorTableName,
		sqlstore.MySQLIdentityType(256), sqlstore.MySQLIdentityType(128),
		sqlstore.MySQLIdentityType(128), sqlstore.MySQLIdentityType(128),
		sqlstore.MySQLIdentityType(64), sqlstore.MySQLIdentityType(64),
		dimension)
}

func (i *MemoryVectorIndex) Replace(
	ctx context.Context,
	db sqlstore.DBTX,
	scopeID string,
	memoryRef artifact.Ref,
	projections []memory.Projection,
) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM pc_memory_vector_entries
        WHERE scope_id = ? AND memory_artifact_id = ?`, scopeID, memoryRef.ID()); err != nil {
		return err
	}
	for _, projection := range projections {
		hash := projection.EmbeddingContentHash()
		if len(projection.Embedding()) == 0 || hash == nil {
			continue
		}
		vector, err := memory.ValidateEmbedding(projection.Embedding(), i.profile.Dimension)
		if err != nil {
			return err
		}
		encoded, err := encodeOceanBaseVector(vector)
		if err != nil {
			return err
		}
		entry := projection.EntryVersion()
		_, err = db.ExecContext(ctx, `INSERT INTO pc_memory_vector_entries (
            scope_id, memory_artifact_id, head_revision, entry_id, entry_version_id,
            entry_content_hash, embedding_content_hash, embedding
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, scopeID, memoryRef.ID(), memoryRef.Revision(),
			entry.EntryID, entry.EntryVersionID, entry.EntryContentHash, *hash, encoded)
		if err != nil {
			return err
		}
	}
	return nil
}

func (i *MemoryVectorIndex) Search(
	ctx context.Context,
	db sqlstore.DBTX,
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
	if len(request.Memories) == 0 {
		return memory.SearchChannels{}, nil
	}
	vector, err := memory.ValidateEmbedding(request.QueryVector, i.profile.Dimension)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	encoded, err := encodeOceanBaseVector(vector)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(request.Memories)), ",")
	statement := fmt.Sprintf(`SELECT m.memory_artifact_id, m.head_revision, m.entry_id,
            m.entry_version_id, v.text, l2_distance(m.embedding, ?) AS distance
        FROM pc_memory_vector_entries AS m
        JOIN pc_memory_entry_versions AS v
          ON v.scope_id = m.scope_id
         AND v.memory_artifact_id = m.memory_artifact_id
         AND v.entry_version_id = m.entry_version_id
        WHERE m.scope_id = ? AND m.memory_artifact_id IN (%s)
        ORDER BY l2_distance(m.embedding, ?) APPROXIMATE,
                 m.memory_artifact_id, m.entry_id, m.entry_version_id
        LIMIT ?`, placeholders)
	arguments := make([]any, 0, len(request.Memories)+4)
	arguments = append(arguments, encoded, scopeID)
	requested := make(map[string]int64, len(request.Memories))
	for _, ref := range request.Memories {
		arguments = append(arguments, ref.ID())
		requested[ref.ID()] = ref.Revision()
	}
	arguments = append(arguments, encoded, request.CandidateLimit)
	rows, err := db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeRows(rows)) }()
	hits := make([]memory.ChannelHit, 0)
	for rows.Next() {
		var artifactID, entryID, versionID, text string
		var revision int64
		var rawDistance any
		if err := rows.Scan(&artifactID, &revision, &entryID, &versionID, &text, &rawDistance); err != nil {
			return memory.SearchChannels{}, err
		}
		if requested[artifactID] != revision {
			continue
		}
		distance, err := numericFloat64(rawDistance)
		if err != nil {
			return memory.SearchChannels{}, err
		}
		ref, err := artifact.NewRef(memory.Family, artifactID, revision)
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

func (i *MemoryVectorIndex) VectorComplete(
	ctx context.Context,
	db sqlstore.DBTX,
	scopeID string,
	memories []artifact.Ref,
	profile memory.EmbeddingProfile,
) (bool, error) {
	if profile != i.profile {
		return false, nil
	}
	for _, memoryRef := range memories {
		expectedRows, err := db.QueryContext(ctx, `SELECT entry_id, entry_version_id, entry_content_hash
            FROM pc_memory_entry_heads WHERE scope_id = ? AND memory_artifact_id = ? AND head_revision = ?`,
			scopeID, memoryRef.ID(), memoryRef.Revision())
		if err != nil {
			return false, err
		}
		expected, err := scanHeadIdentitySet(expectedRows)
		if err != nil {
			return false, err
		}
		actualRows, err := db.QueryContext(ctx, `SELECT entry_id, entry_version_id,
                entry_content_hash, embedding_content_hash
            FROM pc_memory_vector_entries
            WHERE scope_id = ? AND memory_artifact_id = ? AND head_revision = ?`,
			scopeID, memoryRef.ID(), memoryRef.Revision())
		if err != nil {
			return false, err
		}
		actual := make(map[headIdentity]string)
		for actualRows.Next() {
			var identity headIdentity
			var embeddingHash string
			if err := actualRows.Scan(&identity.entryID, &identity.versionID, &identity.contentHash, &embeddingHash); err != nil {
				return false, errors.Join(err, closeRows(actualRows))
			}
			actual[identity] = embeddingHash
		}
		if err := closeRows(actualRows); err != nil {
			return false, err
		}
		if len(actual) != len(expected) {
			return false, nil
		}
		for identity := range expected {
			hash, found := actual[identity]
			if !found {
				return false, nil
			}
			expectedHash, err := memory.EmbeddingContentHash(i.profile, identity.contentHash)
			if err != nil {
				return false, err
			}
			if hash != expectedHash {
				return false, nil
			}
		}
	}
	return true, nil
}

func (i *MemoryVectorIndex) Hydrate(
	ctx context.Context,
	db sqlstore.DBTX,
	scopeID string,
	projections []memory.Projection,
) ([]memory.Projection, error) {
	result := make([]memory.Projection, 0, len(projections))
	for _, projection := range projections {
		entry := projection.EntryVersion()
		var contentHash, embeddingHash string
		var rawEmbedding any
		err := db.QueryRowContext(ctx, `SELECT entry_content_hash, embedding_content_hash, embedding
            FROM pc_memory_vector_entries
            WHERE scope_id = ? AND memory_artifact_id = ? AND entry_id = ? AND entry_version_id = ?`,
			scopeID, entry.MemoryArtifactID, entry.EntryID, entry.EntryVersionID,
		).Scan(&contentHash, &embeddingHash, &rawEmbedding)
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
		vector, err := decodeOceanBaseVector(rawEmbedding, i.profile.Dimension)
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

type headIdentity struct{ entryID, versionID, contentHash string }

func scanHeadIdentitySet(rows *sql.Rows) (map[headIdentity]struct{}, error) {
	result := make(map[headIdentity]struct{})
	for rows.Next() {
		var identity headIdentity
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

func closeRows(rows *sql.Rows) error {
	closeErr := rows.Close()
	return errors.Join(rows.Err(), closeErr)
}

func encodeOceanBaseVector(vector []float64) (string, error) {
	encoded, err := json.Marshal(vector)
	if err != nil {
		return "", &memory.CapabilityNotSupportedError{Capability: "vector", Detail: "vector could not be encoded"}
	}
	return string(encoded), nil
}

func decodeOceanBaseVector(value any, dimension int) ([]float64, error) {
	var encoded []byte
	switch typed := value.(type) {
	case string:
		encoded = []byte(typed)
	case []byte:
		encoded = typed
	default:
		return nil, &memory.CapabilityNotSupportedError{Capability: "vector", Detail: "OceanBase returned an invalid vector"}
	}
	var vector []float64
	if err := json.Unmarshal(encoded, &vector); err != nil {
		return nil, &memory.CapabilityNotSupportedError{Capability: "vector", Detail: "OceanBase returned an invalid vector"}
	}
	return memory.ValidateEmbedding(vector, dimension)
}

func numericFloat64(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		return typed, nil
	case float32:
		return float64(typed), nil
	case int64:
		return float64(typed), nil
	case []byte:
		value, err := strconv.ParseFloat(string(typed), 64)
		if err == nil {
			return value, nil
		}
	case string:
		value, err := strconv.ParseFloat(typed, 64)
		if err == nil {
			return value, nil
		}
	}
	return 0, &memory.CapabilityNotSupportedError{Capability: "vector", Detail: "OceanBase returned an invalid distance"}
}

var _ sqlstore.MemoryIndex = (*MemoryVectorIndex)(nil)
