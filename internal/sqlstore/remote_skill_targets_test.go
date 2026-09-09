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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"

	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

const remoteSkillTargetDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestRemoteSkillTargetRepositoryPersistsAcrossRestartWithoutRawSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-targets.db")
	first := openRemoteSkillTargetDatabase(t, path)
	repository := sqlstore.RemoteSkillTargetRepository{}
	target := remoteSkillTarget(t, "scope-target-restart", "codex-restart", skill.RemoteTargetPending)

	stored := createRemoteSkillTarget(t, first, repository, target)
	if stored.ID() != target.ID() || stored.EnrollmentCodeDigest() != target.EnrollmentCodeDigest() {
		t.Fatalf("Create() = %#v, want durable target", stored)
	}
	closeRemoteSkillTargetDatabase(t, first)

	second := openRemoteSkillTargetDatabase(t, path)
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, second) })
	got := getRemoteSkillTarget(t, second, repository, target.ScopeID(), target.ID())
	if got.ID() != target.ID() || got.State() != skill.RemoteTargetPending || got.Generation() != target.Generation() {
		t.Fatalf("Get() after restart = %#v, want persisted target", got)
	}
	var matched skill.RemoteTarget
	var found bool
	err := second.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var findErr error
		matched, found, findErr = repository.FindByEnrollmentDigest(t.Context(), tx, target.EnrollmentCodeDigest())
		return findErr
	})
	if err != nil || !found || matched.ID() != target.ID() {
		t.Fatalf("FindByEnrollmentDigest() = (%#v, %t, %v), want stored target", matched, found, err)
	}

	file, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, rawSecret := range []string{"enrollment-code-secret", "target-credential-secret"} {
		if bytes.Contains(file, []byte(rawSecret)) {
			t.Fatalf("SQLite database persisted raw secret %q", rawSecret)
		}
	}
}

func TestRemoteSkillTargetRepositoryListsByCreationThenTargetID(t *testing.T) {
	database := openRemoteSkillTargetDatabase(t, filepath.Join(t.TempDir(), "list.db"))
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	repository := sqlstore.RemoteSkillTargetRepository{}
	later := remoteSkillTarget(t, "scope-target-list", "codex-later", skill.RemoteTargetPending)
	later = remoteSkillTargetWith(t, later, func(input *skill.RemoteTargetInput) {
		input.CreatedAt = input.CreatedAt.Add(time.Hour)
		input.EnrollmentExpiresAt = input.CreatedAt.Add(time.Hour)
		input.UpdatedAt = input.CreatedAt
	})
	earlier := remoteSkillTarget(t, "scope-target-list", "workbuddy-earlier", skill.RemoteTargetPending)
	createRemoteSkillTarget(t, database, repository, later)
	createRemoteSkillTarget(t, database, repository, earlier)
	targets, err := inRemoteSkillTargetTransaction(t, database, func(tx sqlstore.DBTX) ([]skill.RemoteTarget, error) {
		return repository.List(t.Context(), tx, "scope-target-list")
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].ID() != earlier.ID() || targets[1].ID() != later.ID() {
		t.Fatalf("List() = %#v, want creation then target-ID order", targets)
	}
}

func TestRemoteSkillTargetRepositoryRejectsNonZeroInitialGeneration(t *testing.T) {
	database := openRemoteSkillTargetDatabase(t, filepath.Join(t.TempDir(), "initial-generation.db"))
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	repository := sqlstore.RemoteSkillTargetRepository{}
	target := remoteSkillTarget(t, "scope-target-initial-generation", "codex-initial-generation", skill.RemoteTargetPending)
	target = remoteSkillTargetWith(t, target, func(input *skill.RemoteTargetInput) {
		input.Generation = 1
		input.UpdatedAt = input.UpdatedAt.Add(time.Second)
	})

	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, createErr := repository.Create(t.Context(), tx, target)
		return createErr
	})
	var invalid *sqlstore.InvalidRepositoryArgumentError
	if !errors.As(err, &invalid) || invalid.Field != "target.generation" ||
		strings.Contains(err.Error(), target.ScopeID()) || strings.Contains(err.Error(), target.ID()) {
		t.Fatalf("nonzero Create() error = %T %v, want redacted invalid generation", err, err)
	}
}

