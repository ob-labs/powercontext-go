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
	"errors"
	"reflect"
	"slices"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
)

// MemoryRepository composes shared Artifact revisions with Memory-owned entry
// and rebuildable search projections for one scope.
type MemoryRepository struct {
	database  *Database
	scopeID   string
	artifacts *ArtifactRepository
	index     MemoryIndex
}

func NewMemoryRepository(
	database *Database,
	scopeID string,
	artifacts *ArtifactRepository,
	index MemoryIndex,
) (*MemoryRepository, error) {
	if database == nil || artifacts == nil {
		return nil, errors.New("sqlstore: Memory database and Artifact repository must not be nil")
	}
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if index == nil {
		index = NoMemoryIndex{}
	}
	return &MemoryRepository{database: database, scopeID: scopeID, artifacts: artifacts, index: index}, nil
}

func (r *MemoryRepository) Initialize(ctx context.Context) error {
	return r.database.Transaction(ctx, func(tx DBTX) error { return r.index.Initialize(ctx, tx) })
}

func (r *MemoryRepository) Capabilities() memory.Capabilities {
	value := r.index.Capabilities()
	if value.EmbeddingProfile != nil {
		profile := *value.EmbeddingProfile
		value.EmbeddingProfile = &profile
	}
	return value
}

func (r *MemoryRepository) Get(ctx context.Context, ref artifact.Ref) (memory.Memory, error) {
	var result memory.Memory
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var getErr error
		result, getErr = r.get(ctx, tx, ref)
		return getErr
	})
	return result, err
}

func (r *MemoryRepository) Latest(ctx context.Context, artifactID string) (memory.Memory, error) {
	var result memory.Memory
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var latestErr error
		result, latestErr = r.latest(ctx, tx, artifactID)
		return latestErr
	})
	return result, err
}

func (r *MemoryRepository) Entries(ctx context.Context, ref artifact.Ref) ([]memory.EntryVersion, error) {
	var result []memory.EntryVersion
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		canonical, err := r.get(ctx, tx, ref)
		if err != nil {
			return err
		}
		result, err = r.entries(ctx, tx, canonical)
		return err
	})
	return result, err
}

