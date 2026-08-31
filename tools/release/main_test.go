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

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLicenseInventoryWritesBoundedDependencyEvidence(t *testing.T) {
	t.Parallel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Clean(filepath.Join("..", ".."))
	output := filepath.Join(t.TempDir(), "dependencies.json")
	var stdout bytes.Buffer
	if inventoryErr := runLicenseInventory([]string{
		"-binary", binary, "-edition", "standard", "-output", output, "-repository", repository,
		"-modules", "test/downstream",
	}, &stdout); inventoryErr != nil {
		t.Fatal(inventoryErr)
	}
	var result licenseInventoryResult
	if decodeErr := json.Unmarshal(stdout.Bytes(), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if result.GoModules == 0 || result.NativeDependencies != 1 || result.Output != "dependencies.json" {
		t.Fatalf("license inventory result = %#v", result)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var manifest dependencyManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || len(manifest.Modules) != result.GoModules || len(manifest.Native) != result.NativeDependencies {
		t.Fatalf("license manifest = %#v", manifest)
	}
	if len(manifest.Native[0].Licenses) != 1 || manifest.Native[0].Path != "github.com/asg017/sqlite-vec" {
		t.Fatalf("native license evidence = %#v", manifest.Native)
	}
}

func TestReleaseEvidenceVerifierRejectsSBOMModuleOmission(t *testing.T) {
	root := t.TempDir()
	writeReleaseEvidenceFixture(t, root, true, `{"spdxVersion":"SPDX-2.3","packages":[]}`)

	output, commandErr := runReleaseEvidenceVerifier(t, root)
	if commandErr == nil {
		t.Fatalf("release evidence verifier accepted an SBOM without the manifest module:\n%s", output)
	}
	if !strings.Contains(string(output), "missing Go module") {
		t.Fatalf("release evidence verifier did not report the omitted Go module:\n%s", output)
	}
}

func TestReleaseEvidenceVerifierRejectsLicenseRecordMissingFromNotice(t *testing.T) {
	root := t.TempDir()
	writeReleaseEvidenceFixture(t, root, false, `{
  "spdxVersion": "SPDX-2.3",
  "packages": [{"externalRefs": [{
    "referenceCategory": "PACKAGE-MANAGER",
    "referenceType": "purl",
    "referenceLocator": "pkg:golang/example.com/covered@v1.2.3"
  }]}]
}`)

	output, commandErr := runReleaseEvidenceVerifier(t, root)
	if commandErr == nil {
		t.Fatalf("release evidence verifier accepted a missing license notice:\n%s", output)
	}
	if !strings.Contains(string(output), "missing license notice") {
		t.Fatalf("release evidence verifier did not report the missing license notice:\n%s", output)
	}
}

func TestReleaseEvidenceVerifierAcceptsReconciledEvidence(t *testing.T) {
	root := t.TempDir()
	writeReleaseEvidenceFixture(t, root, true, `{
  "spdxVersion": "SPDX-2.3",
  "packages": [{"externalRefs": [{
    "referenceCategory": "PACKAGE-MANAGER",
    "referenceType": "purl",
    "referenceLocator": "pkg:golang/example.com/covered@v1.2.3"
  }]}]
}`)

	output, commandErr := runReleaseEvidenceVerifier(t, root)
	if commandErr != nil {
		t.Fatalf("release evidence verifier rejected reconciled evidence: %v\n%s", commandErr, output)
	}
}

func TestReleaseEvidenceVerifierRejectsDependencyWithoutLicenseRecord(t *testing.T) {
	root := t.TempDir()
	writeReleaseEvidenceFixture(t, root, true, `{
  "spdxVersion": "SPDX-2.3",
  "packages": [{"externalRefs": [{
    "referenceCategory": "PACKAGE-MANAGER",
    "referenceType": "purl",
    "referenceLocator": "pkg:golang/example.com/covered@v1.2.3"
  }]}]
}`)
	manifest := dependencyManifest{
		SchemaVersion: 1,
		Modules:       []dependencyRecord{{Path: "example.com/covered", Version: "v1.2.3"}},
		Native: []dependencyRecord{{
			Path: "example.com/native", Version: "v4.5.6",
			Licenses: []licenseRecord{{Name: "NOTICE", SHA256: strings.Repeat("b", 64)}},
		}},
	}
	dependencies, err := json.Marshal(&manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "DEPENDENCIES.json"), dependencies, 0o600); err != nil {
		t.Fatal(err)
	}

	output, commandErr := runReleaseEvidenceVerifier(t, root)
	if commandErr == nil {
		t.Fatalf("release evidence verifier accepted a dependency without license records:\n%s", output)
	}
	if !strings.Contains(string(output), "missing license record") {
		t.Fatalf("release evidence verifier did not report the missing license record:\n%s", output)
	}
}

func writeReleaseEvidenceFixture(t *testing.T, root string, includeNotices bool, sbom string) {
	t.Helper()
	manifest := dependencyManifest{
		SchemaVersion: 1,
		Modules: []dependencyRecord{{
			Path: "example.com/covered", Version: "v1.2.3",
			Licenses: []licenseRecord{{Name: "LICENSE", SHA256: strings.Repeat("a", 64)}},
		}},
		Native: []dependencyRecord{{
			Path: "example.com/native", Version: "v4.5.6",
			Licenses: []licenseRecord{{Name: "NOTICE", SHA256: strings.Repeat("b", 64)}},
		}},
	}
	dependencies, err := json.Marshal(&manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "DEPENDENCIES.json"), dependencies, 0o600); err != nil {
		t.Fatal(err)
	}
	if includeNotices {
		var notices strings.Builder
		for _, dependency := range append(slices.Clone(manifest.Modules), manifest.Native...) {
			writeNoticeHeader(&notices, dependency.Path, dependency.Version)
			for _, license := range dependency.Licenses {
				writeLicense(&notices, license.Name, []byte("license text\n"))
			}
		}
		if err := os.WriteFile(filepath.Join(root, "THIRD-PARTY-LICENSES.txt"), []byte(notices.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "SBOM.spdx.json"), []byte(sbom), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runReleaseEvidenceVerifier(t *testing.T, root string) ([]byte, error) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "go", "run", ".", "verify-evidence", "-root", root)
	command.Dir = "."
	return command.CombinedOutput()
}

func TestGenerateSBOMUsesStablePublicIdentity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a POSIX shell")
	}
	helpRoot := t.TempDir()
	helper := filepath.Join(helpRoot, "syft")
	script := `#!/bin/sh
set -eu
test "$1" = scan
test "$3" = --source-name
test "$4" = powercontext-1.2.3-test
test "$5" = --output
output=${6#spdx-json=}
printf '%s' '{"spdxVersion":"SPDX-2.3","name":"private-path","documentNamespace":"private-path","creationInfo":{"created":"now","creators":["unknown"]}}' > "$output"
`
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	privateParent := filepath.Join(t.TempDir(), "private-builder-path")
	root := filepath.Join(privateParent, "powercontext-1.2.3-test")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "sbom.spdx.json")
	created := time.Date(2026, time.August, 17, 10, 33, 34, 0, time.UTC)
	if err := generateSBOM(helper, root, output, "1.51.0", created); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	value := string(contents)
	for _, forbidden := range []string{privateParent, "private-path", `"created": "now"`, "syft-[not provided]"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("SBOM contains unstable or private value %q: %s", forbidden, value)
		}
	}
	for _, required := range []string{
		`"name": "powercontext-1.2.3-test"`,
		`"documentNamespace": "https://github.com/ob-labs/powercontext-go/sbom/powercontext-1.2.3-test"`,
		`"created": "2026-08-17T10:33:34Z"`,
		`"Tool: syft-1.51.0"`,
	} {
		if !strings.Contains(value, required) {
			t.Fatalf("SBOM is missing %q: %s", required, value)
		}
	}
}

