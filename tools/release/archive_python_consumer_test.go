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

//go:build archive_consumer

package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

const pythonArchiveSmoke = `
import json
import sys
from importlib.metadata import distribution
from pathlib import Path

%s

package_root = Path(sys.argv[1]).resolve()
archive_root = Path(sys.argv[2]).resolve()
package_module = sys.modules[%q]

direct_url_payload = distribution(%q).read_text("direct_url.json")
assert direct_url_payload is not None, "editable install has no direct_url.json"
direct_url = json.loads(direct_url_payload)
assert direct_url["dir_info"]["editable"] is True, direct_url
assert direct_url["url"].rstrip("/") == package_root.as_uri().rstrip("/"), direct_url

module_path = Path(package_module.__file__).resolve()
module_path.relative_to(archive_root)
package_root.relative_to(archive_root)

%s
`

type pythonArchiveConsumer struct {
	distribution string
	module       string
	imports      string
	smoke        string
}

type archivedPythonConsumer struct {
	integration releaseIntegration
	consumer    pythonArchiveConsumer
	projectRoot string
}

func TestReleaseArchivePythonAdaptersConsumeExtractedArtifact(t *testing.T) {
	archive := strings.TrimSpace(os.Getenv("POWERCONTEXT_ARCHIVE"))
	if archive == "" {
		t.Fatal("POWERCONTEXT_ARCHIVE is required with the archive_consumer build tag")
	}
	archive, err := filepath.Abs(archive)
	if err != nil {
		t.Fatalf("resolve POWERCONTEXT_ARCHIVE: %v", err)
	}
	archiveInfo, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("read POWERCONTEXT_ARCHIVE: %v", err)
	}
	if !archiveInfo.Mode().IsRegular() {
		t.Fatal("POWERCONTEXT_ARCHIVE must name a regular release archive")
	}
	uv, err := exec.LookPath("uv")
	if err != nil {
		t.Fatal("uv is required with the archive_consumer build tag")
	}

	releaseRoot := extractPythonConsumerArchive(t, archive)
	repository := filepath.Clean(filepath.Join("..", ".."))
	integrations, err := readReleaseIntegrations(repository)
	if err != nil {
		t.Fatal(err)
	}
	consumers := map[string]pythonArchiveConsumer{
		"bub": {
			distribution: "powercontext-bub",
			module:       "powercontext_bub",
			imports:      "from powercontext_bub import PowerContextPlugin",
			smoke:        "assert callable(PowerContextPlugin)",
		},
		"langchain": {
			distribution: "powercontext-langchain",
			module:       "powercontext_langchain",
			imports:      "from powercontext_langchain import PowerContextMiddleware",
			smoke:        "assert PowerContextMiddleware() is not None",
		},
		"langgraph": {
			distribution: "powercontext-langgraph",
			module:       "powercontext_langgraph",
			imports:      "from powercontext_langgraph import PowerContextRecall, powercontext_tools",
			smoke:        "assert PowerContextRecall() is not None\nassert len(powercontext_tools()) == 3",
		},
		"pydantic-ai": {
			distribution: "powercontext-pydantic-ai",
			module:       "powercontext_pydantic_ai",
			imports:      "from powercontext_pydantic_ai import PowerContext, PowerContextToolset",
			smoke:        "assert PowerContext() is not None\nassert PowerContextToolset() is not None",
		},
	}

	seen := make(map[string]struct{}, len(consumers))
	selected := make([]archivedPythonConsumer, 0, len(consumers))
	for _, integration := range integrations {
		if integration.Class != "python-package" || integration.ConsumerMode != "python" {
			continue
		}
		consumer, ok := consumers[integration.ID]
		if !ok {
			t.Fatalf("release inventory has unexpected Python consumer %q", integration.ID)
		}
		seen[integration.ID] = struct{}{}
		projectRoot := archivedPythonProjectRoot(t, releaseRoot, integration)
		selected = append(selected, archivedPythonConsumer{
			integration: integration,
			consumer:    consumer,
			projectRoot: projectRoot,
		})
	}
	for id := range consumers {
		if _, ok := seen[id]; !ok {
			t.Errorf("release inventory is missing Python consumer %q", id)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	for _, archived := range selected {
		t.Run(archived.integration.ID, func(t *testing.T) {
			sync := exec.CommandContext(
				t.Context(), uv, "sync", "--project", archived.projectRoot, "--locked", "--no-dev", "--no-progress",
			)
			sync.Dir = releaseRoot
			sync.Env = isolatedPythonConsumerEnvironment()
			if output, syncErr := sync.CombinedOutput(); syncErr != nil {
				t.Fatalf("sync archived integration %q from its uv.lock: %v\n%s", archived.integration.ID, syncErr, output)
			}

			script := fmt.Sprintf(
				pythonArchiveSmoke,
				archived.consumer.imports,
				archived.consumer.module,
				archived.consumer.distribution,
				archived.consumer.smoke,
			)
			run := exec.CommandContext(
				t.Context(), uv, "run", "--project", archived.projectRoot, "--locked", "--no-dev", "--no-sync",
				"python", "-I", "-c", script, archived.projectRoot, releaseRoot,
			)
			run.Dir = releaseRoot
			run.Env = isolatedPythonConsumerEnvironment()
			if output, runErr := run.CombinedOutput(); runErr != nil {
				t.Fatalf("smoke archived integration %q: %v\n%s", archived.integration.ID, runErr, output)
			}
		})
	}
}

func archivedPythonProjectRoot(t *testing.T, releaseRoot string, integration releaseIntegration) string {
	t.Helper()
	prefix := "integrations/" + integration.ID + "/"
	wantProject := prefix + "pyproject.toml"
	wantLock := prefix + "uv.lock"
	if len(integration.RequiredPaths) != 1 || integration.RequiredPaths[0] != wantProject {
		t.Fatalf("release inventory Python consumer %q project paths = %v, want [%s]", integration.ID, integration.RequiredPaths, wantProject)
	}
	if len(integration.LockPaths) != 1 || integration.LockPaths[0] != wantLock {
		t.Fatalf("release inventory Python consumer %q lock paths = %v, want [%s]", integration.ID, integration.LockPaths, wantLock)
	}
	for _, releasePath := range []string{wantProject, wantLock} {
		info, err := os.Stat(filepath.Join(releaseRoot, filepath.FromSlash(releasePath)))
		if err != nil {
			t.Fatalf("archived integration %q is missing declared path %q", integration.ID, releasePath)
		}
		if !info.Mode().IsRegular() {
			t.Fatalf("archived integration %q declared path %q is not a regular file", integration.ID, releasePath)
		}
	}
	return filepath.Join(releaseRoot, "integrations", integration.ID)
}

func isolatedPythonConsumerEnvironment() []string {
	blocked := map[string]struct{}{
		"PYTHONHOME":             {},
		"PYTHONPATH":             {},
		"UV_PROJECT":             {},
		"UV_PROJECT_ENVIRONMENT": {},
		"UV_WORKING_DIR":         {},
		"VIRTUAL_ENV":            {},
	}
	environment := make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		if _, ok := blocked[strings.ToUpper(name)]; !ok {
			environment = append(environment, value)
		}
	}
	return append(environment, "PYTHONNOUSERSITE=1")
}

