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

package locomo

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
)

type IngestOptions struct {
	RunID             string
	OutputDirectory   string
	DatabaseKind      string
	ConversationLimit *int
	Concurrency       int
	OperationRetries  int
	Clock             func() time.Time
	Progress          Progress
}

type ConversationIngestion struct {
	ScopeID           string   `json:"scope_id"`
	SessionCount      int      `json:"session_count"`
	MemoryEntryCount  int      `json:"memory_entry_count"`
	MemoryRevision    *int64   `json:"memory_revision"`
	FlushLatencyMSP50 *float64 `json:"flush_latency_ms_p50"`
	FlushLatencyMSP95 *float64 `json:"flush_latency_ms_p95"`
	resumedSessions   int
	unchangedFlushes  int
	transientRetries  int
}

type IngestionReport struct {
	Schema                     string                           `json:"schema"`
	RunID                      string                           `json:"run_id"`
	CompletedAt                string                           `json:"completed_at"`
	DatabaseKind               string                           `json:"database_kind"`
	ConversationCount          int                              `json:"conversation_count"`
	SessionCount               int                              `json:"session_count"`
	ResumedSessionCount        int                              `json:"resumed_session_count"`
	NewlyProcessedSessionCount int                              `json:"newly_processed_session_count"`
	NoMemoryChangeFlushCount   int                              `json:"no_memory_change_flush_count"`
	TransientRetryCount        int                              `json:"transient_retry_count"`
	MemoryEntryCount           int                              `json:"memory_entry_count"`
	DurationSeconds            float64                          `json:"duration_seconds"`
	Conversations              map[string]ConversationIngestion `json:"conversations"`
}

func Ingest(ctx context.Context, dataset Dataset, operations Operations, options IngestOptions) (IngestionReport, error) {
	if operations == nil {
		return IngestionReport{}, fmt.Errorf("benchmark operations must not be nil")
	}
	if options.Concurrency < 1 {
		return IngestionReport{}, fmt.Errorf("ingestion concurrency must be positive")
	}
	if options.OperationRetries < 1 {
		return IngestionReport{}, fmt.Errorf("operation_retries must be positive")
	}
	runID, err := NormalizeRunID(options.RunID)
	if err != nil {
		return IngestionReport{}, err
	}
	conversations := dataset.Conversations()
	if options.ConversationLimit != nil && *options.ConversationLimit < len(conversations) {
		conversations = conversations[:max(*options.ConversationLimit, 0)]
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	progress := options.Progress
	if progress == nil {
		progress = func(string) {}
	}
	started := time.Now()
	results, err := parallelConversations(ctx, conversations, options.Concurrency, func(ctx context.Context, conversation Conversation) (ConversationIngestion, error) {
		return ingestConversation(ctx, dataset, conversation, operations, runID, options.OperationRetries, progress)
	})
	if err != nil {
		return IngestionReport{}, err
	}

	report := IngestionReport{
		Schema: "powercontext.benchmark.locomo.ingestion.v1", RunID: runID,
		CompletedAt: pythonUTC(clock()), DatabaseKind: options.DatabaseKind,
		ConversationCount: len(conversations), DurationSeconds: time.Since(started).Seconds(),
		Conversations: make(map[string]ConversationIngestion, len(results)),
	}
	for index, result := range results {
		report.Conversations[conversations[index].SampleID()] = result
		report.SessionCount += result.SessionCount
		report.ResumedSessionCount += result.resumedSessions
		report.NoMemoryChangeFlushCount += result.unchangedFlushes
		report.TransientRetryCount += result.transientRetries
		report.MemoryEntryCount += result.MemoryEntryCount
	}
	report.NewlyProcessedSessionCount = report.SessionCount - report.ResumedSessionCount
	if err := writeJSON(filepath.Join(options.OutputDirectory, "ingestion.json"), report); err != nil {
		return IngestionReport{}, err
	}
	return report, nil
}

func ingestConversation(
	ctx context.Context,
	dataset Dataset,
	conversation Conversation,
	operations Operations,
	runID string,
	attempts int,
	progress Progress,
) (ConversationIngestion, error) {
	scopeID, err := ScopeID(runID, conversation.SampleID())
	if err != nil {
		return ConversationIngestion{}, err
	}
	sessions := conversation.Sessions()
	for _, session := range sessions {
		metadata := map[string]any{
			"benchmark": "locomo", "dataset_sha256": dataset.SHA256(),
			"sample_id": conversation.SampleID(), "session_id": session.ID(), "date_time": session.DateTime(),
		}
		if _, captureErr := operations.Capture(ctx, scopeID, session.ID(), RenderSession(conversation, session), metadata); captureErr != nil {
			return ConversationIngestion{}, captureErr
		}
	}
	page, err := operations.List(ctx, scopeID)
	if err != nil {
		return ConversationIngestion{}, err
	}
	var previousRevision *int64
	if page.MemoryRef != nil {
		revision := page.MemoryRef.Revision()
		previousRevision = &revision
	}
	result := ConversationIngestion{ScopeID: scopeID, SessionCount: len(sessions)}
	latencies := make([]float64, 0, len(sessions))
	cursor := int64(-1)
	for {
		flushStarted := time.Now()
		flush, retries, flushErr := retryTransient(ctx, attempts, func(ctx context.Context) (pcruntime.MemoryFlushResult, error) {
			return operations.Flush(ctx, scopeID)
		})
		if flushErr != nil {
			return ConversationIngestion{}, flushErr
		}
		result.transientRetries += retries
		if cursor < 0 {
			cursor = flush.PreviousCursor
			result.resumedSessions = int(cursor)
		}
		if flush.PreviousCursor != cursor || flush.HighWatermark > int64(len(sessions)) {
			return ConversationIngestion{}, fmt.Errorf("scope %s contains an unexpected Source cursor", scopeID)
		}
		if flush.ProcessedSourceCount == 0 {
			if flush.CurrentCursor != flush.HighWatermark {
				return ConversationIngestion{}, fmt.Errorf("scope %s did not advance its pending Source", scopeID)
			}
			break
		}
		if flush.ProcessedSourceCount != 1 || flush.CurrentCursor != cursor+1 {
			return ConversationIngestion{}, fmt.Errorf("scope %s did not advance exactly one Source", scopeID)
		}
		latencies = append(latencies, float64(time.Since(flushStarted))/float64(time.Millisecond))
		currentRevision := (*int64)(nil)
		if flush.MemoryRef != nil {
			revision := flush.MemoryRef.Revision()
			currentRevision = &revision
		}
		if equalInt64Pointer(currentRevision, previousRevision) {
			result.unchangedFlushes++
		}
		previousRevision = currentRevision
		cursor = flush.CurrentCursor
		progress(fmt.Sprintf("[ingest] %s %d/%d", conversation.SampleID(), cursor, len(sessions)))
		if cursor == flush.HighWatermark {
			break
		}
	}
	page, err = operations.List(ctx, scopeID)
	if err != nil {
		return ConversationIngestion{}, err
	}
	result.MemoryEntryCount = len(page.Entries)
	if page.MemoryRef != nil {
		revision := page.MemoryRef.Revision()
		result.MemoryRevision = &revision
	}
	result.FlushLatencyMSP50 = Percentile(latencies, .5)
	result.FlushLatencyMSP95 = Percentile(latencies, .95)
	return result, nil
}