func TestNativeAssetManifestSupportsReleaseMatrix(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	assets, err := readAssets(repository)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"} {
		if _, err := assetValue(assets, "tokenizers", target, "url"); err != nil {
			t.Errorf("tokenizers %s: %v", target, err)
		}
		onnx := assets.ONNXRuntime.Assets[target]
		if onnx.BuildFromSource {
			if target != "darwin-amd64" {
				t.Errorf("unexpected source-built target %s", target)
			}
			continue
		}
		if _, err := assetValue(assets, "onnxruntime", target, "url"); err != nil {
			t.Errorf("ONNX Runtime %s: %v", target, err)
		}
	}
}

func TestDockerNativeAssetDefaultsMatchManifest(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	assets, err := readAssets(repository)
	if err != nil {
		t.Fatal(err)
	}
	dockerfile, err := os.ReadFile(filepath.Join(repository, "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"TOKENIZERS_VERSION":       assets.Tokenizers.Version,
		"TOKENIZERS_AMD64_SHA256":  assets.Tokenizers.Assets["linux-amd64"].SHA256,
		"TOKENIZERS_ARM64_SHA256":  assets.Tokenizers.Assets["linux-arm64"].SHA256,
		"ONNXRUNTIME_VERSION":      assets.ONNXRuntime.Version,
		"ONNXRUNTIME_AMD64_SHA256": assets.ONNXRuntime.Assets["linux-amd64"].SHA256,
		"ONNXRUNTIME_ARM64_SHA256": assets.ONNXRuntime.Assets["linux-arm64"].SHA256,
	}
	for name, value := range expected {
		if !strings.Contains(string(dockerfile), "ARG "+name+"="+value) {
			t.Errorf("Dockerfile does not pin %s from native-assets.json", name)
		}
	}
}

func TestCopyTreeRejectsEscapingSymlink(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "escape")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err := copyTree(source, destination)
	if err == nil || !strings.Contains(err.Error(), "escapes its source tree") {
		t.Fatalf("copyTree error = %v", err)
	}
}