func (r *MemoryRepository) Projections(ctx context.Context, ref artifact.Ref) ([]memory.Projection, error) {
	var result []memory.Projection
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		canonical, err := r.get(ctx, tx, ref)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT
            v.memory_artifact_id, v.entry_id, v.entry_version_id, v.version,
            v.previous_version_id, v.kind, v.text, v.source_refs, v.artifact_refs,
            v.entry_content_hash, v.created_in_revision, h.searchable_text
            FROM pc_memory_entry_heads AS h
            JOIN pc_memory_entry_versions AS v
              ON v.scope_id = h.scope_id
             AND v.memory_artifact_id = h.memory_artifact_id
             AND v.entry_version_id = h.entry_version_id
            WHERE h.scope_id = ? AND h.memory_artifact_id = ? AND h.head_revision = ?
            ORDER BY h.entry_id`, r.scopeID, ref.ID(), ref.Revision())
		if err != nil {
			return err
		}
		projections := make([]memory.Projection, 0)
		for rows.Next() {
			var entry memory.EntryVersion
			var searchable string
			entry, err = scanMemoryEntryWithSearchable(rows, &searchable)
			if err != nil {
				return errors.Join(err, closeRows(rows))
			}
			projection, projectionErr := memory.NewProjection(entry, searchable, nil, nil)
			if projectionErr != nil {
				return errors.Join(projectionErr, closeRows(rows))
			}
			projections = append(projections, projection)
		}
		if closeErr := rows.Close(); closeErr != nil {
			return closeErr
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			return rowsErr
		}
		projections, err = r.index.Hydrate(ctx, tx, r.scopeID, projections)
		if err != nil {
			return err
		}
		active := make(map[string]struct{})
		for _, item := range canonical.Content().Manifest().Entries() {
			if item.State() == memory.Active {
				active[item.EntryVersionID()] = struct{}{}
			}
		}
		projected := make(map[string]struct{}, len(projections))
		for _, projection := range projections {
			projected[projection.EntryVersion().EntryVersionID] = struct{}{}
		}
		if !reflect.DeepEqual(active, projected) {
			return &memory.InvalidCitationError{Code: "projection-version"}
		}
		result = projections
		return nil
	})
	return result, err
}

func (r *MemoryRepository) Commit(ctx context.Context, value memory.Commit) (memory.Memory, error) {
	var committed memory.Memory
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var commitErr error
		committed, commitErr = r.commit(ctx, tx, value)
		return commitErr
	})
	return committed, err
}

// commit persists one already-prepared Memory revision using the caller's
// transaction. It is deliberately private: ordinary callers use Commit,
// while transaction-coupled use cases such as Source-window Flush can append
// their own CAS write before the transaction is committed.
func (r *MemoryRepository) commit(ctx context.Context, tx DBTX, value memory.Commit) (memory.Memory, error) {
	if err := validateMemoryCommit(value); err != nil {
		return memory.Memory{}, err
	}
	next := value.Memory()
	lineage := next.Lineage()
	draft, err := memory.NewDraft(next.Content(), lineage.Sources(), lineage.Artifacts())
	if err != nil {
		return memory.Memory{}, err
	}
	var stored artifact.Snapshot
	base := value.Base()
	if base == nil {
		stored, err = r.artifacts.Create(ctx, tx, r.scopeID, next.ID(), draft)
	} else {
		stored, err = r.artifacts.Revise(ctx, tx, r.scopeID, *base, draft)
	}
	if err != nil {
		return memory.Memory{}, err
	}
	decoded, ok := stored.(artifact.Artifact[memory.Content])
	if !ok || !reflect.DeepEqual(decoded, next) {
		return memory.Memory{}, &memory.BackendConfigurationError{Detail: "invalid relational Memory commit: generic Artifact result differs from prepared revision"}
	}
	for _, entry := range value.EntryVersions() {
		if err := insertMemoryEntry(ctx, tx, r.scopeID, entry); err != nil {
			return memory.Memory{}, err
		}
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM pc_memory_entry_heads WHERE scope_id = ? AND memory_artifact_id = ?",
		r.scopeID, next.ID()); err != nil {
		return memory.Memory{}, err
	}
	for _, projection := range value.Projections() {
		entry := projection.EntryVersion()
		if _, err := tx.ExecContext(ctx, `INSERT INTO pc_memory_entry_heads (
            scope_id, family, memory_artifact_id, head_revision, entry_id,
            entry_version_id, entry_content_hash, searchable_text
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.scopeID, memory.Family, next.ID(), next.Revision(),
			entry.EntryID, entry.EntryVersionID, entry.EntryContentHash, projection.SearchableText()); err != nil {
			return memory.Memory{}, err
		}
	}
	if err := r.index.Replace(ctx, tx, r.scopeID, next.Ref(), value.Projections()); err != nil {
		return memory.Memory{}, err
	}
	return decoded, nil
}

// applyPlan provides the exact validation performed by memory.Service.Apply,
// but uses a caller-owned transaction so a trigger cursor can participate in
// the same commit boundary.
func (r *MemoryRepository) applyPlan(ctx context.Context, tx DBTX, plan memory.WritePlan) (*memory.Memory, error) {
	commit := plan.Commit()
	if commit == nil {
		return plan.Result(), nil
	}
	committed, err := r.commit(ctx, tx, *commit)
	if err != nil {
		return nil, err
	}
	result := plan.Result()
	if result == nil || !reflect.DeepEqual(*result, committed) {
		return nil, &memory.InvalidCitationError{Code: "memory-mismatch"}
	}
	return &committed, nil
}

