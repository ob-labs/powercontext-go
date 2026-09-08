//go:build linux && systemd_user_consumer

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
	json "encoding/json/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const personalServiceConsumerArchive = "POWERCONTEXT_PERSONAL_SERVICE_ARCHIVE"

// TestReleaseArchiveProvidesConsumablePersonalService must run only in a
// disposable Linux systemd --user manager. It deliberately fails when that
// manager or the archive input is absent: neither condition is evidence that
// a release binary can own a native personal service lifecycle.
func TestReleaseArchiveProvidesConsumablePersonalService(t *testing.T) {
	archive := strings.TrimSpace(os.Getenv(personalServiceConsumerArchive))
	if archive == "" {
		t.Fatalf("%s is required", personalServiceConsumerArchive)
	}
	releaseRoot := unpackReleaseArchive(t, archive)
	binary := filepath.Join(releaseRoot, "bin", "powercontext")
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Fatalf("release archive has no executable bin/powercontext")
	}

	before := readReleasePersonalServiceStatus(t, binary)
	if before.Support != "supported" {
		t.Fatal("a usable systemd --user manager is required")
	}
	if before.Registration != "not_installed" {
		t.Fatal("disposable user manager already has a personal service registration")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("reserve loopback port")
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal("release loopback port")
	}

	environment := filepath.Join(t.TempDir(), "server.env")
	if err := os.WriteFile(environment, []byte("POWERCONTEXT_SERVER_HTTP_PORT="+strconv.Itoa(port)+"\n"), 0o600); err != nil {
		t.Fatal("write private environment file")
	}
	dataDir := filepath.Join(t.TempDir(), "data")
	if _, err := runReleasePersonalService(t, binary, "server", "install", "--env-file", environment, "--data-dir", dataDir); err != nil {
		// A post-commit liveness observation can race the new Type=exec process.
		// The persisted registration is checked below through the same archive binary.
	}
	t.Cleanup(func() {
		_, _ = runReleasePersonalService(t, binary, "server", "uninstall")
	})

	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	awaitReleasePersonalService(t, endpoint)
	installed := readReleasePersonalServiceStatus(t, binary)
	if installed.Registration != "installed" || installed.Definition != "current" || installed.ManagerOwnership != "owned" ||
		installed.Manager != "active" || installed.Liveness != "live" || installed.Recovery != "" {
		t.Fatalf("release personal service status = %#v", installed)
	}

	if _, err := runReleasePersonalService(t, binary, "server", "uninstall"); err != nil {
		t.Fatal("uninstall release personal service")
	}
	after := readReleasePersonalServiceStatus(t, binary)
	if after.Registration != "not_installed" {
		t.Fatalf("uninstall status = %#v", after)
	}
}

type releasePersonalServiceStatus struct {
	Support          string `json:"support"`
	Registration     string `json:"registration"`
	Definition       string `json:"definition"`
	ManagerOwnership string `json:"manager_ownership"`
	Manager          string `json:"manager"`
	Liveness         string `json:"liveness"`
	Recovery         string `json:"recovery"`
}

func readReleasePersonalServiceStatus(t *testing.T, binary string) releasePersonalServiceStatus {
	t.Helper()
	output, err := runReleasePersonalService(t, binary, "server", "status", "--json")
	if err != nil {
		t.Fatal("inspect release personal service")
	}
	var status releasePersonalServiceStatus
	if err := json.Unmarshal(output, &status); err != nil {
		t.Fatal("decode release personal service status")
	}
	return status
}

func runReleasePersonalService(t *testing.T, binary string, arguments ...string) ([]byte, error) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binary, arguments...)
	command.Env = os.Environ()
	return command.CombinedOutput()
}

func awaitReleasePersonalService(t *testing.T, endpoint string) {
	t.Helper()
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{Proxy: nil},
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(endpoint + "/health/live")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("release personal service did not become live")
}