func TestCopyTreeRejectsAbsoluteSymlink(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	destination := filepath.Join(t.TempDir(), "destination")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(source, "target")
	if err := os.WriteFile(target, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(source, "absolute")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err := copyTree(source, destination)
	if err == nil || !strings.Contains(err.Error(), "is absolute") {
		t.Fatalf("copyTree error = %v", err)
	}
}

func TestStageIntegrationsIncludesEveryRuntimeAdapterAndExcludesWorkspaceState(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	root := t.TempDir()
	if err := stageIntegrations(repository, root); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(root, ".claude-plugin", "marketplace.json")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("Claude Code marketplace manifest was not staged: %v", err)
	}
	for _, required := range []string{
		"bub/src/powercontext_bub/client.py",
		"claude-code/plugins/powercontext/.claude-plugin/plugin.json",
		"codex/plugins/powercontext/.codex-plugin/plugin.json",
		"dsh/plugins/powercontext/lib/index.js",
		"hermes/plugins/powercontext/plugin.yaml",
		"langgraph/src/powercontext_langgraph/client.py",
		"openclaw/plugins/memory-powercontext/dist/index.js",
		"opencode/plugins/powercontext/lib/index.js",
		"pi/plugins/powercontext/extensions/powercontext.ts",
	} {
		path := filepath.Join(root, "integrations", filepath.FromSlash(required))
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Errorf("runtime adapter %q was not staged: %v", required, err)
		}
	}
	err := filepath.WalkDir(filepath.Join(root, "integrations"), func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if slices.Contains([]string{".venv", "__pycache__", "node_modules", ".pytest_cache"}, entry.Name()) {
			t.Errorf("workspace-only entry %q was staged", entry.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestReleaseArchiveProvidesConsumableAdapterSources(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("release adapter consumption is verified against the Linux release artifact")
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	archive := buildAdapterConsumerArchive(t, repository)
	releaseRoot := unpackReleaseArchive(t, archive)

	for _, required := range []string{
		".claude-plugin/marketplace.json",
		"integrations/bub/pyproject.toml",
		"integrations/claude-code/plugins/powercontext/.claude-plugin/plugin.json",
		"integrations/codex/plugins/powercontext/.codex-plugin/plugin.json",
		"integrations/dsh/plugins/powercontext/lib/index.js",
		"integrations/hermes/plugins/powercontext/plugin.yaml",
		"integrations/langgraph/pyproject.toml",
		"integrations/openclaw/plugins/memory-powercontext/dist/index.js",
		"integrations/opencode/plugins/powercontext/lib/index.js",
		"integrations/pi/plugins/powercontext/extensions/powercontext.ts",
	} {
		info, err := os.Stat(filepath.Join(releaseRoot, filepath.FromSlash(required)))
		if err != nil || !info.Mode().IsRegular() {
			t.Errorf("release artifact adapter entrypoint %q is unavailable: %v", required, err)
		}
	}

	binDirectory, commandLog := writeAdapterConsumerHosts(t)
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_HOST_LOG", commandLog)
	t.Setenv("FAKE_PI_PACKAGE", filepath.Join(releaseRoot, "integrations", "pi", "plugins", "powercontext"))
	t.Setenv("FAKE_OPENCODE_CONFIG", filepath.Join(t.TempDir(), "opencode"))

	for _, host := range []string{"codex", "claude-code", "dsh", "hermes", "opencode", "openclaw", "pi"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("POWERCONTEXT_HOME", filepath.Join(t.TempDir(), "powercontext-home"))
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "claude"))
			t.Setenv("HERMES_HOME", filepath.Join(t.TempDir(), "hermes"))
			command := exec.CommandContext(
				t.Context(), filepath.Join(releaseRoot, "bin", "powercontext"), "setup", host, "--source", releaseRoot,
			)
			output, commandErr := command.CombinedOutput()
			if commandErr != nil {
				t.Fatalf("packaged %s setup failed: %v\n%s", host, commandErr, output)
			}
			if !strings.Contains(string(output), "setup complete") {
				t.Fatalf("packaged %s setup output = %q", host, output)
			}
		})
	}

	commands, err := os.ReadFile(commandLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"codex|plugin marketplace add " + releaseRoot,
		"claude|plugin marketplace add " + releaseRoot,
		"dsh|plugin --profile web add " + filepath.Join(releaseRoot, "integrations", "dsh", "plugins", "powercontext"),
		"opencode|plugin " + filepath.Join(releaseRoot, "integrations", "opencode", "plugins", "powercontext"),
		"openclaw|plugins install --link --force " + filepath.Join(releaseRoot, "integrations", "openclaw", "plugins", "memory-powercontext"),
		"pi|install " + filepath.Join(releaseRoot, "integrations", "pi", "plugins", "powercontext"),
	} {
		if !strings.Contains(string(commands), expected) {
			t.Errorf("packaged setup did not consume expected release adapter path %q:\n%s", expected, commands)
		}
	}
}

