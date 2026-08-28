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
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/source"
)

type SourceOperations interface {
	CaptureContent(context.Context, string, string, string, map[string]any) (runtime.SourceReceipt, error)
}

type MemoryOperations interface {
	Flush(context.Context, string) (runtime.MemoryFlushResult, error)
	Remember(context.Context, string, runtime.RememberMemoryRequest) (runtime.MemoryMutationResult, error)
	Search(context.Context, string, string, int, memory.SearchMode) (runtime.MemorySearchPage, error)
	List(context.Context, string, bool) (runtime.MemoryEntriesPage, error)
	Get(context.Context, string, memory.Citation) (runtime.MemoryEntryRecord, error)
	Revise(context.Context, string, memory.Citation, string, string, *string) (runtime.MemoryMutationResult, error)
	Retire(context.Context, string, memory.Citation, *string) (runtime.MemoryMutationResult, error)
	Changes(context.Context, string, *int64) (runtime.MemoryChangesPage, error)
}

func (h *Handler) FlushMemory(ctx context.Context, req *v1.FlushMemoryRequest) (v1.FlushMemoryRes, error) {
	if h.memory == nil {
		return nil, &RuntimeNotReadyError{}
	}
	result, err := h.memory.Flush(ctx, req.ScopeID)
	if err != nil {
		return nil, err
	}
	status := v1.FlushStatusIdle
	if result.Processed() {
		status = v1.FlushStatusProcessed
	}
	return &v1.FlushMemoryResponseHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response: v1.FlushMemoryResponse{
			Status: status, PreviousCursor: int(result.PreviousCursor),
			CurrentCursor: int(result.CurrentCursor), HighWatermark: int(result.HighWatermark),
			ProcessedSourceCount: result.ProcessedSourceCount,
			Memory:               optionalArtifactReference(result.MemoryRef),
		},
	}, nil
}

func (h *Handler) CaptureContentSource(ctx context.Context, req *v1.CaptureContentSourceRequest) (v1.CaptureContentSourceRes, error) {
	if h.sources == nil {
		return nil, &RuntimeNotReadyError{}
	}
	metadata, err := decodeMetadata(req.Metadata)
	if err != nil {
		return nil, &InvalidRequestError{Field: "metadata"}
	}
	receipt, err := h.sources.CaptureContent(ctx, req.ScopeID, req.SourceID, req.Content, metadata)
	if err != nil {
		return nil, err
	}
	return &v1.CaptureContentSourceResponseHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response: v1.CaptureContentSourceResponse{
			Status:   v1.CaptureStatusAccepted,
			Source:   sourceReference(receipt.Ref),
			Position: int(receipt.Sequence),
		},
	}, nil
}

func (h *Handler) RememberMemory(ctx context.Context, req *v1.RememberMemoryRequest) (v1.RememberMemoryRes, error) {
	if h.memory == nil {
		return nil, &RuntimeNotReadyError{}
	}
	result, err := h.memory.Remember(ctx, req.ScopeID, runtime.RememberMemoryRequest{
		Kind: req.Kind, Text: req.Text, Reason: optionalString(req.Reason),
		ExpectedRevision: optionalInt64(req.ExpectedRevision),
	})
	if err != nil {
		return nil, err
	}
	return mutationResponse(ctx, result), nil
}

func (h *Handler) SearchMemory(ctx context.Context, req *v1.SearchMemoryRequest) (v1.SearchMemoryRes, error) {
	if h.memory == nil {
		return nil, &RuntimeNotReadyError{}
	}
	result, err := h.memory.Search(
		ctx,
		req.ScopeID,
		req.Query,
		req.Limit.Or(10),
		memory.SearchMode(req.Mode.Or(v1.MemorySearchModeAuto)),
	)
	if err != nil {
		return nil, err
	}
	hits := make([]v1.SearchMemoryHit, len(result.Hits))
	for index, hit := range result.Hits {
		matched := make([]v1.MemoryMatchedBy, len(hit.MatchedBy))
		for channelIndex, channel := range hit.MatchedBy {
			matched[channelIndex] = v1.MemoryMatchedBy(channel)
		}
		hits[index] = v1.SearchMemoryHit{
			Citation: memoryCitation(memory.Citation{
				MemoryRef: hit.MemoryRef, EntryID: hit.EntryID, EntryVersionID: hit.EntryVersionID,
			}),
			Text: hit.Text, Score: hit.Score, MatchedBy: matched,
		}
	}
	response := v1.SearchMemoryResponse{Memory: optionalArtifactReference(result.MemoryRef), Hits: hits}
	if result.Mode != nil {
		response.Mode = v1.NewOptNilMemoryUsedSearchMode(v1.MemoryUsedSearchMode(*result.Mode))
	} else {
		response.Mode.SetToNull()
	}
	return &v1.SearchMemoryResponseHeaders{XPowerContextRequestID: requestID(ctx), Response: response}, nil
}

