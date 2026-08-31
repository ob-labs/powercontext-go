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

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	openCodeProbeHelperEnabled = "POWERCONTEXT_OPENCODE_PROBE_HELPER"
	openCodeProbeHelperMode    = "POWERCONTEXT_OPENCODE_PROBE_HELPER_MODE"
	openCodeProbeHelperPID     = "POWERCONTEXT_OPENCODE_PROBE_HELPER_PID"
)

func TestRunOpenCodeProbeExecutesRequestWaitsForNonceAndStopsProcess(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "active")
	pidPath := filepath.Join(directory, "pid")
	port, err := availableOpenCodeProbePort()
	if err != nil {
		t.Fatal(err)
	}
	environment := openCodeProbeHelperEnvironment("success", marker, pidPath)
	t.Cleanup(func() { stopOpenCodeProbeHelper(t, pidPath) })
	requests := 0
	requester := func(_ context.Context, endpoint string) error {
		if endpoint != "http://127.0.0.1:"+strconv.Itoa(port)+"/session" {
			t.Fatalf("probe endpoint = %q", endpoint)
		}
		if _, probeStatErr := os.Stat(pidPath); errors.Is(probeStatErr, os.ErrNotExist) {
			return nil
		} else if probeStatErr != nil {
			return probeStatErr
		}
		requests++
		value := "wrong-nonce"
		if requests > 1 {
			value = "expected-nonce"
		}
		return os.WriteFile(marker, []byte(value), 0o600)
	}

	if probeErr := runOpenCodeProbeProcessWithRequest(
		t.Context(), openCodeProbeHelperCommand(port), environment, 5*time.Second, requester,
	); probeErr != nil {
		t.Fatalf("%v; requests = %d", probeErr, requests)
	}
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "expected-nonce" {
		t.Fatalf("activation marker = %q, error = %v", content, err)
	}
	assertOpenCodeProbeHelperStopped(t, pidPath)
}

func TestRunOpenCodeProbeHandlesProcessExitAndStopsProcess(t *testing.T) {
	port, err := availableOpenCodeProbePort()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	marker := filepath.Join(directory, "active")
	pidPath := filepath.Join(directory, "pid")
	environment := openCodeProbeHelperEnvironment("exit", marker, pidPath)
	t.Cleanup(func() { stopOpenCodeProbeHelper(t, pidPath) })

	if err := runOpenCodeProbeProcessWithRequest(
		t.Context(), openCodeProbeHelperCommand(port), environment, 5*time.Second, func(context.Context, string) error { return nil },
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("activation marker exists after early process exit: %v", err)
	}
	assertOpenCodeProbeHelperStopped(t, pidPath)
}

func TestRunOpenCodeProbeTimesOutAndStopsProcess(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "active")
	pidPath := filepath.Join(directory, "pid")
	port, err := availableOpenCodeProbePort()
	if err != nil {
		t.Fatal(err)
	}
	environment := openCodeProbeHelperEnvironment("timeout", marker, pidPath)
	t.Cleanup(func() { stopOpenCodeProbeHelper(t, pidPath) })
	requests := 0

	err = runOpenCodeProbeProcessWithRequest(
		t.Context(), openCodeProbeHelperCommand(port), environment, 200*time.Millisecond,
		func(context.Context, string) error { requests++; return nil },
	)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("probe error = %v", err)
	}
	if requests == 0 {
		t.Fatal("activation request was never attempted before timeout")
	}
	assertOpenCodeProbeHelperStopped(t, pidPath)
}

func TestOpenCodeProbeHelperProcess(t *testing.T) {
	if os.Getenv(openCodeProbeHelperEnabled) != "1" {
		return
	}
	pidPath := os.Getenv(openCodeProbeHelperPID)
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := os.Getenv(openCodeProbeHelperMode)
	if mode == "exit" {
		return
	}
	select {}
}

func openCodeProbeHelperEnvironment(mode, marker, pidPath string) map[string]string {
	return map[string]string{
		openCodeProbeHelperEnabled: "1",
		openCodeProbeHelperMode:    mode,
		openCodeProbeHelperPID:     pidPath,
		openCodeProbePath:          marker,
		openCodeProbeNonce:         "expected-nonce",
	}
}

func openCodeProbeHelperCommand(port int) []string {
	return []string{
		os.Args[0], "-test.run=^TestOpenCodeProbeHelperProcess$", "--",
		"serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port),
	}
}

func assertOpenCodeProbeHelperStopped(t *testing.T, pidPath string) {
	t.Helper()
	payload, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(payload))
	if err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if err := process.Kill(); err == nil {
		t.Fatalf("OpenCode probe process %d was still running", pid)
	}
}

func stopOpenCodeProbeHelper(t *testing.T, pidPath string) {
	t.Helper()
	payload, err := os.ReadFile(pidPath)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(string(payload))
	if err != nil {
		return
	}
	if process, findErr := os.FindProcess(pid); findErr == nil {
		_ = process.Kill()
	}
}
