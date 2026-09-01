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
	"encoding/hex"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
)

func TestSQLiteVecProfileContract(t *testing.T) {
	t.Parallel()
	valid, err := memory.NewEmbeddingProfile("profile", "model", 3, "unit")
	if err != nil {
		t.Fatal(err)
	}
	index, err := NewSQLiteMemoryVectorIndex(valid)
	if err != nil {
		t.Fatal(err)
	}
	capabilities := index.Capabilities()
	if !capabilities.Vector || capabilities.FTS || capabilities.Hybrid ||
		capabilities.EmbeddingProfile == nil || *capabilities.EmbeddingProfile != valid {
		t.Fatalf("capabilities = %#v", capabilities)
	}

	notUnit, err := memory.NewEmbeddingProfile("profile", "model", 3, "none")
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewSQLiteMemoryVectorIndex(notUnit)
	var unsupported *memory.CapabilityNotSupportedError
	if !errors.As(err, &unsupported) || unsupported.Capability != "vector" {
		t.Fatalf("profile error = %v", err)
	}
}

func TestSQLiteVecVersionCompatibility(t *testing.T) {
	t.Parallel()
	for _, value := range []any{requiredSQLiteVecVersion, []byte(requiredSQLiteVecVersion)} {
		if err := validateSQLiteVecVersion(value); err != nil {
			t.Errorf("validateSQLiteVecVersion(%q): %v", value, err)
		}
	}
	for _, value := range []any{nil, "0.1.9", "v0.1.8", "v0.2.0"} {
		err := validateSQLiteVecVersion(value)
		var unsupported *memory.CapabilityNotSupportedError
		if !errors.As(err, &unsupported) || unsupported.Detail != "sqlite-vec v0.1.9 is required" {
			t.Errorf("validateSQLiteVecVersion(%q) error = %v", value, err)
		}
	}
}

func TestSQLiteVecNativeFloat32Codec(t *testing.T) {
	t.Parallel()
	packed, err := packSQLiteVector([]float64{1, -2.5, 0.125})
	if err != nil {
		t.Fatal(err)
	}
	// Linux/macOS amd64 and arm64 use the same little-endian bytes as Python's
	// struct.pack("=3f", ...), which is the Python sqlite-vec contract on every
	// supported little-endian release target.
	if got := hex.EncodeToString(packed); got != "0000803f000020c00000003e" {
		t.Fatalf("packed vector = %s", got)
	}
	decoded, err := unpackSQLiteVector(packed, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, []float64{1, -2.5, 0.125}) {
		t.Fatalf("decoded vector = %#v", decoded)
	}
	if _, err := unpackSQLiteVector(packed, 2); err == nil {
		t.Fatal("wrong vector dimension was accepted")
	}
	if _, err := unpackSQLiteVector("not-a-blob", 3); err == nil {
		t.Fatal("non-blob vector was accepted")
	}
	if _, err := packSQLiteVector([]float64{math.MaxFloat64}); err == nil {
		t.Fatal("float32 overflow was accepted")
	}
}

func TestSQLiteVecDistanceValidation(t *testing.T) {
	t.Parallel()
	for _, value := range []any{float64(1.25), float32(1.25), int64(1), []byte("1.25"), "1.25"} {
		got, err := sqliteVectorDistance(value)
		if err != nil || got != 1.25 && got != 1 {
			t.Errorf("sqliteVectorDistance(%T(%v)) = %v, %v", value, value, got, err)
		}
	}
	for _, value := range []any{-1.0, math.NaN(), math.Inf(1), "NaN", struct{}{}} {
		if _, err := sqliteVectorDistance(value); err == nil {
			t.Errorf("sqliteVectorDistance(%T(%v)) accepted", value, value)
		}
	}
}