func buildAdapterConsumerArchive(t *testing.T, repository string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "powercontext")
	build := exec.CommandContext(t.Context(), "go", "build", "-tags", "sqlite_fts5", "-o", binary, "./cmd/powercontext")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build release CLI: %v\n%s", err, output)
	}
	root := filepath.Join(t.TempDir(), "powercontext-0.0.0-linux-amd64")
	if err := stageRelease(repository, root, packageOptions{Binary: binary}, binaryFacts{}); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "powercontext.tar.gz")
	if err := archiveTree(root, archive, time.Unix(0, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	return archive
}

func unpackReleaseArchive(t *testing.T, archive string) string {
	t.Helper()
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close release artifact: %v", closeErr)
		}
	}()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := compressed.Close(); closeErr != nil {
			t.Errorf("close compressed release artifact: %v", closeErr)
		}
	}()
	root := t.TempDir()
	reader := tar.NewReader(compressed)
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		path := filepath.Join(root, filepath.FromSlash(header.Name))
		if parentErr := os.MkdirAll(filepath.Dir(path), 0o755); parentErr != nil {
			t.Fatal(parentErr)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if directoryErr := os.MkdirAll(path, header.FileInfo().Mode()); directoryErr != nil {
				t.Fatal(directoryErr)
			}
		case tar.TypeReg:
			output, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, header.FileInfo().Mode())
			if createErr != nil {
				t.Fatal(createErr)
			}
			if _, copyErr := io.Copy(output, reader); copyErr != nil {
				_ = output.Close()
				t.Fatal(copyErr)
			}
			if closeErr := output.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
		case tar.TypeSymlink:
			if symlinkErr := os.Symlink(header.Linkname, path); symlinkErr != nil {
				t.Fatal(symlinkErr)
			}
		default:
			t.Fatalf("unexpected release archive entry %q with type %d", header.Name, header.Typeflag)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("release archive roots = %#v, want one directory", entries)
	}
	return filepath.Join(root, entries[0].Name())
}

