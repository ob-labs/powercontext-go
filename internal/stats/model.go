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
)

type Period string

const (
	Today      Period = "today"
	SevenDays  Period = "7d"
	ThirtyDays Period = "30d"
)

type ModelOperation string

const (
	Generation ModelOperation = "generation"
	Embedding  ModelOperation = "embedding"
)

type ModelPurpose string

const (
	MemoryExtraction     ModelPurpose = "memory_extraction"
	MemoryIndexing       ModelPurpose = "memory_indexing"
	MemoryRecall         ModelPurpose = "memory_recall"
	ExperienceGeneration ModelPurpose = "experience_generation"
	SkillGeneration      ModelPurpose = "skill_generation"
	HandoffGeneration    ModelPurpose = "handoff_generation"
)

var modelPurposes = []ModelPurpose{
	ExperienceGeneration,
	HandoffGeneration,
	MemoryExtraction,
	MemoryIndexing,
	MemoryRecall,
	SkillGeneration,
}

func (p Period) Validate() error {
	switch p {
	case Today, SevenDays, ThirtyDays:
		return nil
	default:
		return fmt.Errorf("unsupported statistics period %q", p)
	}
}

func (o ModelOperation) Validate() error {
	if o != Generation && o != Embedding {
		return fmt.Errorf("unsupported model usage operation %q", o)
	}
	return nil
}

func (p ModelPurpose) Validate() error {
	for _, known := range modelPurposes {
		if p == known {
			return nil
		}
	}
	return fmt.Errorf("unsupported model usage purpose %q", p)
}

type FamilyCount struct {
	family string
	total  int64
}

func NewFamilyCount(family string, total int64) (FamilyCount, error) {
	if family == "" || total < 0 {
		return FamilyCount{}, fmt.Errorf("invalid Artifact family count")
	}
	return FamilyCount{family: family, total: total}, nil
}
func (v FamilyCount) Family() string { return v.family }
func (v FamilyCount) Total() int64   { return v.total }

type CandidateFamilyCount struct {
	family                             string
	total, pending, approved, rejected int64
}

func newCandidateFamilyCount(family string, pending, approved, rejected int64) CandidateFamilyCount {
	return CandidateFamilyCount{
		family: family, total: pending + approved + rejected,
		pending: pending, approved: approved, rejected: rejected,
	}
}
func (v CandidateFamilyCount) Family() string  { return v.family }
func (v CandidateFamilyCount) Total() int64    { return v.total }
func (v CandidateFamilyCount) Pending() int64  { return v.pending }
func (v CandidateFamilyCount) Approved() int64 { return v.approved }
func (v CandidateFamilyCount) Rejected() int64 { return v.rejected }

type MemoryKindCount struct {
	kind                    string
	total, active, inactive int64
}

func newMemoryKindCount(kind string, active, inactive int64) MemoryKindCount {
	return MemoryKindCount{kind: kind, total: active + inactive, active: active, inactive: inactive}
}
func (v MemoryKindCount) Kind() string    { return v.kind }
func (v MemoryKindCount) Total() int64    { return v.total }
func (v MemoryKindCount) Active() int64   { return v.active }
func (v MemoryKindCount) Inactive() int64 { return v.inactive }

type SourceInventory struct {
	total, memoryProcessed, memoryPending int64
}

func (v SourceInventory) Total() int64           { return v.total }
func (v SourceInventory) MemoryProcessed() int64 { return v.memoryProcessed }
func (v SourceInventory) MemoryPending() int64   { return v.memoryPending }

type ArtifactInventory struct {
	total    int64
	byFamily []FamilyCount
}

func (v ArtifactInventory) Total() int64            { return v.total }
func (v ArtifactInventory) ByFamily() []FamilyCount { return slices.Clone(v.byFamily) }

type CandidateInventory struct {
	total, pending, approved, rejected int64
	byFamily                           []CandidateFamilyCount
}

func (v CandidateInventory) Total() int64                     { return v.total }
func (v CandidateInventory) Pending() int64                   { return v.pending }
func (v CandidateInventory) Approved() int64                  { return v.approved }
func (v CandidateInventory) Rejected() int64                  { return v.rejected }
func (v CandidateInventory) ByFamily() []CandidateFamilyCount { return slices.Clone(v.byFamily) }

type MemoryEntryInventory struct {
	total, active, inactive int64
	byKind                  []MemoryKindCount
}

func (v MemoryEntryInventory) Total() int64              { return v.total }
func (v MemoryEntryInventory) Active() int64             { return v.active }
func (v MemoryEntryInventory) Inactive() int64           { return v.inactive }
func (v MemoryEntryInventory) ByKind() []MemoryKindCount { return slices.Clone(v.byKind) }

type Inventory struct {
	sources    SourceInventory
	artifacts  ArtifactInventory
	candidates CandidateInventory
	memory     MemoryEntryInventory
}

