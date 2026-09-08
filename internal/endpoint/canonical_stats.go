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

package endpoint

import (
	"context"

	canonicalstats "github.com/ob-labs/powercontext-go/api/canonical/stats"
	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/stats"
)

// CanonicalStatisticsOperations is deliberately independent from the frozen
// single-Scope legacy StatisticsOperations interface.
type CanonicalStatisticsOperations interface {
	OverviewSelection(context.Context, scope.Selection, stats.Period) (stats.SelectionStatistics, error)
}

// CanonicalStatsHandler projects selection-aware statistics through the
// separately generated POST sidecar.
type CanonicalStatsHandler struct{ operations CanonicalStatisticsOperations }

var _ canonicalstats.Handler = (*CanonicalStatsHandler)(nil)

func NewCanonicalStatsHandler(operations CanonicalStatisticsOperations) *CanonicalStatsHandler {
	return &CanonicalStatsHandler{operations: operations}
}

func (h *CanonicalStatsHandler) GetStats(
	ctx context.Context,
	request *canonicalstats.GetStatsRequest,
) (canonicalstats.GetStatsRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return nil, &InvalidRequestError{}
	}
	selection, err := canonicalStatsSelection(request.Selection)
	if err != nil {
		return nil, err
	}
	value, err := h.operations.OverviewSelection(ctx, selection, stats.Period(request.Period.Or(canonicalstats.StatsPeriod30d)))
	if err != nil {
		return nil, err
	}
	response, err := canonicalScopedStatistics(value)
	if err != nil {
		return nil, err
	}
	return &canonicalstats.ScopedStatsHeaders{
		CacheControl:           canonicalstats.NewOptGetStatsOKCacheControl(canonicalstats.GetStatsOKCacheControlNoStore),
		XPowerContextRequestID: canonicalStatsRequestID(ctx),
		Response:               response,
	}, nil
}

func canonicalStatsSelection(value canonicalstats.ScopeSelection) (scope.Selection, error) {
	switch value.Type {
	case canonicalstats.AllScopeSelectionScopeSelection:
		return scope.AllSelection(), nil
	case canonicalstats.ExactScopeSelectionScopeSelection:
		selection, err := scope.NewExactSelection(value.ExactScopeSelection.ScopeIds)
		if err != nil {
			return scope.Selection{}, &scope.ValidationError{}
		}
		return selection, nil
	case canonicalstats.SubtreeScopeSelectionScopeSelection:
		selection, err := scope.NewSubtreeSelection(value.SubtreeScopeSelection.RootScopeID)
		if err != nil {
			return scope.Selection{}, &scope.ValidationError{}
		}
		return selection, nil
	default:
		return scope.Selection{}, &scope.ValidationError{}
	}
}

func canonicalStatsScopeSelection(value scope.Selection) (canonicalstats.ScopeSelection, error) {
	switch value.Mode() {
	case scope.SelectionAll:
		return canonicalstats.NewAllScopeSelectionScopeSelection(canonicalstats.AllScopeSelection{Mode: canonicalstats.AllScopeSelectionModeAll}), nil
	case scope.SelectionExact:
		return canonicalstats.NewExactScopeSelectionScopeSelection(canonicalstats.ExactScopeSelection{
			Mode: canonicalstats.ExactScopeSelectionModeExact, ScopeIds: value.ScopeIDs(),
		}), nil
	case scope.SelectionSubtree:
		return canonicalstats.NewSubtreeScopeSelectionScopeSelection(canonicalstats.SubtreeScopeSelection{
			Mode: canonicalstats.SubtreeScopeSelectionModeSubtree, RootScopeID: value.RootScopeID(),
		}), nil
	default:
		return canonicalstats.ScopeSelection{}, &InvalidRequestError{}
	}
}

