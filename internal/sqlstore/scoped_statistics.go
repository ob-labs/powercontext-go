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
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/stats"
	"github.com/ob-labs/powercontext-go/trigger"
)

// ScopedStatistics assembles one scope's inventory and bounded daily usage in
// a single relational snapshot.
type ScopedStatistics struct {
	database         *Database
	scopeID          string
	memoryArtifactID string
	artifacts        *ArtifactRepository
	cursors          SourceCursorRepository
	repository       StatisticsRepository
	estimator        *inference.TokenEstimatorProfile
}

func NewScopedStatistics(
	database *Database,
	scopeID, memoryArtifactID string,
	artifacts *ArtifactRepository,
	repository StatisticsRepository,
	estimator *inference.TokenEstimatorProfile,
) (*ScopedStatistics, error) {
	if database == nil || artifacts == nil {
		return nil, errors.New("sqlstore: statistics database and Artifact repository must not be nil")
	}
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if _, err := artifact.NewRef(memory.Family, memoryArtifactID, 1); err != nil {
		return nil, err
	}
	var profile *inference.TokenEstimatorProfile
	if estimator != nil {
		if estimator.EstimatorID() == "" || estimator.Version() == "" {
			return nil, fmt.Errorf("sqlstore: statistics token estimator profile is invalid")
		}
		copy := *estimator
		profile = &copy
	}
	return &ScopedStatistics{
		database: database, scopeID: scopeID, memoryArtifactID: memoryArtifactID,
		artifacts: artifacts, repository: repository, estimator: profile,
	}, nil
}

func (s *ScopedStatistics) Overview(ctx context.Context, period stats.Period, asOf time.Time) (stats.Statistics, error) {
	resolved, err := stats.ResolvePeriod(period, asOf)
	if err != nil {
		return stats.Statistics{}, err
	}
	var inventory stats.InventoryCounts
	var processed int64
	var usage []stats.StoredModelUsage
	var recall []stats.StoredRecallTokenUsage
	err = s.database.Transaction(ctx, func(tx DBTX) error {
		var inventoryErr error
		inventory, inventoryErr = s.repository.Inventory(ctx, tx, s.scopeID)
		if inventoryErr != nil {
			return inventoryErr
		}
		var memoryErr error
		inventory.MemoryEntries, memoryErr = s.repository.MemoryEntryStates(ctx, tx, s.scopeID, s.memoryArtifactID, s.artifacts)
		if memoryErr != nil {
			return memoryErr
		}
		cursor, found, cursorErr := s.cursors.Load(ctx, tx, s.scopeID, trigger.SourceWindowName)
		if cursorErr != nil {
			return cursorErr
		}
		if found {
			processed = cursor.Cursor.Sequence()
		}
		var usageErr error
		usage, usageErr = s.repository.Usage(ctx, tx, s.scopeID, resolved.StartDate(), resolved.EndDate())
		if usageErr != nil {
			return usageErr
		}
		if s.estimator != nil {
			var recallErr error
			recall, recallErr = s.repository.RecallUsage(ctx, tx, s.scopeID, resolved.StartDate(), resolved.EndDate(), *s.estimator)
			return recallErr
		}
		return nil
	})
	if err != nil {
		return stats.Statistics{}, err
	}
	return stats.Build(s.scopeID, asOf, period, processed, inventory, usage, s.estimator, recall)
}

func (s *ScopedStatistics) Record(
	ctx context.Context,
	purpose stats.ModelPurpose,
	operation stats.ModelOperation,
	usage inference.Usage,
	usageDate time.Time,
) error {
	return s.database.Transaction(ctx, func(tx DBTX) error {
		return s.repository.Record(ctx, tx, s.scopeID, usageDate, purpose, operation, usage)
	})
}

func (s *ScopedStatistics) RecordRecall(
	ctx context.Context,
	measurement stats.RecallTokenMeasurement,
	usageDate time.Time,
) error {
	if s.estimator == nil || *s.estimator != measurement.Estimator() {
		return fmt.Errorf("recall measurement estimator does not match the deployment profile")
	}
	return s.database.Transaction(ctx, func(tx DBTX) error {
		return s.repository.RecordRecall(ctx, tx, s.scopeID, usageDate, measurement)
	})
}
