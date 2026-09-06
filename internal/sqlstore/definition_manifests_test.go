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
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mattn/go-sqlite3"

	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestDefinitionManifestRepositoryRegistersAndFindsImmutableManifest(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.DefinitionManifestRepository{}
	manifest := definitionManifest(t, "github.issue", "v1", "issue")
	wantPayload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	err = database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		registered, registerErr := repository.Register(t.Context(), tx, manifest)
		if registerErr != nil {
			return registerErr
		}
		assertSameDefinitionManifest(t, registered, manifest)

		found, ok, findErr := repository.Find(t.Context(), tx, manifest.Name(), manifest.Version())
		if findErr != nil || !ok {
			t.Fatalf("Find() = %#v, %t, %v", found, ok, findErr)
		}
		assertSameDefinitionManifest(t, found, manifest)

		got, getErr := repository.Get(t.Context(), tx, manifest.Name(), manifest.Version())
		if getErr != nil {
			return getErr
		}
		assertSameDefinitionManifest(t, got, manifest)

		registered, registerErr = repository.Register(t.Context(), tx, manifest)
		if registerErr != nil {
			return registerErr
		}
		assertSameDefinitionManifest(t, registered, manifest)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var fingerprint string
	var payload []byte
	if err := database.SQLDB().QueryRowContext(t.Context(), `SELECT fingerprint, manifest
        FROM pc_source_definition_manifests WHERE definition_name = ? AND definition_version = ?`,
		manifest.Name(), manifest.Version()).Scan(&fingerprint, &payload); err != nil {
		t.Fatal(err)
	}
	if fingerprint != manifest.Fingerprint() || !bytes.Equal(payload, wantPayload) {
		t.Fatalf("stored manifest = %q %s, want %q %s", fingerprint, payload, manifest.Fingerprint(), wantPayload)
	}
}

func TestDefinitionManifestRepositoryTreatsEquivalentJSONAsIdempotent(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.DefinitionManifestRepository{}
	identity, err := source.NewDefinitionIdentity("github.issue", "v1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, jsontext.Value(
		`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`,
	), nil)
	if err != nil {
		t.Fatal(err)
	}
	stored := fmt.Sprintf(
		`{"source_schema":{"properties":{"b":{"type":"integer"},"a":{"type":"string"}},"type":"object"},"fingerprint":%q,"version":"v1","name":"github.issue","projections":[]}`,
		manifest.Fingerprint(),
	)
	if _, parseErr := source.ParseDefinitionManifest([]byte(stored)); parseErr != nil {
		t.Fatalf("equivalent fixture is not a valid manifest: %v", parseErr)
	}
	if _, insertErr := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_source_definition_manifests
        (definition_name, definition_version, fingerprint, manifest) VALUES (?, ?, ?, ?)`,
		manifest.Name(), manifest.Version(), manifest.Fingerprint(), []byte(stored)); insertErr != nil {
		t.Fatal(insertErr)
	}

	err = database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		registered, registerErr := repository.Register(t.Context(), tx, manifest)
		if registerErr != nil {
			return registerErr
		}
		assertSameDefinitionManifest(t, registered, manifest)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionManifestRepositoryRejectsIdentityReuse(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.DefinitionManifestRepository{}
	first := definitionManifest(t, "github.issue", "v1", "title")
	second := definitionManifest(t, "github.issue", "v1", "body")
	if err := registerDefinitionManifest(t.Context(), database, repository, first); err != nil {
		t.Fatal(err)
	}
	err := registerDefinitionManifest(t.Context(), database, repository, second)
	assertDefinitionManifestConflict(t, err, first, second)
}

func TestDefinitionManifestRepositoryRejectsFingerprintReuseAcrossVersions(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.DefinitionManifestRepository{}
	manifest := definitionManifest(t, "github.issue", "v1", "title")
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, insertErr := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_source_definition_manifests
        (definition_name, definition_version, fingerprint, manifest) VALUES (?, ?, ?, ?)`,
		manifest.Name(), "v2-index-secret", manifest.Fingerprint(), payload); insertErr != nil {
		t.Fatal(insertErr)
	}

	err = registerDefinitionManifest(t.Context(), database, repository, manifest)
	assertDefinitionManifestConflict(t, err, manifest)
}