func writeAdapterConsumerHosts(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	commandLog := filepath.Join(t.TempDir(), "host-commands.log")
	script := `#!/usr/bin/env sh
set -eu
name="$(basename "$0")"
printf '%s|%s\n' "$name" "$*" >> "$FAKE_HOST_LOG"
first="${1-}"
second="${2-}"
third="${3-}"
case "$name" in
  claude)
    if [ "$first $second $third" = "plugin marketplace list" ]; then
      printf '[]\n'
    elif [ "$first $second" = "plugin list" ]; then
      printf '[{"id":"powercontext@powercontext","enabled":true,"version":"0.0.0"}]\n'
    fi
    ;;
  codex)
    if [ "$first $second $third" = "plugin marketplace add" ]; then
      printf '{"marketplaceName":"powercontext"}\n'
    elif [ "$first $second" = "plugin add" ]; then
      printf '{"name":"powercontext","version":"0.0.0"}\n'
    elif [ "$first $second" = "plugin list" ]; then
      printf '{"installed":[{"name":"powercontext","installed":true,"enabled":true,"pluginId":"powercontext@powercontext"}]}\n'
    fi
    ;;
  dsh)
    if [ "$first $second $third" = "--profile web --dump-config" ]; then
      printf 'id: powercontext-dsh\n'
    fi
    ;;
  hermes)
    if [ "$first" = "--version" ]; then
      printf 'Hermes Agent v0.20.4\n'
    fi
    ;;
  opencode)
    if [ "$first" = "--version" ]; then
      printf '1.18.21\n'
    elif [ "$first $second" = "debug paths" ]; then
      printf 'config %s\n' "$FAKE_OPENCODE_CONFIG"
    fi
    ;;
  openclaw)
    if [ "$first" = "--version" ]; then
      printf '2026.8.1-beta.2\n'
    elif [ "$first $second $third" = "config get gateway.mode" ] || [ "$first $second $third" = "config get tools.alsoAllow" ]; then
      exit 1
    fi
    ;;
  pi)
    if [ "$first" = "list" ]; then
      printf 'User packages:\n  %s\n    %s\n' "$FAKE_PI_PACKAGE" "$FAKE_PI_PACKAGE"
    fi
    ;;
esac
`
	for _, name := range []string{"claude", "codex", "dsh", "hermes", "opencode", "openclaw", "pi", "pnpm"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return directory, commandLog
}

func TestCopyONNXRuntimeOnlyCopiesRuntimeLibraries(t *testing.T) {
	source := filepath.Join(t.TempDir(), "lib")
	destination := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(filepath.Join(source, "libonnxruntime.1.24.4.dylib.dSYM", "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	versioned := filepath.Join(source, "libonnxruntime.1.24.4.dylib")
	if err := os.WriteFile(versioned, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(versioned), filepath.Join(source, "libonnxruntime.dylib")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "libonnxruntime.pc"), []byte("metadata"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyONNXRuntime(source, destination, "darwin"); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if got, want := strings.Join(names, ","), "libonnxruntime.1.24.4.dylib,libonnxruntime.dylib"; got != want {
		t.Fatalf("runtime entries = %q, want %q", got, want)
	}
	link, err := os.Readlink(filepath.Join(destination, "libonnxruntime.dylib"))
	if err != nil {
		t.Fatal(err)
	}
	if link != filepath.Base(versioned) {
		t.Fatalf("runtime symlink = %q", link)
	}
}

func TestCopyONNXRuntimeRejectsEscapingSymlink(t *testing.T) {
	source := filepath.Join(t.TempDir(), "lib")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "libonnxruntime.1.dylib")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "libonnxruntime.dylib")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	err := copyONNXRuntime(source, filepath.Join(t.TempDir(), "runtime"), "darwin")
	if err == nil || !strings.Contains(err.Error(), "is absolute") {
		t.Fatalf("copyONNXRuntime error = %v", err)
	}
}

func TestONNXRuntimeLibraryNames(t *testing.T) {
	tests := map[string]bool{
		"darwin/libonnxruntime.dylib":                  true,
		"darwin/libonnxruntime.1.24.4.dylib":           true,
		"darwin/libonnxruntime_providers_shared.dylib": true,
		"darwin/libonnxruntime.dylib.dSYM":             false,
		"linux/libonnxruntime.so":                      true,
		"linux/libonnxruntime.so.1.24.4":               true,
		"linux/libonnxruntime_providers_shared.so":     true,
		"linux/libonnxruntime.so.debug":                false,
	}
	for key, want := range tests {
		goos, name, ok := strings.Cut(key, "/")
		if !ok {
			t.Fatalf("invalid test key %q", key)
		}
		if got := isONNXRuntimeLibrary(name, goos); got != want {
			t.Errorf("isONNXRuntimeLibrary(%q, %q) = %v, want %v", name, goos, got, want)
		}
	}
}

func TestArchiveTreeIsDeterministic(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "powercontext-1.2.3-test")
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "powercontext"), []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("license\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	timestamp := time.Date(2026, time.August, 17, 0, 0, 0, 0, time.UTC)
	first := filepath.Join(parent, "first.tar.gz")
	second := filepath.Join(parent, "second.tar.gz")
	if err := archiveTree(root, first, timestamp); err != nil {
		t.Fatal(err)
	}
	if err := archiveTree(root, second, timestamp); err != nil {
		t.Fatal(err)
	}
	firstHash, _, err := hashFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, _, err := hashFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("archives differ: %s != %s", firstHash, secondHash)
	}

	file, err := os.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close deterministic release archive: %v", closeErr)
		}
	}()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := gzipReader.Close(); err != nil {
			t.Errorf("close deterministic release archive reader: %v", err)
		}
	}()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if !header.ModTime.Equal(timestamp) || header.Uid != 0 || header.Gid != 0 {
			t.Fatalf("non-deterministic header %#v", header)
		}
	}
}

