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

package skill

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v2"
)

const (
	MaxPackageFiles           = 256
	MaxPackageBytes           = 4 * 1024 * 1024
	MaxPackageArchiveBytes    = 5 * 1024 * 1024
	MaxPackageManifestBytes   = 128 * 1024
	MaxPackagePathBytes       = 512
	MaxPackageEntrypointBytes = 128 * 1024
	PackageEntrypoint         = "SKILL.md"
)

var (
	packageNamePattern      = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	packageCaseFold         = cases.Fold()
	forbiddenPackagePathSet = map[string]struct{}{".env": {}, ".git": {}, "node_modules": {}}
)

// PackageError reports an invalid or unsafe managed Skill package. Its message
// is intentionally bounded and never contains package file contents.
type PackageError struct{ message string }

func (e *PackageError) Error() string { return e.message }

func packageErrorf(format string, values ...any) error {
	return &PackageError{message: fmt.Sprintf(format, values...)}
}

// PackageRef identifies one immutable canonical package. TreeDigest identifies
// its file tree; ArchiveDigest identifies its deterministic ZIP representation.
type PackageRef struct {
	treeDigest       string
	archiveDigest    string
	fileCount        int
	uncompressedSize int
	archiveSize      int
}

func (r PackageRef) TreeDigest() string    { return r.treeDigest }
func (r PackageRef) ArchiveDigest() string { return r.archiveDigest }
func (r PackageRef) FileCount() int        { return r.fileCount }
func (r PackageRef) UncompressedSize() int { return r.uncompressedSize }
func (r PackageRef) ArchiveSize() int      { return r.archiveSize }

// PackageEntry is one regular file retained by an immutable package.
type PackageEntry struct {
	path      string
	digest    string
	size      int
	mediaType string
	mode      os.FileMode
}

func (e PackageEntry) Path() string      { return e.path }
func (e PackageEntry) Digest() string    { return e.digest }
func (e PackageEntry) Size() int         { return e.size }
func (e PackageEntry) MediaType() string { return e.mediaType }
func (e PackageEntry) Mode() os.FileMode { return e.mode }

// PackageMetadata contains informational frontmatter. AllowedTools is never
// evaluated as an execution, permission, or tool-grant policy by this package.
type PackageMetadata struct {
	name          string
	description   string
	license       string
	compatibility string
	metadata      map[string]string
	allowedTools  string
}

func (m PackageMetadata) Name() string          { return m.name }
func (m PackageMetadata) Description() string   { return m.description }
func (m PackageMetadata) License() string       { return m.license }
func (m PackageMetadata) Compatibility() string { return m.compatibility }
func (m PackageMetadata) Metadata() map[string]string {
	return maps.Clone(m.metadata)
}
func (m PackageMetadata) AllowedTools() string { return m.allowedTools }

// PackageSnapshot contains only verified regular-file content and its
// canonical ZIP. Callers receive copies of every mutable byte sequence.
type PackageSnapshot struct {
	reference    PackageRef
	entries      []PackageEntry
	metadata     PackageMetadata
	instructions string
	archive      []byte
	manifest     []byte
}

func (s PackageSnapshot) Reference() PackageRef     { return s.reference }
func (s PackageSnapshot) Entries() []PackageEntry   { return slices.Clone(s.entries) }
func (s PackageSnapshot) Metadata() PackageMetadata { return clonePackageMetadata(s.metadata) }
func (s PackageSnapshot) Instructions() string      { return s.instructions }
func (s PackageSnapshot) Archive() []byte           { return bytes.Clone(s.archive) }
func (s PackageSnapshot) Manifest() []byte          { return bytes.Clone(s.manifest) }

// NewPackageContent attaches an already-validated package to a v2 managed
// Skill. It does not create a package for legacy v1 Content.
func NewPackageContent(snapshot PackageSnapshot) (PackageContent, error) {
	verified, err := verifyPackageSnapshot(snapshot)
	if err != nil {
		return PackageContent{}, err
	}
	return PackageContent{snapshot: clonePackageSnapshot(verified)}, nil
}

