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

package sqlstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mattn/go-sqlite3"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestRuntimeSkillPackageReadsExactRevisionAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "exact-packages.db")
	database := openSkillPackageDatabase(t, path)
	first := skillPackageSnapshot(t, "first-package")
	second := skillPackageSnapshot(t, "second-package")
	other := skillPackageSnapshot(t, "other-scope-package")
	old := writeRuntimeSkillPackage(t, database, "scope-a", first, nil)
	latest := writeRuntimeSkillPackage(t, database, "scope-a", second, old)
	isolated := writeRuntimeSkillPackage(t, database, "scope-b", other, nil)
	if old.Ref().Revision() != 1 || latest.Ref().Revision() != 2 || isolated.Ref() != old.Ref() {
		t.Fatal("fixture did not create the exact revisions in isolated Scopes")
	}
	for pass := range 2 {
		reader, err := sqlstore.NewRuntimeSkillPackageReader(database)
		if err != nil {
			t.Fatal(err)
		}
		database.SQLDB().SetMaxOpenConns(1)
		if _, queryErr := database.SQLDB().ExecContext(t.Context(), "PRAGMA query_only = ON"); queryErr != nil {
			t.Fatal(queryErr)
		}
		var before int64
		if queryErr := database.SQLDB().QueryRowContext(t.Context(), "SELECT total_changes()").Scan(&before); queryErr != nil {
			t.Fatal(queryErr)
		}
		for _, request := range []struct {
			scopeID string
			ref     artifact.Ref
			want    skill.PackageSnapshot
		}{
			{scopeID: "scope-a", ref: old.Ref(), want: first},
			{scopeID: "scope-a", ref: latest.Ref(), want: second},
			{scopeID: "scope-b", ref: isolated.Ref(), want: other},
		} {
			got, readErr := reader.ReadSkillPackage(t.Context(), request.scopeID, request.ref)
			if readErr != nil {
				t.Fatalf("pass %d read: %v", pass, readErr)
			}
			assertSameSkillPackageSnapshot(t, got, request.want)
			archive := got.Archive()
			digest := sha256.Sum256(archive)
			if got.Reference().ArchiveDigest() != hex.EncodeToString(digest[:]) || got.Reference().ArchiveSize() != len(archive) {
				t.Fatal("snapshot reference does not describe the returned archive")
			}
			clear(archive)
			clear(got.Manifest())
			clear(got.Entries())
			assertSameSkillPackageSnapshot(t, got, request.want)
			reloaded, reloadErr := reader.ReadSkillPackage(t.Context(), request.scopeID, request.ref)
			if reloadErr != nil {
				t.Fatal(reloadErr)
			}
			assertSameSkillPackageSnapshot(t, reloaded, request.want)
		}
		var after int64
		if queryErr := database.SQLDB().QueryRowContext(t.Context(), "SELECT total_changes()").Scan(&after); queryErr != nil {
			t.Fatal(queryErr)
		}
		if before != after {
			t.Fatalf("package reads wrote state: before=%d after=%d", before, after)
		}
		if pass == 0 {
			if closeErr := database.Close(t.Context()); closeErr != nil {
				t.Fatal(closeErr)
			}
			database = openSkillPackageDatabase(t, path)
		}
	}
}

