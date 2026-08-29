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

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/go-faster/jx"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/source"
)

type endpointOperations struct {
	handler  v1.Handler
	recorder *recordingReranker
}

func (o endpointOperations) Capture(
	ctx context.Context, scopeID, sourceID, content string, metadata map[string]any,
) (int64, error) {
	encoded := make(v1.CaptureContentSourceRequestMetadata, len(metadata))
	for name, value := range metadata {
		raw, err := json.Marshal(value)
		if err != nil {
			return 0, fmt.Errorf("encode Source metadata %s: %w", name, err)
		}
		encoded[name] = jx.Raw(raw)
	}
	result, err := o.handler.CaptureContentSource(ctx, &v1.CaptureContentSourceRequest{
		ScopeID: scopeID, SourceID: sourceID, Content: content,
		Metadata: v1.NewOptNilCaptureContentSourceRequestMetadata(encoded),
	})
	if err != nil {
		return 0, err
	}
	response, ok := result.(*v1.CaptureContentSourceResponseHeaders)
	if !ok {
		return 0, fmt.Errorf("capture response is %T", result)
	}
	return int64(response.Response.Position), nil
}

func (o endpointOperations) Flush(ctx context.Context, scopeID string) (pcruntime.MemoryFlushResult, error) {
	result, err := o.handler.FlushMemory(ctx, &v1.FlushMemoryRequest{ScopeID: scopeID})
	if err != nil {
		return pcruntime.MemoryFlushResult{}, err
	}
	response, ok := result.(*v1.FlushMemoryResponseHeaders)
	if !ok {
		return pcruntime.MemoryFlushResult{}, fmt.Errorf("flush response is %T", result)
	}
	value := response.Response
	converted := pcruntime.MemoryFlushResult{
		PreviousCursor: int64(value.PreviousCursor), CurrentCursor: int64(value.CurrentCursor),
		HighWatermark: int64(value.HighWatermark), ProcessedSourceCount: value.ProcessedSourceCount,
	}
	if wire, ok := value.Memory.Get(); ok {
		ref, err := domainArtifactRef(wire)
		if err != nil {
			return pcruntime.MemoryFlushResult{}, err
		}
		converted.MemoryRef = &ref
	}
	return converted, nil
}

func (o endpointOperations) List(ctx context.Context, scopeID string) (pcruntime.MemoryEntriesPage, error) {
	result, err := o.handler.ListMemoryEntries(ctx, &v1.ListMemoryEntriesRequest{ScopeID: scopeID})
	if err != nil {
		return pcruntime.MemoryEntriesPage{}, err
	}
	response, ok := result.(*v1.ListMemoryEntriesResponseHeaders)
	if !ok {
		return pcruntime.MemoryEntriesPage{}, fmt.Errorf("list Memory response is %T", result)
	}
	page := pcruntime.MemoryEntriesPage{Entries: make([]pcruntime.MemoryEntryRecord, len(response.Response.Entries))}
	if wire, ok := response.Response.Memory.Get(); ok {
		ref, err := domainArtifactRef(wire)
		if err != nil {
			return pcruntime.MemoryEntriesPage{}, err
		}
		page.MemoryRef = &ref
	}
	for index, entry := range response.Response.Entries {
		memoryRef, err := domainArtifactRef(entry.Citation.MemoryRef)
		if err != nil {
			return pcruntime.MemoryEntriesPage{}, err
		}
		sources := make([]source.Ref, len(entry.SourceRefs))
		for sourceIndex, wire := range entry.SourceRefs {
			ref, err := source.NewRef(wire.Name, wire.SourceID)
			if err != nil {
				return pcruntime.MemoryEntriesPage{}, err
			}
			sources[sourceIndex] = ref
		}
		page.Entries[index] = pcruntime.MemoryEntryRecord{
			MemoryRef: memoryRef, State: memory.EntryState(entry.State),
			Entry: memory.EntryVersion{
				MemoryArtifactID: memoryRef.ID(), EntryID: entry.Citation.EntryID,
				EntryVersionID: entry.Citation.EntryVersionID, Version: int64(entry.Version),
				Kind: entry.Kind, Text: entry.Text, Sources: sources,
			},
		}
	}
	return page, nil
}

