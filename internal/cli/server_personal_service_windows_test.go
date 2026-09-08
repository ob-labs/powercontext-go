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

//go:build windows

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

func TestWindowsPersonalServiceRegistrationUsesCredentialFreeEnvironmentIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "service-root")
	envFile := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(envFile, []byte("POWERCONTEXT_SERVER_DATABASE_KIND=seekdb\nPOWERCONTEXT_SERVER_HTTP_HOST=127.0.0.1\nPOWERCONTEXT_SERVER_HTTP_PORT=7614\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	registration, plan, resolvedRoot, err := newWindowsPersonalServiceRegistration(t.Context(), &commandState{version: VersionInfo{Version: "test"}}, envFile, root, true)
	if err != nil {
		t.Fatal(err)
	}
	resolvedEnv, err := filepath.EvalSymlinks(envFile)
	if err != nil {
		t.Fatal(err)
	}
	definition := registration.Definition()
	if definition.Ownership() != personalsvc.OwnershipMarker || definition.Version() != personalsvc.DefinitionVersion ||
		definition.PackageVersion() != "test" || definition.DataDir() != resolvedRoot || definition.EnvFile() != filepath.Clean(resolvedEnv) ||
		definition.Endpoint() != "http://127.0.0.1:7614" || !plan.StartOnLogin() || plan.Registration() != registration {
		t.Fatalf("registration/plan = %#v %#v", definition, plan)
	}
	wantArgs := []string{"server", "_service-run", "--env-file", definition.EnvFile(), "--endpoint", definition.Endpoint(), "--data-dir", definition.DataDir()}
	if !slices.Equal(plan.Arguments(), wantArgs) {
		t.Fatalf("Task Scheduler arguments = %#v, want %#v", plan.Arguments(), wantArgs)
	}
	if _, statErr := os.Stat(resolvedRoot); statErr != nil {
		t.Fatalf("private root was not created: %v", statErr)
	}
}

func TestWindowsPersonalServiceEnvironmentFileRejectsMissingOrRelativePaths(t *testing.T) {
	for _, value := range []string{"", "relative.env", filepath.Join(t.TempDir(), "missing.env")} {
		if _, err := windowsPersonalServiceEnvFile(value); err == nil {
			t.Fatalf("windowsPersonalServiceEnvFile(%q) unexpectedly succeeded", value)
		}
	}
	if _, _, _, err := newWindowsPersonalServiceRegistration(context.Background(), nil, "relative.env", t.TempDir(), false); err == nil {
		t.Fatal("relative environment identity reached configuration loading")
	}
}

func TestWindowsPersonalServiceStatusInspectsWithoutCreatingARegistration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "status-root")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	state := &commandState{stdout: &output}
	if err := runPersonalServiceStatus(t.Context(), state, root); err != nil {
		t.Fatal(err)
	}
	if output.Len() == 0 {
		t.Fatal("status did not render a result")
	}
	if _, err := os.Stat(filepath.Join(root, "PowerContext", "Services", "personal-server.xml")); !os.IsNotExist(err) {
		t.Fatalf("status created a personal-service artifact: %v", err)
	}
}