func TestRemoteSkillTargetRepositoryRejectsDuplicateAndStaleGeneration(t *testing.T) {
	database := openRemoteSkillTargetDatabase(t, filepath.Join(t.TempDir(), "cas.db"))
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	repository := sqlstore.RemoteSkillTargetRepository{}
	original := remoteSkillTarget(t, "scope-target-cas", "codex-cas", skill.RemoteTargetPending)
	createRemoteSkillTarget(t, database, repository, original)

	duplicateErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, err := repository.Create(t.Context(), tx, original)
		return err
	})
	var duplicate *skill.RemoteTargetGenerationConflictError
	if !errors.As(duplicateErr, &duplicate) || strings.Contains(duplicateErr.Error(), original.ScopeID()) || strings.Contains(duplicateErr.Error(), original.ID()) {
		t.Fatalf("duplicate Create() error = %T %v, want redacted refusal", duplicateErr, duplicateErr)
	}

	replacement := remoteSkillTarget(t, original.ScopeID(), original.ID(), skill.RemoteTargetPending)
	replacement = remoteSkillTargetWith(t, replacement, func(input *skill.RemoteTargetInput) {
		input.DisplayName = "Renamed target"
		input.Generation = original.Generation() + 1
		input.UpdatedAt = original.UpdatedAt().Add(time.Second)
	})
	updated := replaceRemoteSkillTarget(t, database, repository, replacement, original.Generation())
	if updated.DisplayName() != "Renamed target" || updated.Generation() != original.Generation()+1 {
		t.Fatalf("Replace() = %#v, want next target generation", updated)
	}

	staleErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, err := repository.Replace(t.Context(), tx, replacement, original.Generation())
		return err
	})
	var stale *skill.RemoteTargetGenerationConflictError
	if !errors.As(staleErr, &stale) || strings.Contains(staleErr.Error(), original.ScopeID()) || strings.Contains(staleErr.Error(), original.ID()) {
		t.Fatalf("stale Replace() error = %T %v, want redacted generation conflict", staleErr, staleErr)
	}
}

func TestRemoteSkillTargetRepositoryRejectsReusedEnrollmentDigest(t *testing.T) {
	database := openRemoteSkillTargetDatabase(t, filepath.Join(t.TempDir(), "enrollment-digest.db"))
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	repository := sqlstore.RemoteSkillTargetRepository{}
	first := remoteSkillTarget(t, "scope-target-enrollment", "codex-first", skill.RemoteTargetPending)
	createRemoteSkillTarget(t, database, repository, first)
	second := remoteSkillTarget(t, first.ScopeID(), "workbuddy-second", skill.RemoteTargetPending)
	second = remoteSkillTargetWith(t, second, func(input *skill.RemoteTargetInput) {
		input.EnrollmentCodeDigest = first.EnrollmentCodeDigest()
	})

	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, createErr := repository.Create(t.Context(), tx, second)
		return createErr
	})
	var conflict *skill.RemoteTargetGenerationConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("reused enrollment digest Create() error = %T %v, want conflict", err, err)
	}
}

