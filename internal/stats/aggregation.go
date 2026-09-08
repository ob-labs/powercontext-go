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

package stats

import (
	"fmt"
	"slices"
	"time"

	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

// ScopeStatistics is one immutable Scope contribution to a selection result.
type ScopeStatistics struct {
	scopeID   string
	inventory Inventory
	usage     Usage
	recall    Recall
}

func (v ScopeStatistics) ScopeID() string      { return v.scopeID }
func (v ScopeStatistics) Inventory() Inventory { return cloneInventory(v.inventory) }
func (v ScopeStatistics) Usage() Usage         { return cloneUsage(v.usage) }
func (v ScopeStatistics) Recall() Recall       { return cloneRecall(v.recall) }

// SelectionStatistics is an additive snapshot over a resolved Scope selection.
type SelectionStatistics struct {
	selection scope.Selection
	scopeIDs  []string
	asOf      time.Time
	inventory Inventory
	usage     Usage
	recall    Recall
	byScope   []ScopeStatistics
}

func (v SelectionStatistics) Selection() scope.Selection { return v.selection }
func (v SelectionStatistics) ScopeIDs() []string         { return slices.Clone(v.scopeIDs) }
func (v SelectionStatistics) AsOf() time.Time            { return v.asOf }
func (v SelectionStatistics) Inventory() Inventory       { return cloneInventory(v.inventory) }
func (v SelectionStatistics) Usage() Usage               { return cloneUsage(v.usage) }
func (v SelectionStatistics) Recall() Recall             { return cloneRecall(v.recall) }

func (v SelectionStatistics) ByScope() []ScopeStatistics {
	result := make([]ScopeStatistics, len(v.byScope))
	for i, item := range v.byScope {
		result[i] = ScopeStatistics{
			scopeID: item.scopeID, inventory: cloneInventory(item.inventory), usage: cloneUsage(item.usage), recall: cloneRecall(item.recall),
		}
	}
	return result
}

// Aggregate combines already-resolved Scope snapshots that use one reporting
// period and one recall-estimator profile. It deliberately preserves the
// original Scope order in both scope_ids and by_scope.
func Aggregate(
	selection scope.Selection,
	scopeIDs []string,
	snapshots []Statistics,
	asOf time.Time,
) (SelectionStatistics, error) {
	if selection.Mode() != scope.SelectionAll && selection.Mode() != scope.SelectionExact && selection.Mode() != scope.SelectionSubtree {
		return SelectionStatistics{}, fmt.Errorf("statistics selection is invalid")
	}
	if len(scopeIDs) == 0 || len(scopeIDs) != len(snapshots) || asOf.IsZero() {
		return SelectionStatistics{}, fmt.Errorf("statistics selection snapshots are invalid")
	}
	if !slices.Equal(selectionScopeIDs(selection, scopeIDs), scopeIDs) {
		return SelectionStatistics{}, fmt.Errorf("statistics selection scope IDs do not match snapshots")
	}
	first := snapshots[0]
	if first.scopeID != scopeIDs[0] {
		return SelectionStatistics{}, fmt.Errorf("statistics snapshot scope does not match selection")
	}
	for index, snapshot := range snapshots {
		if snapshot.scopeID != scopeIDs[index] || snapshot.usage.period != first.usage.period || snapshot.recall.period != first.recall.period ||
			!equalEstimator(snapshot.recall.estimator, first.recall.estimator) {
			return SelectionStatistics{}, fmt.Errorf("statistics snapshots do not share one selection period")
		}
	}
	byScope := make([]ScopeStatistics, len(snapshots))
	for index, snapshot := range snapshots {
		byScope[index] = ScopeStatistics{
			scopeID: snapshot.scopeID, inventory: cloneInventory(snapshot.inventory), usage: cloneUsage(snapshot.usage), recall: cloneRecall(snapshot.recall),
		}
	}
	return SelectionStatistics{
		selection: selection, scopeIDs: slices.Clone(scopeIDs), asOf: asOf.UTC(),
		inventory: aggregateInventory(snapshots), usage: aggregateUsage(snapshots), recall: aggregateRecall(snapshots), byScope: byScope,
	}, nil
}

func selectionScopeIDs(selection scope.Selection, resolved []string) []string {
	if selection.Mode() == scope.SelectionExact {
		return selection.ScopeIDs()
	}
	return resolved
}

func equalEstimator(left, right *inference.TokenEstimatorProfile) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func aggregateInventory(snapshots []Statistics) Inventory {
	var result Inventory
	families := make(map[string]int64)
	candidates := make(map[string][3]int64)
	kinds := make(map[string][2]int64)
	for _, snapshot := range snapshots {
		inventory := snapshot.inventory
		result.sources.total += inventory.sources.total
		result.sources.memoryProcessed += inventory.sources.memoryProcessed
		result.sources.memoryPending += inventory.sources.memoryPending
		for _, family := range inventory.artifacts.byFamily {
			families[family.family] += family.total
		}
		for _, candidate := range inventory.candidates.byFamily {
			value := candidates[candidate.family]
			value[0] += candidate.pending
			value[1] += candidate.approved
			value[2] += candidate.rejected
			candidates[candidate.family] = value
		}
		for _, kind := range inventory.memory.byKind {
			value := kinds[kind.kind]
			value[0] += kind.active
			value[1] += kind.inactive
			kinds[kind.kind] = value
		}
	}
	result.artifacts.byFamily = make([]FamilyCount, 0, len(families))
	for family, total := range families {
		result.artifacts.byFamily = append(result.artifacts.byFamily, FamilyCount{family: family, total: total})
		result.artifacts.total += total
	}
	slices.SortFunc(result.artifacts.byFamily, func(left, right FamilyCount) int { return stringCompare(left.family, right.family) })
	result.candidates.byFamily = make([]CandidateFamilyCount, 0, len(candidates))
	for family, values := range candidates {
		value := newCandidateFamilyCount(family, values[0], values[1], values[2])
		result.candidates.byFamily = append(result.candidates.byFamily, value)
		result.candidates.total += value.total
		result.candidates.pending += value.pending
		result.candidates.approved += value.approved
		result.candidates.rejected += value.rejected
	}
	slices.SortFunc(result.candidates.byFamily, func(left, right CandidateFamilyCount) int { return stringCompare(left.family, right.family) })
	result.memory.byKind = make([]MemoryKindCount, 0, len(kinds))
	for kind, values := range kinds {
		value := newMemoryKindCount(kind, values[0], values[1])
		result.memory.byKind = append(result.memory.byKind, value)
		result.memory.total += value.total
		result.memory.active += value.active
		result.memory.inactive += value.inactive
	}
	slices.SortFunc(result.memory.byKind, func(left, right MemoryKindCount) int { return stringCompare(left.kind, right.kind) })
	return result
}

func aggregateUsage(snapshots []Statistics) Usage {
	first := snapshots[0].usage
	result := Usage{period: first.period, totals: aggregateModelUsage(snapshots, func(value Statistics) ModelUsage { return value.usage.totals })}
	result.byPurpose = aggregatePurposes(snapshots, func(value Statistics) []PurposeBreakdown { return value.usage.byPurpose })
	result.daily = make([]ModelUsageDay, len(first.daily))
	for index, day := range first.daily {
		result.daily[index] = ModelUsageDay{
			date:      day.date,
			usage:     aggregateModelUsage(snapshots, func(value Statistics) ModelUsage { return value.usage.daily[index].usage }),
			byPurpose: aggregatePurposes(snapshots, func(value Statistics) []PurposeBreakdown { return value.usage.daily[index].byPurpose }),
		}
	}
	return result
}

func aggregateModelUsage(snapshots []Statistics, selectUsage func(Statistics) ModelUsage) ModelUsage {
	values := make([]ModelUsageValue, len(snapshots))
	for index, snapshot := range snapshots {
		values[index] = selectUsage(snapshot).generation
	}
	generation := aggregateModelUsageValues(values)
	for index, snapshot := range snapshots {
		values[index] = selectUsage(snapshot).embedding
	}
	return ModelUsage{generation: generation, embedding: aggregateModelUsageValues(values)}
}

func aggregatePurposes(snapshots []Statistics, selectPurposes func(Statistics) []PurposeBreakdown) []PurposeBreakdown {
	present := make(map[ModelPurpose]struct{})
	for _, snapshot := range snapshots {
		for _, value := range selectPurposes(snapshot) {
			present[value.purpose] = struct{}{}
		}
	}
	purposes := make([]ModelPurpose, 0, len(present))
	for purpose := range present {
		purposes = append(purposes, purpose)
	}
	slices.SortFunc(purposes, func(left, right ModelPurpose) int { return stringCompare(string(left), string(right)) })
	result := make([]PurposeBreakdown, len(purposes))
	for index, purpose := range purposes {
		generation := make([]ModelUsageValue, len(snapshots))
		embedding := make([]ModelUsageValue, len(snapshots))
		for snapshotIndex, snapshot := range snapshots {
			value := purposeFor(selectPurposes(snapshot), purpose)
			generation[snapshotIndex], embedding[snapshotIndex] = value.generation, value.embedding
		}
		result[index] = PurposeBreakdown{
			purpose: purpose, generation: aggregateModelUsageValues(generation), embedding: aggregateModelUsageValues(embedding),
		}
	}
	return result
}

func purposeFor(values []PurposeBreakdown, purpose ModelPurpose) PurposeBreakdown {
	for _, value := range values {
		if value.purpose == purpose {
			return value
		}
	}
	zero := ModelUsageValue{inputTokens: new(int64), outputTokens: new(int64)}
	return PurposeBreakdown{purpose: purpose, generation: zero, embedding: zero}
}

func aggregateModelUsageValues(values []ModelUsageValue) ModelUsageValue {
	result := ModelUsageValue{}
	inputComplete, outputComplete := true, true
	for _, value := range values {
		result.requests += value.requests
		if value.inputTokens == nil {
			inputComplete = false
		} else {
			input := int64Value(result.inputTokens) + *value.inputTokens
			result.inputTokens = &input
		}
		if value.outputTokens == nil {
			outputComplete = false
		} else {
			output := int64Value(result.outputTokens) + *value.outputTokens
			result.outputTokens = &output
		}
	}
	if !inputComplete {
		result.inputTokens = nil
	}
	if !outputComplete {
		result.outputTokens = nil
	}
	return result
}

func aggregateRecall(snapshots []Statistics) Recall {
	first := snapshots[0].recall
	result := Recall{period: first.period, estimator: first.Estimator(), totals: aggregateRecallValues(snapshots, func(value Statistics) RecallTokenValue { return value.recall.totals })}
	result.daily = make([]RecallTokenDay, len(first.daily))
	for index, day := range first.daily {
		result.daily[index] = RecallTokenDay{
			date: day.date,
			RecallTokenValue: aggregateRecallValues(snapshots, func(value Statistics) RecallTokenValue {
				return value.recall.daily[index].RecallTokenValue
			}),
		}
	}
	return result
}

func aggregateRecallValues(snapshots []Statistics, selectValue func(Statistics) RecallTokenValue) RecallTokenValue {
	var result RecallTokenValue
	for _, snapshot := range snapshots {
		value := selectValue(snapshot)
		result.preparations += value.preparations
		result.readyPreparations += value.readyPreparations
		result.comparablePreparations += value.comparablePreparations
		result.baselineTokens += value.baselineTokens
		result.recalledTokens += value.recalledTokens
	}
	result.tokenReduction = result.baselineTokens - result.recalledTokens
	return result
}
