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

const preRequiredFieldsRemoteSkillTargetSchema = `CREATE TABLE pc_agent_skill_targets (
    scope_id VARCHAR(256) NOT NULL,
    target_id VARCHAR(64) NOT NULL,
    display_name VARCHAR(128) NOT NULL,
    agent_kind VARCHAR(16) NOT NULL,
    installation_scope VARCHAR(16) NOT NULL,
    delivery_mode VARCHAR(16) NOT NULL,
    state VARCHAR(16) NOT NULL,
    installation_id VARCHAR(128),
    enrollment_code_digest VARCHAR(64),
    enrollment_expires_at TEXT,
    credential_subject VARCHAR(128),
    credential_verifier VARCHAR(64),
    receiver_version VARCHAR(64),
    environment_fingerprint VARCHAR(64),
    machine_hostname VARCHAR(255),
    workspace_name VARCHAR(128),
    last_seen_at TEXT,
    generation INTEGER NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (scope_id, target_id),
    CONSTRAINT fk_pc_agent_skill_targets_scope FOREIGN KEY (scope_id)
        REFERENCES pc_scopes (scope_id) ON DELETE RESTRICT,
    CONSTRAINT uq_pc_agent_skill_targets_enrollment_digest UNIQUE (enrollment_code_digest),
    CONSTRAINT uq_pc_agent_skill_targets_credential_subject UNIQUE (credential_subject),
    CONSTRAINT uq_pc_agent_skill_targets_credential_verifier UNIQUE (credential_verifier),
    CONSTRAINT uq_pc_agent_skill_targets_installation UNIQUE (
        scope_id, agent_kind, installation_scope, installation_id
    ),
    CONSTRAINT ck_pc_agent_skill_targets_agent_kind CHECK (agent_kind IN ('codex', 'workbuddy')),
    CONSTRAINT ck_pc_agent_skill_targets_installation_scope CHECK (installation_scope = 'project'),
    CONSTRAINT ck_pc_agent_skill_targets_delivery_mode CHECK (delivery_mode = 'agent_pull'),
    CONSTRAINT ck_pc_agent_skill_targets_generation_nonnegative CHECK (generation >= 0),
    CONSTRAINT ck_pc_agent_skill_targets_state_payload CHECK (
        (
            state = 'pending'
            AND length(enrollment_code_digest) = 64
            AND enrollment_code_digest NOT GLOB '*[^0-9a-f]*'
            AND length(enrollment_expires_at) > 0
            AND installation_id IS NULL
            AND credential_subject IS NULL
            AND credential_verifier IS NULL
            AND receiver_version IS NULL
            AND environment_fingerprint IS NULL
            AND machine_hostname IS NULL
            AND workspace_name IS NULL
            AND last_seen_at IS NULL
        ) OR (
            state = 'active'
            AND enrollment_code_digest IS NULL
            AND enrollment_expires_at IS NULL
            AND length(installation_id) > 0
            AND length(credential_subject) > 0
            AND length(credential_verifier) = 64
            AND credential_verifier NOT GLOB '*[^0-9a-f]*'
            AND length(receiver_version) > 0
            AND length(last_seen_at) > 0
        ) OR (
            state = 'revoked'
            AND enrollment_code_digest IS NULL
            AND enrollment_expires_at IS NULL
            AND credential_verifier IS NULL
            AND (
                (installation_id IS NULL AND credential_subject IS NULL) OR
                (length(installation_id) > 0 AND length(credential_subject) > 0)
            )
        )
    )
)`