func extractPythonConsumerArchive(t *testing.T, archive string) string {
	t.Helper()
	input, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := input.Close(); closeErr != nil {
			t.Errorf("close release archive: %v", closeErr)
		}
	}()
	compressed, err := gzip.NewReader(input)
	if err != nil {
		t.Fatalf("open release archive compression: %v", err)
	}
	defer func() {
		if closeErr := compressed.Close(); closeErr != nil {
			t.Errorf("close release archive compression: %v", closeErr)
		}
	}()

	destination := t.TempDir()
	reader := tar.NewReader(compressed)
	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("read release archive: %v", nextErr)
		}
		target, targetErr := containedArchivePath(destination, header.Name)
		if targetErr != nil {
			t.Fatal(targetErr)
		}
		if mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if mkdirErr := os.MkdirAll(target, header.FileInfo().Mode()); mkdirErr != nil {
				t.Fatal(mkdirErr)
			}
		case tar.TypeReg:
			output, createErr := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, header.FileInfo().Mode())
			if createErr != nil {
				t.Fatal(createErr)
			}
			_, copyErr := io.Copy(output, reader)
			closeErr := output.Close()
			if copyErr != nil {
				t.Fatal(copyErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		case tar.TypeSymlink:
			linkTarget, linkErr := containedArchivePath(filepath.Dir(target), header.Linkname)
			if linkErr != nil || !isPathWithin(destination, linkTarget) {
				t.Fatalf("release archive symlink %q escapes its root", header.Name)
			}
			if symlinkErr := os.Symlink(filepath.FromSlash(header.Linkname), target); symlinkErr != nil {
				t.Fatal(symlinkErr)
			}
		default:
			t.Fatalf("release archive contains unsupported entry %q with type %d", header.Name, header.Typeflag)
		}
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("release archive must contain one top-level directory, found %d entries", len(entries))
	}
	return filepath.Join(destination, entries[0].Name())
}

func containedArchivePath(root, name string) (string, error) {
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || filepath.IsAbs(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("release archive path %q escapes its root", name)
	}
	target := filepath.Join(root, filepath.FromSlash(clean))
	if !isPathWithin(root, target) {
		return "", fmt.Errorf("release archive path %q escapes its root", name)
	}
	return target, nil
}

func isPathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
