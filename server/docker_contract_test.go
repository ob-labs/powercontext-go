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

package server

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var dockerServerEnvironmentPattern = regexp.MustCompile(
	`(?m)^\s*(?:ENV\s+)?(POWERCONTEXT_SERVER_[A-Z0-9_]+)=([^\s\\]+)`,
)

func TestDockerImageDeclaresControlledNonLoopbackBind(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	environment := make(map[string]string)
	for _, match := range dockerServerEnvironmentPattern.FindAllStringSubmatch(string(payload), -1) {
		environment[match[1]] = match[2]
	}
	for _, name := range []string{
		"POWERCONTEXT_SERVER_HTTP_HOST",
		"POWERCONTEXT_SERVER_HTTP_PORT",
		"POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK",
	} {
		value, ok := environment[name]
		if !ok {
			t.Fatalf("Dockerfile does not define %s", name)
		}
		t.Setenv(name, value)
	}
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.HTTP.Host != "0.0.0.0" || config.HTTP.Port != 8000 || !config.AllowUnauthenticatedNonLoopback {
		t.Fatalf("Docker Server config = %#v", config)
	}
}
