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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandRejectsForbiddenPublicSDKImport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "client", "client.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package client\n\nimport \"os\"\n\nvar _ = os.Getenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "go", "run", ".", "-root", root)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("portable SDK check accepted a forbidden import:\n%s", output)
	}
	if !strings.Contains(string(output), `client/client.go imports forbidden package "os"`) {
		t.Fatalf("portable SDK check output = %q", output)
	}
}

func TestCommandAllowsTheIntentionalExternalSkillFilesystemBoundary(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact", "skill", "projection.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package skill\n\nimport \"os\"\n\nvar _ = os.ReadFile\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.CommandContext(t.Context(), "go", "run", ".", "-root", root)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("portable SDK check rejected the external Skill filesystem boundary: %v\n%s", err, output)
	}
}