func canonicalScopedStatistics(value stats.SelectionStatistics) (canonicalstats.ScopedStats, error) {
	selection, err := canonicalStatsScopeSelection(value.Selection())
	if err != nil {
		return canonicalstats.ScopedStats{}, err
	}
	inventory := value.Inventory()
	usage := value.Usage()
	recall := value.Recall()
	wireRecall, err := canonicalStatsRecall(recall)
	if err != nil {
		return canonicalstats.ScopedStats{}, err
	}
	byScope := value.ByScope()
	wireByScope := make([]canonicalstats.ScopeStats, len(byScope))
	for index, item := range byScope {
		itemRecall, recallErr := canonicalStatsRecall(item.Recall())
		if recallErr != nil {
			return canonicalstats.ScopedStats{}, recallErr
		}
		wireByScope[index] = canonicalstats.ScopeStats{
			ScopeID: item.ScopeID(), Inventory: canonicalStatsInventory(item.Inventory()),
			Usage: canonicalStatsUsage(item.Usage()), Recall: itemRecall,
		}
	}
	return canonicalstats.ScopedStats{
		Selection: selection, ScopeIds: value.ScopeIDs(), AsOf: value.AsOf(),
		Inventory: canonicalStatsInventory(inventory), Usage: canonicalStatsUsage(usage), Recall: wireRecall, ByScope: wireByScope,
	}, nil
}

func canonicalStatsInventory(value stats.Inventory) canonicalstats.InventoryStatistics {
	sources := value.Sources()
	artifacts := value.Artifacts()
	artifactFamilies := make([]canonicalstats.FamilyCount, 0, len(artifacts.ByFamily()))
	for _, item := range artifacts.ByFamily() {
		artifactFamilies = append(artifactFamilies, canonicalstats.FamilyCount{Family: item.Family(), Total: int(item.Total())})
	}
	candidates := value.Candidates()
	candidateFamilies := make([]canonicalstats.CandidateFamilyCount, 0, len(candidates.ByFamily()))
	for _, item := range candidates.ByFamily() {
		candidateFamilies = append(candidateFamilies, canonicalstats.CandidateFamilyCount{
			Family: canonicalstats.CandidateFamily(item.Family()), Total: int(item.Total()),
			Pending: int(item.Pending()), Approved: int(item.Approved()), Rejected: int(item.Rejected()),
		})
	}
	entries := value.MemoryEntries()
	memoryKinds := make([]canonicalstats.MemoryKindCount, 0, len(entries.ByKind()))
	for _, item := range entries.ByKind() {
		memoryKinds = append(memoryKinds, canonicalstats.MemoryKindCount{
			Kind: item.Kind(), Total: int(item.Total()), Active: int(item.Active()), Inactive: int(item.Inactive()),
		})
	}
	return canonicalstats.InventoryStatistics{
		Sources: canonicalstats.SourceInventoryStatistics{
			Total: int(sources.Total()), MemoryProcessed: int(sources.MemoryProcessed()), MemoryPending: int(sources.MemoryPending()),
		},
		Artifacts: canonicalstats.ArtifactInventoryStatistics{Total: int(artifacts.Total()), ByFamily: artifactFamilies},
		Candidates: canonicalstats.CandidateInventoryStatistics{
			Total: int(candidates.Total()), Pending: int(candidates.Pending()), Approved: int(candidates.Approved()),
			Rejected: int(candidates.Rejected()), ByFamily: candidateFamilies,
		},
		Memory: canonicalstats.MemoryInventoryStatistics{Entries: canonicalstats.MemoryEntryInventoryStatistics{
			Total: int(entries.Total()), Active: int(entries.Active()), Inactive: int(entries.Inactive()), ByKind: memoryKinds,
		}},
	}
}

func canonicalStatsUsage(value stats.Usage) canonicalstats.UsageStatistics {
	daily := make([]canonicalstats.ModelUsageDay, 0, len(value.Daily()))
	for _, day := range value.Daily() {
		usage := day.Usage()
		daily = append(daily, canonicalstats.ModelUsageDay{
			Date: day.Date(), Generation: canonicalStatsModelUsageValue(usage.Generation()),
			Embedding: canonicalStatsModelUsageValue(usage.Embedding()), ByPurpose: canonicalStatsPurposeBreakdowns(day.ByPurpose()),
		})
	}
	return canonicalstats.UsageStatistics{
		Period: canonicalStatsResolvedPeriod(value.Period()), Totals: canonicalStatsModelUsage(value.Totals()),
		ByPurpose: canonicalStatsPurposeBreakdowns(value.ByPurpose()), Daily: daily,
	}
}

