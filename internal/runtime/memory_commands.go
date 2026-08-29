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

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/stats"
	"github.com/ob-labs/powercontext-go/trigger"
)

// Flush advances the stable Memory Source window. Planning may invoke models
// and is intentionally performed after ObserveWindow has committed and before
// ApplyWindow opens the final transaction.
func (a *MemoryApplication) Flush(ctx context.Context, scopeID string) (MemoryFlushResult, error) {
	var result MemoryFlushResult
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		var operationErr error
		result, operationErr = a.flush(ctx, scope)
		return operationErr
	})
	return cloneFlushResult(result), err
}

// flush performs an already-admitted, already-serialized Source window. It is
// shared with the scheduled processor so shutdown never requires recursive
// lifecycle admission.
func (a *MemoryApplication) flush(ctx context.Context, scope string) (MemoryFlushResult, error) {
	ctx = a.runtime.withModelUsage(ctx, scope, stats.MemoryExtraction, stats.MemoryIndexing)
	if a.flushes == nil {
		return MemoryFlushResult{}, &StateError{Code: "memory-flush"}
	}
	backend, err := a.flushes(scope)
	if err != nil {
		return MemoryFlushResult{}, err
	}
	if backend == nil {
		return MemoryFlushResult{}, &StateError{Code: "memory-flush"}
	}
	previous, next, generation, highWatermark, sources, err := backend.ObserveWindow(
		ctx, trigger.SourceWindowName, a.sourceWindowLimit,
	)
	if err != nil {
		return MemoryFlushResult{}, err
	}
	result := MemoryFlushResult{
		PreviousCursor: previous.Sequence(), CurrentCursor: next.Sequence(),
		HighWatermark: highWatermark, ProcessedSourceCount: len(sources),
	}
	if next.Sequence() == previous.Sequence() {
		return result, nil
	}

	service, err := a.service(scope)
	if err != nil {
		return result, err
	}
	current, err := a.headOrNone(ctx, service)
	if err != nil {
		return result, err
	}
	plan, err := service.PlanRemember(ctx, current, sources, nil, nil, memory.RememberExtract)
	if err != nil {
		return result, err
	}
	updated, err := backend.ApplyWindow(ctx, trigger.SourceWindowName, plan, next, generation)
	if err != nil {
		return result, err
	}
	if updated != nil {
		ref := updated.Ref()
		result.MemoryRef = &ref
	}
	return result, nil
}

func (a *MemoryApplication) Remember(
	ctx context.Context,
	scopeID string,
	request RememberMemoryRequest,
) (MemoryMutationResult, error) {
	var result MemoryMutationResult
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		ctx = a.runtime.withModelUsage(ctx, scope, "", stats.MemoryIndexing)
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		current, err := a.headOrNone(ctx, service)
		if err != nil {
			return err
		}
		if validationErr := validateExpectedRevision(a.memoryArtifactID, current, request.ExpectedRevision); validationErr != nil {
			return validationErr
		}
		input := memory.NewEntryInput(nil, request.Kind, request.Text, nil, nil, request.Reason)
		updated, err := service.Remember(ctx, current, nil, nil, []memory.EntryInput{input}, memory.RememberAppend)
		if err != nil {
			return err
		}
		if updated == nil {
			return &StateError{Code: "empty-write"}
		}
		result.MemoryRef = updated.Ref()
		if current != nil {
			previous := current.Revision()
			result.PreviousRevision = &previous
		}
		if current == nil || current.Ref() != updated.Ref() {
			result.Entry, err = lastChangedEntry(ctx, service, *updated)
		}
		return err
	})
	return cloneMutation(result), err
}

func (a *MemoryApplication) Revise(
	ctx context.Context,
	scopeID string,
	citation memory.Citation,
	kind, text string,
	reason *string,
) (MemoryMutationResult, error) {
	var result MemoryMutationResult
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		ctx = a.runtime.withModelUsage(ctx, scope, "", stats.MemoryIndexing)
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		current, entry, err := a.currentCitation(ctx, service, citation)
		if err != nil {
			return err
		}
		input := memory.NewEntryInput(&entry, kind, text, nil, nil, reason)
		updated, err := service.Remember(ctx, &current, nil, nil, []memory.EntryInput{input}, memory.RememberAppend)
		if err != nil {
			return err
		}
		if updated == nil {
			return &StateError{Code: "empty-write"}
		}
		revised, err := logicalEntry(ctx, service, *updated, entry.EntryID)
		if err != nil {
			return err
		}
		record, err := entryRecord(*updated, revised)
		if err != nil {
			return err
		}
		previous := current.Revision()
		result = MemoryMutationResult{PreviousRevision: &previous, MemoryRef: updated.Ref(), Entry: &record}
		return nil
	})
	return cloneMutation(result), err
}

func (a *MemoryApplication) Retire(
	ctx context.Context,
	scopeID string,
	citation memory.Citation,
	reason *string,
) (MemoryMutationResult, error) {
	var result MemoryMutationResult
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		ctx = a.runtime.withModelUsage(ctx, scope, "", stats.MemoryIndexing)
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		current, entry, err := a.currentCitation(ctx, service, citation)
		if err != nil {
			return err
		}
		updated, err := service.Forget(ctx, current, []memory.EntryVersion{entry}, reason)
		if err != nil {
			return err
		}
		retired, err := logicalEntry(ctx, service, updated, entry.EntryID)
		if err != nil {
			return err
		}
		record, err := entryRecord(updated, retired)
		if err != nil {
			return err
		}
		previous := current.Revision()
		result = MemoryMutationResult{PreviousRevision: &previous, MemoryRef: updated.Ref(), Entry: &record}
		return nil
	})
	return cloneMutation(result), err
}

func (a *MemoryApplication) currentCitation(
	ctx context.Context,
	service *memory.Service,
	citation memory.Citation,
) (memory.Memory, memory.EntryVersion, error) {
	current, err := service.Head(ctx, a.memoryArtifactID)
	if err != nil {
		return memory.Memory{}, memory.EntryVersion{}, err
	}
	if citation.MemoryRef.ID() != current.ID() {
		return memory.Memory{}, memory.EntryVersion{}, &artifact.NotFoundError{Ref: citation.MemoryRef}
	}
	if citation.MemoryRef != current.Ref() {
		return memory.Memory{}, memory.EntryVersion{}, &artifact.RevisionConflictError{
			Requested: citation.MemoryRef, Current: current.Ref(),
		}
	}
	entry, err := citedEntry(ctx, service, current, citation)
	return current, entry, err
}

func validateExpectedRevision(memoryArtifactID string, current *memory.Memory, expected *int64) error {
	if expected == nil {
		return nil
	}
	requested, err := artifact.NewRef(memory.Family, memoryArtifactID, *expected)
	if err != nil {
		return err
	}
	if current == nil {
		return &artifact.NotFoundError{Ref: requested}
	}
	if current.Revision() != *expected {
		return &artifact.RevisionConflictError{Requested: requested, Current: current.Ref()}
	}
	return nil
}
