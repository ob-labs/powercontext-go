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
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestRuntimeSkillUsageSQLiteVerifiesExactPackageBeforeIdempotentSourceWrite(t *testing.T) {
	database := openTestDatabase(t)
	snapshot := skillPackageSnapshot(t, "usage-package")
	skillSnapshot := writeRuntimeSkillPackage(t, database, "usage-scope", snapshot, nil)
	sources, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec(), sqlstore.SkillUsageSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	taskCapture, err := source.NewContentCapture("usage-task", "completed task", nil)
	if err != nil {
		t.Fatal(err)
	}
	task, err := (source.ContentAdapter{}).Resolve(t.Context(), taskCapture)
	if err != nil {
		t.Fatal(err)
	}
	var taskRef source.Ref
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		stored, addErr := sources.Add(t.Context(), tx, "usage-scope", task)
		taskRef = stored.Ref
		return addErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	recorder, err := sqlstore.NewRuntimeSkillUsageRecorder(database, sources)
	if err != nil {
		t.Fatal(err)
	}
	skillRef, err := source.NewSkillUsageArtifactReference(
		skillSnapshot.Ref().Family(),
		skillSnapshot.Ref().ID(),
		skillSnapshot.Ref().Revision(),
	)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + snapshot.Reference().TreeDigest()
	validUsage, err := source.NewSkillUsageCapture(
		"usage-observation",
		skillRef,
		digest,
		"codex-target",
		true,
		source.ObservedInvocationTrue,
		source.ObservedValidationPassed,
		source.ObservedOutcomeSuccess,
		&taskRef,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	invalidDigest, err := source.NewSkillUsageCapture(
		"rejected-observation",
		skillRef,
		"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"codex-target",
		true,
		source.ObservedInvocationTrue,
		source.ObservedValidationPassed,
		source.ObservedOutcomeSuccess,
		&taskRef,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, recordErr := recorder.Record(t.Context(), "usage-scope", invalidDigest); recordErr == nil {
		t.Fatal("mismatched package digest was accepted")
	} else if _, ok := errors.AsType[*source.InvalidSkillUsageError](recordErr); !ok {
		t.Fatalf("mismatched digest error = %T %v", recordErr, recordErr)
	}
	if count := skillUsageSourceCount(t, database); count != 1 {
		t.Fatalf("failed package validation wrote a Source: count=%d", count)
	}
	firstRef, firstPosition, recordErr := recorder.Record(t.Context(), "usage-scope", validUsage)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	if firstRef.Type() != source.SkillUsageType || firstRef.ID() != "usage-observation" || firstPosition != 2 {
		t.Fatalf("first usage receipt = %v at %d", firstRef, firstPosition)
	}
	secondRef, secondPosition, recordErr := recorder.Record(t.Context(), "usage-scope", validUsage)
	if recordErr != nil {
		t.Fatal(recordErr)
	}
	if secondRef != firstRef || secondPosition != firstPosition || skillUsageSourceCount(t, database) != 2 {
		t.Fatalf("idempotent usage receipt = %v at %d", secondRef, secondPosition)
	}
	changedUsage, err := source.NewSkillUsageCapture(
		"usage-observation",
		skillRef,
		digest,
		"codex-target",
		false,
		source.ObservedInvocationTrue,
		source.ObservedValidationPassed,
		source.ObservedOutcomeSuccess,
		&taskRef,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, recordErr := recorder.Record(t.Context(), "usage-scope", changedUsage); recordErr == nil {
		t.Fatal("observation identity reuse with different content was accepted")
	} else if _, ok := errors.AsType[*source.ConflictError](recordErr); !ok {
		t.Fatalf("usage identity conflict = %T %v", recordErr, recordErr)
	}
	missingTask, err := source.NewRef("content", "missing-task")
	if err != nil {
		t.Fatal(err)
	}
	missingTaskUsage, err := source.NewSkillUsageCapture(
		"missing-task-observation",
		skillRef,
		digest,
		"codex-target",
		true,
		source.ObservedInvocationTrue,
		source.ObservedValidationPassed,
		source.ObservedOutcomeSuccess,
		&missingTask,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, recordErr := recorder.Record(t.Context(), "usage-scope", missingTaskUsage); recordErr == nil {
		t.Fatal("missing task Source was accepted")
	} else if _, ok := errors.AsType[*source.SkillUsageTaskSourceNotFoundError](recordErr); !ok {
		t.Fatalf("missing task Source = %T %v", recordErr, recordErr)
	}
	if count := skillUsageSourceCount(t, database); count != 2 {
		t.Fatalf("rejected usage changed the Source journal: count=%d", count)
	}
	var stored sqlstore.StoredSource
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var getErr error
		stored, getErr = sources.Get(t.Context(), tx, "usage-scope", firstRef)
		return getErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	usage, ok := stored.Value.(source.SkillUsageSource)
	if !ok || usage.Capture().PackageDigest() != digest || usage.Capture().SkillRef().Family() != skillSnapshot.Ref().Family() {
		t.Fatalf("persisted usage = %#v", stored.Value)
	}
}

func skillUsageSourceCount(t *testing.T, database *sqlstore.Database) int {
	t.Helper()
	var count int
	if err := database.SQLDB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_sources WHERE scope_id = ?", "usage-scope").Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