func TestDefinitionManifestRepositoryClassifiesStoredCorruption(t *testing.T) {
	for _, scenario := range []string{"malformed payload", "name mismatch", "version mismatch", "fingerprint mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			database := openTestDatabase(t)
			repository := sqlstore.DefinitionManifestRepository{}
			manifest := definitionManifest(t, "decoded-secret", "v1", "title")
			payload, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			indexedName := manifest.Name()
			indexedVersion := manifest.Version()
			indexedFingerprint := manifest.Fingerprint()
			switch scenario {
			case "malformed payload":
				payload = []byte(`{"unknown":"stored-secret"}`)
			case "name mismatch":
				indexedName = "indexed-secret"
			case "version mismatch":
				indexedVersion = "v2-index-secret"
			case "fingerprint mismatch":
				indexedFingerprint = "sha256:" + strings.Repeat("0", 64)
			}
			if _, insertErr := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_source_definition_manifests
                (definition_name, definition_version, fingerprint, manifest) VALUES (?, ?, ?, ?)`,
				indexedName, indexedVersion, indexedFingerprint, payload); insertErr != nil {
				t.Fatal(insertErr)
			}

			_, found, err := repository.Find(t.Context(), database.SQLDB(), indexedName, indexedVersion)
			if found {
				t.Fatal("Find() reported a corrupt row as found")
			}
			switch scenario {
			case "malformed payload":
				if _, ok := errors.AsType[*sqlstore.InvalidStoredPayloadError](err); !ok {
					t.Fatalf("corruption error = %T %v", err, err)
				}
			default:
				if _, ok := errors.AsType[*sqlstore.IdentityMismatchError](err); !ok {
					t.Fatalf("identity mismatch error = %T %v", err, err)
				}
			}
			assertDefinitionManifestErrorRedacted(t, err,
				"decoded-secret", "indexed-secret", "v2-index-secret", "stored-secret", indexedFingerprint)
		})
	}
}

func TestDefinitionManifestRepositoryPreservesStoredColumnTypeError(t *testing.T) {
	for _, test := range []struct {
		name   string
		stored any
		secret string
	}{
		{name: "text", stored: "stored-text-secret", secret: "stored-text-secret"},
		{name: "integer", stored: int64(73), secret: "73"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := openTestDatabase(t)
			manifest := definitionManifest(t, "definition-secret", "version-secret", "title")
			if _, insertErr := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_source_definition_manifests
                (definition_name, definition_version, fingerprint, manifest) VALUES (?, ?, ?, ?)`,
				manifest.Name(), manifest.Version(), manifest.Fingerprint(), test.stored); insertErr != nil {
				t.Fatal(insertErr)
			}

			_, found, err := (sqlstore.DefinitionManifestRepository{}).Find(
				t.Context(), database.SQLDB(), manifest.Name(), manifest.Version(),
			)
			if found {
				t.Fatal("Find() reported a wrongly typed manifest column as found")
			}
			columnError, ok := errors.AsType[*sqlstore.InvalidStoredColumnError](err)
			if !ok {
				t.Fatalf("stored column error = %T %v", err, err)
			}
			if columnError.Column != "manifest" || columnError.Expected != "bytes" {
				t.Fatalf("stored column error = %#v", columnError)
			}
			assertDefinitionManifestErrorRedacted(t, err,
				manifest.Name(), manifest.Version(), manifest.Fingerprint(), test.secret)
		})
	}
}