// CapturePackageDirectory captures every regular file in a standard Skill
// directory. It rejects links and special files instead of following them.
func CapturePackageDirectory(packagePath string) (PackageSnapshot, error) {
	root, err := filepath.Abs(packagePath)
	if err != nil {
		return PackageSnapshot{}, packageErrorf("managed Skill package path is invalid")
	}
	root = filepath.Clean(root)
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return PackageSnapshot{}, packageErrorf("managed Skill package must be a regular directory")
	}

	files := make([]canonicalPackageFile, 0)
	total := 0
	err = filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return packageErrorf("managed Skill package cannot be read")
		}
		if current == root {
			return nil
		}
		relative, relativeErr := filepath.Rel(root, current)
		if relativeErr != nil {
			return packageErrorf("managed Skill package path is invalid")
		}
		path, pathErr := validatePackagePath(filepath.ToSlash(relative))
		if pathErr != nil {
			return pathErr
		}
		info, infoErr := os.Lstat(current)
		if infoErr != nil {
			return packageErrorf("managed Skill package cannot be read")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return packageErrorf("managed Skill package contains a symbolic link")
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return packageErrorf("managed Skill package contains a special file")
		}
		if len(files) >= MaxPackageFiles {
			return packageErrorf("managed Skill package has an unsupported file count")
		}
		content, readErr := readStablePackageFile(current, info, MaxPackageBytes-total)
		if readErr != nil {
			return readErr
		}
		total += len(content)
		files = append(files, canonicalPackageFile{path: path, content: content, mode: normalizePackageMode(info.Mode())})
		return nil
	})
	if err != nil {
		return PackageSnapshot{}, err
	}
	return canonicalPackageSnapshot(files, filepath.Base(root))
}

// CapturePackageArchive validates an untrusted ZIP and rewrites it to the one
// deterministic representation used by package persistence and distribution.
func CapturePackageArchive(archive []byte) (PackageSnapshot, error) {
	if len(archive) == 0 || len(archive) > MaxPackageArchiveBytes {
		return PackageSnapshot{}, packageErrorf("managed Skill package archive exceeds the supported size")
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return PackageSnapshot{}, packageErrorf("managed Skill package archive is invalid")
	}
	files := make([]canonicalPackageFile, 0)
	seen := make(map[string]struct{})
	total := 0
	for _, entry := range reader.File {
		if strings.Contains(entry.Name, "..") {
			return PackageSnapshot{}, packageErrorf("managed Skill package archive contains a path traversal sequence")
		}
		isDirectory := strings.HasSuffix(entry.Name, "/")
		name := entry.Name
		if isDirectory {
			name = strings.TrimSuffix(name, "/")
		}
		path, pathErr := validatePackagePath(name)
		if pathErr != nil {
			return PackageSnapshot{}, pathErr
		}
		collision := packagePathCollisionKey(path)
		if _, exists := seen[collision]; exists {
			return PackageSnapshot{}, packageErrorf("managed Skill package archive contains duplicate or colliding paths")
		}
		seen[collision] = struct{}{}
		if entry.Flags&0x1 != 0 {
			return PackageSnapshot{}, packageErrorf("managed Skill package archive contains an encrypted entry")
		}
		mode := entry.Mode()
		if isDirectory {
			if mode&os.ModeSymlink != 0 || !mode.IsDir() && mode&os.ModeType != 0 {
				return PackageSnapshot{}, packageErrorf("managed Skill package archive contains a non-regular entry")
			}
			continue
		}
		if !mode.IsRegular() {
			return PackageSnapshot{}, packageErrorf("managed Skill package archive contains a non-regular entry")
		}
		if entry.UncompressedSize64 > uint64(MaxPackageBytes) ||
			entry.UncompressedSize64 > uint64(MaxPackageBytes-total) {
			return PackageSnapshot{}, packageErrorf("managed Skill package exceeds the supported uncompressed size")
		}
		if len(files) >= MaxPackageFiles {
			return PackageSnapshot{}, packageErrorf("managed Skill package has an unsupported file count")
		}
		content, readErr := readZIPEntry(entry)
		if readErr != nil {
			return PackageSnapshot{}, readErr
		}
		total += len(content)
		files = append(files, canonicalPackageFile{path: path, content: content, mode: normalizePackageMode(mode)})
	}
	return canonicalPackageSnapshot(files, "")
}

