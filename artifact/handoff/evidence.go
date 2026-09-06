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

package handoff

import (
	"fmt"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/sourceevidence"
	"github.com/ob-labs/powercontext-go/source"
)

type Evidence interface {
	Citation() Citation
	evidenceValue()
}

type SourceEvidence struct {
	citation SourceCitation
	source   source.Value
}

func NewSourceEvidence(citation SourceCitation, value source.Value) (SourceEvidence, error) {
	evidence := SourceEvidence{citation: citation, source: value}
	if err := evidence.Validate(); err != nil {
		return SourceEvidence{}, err
	}
	return evidence, nil
}
func (e SourceEvidence) Citation() Citation   { return e.citation }
func (SourceEvidence) evidenceValue()         {}
func (e SourceEvidence) Source() source.Value { return e.source }
func (e SourceEvidence) Validate() error {
	if err := e.citation.Validate(); err != nil {
		return err
	}
	if err := sourceevidence.Require(e.source); err != nil {
		return err
	}
	if isNilInterface(e.source) || e.source.SourceName() != e.citation.ref.ID() {
		return fmt.Errorf("resolved Source does not match its Handoff citation")
	}
	return nil
}

type ArtifactEvidence struct {
	citation ArtifactCitation
	value    artifact.Snapshot
}

func NewArtifactEvidence(citation ArtifactCitation, value artifact.Snapshot) (ArtifactEvidence, error) {
	evidence := ArtifactEvidence{citation: citation, value: value}
	if err := evidence.Validate(); err != nil {
		return ArtifactEvidence{}, err
	}
	return evidence, nil
}
func (e ArtifactEvidence) Citation() Citation          { return e.citation }
func (ArtifactEvidence) evidenceValue()                {}
func (e ArtifactEvidence) Artifact() artifact.Snapshot { return e.value }
func (e ArtifactEvidence) Validate() error {
	if err := e.citation.Validate(); err != nil {
		return err
	}
	if isNilInterface(e.value) || e.value.Ref() != e.citation.ref {
		return fmt.Errorf("resolved Artifact does not match its Handoff citation")
	}
	return nil
}

type MemoryEvidence struct {
	citation MemoryCitation
	entry    memory.EntryVersion
}

func NewMemoryEvidence(citation MemoryCitation, entry memory.EntryVersion) (MemoryEvidence, error) {
	evidence := MemoryEvidence{citation: citation, entry: entry.Clone()}
	if err := evidence.Validate(); err != nil {
		return MemoryEvidence{}, err
	}
	return evidence, nil
}
func (e MemoryEvidence) Citation() Citation         { return e.citation }
func (MemoryEvidence) evidenceValue()               {}
func (e MemoryEvidence) Entry() memory.EntryVersion { return e.entry.Clone() }
func (e MemoryEvidence) Validate() error {
	if err := e.citation.Validate(); err != nil {
		return err
	}
	reference := e.citation.citation
	if reference.MemoryRef.ID() != e.entry.MemoryArtifactID || reference.EntryID != e.entry.EntryID ||
		reference.EntryVersionID != e.entry.EntryVersionID {
		return fmt.Errorf("resolved Memory entry does not match its Handoff citation")
	}
	return nil
}