func TestRemoteSkillTargetRepositoryConcurrentReplaceHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent-cas.db")
	first := openRemoteSkillTargetDatabase(t, path)
	second := openRemoteSkillTargetDatabase(t, path)
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, second) })
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, first) })
	repository := sqlstore.RemoteSkillTargetRepository{}
	original := remoteSkillTarget(t, "scope-target-concurrent", "codex-concurrent", skill.RemoteTargetPending)
	createRemoteSkillTarget(t, first, repository, original)
	replacement := remoteSkillTargetWith(t, original, func(input *skill.RemoteTargetInput) {
		input.Generation++
		input.UpdatedAt = input.UpdatedAt.Add(time.Second)
	})

	results := make([]error, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index, database := range []*sqlstore.Database{first, second} {
		group.Go(func() {
			<-start
			results[index] = database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
				_, err := repository.Replace(t.Context(), tx, replacement, original.Generation())
				return err
			})
		})
	}
	close(start)
	group.Wait()

	winners := 0
	conflicts := 0
	for _, err := range results {
		if err == nil {
			winners++
			continue
		}
		var conflict *skill.RemoteTargetGenerationConflictError
		if errors.As(err, &conflict) {
			conflicts++
			continue
		}
		t.Fatalf("concurrent Replace() error = %T %v", err, err)
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent Replace() outcomes: winners=%d conflicts=%d", winners, conflicts)
	}
}

func TestRemoteSkillTargetRepositoryDistinguishesMissingTarget(t *testing.T) {
	database := openRemoteSkillTargetDatabase(t, filepath.Join(t.TempDir(), "missing.db"))
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	repository := sqlstore.RemoteSkillTargetRepository{}
	target := remoteSkillTarget(t, "scope-target-missing", "codex-missing", skill.RemoteTargetPending)

	getErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, err := repository.Get(t.Context(), tx, target.ScopeID(), target.ID())
		return err
	})
	var missing *skill.RemoteTargetNotFoundError
	if !errors.As(getErr, &missing) || strings.Contains(getErr.Error(), target.ScopeID()) || strings.Contains(getErr.Error(), target.ID()) {
		t.Fatalf("Get() missing error = %T %v, want redacted not found", getErr, getErr)
	}

	replacement := remoteSkillTargetWith(t, target, func(input *skill.RemoteTargetInput) {
		input.Generation++
		input.UpdatedAt = input.UpdatedAt.Add(time.Second)
	})
	replaceErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, err := repository.Replace(t.Context(), tx, replacement, target.Generation())
		return err
	})
	if !errors.As(replaceErr, &missing) || strings.Contains(replaceErr.Error(), target.ScopeID()) || strings.Contains(replaceErr.Error(), target.ID()) {
		t.Fatalf("Replace() missing error = %T %v, want redacted not found", replaceErr, replaceErr)
	}
}

func TestSQLiteSkillDistributionSchemaRejectsInvalidAgentKind(t *testing.T) {
	database := openRemoteSkillTargetDatabase(t, filepath.Join(t.TempDir(), "agent-kind.db"))
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	seedRemoteSkillTargetScope(t, database, "scope-target-check")
	_, err := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_agent_skill_targets (
        scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state,
        enrollment_code_digest, enrollment_expires_at, generation, created_at, updated_at
    ) VALUES (?, 'codex-check', 'Invalid agent', 'claude_code', 'project', 'agent_pull', 'pending', ?, ?, 0, ?, ?)`,
		"scope-target-check", remoteSkillTargetDigest, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrConstraint {
		t.Fatalf("invalid agent kind insert = %T %v, want SQLite CHECK refusal", err, err)
	}
}

func TestSQLiteSkillDistributionSchemaRejectsInvalidPendingPayload(t *testing.T) {
	database := openRemoteSkillTargetDatabase(t, filepath.Join(t.TempDir(), "state-payload.db"))
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	seedRemoteSkillTargetScope(t, database, "scope-target-state")
	_, err := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_agent_skill_targets (
        scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state,
        enrollment_code_digest, enrollment_expires_at, installation_id, generation, created_at, updated_at
    ) VALUES (?, 'codex-state', 'Invalid pending state', 'codex', 'project', 'agent_pull', 'pending', ?, ?, 'forbidden-installation', 0, ?, ?)`,
		"scope-target-state", remoteSkillTargetDigest, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrConstraint {
		t.Fatalf("invalid pending payload insert = %T %v, want SQLite CHECK refusal", err, err)
	}
}