func (r *MemoryRepository) Changes(
	ctx context.Context,
	ref artifact.Ref,
	sinceRevision *int64,
) ([]memory.RevisionChanges, error) {
	var result []memory.RevisionChanges
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		target, err := r.get(ctx, tx, ref)
		if err != nil {
			return err
		}
		lower := target.Revision() - 1
		if sinceRevision != nil {
			lower = *sinceRevision
		}
		revisions, err := r.artifacts.Revisions(ctx, tx, r.scopeID, memory.Family, ref.ID())
		if err != nil {
			return err
		}
		result = make([]memory.RevisionChanges, 0)
		for _, revision := range revisions {
			if revision.Ref().Revision() <= lower || revision.Ref().Revision() > target.Revision() {
				continue
			}
			value, ok := revision.(artifact.Artifact[memory.Content])
			if !ok {
				return &memory.BackendConfigurationError{Detail: "stored memory family decoded to an unexpected content type"}
			}
			result = append(result, memory.RevisionChanges{MemoryRef: value.Ref(), Changes: value.Content().Changes()})
		}
		return nil
	})
	return result, err
}

func (r *MemoryRepository) VectorComplete(
	ctx context.Context,
	refs []artifact.Ref,
	profile memory.EmbeddingProfile,
) (bool, error) {
	var complete bool
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		var checkErr error
		complete, checkErr = r.index.VectorComplete(ctx, tx, r.scopeID, slices.Clone(refs), profile)
		return checkErr
	})
	return complete, err
}

func (r *MemoryRepository) Search(ctx context.Context, request memory.SearchRequest) (memory.SearchChannels, error) {
	var channels memory.SearchChannels
	request = request.Clone()
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		if err := r.validateSearchHeads(ctx, tx, request.Memories); err != nil {
			return err
		}
		var err error
		channels, err = r.index.Search(ctx, tx, r.scopeID, request)
		if err != nil {
			return err
		}
		return r.validateSearchHeads(ctx, tx, request.Memories)
	})
	return channels, err
}

// validateSearchHeads surrounds projection reads so a backend never treats a
// stale requested revision as the current active-head projection. Calling it
// both before and after Search is deliberate: OceanBase READ COMMITTED can
// observe a concurrent head advance between the two checks, while SQLite
// still returns one internally consistent snapshot.
func (r *MemoryRepository) validateSearchHeads(ctx context.Context, tx DBTX, refs []artifact.Ref) error {
	for _, ref := range refs {
		exact, err := r.get(ctx, tx, ref)
		if err != nil {
			return err
		}
		latest, err := r.latest(ctx, tx, ref.ID())
		if err != nil {
			return err
		}
		if exact.Ref() != latest.Ref() {
			return &memory.InvalidCitationError{Code: "memory-mismatch"}
		}
	}
	return nil
}

func (r *MemoryRepository) Expand(ctx context.Context, hits []memory.Hit) ([]memory.EntryVersion, error) {
	var result []memory.EntryVersion
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		result = make([]memory.EntryVersion, 0, len(hits))
		for _, hit := range hits {
			entry, found, err := findMemoryEntry(
				ctx, tx, r.scopeID, hit.MemoryRef.ID(), hit.EntryID, hit.EntryVersionID,
			)
			if err != nil {
				return err
			}
			if !found {
				return &memory.InvalidCitationError{Code: "expand-anchor"}
			}
			result = append(result, entry)
		}
		return nil
	})
	return result, err
}

func (r *MemoryRepository) get(ctx context.Context, tx DBTX, ref artifact.Ref) (memory.Memory, error) {
	if ref.Family() != memory.Family {
		return memory.Memory{}, &artifact.NotFoundError{Ref: ref}
	}
	value, err := r.artifacts.Get(ctx, tx, r.scopeID, ref)
	if err != nil {
		var missing *RepositoryNotFoundError
		if errors.As(err, &missing) {
			return memory.Memory{}, &artifact.NotFoundError{Ref: ref}
		}
		return memory.Memory{}, err
	}
	decoded, ok := value.(artifact.Artifact[memory.Content])
	if !ok {
		return memory.Memory{}, &memory.BackendConfigurationError{Detail: "stored memory family decoded to an unexpected content type"}
	}
	return decoded, nil
}

