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
	"crypto/sha256"
	"encoding/hex"
	json "encoding/json/v2"
	"errors"
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
	if before.Support != "supported" || before.Registration != "not_installed" {
		t.Fatalf("initial release personal service status = %#v", before)
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
		// Type=exec may report the post-commit liveness observation before the
		// new process is ready. Do not hide an install failure unless the same
		// archive already observes the only durable state that permits a retry.
		postCommit := readReleasePersonalServiceStatus(t, binary)
		if postCommit.Registration != "installed" || postCommit.Definition != "current" || postCommit.ManagerOwnership != "owned" {
			t.Fatal("release personal service install did not establish an owned current registration")
		}
	}
	t.Cleanup(func() {
		_, _ = runReleasePersonalService(t, binary, "server", "uninstall")
	})

	endpoint := "http://127.0.0.1:" + strconv.Itoa(port)
	awaitReleasePersonalService(t, endpoint)
	data, err := os.Stat(filepath.Join(dataDir, "powercontext.db"))
	if err != nil || !data.Mode().IsRegular() {
		t.Fatal("release personal service did not create a SQLite database file")
	}
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
		t.Fatalf("inspect release personal service: %s", releasePersonalServiceFailureClass(output, err))
	}
	var status releasePersonalServiceStatus
	if err := json.Unmarshal(output, &status); err != nil {
		t.Fatal("decode release personal service status")
	}
	return status
}

func releasePersonalServiceFailureClass(output []byte, commandErr error) string {
	value := string(output)
	switch {
	case strings.Contains(value, "personal Server service registration failed"):
		return "registration"
	case strings.Contains(value, "systemd user service unit inspect failed"):
		return "unit_inspect"
	case strings.Contains(value, "systemd user service inspect manager failed"):
		return "manager_inspect"
	case strings.Contains(value, "personal service operation failed during inspect_manager"):
		return "manager_inspect"
	case strings.Contains(value, "systemd user service manager command failed"):
		return "manager_command"
	case strings.Contains(value, "systemd user service support failed"):
		return "support"
	case strings.Contains(value, "personal Server service configuration failed"):
		return "configuration"
	case strings.Contains(value, "personal Server service lifecycle is not available"):
		return "unsupported"
	}
	if exitError, found := errors.AsType[*exec.ExitError](commandErr); found {
		return "redacted_exit_" + strconv.Itoa(exitError.ExitCode()) + "_bytes_" + strconv.Itoa(len(output)) + "_sha256_" + releasePersonalServiceFailureDigest(output)
	}
	return "redacted_non_exit_bytes_" + strconv.Itoa(len(output)) + "_sha256_" + releasePersonalServiceFailureDigest(output)
}

func releasePersonalServiceFailureDigest(output []byte) string {
	digest := sha256.Sum256(output)
	return hex.EncodeToString(digest[:8])
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
		ready := true
		for _, path := range []string{"/health/live", "/health/ready"} {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, endpoint+path, nil)
			if err != nil {
				t.Fatal("construct release health request")
			}
			response, requestErr := client.Do(request)
			if requestErr != nil {
				ready = false
				continue
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				ready = false
			}
		}
		if ready {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("release personal service did not become live and ready")
}
