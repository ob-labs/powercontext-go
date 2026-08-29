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
	"fmt"
	"strings"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
)

// SQLiteMemoryFTSIndex implements the Python FTS5 active-head projection.
// Binaries using it must be built with mattn/go-sqlite3's sqlite_fts5 tag.
type SQLiteMemoryFTSIndex struct{}

func (SQLiteMemoryFTSIndex) Capabilities() memory.Capabilities {
	return memory.Capabilities{FTS: true}
}

func (SQLiteMemoryFTSIndex) Initialize(ctx context.Context, db DBTX) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS pc_memory_fts_index (
            singleton INTEGER NOT NULL,
            schema_version INTEGER NOT NULL,
            PRIMARY KEY (singleton),
            CONSTRAINT ck_pc_memory_fts_index_singleton CHECK (singleton = 1)
        )`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS pc_memory_entry_fts USING fts5(
            scope_id UNINDEXED,
            memory_artifact_id UNINDEXED,
            head_revision UNINDEXED,
            entry_id UNINDEXED,
            entry_version_id UNINDEXED,
            searchable_text,
            tokenize='unicode61'
        )`,
		`INSERT OR IGNORE INTO pc_memory_fts_index (singleton, schema_version) VALUES (1, 1)`,
		`DELETE FROM pc_memory_entry_fts`,
		`INSERT INTO pc_memory_entry_fts (
            scope_id, memory_artifact_id, head_revision, entry_id, entry_version_id, searchable_text
        ) SELECT scope_id, memory_artifact_id, head_revision, entry_id, entry_version_id, searchable_text
          FROM pc_memory_entry_heads`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return &memory.CapabilityNotSupportedError{Capability: "sqlite-fts", Detail: err.Error()}
		}
	}
	var probe any
	if err := db.QueryRowContext(ctx,
		`SELECT rowid FROM pc_memory_entry_fts WHERE pc_memory_entry_fts MATCH 'powercontext' LIMIT 1`,
	).Scan(&probe); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return &memory.CapabilityNotSupportedError{Capability: "sqlite-fts", Detail: err.Error()}
	}
	return nil
}

func (SQLiteMemoryFTSIndex) Replace(
	ctx context.Context,
	db DBTX,
	scopeID string,
	memoryRef artifact.Ref,
	projections []memory.Projection,
) error {
	if _, err := db.ExecContext(ctx,
		`DELETE FROM pc_memory_entry_fts WHERE scope_id = ? AND memory_artifact_id = ?`,
		scopeID, memoryRef.ID()); err != nil {
		return err
	}
	for _, projection := range projections {
		entry := projection.EntryVersion()
		if _, err := db.ExecContext(ctx, `INSERT INTO pc_memory_entry_fts (
            scope_id, memory_artifact_id, head_revision, entry_id, entry_version_id, searchable_text
        ) VALUES (?, ?, ?, ?, ?, ?)`, scopeID, memoryRef.ID(), memoryRef.Revision(),
			entry.EntryID, entry.EntryVersionID, projection.SearchableText()); err != nil {
			return err
		}
	}
	return nil
}

func (SQLiteMemoryFTSIndex) Search(
	ctx context.Context,
	db DBTX,
	scopeID string,
	request memory.SearchRequest,
) (channels memory.SearchChannels, returnErr error) {
	request = request.Clone()
	if request.Mode != memory.SearchFTS && request.Mode != memory.SearchHybrid {
		return memory.SearchChannels{}, nil
	}
	query, ok := memory.FTSMatchQuery(request.Query)
	if !ok {
		return memory.SearchChannels{}, nil
	}
	if len(request.Memories) == 0 {
		return memory.SearchChannels{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(request.Memories)), ",")
	statement := fmt.Sprintf(`SELECT f.memory_artifact_id, f.head_revision, f.entry_id,
            f.entry_version_id, v.text
        FROM pc_memory_entry_fts AS f
        JOIN pc_memory_entry_versions AS v
          ON v.scope_id = f.scope_id
         AND v.memory_artifact_id = f.memory_artifact_id
         AND v.entry_version_id = f.entry_version_id
        WHERE pc_memory_entry_fts MATCH ?
          AND f.scope_id = ?
          AND f.memory_artifact_id IN (%s)
        ORDER BY bm25(pc_memory_entry_fts), f.memory_artifact_id, f.entry_id, f.entry_version_id
        LIMIT ?`, placeholders)
	arguments := make([]any, 0, len(request.Memories)+3)
	arguments = append(arguments, query, scopeID)
	requested := make(map[string]int64, len(request.Memories))
	for _, ref := range request.Memories {
		arguments = append(arguments, ref.ID())
		requested[ref.ID()] = ref.Revision()
	}
	arguments = append(arguments, request.CandidateLimit)
	rows, err := db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	hits := make([]memory.ChannelHit, 0)
	for rows.Next() {
		var artifactID, entryID, versionID, text string
		var revision any
		if err := rows.Scan(&artifactID, &revision, &entryID, &versionID, &text); err != nil {
			return memory.SearchChannels{}, err
		}
		decodedRevision, ok := integer(revision)
		if !ok || requested[artifactID] != decodedRevision {
			continue
		}
		ref, err := artifact.NewRef(memory.Family, artifactID, decodedRevision)
		if err != nil {
			return memory.SearchChannels{}, err
		}
		hit, err := memory.NewChannelHit(ref, entryID, versionID, text, nil)
		if err != nil {
			return memory.SearchChannels{}, err
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return memory.SearchChannels{}, err
	}
	channels = memory.SearchChannels{FTS: hits}
	return channels, nil
}

func (SQLiteMemoryFTSIndex) VectorComplete(
	context.Context,
	DBTX,
	string,
	[]artifact.Ref,
	memory.EmbeddingProfile,
) (bool, error) {
	return false, nil
}

func (SQLiteMemoryFTSIndex) Hydrate(
	_ context.Context,
	_ DBTX,
	_ string,
	projections []memory.Projection,
) ([]memory.Projection, error) {
	return cloneMemoryProjections(projections), nil
}