func (r *MemoryRepository) latest(ctx context.Context, tx DBTX, artifactID string) (memory.Memory, error) {
	value, err := r.artifacts.Latest(ctx, tx, r.scopeID, memory.Family, artifactID)
	if err != nil {
		var missing *RepositoryNotFoundError
		if errors.As(err, &missing) {
			requested, refErr := artifact.NewRef(memory.Family, artifactID, 1)
			if refErr != nil {
				return memory.Memory{}, refErr
			}
			return memory.Memory{}, &artifact.NotFoundError{Ref: requested}
		}
		return memory.Memory{}, err
	}
	decoded, ok := value.(artifact.Artifact[memory.Content])
	if !ok {
		return memory.Memory{}, &memory.BackendConfigurationError{Detail: "stored memory family decoded to an unexpected content type"}
	}
	return decoded, nil
}

func (r *MemoryRepository) entries(
	ctx context.Context,
	tx DBTX,
	canonical memory.Memory,
) ([]memory.EntryVersion, error) {
	manifest := canonical.Content().Manifest().Entries()
	if len(manifest) == 0 {
		return []memory.EntryVersion{}, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT memory_artifact_id, entry_id, entry_version_id, version,
        previous_version_id, kind, text, source_refs, artifact_refs,
        entry_content_hash, created_in_revision
        FROM pc_memory_entry_versions WHERE scope_id = ? AND memory_artifact_id = ?`,
		r.scopeID, canonical.ID())
	if err != nil {
		return nil, err
	}
	byID := make(map[string]memory.EntryVersion)
	for rows.Next() {
		entry, err := scanMemoryEntry(rows)
		if err != nil {
			return nil, errors.Join(err, closeRows(rows))
		}
		byID[entry.EntryVersionID] = entry
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]memory.EntryVersion, len(manifest))
	for index, item := range manifest {
		entry, ok := byID[item.EntryVersionID()]
		if !ok {
			return nil, &memory.InvalidCitationError{Code: "missing-version"}
		}
		result[index] = entry
	}
	return result, nil
}

func validateMemoryCommit(value memory.Commit) error {
	next := value.Memory()
	if next.Family() != memory.Family {
		return &memory.BackendConfigurationError{Detail: "invalid relational Memory commit: family is not memory"}
	}
	hash, err := memory.ContentHash(next.Content())
	if err != nil {
		return err
	}
	if hash != value.ContentHash() {
		return &memory.BackendConfigurationError{Detail: "invalid relational Memory commit: content hash does not match canonical content"}
	}
	base := value.Base()
	expectedRevision := int64(1)
	if base != nil {
		expectedRevision = base.Revision() + 1
		if base.ID() != next.ID() {
			return &memory.BackendConfigurationError{Detail: "invalid relational Memory commit: base and revision identities differ"}
		}
	}
	if next.Revision() != expectedRevision {
		return &memory.BackendConfigurationError{Detail: "invalid relational Memory commit: revision is not the next prepared revision"}
	}
	active := make(map[string]struct{})
	for _, item := range next.Content().Manifest().Entries() {
		if item.State() == memory.Active {
			active[item.EntryVersionID()] = struct{}{}
		}
	}
	projected := make(map[string]struct{})
	for _, projection := range value.Projections() {
		projected[projection.EntryVersion().EntryVersionID] = struct{}{}
	}
	if !reflect.DeepEqual(active, projected) {
		return &memory.BackendConfigurationError{Detail: "invalid relational Memory commit: active manifest and projections differ"}
	}
	return nil
}

func scanMemoryEntryWithSearchable(value scanner, searchable *string) (memory.EntryVersion, error) {
	var memoryID, entryID, versionID, kind, text, contentHash string
	var version, previous, sourcePayload, artifactPayload, revision any
	if err := value.Scan(&memoryID, &entryID, &versionID, &version, &previous, &kind, &text,
		&sourcePayload, &artifactPayload, &contentHash, &revision, searchable); err != nil {
		return memory.EntryVersion{}, err
	}
	return decodeMemoryEntryColumns(
		memoryID, entryID, versionID, kind, text, contentHash,
		version, previous, sourcePayload, artifactPayload, revision,
	)
}
