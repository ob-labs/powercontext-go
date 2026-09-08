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
	"errors"
	"time"

	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/stats"
)

// StatisticsReader is the exact read surface needed by the product-facing
// application. Persistence implementations may additionally record usage,
// but the HTTP operation does not depend on those mutation methods.
type StatisticsReader interface {
	Overview(context.Context, stats.Period, time.Time) (stats.Statistics, error)
}

type StatisticsReaderFactory func(string) (StatisticsReader, error)

// StatisticsSelectionResolver is the durable Scope selection boundary needed
// by canonical multi-Scope statistics reads.
type StatisticsSelectionResolver interface {
	ResolveSelection(context.Context, scope.Selection) ([]scope.Descriptor, error)
}

type StatisticsApplication struct {
	runtime  *Runtime
	readers  StatisticsReaderFactory
	clock    Clock
	resolver StatisticsSelectionResolver
}

func NewStatisticsApplication(
	runtime *Runtime,
	readers StatisticsReaderFactory,
	clock Clock,
	resolvers ...StatisticsSelectionResolver,
) (*StatisticsApplication, error) {
	if runtime == nil || readers == nil {
		return nil, errors.New("runtime: Statistics application dependencies must not be nil")
	}
	if len(resolvers) > 1 || (len(resolvers) == 1 && resolvers[0] == nil) {
		return nil, errors.New("runtime: Statistics selection resolver is invalid")
	}
	if clock == nil {
		clock = time.Now
	}
	application := &StatisticsApplication{runtime: runtime, readers: readers, clock: clock}
	if len(resolvers) == 1 {
		application.resolver = resolvers[0]
	}
	return application, nil
}

func (a *StatisticsApplication) Overview(
	ctx context.Context,
	scopeID string,
	period stats.Period,
) (stats.Statistics, error) {
	return a.overview(ctx, scopeID, period, a.clock().UTC())
}

// OverviewSelection resolves the complete immutable Scope set before reading
// any Scope. Every selected snapshot uses the same as_of time, then stats
// aggregates its already-bounded values without changing legacy single-Scope
// reads.
func (a *StatisticsApplication) OverviewSelection(
	ctx context.Context,
	selection scope.Selection,
	period stats.Period,
) (stats.SelectionStatistics, error) {
	if a.resolver == nil {
		return stats.SelectionStatistics{}, &StateError{Code: "statistics"}
	}
	descriptors, err := a.resolver.ResolveSelection(ctx, selection)
	if err != nil {
		return stats.SelectionStatistics{}, err
	}
	asOf := a.clock().UTC()
	scopeIDs := make([]string, len(descriptors))
	snapshots := make([]stats.Statistics, len(descriptors))
	for index, descriptor := range descriptors {
		scopeIDs[index] = descriptor.ID()
		snapshot, overviewErr := a.overview(ctx, descriptor.ID(), period, asOf)
		if overviewErr != nil {
			return stats.SelectionStatistics{}, overviewErr
		}
		snapshots[index] = snapshot
	}
	return stats.Aggregate(selection, scopeIDs, snapshots, asOf)
}

func (a *StatisticsApplication) overview(
	ctx context.Context,
	scopeID string,
	period stats.Period,
	asOf time.Time,
) (stats.Statistics, error) {
	var result stats.Statistics
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		reader, err := a.readers(scope)
		if err != nil {
			return err
		}
		if reader == nil {
			return &StateError{Code: "statistics"}
		}
		result, err = reader.Overview(ctx, period, asOf)
		return err
	})
	return result, err
}
