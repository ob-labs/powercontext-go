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
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestServerPersonalServiceCommandsExposeThePortableContract(t *testing.T) {
	command := newCommand(VersionInfo{}, nil, nil)
	server, _, err := command.Find([]string{"server"})
	if err != nil {
		t.Fatalf("server command = %v", err)
	}
	for _, test := range []struct {
		name     string
		hidden   bool
		required []string
		optional []string
	}{
		{name: "install", required: []string{"env-file"}, optional: []string{"data-dir"}},
		{name: "status", optional: []string{"json"}},
		{name: "uninstall", optional: []string{"json"}},
		{name: "_service-run", hidden: true, required: []string{"env-file", "endpoint", "data-dir"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			child, _, findErr := server.Find([]string{test.name})
			if findErr != nil || child.Name() != test.name {
				t.Fatalf("server %s command = %v, %v", test.name, child, findErr)
			}
			if child.Hidden != test.hidden {
				t.Fatalf("server %s Hidden = %t, want %t", test.name, child.Hidden, test.hidden)
			}
			for _, name := range test.required {
				flag := child.Flags().Lookup(name)
				if flag == nil || flag.Annotations["cobra_annotation_bash_completion_one_required_flag"] == nil {
					t.Fatalf("server %s --%s is not required", test.name, name)
				}
			}
			for _, name := range test.optional {
				if child.Flags().Lookup(name) == nil && child.InheritedFlags().Lookup(name) == nil {
					t.Fatalf("server %s --%s is missing", test.name, name)
				}
			}
		})
	}
}

func TestServerPersonalServiceCommandsRejectMissingOrBlankValuesBeforeNativeEffects(t *testing.T) {
	for _, arguments := range [][]string{
		{"server", "install"},
		{"server", "install", "--env-file="},
		{"server", "install", "--env-file", " /etc/powercontext/server.env"},
		{"server", "install", "--env-file", "/etc/powercontext/server.env", "--data-dir="},
		{"server", "install", "--env-file", "/etc/powercontext/server.env", "--data-dir", "/var/lib/powercontext "},
		{"server", "_service-run", "--endpoint", "http://127.0.0.1:7614", "--data-dir", "/var/lib/powercontext"},
		{"server", "_service-run", "--env-file", "/etc/powercontext/server.env", "--data-dir", "/var/lib/powercontext"},
		{"server", "_service-run", "--env-file", "/etc/powercontext/server.env", "--endpoint", "http://127.0.0.1:7614"},
		{"server", "_service-run", "--env-file=", "--endpoint", "http://127.0.0.1:7614", "--data-dir", "/var/lib/powercontext"},
		{"server", "_service-run", "--env-file", "/etc/powercontext/server.env", "--endpoint=", "--data-dir", "/var/lib/powercontext"},
		{"server", "_service-run", "--env-file", "/etc/powercontext/server.env", "--endpoint", " http://127.0.0.1:7614", "--data-dir", "/var/lib/powercontext"},
		{"server", "_service-run", "--env-file", "/etc/powercontext/server.env", "--endpoint", "http://127.0.0.1:7614", "--data-dir="},
	} {
		t.Run(strings.Join(arguments[1:], " "), func(t *testing.T) {
			system := &scriptedSystemCommands{t: t}
			command := newCommandWithAllDependencies(VersionInfo{}, nil, nil, nil, nil, system)
			command.SetArgs(arguments)
			err := command.ExecuteContext(t.Context())
			if _, found := errors.AsType[*UsageError](err); !found || ExitCode(err) != 2 {
				t.Fatalf("ExecuteContext() error = %T %v, want typed usage error", err, err)
			}
			if len(system.calls) != 0 || len(system.lookups) != 0 {
				t.Fatalf("invalid command reached native boundary: calls=%v lookups=%v", system.calls, system.lookups)
			}
		})
	}
}

func TestServerPersonalServiceCommandsAreTypedUnsupportedWithoutNativeEffectsOnNonLinux(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-Linux unsupported behavior")
	}
	for _, arguments := range [][]string{
		{"server", "install", "--env-file", "/etc/powercontext/server.env"},
		{"server", "status", "--json"},
		{"server", "uninstall", "--json"},
		{"server", "_service-run", "--env-file", "/etc/powercontext/server.env", "--endpoint", "http://127.0.0.1:7614", "--data-dir", "/var/lib/powercontext"},
	} {
		t.Run(strings.Join(arguments[1:], " "), func(t *testing.T) {
			system := &scriptedSystemCommands{t: t}
			command := newCommandWithAllDependencies(VersionInfo{}, nil, nil, nil, nil, system)
			command.SetArgs(arguments)
			err := command.ExecuteContext(t.Context())
			if _, found := errors.AsType[*UnsupportedPersonalServiceError](err); !found || ExitCode(err) != 1 {
				t.Fatalf("ExecuteContext() error = %T %v, want typed unsupported error", err, err)
			}
			if len(system.calls) != 0 || len(system.lookups) != 0 {
				t.Fatalf("unsupported command reached native boundary: calls=%v lookups=%v", system.calls, system.lookups)
			}
		})
	}
}