func TestSQLiteVecEmbeddedExtensionAndVec0Schema(t *testing.T) {
	ctx := context.Background()
	config := DefaultSQLiteConfig(":memory:")
	database, err := OpenSQLite(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(context.Background()); closeErr != nil {
			t.Errorf("close database: %v", closeErr)
		}
	})
	profile, err := memory.NewEmbeddingProfile("profile", "model", 3, "unit")
	if err != nil {
		t.Fatal(err)
	}
	index, err := NewSQLiteMemoryVectorIndex(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(ctx, func(tx DBTX) error {
		var version string
		if err := tx.QueryRowContext(ctx, "SELECT vec_version()").Scan(&version); err != nil {
			return err
		}
		if version != requiredSQLiteVecVersion {
			t.Fatalf("vec_version() = %q", version)
		}
		if err := index.Initialize(ctx, tx); err != nil {
			return err
		}
		var schema string
		if err := tx.QueryRowContext(ctx,
			"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'pc_memory_entry_vec'",
		).Scan(&schema); err != nil {
			return err
		}
		if schema != "CREATE VIRTUAL TABLE pc_memory_entry_vec USING vec0(embedding float[3])" {
			t.Fatalf("vec0 schema = %q", schema)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteVec0ReplaceHydrateAndSearch(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, DefaultSQLiteConfig(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(context.Background()); closeErr != nil {
			t.Errorf("close database: %v", closeErr)
		}
	})
	profile, err := memory.NewEmbeddingProfile("profile", "model", 3, "unit")
	if err != nil {
		t.Fatal(err)
	}
	index, err := NewSQLiteMemoryVectorIndex(profile)
	if err != nil {
		t.Fatal(err)
	}
	memoryRef, err := artifact.NewRef(memory.Family, "memory-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	const scopeID = "scope"
	entries := []memory.EntryVersion{
		{
			MemoryArtifactID: memoryRef.ID(), EntryID: "entry-a", EntryVersionID: "version-a", Version: 1,
			Kind: "fact", Text: "nearest", EntryContentHash: strings.Repeat("a", 64), CreatedInRevision: 1,
		},
		{
			MemoryArtifactID: memoryRef.ID(), EntryID: "entry-b", EntryVersionID: "version-b", Version: 1,
			Kind: "fact", Text: "farther", EntryContentHash: strings.Repeat("b", 64), CreatedInRevision: 1,
		},
	}
	vectors := [][]float64{{1, 0, 0}, {0, 1, 0}}
	projections := make([]memory.Projection, len(entries))
	for position := range entries {
		embeddingHash, hashErr := memory.EmbeddingContentHash(profile, entries[position].EntryContentHash)
		if hashErr != nil {
			t.Fatal(hashErr)
		}
		projections[position], err = memory.NewProjection(
			entries[position], entries[position].Text, vectors[position], &embeddingHash,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Transaction(ctx, func(tx DBTX) error {
		if err := index.Initialize(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO pc_artifacts (scope_id, family, artifact_id, revision, content) VALUES (?, ?, ?, ?, ?)",
			scopeID, memory.Family, memoryRef.ID(), memoryRef.Revision(), []byte("{}"),
		); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := insertMemoryEntry(ctx, tx, scopeID, entry); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO pc_memory_entry_heads (
                scope_id, family, memory_artifact_id, head_revision, entry_id,
                entry_version_id, entry_content_hash, searchable_text
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, scopeID, memory.Family, memoryRef.ID(), 1,
				entry.EntryID, entry.EntryVersionID, entry.EntryContentHash, entry.Text); err != nil {
				return err
			}
		}
		return index.Replace(ctx, tx, scopeID, memoryRef, projections)
	}); err != nil {
		t.Fatal(err)
	}
	request := memory.SearchRequest{
		Memories: []artifact.Ref{memoryRef}, CandidateLimit: 10, Mode: memory.SearchVector,
		QueryVector: []float64{1, 0, 0}, EmbeddingProfile: &profile,
	}
	if err := database.Transaction(ctx, func(tx DBTX) error {
		complete, err := index.VectorComplete(ctx, tx, scopeID, request.Memories, profile)
		if err != nil {
			return err
		}
		if !complete {
			t.Fatal("vector projection is incomplete")
		}
		hydrated, err := index.Hydrate(ctx, tx, scopeID, projections)
		if err != nil {
			return err
		}
		if got := hydrated[0].Embedding(); !reflect.DeepEqual(got, vectors[0]) {
			t.Fatalf("hydrated embedding = %#v", got)
		}
		channels, err := index.Search(ctx, tx, scopeID, request)
		if err != nil {
			return err
		}
		if len(channels.Vector) != 2 || channels.Vector[0].EntryID != "entry-a" ||
			channels.Vector[1].EntryID != "entry-b" || channels.Vector[0].Distance == nil ||
			*channels.Vector[0].Distance != 0 {
			t.Fatalf("vector hits = %#v", channels.Vector)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteVecInitializeRejectsProjectionDimensionMismatch(t *testing.T) {
	database := openSQLiteVecTestDatabase(t)
	initializeSQLiteVecTestIndex(t, database, 3)

	err := database.Transaction(t.Context(), func(tx DBTX) error {
		return newSQLiteVecTestIndex(t, 4).Initialize(t.Context(), tx)
	})
	capability, ok := errors.AsType[*memory.CapabilityNotSupportedError](err)
	if !ok || capability.Capability != "vector" {
		t.Fatalf("capability = %#v, err = %v", capability, err)
	}
	if got, want := err.Error(), "memory capability is not supported: vector (sqlite-vec projection dimension 3 does not match configured dimension 4; rebuild the projection)"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	assertSQLiteVecErrorDoesNotLeak(t, err, "projection.db", "CREATE VIRTUAL TABLE", "Memory private text", "[0 0 0]")
	assertSQLiteVecSchema(t, database, "CREATE VIRTUAL TABLE pc_memory_entry_vec USING vec0(embedding float[3])")
}

func TestSQLiteVecInitializeRejectsIncompatibleProjectionSchema(t *testing.T) {
	database := openSQLiteVecTestDatabase(t)
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		_, err := tx.ExecContext(t.Context(), "CREATE TABLE pc_memory_entry_vec (embedding BLOB)")
		return err
	}); err != nil {
		t.Fatal(err)
	}

	err := database.Transaction(t.Context(), func(tx DBTX) error {
		return newSQLiteVecTestIndex(t, 3).Initialize(t.Context(), tx)
	})
	capability, ok := errors.AsType[*memory.CapabilityNotSupportedError](err)
	if !ok || capability.Capability != "vector" {
		t.Fatalf("capability = %#v, err = %v", capability, err)
	}
	if got, want := err.Error(), "memory capability is not supported: vector (sqlite-vec projection schema is incompatible; rebuild the projection)"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	assertSQLiteVecErrorDoesNotLeak(t, err, "CREATE TABLE", "pc_memory_entry_vec (embedding BLOB)")
}

func TestSQLiteVecInitializeRejectsFreshOversizedDimension(t *testing.T) {
	database := openSQLiteVecTestDatabase(t)
	err := database.Transaction(t.Context(), func(tx DBTX) error {
		return newSQLiteVecTestIndex(t, 65536).Initialize(t.Context(), tx)
	})
	capability, ok := errors.AsType[*memory.CapabilityNotSupportedError](err)
	if !ok || capability.Capability != "vector" {
		t.Fatalf("capability = %#v, err = %v", capability, err)
	}
	if got, want := err.Error(), "memory capability is not supported: vector (sqlite-vec supports at most 8192 dimensions; configured dimension 65536 is unsupported)"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	assertSQLiteVecErrorDoesNotLeak(t, err, "projection.db", "migrate", "CREATE VIRTUAL TABLE", "[0 0 0]")
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		var count int
		if err := tx.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM sqlite_master WHERE name = 'pc_memory_entry_vec'",
		).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("fresh oversized profile created %d vec0 tables", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteVecInitializeClearsStaleProbeRow(t *testing.T) {
	database := openSQLiteVecTestDatabase(t)
	initializeSQLiteVecTestIndex(t, database, 3)
	packed, err := packSQLiteVector([]float64{0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		_, err := tx.ExecContext(t.Context(),
			"INSERT INTO pc_memory_entry_vec (rowid, embedding) VALUES (?, ?)", -1, packed,
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	initializeSQLiteVecTestIndex(t, database, 3)
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		var count int
		if err := tx.QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM pc_memory_entry_vec WHERE rowid = ?", -1,
		).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("stale probe rows = %d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteVecInitializePreservesProbeQueryAndCleanupCauses(t *testing.T) {
	database := openSQLiteVecTestDatabase(t)
	queryCause := errors.New("query failure /private/db API_KEY=secret")
	cleanupCause := errors.New("cleanup failure Memory private text")
	failed := &sqliteVecProbeFailureDB{DBTX: database.SQLDB(), queryCause: queryCause, cleanupCause: cleanupCause}

	err := newSQLiteVecTestIndex(t, 3).Initialize(t.Context(), failed)
	capability, ok := errors.AsType[*memory.CapabilityNotSupportedError](err)
	if !ok || capability.Capability != "vector" {
		t.Fatalf("capability = %#v, err = %v", capability, err)
	}
	if !errors.Is(err, queryCause) || !errors.Is(err, cleanupCause) {
		t.Fatalf("root causes are not matchable: %v", err)
	}
	assertSQLiteVecErrorDoesNotLeak(t, err, "/private/db", "API_KEY=secret", "Memory private text")
}

func openSQLiteVecTestDatabase(t *testing.T) *Database {
	t.Helper()
	database, err := OpenSQLite(t.Context(), DefaultSQLiteConfig(filepath.Join(t.TempDir(), "projection.db")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return database
}

func newSQLiteVecTestIndex(t *testing.T, dimension int) *SQLiteMemoryVectorIndex {
	t.Helper()
	profile, err := memory.NewEmbeddingProfile("profile", "model", dimension, "unit")
	if err != nil {
		t.Fatal(err)
	}
	index, err := NewSQLiteMemoryVectorIndex(profile)
	if err != nil {
		t.Fatal(err)
	}
	return index
}

func initializeSQLiteVecTestIndex(t *testing.T, database *Database, dimension int) {
	t.Helper()
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		return newSQLiteVecTestIndex(t, dimension).Initialize(t.Context(), tx)
	}); err != nil {
		t.Fatal(err)
	}
}

func assertSQLiteVecSchema(t *testing.T, database *Database, want string) {
	t.Helper()
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		var schema string
		if err := tx.QueryRowContext(t.Context(),
			"SELECT sql FROM sqlite_master WHERE name = 'pc_memory_entry_vec'",
		).Scan(&schema); err != nil {
			return err
		}
		if schema != want {
			t.Fatalf("schema = %q, want %q", schema, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertSQLiteVecErrorDoesNotLeak(t *testing.T, err error, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if strings.Contains(err.Error(), value) {
			t.Fatalf("error leaked %q: %v", value, err)
		}
	}
}

type sqliteVecProbeFailureDB struct {
	DBTX
	queryCause   error
	cleanupCause error
	deleteCalls  int
}

func (d *sqliteVecProbeFailureDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if query == "DELETE FROM pc_memory_entry_vec WHERE rowid = ?" {
		d.deleteCalls++
		if d.deleteCalls == 2 {
			return nil, d.cleanupCause
		}
	}
	return d.DBTX.ExecContext(ctx, query, args...)
}

func (d *sqliteVecProbeFailureDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if query == "SELECT rowid FROM pc_memory_entry_vec WHERE embedding MATCH ? AND k = 1" {
		return nil, d.queryCause
	}
	return d.DBTX.QueryContext(ctx, query, args...)
}
