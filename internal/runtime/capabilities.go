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
	"fmt"
	"slices"

	"github.com/ob-labs/powercontext-go/artifact/memory"
)

const PreparedContextV1 = "powercontext.prepared-context.v1"

const (
	SupportedDatabaseSQLite         = "sqlite"
	SupportedExternalAgentCodex     = "codex"
	SupportedExternalAgentWorkBuddy = "workbuddy"
)

type Capabilities struct {
	sourceTypes            []string
	artifactFamilies       []string
	memoryExtraction       bool
	experienceGeneration   bool
	managedSkillGeneration bool
	externalSkillRegistry  bool
	handoffGeneration      bool
	searchModes            []memory.SearchMode
	contextVersions        []string
}

type CapabilityOptions struct {
	SourceTypes            []string
	ArtifactFamilies       []string
	MemoryExtraction       bool
	ExperienceGeneration   bool
	ManagedSkillGeneration bool
	ExternalSkillRegistry  bool
	HandoffGeneration      bool
	SearchModes            []memory.SearchMode
	ContextVersions        []string
}

func NewCapabilities(options CapabilityOptions) (Capabilities, error) {
	if err := uniqueNonEmpty("source type", options.SourceTypes); err != nil {
		return Capabilities{}, err
	}
	if err := uniqueNonEmpty("Artifact family", options.ArtifactFamilies); err != nil {
		return Capabilities{}, err
	}
	seenModes := make(map[memory.SearchMode]struct{}, len(options.SearchModes))
	for _, mode := range options.SearchModes {
		if mode != memory.SearchAuto && mode != memory.SearchFTS && mode != memory.SearchVector && mode != memory.SearchHybrid {
			return Capabilities{}, fmt.Errorf("runtime: unsupported Memory search mode %q", mode)
		}
		if _, duplicate := seenModes[mode]; duplicate {
			return Capabilities{}, fmt.Errorf("runtime: duplicate Memory search mode %q", mode)
		}
		seenModes[mode] = struct{}{}
	}
	if err := uniqueNonEmpty("context version", options.ContextVersions); err != nil {
		return Capabilities{}, err
	}
	for _, version := range options.ContextVersions {
		if version != PreparedContextV1 {
			return Capabilities{}, fmt.Errorf("runtime: unsupported context version %q", version)
		}
	}
	return Capabilities{
		sourceTypes: slices.Clone(options.SourceTypes), artifactFamilies: slices.Clone(options.ArtifactFamilies),
		memoryExtraction: options.MemoryExtraction, experienceGeneration: options.ExperienceGeneration,
		managedSkillGeneration: options.ManagedSkillGeneration, externalSkillRegistry: options.ExternalSkillRegistry,
		handoffGeneration: options.HandoffGeneration, searchModes: slices.Clone(options.SearchModes),
		contextVersions: slices.Clone(options.ContextVersions),
	}, nil
}

func EmptyCapabilities() Capabilities { return Capabilities{} }

func SupportedDatabases() []string {
	return []string{SupportedDatabaseSQLite}
}

func SupportedExternalAgents() []string {
	return []string{SupportedExternalAgentCodex, SupportedExternalAgentWorkBuddy}
}

func (c Capabilities) SourceTypes() []string            { return slices.Clone(c.sourceTypes) }
func (c Capabilities) ArtifactFamilies() []string       { return slices.Clone(c.artifactFamilies) }
func (c Capabilities) MemoryExtraction() bool           { return c.memoryExtraction }
func (c Capabilities) ExperienceGeneration() bool       { return c.experienceGeneration }
func (c Capabilities) ManagedSkillGeneration() bool     { return c.managedSkillGeneration }
func (c Capabilities) ExternalSkillRegistry() bool      { return c.externalSkillRegistry }
func (c Capabilities) HandoffGeneration() bool          { return c.handoffGeneration }
func (c Capabilities) SearchModes() []memory.SearchMode { return slices.Clone(c.searchModes) }
func (c Capabilities) ContextVersions() []string        { return slices.Clone(c.contextVersions) }

func uniqueNonEmpty(label string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("runtime: %s must not be empty", label)
		}
		if _, duplicate := seen[value]; duplicate {
			return fmt.Errorf("runtime: duplicate %s %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
