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

// Package oceanbase implements OceanBase-specific rebuildable indexes. The
// authoritative relational stores remain in sqlstore and use MySQLDialect.
package oceanbase

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

const (
	memoryFTSIndexName     = "ix_pc_memory_entry_heads_fts"
	experienceFTSIndexName = "ix_pc_artifact_heads_fts"
)

type MemoryFTSIndex struct{}

func (MemoryFTSIndex) Capabilities() memory.Capabilities { return memory.Capabilities{FTS: true} }

func (MemoryFTSIndex) Initialize(ctx context.Context, db sqlstore.DBTX) error {
	if err := ensureFullTextIndex(ctx, db, "pc_memory_entry_heads", memoryFTSIndexName, "searchable_text"); err != nil {
		return &memory.CapabilityNotSupportedError{Capability: "oceanbase-fts", Detail: err.Error()}
	}
	var probe any
	err := db.QueryRowContext(ctx, `SELECT entry_version_id FROM pc_memory_entry_heads
        WHERE MATCH(searchable_text) AGAINST ('powercontext') LIMIT 1`).Scan(&probe)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return &memory.CapabilityNotSupportedError{Capability: "oceanbase-fts", Detail: err.Error()}
	}
	return nil
}

func (MemoryFTSIndex) Replace(context.Context, sqlstore.DBTX, string, artifact.Ref, []memory.Projection) error {
	// pc_memory_entry_heads is itself the active-head projection indexed by
	// OceanBase, so no side table needs to be maintained.
	return nil
}

func (MemoryFTSIndex) Search(
	ctx context.Context,
	db sqlstore.DBTX,
	scopeID string,
	request memory.SearchRequest,
) (channels memory.SearchChannels, returnErr error) {
	request = request.Clone()
	if request.Mode != memory.SearchFTS && request.Mode != memory.SearchHybrid {
		return memory.SearchChannels{}, nil
	}
	if request.AnalyzedQuery == "" || len(request.Memories) == 0 {
		return memory.SearchChannels{}, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(request.Memories)), ",")
	statement := fmt.Sprintf(`SELECT h.memory_artifact_id, h.head_revision, h.entry_id,
            h.entry_version_id, v.text
        FROM pc_memory_entry_heads AS h
        JOIN pc_memory_entry_versions AS v
          ON v.scope_id = h.scope_id
         AND v.memory_artifact_id = h.memory_artifact_id
         AND v.entry_version_id = h.entry_version_id
        WHERE h.scope_id = ?
          AND h.memory_artifact_id IN (%s)
          AND MATCH(h.searchable_text) AGAINST (?)
        ORDER BY MATCH(h.searchable_text) AGAINST (?) DESC,
                 h.memory_artifact_id, h.entry_id, h.entry_version_id
        LIMIT ?`, placeholders)
	arguments := make([]any, 0, len(request.Memories)+4)
	arguments = append(arguments, scopeID)
	requested := make(map[string]int64, len(request.Memories))
	for _, ref := range request.Memories {
		arguments = append(arguments, ref.ID())
		requested[ref.ID()] = ref.Revision()
	}
	arguments = append(arguments, request.AnalyzedQuery, request.AnalyzedQuery, request.CandidateLimit)
	rows, err := db.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return memory.SearchChannels{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeRows(rows)) }()
	hits := make([]memory.ChannelHit, 0)
	for rows.Next() {
		var artifactID, entryID, versionID, text string
		var revision int64
		if err := rows.Scan(&artifactID, &revision, &entryID, &versionID, &text); err != nil {
			return memory.SearchChannels{}, err
		}
		if requested[artifactID] != revision {
			continue
		}
		ref, err := artifact.NewRef(memory.Family, artifactID, revision)
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

func (MemoryFTSIndex) VectorComplete(context.Context, sqlstore.DBTX, string, []artifact.Ref, memory.EmbeddingProfile) (bool, error) {
	return false, nil
}

func (MemoryFTSIndex) Hydrate(
	_ context.Context,
	_ sqlstore.DBTX,
	_ string,
	projections []memory.Projection,
) ([]memory.Projection, error) {
	result := make([]memory.Projection, len(projections))
	for index, projection := range projections {
		cloned, err := memory.NewProjection(
			projection.EntryVersion(), projection.SearchableText(), projection.Embedding(), projection.EmbeddingContentHash(),
		)
		if err != nil {
			return nil, err
		}
		result[index] = cloned
	}
	return result, nil
}

type ExperienceFTSIndex struct{}

func (ExperienceFTSIndex) Initialize(ctx context.Context, db sqlstore.DBTX) error {
	if err := sqlstore.EnsureArtifactHeadSearchableText(ctx, db, sqlstore.MySQLDialect); err != nil {
		return err
	}
	if err := sqlstore.RebuildExperienceProjections(ctx, db); err != nil {
		return err
	}
	if err := ensureFullTextIndex(ctx, db, "pc_artifact_heads", experienceFTSIndexName, "searchable_text"); err != nil {
		return &memory.CapabilityNotSupportedError{Capability: "oceanbase-experience-fts", Detail: err.Error()}
	}
	var probe any
	err := db.QueryRowContext(ctx, `SELECT artifact_id FROM pc_artifact_heads
        WHERE MATCH(searchable_text) AGAINST ('powercontext') LIMIT 1`).Scan(&probe)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return &memory.CapabilityNotSupportedError{Capability: "oceanbase-experience-fts", Detail: err.Error()}
	}
	return nil
}

