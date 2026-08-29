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
	"fmt"
	"reflect"
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/source"
)

type RememberMode string

const (
	RememberAppend  RememberMode = "append"
	RememberExtract RememberMode = "extract"
	RememberAuto    RememberMode = "auto"
)

type OrganizeMode string

const (
	OrganizeDefault   OrganizeMode = "default"
	OrganizeDedupe    OrganizeMode = "dedupe"
	OrganizeNormalize OrganizeMode = "normalize"
)

// Backend is the exact persistence/search surface consumed by Memory. Commit
// must atomically enforce the Artifact head CAS and replace all projections.
type Backend interface {
	Capabilities() Capabilities
	Get(context.Context, artifact.Ref) (Memory, error)
	Latest(context.Context, string) (Memory, error)
	Entries(context.Context, artifact.Ref) ([]EntryVersion, error)
	Projections(context.Context, artifact.Ref) ([]Projection, error)
	Commit(context.Context, Commit) (Memory, error)
	Changes(context.Context, artifact.Ref, *int64) ([]RevisionChanges, error)
	VectorComplete(context.Context, []artifact.Ref, EmbeddingProfile) (bool, error)
	Search(context.Context, SearchRequest) (SearchChannels, error)
	Expand(context.Context, []Hit) ([]EntryVersion, error)
}

type SourceResolver interface {
	Get(context.Context, source.Value) (source.Value, error)
	Ref(source.Value) (source.Ref, error)
}

type ArtifactResolver interface {
	Get(context.Context, artifact.Snapshot) (artifact.Snapshot, error)
}

type (
	IDFactory func(string) (string, error)
	Clock     func() time.Time
)

type ServiceOptions struct {
	CandidatePipeline    CandidatePipeline
	EmbeddingModel       inference.EmbeddingModel
	Reranker             Reranker
	RerankCandidateLimit int
	SourceResolver       SourceResolver
	ArtifactResolver     ArtifactResolver
	IDFactory            IDFactory
	Clock                Clock
}

type Service struct {
	backend              Backend
	candidatePipeline    CandidatePipeline
	embeddingModel       inference.EmbeddingModel
	reranker             Reranker
	rerankCandidateLimit int
	sourceResolver       SourceResolver
	artifactResolver     ArtifactResolver
	idFactory            IDFactory
	now                  Clock
}

func NewService(backend Backend, options ServiceOptions) (*Service, error) {
	if backend == nil || isNilInterface(backend) {
		return nil, fmt.Errorf("Memory backend must not be nil")
	}
	rerankLimit := options.RerankCandidateLimit
	if rerankLimit == 0 {
		rerankLimit = 30
	}
	if rerankLimit < 1 {
		return nil, &InvalidOperationError{Code: "search-limit"}
	}
	idFactory := options.IDFactory
	if idFactory == nil {
		idFactory = defaultID
	}
	now := options.Clock
	if now == nil {
		now = time.Now
	}
	return &Service{
		backend: backend, candidatePipeline: options.CandidatePipeline,
		embeddingModel: options.EmbeddingModel, reranker: options.Reranker,
		rerankCandidateLimit: rerankLimit, sourceResolver: options.SourceResolver,
		artifactResolver: options.ArtifactResolver, idFactory: idFactory, now: now,
	}, nil
}

func (s *Service) Get(ctx context.Context, value Memory) (Memory, error) {
	return s.canonicalMemory(ctx, value)
}

func (s *Service) Latest(ctx context.Context, value Memory) (Memory, error) {
	canonical, err := s.Get(ctx, value)
	if err != nil {
		return Memory{}, err
	}
	return s.backend.Latest(ctx, canonical.ID())
}