func canonicalStatsModelUsage(value stats.ModelUsage) canonicalstats.ModelUsageStatistics {
	return canonicalstats.ModelUsageStatistics{
		Generation: canonicalStatsModelUsageValue(value.Generation()), Embedding: canonicalStatsModelUsageValue(value.Embedding()),
	}
}

func canonicalStatsModelUsageValue(value stats.ModelUsageValue) canonicalstats.ModelUsageValue {
	result := canonicalstats.ModelUsageValue{Requests: int(value.Requests())}
	if input := value.InputTokens(); input == nil {
		result.InputTokens.SetToNull()
	} else {
		result.InputTokens.SetTo(int(*input))
	}
	if output := value.OutputTokens(); output == nil {
		result.OutputTokens.SetToNull()
	} else {
		result.OutputTokens.SetTo(int(*output))
	}
	return result
}

func canonicalStatsPurposeBreakdowns(values []stats.PurposeBreakdown) []canonicalstats.ModelUsagePurposeBreakdown {
	result := make([]canonicalstats.ModelUsagePurposeBreakdown, len(values))
	for index, value := range values {
		result[index] = canonicalstats.ModelUsagePurposeBreakdown{
			Purpose: string(value.Purpose()), Generation: canonicalStatsModelUsageValue(value.Generation()),
			Embedding: canonicalStatsModelUsageValue(value.Embedding()),
		}
	}
	return result
}

func canonicalStatsRecall(value stats.Recall) (canonicalstats.RecallTokenStatistics, error) {
	profile := value.Estimator()
	daily := make([]canonicalstats.RecallTokenDay, 0, len(value.Daily()))
	for _, day := range value.Daily() {
		wire := canonicalStatsRecallValue(day.RecallTokenValue)
		daily = append(daily, canonicalstats.RecallTokenDay{
			Date: day.Date(), Preparations: wire.Preparations, ReadyPreparations: wire.ReadyPreparations,
			ComparablePreparations: wire.ComparablePreparations, BaselineTokens: wire.BaselineTokens,
			RecalledTokens: wire.RecalledTokens, TokenReduction: wire.TokenReduction,
		})
	}
	result := canonicalstats.RecallTokenStatistics{
		Period: canonicalStatsResolvedPeriod(value.Period()), Totals: canonicalStatsRecallValue(value.Totals()), Daily: daily,
	}
	if profile == nil {
		result.Estimator.SetToNull()
	} else {
		result.Estimator.SetTo(canonicalstats.TokenEstimatorProfile{EstimatorID: profile.EstimatorID(), Version: profile.Version()})
	}
	return result, nil
}

func canonicalStatsRecallValue(value stats.RecallTokenValue) canonicalstats.RecallTokenValue {
	return canonicalstats.RecallTokenValue{
		Preparations: int(value.Preparations()), ReadyPreparations: int(value.ReadyPreparations()),
		ComparablePreparations: int(value.ComparablePreparations()), BaselineTokens: int(value.BaselineTokens()),
		RecalledTokens: int(value.RecalledTokens()), TokenReduction: int(value.TokenReduction()),
	}
}

func canonicalStatsResolvedPeriod(value stats.ResolvedPeriod) canonicalstats.ResolvedUsagePeriod {
	return canonicalstats.ResolvedUsagePeriod{
		Preset: canonicalstats.StatsPeriod(value.Preset()), StartDate: value.StartDate(), EndDate: value.EndDate(),
		Timezone: canonicalstats.ResolvedUsagePeriodTimezoneUTC,
	}
}

func canonicalStatsRequestID(ctx context.Context) canonicalstats.OptString {
	if id, exists := requestID(ctx).Get(); exists {
		return canonicalstats.NewOptString(id)
	}
	return canonicalstats.OptString{}
}
