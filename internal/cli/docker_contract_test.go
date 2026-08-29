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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/server"
)

func TestDockerImageServerCommandStartsWithDeclaredEnvironment(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`(?m)^\s*(?:ENV\s+)?(POWERCONTEXT_SERVER_[A-Z0-9_]+)=([^\s\\]+)`)
	environment := make(map[string]string)
	for _, match := range pattern.FindAllStringSubmatch(string(payload), -1) {
		environment[match[1]] = match[2]
	}
	for _, item := range os.Environ() {
		name, value, ok := strings.Cut(item, "=")
		if !ok || !strings.HasPrefix(name, "POWERCONTEXT_SERVER_") {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = os.Setenv(name, value)
		})
	}
	for name, value := range environment {
		t.Setenv(name, value)
	}
	t.Setenv(server.PowerContextHomeEnv, t.TempDir())

	called := false
	var received server.ProcessConfig
	runner := func(_ context.Context, _ *commandState, config server.ProcessConfig) error {
		called = true
		received = config
		return nil
	}
	var stdout, stderr bytes.Buffer
	command := newCommandWithDependencies(VersionInfo{Version: "test"}, &stdout, &stderr, nil, runner)
	command.SetArgs([]string{"server", "run"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("Docker Server configuration did not reach the runner")
	}
	if received.HTTP.Host != "0.0.0.0" || received.HTTP.Port != 8000 || !received.AllowUnauthenticatedNonLoopback {
		t.Fatalf("Docker Server config = %#v", received)
	}
}
