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

import "testing"

func TestServerPersonalServiceCommandsExposeLifecycleContract(t *testing.T) {
	command := newCommand(VersionInfo{}, nil, nil)
	server, _, err := command.Find([]string{"server"})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		hidden   bool
		required []string
		optional []string
	}{
		{name: "install", required: []string{"env-file"}, optional: []string{"data-dir", "start-on-login"}},
		{name: "status", optional: []string{"data-dir", "json"}},
		{name: "uninstall", optional: []string{"data-dir", "json"}},
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
