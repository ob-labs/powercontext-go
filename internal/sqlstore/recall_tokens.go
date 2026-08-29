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

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/contextpack"
	"github.com/ob-labs/powercontext-go/internal/stats"
	"github.com/ob-labs/powercontext-go/source"
)

// RecallTokenProjectionError identifies a persisted Source type that has no
// stable primary-text projection. The Source identity and content are omitted
// deliberately so the error is safe to classify at an operational boundary.
type RecallTokenProjectionError struct{ SourceType string }

func (e *RecallTokenProjectionError) Error() string {
	return fmt.Sprintf("sqlstore: no recall token projection is registered for Source type %q", e.SourceType)
}

// RelationalRecallTokenEstimator resolves the exact Source lineage of the
// entries that survived Context Pack budgeting. All Artifact, Memory entry,
// and Source reads share one relational snapshot; token estimation happens
// after that snapshot has been released.
type RelationalRecallTokenEstimator struct {
	database  *Database
	scopeID   string
	sources   *SourceRepository
	artifacts *ArtifactRepository
	estimator *inference.TokenEstimator
}

func NewRelationalRecallTokenEstimator(
	database *Database,
	scopeID string,
	sources *SourceRepository,
	artifacts *ArtifactRepository,
	estimator *inference.TokenEstimator,
) (*RelationalRecallTokenEstimator, error) {
	if database == nil || sources == nil || artifacts == nil || estimator == nil {
		return nil, errors.New("sqlstore: recall token estimator dependencies must not be nil")
	}
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	profile := estimator.Profile()
	if profile.EstimatorID() == "" || profile.Version() == "" {
		return nil, errors.New("sqlstore: recall token estimator profile is invalid")
	}
	return &RelationalRecallTokenEstimator{
		database: database, scopeID: scopeID, sources: sources,
		artifacts: artifacts, estimator: estimator,
	}, nil
}

func (e *RelationalRecallTokenEstimator) Estimate(
	ctx context.Context,
	build contextpack.Build,
) (stats.RecallTokenMeasurement, error) {
	build = build.Clone()
	ready := build.Context.Status() == contextpack.Ready
	comparable := ready && len(build.Origins) > 0
	texts := make([]string, 0)

	err := e.database.Transaction(ctx, func(tx DBTX) error {
		allSources := make(map[source.Ref]struct{})
		artifactSources := make(map[artifact.Ref]map[source.Ref]struct{})
		resolving := make(map[artifact.Ref]struct{})

		var resolveArtifact func(artifact.Ref) (map[source.Ref]struct{}, error)
		resolveArtifact = func(ref artifact.Ref) (map[source.Ref]struct{}, error) {
			if cached, ok := artifactSources[ref]; ok {
				return cloneSourceSet(cached), nil
			}
			if _, cycle := resolving[ref]; cycle {
				return map[source.Ref]struct{}{}, nil
			}
			resolving[ref] = struct{}{}
			defer delete(resolving, ref)

			value, err := e.artifacts.Get(ctx, tx, e.scopeID, ref)
			if err != nil {
				return nil, err
			}
			resolved := sourceSet(value.Lineage().Sources())
			for _, parent := range value.Lineage().Artifacts() {
				parentSources, err := resolveArtifact(parent)
				if err != nil {
					return nil, err
				}
				mergeSourceSets(resolved, parentSources)
			}
			artifactSources[ref] = cloneSourceSet(resolved)
			return resolved, nil
		}

		for _, origin := range build.Origins {
			if (origin.Memory == nil) == (origin.Artifact == nil) {
				return &contextpack.InvariantError{Code: "origin-kind"}
			}
			originSources := make(map[source.Ref]struct{})
			if origin.Memory != nil {
				entry, err := e.memoryEntry(ctx, tx, *origin.Memory)
				if err != nil {
					return err
				}
				mergeSourceSets(originSources, sourceSet(entry.Sources))
				for _, ref := range entry.Artifacts {
					resolved, err := resolveArtifact(ref)
					if err != nil {
						return err
					}
					mergeSourceSets(originSources, resolved)
				}
			} else {
				resolved, err := resolveArtifact(*origin.Artifact)
				if err != nil {
					return err
				}
				mergeSourceSets(originSources, resolved)
			}
			if len(originSources) == 0 {
				comparable = false
			}
			mergeSourceSets(allSources, originSources)
		}

		if !comparable {
			return nil
		}
		refs := make([]source.Ref, 0, len(allSources))
		for ref := range allSources {
			refs = append(refs, ref)
		}
		slices.SortFunc(refs, func(left, right source.Ref) int {
			if left.Type() < right.Type() {
				return -1
			}
			if left.Type() > right.Type() {
				return 1
			}
			if left.ID() < right.ID() {
				return -1
			}
			if left.ID() > right.ID() {
				return 1
			}
			return 0
		})
		texts = make([]string, 0, len(refs))
		for _, ref := range refs {
			stored, err := e.sources.Get(ctx, tx, e.scopeID, ref)
			if err != nil {
				return err
			}
			text, err := recallSourceText(ref, stored.Value)
			if err != nil {
				return err
			}
			texts = append(texts, text)
		}
		return nil
	})
	if err != nil {
		return stats.RecallTokenMeasurement{}, err
	}
	if !comparable {
		return stats.NewRecallTokenMeasurement(e.estimator.Profile(), ready, false, 0, 0)
	}

	baseline := int64(0)
	for _, text := range texts {
		count, estimateErr := e.estimator.Estimate(text)
		if estimateErr != nil {
			return stats.RecallTokenMeasurement{}, estimateErr
		}
		if uint64(count) > uint64(math.MaxInt64-baseline) {
			return stats.RecallTokenMeasurement{}, errors.New("sqlstore: recall baseline token count overflow")
		}
		baseline += int64(count)
	}
	content := ""
	if value := build.Context.Content(); value != nil {
		content = *value
	}
	recalled, err := e.estimator.Estimate(content)
	if err != nil {
		return stats.RecallTokenMeasurement{}, err
	}
	return stats.NewRecallTokenMeasurement(
		e.estimator.Profile(), ready, true, baseline, int64(recalled),
	)
}