// ReadPackageFile returns a copy of one verified regular package file. It does
// not expose a ZIP reader and never executes the selected file.
func ReadPackageFile(snapshot PackageSnapshot, path string) ([]byte, error) {
	verified, err := verifyPackageSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	canonical, err := validatePackagePath(path)
	if err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(verified.entries, func(entry PackageEntry) bool { return entry.path == canonical }) {
		return nil, packageErrorf("managed Skill package file was not found")
	}
	reader, err := zip.NewReader(bytes.NewReader(verified.archive), int64(len(verified.archive)))
	if err != nil {
		return nil, packageErrorf("stored managed Skill package archive is invalid")
	}
	for _, entry := range reader.File {
		if entry.Name == canonical {
			return readZIPEntry(entry)
		}
	}
	return nil, packageErrorf("stored managed Skill package archive is invalid")
}

// MaterializePackage writes verified package bytes into a new directory. It
// creates files with their stored regular-file modes but never executes them.
func MaterializePackage(snapshot PackageSnapshot, destination string) error {
	verified, err := verifyPackageSnapshot(snapshot)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return packageErrorf("managed Skill package destination is invalid")
	}
	absolute = filepath.Clean(absolute)
	if info, statErr := os.Lstat(absolute); statErr == nil || !os.IsNotExist(statErr) {
		if statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return packageErrorf("managed Skill package destination is not a new directory")
		}
		return packageErrorf("managed Skill package destination already exists")
	}
	if err := os.MkdirAll(absolute, 0o755); err != nil {
		return packageErrorf("managed Skill package destination cannot be created")
	}
	removePartial := true
	defer func() {
		if removePartial {
			_ = os.RemoveAll(absolute)
		}
	}()
	for _, entry := range verified.entries {
		target, targetErr := packageDestination(absolute, entry.path)
		if targetErr != nil {
			return targetErr
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return packageErrorf("managed Skill package destination cannot be created")
		}
		content, readErr := ReadPackageFile(verified, entry.path)
		if readErr != nil {
			return readErr
		}
		file, openErr := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, entry.mode)
		if openErr != nil {
			return packageErrorf("managed Skill package file cannot be materialized")
		}
		_, writeErr := file.Write(content)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return packageErrorf("managed Skill package file cannot be materialized")
		}
		if err := os.Chmod(target, entry.mode); err != nil {
			return packageErrorf("managed Skill package file mode cannot be materialized")
		}
	}
	removePartial = false
	return nil
}

type canonicalPackageFile struct {
	path    string
	content []byte
	mode    os.FileMode
}

func canonicalPackageSnapshot(files []canonicalPackageFile, expectedName string) (PackageSnapshot, error) {
	if len(files) == 0 || len(files) > MaxPackageFiles {
		return PackageSnapshot{}, packageErrorf("managed Skill package has an unsupported file count")
	}
	slices.SortFunc(files, func(left, right canonicalPackageFile) int { return strings.Compare(left.path, right.path) })
	entries := make([]PackageEntry, 0, len(files))
	seen := make(map[string]string)
	total := 0
	var entrypoint []byte
	for _, file := range files {
		path, err := validatePackagePath(file.path)
		if err != nil {
			return PackageSnapshot{}, err
		}
		collision := packagePathCollisionKey(path)
		if _, exists := seen[collision]; exists {
			return PackageSnapshot{}, packageErrorf("managed Skill package contains colliding paths")
		}
		seen[collision] = path
		if len(file.content) > MaxPackageBytes-total {
			return PackageSnapshot{}, packageErrorf("managed Skill package exceeds the supported uncompressed size")
		}
		total += len(file.content)
		digest := sha256.Sum256(file.content)
		entries = append(entries, PackageEntry{
			path: path, digest: hex.EncodeToString(digest[:]), size: len(file.content),
			mediaType: packageMediaType(path), mode: normalizePackageMode(file.mode),
		})
		if path == PackageEntrypoint {
			entrypoint = file.content
		}
	}
	if entrypoint == nil {
		return PackageSnapshot{}, packageErrorf("managed Skill package must contain SKILL.md at its root")
	}
	metadata, instructions, err := parsePackageEntrypoint(entrypoint, expectedName)
	if err != nil {
		return PackageSnapshot{}, err
	}
	manifest, err := packageManifest(entries)
	if err != nil {
		return PackageSnapshot{}, packageErrorf("managed Skill package manifest cannot be encoded")
	}
	if len(manifest) > MaxPackageManifestBytes {
		return PackageSnapshot{}, packageErrorf("managed Skill package manifest exceeds the supported size")
	}
	archive, err := canonicalPackageArchive(files)
	if err != nil {
		return PackageSnapshot{}, packageErrorf("managed Skill package archive cannot be encoded")
	}
	if len(archive) > MaxPackageArchiveBytes {
		return PackageSnapshot{}, packageErrorf("canonical managed Skill package archive exceeds the supported size")
	}
	archiveDigest := sha256.Sum256(archive)
	return PackageSnapshot{
		reference: PackageRef{
			treeDigest: packageTreeDigest(entries), archiveDigest: hex.EncodeToString(archiveDigest[:]),
			fileCount: len(entries), uncompressedSize: total, archiveSize: len(archive),
		},
		entries: entries, metadata: metadata, instructions: instructions, archive: archive, manifest: manifest,
	}, nil
}

