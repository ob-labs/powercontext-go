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

	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/sourceevidence"
	"github.com/ob-labs/powercontext-go/source"
	"github.com/ob-labs/powercontext-go/trigger"
)

// ExperienceIncubationStore keeps the authoritative window snapshot separate
// from model inference, then commits all pending Candidates and the cursor CAS
// in one transaction.
type ExperienceIncubationStore struct {
	database   *Database
	scopeID    string
	sources    *SourceRepository
	candidates *CandidateRepository
	cursors    SourceCursorRepository
	policy     trigger.SourceWindowPolicy
}

func NewExperienceIncubationStore(
	database *Database,
	scopeID string,
	sources *SourceRepository,
	candidates *CandidateRepository,
) (*ExperienceIncubationStore, error) {
	if database == nil || sources == nil || candidates == nil {
		return nil, errors.New("sqlstore: Experience incubation dependencies must not be nil")
	}
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	return &ExperienceIncubationStore{
		database: database, scopeID: scopeID, sources: sources, candidates: candidates,
	}, nil
}

func (s *ExperienceIncubationStore) ObserveWindow(
	ctx context.Context,
	bindingName string,
	limit int64,
) (
	previous source.Cursor,
	next source.Cursor,
	generation *int64,
	highWatermark int64,
	values []source.Value,
	available []source.Ref,
	err error,
) {
	previous = s.policy.InitialState()
	next = previous
	err = s.database.Transaction(ctx, func(tx DBTX) error {
		state, found, loadErr := s.cursors.Load(ctx, tx, s.scopeID, bindingName)
		if loadErr != nil {
			return loadErr
		}
		if found {
			previous = state.Cursor
			next = previous
			value := state.Generation
			generation = &value
		}
		highWatermark, loadErr = s.sources.JournalPosition(ctx, tx, s.scopeID)
		if loadErr != nil {
			return loadErr
		}
		signal, signalErr := trigger.NewSourceHighWatermark(highWatermark, limit)
		if signalErr != nil {
			return signalErr
		}
		transition := s.policy.Activate(signal, previous)
		next = transition.State()
		actions := transition.Actions()
		if len(actions) == 0 {
			values = []source.Value{}
			available = []source.Ref{}
			return nil
		}
		windowLimit := int(actions[0].Through() - actions[0].After())
		rows, listErr := s.sources.List(ctx, tx, s.scopeID, actions[0].After(), &windowLimit)
		if listErr != nil {
			return listErr
		}
		values = make([]source.Value, 0, len(rows))
		available = make([]source.Ref, 0, len(rows))
		for _, row := range rows {
			// Unaccepted worker observations stay in the journal and consume the
			// window range, but never become arbitrary pipeline input.
			if !sourceevidence.Allows(row.Value) {
				continue
			}
			values = append(values, row.Value)
			available = append(available, row.Ref)
		}
		return nil
	})
	return previous, next, generation, highWatermark, values, available, err
}

func (s *ExperienceIncubationStore) ApplyWindow(
	ctx context.Context,
	bindingName string,
	candidateIDs []string,
	plans []experience.CandidateInput,
	next source.Cursor,
	expectedGeneration *int64,
) error {
	if len(candidateIDs) != len(plans) {
		return &InvalidRepositoryArgumentError{
			Field: "candidate_ids", Detail: "must correspond exactly to Experience plans",
		}
	}
	return s.database.Transaction(ctx, func(tx DBTX) error {
		for index, plan := range plans {
			refs := plan.Sources()
			for _, ref := range refs {
				stored, err := s.sources.Get(ctx, tx, s.scopeID, ref)
				if err != nil {
					return &review.InvalidCandidateError{
						Field: "evidence", Detail: "reference is not available in this scope",
					}
				}
				if err := sourceevidence.Require(stored.Value); err != nil {
					return err
				}
			}
			reason := plan.Reason()
			if _, err := s.candidates.Create(
				ctx, tx, s.scopeID, candidateIDs[index], experience.Family,
				plan.Proposal(), refs, nil, nil, &reason,
			); err != nil {
				return err
			}
		}
		_, err := s.cursors.Save(ctx, tx, s.scopeID, bindingName, next, expectedGeneration)
		return err
	})
}