func TestRemoteSkillTargetRepositoryPersistsAcrossRestartWithoutRawSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-targets.db")
	first := openRemoteSkillTargetDatabase(t, path)
	repository := sqlstore.RemoteSkillTargetRepository{}
	const rawEnrollmentCode = "enrollment-code-secret"
	const rawTargetCredential = "target-credential-secret"
	enrollmentDigest := sha256.Sum256([]byte(rawEnrollmentCode))
	credentialDigest := sha256.Sum256([]byte(rawTargetCredential))
	pending := remoteSkillTargetWith(t, remoteSkillTarget(t, "scope-target-restart", "codex-restart", skill.RemoteTargetPending), func(input *skill.RemoteTargetInput) {
		input.EnrollmentCodeDigest = hex.EncodeToString(enrollmentDigest[:])
	})
	active := remoteSkillTargetWith(t, remoteSkillTarget(t, "scope-target-restart", "workbuddy-restart", skill.RemoteTargetActive), func(input *skill.RemoteTargetInput) {
		input.CredentialVerifier = hex.EncodeToString(credentialDigest[:])
	})

	stored := createRemoteSkillTarget(t, first, repository, pending)
	if stored.ID() != pending.ID() || stored.EnrollmentCodeDigest() != pending.EnrollmentCodeDigest() {
		t.Fatalf("Create() = %#v, want durable pending target", stored)
	}
	createRemoteSkillTarget(t, first, repository, active)
	closeRemoteSkillTargetDatabase(t, first)

	second := openRemoteSkillTargetDatabase(t, path)
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, second) })
	got := getRemoteSkillTarget(t, second, repository, pending.ScopeID(), pending.ID())
	if got.ID() != pending.ID() || got.State() != skill.RemoteTargetPending || got.Generation() != pending.Generation() {
		t.Fatalf("Get() after restart = %#v, want persisted target", got)
	}
	var matched skill.RemoteTarget
	var found bool
	err := second.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var findErr error
		matched, found, findErr = repository.FindByEnrollmentDigest(t.Context(), tx, pending.EnrollmentCodeDigest())
		return findErr
	})
	if err != nil || !found || matched.ID() != pending.ID() {
		t.Fatalf("FindByEnrollmentDigest() = (%#v, %t, %v), want stored target", matched, found, err)
	}
	var persistedEnrollmentDigest, persistedCredentialVerifier string
	if err := second.SQLDB().QueryRowContext(t.Context(), `SELECT enrollment_code_digest FROM pc_agent_skill_targets WHERE target_id = ?`, pending.ID()).Scan(&persistedEnrollmentDigest); err != nil {
		t.Fatal(err)
	}
	if err := second.SQLDB().QueryRowContext(t.Context(), `SELECT credential_verifier FROM pc_agent_skill_targets WHERE target_id = ?`, active.ID()).Scan(&persistedCredentialVerifier); err != nil {
		t.Fatal(err)
	}
	if persistedEnrollmentDigest != pending.EnrollmentCodeDigest() || persistedCredentialVerifier != active.CredentialVerifier() {
		t.Fatalf("persisted secret digests = (%q, %q), want supplied SHA-256 digests", persistedEnrollmentDigest, persistedCredentialVerifier)
	}

	file, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, rawSecret := range []string{rawEnrollmentCode, rawTargetCredential} {
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

func TestSQLiteSkillDistributionSchemaRejectsOmittedRequiredStateFields(t *testing.T) {
	database := openRemoteSkillTargetDatabase(t, filepath.Join(t.TempDir(), "required-state-fields.db"))
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	seedRemoteSkillTargetScope(t, database, "scope-target-required-state")
	assertRemoteSkillTargetRequiredStateFieldsRejected(t, database, "scope-target-required-state")
}

func TestOpenSQLiteUpgradesPreRequiredFieldsTargetSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-required-fields-targets.db")
	seedLegacyRemoteSkillTargetSchema(t, path)

	database := openRemoteSkillTargetDatabase(t, path)
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, database) })
	seedRemoteSkillTargetScope(t, database, "scope-target-upgrade-required-state")
	assertRemoteSkillTargetRequiredStateFieldsRejected(t, database, "scope-target-upgrade-required-state")
}

func TestOpenSQLiteRejectsInvalidLegacyRemoteTargetWithoutPartialSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-pre-required-fields-targets.db")
	database := openRemoteSkillTargetDatabase(t, path)
	const scopeID = "legacy-scope"
	seedRemoteSkillTargetScope(t, database, scopeID)
	if _, err := database.SQLDB().ExecContext(t.Context(), "DROP TABLE pc_agent_skill_targets"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SQLDB().ExecContext(t.Context(), preRequiredFieldsRemoteSkillTargetSchema); err != nil {
		t.Fatal(err)
	}
	_, err := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_agent_skill_targets (
        scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state,
        credential_subject, credential_verifier, receiver_version, last_seen_at, generation, created_at, updated_at
    ) VALUES (?, 'legacy-invalid-active', 'Legacy invalid active', 'codex', 'project', 'agent_pull', 'active',
        'legacy-subject', ?, '0.1.0', ?, 0, ?, ?)`, scopeID, remoteSkillTargetDigest,
		time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	closeRemoteSkillTargetDatabase(t, database)

	if database, openErr := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path)); openErr == nil {
		_ = database.Close(context.Background())
		t.Fatal("OpenSQLite succeeded with an invalid legacy remote target")
	}

	verify, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = verify.Close() })
	var tableSQL string
	if err := verify.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'pc_agent_skill_targets'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tableSQL, "installation_id IS NOT NULL") {
		t.Fatal("failed legacy upgrade replaced the original target table")
	}
	var legacyRows int
	if err := verify.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pc_agent_skill_targets WHERE target_id = 'legacy-invalid-active'`).Scan(&legacyRows); err != nil || legacyRows != 1 {
		t.Fatalf("legacy target rows = %d, err=%v; want original invalid row", legacyRows, err)
	}
	var scopes int
	if err := verify.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_scopes WHERE scope_id = ?", scopeID).Scan(&scopes); err != nil || scopes != 1 {
		t.Fatalf("legacy Scope rows = %d, err=%v; want original Scope", scopes, err)
	}
	var temporaryTables int
	if err := verify.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM sqlite_master WHERE name = ?", "pc_agent_skill_targets_legacy_state_payload").Scan(&temporaryTables); err != nil || temporaryTables != 0 {
		t.Fatalf("failed upgrade temporary tables = %d, err=%v; want none", temporaryTables, err)
	}
}