func verifyPackageSnapshot(snapshot PackageSnapshot) (PackageSnapshot, error) {
	if len(snapshot.archive) == 0 {
		return PackageSnapshot{}, packageErrorf("managed Skill package snapshot is invalid")
	}
	verified, err := CapturePackageArchive(snapshot.archive)
	if err != nil {
		return PackageSnapshot{}, packageErrorf("stored managed Skill package archive is invalid")
	}
	if verified.reference != snapshot.reference || !bytes.Equal(verified.archive, snapshot.archive) ||
		!bytes.Equal(verified.manifest, snapshot.manifest) || !samePackageMetadata(verified.metadata, snapshot.metadata) ||
		verified.instructions != snapshot.instructions || !slices.EqualFunc(verified.entries, snapshot.entries, equalPackageEntry) {
		return PackageSnapshot{}, packageErrorf("managed Skill package snapshot does not match its archive")
	}
	return verified, nil
}

func readStablePackageFile(path string, checked os.FileInfo, maximum int) ([]byte, error) {
	if maximum < 0 {
		return nil, packageErrorf("managed Skill package exceeds the supported uncompressed size")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, packageErrorf("managed Skill package file cannot be read")
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(checked, before) {
		return nil, packageErrorf("managed Skill package changed during capture")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, packageErrorf("managed Skill package file cannot be read")
	}
	after, err := file.Stat()
	current, currentErr := os.Lstat(path)
	if err != nil || currentErr != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, after) ||
		!os.SameFile(before, current) || before.Size() != after.Size() || before.ModTime() != after.ModTime() {
		return nil, packageErrorf("managed Skill package changed during capture")
	}
	if len(content) > maximum {
		return nil, packageErrorf("managed Skill package exceeds the supported uncompressed size")
	}
	return content, nil
}

func readZIPEntry(entry *zip.File) ([]byte, error) {
	reader, err := entry.Open()
	if err != nil {
		return nil, packageErrorf("managed Skill package archive is invalid")
	}
	defer func() { _ = reader.Close() }()
	content, err := io.ReadAll(io.LimitReader(reader, int64(entry.UncompressedSize64)+1))
	if err != nil || uint64(len(content)) != entry.UncompressedSize64 {
		return nil, packageErrorf("managed Skill package archive is invalid")
	}
	return content, nil
}

func packageManifest(entries []PackageEntry) ([]byte, error) {
	values := make([]packageManifestEntry, len(entries))
	for index, entry := range entries {
		values[index] = packageManifestEntry{
			Path: entry.path, Digest: entry.digest, Size: entry.size, MediaType: entry.mediaType, Mode: uint32(entry.mode),
		}
	}
	return json.Marshal(values, json.Deterministic(true))
}

type packageManifestEntry struct {
	Path      string `json:"path"`
	Digest    string `json:"digest"`
	Size      int    `json:"size"`
	MediaType string `json:"media_type"`
	Mode      uint32 `json:"mode"`
}

