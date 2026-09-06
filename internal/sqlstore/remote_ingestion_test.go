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
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"

	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestRuntimeRemoteIngestionBackendOwnsSQLiteTransactions(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	backend, err := sqlstore.NewRuntimeRemoteIngestionBackend(database, sqlstore.DefinitionManifestRepository{}, repository)
	if err != nil {
		t.Fatal(err)
	}
	if !backend.HasNativeDefinition(source.ContentType) || backend.HasNativeDefinition("remote.note") {
		t.Fatal("native definition lookup did not use the Source codec registry")
	}
	manifest := remoteStoredManifest(t, "remote.note", jsontext.Value(`{"type":"object"}`))
	registered, err := backend.Register(t.Context(), manifest)
	if err != nil || registered.Fingerprint() != manifest.Fingerprint() {
		t.Fatalf("Register() = %#v, %v", registered, err)
	}
	found, exists, err := backend.Find(t.Context(), manifest.Identity())
	if err != nil || !exists || found.Fingerprint() != manifest.Fingerprint() {
		t.Fatalf("Find() = %#v, %t, %v", found, exists, err)
	}
	observation := remoteStoredObservation(t, manifest, `{"name":"item-1","definition_version":"1","materialization":"captured","large":9007199254740993}`)
	ref, sequence, err := backend.Add(t.Context(), "scope-remote", observation)
	if err != nil || ref != observation.Ref() || sequence != 1 {
		t.Fatalf("Add() = %s, %d, %v", ref, sequence, err)
	}
}

func TestRuntimeRemoteIngestionBackendReturnsTypedRedactedConflicts(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	backend, err := sqlstore.NewRuntimeRemoteIngestionBackend(database, sqlstore.DefinitionManifestRepository{}, repository)
	if err != nil {
		t.Fatal(err)
	}
	first := remoteStoredManifest(t, "remote.secret", jsontext.Value(`{"type":"object"}`))
	second := remoteStoredManifest(t, "remote.secret", jsontext.Value(`{"type":"string"}`))
	if _, registerErr := backend.Register(t.Context(), first); registerErr != nil {
		t.Fatal(registerErr)
	}
	_, err = backend.Register(t.Context(), second)
	if _, ok := errors.AsType[*source.DefinitionConflictError](err); !ok {
		t.Fatalf("definition conflict = %T %v", err, err)
	}
	assertRemoteIngestionErrorRedacted(t, err, "remote.secret", first.Fingerprint(), second.Fingerprint())

	firstObservation := remoteStoredObservation(t, first, `{"name":"item-1","definition_version":"1","materialization":"captured","large":1}`)
	secondObservation := remoteStoredObservation(t, first, `{"name":"item-1","definition_version":"1","materialization":"captured","large":2}`)
	if _, _, addErr := backend.Add(t.Context(), "scope-remote", firstObservation); addErr != nil {
		t.Fatal(addErr)
	}
	_, _, err = backend.Add(t.Context(), "scope-remote", secondObservation)
	if _, ok := errors.AsType[*source.ObservationConflictError](err); !ok {
		t.Fatalf("observation conflict = %T %v", err, err)
	}
	assertRemoteIngestionErrorRedacted(t, err, "remote.secret", "item-1", first.Fingerprint())
}

func TestRemoteIngestionApplicationUsesSQLiteAdapterAfterValidation(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	backend, err := sqlstore.NewRuntimeRemoteIngestionBackend(database, sqlstore.DefinitionManifestRepository{}, repository)
	if err != nil {
		t.Fatal(err)
	}
	application, err := pcruntime.NewRemoteIngestionApplication(pcruntime.New(), backend)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := source.NewDefinitionIdentity("remote.note", "1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := source.NewProjectionManifest(source.TextEvidenceProjectionKey(), source.TextEvidenceSchema())
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"type":"object","properties":{"name":{"type":"string"},"definition_version":{"type":"string"},"materialization":{"const":"captured"}},"required":["name","definition_version","materialization"]}`), []source.ProjectionManifest{projection})
	if err != nil {
		t.Fatal(err)
	}
	if _, registerErr := application.Register(t.Context(), manifest); registerErr != nil {
		t.Fatal(registerErr)
	}
	ref, err := source.NewRef("remote.note", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := source.NewSourceProjectionValue(source.TextEvidenceProjectionKey(), jsontext.Value(`{"source_type":"remote.note","source_id":"item-1","content":"durable text"}`))
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(ref, "1", manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured"}`),
		[]source.SourceProjectionValue{evidence},
	)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := application.Submit(t.Context(), "scope-remote", observation)
	if err != nil || receipt.Ref != ref || receipt.Sequence != 1 {
		t.Fatalf("Submit() = %#v, %v", receipt, err)
	}
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		stored, getErr := repository.Get(t.Context(), tx, "scope-remote", ref)
		if getErr != nil {
			return getErr
		}
		if stored.JournalPosition != receipt.Sequence {
			t.Fatalf("stored sequence = %d, want %d", stored.JournalPosition, receipt.Sequence)
		}
		_, ok := stored.Value.(source.SourceObservation)
		if !ok {
			t.Fatalf("stored value = %T, want SourceObservation", stored.Value)
		}
		return nil
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}

	wrongFingerprint, err := source.NewSourceObservation(ref, "1", "wrong", nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured"}`),
		[]source.SourceProjectionValue{evidence},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.Submit(t.Context(), "scope-remote", wrongFingerprint)
	if _, ok := errors.AsType[*source.InvalidSourceObservationError](err); !ok {
		t.Fatalf("invalid observation error = %T %v", err, err)
	}
	var count int
	if err := database.SQLDB().QueryRowContext(t.Context(), "SELECT count(*) FROM pc_sources WHERE scope_id = ?", "scope-remote").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("invalid observation changed stored Source count to %d", count)
	}
}

func TestRuntimeRemoteIngestionBackendRejectsNilDependencies(t *testing.T) {
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlstore.NewRuntimeRemoteIngestionBackend(nil, sqlstore.DefinitionManifestRepository{}, repository); err == nil {
		t.Fatal("nil database was accepted")
	}
	if _, err := sqlstore.NewRuntimeRemoteIngestionBackend(openTestDatabase(t), sqlstore.DefinitionManifestRepository{}, nil); err == nil {
		t.Fatal("nil Source repository was accepted")
	}
}

func remoteStoredManifest(t *testing.T, name string, schema jsontext.Value) source.DefinitionManifest {
	t.Helper()
	identity, err := source.NewDefinitionIdentity(name, "1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func remoteStoredObservation(t *testing.T, manifest source.DefinitionManifest, payload string) source.SourceObservation {
	t.Helper()
	ref, err := source.NewRef(manifest.Name(), "item-1")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(ref, manifest.Version(), manifest.Fingerprint(), nil, jsontext.Value(payload), nil)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func assertRemoteIngestionErrorRedacted(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a remote-ingestion error")
	}
	for _, secret := range secrets {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("remote-ingestion error exposed %q: %v", secret, err)
		}
	}
}