func TestDefinitionManifestRepositoryValidatesLookupIdentity(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.DefinitionManifestRepository{}
	for _, operation := range []struct {
		name string
		run  func(string, string) error
	}{
		{name: "find", run: func(name, version string) error {
			_, _, err := repository.Find(t.Context(), database.SQLDB(), name, version)
			return err
		}},
		{name: "get", run: func(name, version string) error {
			_, err := repository.Get(t.Context(), database.SQLDB(), name, version)
			return err
		}},
	} {
		t.Run(operation.name+" rejects invalid name", func(t *testing.T) {
			const invalid = " manifest-name-secret "
			err := operation.run(invalid, "v1")
			if _, ok := errors.AsType[*source.InvalidDefinitionManifestError](err); !ok {
				t.Fatalf("invalid name error = %T %v", err, err)
			}
			assertDefinitionManifestErrorRedacted(t, err, invalid)
		})
		t.Run(operation.name+" rejects invalid version", func(t *testing.T) {
			const invalid = " manifest-version-secret "
			err := operation.run("github.issue", invalid)
			if _, ok := errors.AsType[*source.InvalidDefinitionManifestError](err); !ok {
				t.Fatalf("invalid version error = %T %v", err, err)
			}
			assertDefinitionManifestErrorRedacted(t, err, invalid)
		})
	}
}

func TestDefinitionManifestRepositoryReportsMissing(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.DefinitionManifestRepository{}
	manifest, found, err := repository.Find(t.Context(), database.SQLDB(), "missing-secret", "v1")
	if err != nil || found {
		t.Fatalf("Find() = %#v, %t, %v; want zero, false, nil", manifest, found, err)
	}
	_, err = repository.Get(t.Context(), database.SQLDB(), "missing-secret", "v1")
	if _, ok := errors.AsType[*sqlstore.RepositoryNotFoundError](err); !ok {
		t.Fatalf("missing manifest error = %T %v", err, err)
	}
	assertDefinitionManifestErrorRedacted(t, err, "missing-secret")
}

func TestDefinitionManifestRepositoryValidatesManifestBeforeWrite(t *testing.T) {
	database := openTestDatabase(t)
	_, err := (sqlstore.DefinitionManifestRepository{}).Register(
		t.Context(), database.SQLDB(), source.DefinitionManifest{},
	)
	if _, ok := errors.AsType[*source.InvalidDefinitionManifestError](err); !ok {
		t.Fatalf("invalid manifest error = %T %v", err, err)
	}
	var count int
	if err := database.SQLDB().QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM pc_source_definition_manifests",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows after invalid Register() = %d, want 0", count)
	}
}

func TestDefinitionManifestRepositoryPreservesSQLiteError(t *testing.T) {
	database := openTestDatabase(t)
	if _, err := database.SQLDB().ExecContext(t.Context(), "DROP TABLE pc_source_definition_manifests"); err != nil {
		t.Fatal(err)
	}
	manifest := definitionManifest(t, "github.issue", "v1", "title")
	err := registerDefinitionManifest(t.Context(), database, sqlstore.DefinitionManifestRepository{}, manifest)
	if _, ok := errors.AsType[sqlite3.Error](err); !ok {
		t.Fatalf("database error = %T %v", err, err)
	}
	if _, conflict := errors.AsType[*sqlstore.StoredPayloadConflictError](err); conflict {
		t.Fatalf("database error became manifest conflict: %v", err)
	}
}

func TestDefinitionManifestRepositoryConcurrentEqualRegistrationIsIdempotent(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "equal-manifest-race.db")
	databases := []*sqlstore.Database{
		openDefinitionManifestDatabase(t, databasePath),
		openDefinitionManifestDatabase(t, databasePath),
	}
	manifest := definitionManifest(t, "github.issue", "v1", "title")
	results := registerDefinitionManifestsConcurrently(t, databases, []source.DefinitionManifest{manifest, manifest})
	for index, err := range results {
		if err != nil {
			t.Fatalf("concurrent equal Register()[%d] = %T %v", index, err, err)
		}
	}
}

