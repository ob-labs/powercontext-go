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
	"encoding/hex"
	"errors"

	"github.com/ob-labs/powercontext-go/artifact/skill"
)

// SkillPackageConflictError reports reuse of a scope-local package tree for a
// different immutable package snapshot. It intentionally exposes no package
// identity, path, or content.
type SkillPackageConflictError struct{}

func (*SkillPackageConflictError) Error() string {
	return "managed Skill package conflicts with the stored snapshot"
}

// SkillPackageNotFoundError reports a missing scope-local immutable package.
// It intentionally exposes no package identity or scope.
type SkillPackageNotFoundError struct{}

func (*SkillPackageNotFoundError) Error() string { return "managed Skill package was not found" }

// SkillPackageRepository persists immutable managed Skill package snapshots.
// Its methods run entirely within the transaction supplied by the caller.
type SkillPackageRepository struct{}

// Add records one verified canonical package under its scope-local tree
// identity. Repeating the exact same snapshot is idempotent.
func (repository SkillPackageRepository) Add(
	ctx context.Context,
	db DBTX,
	scopeID string,
	snapshot skill.PackageSnapshot,
) (skill.PackageSnapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return skill.PackageSnapshot{}, err
	}
	content, err := skill.NewPackageContent(snapshot)
	if err != nil {
		return skill.PackageSnapshot{}, err
	}
	verified := content.Snapshot()
	ref := verified.Reference()
	_, err = db.ExecContext(ctx, `INSERT INTO pc_skill_packages
        (scope_id, tree_digest, archive_digest, file_count, uncompressed_size, archive_size, archive, manifest)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		scopeID, ref.TreeDigest(), ref.ArchiveDigest(), ref.FileCount(), ref.UncompressedSize(), ref.ArchiveSize(),
		verified.Archive(), verified.Manifest(),
	)
	if err == nil {
		return verified, nil
	}
	if !isIntegrityConstraint(err) {
		return skill.PackageSnapshot{}, err
	}

	stored, found, findErr := repository.find(ctx, db, scopeID, ref.TreeDigest())
	if findErr != nil {
		return skill.PackageSnapshot{}, findErr
	}
	if !found {
		return skill.PackageSnapshot{}, err
	}
	if !sameSkillPackageRecord(stored, verified) {
		return skill.PackageSnapshot{}, &SkillPackageConflictError{}
	}
	decoded, decodeErr := decodeSkillPackageRecord(stored, scopeID, ref)
	if decodeErr != nil {
		return skill.PackageSnapshot{}, decodeErr
	}
	return decoded, nil
}

// Get returns a verified independently owned package snapshot for one
// scope-local reference.
func (repository SkillPackageRepository) Get(
	ctx context.Context,
	db DBTX,
	scopeID string,
	ref skill.PackageRef,
) (skill.PackageSnapshot, error) {
	if err := requireScope(scopeID); err != nil {
		return skill.PackageSnapshot{}, err
	}
	if err := validateSkillPackageReference(ref); err != nil {
		return skill.PackageSnapshot{}, err
	}
	stored, found, err := repository.find(ctx, db, scopeID, ref.TreeDigest())
	if err != nil {
		return skill.PackageSnapshot{}, err
	}
	if !found {
		return skill.PackageSnapshot{}, &SkillPackageNotFoundError{}
	}
	return decodeSkillPackageRecord(stored, scopeID, ref)
}

type skillPackageRecord struct {
	scopeID          string
	treeDigest       string
	archiveDigest    string
	fileCount        int64
	uncompressedSize int64
	archiveSize      int64
	archive          []byte
	manifest         []byte
}

func (repository SkillPackageRepository) find(
	ctx context.Context,
	db DBTX,
	scopeID, treeDigest string,
) (skillPackageRecord, bool, error) {
	record, err := scanSkillPackageRecord(db.QueryRowContext(ctx, `SELECT scope_id, tree_digest, archive_digest,
        file_count, uncompressed_size, archive_size, archive, manifest
        FROM pc_skill_packages WHERE scope_id = ? AND tree_digest = ?`, scopeID, treeDigest))
	if errors.Is(err, sql.ErrNoRows) {
		return skillPackageRecord{}, false, nil
	}
	if err != nil {
		return skillPackageRecord{}, false, err
	}
	return record, true, nil
}

func scanSkillPackageRecord(value scanner) (skillPackageRecord, error) {
	var record skillPackageRecord
	var scopeID, treeDigest, archiveDigest, fileCount, uncompressedSize, archiveSize, archive, manifest any
	if err := value.Scan(&scopeID, &treeDigest, &archiveDigest, &fileCount, &uncompressedSize, &archiveSize, &archive, &manifest); err != nil {
		return skillPackageRecord{}, err
	}
	var err error
	if record.scopeID, err = storedSkillPackageString(scopeID, "scope_id"); err != nil {
		return skillPackageRecord{}, err
	}
	if record.treeDigest, err = storedSkillPackageString(treeDigest, "tree_digest"); err != nil {
		return skillPackageRecord{}, err
	}
	if record.archiveDigest, err = storedSkillPackageString(archiveDigest, "archive_digest"); err != nil {
		return skillPackageRecord{}, err
	}
	if record.fileCount, err = storedSkillPackageInteger(fileCount, "file_count"); err != nil {
		return skillPackageRecord{}, err
	}
	if record.uncompressedSize, err = storedSkillPackageInteger(uncompressedSize, "uncompressed_size"); err != nil {
		return skillPackageRecord{}, err
	}
	if record.archiveSize, err = storedSkillPackageInteger(archiveSize, "archive_size"); err != nil {
		return skillPackageRecord{}, err
	}
	if record.archive, err = storedBytes(archive, "archive"); err != nil {
		return skillPackageRecord{}, err
	}
	if record.manifest, err = storedBytes(manifest, "manifest"); err != nil {
		return skillPackageRecord{}, err
	}
	return record, nil
}

func decodeSkillPackageRecord(record skillPackageRecord, scopeID string, expected skill.PackageRef) (skill.PackageSnapshot, error) {
	decoded, err := skill.CapturePackageArchive(record.archive)
	if err != nil {
		return skill.PackageSnapshot{}, invalidStoredSkillPackage("archive does not match the package format")
	}
	ref := decoded.Reference()
	if record.scopeID != scopeID || record.treeDigest != ref.TreeDigest() || record.archiveDigest != ref.ArchiveDigest() ||
		record.fileCount != int64(ref.FileCount()) || record.uncompressedSize != int64(ref.UncompressedSize()) ||
		record.archiveSize != int64(ref.ArchiveSize()) || !bytes.Equal(record.archive, decoded.Archive()) ||
		!bytes.Equal(record.manifest, decoded.Manifest()) || ref != expected {
		return skill.PackageSnapshot{}, invalidStoredSkillPackage("indexed values do not match the package archive")
	}
	content, err := skill.NewPackageContent(decoded)
	if err != nil {
		return skill.PackageSnapshot{}, invalidStoredSkillPackage("archive does not match the package format")
	}
	return content.Snapshot(), nil
}

func sameSkillPackageRecord(record skillPackageRecord, snapshot skill.PackageSnapshot) bool {
	ref := snapshot.Reference()
	return record.treeDigest == ref.TreeDigest() && record.archiveDigest == ref.ArchiveDigest() &&
		record.fileCount == int64(ref.FileCount()) && record.uncompressedSize == int64(ref.UncompressedSize()) &&
		record.archiveSize == int64(ref.ArchiveSize()) && bytes.Equal(record.archive, snapshot.Archive()) &&
		bytes.Equal(record.manifest, snapshot.Manifest())
}

func validateSkillPackageReference(ref skill.PackageRef) error {
	if !validSkillPackageDigest(ref.TreeDigest()) || !validSkillPackageDigest(ref.ArchiveDigest()) ||
		ref.FileCount() < 1 || ref.FileCount() > skill.MaxPackageFiles || ref.UncompressedSize() < 0 ||
		ref.UncompressedSize() > skill.MaxPackageBytes || ref.ArchiveSize() < 1 || ref.ArchiveSize() > skill.MaxPackageArchiveBytes {
		return &InvalidRepositoryArgumentError{Field: "package_ref", Detail: "must be a canonical managed Skill package reference"}
	}
	return nil
}

func validSkillPackageDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func storedSkillPackageString(value any, column string) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", &InvalidStoredColumnError{Column: column, Expected: "a string"}
	}
	return text, nil
}

func storedSkillPackageInteger(value any, column string) (int64, error) {
	integerValue, ok := integer(value)
	if !ok {
		return 0, &InvalidStoredColumnError{Column: column, Expected: "an integer"}
	}
	return integerValue, nil
}

func invalidStoredSkillPackage(issue string) error {
	return &InvalidStoredPayloadError{Kind: "managed-skill-package", Name: "<redacted>", Issue: issue}
}
