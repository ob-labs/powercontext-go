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
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestPackageCaptureCanonicalizesWholeTreeAndPreservesExecutableMode(t *testing.T) {
	packagePath := writeManagedPackage(t)

	directorySnapshot, err := CapturePackageDirectory(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	if directorySnapshot.Reference().FileCount() != 5 {
		t.Fatalf("file count = %d, want 5", directorySnapshot.Reference().FileCount())
	}
	if directorySnapshot.Metadata().Name() != "release-check" || directorySnapshot.Metadata().AllowedTools() != "Bash(git:*) Read" {
		t.Fatalf("metadata = %#v", directorySnapshot.Metadata())
	}
	if directorySnapshot.Metadata().AllowedTools() == "" {
		t.Fatal("allowed-tools was not retained as inert package metadata")
	}
	paths := []string{"SKILL.md", "scripts/verify.sh", "references/policy.md", "assets/report.json", ".hidden-note"}
	snapshot, err := CapturePackageArchive(zipManagedPackage(t, packagePath, paths))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && snapshot.Reference() != directorySnapshot.Reference() {
		t.Fatalf("directory and archive references diverged: %v != %v", directorySnapshot.Reference(), snapshot.Reference())
	}
	packageContent, contentErr := NewPackageContent(snapshot)
	if contentErr != nil {
		t.Fatal(contentErr)
	}
	if packageContent.Reference() != snapshot.Reference() || packageContent.Snapshot().Reference() != snapshot.Reference() {
		t.Fatalf("package content reference = %v, want %v", packageContent.Reference(), snapshot.Reference())
	}
	firstArchiveByte := snapshot.archive[0]
	snapshot.archive[0] ^= 0xff
	if _, readErr := ReadPackageFile(packageContent.Snapshot(), PackageEntrypoint); readErr != nil {
		t.Fatalf("package content retained a mutable input snapshot: %v", readErr)
	}
	snapshot.archive[0] = firstArchiveByte
	if _, implementsV1Validation := any(packageContent).(interface{ Validation() []string }); implementsV1Validation {
		t.Fatal("package-backed v2 content must not satisfy the v1 validation contract")
	}
	legacy, err := NewContent("legacy-skill", "Legacy Skill", "Use the legacy instructions.", []string{"Check the output."})
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy.Validation()) != 1 || legacy.Validation()[0] != "Check the output." {
		t.Fatalf("legacy validation = %v, want one v1 item", legacy.Validation())
	}

	restored := filepath.Join(t.TempDir(), "restored")
	if materializeErr := MaterializePackage(snapshot, restored); materializeErr != nil {
		t.Fatal(materializeErr)
	}
	for _, entry := range snapshot.Entries() {
		got, readErr := os.ReadFile(filepath.Join(restored, filepath.FromSlash(entry.Path())))
		if readErr != nil {
			t.Fatalf("read restored %q: %v", entry.Path(), readErr)
		}
		want, fileErr := ReadPackageFile(snapshot, entry.Path())
		if fileErr != nil {
			t.Fatalf("read canonical %q: %v", entry.Path(), fileErr)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("restored %q does not match canonical bytes", entry.Path())
		}
	}
	if executable, ok := packageEntry(snapshot, "scripts/verify.sh"); !ok || executable.Mode() != 0o755 {
		t.Fatalf("executable entry = %#v, want 0755", executable)
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filepath.Join(restored, "scripts", "verify.sh"))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("restored executable mode = %04o, want 0755", info.Mode().Perm())
		}
	}

	recaptured, err := CapturePackageArchive(snapshot.Archive())
	if err != nil {
		t.Fatal(err)
	}
	if recaptured.Reference() != snapshot.Reference() {
		t.Fatalf("archive reference = %v, want %v", recaptured.Reference(), snapshot.Reference())
	}
}

func TestPackageArchivesWithDifferentOrdersHaveOneCanonicalIdentity(t *testing.T) {
	packagePath := writeManagedPackage(t)
	paths := []string{"SKILL.md", "scripts/verify.sh", "references/policy.md", "assets/report.json", ".hidden-note"}
	first := zipManagedPackage(t, packagePath, paths)
	second := zipManagedPackage(t, packagePath, []string{paths[4], paths[2], paths[0], paths[3], paths[1]})

	left, err := CapturePackageArchive(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := CapturePackageArchive(second)
	if err != nil {
		t.Fatal(err)
	}
	if left.Reference() != right.Reference() || !bytes.Equal(left.Archive(), right.Archive()) || !bytes.Equal(left.Manifest(), right.Manifest()) {
		t.Fatalf("equivalent package archives diverged:\nleft=%v\nright=%v", left.Reference(), right.Reference())
	}
}

func TestPackageArchiveRejectsUntrustedEntriesBeforeAnyMaterialization(t *testing.T) {
	valid := []byte("---\nname: safe-skill\ndescription: Safe package.\n---\n")
	tests := []struct {
		name    string
		archive []byte
	}{
		{name: "traversal", archive: zipEntries(t, []zipEntry{{name: "SKILL.md", content: valid}, {name: "../outside", content: []byte("bad")}})},
		{name: "duplicate", archive: zipEntries(t, []zipEntry{{name: "SKILL.md", content: valid}, {name: "SKILL.md", content: valid}})},
		{name: "case collision", archive: zipEntries(t, []zipEntry{{name: "SKILL.md", content: valid}, {name: "References/Policy.md", content: []byte("one")}, {name: "references/policy.md", content: []byte("two")}})},
		{name: "unicode collision", archive: zipEntries(t, []zipEntry{{name: "SKILL.md", content: valid}, {name: "references/caf\u00e9.md", content: []byte("one")}, {name: "references/cafe\u0301.md", content: []byte("two")}})},
		{name: "symlink", archive: zipEntries(t, []zipEntry{{name: "SKILL.md", content: valid}, {name: "scripts/link", content: []byte("target"), mode: os.ModeSymlink | 0o777}})},
		{name: "special file", archive: zipEntries(t, []zipEntry{{name: "SKILL.md", content: valid}, {name: "scripts/pipe", mode: os.ModeNamedPipe | 0o644}})},
		{name: "bad frontmatter", archive: zipEntries(t, []zipEntry{{name: "SKILL.md", content: []byte("---\nname: [unterminated\n---\n")}})},
		{name: "entry over uncompressed bound", archive: zipEntries(t, []zipEntry{{name: "SKILL.md", content: valid}, {name: "assets/large.bin", content: bytes.Repeat([]byte("x"), MaxPackageBytes+1)}})},
		{name: "file count over bound", archive: zipPackageWithTooManyFiles(t, valid)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := CapturePackageArchive(test.archive)
			var packageErr *PackageError
			if !errors.As(err, &packageErr) {
				t.Fatalf("CapturePackageArchive error = %T %v, want PackageError", err, err)
			}
		})
	}
	if _, err := CapturePackageArchive(make([]byte, MaxPackageArchiveBytes+1)); err == nil {
		t.Fatal("archive larger than the compressed bound was accepted")
	}
}