func TestSQLiteSkillDistributionSchemaRejectsEmptyActiveIdentity(t *testing.T) {
	database := openRemoteSkillTargetDatabase(t, filepath.Join(t.TempDir(), "active-identity.db"))
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	seedRemoteSkillTargetScope(t, database, "scope-target-active")
	_, err := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_agent_skill_targets (
        scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state,
        installation_id, credential_subject, credential_verifier, receiver_version, last_seen_at,
        generation, created_at, updated_at
    ) VALUES (?, 'codex-active', 'Invalid active state', 'codex', 'project', 'agent_pull', 'active',
        '', 'target-subject', ?, '0.1.0', ?, 0, ?, ?)`,
		"scope-target-active", remoteSkillTargetDigest, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	var sqliteErr sqlite3.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrConstraint {
		t.Fatalf("empty active identity insert = %T %v, want SQLite CHECK refusal", err, err)
	}
}

func TestSQLiteSkillDistributionSchemaRejectsSameNamedViewWithoutPartialSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target-view.db")
	seed, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE prephase_remote_skill_target_sentinel (value TEXT NOT NULL)",
		"CREATE VIEW pc_agent_skill_targets AS SELECT value FROM prephase_remote_skill_target_sentinel",
	} {
		if _, execErr := seed.ExecContext(t.Context(), statement); execErr != nil {
			t.Fatal(execErr)
		}
	}
	if closeErr := seed.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	database, openErr := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
	if database != nil {
		closeRemoteSkillTargetDatabase(t, database)
		t.Fatal("OpenSQLite returned a database for a remote target view conflict")
	}
	if openErr == nil {
		t.Fatal("OpenSQLite accepted a view where the remote target table is required")
	}

	verify, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = verify.Close() })
	for name, wantType := range map[string]string{
		"prephase_remote_skill_target_sentinel": "table",
		"pc_agent_skill_targets":                "view",
	} {
		var gotType string
		if lookupErr := verify.QueryRowContext(t.Context(), "SELECT type FROM sqlite_master WHERE name = ?", name).Scan(&gotType); lookupErr != nil || gotType != wantType {
			t.Fatalf("schema object %q = %q, err=%v; want %q", name, gotType, lookupErr, wantType)
		}
	}
	for _, name := range []string{"pc_scopes", "pc_skill_packages"} {
		var count int
		if lookupErr := verify.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", name).Scan(&count); lookupErr != nil || count != 0 {
			t.Fatalf("failed initialization left %q count=%d err=%v", name, count, lookupErr)
		}
	}
}

func remoteSkillTarget(t *testing.T, scopeID, targetID string, state skill.RemoteTargetState) skill.RemoteTarget {
	t.Helper()
	created := time.Date(2026, time.September, 9, 14, 0, 0, 0, time.UTC)
	input := skill.RemoteTargetInput{
		ScopeID: scopeID, TargetID: targetID, DisplayName: "Remote target", AgentKind: skill.CodexAgent,
		InstallationScope: skill.RemoteTargetProjectScope, DeliveryMode: skill.RemoteTargetAgentPull,
		State: state, Generation: 0, CreatedAt: created, UpdatedAt: created,
	}
	switch state {
	case skill.RemoteTargetPending:
		input.EnrollmentCodeDigest = remoteSkillTargetDigestFor(scopeID, targetID)
		input.EnrollmentExpiresAt = created.Add(time.Hour)
	case skill.RemoteTargetActive:
		input.InstallationID = "project-installation"
		input.CredentialSubject = "target-subject"
		input.CredentialVerifier = remoteSkillTargetDigest
		input.ReceiverVersion = "0.1.0"
		input.LastSeenAt = created.Add(time.Minute)
	case skill.RemoteTargetRevoked:
		input.InstallationID = "project-installation"
		input.CredentialSubject = "target-subject"
	}
	target, err := skill.NewRemoteTarget(input)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func remoteSkillTargetDigestFor(scopeID, targetID string) string {
	digest := sha256.Sum256([]byte(scopeID + "\x00" + targetID))
	return hex.EncodeToString(digest[:])
}

func remoteSkillTargetWith(t *testing.T, target skill.RemoteTarget, update func(*skill.RemoteTargetInput)) skill.RemoteTarget {
	t.Helper()
	input := skill.RemoteTargetInput{
		ScopeID: target.ScopeID(), TargetID: target.ID(), DisplayName: target.DisplayName(), AgentKind: target.AgentKind(),
		InstallationScope: target.InstallationScope(), DeliveryMode: target.DeliveryMode(), State: target.State(),
		InstallationID: target.InstallationID(), EnrollmentCodeDigest: target.EnrollmentCodeDigest(), EnrollmentExpiresAt: target.EnrollmentExpiresAt(),
		CredentialSubject: target.CredentialSubject(), CredentialVerifier: target.CredentialVerifier(), ReceiverVersion: target.ReceiverVersion(),
		EnvironmentFingerprint: target.EnvironmentFingerprint(), MachineHostname: target.MachineHostname(), WorkspaceName: target.WorkspaceName(),
		LastSeenAt: target.LastSeenAt(), Generation: target.Generation(), CreatedAt: target.CreatedAt(), UpdatedAt: target.UpdatedAt(),
	}
	update(&input)
	updated, err := skill.NewRemoteTarget(input)
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func createRemoteSkillTarget(t *testing.T, database *sqlstore.Database, repository sqlstore.RemoteSkillTargetRepository, target skill.RemoteTarget) skill.RemoteTarget {
	t.Helper()
	seedRemoteSkillTargetScope(t, database, target.ScopeID())
	stored, err := inRemoteSkillTargetTransaction(t, database, func(tx sqlstore.DBTX) (skill.RemoteTarget, error) {
		return repository.Create(t.Context(), tx, target)
	})
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func getRemoteSkillTarget(t *testing.T, database *sqlstore.Database, repository sqlstore.RemoteSkillTargetRepository, scopeID, targetID string) skill.RemoteTarget {
	t.Helper()
	target, err := inRemoteSkillTargetTransaction(t, database, func(tx sqlstore.DBTX) (skill.RemoteTarget, error) {
		return repository.Get(t.Context(), tx, scopeID, targetID)
	})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func replaceRemoteSkillTarget(t *testing.T, database *sqlstore.Database, repository sqlstore.RemoteSkillTargetRepository, target skill.RemoteTarget, expected int) skill.RemoteTarget {
	t.Helper()
	updated, err := inRemoteSkillTargetTransaction(t, database, func(tx sqlstore.DBTX) (skill.RemoteTarget, error) {
		return repository.Replace(t.Context(), tx, target, expected)
	})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}

func seedRemoteSkillTargetScope(t *testing.T, database *sqlstore.Database, scopeID string) {
	t.Helper()
	_, err := database.SQLDB().ExecContext(t.Context(), `INSERT OR IGNORE INTO pc_scopes (scope_id, title, summary, version)
        VALUES (?, 'Remote target test scope', 'Remote target test scope', 1)`, scopeID)
	if err != nil {
		t.Fatal(err)
	}
}

func openRemoteSkillTargetDatabase(t *testing.T, path string) *sqlstore.Database {
	t.Helper()
	database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func closeRemoteSkillTargetDatabase(t *testing.T, database *sqlstore.Database) {
	t.Helper()
	if err := database.Close(context.Background()); err != nil {
		t.Error(err)
	}
}

func inRemoteSkillTargetTransaction[T any](t *testing.T, database *sqlstore.Database, fn func(sqlstore.DBTX) (T, error)) (T, error) {
	t.Helper()
	var value T
	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var transactionErr error
		value, transactionErr = fn(tx)
		return transactionErr
	})
	return value, err
}