func canonicalPackageArchive(files []canonicalPackageFile) ([]byte, error) {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, file := range files {
		header := &zip.FileHeader{
			Name: file.path, Method: zip.Store,
			Modified: time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC),
		}
		header.SetMode(normalizePackageMode(file.mode))
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(file.content); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func packageTreeDigest(entries []PackageEntry) string {
	digest := sha256.New()
	_, _ = digest.Write([]byte("powercontext.skill-package-tree.v1\x00"))
	var length [8]byte
	for _, entry := range entries {
		path := []byte(entry.path)
		binary.BigEndian.PutUint32(length[:4], uint32(len(path)))
		_, _ = digest.Write(length[:4])
		_, _ = digest.Write(path)
		binary.BigEndian.PutUint32(length[:4], uint32(entry.mode))
		_, _ = digest.Write(length[:4])
		binary.BigEndian.PutUint64(length[:], uint64(entry.size))
		_, _ = digest.Write(length[:])
		fileDigest, _ := hex.DecodeString(entry.digest)
		_, _ = digest.Write(fileDigest)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func parsePackageEntrypoint(content []byte, expectedName string) (PackageMetadata, string, error) {
	if len(content) == 0 || len(content) > MaxPackageEntrypointBytes {
		return PackageMetadata{}, "", packageErrorf("managed Skill SKILL.md exceeds the supported size")
	}
	if !utf8.Valid(content) {
		return PackageMetadata{}, "", packageErrorf("managed Skill SKILL.md must be UTF-8")
	}
	text := string(content)
	lines := strings.SplitAfter(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return PackageMetadata{}, "", packageErrorf("managed Skill SKILL.md is missing YAML frontmatter")
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			closing = index
			break
		}
	}
	if closing < 0 {
		return PackageMetadata{}, "", packageErrorf("managed Skill SKILL.md frontmatter is not terminated")
	}
	var values map[any]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:closing], "")), &values); err != nil || values == nil {
		return PackageMetadata{}, "", packageErrorf("managed Skill SKILL.md frontmatter is invalid YAML")
	}
	metadata, err := packageMetadata(values, expectedName)
	if err != nil {
		return PackageMetadata{}, "", err
	}
	instructions := strings.TrimLeft(strings.Join(lines[closing+1:], ""), "\r\n")
	instructions = strings.TrimRightFunc(instructions, isPythonWhitespace)
	return metadata, instructions, nil
}

func packageMetadata(values map[any]any, expectedName string) (PackageMetadata, error) {
	stringsOnly := make(map[string]any, len(values))
	for key, value := range values {
		name, ok := key.(string)
		if !ok {
			return PackageMetadata{}, packageErrorf("managed Skill SKILL.md frontmatter must use string keys")
		}
		stringsOnly[name] = value
	}
	name, err := requiredPackageText(stringsOnly, "name", 64)
	if err != nil {
		return PackageMetadata{}, err
	}
	if !packageNamePattern.MatchString(name) {
		return PackageMetadata{}, packageErrorf("managed Skill name must use lowercase letters, digits, and single hyphens")
	}
	if expectedName != "" && name != expectedName {
		return PackageMetadata{}, packageErrorf("managed Skill name must match its package directory")
	}
	description, err := requiredPackageText(stringsOnly, "description", 1_024)
	if err != nil {
		return PackageMetadata{}, err
	}
	license, err := optionalPackageText(stringsOnly, "license", 512)
	if err != nil {
		return PackageMetadata{}, err
	}
	compatibility, err := optionalPackageText(stringsOnly, "compatibility", 500)
	if err != nil {
		return PackageMetadata{}, err
	}
	allowedTools, err := optionalPackageText(stringsOnly, "allowed-tools", 2_000)
	if err != nil {
		return PackageMetadata{}, err
	}
	metadata, err := packageMetadataValues(stringsOnly["metadata"])
	if err != nil {
		return PackageMetadata{}, err
	}
	return PackageMetadata{
		name: name, description: description, license: license, compatibility: compatibility,
		metadata: metadata, allowedTools: allowedTools,
	}, nil
}

func requiredPackageText(values map[string]any, field string, maximum int) (string, error) {
	value, exists := values[field]
	text, ok := value.(string)
	if !exists || !ok || !validPackageText(text, maximum) {
		return "", packageErrorf("managed Skill %s must be a non-empty trimmed string", field)
	}
	return text, nil
}

func optionalPackageText(values map[string]any, field string, maximum int) (string, error) {
	value, exists := values[field]
	if !exists || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok || !validPackageText(text, maximum) {
		return "", packageErrorf("managed Skill %s must be a non-empty trimmed string", field)
	}
	return text, nil
}