func (e *RelationalRecallTokenEstimator) memoryEntry(
	ctx context.Context,
	tx DBTX,
	citation memory.Citation,
) (memory.EntryVersion, error) {
	value, err := e.artifacts.Get(ctx, tx, e.scopeID, citation.MemoryRef)
	if err != nil {
		return memory.EntryVersion{}, err
	}
	stored, ok := value.(artifact.Artifact[memory.Content])
	if !ok {
		return memory.EntryVersion{}, &memory.InvalidCitationError{Code: "memory-mismatch"}
	}
	foundInManifest := false
	for _, item := range stored.Content().Manifest().Entries() {
		if item.EntryID() == citation.EntryID && item.EntryVersionID() == citation.EntryVersionID {
			foundInManifest = true
			break
		}
	}
	if !foundInManifest {
		return memory.EntryVersion{}, &memory.InvalidCitationError{Code: "expand-anchor"}
	}
	entry, found, err := findMemoryEntry(
		ctx, tx, e.scopeID, citation.MemoryRef.ID(), citation.EntryID, citation.EntryVersionID,
	)
	if err != nil {
		return memory.EntryVersion{}, err
	}
	if !found {
		return memory.EntryVersion{}, &memory.InvalidCitationError{Code: "expand-anchor"}
	}
	return entry, nil
}

func recallSourceText(ref source.Ref, value source.Value) (string, error) {
	switch typed := value.(type) {
	case source.ContentSource:
		return typed.Content(), nil
	case skill.SnapshotSource:
		return typed.Snapshot().Manifest(), nil
	default:
		return "", &RecallTokenProjectionError{SourceType: ref.Type()}
	}
}

func sourceSet(values []source.Ref) map[source.Ref]struct{} {
	result := make(map[source.Ref]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func cloneSourceSet(values map[source.Ref]struct{}) map[source.Ref]struct{} {
	result := make(map[source.Ref]struct{}, len(values))
	mergeSourceSets(result, values)
	return result
}

func mergeSourceSets(target, values map[source.Ref]struct{}) {
	for value := range values {
		target[value] = struct{}{}
	}
}
