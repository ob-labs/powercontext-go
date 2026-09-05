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

package runtime

import (
	"slices"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/memory"
)

func TestCapabilitiesAreValidatedAndImmutable(t *testing.T) {
	t.Parallel()
	sources := []string{"content"}
	capabilities, err := NewCapabilities(CapabilityOptions{
		SourceTypes: sources, ArtifactFamilies: []string{"memory", "experience", "skill", "handoff"},
		MemoryExtraction: true, ExperienceGeneration: true, ManagedSkillGeneration: true,
		ExternalSkillRegistry: true, HandoffGeneration: true,
		SearchModes:     []memory.SearchMode{memory.SearchAuto, memory.SearchFTS},
		ContextVersions: []string{PreparedContextV1},
	})
	if err != nil {
		t.Fatal(err)
	}
	sources[0] = "mutated"
	got := capabilities.SourceTypes()
	got[0] = "also-mutated"
	if !slices.Equal(capabilities.SourceTypes(), []string{"content"}) {
		t.Fatalf("SourceTypes mutated: %#v", capabilities.SourceTypes())
	}
	if !capabilities.MemoryExtraction() || !capabilities.HandoffGeneration() {
		t.Fatal("boolean capabilities were lost")
	}
}

func TestCapabilitiesRejectDuplicatesAndUnknownValues(t *testing.T) {
	t.Parallel()
	for _, options := range []CapabilityOptions{
		{SourceTypes: []string{"content", "content"}},
		{SearchModes: []memory.SearchMode{"unknown"}},
		{ContextVersions: []string{"powercontext.future"}},
	} {
		if _, err := NewCapabilities(options); err == nil {
			t.Fatalf("NewCapabilities(%#v) succeeded", options)
		}
	}
}

func TestSupportedProductMatrixIsExactAndImmutable(t *testing.T) {
	t.Parallel()
	databases := SupportedDatabases()
	agents := SupportedExternalAgents()
	if !slices.Equal(databases, []string{"sqlite"}) || !slices.Equal(agents, []string{"codex", "workbuddy"}) {
		t.Fatalf("supported product matrix = (%v, %v)", databases, agents)
	}
	databases[0] = "mutated"
	agents[0] = "mutated"
	if !slices.Equal(SupportedDatabases(), []string{"sqlite"}) ||
		!slices.Equal(SupportedExternalAgents(), []string{"codex", "workbuddy"}) {
		t.Fatal("supported product matrix is mutable")
	}
}
