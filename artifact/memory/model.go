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

package memory

import (
	"fmt"
	"math"
	"slices"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/source"
)

const Family = "memory"

type EntryState string

const (
	Active   EntryState = "active"
	Inactive EntryState = "inactive"
)

type ChangeOp string

const (
	Add        ChangeOp = "add"
	Revise     ChangeOp = "revise"
	Deactivate ChangeOp = "deactivate"
	Reactivate ChangeOp = "reactivate"
)

type SearchMode string

const (
	SearchFTS    SearchMode = "fts"
	SearchVector SearchMode = "vector"
	SearchHybrid SearchMode = "hybrid"
	SearchAuto   SearchMode = "auto"
)

type MatchedBy string

const (
	MatchedFTS    MatchedBy = "fts"
	MatchedVector MatchedBy = "vector"
)

type EmbeddingProfile struct {
	ProfileID     string
	Model         string
	Dimension     int
	Distance      string
	Normalization string
}

func NewEmbeddingProfile(profileID, model string, dimension int, normalization string) (EmbeddingProfile, error) {
	if profileID == "" || model == "" {
		return EmbeddingProfile{}, fmt.Errorf("embedding profile identifiers must not be empty")
	}
	if dimension < 1 {
		return EmbeddingProfile{}, &CanonicalError{Code: "dimension-positive"}
	}
	if normalization != "none" && normalization != "unit" {
		return EmbeddingProfile{}, fmt.Errorf("unsupported embedding normalization %q", normalization)
	}
	return EmbeddingProfile{
		ProfileID: profileID, Model: model, Dimension: dimension,
		Distance: "l2", Normalization: normalization,
	}, nil
}

type Capabilities struct {
	FTS              bool
	Vector           bool
	Hybrid           bool
	EmbeddingProfile *EmbeddingProfile
}

type ManifestEntry struct {
	entryID          string
	entryVersionID   string
	entryContentHash string
	state            EntryState
}

func NewManifestEntry(entryID, entryVersionID, entryContentHash string, state EntryState) (ManifestEntry, error) {
	if _, err := ValidateIdentifier(entryID); err != nil {
		return ManifestEntry{}, err
	}
	if _, err := ValidateIdentifier(entryVersionID); err != nil {
		return ManifestEntry{}, err
	}
	if _, err := ValidateContentHash(entryContentHash); err != nil {
		return ManifestEntry{}, err
	}
	if state != Active && state != Inactive {
		return ManifestEntry{}, fmt.Errorf("invalid memory entry state %q", state)
	}
	return ManifestEntry{entryID: entryID, entryVersionID: entryVersionID, entryContentHash: entryContentHash, state: state}, nil
}

func (e ManifestEntry) EntryID() string          { return e.entryID }
func (e ManifestEntry) EntryVersionID() string   { return e.entryVersionID }
func (e ManifestEntry) EntryContentHash() string { return e.entryContentHash }
func (e ManifestEntry) State() EntryState        { return e.state }

type Manifest struct{ entries []ManifestEntry }

func NewManifest(entries []ManifestEntry) Manifest { return Manifest{entries: cloneValues(entries)} }
func (m Manifest) Format() string                  { return "flat-v1" }
func (m Manifest) Entries() []ManifestEntry        { return cloneValues(m.entries) }

type Change struct {
	op                 ChangeOp
	entryID            string
	fromEntryVersionID *string
	toEntryVersionID   *string
	reason             *string
}

func NewChange(op ChangeOp, entryID string, fromVersion, toVersion, reason *string) (Change, error) {
	if op != Add && op != Revise && op != Deactivate && op != Reactivate {
		return Change{}, fmt.Errorf("invalid memory change operation %q", op)
	}
	if _, err := ValidateIdentifier(entryID); err != nil {
		return Change{}, err
	}
	for _, value := range []*string{fromVersion, toVersion} {
		if value != nil {
			if _, err := ValidateIdentifier(*value); err != nil {
				return Change{}, err
			}
		}
	}
	normalizedReason, err := NormalizeReason(reason)
	if err != nil {
		return Change{}, err
	}
	return Change{
		op: op, entryID: entryID, fromEntryVersionID: cloneString(fromVersion),
		toEntryVersionID: cloneString(toVersion), reason: normalizedReason,
	}, nil
}