func (v Inventory) Sources() SourceInventory     { return v.sources }
func (v Inventory) Artifacts() ArtifactInventory { return cloneArtifactInventory(v.artifacts) }

func (v Inventory) Candidates() CandidateInventory      { return cloneCandidateInventory(v.candidates) }
func (v Inventory) MemoryEntries() MemoryEntryInventory { return cloneMemoryInventory(v.memory) }

type ModelUsageValue struct {
	requests     int64
	inputTokens  *int64
	outputTokens *int64
}

func NewModelUsageValue(requests int64, inputTokens, outputTokens *int64) (ModelUsageValue, error) {
	if requests < 0 || (inputTokens != nil && *inputTokens < 0) || (outputTokens != nil && *outputTokens < 0) {
		return ModelUsageValue{}, fmt.Errorf("model usage values must be non-negative")
	}
	return ModelUsageValue{requests: requests, inputTokens: cloneInt64(inputTokens), outputTokens: cloneInt64(outputTokens)}, nil
}
func (v ModelUsageValue) Requests() int64      { return v.requests }
func (v ModelUsageValue) InputTokens() *int64  { return cloneInt64(v.inputTokens) }
func (v ModelUsageValue) OutputTokens() *int64 { return cloneInt64(v.outputTokens) }

type ModelUsage struct {
	generation ModelUsageValue
	embedding  ModelUsageValue
}

func (v ModelUsage) Generation() ModelUsageValue { return cloneUsageValue(v.generation) }
func (v ModelUsage) Embedding() ModelUsageValue  { return cloneUsageValue(v.embedding) }

type PurposeBreakdown struct {
	purpose    ModelPurpose
	generation ModelUsageValue
	embedding  ModelUsageValue
}

func (v PurposeBreakdown) Purpose() ModelPurpose       { return v.purpose }
func (v PurposeBreakdown) Generation() ModelUsageValue { return cloneUsageValue(v.generation) }
func (v PurposeBreakdown) Embedding() ModelUsageValue  { return cloneUsageValue(v.embedding) }

type ResolvedPeriod struct {
	preset             Period
	startDate, endDate time.Time
}

func ResolvePeriod(preset Period, end time.Time) (ResolvedPeriod, error) {
	if err := preset.Validate(); err != nil {
		return ResolvedPeriod{}, err
	}
	end = utcDate(end)
	days := 1
	if preset == SevenDays {
		days = 7
	} else if preset == ThirtyDays {
		days = 30
	}
	return ResolvedPeriod{preset: preset, startDate: end.AddDate(0, 0, -(days - 1)), endDate: end}, nil
}
func (v ResolvedPeriod) Preset() Period       { return v.preset }
func (v ResolvedPeriod) StartDate() time.Time { return v.startDate }
func (v ResolvedPeriod) EndDate() time.Time   { return v.endDate }
func (ResolvedPeriod) Timezone() string       { return "UTC" }

type ModelUsageDay struct {
	date      time.Time
	usage     ModelUsage
	byPurpose []PurposeBreakdown
}

func (v ModelUsageDay) Date() time.Time               { return v.date }
func (v ModelUsageDay) Usage() ModelUsage             { return cloneModelUsage(v.usage) }
func (v ModelUsageDay) ByPurpose() []PurposeBreakdown { return clonePurposeBreakdowns(v.byPurpose) }

type Usage struct {
	period    ResolvedPeriod
	totals    ModelUsage
	byPurpose []PurposeBreakdown
	daily     []ModelUsageDay
}

func (v Usage) Period() ResolvedPeriod        { return v.period }
func (v Usage) Totals() ModelUsage            { return cloneModelUsage(v.totals) }
func (v Usage) ByPurpose() []PurposeBreakdown { return clonePurposeBreakdowns(v.byPurpose) }
func (v Usage) Daily() []ModelUsageDay        { return cloneUsageDays(v.daily) }

type RecallTokenMeasurement struct {
	estimator                      inference.TokenEstimatorProfile
	ready, comparable              bool
	baselineTokens, recalledTokens int64
}

func NewRecallTokenMeasurement(
	estimator inference.TokenEstimatorProfile,
	ready, comparable bool,
	baselineTokens, recalledTokens int64,
) (RecallTokenMeasurement, error) {
	if estimator.EstimatorID() == "" || estimator.Version() == "" {
		return RecallTokenMeasurement{}, fmt.Errorf("recall measurement estimator is invalid")
	}
	if baselineTokens < 0 || recalledTokens < 0 {
		return RecallTokenMeasurement{}, fmt.Errorf("recall token estimates must be non-negative")
	}
	if comparable && !ready {
		return RecallTokenMeasurement{}, fmt.Errorf("only ready context can be comparable")
	}
	if !comparable && (baselineTokens != 0 || recalledTokens != 0) {
		return RecallTokenMeasurement{}, fmt.Errorf("non-comparable context cannot contribute token estimates")
	}
	return RecallTokenMeasurement{
		estimator: estimator, ready: ready, comparable: comparable,
		baselineTokens: baselineTokens, recalledTokens: recalledTokens,
	}, nil
}
func (v RecallTokenMeasurement) Estimator() inference.TokenEstimatorProfile { return v.estimator }
func (v RecallTokenMeasurement) Ready() bool                                { return v.ready }
func (v RecallTokenMeasurement) Comparable() bool                           { return v.comparable }
func (v RecallTokenMeasurement) BaselineTokens() int64                      { return v.baselineTokens }

