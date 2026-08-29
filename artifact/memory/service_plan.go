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
	"context"
	"reflect"
	"slices"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/source"
)

type operationEvidence struct {
	sources   []source.Value
	artifacts []artifact.Snapshot
}

type entryMaterial struct {
	kind         string
	text         string
	sources      []source.Ref
	artifacts    []artifact.Ref
	contentBytes []byte
	contentHash  string
}

func selectRememberMode(mode RememberMode, hasEntries, hasEvidence bool) (RememberMode, error) {
	switch mode {
	case RememberAppend:
		if !hasEntries {
			return "", &InvalidOperationError{Code: "append-entries"}
		}
		return RememberAppend, nil
	case RememberExtract:
		if hasEntries || !hasEvidence {
			return "", &InvalidOperationError{Code: "extract-evidence"}
		}
		return RememberExtract, nil
	case RememberAuto:
		if hasEntries {
			return RememberAppend, nil
		}
		if hasEvidence {
			return RememberExtract, nil
		}
		return "", &InvalidOperationError{Code: "no-work"}
	default:
		return "", &InvalidCandidateError{Code: "remember-mode", Detail: string(mode)}
	}
}

func (s *Service) canonicalBase(ctx context.Context, value *Memory) (*Memory, error) {
	if value == nil {
		return nil, nil
	}
	exact, err := s.backend.Get(ctx, value.Ref())
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(exact, *value) {
		return nil, &InvalidCitationError{Code: "base-mismatch"}
	}
	latest, err := s.backend.Latest(ctx, value.ID())
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(latest, exact) {
		return nil, &artifact.RevisionConflictError{Requested: value.Ref(), Current: latest.Ref()}
	}
	return cloneMemory(&exact), nil
}

func (s *Service) canonicalMemory(ctx context.Context, value Memory) (Memory, error) {
	exact, err := s.backend.Get(ctx, value.Ref())
	if err != nil {
		return Memory{}, err
	}
	if !reflect.DeepEqual(exact, value) {
		return Memory{}, &InvalidCitationError{Code: "memory-mismatch"}
	}
	return exact, nil
}

func (s *Service) canonicalOperationEvidence(
	ctx context.Context,
	sources []source.Value,
	artifacts []artifact.Snapshot,
) (operationEvidence, error) {
	result := operationEvidence{}
	for _, value := range sources {
		if s.sourceResolver == nil || isNilInterface(s.sourceResolver) {
			return operationEvidence{}, &InvalidEvidenceError{Code: "source-resolver"}
		}
		canonical, err := s.sourceResolver.Get(ctx, value)
		if err != nil {
			return operationEvidence{}, err
		}
		appendUniqueSource(&result.sources, canonical)
	}
	for _, value := range artifacts {
		if s.artifactResolver == nil || isNilInterface(s.artifactResolver) {
			return operationEvidence{}, &InvalidEvidenceError{Code: "artifact-resolver"}
		}
		canonical, err := s.artifactResolver.Get(ctx, value)
		if err != nil {
			return operationEvidence{}, err
		}
		appendUniqueArtifact(&result.artifacts, canonical)
	}
	return result, nil
}

func (s *Service) validatedEntries(ctx context.Context, value Memory) ([]EntryVersion, error) {
	versions, err := s.backend.Entries(ctx, value.Ref())
	if err != nil {
		return nil, err
	}
	byID := make(map[string]EntryVersion, len(versions))
	for _, version := range versions {
		if _, exists := byID[version.EntryVersionID]; exists {
			return nil, &InvalidCitationError{Code: "duplicate-versions"}
		}
		byID[version.EntryVersionID] = version.Clone()
	}
	manifest := value.Content().Manifest().Entries()
	ordered := make([]EntryVersion, 0, len(manifest))
	for _, item := range manifest {
		version, exists := byID[item.EntryVersionID()]
		if !exists {
			return nil, &InvalidCitationError{Code: "missing-version"}
		}
		if version.MemoryArtifactID != value.ID() || version.EntryID != item.EntryID() {
			return nil, &InvalidCitationError{Code: "cross-identity"}
		}
		material, err := s.materialFromVersion(version)
		if err != nil {
			return nil, err
		}
		if material.contentHash != item.EntryContentHash() {
			return nil, &InvalidCitationError{Code: "hash-mismatch"}
		}
		ordered = append(ordered, version.Clone())
	}
	return ordered, nil
}