func (h *Handler) ListMemoryEntries(ctx context.Context, req *v1.ListMemoryEntriesRequest) (v1.ListMemoryEntriesRes, error) {
	if h.memory == nil {
		return nil, &RuntimeNotReadyError{}
	}
	result, err := h.memory.List(ctx, req.ScopeID, req.IncludeInactive.Or(false))
	if err != nil {
		return nil, err
	}
	entries := make([]v1.MemoryEntry, len(result.Entries))
	for index, entry := range result.Entries {
		entries[index] = memoryEntry(entry)
	}
	return &v1.ListMemoryEntriesResponseHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response: v1.ListMemoryEntriesResponse{
			Memory: optionalArtifactReference(result.MemoryRef), Entries: entries,
		},
	}, nil
}

func (h *Handler) GetMemoryEntry(ctx context.Context, req *v1.GetMemoryEntryRequest) (v1.GetMemoryEntryRes, error) {
	if h.memory == nil {
		return nil, &RuntimeNotReadyError{}
	}
	citation, err := runtimeCitation(req.Citation)
	if err != nil {
		return nil, err
	}
	result, err := h.memory.Get(ctx, req.ScopeID, citation)
	if err != nil {
		return nil, err
	}
	return &v1.MemoryEntryHeaders{XPowerContextRequestID: requestID(ctx), Response: memoryEntry(result)}, nil
}

func (h *Handler) ReviseMemoryEntry(ctx context.Context, req *v1.ReviseMemoryEntryRequest) (v1.ReviseMemoryEntryRes, error) {
	if h.memory == nil {
		return nil, &RuntimeNotReadyError{}
	}
	citation, err := runtimeCitation(req.Citation)
	if err != nil {
		return nil, err
	}
	result, err := h.memory.Revise(ctx, req.ScopeID, citation, req.Kind, req.Text, optionalString(req.Reason))
	if err != nil {
		return nil, err
	}
	return mutationResponse(ctx, result), nil
}

func (h *Handler) RetireMemoryEntry(ctx context.Context, req *v1.RetireMemoryEntryRequest) (v1.RetireMemoryEntryRes, error) {
	if h.memory == nil {
		return nil, &RuntimeNotReadyError{}
	}
	citation, err := runtimeCitation(req.Citation)
	if err != nil {
		return nil, err
	}
	result, err := h.memory.Retire(ctx, req.ScopeID, citation, optionalString(req.Reason))
	if err != nil {
		return nil, err
	}
	return mutationResponse(ctx, result), nil
}

func (h *Handler) ListMemoryChanges(ctx context.Context, req *v1.ListMemoryChangesRequest) (v1.ListMemoryChangesRes, error) {
	if h.memory == nil {
		return nil, &RuntimeNotReadyError{}
	}
	result, err := h.memory.Changes(ctx, req.ScopeID, optionalInt64(req.SinceRevision))
	if err != nil {
		return nil, err
	}
	revisions := make([]v1.MemoryRevisionChanges, len(result.Revisions))
	for index, revision := range result.Revisions {
		changes := make([]v1.EntryChange, len(revision.Changes))
		for changeIndex, change := range revision.Changes {
			changes[changeIndex] = v1.EntryChange{
				Op: v1.EntryChangeOperation(change.Op()), EntryID: change.EntryID(),
				FromEntryVersionID: nullableString(change.FromEntryVersionID()),
				ToEntryVersionID:   nullableString(change.ToEntryVersionID()),
				Reason:             nullableString(change.Reason()),
			}
		}
		revisions[index] = v1.MemoryRevisionChanges{MemoryRef: artifactReference(revision.MemoryRef), Changes: changes}
	}
	return &v1.ListMemoryChangesResponseHeaders{
		XPowerContextRequestID: requestID(ctx),
		Response: v1.ListMemoryChangesResponse{
			Memory: optionalArtifactReference(result.MemoryRef), Revisions: revisions,
		},
	}, nil
}