func TestRuntimeSkillPackageClassifiesMissingAndNonPackageArtifacts(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		family     string
		mutation   string
		value      []byte
		missing    bool
		nonPackage bool
	}{
		{name: "missing artifact", family: "skill", mutation: "DELETE FROM pc_artifact_heads; DELETE FROM pc_artifacts", missing: true},
		{name: "missing package", family: "skill", mutation: "DELETE FROM pc_skill_packages", missing: true},
		{name: "missing other family", family: "future.family", missing: true},
		{name: "existing other family", family: "future.family", mutation: "INSERT INTO pc_artifacts SELECT scope_id, 'future.family', artifact_id, revision, content FROM pc_artifacts", nonPackage: true},
		{name: "legacy Skill", family: "skill", mutation: "UPDATE pc_artifacts SET content = ?", value: []byte(`{"name":"legacy-secret","description":"description-secret","instructions":"instruction-secret","validation":["check-secret"]}`), nonPackage: true},
		{name: "invalid artifact JSON", family: "skill", mutation: "UPDATE pc_artifacts SET content = ?", value: []byte(`{"payload-secret":`)},
		{name: "invalid package reference", family: "skill", mutation: "UPDATE pc_artifacts SET content = ?", value: []byte(`{"package_ref":{"tree_digest":"digest-secret"}}`)},
		{name: "invalid archive", family: "skill", mutation: "UPDATE pc_skill_packages SET archive = ?", value: []byte("archive-secret")},
		{name: "invalid manifest", family: "skill", mutation: "UPDATE pc_skill_packages SET manifest = ?", value: []byte("manifest-secret")},
		{name: "mismatched package index", family: "skill", mutation: "UPDATE pc_skill_packages SET file_count = file_count + 1"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			database := openTestDatabase(t)
			snapshot := skillPackageSnapshot(t, "classification-secret")
			stored := writeRuntimeSkillPackage(t, database, "scope-secret", snapshot, nil)
			if scenario.mutation != "" {
				var args []any
				if scenario.value != nil {
					args = append(args, scenario.value)
				}
				if _, mutationErr := database.SQLDB().ExecContext(t.Context(), scenario.mutation, args...); mutationErr != nil {
					t.Fatal(mutationErr)
				}
			}
			reader, err := sqlstore.NewRuntimeSkillPackageReader(database)
			if err != nil {
				t.Fatal(err)
			}
			ref, err := artifact.NewRef(scenario.family, stored.Ref().ID(), 1)
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := reader.ReadSkillPackage(t.Context(), "scope-secret", ref)
			if readErr == nil || len(got.Archive()) != 0 {
				t.Fatalf("invalid state returned a package: %v", readErr)
			}
			_, missing := errors.AsType[*artifact.NotFoundError](readErr)
			_, nonPackage := errors.AsType[*sqlstore.SkillPackageRequiredError](readErr)
			if missing != scenario.missing || nonPackage != scenario.nonPackage {
				t.Fatalf("wrong classification: %T %v", readErr, readErr)
			}
			assertSkillPackageErrorRedacted(t, readErr, "scope-secret", "exact-secret", "classification-secret", "legacy-secret", "description-secret", "instruction-secret", "payload-secret", "digest-secret", "archive-secret", "manifest-secret", "future.family")
			if scenario.name == "missing package" {
				_, repository := repositories(t)
				legacy, constructErr := sqlstore.NewRuntimeArtifactReader(database, repository)
				if constructErr != nil {
					t.Fatal(constructErr)
				}
				_, legacyErr := legacy.ReadArtifact(t.Context(), "scope-secret", "skill", "exact-secret", 1)
				if _, corrupt := errors.AsType[*sqlstore.InvalidStoredPayloadError](legacyErr); !corrupt {
					t.Fatalf("legacy Artifact GET lost its corruption classification: %v", legacyErr)
				}
			}
		})
	}
}