func (o endpointOperations) Search(
	ctx context.Context, scopeID, query string, limit int, mode memory.SearchMode,
) (pcruntime.MemorySearchPage, error) {
	var traceChannel chan *memory.RerankTrace
	if o.recorder != nil {
		traceChannel = make(chan *memory.RerankTrace, 1)
		ctx = context.WithValue(ctx, rerankTraceContextKey{}, traceChannel)
	}
	result, err := o.handler.SearchMemory(ctx, &v1.SearchMemoryRequest{
		ScopeID: scopeID, Query: query, Limit: v1.NewOptInt(limit),
		Mode: v1.NewOptMemorySearchMode(v1.MemorySearchMode(mode)),
	})
	if err != nil {
		return pcruntime.MemorySearchPage{}, err
	}
	response, ok := result.(*v1.SearchMemoryResponseHeaders)
	if !ok {
		return pcruntime.MemorySearchPage{}, fmt.Errorf("search response is %T", result)
	}
	page := pcruntime.MemorySearchPage{Hits: make([]memory.Hit, len(response.Response.Hits))}
	if wire, ok := response.Response.Memory.Get(); ok {
		ref, err := domainArtifactRef(wire)
		if err != nil {
			return pcruntime.MemorySearchPage{}, err
		}
		page.MemoryRef = &ref
	}
	if wire, ok := response.Response.Mode.Get(); ok {
		used := memory.SearchMode(wire)
		page.Mode = &used
	}
	for index, hit := range response.Response.Hits {
		ref, err := domainArtifactRef(hit.Citation.MemoryRef)
		if err != nil {
			return pcruntime.MemorySearchPage{}, err
		}
		matched := make([]memory.MatchedBy, len(hit.MatchedBy))
		for channel, wire := range hit.MatchedBy {
			matched[channel] = memory.MatchedBy(wire)
		}
		page.Hits[index] = memory.Hit{
			MemoryRef: ref, EntryID: hit.Citation.EntryID, EntryVersionID: hit.Citation.EntryVersionID,
			Text: hit.Text, Score: hit.Score, MatchedBy: matched,
		}
	}
	if traceChannel != nil {
		select {
		case page.Rerank = <-traceChannel:
		default:
		}
	}
	return page, nil
}

func domainArtifactRef(value v1.ArtifactReference) (artifact.Ref, error) {
	return artifact.NewRef(value.Family, value.ArtifactID, int64(value.Revision))
}

type rerankTraceContextKey struct{}

type recordingReranker struct{ next memory.Reranker }

func (r *recordingReranker) PolicyID() string { return r.next.PolicyID() }

func (r *recordingReranker) Rerank(
	ctx context.Context, query string, candidates []memory.Hit, limit int,
) (memory.RerankDecision, error) {
	started := time.Now()
	decision, err := r.next.Rerank(ctx, query, candidates, limit)
	if err != nil {
		return memory.RerankDecision{}, err
	}
	trace := &memory.RerankTrace{
		PolicyID: r.next.PolicyID(), CandidateHits: cloneMemoryHits(candidates),
		SelectedRanks: decision.SelectedRanks(), DiscardedRankCount: decision.DiscardedRankCount(),
		UsedFallback: decision.UsedFallback(), LatencyMS: float64(time.Since(started)) / float64(time.Millisecond),
		Usage: decision.Usage(),
	}
	if output, ok := ctx.Value(rerankTraceContextKey{}).(chan *memory.RerankTrace); ok {
		select {
		case output <- trace:
		default:
		}
	}
	return decision, nil
}

func cloneMemoryHits(values []memory.Hit) []memory.Hit {
	result := make([]memory.Hit, len(values))
	for index, value := range values {
		value.MatchedBy = slices.Clone(value.MatchedBy)
		result[index] = value
	}
	return result
}
