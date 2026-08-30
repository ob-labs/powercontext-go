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
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/server"
)

var (
	documentedShellBlock = regexp.MustCompile("(?s)```(?:sh|bash)\\r?\\n(.*?)```")
	documentedEnvToken   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

type documentedServerRun struct {
	environment map[string]string
	arguments   []string
	host        string
	port        string
}

func TestRemoteAccessDocumentationIncludesNonLoopbackServerCommand(t *testing.T) {
	commands := readDocumentedServerRuns(t)
	for _, command := range commands {
		if command.host == "0.0.0.0" {
			return
		}
	}
	t.Fatal("README.md does not document a non-loopback Server command")
}

func TestDocumentedServerRunCommandsStart(t *testing.T) {
	clearServerEnvironment(t)
	for _, documented := range readDocumentedServerRuns(t) {
		t.Run(documented.host+":"+documented.port, func(t *testing.T) {
			for name, value := range documented.environment {
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
			command.SetArgs(documented.arguments)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatalf("documented command failed: %v", err)
			}
			if !called {
				t.Fatal("documented command did not reach the Server runner")
			}
			port, err := strconv.Atoi(documented.port)
			if err != nil {
				t.Fatalf("documented port %q is not numeric: %v", documented.port, err)
			}
			if received.HTTP.Host != documented.host || received.HTTP.Port != port {
				t.Fatalf("Server address = %s:%d, want %s:%d", received.HTTP.Host, received.HTTP.Port, documented.host, port)
			}
		})
	}
}

func readDocumentedServerRuns(t *testing.T) []documentedServerRun {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]documentedServerRun, 0)
	for _, match := range documentedShellBlock.FindAllStringSubmatch(string(payload), -1) {
		block := strings.ReplaceAll(match[1], "\\\r\n", " ")
		block = strings.ReplaceAll(block, "\\\n", " ")
		for line := range strings.SplitSeq(block, "\n") {
			fields := strings.Fields(line)
			command, ok := parseDocumentedServerRun(fields)
			if ok {
				commands = append(commands, command)
			}
		}
	}
	if len(commands) == 0 {
		t.Fatal("README.md does not contain a documented Server command")
	}
	return commands
}

func parseDocumentedServerRun(fields []string) (documentedServerRun, bool) {
	environment := make(map[string]string)
	index := 0
	for index < len(fields) && documentedEnvToken.MatchString(fields[index]) {
		name, value, _ := strings.Cut(fields[index], "=")
		environment[name] = strings.Trim(value, `'"`)
		index++
	}
	for index+2 < len(fields) {
		if path.Base(fields[index]) == "powercontext" && fields[index+1] == "server" && fields[index+2] == "run" {
			arguments := fields[index+1:]
			host := documentedOption(arguments, "--host", "127.0.0.1")
			port := documentedOption(arguments, "--port", "8000")
			return documentedServerRun{environment: environment, arguments: arguments, host: host, port: port}, true
		}
		index++
	}
	return documentedServerRun{}, false
}

func documentedOption(arguments []string, name, fallback string) string {
	for index, argument := range arguments {
		if argument == name && index+1 < len(arguments) {
			return arguments[index+1]
		}
	}
	return fallback
}

func clearServerEnvironment(t *testing.T) {
	t.Helper()
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
}