func TestPackageArchiveRejectsAnyDotDotEntryNameBeforeOpening(t *testing.T) {
	archive := zipEntries(t, []zipEntry{
		{name: "SKILL.md", content: []byte("---\nname: safe-skill\ndescription: Safe package.\n---\n")},
		{name: "references/release..notes.md", content: []byte("untrusted")},
	})

	_, err := CapturePackageArchive(archive)
	var packageErr *PackageError
	if !errors.As(err, &packageErr) {
		t.Fatalf("CapturePackageArchive error = %T %v, want PackageError", err, err)
	}
}

func packageEntry(snapshot PackageSnapshot, path string) (PackageEntry, bool) {
	for _, entry := range snapshot.Entries() {
		if entry.Path() == path {
			return entry, true
		}
	}
	return PackageEntry{}, false
}

func writeManagedPackage(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "release-check")
	for _, directory := range []string{"scripts", "references", "assets"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{
		"SKILL.md":             []byte("---\nname: release-check\ndescription: Verify a release before publishing it.\nlicense: Apache-2.0\ncompatibility: Requires a supported host.\nmetadata:\n  owner: release-team\nallowed-tools: Bash(git:*) Read\n---\n\nRun the verification script and inspect its report.\n"),
		"references/policy.md": []byte("# Release policy\n"),
		"assets/report.json":   []byte("{\"status\":\"pending\"}\n"),
		".hidden-note":         []byte("Preserved.\n"),
	}
	for path, contents := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), contents, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(root, "scripts", "verify.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf 'verified\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

func zipManagedPackage(t *testing.T, root string, paths []string) []byte {
	t.Helper()
	entries := make([]zipEntry, 0, len(paths))
	for _, path := range paths {
		contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if path == "scripts/verify.sh" {
			mode = 0o755
		}
		entries = append(entries, zipEntry{name: path, content: contents, mode: mode})
	}
	return zipEntries(t, entries)
}

type zipEntry struct {
	name    string
	content []byte
	mode    os.FileMode
}

func zipEntries(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		writer, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.content); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func zipPackageWithTooManyFiles(t *testing.T, skillMarkdown []byte) []byte {
	t.Helper()
	entries := make([]zipEntry, 0, MaxPackageFiles+1)
	entries = append(entries, zipEntry{name: "SKILL.md", content: skillMarkdown})
	for index := range MaxPackageFiles {
		entries = append(entries, zipEntry{name: "references/file-" + strconv.Itoa(index) + ".txt", content: []byte("x")})
	}
	return zipEntries(t, entries)
}

func TestPackageReadReturnsVerifiedFileBytes(t *testing.T) {
	archive := zipEntries(t, []zipEntry{{
		name:    "SKILL.md",
		content: []byte("---\nname: read-only\ndescription: Read only.\n---\n"),
	}})
	snapshot, err := CapturePackageArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	content, err := ReadPackageFile(snapshot, "SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(content, []byte("name: read-only")) {
		t.Fatalf("package content = %q", content)
	}
	if _, err := ReadPackageFile(snapshot, "missing.md"); err == nil {
		t.Fatal("missing package file was readable")
	}
}

func TestPackageSnapshotOperationsRejectArchiveMismatch(t *testing.T) {
	snapshot, err := CapturePackageArchive(zipEntries(t, []zipEntry{{
		name:    "SKILL.md",
		content: []byte("---\nname: immutable\ndescription: Immutable.\n---\n"),
	}}))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.archive[0] ^= 0xff
	operations := []struct {
		name string
		run  func() error
	}{
		{name: "content", run: func() error { _, err := NewPackageContent(snapshot); return err }},
		{name: "read", run: func() error { _, err := ReadPackageFile(snapshot, "SKILL.md"); return err }},
		{name: "materialize", run: func() error { return MaterializePackage(snapshot, filepath.Join(t.TempDir(), "output")) }},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			var packageErr *PackageError
			if err := operation.run(); !errors.As(err, &packageErr) {
				t.Fatalf("operation error = %T %v, want PackageError", err, err)
			}
		})
	}
}
