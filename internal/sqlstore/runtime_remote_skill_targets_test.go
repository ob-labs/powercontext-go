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
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/skill"
	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

// This catches a lost update where independent process-local Runtime locks
// would otherwise each accept the same one-time enrollment code.
func TestRuntimeRemoteSkillTargetEnrollTwoDatabasesHasOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-target-race.db")
	first := openRuntimeRemoteTargetDatabase(t, path)
	second := openRuntimeRemoteTargetDatabase(t, path)
	t.Cleanup(func() { _ = second.Close(context.Background()) })
	t.Cleanup(func() { _ = first.Close(context.Background()) })
	seedRuntimeRemoteTargetScope(t, first, "scope-race")

	clock := func() time.Time { return time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC) }
	creator := newRuntimeRemoteTargetApplication(t, first, clock, "target-race", "enrollment-code", "unused")
	created, err := creator.Create(t.Context(), pcruntime.RemoteSkillTargetCreateInput{
		ScopeID: "scope-race", DisplayName: "Race", AgentKind: skill.CodexAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.EnrollmentCode != "enrollment-code" {
		t.Fatalf("Create enrollment code = %q", created.EnrollmentCode)
	}

	firstRuntime := newRuntimeRemoteTargetApplication(t, first, clock, "unused", "unused", "credential-one")
	secondRuntime := newRuntimeRemoteTargetApplication(t, second, clock, "unused", "unused", "credential-two")
	start := make(chan struct{})
	errorsByAttempt := make(chan error, 2)
	var group sync.WaitGroup
	group.Go(func() {
		<-start
		_, attemptErr := firstRuntime.Enroll(t.Context(), pcruntime.RemoteSkillTargetEnrollInput{
			EnrollmentCode: "enrollment-code", InstallationID: "install-one", CredentialSubject: "subject-one", ReceiverVersion: "1.0.0",
		})
		errorsByAttempt <- attemptErr
	})
	group.Go(func() {
		<-start
		_, attemptErr := secondRuntime.Enroll(t.Context(), pcruntime.RemoteSkillTargetEnrollInput{
			EnrollmentCode: "enrollment-code", InstallationID: "install-two", CredentialSubject: "subject-two", ReceiverVersion: "1.0.0",
		})
		errorsByAttempt <- attemptErr
	})
	close(start)
	group.Wait()
	close(errorsByAttempt)

	successes, refusals := 0, 0
	for attemptErr := range errorsByAttempt {
		if attemptErr == nil {
			successes++
			continue
		}
		if _, ok := errors.AsType[*pcruntime.RemoteSkillTargetEnrollmentRefusedError](attemptErr); ok {
			refusals++
			continue
		}
		t.Fatalf("Enroll error = %T %v", attemptErr, attemptErr)
	}
	if successes != 1 || refusals != 1 {
		t.Fatalf("successful enrollments=%d refusals=%d, want 1,1", successes, refusals)
	}
}

// This catches authorization from a stale lookup snapshot. The repository must
// recheck the pending state, digest, generation, and expiry in its write.
func TestRuntimeRemoteSkillTargetConsumePendingRequiresCurrentEnrollmentState(t *testing.T) {
	database := openRuntimeRemoteTargetDatabase(t, filepath.Join(t.TempDir(), "remote-target-consume.db"))
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	seedRuntimeRemoteTargetScope(t, database, "scope-consume")

	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	target := runtimeRemoteTarget(t, "scope-consume", "target-consume", "enrollment-code", now.Add(time.Minute))
	repository := sqlstore.RemoteSkillTargetRepository{}
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, err := repository.Create(t.Context(), tx, target)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	next := runtimeRemoteTargetActive(t, target, "credential")

	var consumed skill.RemoteTarget
	var matched bool
	err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var consumeErr error
		consumed, matched, consumeErr = repository.ConsumePending(
			t.Context(), tx, next, runtimeRemoteTargetDigest("enrollment-code"), now,
		)
		return consumeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if !matched || consumed.State() != skill.RemoteTargetActive || consumed.Generation() != target.Generation()+1 {
		t.Fatalf("ConsumePending = (%v, %t), want current pending target consumed", consumed, matched)
	}

	err = database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var consumeErr error
		_, matched, consumeErr = repository.ConsumePending(
			t.Context(), tx, next, runtimeRemoteTargetDigest("enrollment-code"), now,
		)
		return consumeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatal("ConsumePending accepted a stale active target")
	}

	for _, test := range []struct {
		name         string
		targetID     string
		expiresAt    time.Time
		suppliedCode string
	}{
		{
			name: "digest", targetID: "target-wrong-digest", expiresAt: now.Add(time.Minute), suppliedCode: "wrong-code",
		},
		{
			name: "expiry", targetID: "target-expired", expiresAt: now, suppliedCode: "target-expired-code",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			pending := runtimeRemoteTarget(t, "scope-consume", test.targetID, test.targetID+"-code", test.expiresAt)
			if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
				_, createErr := repository.Create(t.Context(), tx, pending)
				return createErr
			}); err != nil {
				t.Fatal(err)
			}
			next := runtimeRemoteTargetActive(t, pending, test.targetID+"-credential")
			var consumed bool
			err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
				_, consumed, consumeErr = repository.ConsumePending(
					t.Context(), tx, next, runtimeRemoteTargetDigest(test.suppliedCode), now,
				)
				if consumeErr != nil {
					return consumeErr
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if consumed {
				t.Fatal("ConsumePending accepted an unqualified pending row")
			}
		})
	}
}