func (ExperienceFTSIndex) Replace(ctx context.Context, db sqlstore.DBTX, scopeID string, value experience.Experience) error {
	return sqlstore.ReplaceExperienceProjection(ctx, db, scopeID, value)
}

func (ExperienceFTSIndex) Search(
	ctx context.Context,
	db sqlstore.DBTX,
	scopeID, query string,
	limit int,
) (hits []experience.SearchHit, returnErr error) {
	analyzed := memory.AnalyzeText(query)
	if analyzed == "" {
		return []experience.SearchHit{}, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT h.artifact_id, h.revision, a.content
        FROM pc_artifact_heads AS h
        JOIN pc_artifacts AS a
          ON a.scope_id = h.scope_id AND a.family = h.family
         AND a.artifact_id = h.artifact_id AND a.revision = h.revision
        WHERE h.scope_id = ? AND h.family = ?
          AND MATCH(h.searchable_text) AGAINST (?)
        ORDER BY MATCH(h.searchable_text) AGAINST (?) DESC, h.artifact_id, h.revision
        LIMIT ?`, scopeID, experience.Family, analyzed, analyzed, limit*4)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeRows(rows)) }()
	hits = make([]experience.SearchHit, 0, limit)
	for rows.Next() {
		var artifactID string
		var revision int64
		var content any
		if err := rows.Scan(&artifactID, &revision, &content); err != nil {
			return nil, err
		}
		value, err := sqlstore.DecodeExperienceStoredValue(content)
		if err != nil {
			return nil, err
		}
		if !memory.AdmitsFTSText(query, experience.SearchText(value)) {
			continue
		}
		ref, err := artifact.NewRef(experience.Family, artifactID, revision)
		if err != nil {
			return nil, err
		}
		hits = append(hits, experience.SearchHit{ArtifactRef: ref, Content: value})
		if len(hits) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return hits, nil
}

func ensureFullTextIndex(ctx context.Context, db sqlstore.DBTX, table, index, column string) error {
	var count int64
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics
        WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`, table, index).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	// Names are compile-time constants owned by this package, never input.
	_, err = db.ExecContext(ctx, fmt.Sprintf(
		"CREATE FULLTEXT INDEX %s ON %s (%s) WITH PARSER SPACE", index, table, column,
	))
	return err
}

var (
	_ sqlstore.MemoryIndex     = MemoryFTSIndex{}
	_ sqlstore.ExperienceIndex = ExperienceFTSIndex{}
)