func mutationResponse(ctx context.Context, result runtime.MemoryMutationResult) *v1.MemoryMutationResponseHeaders {
	response := v1.MemoryMutationResponse{Memory: artifactReference(result.MemoryRef)}
	if result.Entry != nil {
		response.Entry = v1.NewOptNilMemoryEntry(memoryEntry(*result.Entry))
	} else {
		response.Entry.SetToNull()
	}
	return &v1.MemoryMutationResponseHeaders{XPowerContextRequestID: requestID(ctx), Response: response}
}

func memoryEntry(value runtime.MemoryEntryRecord) v1.MemoryEntry {
	entry := value.Entry
	sources := make([]v1.SourceReference, len(entry.Sources))
	for index, ref := range entry.Sources {
		sources[index] = sourceReference(ref)
	}
	artifacts := make([]v1.ArtifactReference, len(entry.Artifacts))
	for index, ref := range entry.Artifacts {
		artifacts[index] = artifactReference(ref)
	}
	return v1.MemoryEntry{
		Citation: memoryCitation(memory.Citation{
			MemoryRef: value.MemoryRef, EntryID: entry.EntryID, EntryVersionID: entry.EntryVersionID,
		}),
		Version: int(entry.Version), Kind: entry.Kind, Text: entry.Text,
		State: v1.MemoryEntryState(value.State), SourceRefs: sources, ArtifactRefs: artifacts,
	}
}

func runtimeCitation(value v1.MemoryCitation) (memory.Citation, error) {
	ref, err := runtimeArtifactReference(value.MemoryRef)
	if err != nil {
		return memory.Citation{}, err
	}
	if _, err := memory.ValidateIdentifier(value.EntryID); err != nil {
		return memory.Citation{}, err
	}
	if _, err := memory.ValidateIdentifier(value.EntryVersionID); err != nil {
		return memory.Citation{}, err
	}
	return memory.Citation{MemoryRef: ref, EntryID: value.EntryID, EntryVersionID: value.EntryVersionID}, nil
}

func memoryCitation(value memory.Citation) v1.MemoryCitation {
	return v1.MemoryCitation{
		MemoryRef: artifactReference(value.MemoryRef), EntryID: value.EntryID, EntryVersionID: value.EntryVersionID,
	}
}

func artifactReference(value artifact.Ref) v1.ArtifactReference {
	return v1.ArtifactReference{Family: value.Family(), ArtifactID: value.ID(), Revision: int(value.Revision())}
}

func runtimeArtifactReference(value v1.ArtifactReference) (artifact.Ref, error) {
	return artifact.NewRef(value.Family, value.ArtifactID, int64(value.Revision))
}

func sourceReference(value source.Ref) v1.SourceReference {
	return v1.SourceReference{Name: value.Type(), SourceID: value.ID()}
}

func optionalArtifactReference(value *artifact.Ref) v1.OptNilArtifactReference {
	result := v1.OptNilArtifactReference{}
	if value == nil {
		result.SetToNull()
		return result
	}
	return v1.NewOptNilArtifactReference(artifactReference(*value))
}

func optionalString(value v1.OptNilString) *string {
	if resolved, ok := value.Get(); ok {
		return &resolved
	}
	return nil
}

func optionalInt64(value v1.OptNilInt) *int64 {
	if resolved, ok := value.Get(); ok {
		converted := int64(resolved)
		return &converted
	}
	return nil
}

func nullableString(value *string) v1.NilString {
	if value == nil {
		result := v1.NilString{}
		result.SetToNull()
		return result
	}
	return v1.NewNilString(*value)
}

func decodeMetadata(value v1.OptNilCaptureContentSourceRequestMetadata) (map[string]any, error) {
	raw, ok := value.Get()
	if !ok {
		return map[string]any{}, nil
	}
	result := make(map[string]any, len(raw))
	for key, encoded := range raw {
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		var decoded any
		if err := decoder.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("metadata %q: %w", key, err)
		}
		result[key] = decoded
	}
	return result, nil
}

var (
	_ SourceOperations = (*runtime.SourceApplication)(nil)
	_ MemoryOperations = (*runtime.MemoryApplication)(nil)
)