func (v RecallTokenMeasurement) RecalledTokens() int64 { return v.recalledTokens }

type RecallTokenValue struct {
	preparations, readyPreparations, comparablePreparations int64
	baselineTokens, recalledTokens, tokenReduction          int64
}

func (v RecallTokenValue) Preparations() int64           { return v.preparations }
func (v RecallTokenValue) ReadyPreparations() int64      { return v.readyPreparations }
func (v RecallTokenValue) ComparablePreparations() int64 { return v.comparablePreparations }
func (v RecallTokenValue) BaselineTokens() int64         { return v.baselineTokens }
func (v RecallTokenValue) RecalledTokens() int64         { return v.recalledTokens }
func (v RecallTokenValue) TokenReduction() int64         { return v.tokenReduction }

type RecallTokenDay struct {
	date time.Time
	RecallTokenValue
}

func (v RecallTokenDay) Date() time.Time { return v.date }

type Recall struct {
	period    ResolvedPeriod
	estimator *inference.TokenEstimatorProfile
	totals    RecallTokenValue
	daily     []RecallTokenDay
}

func (v Recall) Period() ResolvedPeriod { return v.period }
func (v Recall) Estimator() *inference.TokenEstimatorProfile {
	if v.estimator == nil {
		return nil
	}
	value := *v.estimator
	return &value
}
func (v Recall) Totals() RecallTokenValue { return v.totals }
func (v Recall) Daily() []RecallTokenDay  { return slices.Clone(v.daily) }

type Statistics struct {
	scopeID   string
	asOf      time.Time
	inventory Inventory
	usage     Usage
	recall    Recall
}

func (v Statistics) ScopeID() string      { return v.scopeID }
func (v Statistics) AsOf() time.Time      { return v.asOf }
func (v Statistics) Inventory() Inventory { return cloneInventory(v.inventory) }
func (v Statistics) Usage() Usage         { return cloneUsage(v.usage) }
func (v Statistics) Recall() Recall       { return cloneRecall(v.recall) }

func utcDate(value time.Time) time.Time {
	value = value.UTC()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneUsageValue(v ModelUsageValue) ModelUsageValue {
	v.inputTokens, v.outputTokens = cloneInt64(v.inputTokens), cloneInt64(v.outputTokens)
	return v
}

func cloneModelUsage(v ModelUsage) ModelUsage {
	v.generation, v.embedding = cloneUsageValue(v.generation), cloneUsageValue(v.embedding)
	return v
}

func clonePurposeBreakdowns(values []PurposeBreakdown) []PurposeBreakdown {
	result := make([]PurposeBreakdown, len(values))
	for i, value := range values {
		value.generation, value.embedding = cloneUsageValue(value.generation), cloneUsageValue(value.embedding)
		result[i] = value
	}
	return result
}

func cloneUsageDays(values []ModelUsageDay) []ModelUsageDay {
	result := make([]ModelUsageDay, len(values))
	for i, value := range values {
		value.usage = cloneModelUsage(value.usage)
		value.byPurpose = clonePurposeBreakdowns(value.byPurpose)
		result[i] = value
	}
	return result
}

func cloneArtifactInventory(v ArtifactInventory) ArtifactInventory {
	v.byFamily = slices.Clone(v.byFamily)
	return v
}

func cloneCandidateInventory(v CandidateInventory) CandidateInventory {
	v.byFamily = slices.Clone(v.byFamily)
	return v
}

func cloneMemoryInventory(v MemoryEntryInventory) MemoryEntryInventory {
	v.byKind = slices.Clone(v.byKind)
	return v
}

func cloneInventory(v Inventory) Inventory {
	v.artifacts = cloneArtifactInventory(v.artifacts)
	v.candidates = cloneCandidateInventory(v.candidates)
	v.memory = cloneMemoryInventory(v.memory)
	return v
}

func cloneUsage(v Usage) Usage {
	v.totals = cloneModelUsage(v.totals)
	v.byPurpose = clonePurposeBreakdowns(v.byPurpose)
	v.daily = cloneUsageDays(v.daily)
	return v
}

func cloneRecall(v Recall) Recall {
	if v.estimator != nil {
		profile := *v.estimator
		v.estimator = &profile
	}
	v.daily = slices.Clone(v.daily)
	return v
}