func (s *Service) candidates(
	ctx context.Context,
	mode RememberMode,
	entries []EntryInput,
	evidence operationEvidence,
	base *Memory,
	currentEntries []EntryVersion,
) ([]EntryInput, error) {
	if mode == RememberAppend {
		return cloneEntryInputs(entries), nil
	}
	if s.candidatePipeline == nil || isNilInterface(s.candidatePipeline) {
		return nil, &CapabilityNotSupportedError{Capability: "extract"}
	}
	activeVersionIDs := make(map[string]struct{})
	if base != nil {
		for _, item := range base.Content().Manifest().Entries() {
			if item.State() == Active {
				activeVersionIDs[item.EntryVersionID()] = struct{}{}
			}
		}
	}
	bounded := make([]EntryVersion, 0, len(currentEntries))
	for _, entry := range currentEntries {
		if _, active := activeVersionIDs[entry.EntryVersionID]; active {
			bounded = append(bounded, entry.Clone())
		}
	}
	request, err := NewCandidateRequest(evidence.sources, evidence.artifacts, bounded)
	if err != nil {
		return nil, err
	}
	return s.candidatePipeline.Extract(ctx, request)
}

func (s *Service) prepareCommit(
	ctx context.Context,
	base *Memory,
	candidates []EntryInput,
	evidence operationEvidence,
	currentEntries []EntryVersion,
) (*Commit, error) {
	var memoryID string
	nextRevision := int64(1)
	manifest := make(map[string]ManifestEntry)
	if base == nil {
		var err error
		memoryID, err = s.newID("memory")
		if err != nil {
			return nil, err
		}
	} else {
		memoryID = base.ID()
		nextRevision = base.Revision() + 1
		manifest = manifestMap(base.Content().Manifest().Entries())
	}
	current := entryMap(currentEntries)
	newVersions := make([]EntryVersion, 0)
	changes := make([]Change, 0)
	targeted := make(map[string]struct{})
	newContent := make(map[string]struct{})
	for _, version := range currentEntries {
		if item, exists := manifest[version.EntryID]; exists && item.State() == Active {
			material, err := s.materialFromVersion(version)
			if err != nil {
				return nil, err
			}
			newContent[string(material.contentBytes)] = struct{}{}
		}
	}

	for _, candidate := range candidates {
		if candidate.entry == nil {
			material, err := s.materialFromCandidate(ctx, candidate, evidence.sources, evidence.artifacts, nil)
			if err != nil {
				return nil, err
			}
			if _, duplicate := newContent[string(material.contentBytes)]; duplicate {
				continue
			}
			newContent[string(material.contentBytes)] = struct{}{}
			entryID, err := s.newID("entry")
			if err != nil {
				return nil, err
			}
			if _, collision := manifest[entryID]; collision {
				return nil, &InvalidOperationError{Code: "id-collision"}
			}
			version, err := s.newEntryVersion(memoryID, entryID, nil, material, nextRevision)
			if err != nil {
				return nil, err
			}
			item, _ := NewManifestEntry(entryID, version.EntryVersionID, version.EntryContentHash, Active)
			manifest[entryID] = item
			current[entryID] = version
			newVersions = append(newVersions, version)
			change, err := NewChange(Add, entryID, nil, &version.EntryVersionID, candidate.reason)
			if err != nil {
				return nil, err
			}
			changes = append(changes, change)
			continue
		}

		entryID, previous, err := claimRevisionTarget(candidate, current, targeted)
		if err != nil {
			return nil, err
		}
		item, exists := manifest[entryID]
		if !exists {
			return nil, &EntryNotFoundError{EntryID: entryID}
		}
		if item.State() == Inactive {
			return nil, &EntryInactiveError{EntryID: entryID}
		}
		material, err := s.materialFromCandidate(ctx, candidate, evidence.sources, evidence.artifacts, &previous)
		if err != nil {
			return nil, err
		}
		previousMaterial, err := s.materialFromVersion(previous)
		if err != nil {
			return nil, err
		}
		if material.contentHash == previousMaterial.contentHash && bytesEqual(material.contentBytes, previousMaterial.contentBytes) {
			continue
		}
		version, err := s.newEntryVersion(memoryID, entryID, &previous, material, nextRevision)
		if err != nil {
			return nil, err
		}
		manifestItem, _ := NewManifestEntry(entryID, version.EntryVersionID, version.EntryContentHash, Active)
		manifest[entryID] = manifestItem
		current[entryID] = version
		newVersions = append(newVersions, version)
		change, err := NewChange(Revise, entryID, &previous.EntryVersionID, &version.EntryVersionID, candidate.reason)
		if err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	if len(changes) == 0 {
		return nil, nil
	}
	manifestEntries := sortedManifest(manifest)
	sortedChanges := sortedChanges(changes)
	content := NewContent(NewManifest(manifestEntries), sortedChanges)
	sourceRefs, err := s.sourceRefs(evidence.sources)
	if err != nil {
		return nil, err
	}
	artifactRefs := make([]artifact.Ref, len(evidence.artifacts))
	for index, value := range evidence.artifacts {
		artifactRefs[index] = value.Ref()
	}
	draft, err := NewDraft(content, sourceRefs, artifactRefs)
	if err != nil {
		return nil, err
	}
	next, err := artifact.New(memoryID, nextRevision, draft)
	if err != nil {
		return nil, err
	}
	changedIDs := make(map[string]struct{}, len(newVersions))
	for _, version := range newVersions {
		changedIDs[version.EntryVersionID] = struct{}{}
	}
	projections, err := s.prepareProjections(ctx, base, manifestEntries, current, changedIDs)
	if err != nil {
		return nil, err
	}
	contentHash, err := ContentHash(content)
	if err != nil {
		return nil, err
	}
	commit := NewCommit(base, next, contentHash, newVersions, projections)
	return &commit, nil
}

func claimRevisionTarget(
	candidate EntryInput,
	current map[string]EntryVersion,
	targeted map[string]struct{},
) (string, EntryVersion, error) {
	if candidate.entry == nil {
		return "", EntryVersion{}, &InvalidCitationError{Code: "entry-missing"}
	}
	entryID, err := ValidateIdentifier(candidate.entry.EntryID)
	if err != nil {
		return "", EntryVersion{}, err
	}
	if _, duplicate := targeted[entryID]; duplicate {
		return "", EntryVersion{}, &InvalidOperationError{Code: "duplicate-target"}
	}
	targeted[entryID] = struct{}{}
	previous, exists := current[entryID]
	if !exists {
		return "", EntryVersion{}, &EntryNotFoundError{EntryID: entryID}
	}
	if !reflect.DeepEqual(candidate.entry.Clone(), previous) {
		return "", EntryVersion{}, &InvalidCitationError{Code: "entry-mismatch"}
	}
	return entryID, previous.Clone(), nil
}

func (s *Service) materialFromCandidate(
	ctx context.Context,
	candidate EntryInput,
	allowedSources []source.Value,
	allowedArtifacts []artifact.Snapshot,
	previous *EntryVersion,
) (entryMaterial, error) {
	canonicalSources, err := s.canonicalCandidateSources(ctx, candidate.sources, allowedSources)
	if err != nil {
		return entryMaterial{}, err
	}
	candidateSourceRefs, err := s.sourceRefs(canonicalSources)
	if err != nil {
		return entryMaterial{}, err
	}
	var previousArtifacts []artifact.Ref
	if previous != nil {
		previousArtifacts = previous.Artifacts
	}
	candidateArtifactRefs, err := s.canonicalCandidateArtifacts(
		ctx, candidate.artifacts, allowedArtifacts, previousArtifacts,
	)
	if err != nil {
		return entryMaterial{}, err
	}
	selectedSources := candidateSourceRefs
	selectedArtifacts := candidateArtifactRefs
	if previous != nil {
		selectedSources = append(slices.Clone(previous.Sources), selectedSources...)
		selectedArtifacts = append(slices.Clone(previous.Artifacts), selectedArtifacts...)
	}
	material, err := newEntryMaterial(candidate.kind, candidate.text, selectedSources, selectedArtifacts)
	if err != nil {
		return entryMaterial{}, &InvalidCandidateError{Code: "canonical", Detail: err.Error()}
	}
	return material, nil
}

func (s *Service) canonicalCandidateSources(
	ctx context.Context,
	values []source.Value,
	allowed []source.Value,
) ([]source.Value, error) {
	result := make([]source.Value, 0, len(values))
	for _, value := range values {
		canonical := value
		var err error
		if s.sourceResolver != nil && !isNilInterface(s.sourceResolver) {
			canonical, err = s.sourceResolver.Get(ctx, value)
			if err != nil {
				return nil, err
			}
		}
		match := matchingSource(canonical, allowed)
		if match == nil {
			return nil, &InvalidEvidenceError{Code: "source-outside"}
		}
		appendUniqueSource(&result, match)
	}
	return result, nil
}

func (s *Service) canonicalCandidateArtifacts(
	ctx context.Context,
	values []artifact.Snapshot,
	allowed []artifact.Snapshot,
	previous []artifact.Ref,
) ([]artifact.Ref, error) {
	allowedRefs := make(map[artifact.Ref]struct{}, len(allowed)+len(previous))
	for _, value := range allowed {
		allowedRefs[value.Ref()] = struct{}{}
	}
	for _, ref := range previous {
		allowedRefs[ref] = struct{}{}
	}
	result := make([]artifact.Ref, 0, len(values))
	seen := make(map[artifact.Ref]struct{})
	for _, value := range values {
		canonical := value
		var err error
		if s.artifactResolver != nil && !isNilInterface(s.artifactResolver) {
			canonical, err = s.artifactResolver.Get(ctx, value)
			if err != nil {
				return nil, err
			}
		}
		ref := canonical.Ref()
		if _, exists := allowedRefs[ref]; !exists {
			return nil, &InvalidEvidenceError{Code: "artifact-outside"}
		}
		if _, exists := seen[ref]; !exists {
			seen[ref] = struct{}{}
			result = append(result, ref)
		}
	}
	return result, nil
}

func (s *Service) materialFromVersion(value EntryVersion) (entryMaterial, error) {
	return newEntryMaterial(value.Kind, value.Text, value.Sources, value.Artifacts)
}

func newEntryMaterial(kind, text string, sources []source.Ref, artifacts []artifact.Ref) (entryMaterial, error) {
	normalizedKind, err := NormalizeKind(kind)
	if err != nil {
		return entryMaterial{}, err
	}
	normalizedText, err := NormalizeText(text)
	if err != nil {
		return entryMaterial{}, err
	}
	canonicalSources, err := canonicalSourceRefs(sources)
	if err != nil {
		return entryMaterial{}, err
	}
	canonicalArtifacts, err := canonicalArtifactRefs(artifacts)
	if err != nil {
		return entryMaterial{}, err
	}
	contentBytes, err := EntryContentBytes(normalizedKind, normalizedText, canonicalSources, canonicalArtifacts)
	if err != nil {
		return entryMaterial{}, err
	}
	contentHash, err := EntryContentHash(normalizedKind, normalizedText, canonicalSources, canonicalArtifacts)
	if err != nil {
		return entryMaterial{}, err
	}
	return entryMaterial{
		kind: normalizedKind, text: normalizedText,
		sources: canonicalSources, artifacts: canonicalArtifacts,
		contentBytes: contentBytes, contentHash: contentHash,
	}, nil
}

func (s *Service) sourceRefs(values []source.Value) ([]source.Ref, error) {
	if len(values) == 0 {
		return []source.Ref{}, nil
	}
	if s.sourceResolver == nil || isNilInterface(s.sourceResolver) {
		return nil, &InvalidEvidenceError{Code: "source-adapter"}
	}
	refs := make([]source.Ref, 0, len(values))
	for _, value := range values {
		ref, err := s.sourceResolver.Ref(value)
		if err != nil {
			return nil, &InvalidEvidenceError{Code: "source-adapter"}
		}
		refs = append(refs, ref)
	}
	return canonicalSourceRefs(refs)
}

func (s *Service) newEntryVersion(
	memoryID, entryID string,
	previous *EntryVersion,
	material entryMaterial,
	createdInRevision int64,
) (EntryVersion, error) {
	versionID, err := s.newID("version")
	if err != nil {
		return EntryVersion{}, err
	}
	version := int64(1)
	var previousID *string
	if previous != nil {
		version = previous.Version + 1
		previousID = &previous.EntryVersionID
	}
	return EntryVersion{
		MemoryArtifactID: memoryID, EntryID: entryID, EntryVersionID: versionID,
		Version: version, PreviousVersionID: cloneString(previousID),
		Kind: material.kind, Text: material.text, EntryContentHash: material.contentHash,
		CreatedInRevision: createdInRevision, Sources: slices.Clone(material.sources),
		Artifacts: slices.Clone(material.artifacts),
	}, nil
}

func (s *Service) newID(kind string) (string, error) {
	value, err := s.idFactory(kind)
	if err != nil {
		return "", err
	}
	if _, err := ValidateIdentifier(value); err != nil {
		return "", err
	}
	return value, nil
}