func packageMetadataValues(value any) (map[string]string, error) {
	if value == nil {
		return nil, nil
	}
	values, ok := value.(map[any]any)
	if !ok || len(values) > 64 {
		return nil, packageErrorf("managed Skill metadata must map strings to strings")
	}
	result := make(map[string]string, len(values))
	for key, raw := range values {
		name, nameOK := key.(string)
		text, textOK := raw.(string)
		if !nameOK || !textOK || !validPackageText(name, 128) || !validPackageText(text, 2_000) {
			return nil, packageErrorf("managed Skill metadata must map trimmed strings to strings")
		}
		result[name] = text
	}
	return result, nil
}

func validPackageText(value string, maximum int) bool {
	return utf8.ValidString(value) && value != "" && value == strings.TrimFunc(value, isPythonWhitespace) && utf8.RuneCountInString(value) <= maximum
}

func isPythonWhitespace(value rune) bool {
	return value == '\u001c' || value == '\u001d' || value == '\u001e' || value == '\u001f' || value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\v' || value == '\f' || value == '\u0085' || value == '\u00a0' || value == '\u1680' || value == '\u2000' || value == '\u2001' || value == '\u2002' || value == '\u2003' || value == '\u2004' || value == '\u2005' || value == '\u2006' || value == '\u2007' || value == '\u2008' || value == '\u2009' || value == '\u200a' || value == '\u2028' || value == '\u2029' || value == '\u202f' || value == '\u205f' || value == '\u3000'
}

func validatePackagePath(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.Contains(value, "\x00") || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return "", packageErrorf("managed Skill package contains an invalid path")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return "", packageErrorf("managed Skill package path is not relative")
		}
		if _, forbidden := forbiddenPackagePathSet[part]; forbidden {
			return "", packageErrorf("managed Skill package contains a forbidden path")
		}
	}
	if norm.NFC.String(value) != value {
		return "", packageErrorf("managed Skill package path must use NFC Unicode normalization")
	}
	if len(value) > MaxPackagePathBytes {
		return "", packageErrorf("managed Skill package path exceeds the supported size")
	}
	return value, nil
}

func packagePathCollisionKey(value string) string {
	return packageCaseFold.String(norm.NFC.String(value))
}

func normalizePackageMode(value os.FileMode) os.FileMode {
	if value&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func packageMediaType(path string) string {
	_, suffix, found := strings.CutLast(path, ".")
	if !found {
		return "application/octet-stream"
	}
	switch "." + strings.ToLower(suffix) {
	case ".css":
		return "text/css"
	case ".csv":
		return "text/csv"
	case ".gif":
		return "image/gif"
	case ".htm", ".html":
		return "text/html"
	case ".jpeg", ".jpg":
		return "image/jpeg"
	case ".js", ".mjs":
		return "text/javascript"
	case ".json":
		return "application/json"
	case ".md":
		return "text/markdown"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".py":
		return "text/x-python"
	case ".sh":
		return "application/x-sh"
	case ".svg":
		return "image/svg+xml"
	case ".txt":
		return "text/plain"
	case ".wasm":
		return "application/wasm"
	case ".xml":
		return "text/xml"
	case ".yaml", ".yml":
		return "application/yaml"
	default:
		return "application/octet-stream"
	}
}

func packageDestination(root, path string) (string, error) {
	target := filepath.Join(root, filepath.FromSlash(path))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", packageErrorf("managed Skill package destination is invalid")
	}
	return target, nil
}

func clonePackageMetadata(value PackageMetadata) PackageMetadata {
	value.metadata = maps.Clone(value.metadata)
	return value
}

func clonePackageSnapshot(value PackageSnapshot) PackageSnapshot {
	return PackageSnapshot{
		reference: value.reference, entries: slices.Clone(value.entries), metadata: clonePackageMetadata(value.metadata),
		instructions: value.instructions, archive: bytes.Clone(value.archive), manifest: bytes.Clone(value.manifest),
	}
}

func equalPackageEntry(left, right PackageEntry) bool {
	return left == right
}

func samePackageMetadata(left, right PackageMetadata) bool {
	return left.name == right.name && left.description == right.description && left.license == right.license &&
		left.compatibility == right.compatibility && left.allowedTools == right.allowedTools && maps.Equal(left.metadata, right.metadata)
}