func TestRuntimeRemoteSkillTargetEnrollmentIsOneTimeAndClearsSecretsOnTransitions(t *testing.T) {
	database := openRuntimeRemoteTargetDatabase(t, filepath.Join(t.TempDir(), "remote-target-lifecycle.db"))
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	seedRuntimeRemoteTargetScope(t, database, "scope-lifecycle")
	clock := func() time.Time { return time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC) }
	creator := newRuntimeRemoteTargetApplication(t, database, clock, "target-lifecycle", "enrollment-code", "unused")
	created, err := creator.Create(t.Context(), pcruntime.RemoteSkillTargetCreateInput{
		ScopeID: "scope-lifecycle", DisplayName: "Lifecycle", AgentKind: skill.WorkBuddyAgent,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeRemoteTargetDoesNotRender(t, created.Target, "enrollment-code", runtimeRemoteTargetDigest("enrollment-code"))
	assertRuntimeRemoteTargetSecretColumns(t, database, "scope-lifecycle", "target-lifecycle", true, false)

	enroller := newRuntimeRemoteTargetApplication(t, database, clock, "unused", "unused", "credential")
	enrolled, err := enroller.Enroll(t.Context(), pcruntime.RemoteSkillTargetEnrollInput{
		EnrollmentCode: "enrollment-code", InstallationID: "installation", CredentialSubject: "subject", ReceiverVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if enrolled.Credential != "credential" || enrolled.Target.State() != skill.RemoteTargetActive || enrolled.Target.Generation() != 1 {
		t.Fatalf("Enroll result = %#v, want active target and one issued credential", enrolled)
	}
	assertRuntimeRemoteTargetDoesNotRender(t, enrolled.Target, "enrollment-code", "credential", runtimeRemoteTargetDigest("credential"))
	assertRuntimeRemoteTargetSecretColumns(t, database, "scope-lifecycle", "target-lifecycle", false, true)

	_, err = enroller.Enroll(t.Context(), pcruntime.RemoteSkillTargetEnrollInput{
		EnrollmentCode: "enrollment-code", InstallationID: "other", CredentialSubject: "other", ReceiverVersion: "1.0.0",
	})
	if _, ok := errors.AsType[*pcruntime.RemoteSkillTargetEnrollmentRefusedError](err); !ok {
		t.Fatalf("second Enroll error = %T %v, want redacted refusal", err, err)
	}
	assertRuntimeRemoteTargetDoesNotRender(t, err, "enrollment-code", runtimeRemoteTargetDigest("enrollment-code"))

	revoked, err := enroller.Revoke(t.Context(), pcruntime.RemoteSkillTargetRevokeInput{
		ScopeID: "scope-lifecycle", TargetID: "target-lifecycle", ExpectedGeneration: enrolled.Target.Generation(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if revoked.State() != skill.RemoteTargetRevoked || revoked.Generation() != 2 || revoked.InstallationID() != "installation" || revoked.CredentialSubject() != "subject" {
		t.Fatalf("Revoke result = %#v, want identity-preserving revoked target", revoked)
	}
	assertRuntimeRemoteTargetDoesNotRender(t, revoked, "enrollment-code", "credential", runtimeRemoteTargetDigest("credential"))
	assertRuntimeRemoteTargetSecretColumns(t, database, "scope-lifecycle", "target-lifecycle", false, false)
}

func TestRuntimeRemoteSkillTargetExpiredEnrollmentDoesNotIssueCredential(t *testing.T) {
	database := openRuntimeRemoteTargetDatabase(t, filepath.Join(t.TempDir(), "remote-target-expired.db"))
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	seedRuntimeRemoteTargetScope(t, database, "scope-expired")
	createdAt := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	creator := newRuntimeRemoteTargetApplication(t, database, func() time.Time { return createdAt }, "target-expired", "expired-code", "unused")
	if _, err := creator.Create(t.Context(), pcruntime.RemoteSkillTargetCreateInput{
		ScopeID: "scope-expired", DisplayName: "Expired", AgentKind: skill.CodexAgent,
	}); err != nil {
		t.Fatal(err)
	}
	credentialCalls := 0
	enroller := newRuntimeRemoteTargetApplicationWithSecret(t, database, func() time.Time {
		return createdAt.Add(16 * time.Minute)
	}, "unused", func() string {
		credentialCalls++
		return "credential"
	})

	_, err := enroller.Enroll(t.Context(), pcruntime.RemoteSkillTargetEnrollInput{
		EnrollmentCode: "expired-code", InstallationID: "installation", CredentialSubject: "subject", ReceiverVersion: "1.0.0",
	})
	if _, ok := errors.AsType[*pcruntime.RemoteSkillTargetEnrollmentRefusedError](err); !ok {
		t.Fatalf("Enroll error = %T %v, want redacted refusal", err, err)
	}
	if credentialCalls != 0 {
		t.Fatalf("expired enrollment issued %d credentials, want 0", credentialCalls)
	}
}

func TestRuntimeRemoteSkillTargetLookupStorageFailureIsRedactedAndPropagated(t *testing.T) {
	database := openRuntimeRemoteTargetDatabase(t, filepath.Join(t.TempDir(), "remote-target-storage.db"))
	application := newRuntimeRemoteTargetApplication(t, database, time.Now, "unused", "unused", "credential")
	if err := database.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err := application.Enroll(t.Context(), pcruntime.RemoteSkillTargetEnrollInput{EnrollmentCode: "private-enrollment-code"})
	if _, ok := errors.AsType[*sqlstore.RemoteSkillTargetStorageError](err); !ok {
		t.Fatalf("Enroll error = %T %v, want redacted storage error", err, err)
	}
	assertRuntimeRemoteTargetDoesNotRender(t, err, "private-enrollment-code", "remote-target-storage.db")
}

func TestRuntimeRemoteSkillTargetRenameAndRevokePreserveGenerationContracts(t *testing.T) {
	database := openRuntimeRemoteTargetDatabase(t, filepath.Join(t.TempDir(), "remote-target-generation.db"))
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	seedRuntimeRemoteTargetScope(t, database, "scope-generation")
	clock := func() time.Time { return time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC) }
	application := newRuntimeRemoteTargetApplication(t, database, clock, "target-generation", "generation-code", "unused")
	created, err := application.Create(t.Context(), pcruntime.RemoteSkillTargetCreateInput{
		ScopeID: "scope-generation", DisplayName: "Original", AgentKind: skill.CodexAgent,
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = application.Rename(t.Context(), pcruntime.RemoteSkillTargetRenameInput{
		ScopeID: "scope-generation", TargetID: "missing", DisplayName: "Missing", ExpectedGeneration: 0,
	})
	if _, ok := errors.AsType[*skill.RemoteTargetNotFoundError](err); !ok {
		t.Fatalf("missing Rename error = %T %v, want target not found", err, err)
	}
	_, err = application.Rename(t.Context(), pcruntime.RemoteSkillTargetRenameInput{
		ScopeID: "scope-generation", TargetID: created.Target.TargetID(), DisplayName: "Updated", ExpectedGeneration: 1,
	})
	if _, ok := errors.AsType[*skill.RemoteTargetGenerationConflictError](err); !ok {
		t.Fatalf("stale Rename error = %T %v, want generation conflict", err, err)
	}

	renamed, err := application.Rename(t.Context(), pcruntime.RemoteSkillTargetRenameInput{
		ScopeID: "scope-generation", TargetID: created.Target.TargetID(), DisplayName: "Updated", ExpectedGeneration: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Generation() != 1 || renamed.DisplayName() != "Updated" {
		t.Fatalf("Rename result = %#v, want generation 1 renamed target", renamed)
	}
	sameName, err := application.Rename(t.Context(), pcruntime.RemoteSkillTargetRenameInput{
		ScopeID: "scope-generation", TargetID: created.Target.TargetID(), DisplayName: "Updated", ExpectedGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sameName.Generation() != 1 {
		t.Fatalf("same-name Rename generation = %d, want 1", sameName.Generation())
	}

	revoked, err := application.Revoke(t.Context(), pcruntime.RemoteSkillTargetRevokeInput{
		ScopeID: "scope-generation", TargetID: created.Target.TargetID(), ExpectedGeneration: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if revoked.Generation() != 2 || revoked.State() != skill.RemoteTargetRevoked {
		t.Fatalf("Revoke result = %#v, want revoked generation 2", revoked)
	}
	sameRevoke, err := application.Revoke(t.Context(), pcruntime.RemoteSkillTargetRevokeInput{
		ScopeID: "scope-generation", TargetID: created.Target.TargetID(), ExpectedGeneration: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sameRevoke.Generation() != 2 {
		t.Fatalf("idempotent Revoke generation = %d, want 2", sameRevoke.Generation())
	}
	_, err = application.Revoke(t.Context(), pcruntime.RemoteSkillTargetRevokeInput{
		ScopeID: "scope-generation", TargetID: created.Target.TargetID(), ExpectedGeneration: 1,
	})
	if _, ok := errors.AsType[*skill.RemoteTargetGenerationConflictError](err); !ok {
		t.Fatalf("stale Revoke error = %T %v, want generation conflict", err, err)
	}
}

func runtimeRemoteTarget(t *testing.T, scopeID, targetID, enrollmentCode string, expiresAt time.Time) skill.RemoteTarget {
	t.Helper()
	createdAt := expiresAt.Add(-time.Minute)
	target, err := skill.NewRemoteTarget(skill.RemoteTargetInput{
		ScopeID: scopeID, TargetID: targetID, DisplayName: "Remote target", AgentKind: skill.CodexAgent,
		InstallationScope: skill.RemoteTargetProjectScope, DeliveryMode: skill.RemoteTargetAgentPull,
		State: skill.RemoteTargetPending, EnrollmentCodeDigest: runtimeRemoteTargetDigest(enrollmentCode),
		EnrollmentExpiresAt: expiresAt, CreatedAt: createdAt, UpdatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func runtimeRemoteTargetActive(t *testing.T, target skill.RemoteTarget, credential string) skill.RemoteTarget {
	t.Helper()
	updatedAt := target.UpdatedAt().Add(time.Second)
	next, err := skill.NewRemoteTarget(skill.RemoteTargetInput{
		ScopeID: target.ScopeID(), TargetID: target.ID(), DisplayName: target.DisplayName(), AgentKind: target.AgentKind(),
		InstallationScope: target.InstallationScope(), DeliveryMode: target.DeliveryMode(), State: skill.RemoteTargetActive,
		InstallationID: "installation", CredentialSubject: "subject", CredentialVerifier: runtimeRemoteTargetDigest(credential),
		ReceiverVersion: "1.0.0", LastSeenAt: updatedAt, Generation: target.Generation() + 1,
		CreatedAt: target.CreatedAt(), UpdatedAt: updatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func runtimeRemoteTargetDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func openRuntimeRemoteTargetDatabase(t *testing.T, path string) *sqlstore.Database {
	t.Helper()
	database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func seedRuntimeRemoteTargetScope(t *testing.T, database *sqlstore.Database, scopeID string) {
	t.Helper()
	repository := sqlstore.ScopeRepository{}
	descriptor, err := scope.NewDescriptor(scopeID, "remote target scope", time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error { return repository.Create(t.Context(), tx, descriptor) }); err != nil {
		t.Fatal(err)
	}
}

func newRuntimeRemoteTargetApplication(t *testing.T, database *sqlstore.Database, clock func() time.Time, id, enrollmentCode, credential string) *pcruntime.RemoteSkillTargetApplication {
	t.Helper()
	return newRuntimeRemoteTargetApplicationWithSecret(t, database, clock, id, func() string {
		if enrollmentCode != "unused" {
			return enrollmentCode
		}
		return credential
	})
}

func newRuntimeRemoteTargetApplicationWithSecret(t *testing.T, database *sqlstore.Database, clock func() time.Time, id string, newSecret func() string) *pcruntime.RemoteSkillTargetApplication {
	t.Helper()
	store, err := sqlstore.NewRuntimeRemoteSkillTargetStore(database, sqlstore.RemoteSkillTargetRepository{})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sqlstore.NewRuntimeScopeReader(database, sqlstore.ScopeRepository{})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := pcruntime.NewConfigured(pcruntime.RuntimeOptions{ScopeReader: reader}, nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := pcruntime.NewRemoteSkillTargetApplication(lifecycle, store, pcruntime.RemoteSkillTargetApplicationOptions{
		Clock: clock, NewID: func() string { return id }, NewSecret: newSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func assertRuntimeRemoteTargetSecretColumns(t *testing.T, database *sqlstore.Database, scopeID, targetID string, wantEnrollment, wantCredential bool) {
	t.Helper()
	var enrollmentDigest, enrollmentExpiry, credentialVerifier sql.NullString
	err := database.SQLDB().QueryRowContext(t.Context(), `SELECT enrollment_code_digest, enrollment_expires_at, credential_verifier
        FROM pc_agent_skill_targets WHERE scope_id = ? AND target_id = ?`, scopeID, targetID).Scan(
		&enrollmentDigest, &enrollmentExpiry, &credentialVerifier,
	)
	if err != nil {
		t.Fatal(err)
	}
	if enrollmentDigest.Valid != wantEnrollment || enrollmentExpiry.Valid != wantEnrollment || credentialVerifier.Valid != wantCredential {
		t.Fatalf("secret columns = enrollment digest %t expiry %t credential verifier %t, want enrollment %t credential %t",
			enrollmentDigest.Valid, enrollmentExpiry.Valid, credentialVerifier.Valid, wantEnrollment, wantCredential)
	}
}

func assertRuntimeRemoteTargetDoesNotRender(t *testing.T, value any, protected ...string) {
	t.Helper()
	rendered := strings.ToLower(fmt.Sprintf("%v", value))
	for _, secret := range protected {
		if strings.Contains(rendered, strings.ToLower(secret)) {
			t.Fatalf("rendered value %q exposes protected material", rendered)
		}
	}
}