func (s *Service) Revisions(ctx context.Context, value Memory) ([]Memory, error) {
	canonical, err := s.Get(ctx, value)
	if err != nil {
		return nil, err
	}
	latest, err := s.backend.Latest(ctx, canonical.ID())
	if err != nil {
		return nil, err
	}
	result := make([]Memory, 0, latest.Revision())
	for revision := int64(1); revision <= latest.Revision(); revision++ {
		ref, _ := artifact.NewRef(Family, canonical.ID(), revision)
		item, err := s.backend.Get(ctx, ref)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Service) Head(ctx context.Context, artifactID string) (Memory, error) {
	return s.backend.Latest(ctx, artifactID)
}

func (s *Service) Revision(ctx context.Context, ref artifact.Ref) (Memory, error) {
	return s.backend.Get(ctx, ref)
}

func (s *Service) Remember(
	ctx context.Context,
	base *Memory,
	sources []source.Value,
	artifacts []artifact.Snapshot,
	entries []EntryInput,
	mode RememberMode,
) (*Memory, error) {
	plan, err := s.PlanRemember(ctx, base, sources, artifacts, entries, mode)
	if err != nil {
		return nil, err
	}
	return s.Apply(ctx, plan)
}

func (s *Service) PlanRemember(
	ctx context.Context,
	base *Memory,
	sources []source.Value,
	artifacts []artifact.Snapshot,
	entries []EntryInput,
	mode RememberMode,
) (WritePlan, error) {
	selectedMode, err := selectRememberMode(mode, len(entries) > 0, len(sources)+len(artifacts) > 0)
	if err != nil {
		return WritePlan{}, err
	}
	canonicalBase, err := s.canonicalBase(ctx, base)
	if err != nil {
		return WritePlan{}, err
	}
	evidence, err := s.canonicalOperationEvidence(ctx, sources, artifacts)
	if err != nil {
		return WritePlan{}, err
	}
	var currentEntries []EntryVersion
	if canonicalBase != nil {
		currentEntries, err = s.validatedEntries(ctx, *canonicalBase)
		if err != nil {
			return WritePlan{}, err
		}
	}
	candidates, err := s.candidates(ctx, selectedMode, entries, evidence, canonicalBase, currentEntries)
	if err != nil {
		return WritePlan{}, err
	}
	if len(candidates) == 0 {
		return NewWritePlan(canonicalBase, nil), nil
	}
	commit, err := s.prepareCommit(ctx, canonicalBase, candidates, evidence, currentEntries)
	if err != nil {
		return WritePlan{}, err
	}
	if commit == nil {
		return NewWritePlan(canonicalBase, nil), nil
	}
	result := commit.Memory()
	return NewWritePlan(&result, commit), nil
}

func (s *Service) Apply(ctx context.Context, plan WritePlan) (*Memory, error) {
	commit := plan.Commit()
	if commit == nil {
		return plan.Result(), nil
	}
	committed, err := s.backend.Commit(ctx, *commit)
	if err != nil {
		return nil, err
	}
	result := plan.Result()
	if result == nil || !reflect.DeepEqual(*result, committed) {
		return nil, &InvalidCitationError{Code: "memory-mismatch"}
	}
	return &committed, nil
}

func (s *Service) Forget(ctx context.Context, value Memory, entries []EntryVersion, reason *string) (Memory, error) {
	return s.setEntryState(ctx, value, entries, Inactive, reason)
}

func (s *Service) Reactivate(ctx context.Context, value Memory, entries []EntryVersion, reason *string) (Memory, error) {
	return s.setEntryState(ctx, value, entries, Active, reason)
}

func (s *Service) Organize(ctx context.Context, value Memory, mode OrganizeMode) (Memory, error) {
	if mode != OrganizeDefault && mode != OrganizeDedupe && mode != OrganizeNormalize {
		return Memory{}, &InvalidOperationError{Code: "organize-mode"}
	}
	base, err := s.canonicalBase(ctx, &value)
	if err != nil {
		return Memory{}, err
	}
	currentEntries, err := s.validatedEntries(ctx, *base)
	if err != nil {
		return Memory{}, err
	}
	manifest := manifestMap(base.Content().Manifest().Entries())
	current := entryMap(currentEntries)
	var changes []Change
	changedIDs := make(map[string]struct{})
	var newVersions []EntryVersion
	if mode == OrganizeDefault || mode == OrganizeDedupe {
		var dedupe []Change
		dedupe, changedIDs, err = s.deduplicateManifest(manifest, current)
		if err != nil {
			return Memory{}, err
		}
		changes = append(changes, dedupe...)
	}
	if mode == OrganizeDefault || mode == OrganizeNormalize {
		var normalized []Change
		normalized, newVersions, err = s.normalizeManifestEntries(*base, manifest, current, changedIDs)
		if err != nil {
			return Memory{}, err
		}
		changes = append(changes, normalized...)
	}
	if len(changes) == 0 {
		return *base, nil
	}
	return s.commitExistingTransition(ctx, *base, manifest, changes, current, newVersions)
}

func (s *Service) Changes(ctx context.Context, value Memory, sinceRevision *int64) ([]RevisionChanges, error) {
	target, err := s.canonicalMemory(ctx, value)
	if err != nil {
		return nil, err
	}
	if sinceRevision != nil {
		if *sinceRevision < 0 {
			return nil, &InvalidOperationError{Code: "since-negative"}
		}
		if *sinceRevision > target.Revision() {
			return nil, &InvalidOperationError{Code: "since-greater"}
		}
		if *sinceRevision == target.Revision() {
			return []RevisionChanges{}, nil
		}
		if *sinceRevision > 0 {
			ref, _ := artifact.NewRef(Family, target.ID(), *sinceRevision)
			if _, err := s.backend.Get(ctx, ref); err != nil {
				return nil, err
			}
		}
	}
	return s.backend.Changes(ctx, target.Ref(), sinceRevision)
}