func TestOpenSQLiteUpgradesLegacyRemoteTargetPreservingEveryColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-target-columns.db")
	database := openRemoteSkillTargetDatabase(t, path)
	const scopeID = "legacy-column-scope"
	seedRemoteSkillTargetScope(t, database, scopeID)
	if _, err := database.SQLDB().ExecContext(t.Context(), "DROP TABLE pc_agent_skill_targets"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SQLDB().ExecContext(t.Context(), preRequiredFieldsRemoteSkillTargetSchema); err != nil {
		t.Fatal(err)
	}
	const (
		targetID        = "legacy-active-columns"
		installationID  = "legacy-installation"
		credentialSub   = "legacy-subject"
		receiverVersion = "1.2.3"
		environment     = "legacy-environment"
		hostname        = "legacy-host"
		workspace       = "legacy-workspace"
		lastSeen        = "2026-09-09T14:01:00Z"
		createdAt       = "2026-09-09T14:00:00Z"
		updatedAt       = "2026-09-09T14:02:00Z"
	)
	if _, err := database.SQLDB().ExecContext(t.Context(), `INSERT INTO pc_agent_skill_targets (
        scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state,
        installation_id, enrollment_code_digest, enrollment_expires_at, credential_subject,
        credential_verifier, receiver_version, environment_fingerprint, machine_hostname,
        workspace_name, last_seen_at, generation, created_at, updated_at
    ) VALUES (?, ?, 'Legacy active', 'codex', 'project', 'agent_pull', 'active', ?, NULL, NULL, ?, ?, ?, ?, ?, ?, ?, 7, ?, ?)`,
		scopeID, targetID, installationID, credentialSub, remoteSkillTargetDigest, receiverVersion,
		environment, hostname, workspace, lastSeen, createdAt, updatedAt); err != nil {
		t.Fatal(err)
	}
	closeRemoteSkillTargetDatabase(t, database)

	upgraded := openRemoteSkillTargetDatabase(t, path)
	t.Cleanup(func() { closeRemoteSkillTargetDatabase(t, upgraded) })
	var copied int
	if err := upgraded.SQLDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM pc_agent_skill_targets
        WHERE scope_id = ? AND target_id = ? AND display_name = 'Legacy active'
          AND agent_kind = 'codex' AND installation_scope = 'project' AND delivery_mode = 'agent_pull'
          AND state = 'active' AND installation_id = ?
          AND enrollment_code_digest IS NULL AND enrollment_expires_at IS NULL
          AND credential_subject = ? AND credential_verifier = ? AND receiver_version = ?
          AND environment_fingerprint = ? AND machine_hostname = ? AND workspace_name = ?
          AND last_seen_at = ? AND generation = 7 AND created_at = ? AND updated_at = ?`,
		scopeID, targetID, installationID, credentialSub, remoteSkillTargetDigest, receiverVersion,
		environment, hostname, workspace, lastSeen, createdAt, updatedAt).Scan(&copied); err != nil || copied != 1 {
		t.Fatalf("upgraded legacy target rows = %d, err=%v; want every persisted column", copied, err)
	}
}

func TestOpenSQLiteKeepsLegacyTargetWhenMigrationTemporaryTableExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-target-temporary-table.db")
	database := openRemoteSkillTargetDatabase(t, path)
	const scopeID = "legacy-temporary-table-scope"
	seedRemoteSkillTargetScope(t, database, scopeID)
	for _, statement := range []string{
		"DROP TABLE pc_agent_skill_targets",
		preRequiredFieldsRemoteSkillTargetSchema,
		"CREATE TABLE pc_agent_skill_targets_legacy_state_payload (value TEXT NOT NULL)",
		"INSERT INTO pc_agent_skill_targets_legacy_state_payload (value) VALUES ('sentinel')",
	} {
		if _, err := database.SQLDB().ExecContext(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}
	closeRemoteSkillTargetDatabase(t, database)

	if database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path)); err == nil {
		_ = database.Close(context.Background())
		t.Fatal("OpenSQLite accepted a legacy migration temporary table conflict")
	}

	verify, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = verify.Close() })
	var tableSQL string
	if err := verify.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'pc_agent_skill_targets'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(tableSQL, "installation_id IS NOT NULL") {
		t.Fatal("temporary-table conflict replaced the original target table")
	}
	var sentinelRows int
	if err := verify.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_agent_skill_targets_legacy_state_payload WHERE value = 'sentinel'").Scan(&sentinelRows); err != nil || sentinelRows != 1 {
		t.Fatalf("temporary-table sentinel rows = %d, err=%v; want original row", sentinelRows, err)
	}
	var scopes int
	if err := verify.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_scopes WHERE scope_id = ?", scopeID).Scan(&scopes); err != nil || scopes != 1 {
		t.Fatalf("Scope rows = %d, err=%v; want original Scope", scopes, err)
	}
}

func assertRemoteSkillTargetRequiredStateFieldsRejected(t *testing.T, database *sqlstore.Database, scopeID string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)

	tests := []struct {
		name    string
		columns string
		values  string
		args    []any
	}{
		{
			name:    "pending enrollment code digest",
			columns: "scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state, enrollment_expires_at, generation, created_at, updated_at",
			values:  "?, ?, 'Missing pending digest', 'codex', 'project', 'agent_pull', 'pending', ?, 0, ?, ?",
			args:    []any{scopeID, "pending-missing-digest", expires, now, now},
		},
		{
			name:    "pending enrollment expiry",
			columns: "scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state, enrollment_code_digest, generation, created_at, updated_at",
			values:  "?, ?, 'Missing pending expiry', 'codex', 'project', 'agent_pull', 'pending', ?, 0, ?, ?",
			args:    []any{scopeID, "pending-missing-expiry", remoteSkillTargetDigest, now, now},
		},
		{
			name:    "active installation ID",
			columns: "scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state, credential_subject, credential_verifier, receiver_version, last_seen_at, generation, created_at, updated_at",
			values:  "?, ?, 'Missing active installation', 'codex', 'project', 'agent_pull', 'active', 'target-subject', ?, '0.1.0', ?, 0, ?, ?",
			args:    []any{scopeID, "active-missing-installation", remoteSkillTargetDigest, now, now, now},
		},
		{
			name:    "active credential subject",
			columns: "scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state, installation_id, credential_verifier, receiver_version, last_seen_at, generation, created_at, updated_at",
			values:  "?, ?, 'Missing active subject', 'codex', 'project', 'agent_pull', 'active', 'project-installation', ?, '0.1.0', ?, 0, ?, ?",
			args:    []any{scopeID, "active-missing-subject", remoteSkillTargetDigest, now, now, now},
		},
		{
			name:    "active credential verifier",
			columns: "scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state, installation_id, credential_subject, receiver_version, last_seen_at, generation, created_at, updated_at",
			values:  "?, ?, 'Missing active verifier', 'codex', 'project', 'agent_pull', 'active', 'project-installation', 'target-subject', '0.1.0', ?, 0, ?, ?",
			args:    []any{scopeID, "active-missing-verifier", now, now, now},
		},
		{
			name:    "active receiver version",
			columns: "scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state, installation_id, credential_subject, credential_verifier, last_seen_at, generation, created_at, updated_at",
			values:  "?, ?, 'Missing active receiver', 'codex', 'project', 'agent_pull', 'active', 'project-installation', 'target-subject', ?, ?, 0, ?, ?",
			args:    []any{scopeID, "active-missing-receiver", remoteSkillTargetDigest, now, now, now},
		},
		{
			name:    "active last seen",
			columns: "scope_id, target_id, display_name, agent_kind, installation_scope, delivery_mode, state, installation_id, credential_subject, credential_verifier, receiver_version, generation, created_at, updated_at",
			values:  "?, ?, 'Missing active last seen', 'codex', 'project', 'agent_pull', 'active', 'project-installation', 'target-subject', ?, '0.1.0', 0, ?, ?",
			args:    []any{scopeID, "active-missing-last-seen", remoteSkillTargetDigest, now, now},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := "INSERT INTO pc_agent_skill_targets (" + test.columns + ") VALUES (" + test.values + ")"
			_, err := database.SQLDB().ExecContext(t.Context(), query, test.args...)
			var sqliteErr sqlite3.Error
			if !errors.As(err, &sqliteErr) || sqliteErr.Code != sqlite3.ErrConstraint {
				t.Fatalf("direct insert omitting %s = %T %v, want SQLite CHECK refusal", test.name, err, err)
			}
		})
	}
}

func seedLegacyRemoteSkillTargetSchema(t *testing.T, path string) {
	t.Helper()
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(t.Context(), preRequiredFieldsRemoteSkillTargetSchema); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
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
