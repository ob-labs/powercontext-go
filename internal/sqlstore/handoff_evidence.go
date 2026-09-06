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

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/source"
)

// HandoffEvidenceResolver resolves exact immutable citations inside one Scope.
// Each lookup owns a short transaction; model generation is therefore never
// performed while a database transaction is open.
type HandoffEvidenceResolver struct {
	database  *Database
	scopeID   string
	sources   *SourceRepository
	artifacts *ArtifactRepository
	memory    *memory.Service
}

func NewHandoffEvidenceResolver(
	database *Database,
	scopeID string,
	sources *SourceRepository,
	artifacts *ArtifactRepository,
	memoryService *memory.Service,
) (*HandoffEvidenceResolver, error) {
	if database == nil || sources == nil || artifacts == nil || memoryService == nil {
		return nil, errors.New("sqlstore: Handoff evidence dependencies must not be nil")
	}
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	return &HandoffEvidenceResolver{
		database: database, scopeID: scopeID, sources: sources,
		artifacts: artifacts, memory: memoryService,
	}, nil
}

func (r *HandoffEvidenceResolver) Resolve(
	ctx context.Context,
	citation handoff.Citation,
) (handoff.Evidence, error) {
	var resolved handoff.Evidence
	var err error
	switch value := citation.(type) {
	case handoff.SourceCitation:
		var stored StoredSource
		err = r.database.Transaction(ctx, func(tx DBTX) error {
			var getErr error
			stored, getErr = r.sources.Get(ctx, tx, r.scopeID, value.Ref())
			return getErr
		})
		if err == nil {
			resolved, err = handoff.NewSourceEvidence(value, stored.Value)
		}
	case handoff.ArtifactCitation:
		var stored artifact.Snapshot
		err = r.database.Transaction(ctx, func(tx DBTX) error {
			var getErr error
			stored, getErr = r.artifacts.Get(ctx, tx, r.scopeID, value.Ref())
			return getErr
		})
		if err == nil {
			resolved, err = handoff.NewArtifactEvidence(value, stored)
		}
	case handoff.MemoryCitation:
		var entry memory.EntryVersion
		entry, err = r.memory.ValidateCitation(ctx, value.Citation())
		if err == nil {
			resolved, err = handoff.NewMemoryEvidence(value, entry)
		}
	default:
		return nil, fmt.Errorf("sqlstore: unsupported Handoff citation %T", citation)
	}
	if err != nil {
		if unavailableHandoffEvidence(err) {
			return nil, &handoff.EvidenceUnavailableError{Citation: citation}
		}
		return nil, err
	}
	return resolved, nil
}

func (r *HandoffEvidenceResolver) Validate(ctx context.Context, citation handoff.Citation) error {
	_, err := r.Resolve(ctx, citation)
	return err
}

func unavailableHandoffEvidence(err error) bool {
	var repositoryMissing *RepositoryNotFoundError
	var artifactMissing *artifact.NotFoundError
	var invalidMemory *memory.InvalidCitationError
	var entryMissing *memory.EntryNotFoundError
	var rawObservation *source.UnacceptedObservationError
	var invalidTextEvidence *source.InvalidTextEvidenceError
	return errors.As(err, &repositoryMissing) || errors.As(err, &artifactMissing) ||
		errors.As(err, &invalidMemory) || errors.As(err, &entryMissing) ||
		errors.As(err, &rawObservation) || errors.As(err, &invalidTextEvidence)
}

var _ handoff.EvidenceResolver = (*HandoffEvidenceResolver)(nil)
