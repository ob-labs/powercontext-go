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
	"bytes"
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"

	"github.com/ob-labs/powercontext-go/source"
)

const (
	definitionManifestKind             = "source-definition-manifest"
	redactedDefinitionManifestIdentity = "<redacted>"
)

// DefinitionManifestRepository persists immutable Source Definition
// declarations in the global registry.
type DefinitionManifestRepository struct{}

func (repository DefinitionManifestRepository) Register(
	ctx context.Context,
	db DBTX,
	manifest source.DefinitionManifest,
) (source.DefinitionManifest, error) {
	if err := manifest.Validate(); err != nil {
		return source.DefinitionManifest{}, err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return source.DefinitionManifest{}, err
	}
	result, err := db.ExecContext(ctx, `INSERT INTO pc_source_definition_manifests
        (definition_name, definition_version, fingerprint, manifest) VALUES (?, ?, ?, ?)
        ON CONFLICT DO NOTHING`, manifest.Name(), manifest.Version(), manifest.Fingerprint(), payload)
	if err != nil {
		return source.DefinitionManifest{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return source.DefinitionManifest{}, err
	}
	if affected == 1 {
		return manifest, nil
	}

	stored, found, err := repository.Find(ctx, db, manifest.Name(), manifest.Version())
	if err != nil {
		return source.DefinitionManifest{}, err
	}
	if !found || !sameDefinitionManifest(stored, payload) {
		return source.DefinitionManifest{}, definitionManifestConflict()
	}
	return manifest, nil
}

func (DefinitionManifestRepository) Find(
	ctx context.Context,
	db DBTX,
	name, version string,
) (source.DefinitionManifest, bool, error) {
	if _, err := source.NewDefinitionIdentity(name, version); err != nil {
		return source.DefinitionManifest{}, false, err
	}
	var indexedName, indexedVersion, indexedFingerprint string
	var stored any
	err := db.QueryRowContext(ctx, `SELECT definition_name, definition_version, fingerprint, manifest
        FROM pc_source_definition_manifests WHERE definition_name = ? AND definition_version = ?`,
		name, version).Scan(&indexedName, &indexedVersion, &indexedFingerprint, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return source.DefinitionManifest{}, false, nil
	}
	if err != nil {
		return source.DefinitionManifest{}, false, err
	}
	payload, err := storedBytes(stored, "manifest")
	if err != nil {
		return source.DefinitionManifest{}, false, err
	}
	manifest, err := source.ParseDefinitionManifest(payload)
	if err != nil {
		return source.DefinitionManifest{}, false, invalidStoredDefinitionManifest()
	}
	if indexedName != manifest.Name() || indexedVersion != manifest.Version() ||
		indexedFingerprint != manifest.Fingerprint() {
		return source.DefinitionManifest{}, false, definitionManifestIdentityMismatch()
	}
	return manifest, true, nil
}

func (repository DefinitionManifestRepository) Get(
	ctx context.Context,
	db DBTX,
	name, version string,
) (source.DefinitionManifest, error) {
	manifest, found, err := repository.Find(ctx, db, name, version)
	if err != nil {
		return source.DefinitionManifest{}, err
	}
	if !found {
		return source.DefinitionManifest{}, &RepositoryNotFoundError{
			Kind:     definitionManifestKind,
			Identity: redactedDefinitionManifestIdentity,
		}
	}
	return manifest, nil
}

func sameDefinitionManifest(manifest source.DefinitionManifest, expected []byte) bool {
	payload, err := json.Marshal(manifest)
	if err != nil {
		return false
	}
	storedCanonical := jsontext.Value(bytes.Clone(payload))
	expectedCanonical := jsontext.Value(bytes.Clone(expected))
	if err := storedCanonical.Canonicalize(); err != nil {
		return false
	}
	if err := expectedCanonical.Canonicalize(); err != nil {
		return false
	}
	return bytes.Equal(storedCanonical, expectedCanonical)
}

func invalidStoredDefinitionManifest() error {
	return &InvalidStoredPayloadError{
		Kind:  definitionManifestKind,
		Name:  redactedDefinitionManifestIdentity,
		Issue: "payload does not match the model",
	}
}

func definitionManifestIdentityMismatch() error {
	return &IdentityMismatchError{
		Kind:    definitionManifestKind,
		Indexed: redactedDefinitionManifestIdentity,
		Decoded: redactedDefinitionManifestIdentity,
	}
}

func definitionManifestConflict() error {
	return &StoredPayloadConflictError{
		Kind:     definitionManifestKind,
		Identity: redactedDefinitionManifestIdentity,
	}
}