func TestReleaseArchiveKeepsStableExecutablePathAndMode(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "powercontext-1.2.3-linux-amd64")
	binary := filepath.Join(root, "bin", "powercontext")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(parent, "release.tar.gz")
	if err := archiveTree(root, archive, time.Unix(0, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close release archive: %v", closeErr)
		}
	}()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := compressed.Close(); err != nil {
			t.Errorf("close release archive reader: %v", err)
		}
	}()
	reader := tar.NewReader(compressed)
	found := false
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(header.Name, "/bin/powercontext") {
			found = true
			if header.Mode&0o111 == 0 {
				t.Fatalf("packaged binary mode = %#o", header.Mode)
			}
		}
	}
	if !found {
		t.Fatal("release archive does not contain bin/powercontext")
	}
}

func TestReleaseWorkflowPublishesCompleteAssetsAndAllowsPrereleases(t *testing.T) {
	repository := filepath.Clean(filepath.Join("..", ".."))
	payload, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(payload)
	for _, required := range []string{
		"types: [published]",
		"needs: [prepare, binaries, images]",
		"make package-standard",
		"make package-full",
		"Smoke test both released process surfaces",
		`test "$(find dist -name '*.tar.gz' | wc -l | tr -d ' ')" = 8`,
		`test "$(find dist -name '*.spdx.json' | wc -l | tr -d ' ')" = 8`,
		`test "$(wc -l < dist/SHA256SUMS | tr -d ' ')" = 17`,
		"dist/IMAGE-DIGESTS.json",
		"dist/SHA256SUMS",
		"fail_on_unmatched_files: true",
		"uses: ./.github/workflows/release-verify.yml",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("release workflow is missing %q", required)
		}
	}
	for _, prereleaseFilter := range []string{"prerelease == false", "prerelease: false", "!github.event.release.prerelease"} {
		if strings.Contains(workflow, prereleaseFilter) {
			t.Errorf("release workflow rejects published prereleases through %q", prereleaseFilter)
		}
	}
}

func TestTreeChecksumsUsePortableRelativePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "value.txt"), []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeTreeChecksums(root); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "cd42404d52ad55ccfa9aca4adc828aa5800ad9d385a0671fbcbf724118320619  nested/value.txt\n" {
		t.Fatalf("checksums = %q", contents)
	}
}

func TestLicenseDiscoveryIsStrictAndDeterministic(t *testing.T) {
	directory := t.TempDir()
	if _, err := findLicenseFiles(directory); err == nil {
		t.Fatal("missing license accepted")
	}
	for _, name := range []string{"NOTICE", "LICENSE.md", "source.go"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := findLicenseFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got := []string{filepath.Base(paths[0]), filepath.Base(paths[1])}; strings.Join(got, ",") != "LICENSE.md,NOTICE" {
		t.Fatalf("licenses = %v", got)
	}
}

func TestVerifyRejectsWrongDigestWithoutLeakingPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential-name")
	if err := os.WriteFile(path, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runVerify([]string{"-sha256", strings.Repeat("0", 64), path})
	if err == nil || strings.Contains(err.Error(), filepath.Dir(path)) || !strings.Contains(err.Error(), "credential-name") {
		t.Fatalf("verify error = %v", err)
	}
}
