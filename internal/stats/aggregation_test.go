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
	"slices"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

func TestAggregateSelectionPreservesScopeSnapshotsAndAdditiveTotals(t *testing.T) {
	t.Parallel()
	asOf := time.Date(2026, time.September, 8, 13, 14, 15, 0, time.UTC)
	profile, err := inference.NewTokenEstimatorProfile("character", "v1")
	if err != nil {
		t.Fatal(err)
	}
	first := aggregateFixture(t, "scope-a", asOf, profile, 2, true)
	second := aggregateFixture(t, "scope-b", asOf, profile, 3, false)
	selection, err := scope.NewExactSelection([]string{"scope-b", "scope-a"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := Aggregate(selection, []string{"scope-a", "scope-b"}, []Statistics{first, second}, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.ScopeIDs(), []string{"scope-a", "scope-b"}) {
		t.Fatalf("ScopeIDs() = %v", result.ScopeIDs())
	}
	if !slices.Equal(result.Selection().ScopeIDs(), []string{"scope-a", "scope-b"}) {
		t.Fatalf("Selection() = %#v", result.Selection())
	}
	if result.AsOf() != asOf {
		t.Fatalf("AsOf() = %s, want %s", result.AsOf(), asOf)
	}
	if len(result.ByScope()) != 2 || result.ByScope()[0].ScopeID() != "scope-a" || result.ByScope()[1].ScopeID() != "scope-b" {
		t.Fatalf("ByScope() = %#v", result.ByScope())
	}

	inventory := result.Inventory()
	if sources := inventory.Sources(); sources.Total() != 5 || sources.MemoryProcessed() != 5 || sources.MemoryPending() != 0 {
		t.Fatalf("Sources() = %#v", sources)
	}
	if artifacts := inventory.Artifacts(); artifacts.Total() != 5 || len(artifacts.ByFamily()) != 1 || artifacts.ByFamily()[0].Total() != 5 {
		t.Fatalf("Artifacts() = %#v", artifacts)
	}
	if entries := inventory.MemoryEntries(); entries.Total() != 2 || entries.Active() != 2 || len(entries.ByKind()) != 1 {
		t.Fatalf("MemoryEntries() = %#v", entries)
	}

	usage := result.Usage()
	generation := usage.Totals().Generation()
	if generation.Requests() != 5 || generation.InputTokens() != nil {
		t.Fatalf("Generation() = %#v", generation)
	}
	if output := generation.OutputTokens(); output == nil || *output != 10 {
		t.Fatalf("Generation().OutputTokens() = %v, want 10", output)
	}
	if len(usage.Daily()) != 1 || usage.Daily()[0].Usage().Generation().Requests() != 5 {
		t.Fatalf("Usage().Daily() = %#v", usage.Daily())
	}
	recall := result.Recall().Totals()
	if recall.Preparations() != 5 || recall.BaselineTokens() != 10 || recall.RecalledTokens() != 5 || recall.TokenReduction() != 5 {
		t.Fatalf("Recall().Totals() = %#v", recall)
	}
}

func aggregateFixture(
	t *testing.T,
	scopeID string,
	asOf time.Time,
	profile inference.TokenEstimatorProfile,
	requests int64,
	inputComplete bool,
) Statistics {
	t.Helper()
	value, err := Build(
		scopeID,
		asOf,
		Today,
		requests,
		InventoryCounts{
			Sources:       requests,
			Artifacts:     []ArtifactCountRow{{Family: "memory", Total: requests}},
			MemoryEntries: []MemoryEntryStateRow{{Kind: "fact", State: "active"}},
		},
		[]StoredModelUsage{{
			UsageDate: asOf, Purpose: MemoryExtraction, Operation: Generation, Requests: requests,
			InputTokens: requests * 2, OutputTokens: requests * 2, InputComplete: inputComplete, OutputComplete: true,
		}},
		&profile,
		[]StoredRecallTokenUsage{{
			UsageDate: asOf, Preparations: requests, ReadyPreparations: requests, ComparablePreparations: requests,
			BaselineTokens: requests * 2, RecalledTokens: requests,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
