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

	"github.com/ob-labs/powercontext-go/source"
)

// RuntimeRemoteIngestionBackend is the transaction-owning SQLite adapter for
// runtime.RemoteIngestionApplication. Runtime performs all Schema validation
// before calling Add, leaving this adapter with only durable registry/journal
// operations.
type RuntimeRemoteIngestionBackend struct {
	database    *Database
	definitions DefinitionManifestRepository
	sources     *SourceRepository
}

// NewRuntimeRemoteIngestionBackend constructs the remote-ingestion persistence
// adapter from the existing immutable Definition and Source repositories.
func NewRuntimeRemoteIngestionBackend(
	database *Database,
	definitions DefinitionManifestRepository,
	sources *SourceRepository,
) (*RuntimeRemoteIngestionBackend, error) {
	if database == nil || sources == nil {
		return nil, errors.New("sqlstore: Runtime remote ingestion dependencies must not be nil")
	}
	return &RuntimeRemoteIngestionBackend{
		database: database, definitions: definitions, sources: sources,
	}, nil
}

// Register persists a Definition in one short transaction.
func (b *RuntimeRemoteIngestionBackend) Register(
	ctx context.Context,
	manifest source.DefinitionManifest,
) (result source.DefinitionManifest, err error) {
	err = b.database.Transaction(ctx, func(tx DBTX) error {
		var registerErr error
		result, registerErr = b.definitions.Register(ctx, tx, manifest)
		return registerErr
	})
	if _, ok := errors.AsType[*StoredPayloadConflictError](err); ok {
		return source.DefinitionManifest{}, &source.DefinitionConflictError{}
	}
	return result, err
}

// Find loads one Definition in a standalone read transaction.
func (b *RuntimeRemoteIngestionBackend) Find(
	ctx context.Context,
	identity source.DefinitionIdentity,
) (result source.DefinitionManifest, found bool, err error) {
	if validationErr := identity.Validate(); validationErr != nil {
		return source.DefinitionManifest{}, false, validationErr
	}
	err = b.database.Transaction(ctx, func(tx DBTX) error {
		var findErr error
		result, found, findErr = b.definitions.Find(ctx, tx, identity.Name(), identity.Version())
		return findErr
	})
	return result, found, err
}

// Add appends a pre-validated observation in one short transaction.
func (b *RuntimeRemoteIngestionBackend) Add(
	ctx context.Context,
	scopeID string,
	observation source.SourceObservation,
) (ref source.Ref, sequence int64, err error) {
	err = b.database.Transaction(ctx, func(tx DBTX) error {
		stored, addErr := b.sources.Add(ctx, tx, scopeID, observation)
		if addErr != nil {
			return addErr
		}
		ref, sequence = stored.Ref, stored.JournalPosition
		return nil
	})
	if _, ok := errors.AsType[*StoredPayloadConflictError](err); ok {
		return source.Ref{}, 0, &source.ObservationConflictError{}
	}
	return ref, sequence, err
}

// HasNativeDefinition reports definitions that are already owned by concrete
// local Source codecs.
func (b *RuntimeRemoteIngestionBackend) HasNativeDefinition(name string) bool {
	return b.sources.HasNativeDefinition(name)
}
