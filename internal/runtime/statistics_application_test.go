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
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/stats"
)

func TestStatisticsApplicationOverviewSelectionResolvesBeforeReadingScopes(t *testing.T) {
	t.Parallel()
	first := statisticsScopeDescriptor(t, "scope-a")
	second := statisticsScopeDescriptor(t, "scope-b")
	resolver := &statisticsSelectionResolver{descriptors: []scope.Descriptor{first, second}}
	readers := map[string]*statisticsReader{
		"scope-a": {value: statisticsSnapshot(t, "scope-a", 2)},
		"scope-b": {value: statisticsSnapshot(t, "scope-b", 3)},
	}
	clock := func() time.Time { return time.Date(2026, time.September, 8, 13, 14, 15, 0, time.UTC) }
	application, err := NewStatisticsApplication(
		New(),
		func(scopeID string) (StatisticsReader, error) { return readers[scopeID], nil },
		clock,
		resolver,
	)
	if err != nil {
		t.Fatal(err)
	}
	selection := scope.AllSelection()
	result, err := application.OverviewSelection(t.Context(), selection, stats.Today)
	if err != nil {
		t.Fatal(err)
	}
	if resolver.calls != 1 || resolver.selection.Mode() != selection.Mode() {
		t.Fatalf("ResolveSelection() = (%d, %#v)", resolver.calls, resolver.selection)
	}
	if !slices.Equal(result.ScopeIDs(), []string{"scope-a", "scope-b"}) || result.Inventory().Sources().Total() != 5 {
		t.Fatalf("OverviewSelection() = %#v", result)
	}
	for scopeID, reader := range readers {
		if reader.calls != 1 || reader.asOf != clock() || reader.period != stats.Today {
			t.Fatalf("reader %s = %#v", scopeID, reader)
		}
	}
}

func TestStatisticsApplicationOverviewSelectionDoesNotReadUnknownScope(t *testing.T) {
	t.Parallel()
	resolver := &statisticsSelectionResolver{err: &scope.NotFoundError{}}
	reader := &statisticsReader{}
	application, err := NewStatisticsApplication(
		New(),
		func(string) (StatisticsReader, error) { return reader, nil },
		time.Now,
		resolver,
	)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := scope.NewExactSelection([]string{"unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if _, overviewErr := application.OverviewSelection(t.Context(), selection, stats.Today); overviewErr == nil {
		t.Fatal("OverviewSelection() accepted an unknown Scope")
	}
	if reader.calls != 0 {
		t.Fatalf("reader calls = %d, want 0", reader.calls)
	}
}

type statisticsSelectionResolver struct {
	descriptors []scope.Descriptor
	selection   scope.Selection
	err         error
	calls       int
}

func (s *statisticsSelectionResolver) ResolveSelection(_ context.Context, selection scope.Selection) ([]scope.Descriptor, error) {
	s.calls++
	s.selection = selection
	return slices.Clone(s.descriptors), s.err
}

type statisticsReader struct {
	value  stats.Statistics
	period stats.Period
	asOf   time.Time
	calls  int
}

func (s *statisticsReader) Overview(_ context.Context, period stats.Period, asOf time.Time) (stats.Statistics, error) {
	s.calls++
	s.period, s.asOf = period, asOf
	return s.value, nil
}

func statisticsScopeDescriptor(t *testing.T, id string) scope.Descriptor {
	t.Helper()
	value, err := scope.NewDescriptor(id, "Statistics", "Statistics scope", "", nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func statisticsSnapshot(t *testing.T, scopeID string, sources int64) stats.Statistics {
	t.Helper()
	value, err := stats.Build(
		scopeID,
		time.Date(2026, time.September, 8, 13, 14, 15, 0, time.UTC),
		stats.Today,
		sources,
		stats.InventoryCounts{Sources: sources},
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
