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

package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

const (
	// SkillPackageUploadType identifies immutable evidence for one caller-uploaded
	// standard Skill package. The package bytes remain in package storage only.
	SkillPackageUploadType = "skill-package-upload"

	skillPackageUploadDescription = "Exact standard Skill package captured by an explicit caller upload."
	maxSkillPackageNameLength     = 64
	maxSkillPackageDescription    = 1024
	maxSkillPackageFiles          = 256
	maxSkillPackageBytes          = 4 * 1024 * 1024
	maxSkillPackageArchiveBytes   = 5 * 1024 * 1024
)

// InvalidSkillPackageUploadError reports a malformed bounded upload value
// without including archive bytes, metadata, or package identifiers.
type InvalidSkillPackageUploadError struct{ Field string }

func (e *InvalidSkillPackageUploadError) Error() string {
	if e == nil || e.Field == "" {
		return "invalid Skill package upload"
	}
	return "invalid Skill package upload " + e.Field
}

// SkillPackageUploadReference is the public, archive-free identity of a
// canonical standard Skill package. It deliberately does not import skill.
type SkillPackageUploadReference struct {
	treeDigest       string
	archiveDigest    string
	fileCount        int
	uncompressedSize int
	archiveSize      int
}

// NewSkillPackageUploadReference restores one validated package identity.
func NewSkillPackageUploadReference(
	treeDigest, archiveDigest string,
	fileCount, uncompressedSize, archiveSize int,
) (SkillPackageUploadReference, error) {
	value := SkillPackageUploadReference{
		treeDigest: treeDigest, archiveDigest: archiveDigest, fileCount: fileCount,
		uncompressedSize: uncompressedSize, archiveSize: archiveSize,
	}
	if err := value.Validate(); err != nil {
		return SkillPackageUploadReference{}, err
	}
	return value, nil
}

func (r SkillPackageUploadReference) TreeDigest() string    { return r.treeDigest }
func (r SkillPackageUploadReference) ArchiveDigest() string { return r.archiveDigest }
func (r SkillPackageUploadReference) FileCount() int        { return r.fileCount }
func (r SkillPackageUploadReference) UncompressedSize() int { return r.uncompressedSize }
func (r SkillPackageUploadReference) ArchiveSize() int      { return r.archiveSize }

// Validate reports whether r can identify a canonical bounded package.
func (r SkillPackageUploadReference) Validate() error {
	if !skillPackageUploadDigest(r.treeDigest) || !skillPackageUploadDigest(r.archiveDigest) ||
		r.fileCount < 1 || r.fileCount > maxSkillPackageFiles ||
		r.uncompressedSize < 1 || r.uncompressedSize > maxSkillPackageBytes ||
		r.archiveSize < 1 || r.archiveSize > maxSkillPackageArchiveBytes {
		return &InvalidSkillPackageUploadError{Field: "package"}
	}
	return nil
}

// SkillPackageUploadCapture is the caller-stable evidence recorded alongside
// a pending package Candidate. It intentionally cannot contain archive bytes
// or package file content.
type SkillPackageUploadCapture struct {
	packageRef       SkillPackageUploadReference
	skillName        string
	skillDescription string
}

// NewSkillPackageUploadCapture validates and freezes upload evidence.
func NewSkillPackageUploadCapture(
	treeDigest, archiveDigest string,
	fileCount, uncompressedSize, archiveSize int,
	skillName, skillDescription string,
) (SkillPackageUploadCapture, error) {
	packageRef, err := NewSkillPackageUploadReference(treeDigest, archiveDigest, fileCount, uncompressedSize, archiveSize)
	if err != nil {
		return SkillPackageUploadCapture{}, err
	}
	value := SkillPackageUploadCapture{
		packageRef: packageRef, skillName: skillName, skillDescription: skillDescription,
	}
	if err := value.Validate(); err != nil {
		return SkillPackageUploadCapture{}, err
	}
	return value, nil
}

// Validate reports whether c is bounded immutable upload evidence.
func (c SkillPackageUploadCapture) Validate() error {
	if err := c.packageRef.Validate(); err != nil {
		return err
	}
	if err := validateReferencePart("skill_name", c.skillName, maxSkillPackageNameLength); err != nil {
		return &InvalidSkillPackageUploadError{Field: "skill_name"}
	}
	if err := validateReferencePart("skill_description", c.skillDescription, maxSkillPackageDescription); err != nil {
		return &InvalidSkillPackageUploadError{Field: "skill_description"}
	}
	_, err := NewRef(SkillPackageUploadType, skillPackageUploadSourceID(c.packageRef.TreeDigest()))
	return err
}

func (c SkillPackageUploadCapture) Package() SkillPackageUploadReference { return c.packageRef }
func (c SkillPackageUploadCapture) SkillName() string                    { return c.skillName }
func (c SkillPackageUploadCapture) SkillDescription() string             { return c.skillDescription }

// SkillPackageUploadSource is the durable representation of upload evidence.
type SkillPackageUploadSource struct{ capture SkillPackageUploadCapture }

// NewSkillPackageUploadSource resolves valid capture input to a durable value.
func NewSkillPackageUploadSource(capture SkillPackageUploadCapture) (SkillPackageUploadSource, error) {
	if err := capture.Validate(); err != nil {
		return SkillPackageUploadSource{}, err
	}
	return SkillPackageUploadSource{capture: capture}, nil
}

func (s SkillPackageUploadSource) SourceName() string {
	return skillPackageUploadSourceID(s.capture.Package().TreeDigest())
}

func (SkillPackageUploadSource) SourceMaterialization() Materialization { return Captured }

func (SkillPackageUploadSource) SourceDescription() (string, bool) {
	return skillPackageUploadDescription, true
}

func (s SkillPackageUploadSource) Capture() SkillPackageUploadCapture { return s.capture }

// SkillPackageUploadSourceAdapter maps exact bounded upload evidence to its
// immutable Source representation.
type SkillPackageUploadSourceAdapter struct{}

func (SkillPackageUploadSourceAdapter) Name() string { return SkillPackageUploadType }

func (SkillPackageUploadSourceAdapter) Resolve(
	_ context.Context,
	value SkillPackageUploadCapture,
) (SkillPackageUploadSource, error) {
	return NewSkillPackageUploadSource(value)
}

func (SkillPackageUploadSourceAdapter) Read(
	_ context.Context,
	value SkillPackageUploadSource,
) (SkillPackageUploadCapture, error) {
	if err := value.capture.Validate(); err != nil {
		return SkillPackageUploadCapture{}, err
	}
	return value.capture, nil
}

func skillPackageUploadSourceID(treeDigest string) string { return "skill_pkg_" + treeDigest }

func skillPackageUploadDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