func (c Change) Op() ChangeOp                { return c.op }
func (c Change) EntryID() string             { return c.entryID }
func (c Change) FromEntryVersionID() *string { return cloneString(c.fromEntryVersionID) }
func (c Change) ToEntryVersionID() *string   { return cloneString(c.toEntryVersionID) }
func (c Change) Reason() *string             { return cloneString(c.reason) }
func (c Change) Clone() Change {
	c.fromEntryVersionID = cloneString(c.fromEntryVersionID)
	c.toEntryVersionID = cloneString(c.toEntryVersionID)
	c.reason = cloneString(c.reason)
	return c
}

type Content struct {
	manifest Manifest
	changes  []Change
}

func NewContent(manifest Manifest, changes []Change) Content {
	clonedChanges := make([]Change, len(changes))
	for index, change := range changes {
		clonedChanges[index] = change.Clone()
	}
	return Content{manifest: NewManifest(manifest.entries), changes: clonedChanges}
}

func (c Content) Schema() string     { return "powercontext.memory.v1" }
func (c Content) Manifest() Manifest { return NewManifest(c.manifest.entries) }
func (c Content) Changes() []Change {
	result := make([]Change, len(c.changes))
	for index, change := range c.changes {
		result[index] = change.Clone()
	}
	return result
}

type (
	Memory = artifact.Artifact[Content]
	Draft  = artifact.Draft[Content]
)

func NewDraft(content Content, sources []source.Ref, artifacts []artifact.Ref) (Draft, error) {
	return artifact.NewDraft(Family, content, sources, artifacts)
}

type EntryVersion struct {
	MemoryArtifactID  string
	EntryID           string
	EntryVersionID    string
	Version           int64
	PreviousVersionID *string
	Kind              string
	Text              string
	EntryContentHash  string
	CreatedInRevision int64
	Sources           []source.Ref
	Artifacts         []artifact.Ref
}

func (e EntryVersion) Clone() EntryVersion {
	e.PreviousVersionID = cloneString(e.PreviousVersionID)
	e.Sources = slices.Clone(e.Sources)
	e.Artifacts = slices.Clone(e.Artifacts)
	return e
}

type ChannelHit struct {
	MemoryRef      artifact.Ref
	EntryID        string
	EntryVersionID string
	Text           string
	Distance       *float64
}

func (h ChannelHit) Clone() ChannelHit {
	h.Distance = cloneFloat(h.Distance)
	return h
}

func NewChannelHit(memoryRef artifact.Ref, entryID, entryVersionID, text string, distance *float64) (ChannelHit, error) {
	if err := memoryRef.Validate(); err != nil {
		return ChannelHit{}, err
	}
	if _, err := ValidateIdentifier(entryID); err != nil {
		return ChannelHit{}, err
	}
	if _, err := ValidateIdentifier(entryVersionID); err != nil {
		return ChannelHit{}, err
	}
	if distance != nil && (*distance < 0 || math.IsNaN(*distance) || math.IsInf(*distance, 0)) {
		return ChannelHit{}, fmt.Errorf("memory channel distance must be finite and non-negative")
	}
	return ChannelHit{MemoryRef: memoryRef, EntryID: entryID, EntryVersionID: entryVersionID, Text: text, Distance: cloneFloat(distance)}, nil
}

type Hit struct {
	MemoryRef      artifact.Ref
	EntryID        string
	EntryVersionID string
	Text           string
	Score          float64
	MatchedBy      []MatchedBy
}

func (h Hit) Clone() Hit { h.MatchedBy = slices.Clone(h.MatchedBy); return h }

type RerankTrace struct {
	PolicyID           string
	CandidateHits      []Hit
	SelectedRanks      []int
	DiscardedRankCount int
	UsedFallback       bool
	LatencyMS          float64
	Usage              inference.Usage
}

type SearchResult struct {
	Mode   SearchMode
	Hits   []Hit
	Rerank *RerankTrace
}

type Citation struct {
	MemoryRef      artifact.Ref
	EntryID        string
	EntryVersionID string
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneValues[T any](values []T) []T {
	result := make([]T, len(values))
	copy(result, values)
	return result
}