func TestDefinitionManifestRepositoryConcurrentConflictHasOneWinner(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "conflicting-manifest-race.db")
	databases := []*sqlstore.Database{
		openDefinitionManifestDatabase(t, databasePath),
		openDefinitionManifestDatabase(t, databasePath),
	}
	manifests := []source.DefinitionManifest{
		definitionManifest(t, "github.issue", "v1", "title"),
		definitionManifest(t, "github.issue", "v1", "body"),
	}
	results := registerDefinitionManifestsConcurrently(t, databases, manifests)
	var succeeded, conflicted int
	for _, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		if _, ok := errors.AsType[*sqlstore.StoredPayloadConflictError](err); !ok {
			t.Fatalf("concurrent conflicting Register() = %T %v", err, err)
		}
		conflicted++
		assertDefinitionManifestErrorRedacted(t, err,
			manifests[0].Name(), manifests[0].Version(), manifests[0].Fingerprint(), manifests[1].Fingerprint())
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent outcomes = success:%d conflict:%d", succeeded, conflicted)
	}
}

func TestDefinitionManifestRepositoryPreservesCancellation(t *testing.T) {
	database := openTestDatabase(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	manifest := definitionManifest(t, "github.issue", "v1", "title")
	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, registerErr := (sqlstore.DefinitionManifestRepository{}).Register(ctx, tx, manifest)
		return registerErr
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Register() = %T %v", err, err)
	}
	if _, conflict := errors.AsType[*sqlstore.StoredPayloadConflictError](err); conflict {
		t.Fatalf("canceled Register() became manifest conflict: %v", err)
	}
	var count int
	if err := database.SQLDB().QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM pc_source_definition_manifests",
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rows after canceled Register() = %d, want 0", count)
	}
}

func definitionManifest(t *testing.T, name, version, property string) source.DefinitionManifest {
	t.Helper()
	identity, err := source.NewDefinitionIdentity(name, version)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, map[string]any{
		"type": "object",
		"properties": map[string]any{
			property: map[string]any{"type": "string"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func assertSameDefinitionManifest(t *testing.T, got, want source.DefinitionManifest) {
	t.Helper()
	gotPayload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantPayload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPayload, wantPayload) {
		t.Fatalf("manifest = %s, want %s", gotPayload, wantPayload)
	}
}

func registerDefinitionManifest(
	ctx context.Context,
	database *sqlstore.Database,
	repository sqlstore.DefinitionManifestRepository,
	manifest source.DefinitionManifest,
) error {
	return database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, err := repository.Register(ctx, tx, manifest)
		return err
	})
}

func assertDefinitionManifestConflict(
	t *testing.T,
	err error,
	manifests ...source.DefinitionManifest,
) {
	t.Helper()
	if _, ok := errors.AsType[*sqlstore.StoredPayloadConflictError](err); !ok {
		t.Fatalf("manifest conflict error = %T %v", err, err)
	}
	secrets := make([]string, 0, len(manifests)*3)
	for _, manifest := range manifests {
		secrets = append(secrets, manifest.Name(), manifest.Version(), manifest.Fingerprint())
	}
	assertDefinitionManifestErrorRedacted(t, err, secrets...)
}

func assertDefinitionManifestErrorRedacted(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(message, secret) {
			t.Fatalf("error disclosed manifest data %q: %v", secret, err)
		}
	}
}

func openDefinitionManifestDatabase(t *testing.T, path string) *sqlstore.Database {
	t.Helper()
	database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return database
}

func registerDefinitionManifestsConcurrently(
	t *testing.T,
	databases []*sqlstore.Database,
	manifests []source.DefinitionManifest,
) []error {
	t.Helper()
	start := make(chan struct{})
	results := make([]error, len(databases))
	var waitGroup sync.WaitGroup
	for index, database := range databases {
		waitGroup.Go(func() {
			<-start
			results[index] = registerDefinitionManifest(
				t.Context(), database, sqlstore.DefinitionManifestRepository{}, manifests[index],
			)
		})
	}
	close(start)
	waitGroup.Wait()
	return results
}
