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

package server

import (
	"context"
	"fmt"

	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	sqlstoreoceanbase "github.com/ob-labs/powercontext-go/internal/sqlstore/oceanbase"
)

type applicationRepositories struct {
	artifacts       *sqlstore.ArtifactRepository
	sources         *sqlstore.SourceRepository
	candidates      *sqlstore.CandidateRepository
	memoryIndex     *sqlstore.CompositeMemoryIndex
	experienceIndex sqlstore.ExperienceIndex
}

func buildApplicationRepositories(
	ctx context.Context,
	foundation applicationFoundation,
) (applicationRepositories, error) {
	dialect := foundation.storage.dialect
	artifacts, err := sqlstore.NewArtifactRepository(
		dialect,
		sqlstore.MemoryArtifactCodec(), sqlstore.ExperienceArtifactCodec(),
		sqlstore.SkillArtifactCodec(), sqlstore.HandoffArtifactCodec(),
	)
	if err != nil {
		return applicationRepositories{}, err
	}
	sources, err := sqlstore.NewSourceRepository(
		dialect,
		sqlstore.ContentSourceCodec(), sqlstore.ExternalSkillSnapshotSourceCodec(), sqlstore.SkillUsageSourceCodec(),
		sqlstore.SkillPackageUploadSourceCodec(),
	)
	if err != nil {
		return applicationRepositories{}, err
	}
	candidates, err := sqlstore.NewCandidateRepository(
		dialect, sqlstore.ExperienceArtifactCodec(), sqlstore.SkillArtifactCodec(),
	)
	if err != nil {
		return applicationRepositories{}, err
	}

	var memoryIndexes []sqlstore.MemoryIndex
	var experienceIndex sqlstore.ExperienceIndex
	if dialect == sqlstore.SQLiteDialect {
		memoryIndexes = append(memoryIndexes, sqlstore.SQLiteMemoryFTSIndex{})
		experienceIndex = sqlstore.SQLiteExperienceFTSIndex{}
		if foundation.assembled.embeddingModel != nil {
			profile := foundation.assembled.embeddingModel.Profile()
			memoryProfile, buildErr := memory.NewEmbeddingProfile(
				profile.ID(), profile.ModelName(), profile.DimensionCount(), profile.NormalizationMode(),
			)
			if buildErr != nil {
				return applicationRepositories{}, buildErr
			}
			vectorIndex, buildErr := sqlstore.NewSQLiteMemoryVectorIndex(memoryProfile)
			if buildErr != nil {
				return applicationRepositories{}, buildErr
			}
			memoryIndexes = append(memoryIndexes, vectorIndex)
		}
	} else {
		memoryIndexes = append(memoryIndexes, sqlstoreoceanbase.MemoryFTSIndex{})
		experienceIndex = sqlstoreoceanbase.ExperienceFTSIndex{}
		if foundation.assembled.embeddingModel != nil {
			profile := foundation.assembled.embeddingModel.Profile()
			memoryProfile, buildErr := memory.NewEmbeddingProfile(
				profile.ID(), profile.ModelName(), profile.DimensionCount(), profile.NormalizationMode(),
			)
			if buildErr != nil {
				return applicationRepositories{}, buildErr
			}
			vectorIndex, buildErr := sqlstoreoceanbase.NewMemoryVectorIndex(memoryProfile)
			if buildErr != nil {
				return applicationRepositories{}, buildErr
			}
			memoryIndexes = append(memoryIndexes, vectorIndex)
		}
	}
	memoryIndex, err := sqlstore.NewCompositeMemoryIndex(memoryIndexes...)
	if err != nil {
		return applicationRepositories{}, err
	}
	if err := foundation.storage.database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		if err := memoryIndex.Initialize(ctx, tx); err != nil {
			return err
		}
		return experienceIndex.Initialize(ctx, tx)
	}); err != nil {
		return applicationRepositories{}, fmt.Errorf("server: initialize search projections: %w", err)
	}
	return applicationRepositories{
		artifacts: artifacts, sources: sources, candidates: candidates,
		memoryIndex: memoryIndex, experienceIndex: experienceIndex,
	}, nil
}