func TestRuntimeSkillPackageAdmissionAndDatabaseFailures(t *testing.T) {
	database := openTestDatabase(t)
	snapshot := skillPackageSnapshot(t, "admission-package")
	stored := writeRuntimeSkillPackage(t, database, "persisted-scope", snapshot, nil)
	scopeStore, err := sqlstore.NewRuntimeScopeStore(database, sqlstore.ScopeRepository{})
	if err != nil {
		t.Fatal(err)
	}
	scopeDraft, err := scope.NewDraft("title", "summary", "", nil, nil, "package-read-scope")
	if err != nil {
		t.Fatal(err)
	}
	if _, createErr := scopeStore.Create(t.Context(), "persisted-scope", scopeDraft, func([]scope.Descriptor) error { return nil }); createErr != nil {
		t.Fatal(createErr)
	}
	reader, err := sqlstore.NewRuntimeSkillPackageReader(database)
	if err != nil {
		t.Fatal(err)
	}
	scopeReader, err := sqlstore.NewRuntimeScopeReader(database, sqlstore.ScopeRepository{})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := runtime.NewConfigured(runtime.RuntimeOptions{ScopeReader: scopeReader}, nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := runtime.NewSkillPackageResourceApplication(lifecycle, reader)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := application.ReadSkillPackage(t.Context(), "persisted-scope", stored.Ref())
	if readErr != nil {
		t.Fatal(readErr)
	}
	assertSameSkillPackageSnapshot(t, got, snapshot)
	if _, readErr := application.ReadSkillPackage(t.Context(), "unknown-scope", stored.Ref()); readErr == nil {
		t.Fatal("unknown Scope accepted")
	} else if _, ok := errors.AsType[*scope.NotFoundError](readErr); !ok {
		t.Fatalf("unknown Scope = %v", readErr)
	}
	for _, request := range []struct {
		scopeID string
		ref     artifact.Ref
	}{
		{scopeID: "persisted-scope"},
		{ref: stored.Ref()},
	} {
		if _, readErr := reader.ReadSkillPackage(t.Context(), request.scopeID, request.ref); readErr == nil {
			t.Fatal("invalid read controls accepted")
		}
	}
	future, err := artifact.NewRef("skill", stored.Ref().ID(), 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []artifact.Ref{future, stored.Ref()} {
		scopeID := "persisted-scope"
		if ref == stored.Ref() {
			scopeID = "other-scope"
		}
		if _, readErr := reader.ReadSkillPackage(t.Context(), scopeID, ref); readErr == nil {
			t.Fatal("missing exact scoped revision accepted")
		} else if _, ok := errors.AsType[*artifact.NotFoundError](readErr); !ok {
			t.Fatalf("missing exact scoped revision = %v", readErr)
		}
	}
	if _, dropErr := database.SQLDB().ExecContext(t.Context(), "DROP TABLE pc_skill_packages"); dropErr != nil {
		t.Fatal(dropErr)
	}
	if _, readErr := reader.ReadSkillPackage(t.Context(), "persisted-scope", stored.Ref()); readErr == nil {
		t.Fatal("storage failure accepted")
	} else if _, ok := errors.AsType[sqlite3.Error](readErr); !ok {
		t.Fatalf("storage failure cause lost: %v", readErr)
	}
	if closeErr := database.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, readErr := reader.ReadSkillPackage(t.Context(), "persisted-scope", stored.Ref()); readErr == nil {
		t.Fatal("closed database accepted read")
	} else if _, ok := errors.AsType[*sqlstore.DatabaseClosedError](readErr); !ok {
		t.Fatalf("closed database = %v", readErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, readErr := reader.ReadSkillPackage(ctx, "persisted-scope", stored.Ref()); !errors.Is(readErr, context.Canceled) {
		t.Fatalf("cancellation did not dominate closed database: %v", readErr)
	}
}

func writeRuntimeSkillPackage(t *testing.T, database *sqlstore.Database, scopeID string, snapshot skill.PackageSnapshot, previous artifact.Snapshot) artifact.Snapshot {
	t.Helper()
	content, err := skill.NewPackageContent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	draft, err := skill.NewPackageDraft(content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, repository := repositories(t)
	var stored artifact.Snapshot
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		if _, addErr := (sqlstore.SkillPackageRepository{}).Add(t.Context(), tx, scopeID, snapshot); addErr != nil {
			return addErr
		}
		var writeErr error
		if previous == nil {
			stored, writeErr = repository.Create(t.Context(), tx, scopeID, "exact-secret", draft)
		} else {
			stored, writeErr = repository.Revise(t.Context(), tx, scopeID, previous, draft)
		}
		return writeErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	return stored
}
