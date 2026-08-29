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
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/stats"
)

// StatisticsRepository maintains bounded additive aggregates and reads current
// authoritative inventory. It deliberately does not cache derived totals.
type StatisticsRepository struct {
	dialect Dialect
}

func NewStatisticsRepository(dialect Dialect) (StatisticsRepository, error) {
	if dialect != SQLiteDialect && dialect != MySQLDialect {
		return StatisticsRepository{}, &InvalidRepositoryArgumentError{Field: "dialect", Detail: "unsupported database dialect"}
	}
	return StatisticsRepository{dialect: dialect}, nil
}

func (r StatisticsRepository) Inventory(
	ctx context.Context,
	db DBTX,
	scopeID string,
) (stats.InventoryCounts, error) {
	if err := requireScope(scopeID); err != nil {
		return stats.InventoryCounts{}, err
	}
	var sourcePosition any
	err := db.QueryRowContext(ctx, `SELECT position FROM pc_source_journal_heads WHERE scope_id = ?`, scopeID).Scan(&sourcePosition)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return stats.InventoryCounts{}, err
	}
	result := stats.InventoryCounts{}
	if err == nil {
		value, ok := integer(sourcePosition)
		if !ok || value < 0 {
			return stats.InventoryCounts{}, &InvalidStoredColumnError{Column: "position", Expected: "a non-negative integer"}
		}
		result.Sources = value
	}

	artifactRows, err := db.QueryContext(ctx, `SELECT family, COUNT(*) FROM pc_artifact_heads
        WHERE scope_id = ? GROUP BY family ORDER BY family`, scopeID)
	if err != nil {
		return stats.InventoryCounts{}, err
	}
	for artifactRows.Next() {
		var family string
		var total any
		if scanErr := artifactRows.Scan(&family, &total); scanErr != nil {
			return stats.InventoryCounts{}, errors.Join(scanErr, closeRows(artifactRows))
		}
		count, ok := integer(total)
		if !ok || count < 0 {
			columnErr := &InvalidStoredColumnError{Column: "artifact count", Expected: "a non-negative integer"}
			return stats.InventoryCounts{}, errors.Join(columnErr, closeRows(artifactRows))
		}
		result.Artifacts = append(result.Artifacts, stats.ArtifactCountRow{Family: family, Total: count})
	}
	if rowsErr := closeRows(artifactRows); rowsErr != nil {
		return stats.InventoryCounts{}, rowsErr
	}

	candidateRows, err := db.QueryContext(ctx, `SELECT family, status, COUNT(*) FROM pc_artifact_candidate_heads
        WHERE scope_id = ? GROUP BY family, status ORDER BY family, status`, scopeID)
	if err != nil {
		return stats.InventoryCounts{}, err
	}
	for candidateRows.Next() {
		var family, status string
		var total any
		if err := candidateRows.Scan(&family, &status, &total); err != nil {
			return stats.InventoryCounts{}, errors.Join(err, closeRows(candidateRows))
		}
		count, ok := integer(total)
		if !ok || count < 0 {
			columnErr := &InvalidStoredColumnError{Column: "candidate count", Expected: "a non-negative integer"}
			return stats.InventoryCounts{}, errors.Join(columnErr, closeRows(candidateRows))
		}
		result.Candidates = append(result.Candidates, stats.CandidateCountRow{Family: family, Status: status, Total: count})
	}
	if err := closeRows(candidateRows); err != nil {
		return stats.InventoryCounts{}, err
	}
	return result, nil
}

// MemoryEntryStates joins the latest authoritative manifest to immutable entry
// versions. It never derives inventory from the rebuildable head/search tables.
func (StatisticsRepository) MemoryEntryStates(
	ctx context.Context,
	db DBTX,
	scopeID, memoryArtifactID string,
	artifacts *ArtifactRepository,
) ([]stats.MemoryEntryStateRow, error) {
	if artifacts == nil {
		return nil, &InvalidRepositoryArgumentError{Field: "artifacts", Detail: "must not be nil"}
	}
	stored, err := artifacts.Latest(ctx, db, scopeID, memory.Family, memoryArtifactID)
	if err != nil {
		var missing *RepositoryNotFoundError
		if errors.As(err, &missing) {
			return []stats.MemoryEntryStateRow{}, nil
		}
		return nil, err
	}
	value, ok := stored.(artifact.Artifact[memory.Content])
	if !ok {
		return nil, &memory.BackendConfigurationError{Detail: "stored memory family decoded to an unexpected content type"}
	}
	manifest := value.Content().Manifest().Entries()
	if len(manifest) == 0 {
		return []stats.MemoryEntryStateRow{}, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT entry_version_id, kind FROM pc_memory_entry_versions
        WHERE scope_id = ? AND memory_artifact_id = ?`, scopeID, memoryArtifactID)
	if err != nil {
		return nil, err
	}
	kinds := make(map[string]string, len(manifest))
	for rows.Next() {
		var versionID, kind string
		if err := rows.Scan(&versionID, &kind); err != nil {
			return nil, errors.Join(err, closeRows(rows))
		}
		kinds[versionID] = kind
	}
	if err := closeRows(rows); err != nil {
		return nil, err
	}
	result := make([]stats.MemoryEntryStateRow, len(manifest))
	for index, entry := range manifest {
		kind, found := kinds[entry.EntryVersionID()]
		if !found {
			return nil, &memory.BackendConfigurationError{Detail: "Memory manifest references a missing entry version"}
		}
		result[index] = stats.MemoryEntryStateRow{Kind: kind, State: string(entry.State())}
	}
	return result, nil
}

func (r StatisticsRepository) Record(
	ctx context.Context,
	db DBTX,
	scopeID string,
	usageDate time.Time,
	purpose stats.ModelPurpose,
	operation stats.ModelOperation,
	usage inference.Usage,
) error {
	if err := requireScope(scopeID); err != nil {
		return err
	}
	if err := purpose.Validate(); err != nil {
		return err
	}
	if err := operation.Validate(); err != nil {
		return err
	}
	if usage.Requests < 0 || (usage.InputTokens != nil && *usage.InputTokens < 0) ||
		(usage.OutputTokens != nil && *usage.OutputTokens < 0) {
		return &InvalidRepositoryArgumentError{Field: "usage", Detail: "counts must be non-negative"}
	}
	if usage.Requests == 0 {
		return nil
	}
	date, err := requireDate(usageDate)
	if err != nil {
		return err
	}
	input, output := int64(0), int64(0)
	inputComplete, outputComplete := 0, 0
	if usage.InputTokens != nil {
		input, inputComplete = *usage.InputTokens, 1
	}
	if usage.OutputTokens != nil {
		output, outputComplete = *usage.OutputTokens, 1
	}
	arguments := []any{scopeID, date, string(purpose), string(operation), usage.Requests, input, output, inputComplete, outputComplete}
	var statement string
	if r.dialect == SQLiteDialect {
		statement = `INSERT INTO pc_model_usage_daily
            (scope_id, usage_date, purpose, operation, requests, input_tokens, output_tokens, input_complete, output_complete)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(scope_id, usage_date, purpose, operation) DO UPDATE SET
              requests = pc_model_usage_daily.requests + excluded.requests,
              input_tokens = pc_model_usage_daily.input_tokens + excluded.input_tokens,
              output_tokens = pc_model_usage_daily.output_tokens + excluded.output_tokens,
              input_complete = pc_model_usage_daily.input_complete AND excluded.input_complete,
              output_complete = pc_model_usage_daily.output_complete AND excluded.output_complete`
	} else {
		statement = `INSERT INTO pc_model_usage_daily
            (scope_id, usage_date, purpose, operation, requests, input_tokens, output_tokens, input_complete, output_complete)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) AS incoming
            ON DUPLICATE KEY UPDATE
              requests = pc_model_usage_daily.requests + incoming.requests,
              input_tokens = pc_model_usage_daily.input_tokens + incoming.input_tokens,
              output_tokens = pc_model_usage_daily.output_tokens + incoming.output_tokens,
              input_complete = pc_model_usage_daily.input_complete AND incoming.input_complete,
              output_complete = pc_model_usage_daily.output_complete AND incoming.output_complete`
	}
	_, err = db.ExecContext(ctx, statement, arguments...)
	return err
}

func (r StatisticsRepository) Usage(
	ctx context.Context,
	db DBTX,
	scopeID string,
	startDate, endDate time.Time,
) (result []stats.StoredModelUsage, returnErr error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	start, end, err := requireDateRange(startDate, endDate)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT usage_date, purpose, operation, requests,
        input_tokens, output_tokens, input_complete, output_complete
        FROM pc_model_usage_daily WHERE scope_id = ? AND usage_date >= ? AND usage_date <= ?
        ORDER BY usage_date, purpose, operation`, scopeID, start, end)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	result = make([]stats.StoredModelUsage, 0)
	for rows.Next() {
		var dateValue, requests, input, output, inputComplete, outputComplete any
		var purpose, operation string
		if err := rows.Scan(&dateValue, &purpose, &operation, &requests, &input, &output, &inputComplete, &outputComplete); err != nil {
			return nil, err
		}
		date, err := storedDate(dateValue)
		if err != nil {
			return nil, err
		}
		requestCount, requestOK := integer(requests)
		inputCount, inputOK := integer(input)
		outputCount, outputOK := integer(output)
		inputDone, inputDoneOK := boolean(inputComplete)
		outputDone, outputDoneOK := boolean(outputComplete)
		if !requestOK || !inputOK || !outputOK || !inputDoneOK || !outputDoneOK {
			return nil, &InvalidStoredColumnError{Column: "model usage", Expected: "valid aggregate values"}
		}
		result = append(result, stats.StoredModelUsage{
			UsageDate: date, Purpose: stats.ModelPurpose(purpose), Operation: stats.ModelOperation(operation),
			Requests: requestCount, InputTokens: inputCount, OutputTokens: outputCount,
			InputComplete: inputDone, OutputComplete: outputDone,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r StatisticsRepository) RecordRecall(
	ctx context.Context,
	db DBTX,
	scopeID string,
	usageDate time.Time,
	measurement stats.RecallTokenMeasurement,
) error {
	if err := requireScope(scopeID); err != nil {
		return err
	}
	date, err := requireDate(usageDate)
	if err != nil {
		return err
	}
	profile := measurement.Estimator()
	values := []any{
		scopeID, date, profile.EstimatorID(), profile.Version(), 1,
		boolInteger(measurement.Ready()), boolInteger(measurement.Comparable()),
		measurement.BaselineTokens(), measurement.RecalledTokens(),
	}
	var statement string
	if r.dialect == SQLiteDialect {
		statement = `INSERT INTO pc_recall_token_daily
            (scope_id, usage_date, estimator_id, estimator_version, preparations,
             ready_preparations, comparable_preparations, baseline_tokens, recalled_tokens)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(scope_id, usage_date, estimator_id, estimator_version) DO UPDATE SET
              preparations = pc_recall_token_daily.preparations + excluded.preparations,
              ready_preparations = pc_recall_token_daily.ready_preparations + excluded.ready_preparations,
              comparable_preparations = pc_recall_token_daily.comparable_preparations + excluded.comparable_preparations,
              baseline_tokens = pc_recall_token_daily.baseline_tokens + excluded.baseline_tokens,
              recalled_tokens = pc_recall_token_daily.recalled_tokens + excluded.recalled_tokens`
	} else {
		statement = `INSERT INTO pc_recall_token_daily
            (scope_id, usage_date, estimator_id, estimator_version, preparations,
             ready_preparations, comparable_preparations, baseline_tokens, recalled_tokens)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) AS incoming
            ON DUPLICATE KEY UPDATE
              preparations = pc_recall_token_daily.preparations + incoming.preparations,
              ready_preparations = pc_recall_token_daily.ready_preparations + incoming.ready_preparations,
              comparable_preparations = pc_recall_token_daily.comparable_preparations + incoming.comparable_preparations,
              baseline_tokens = pc_recall_token_daily.baseline_tokens + incoming.baseline_tokens,
              recalled_tokens = pc_recall_token_daily.recalled_tokens + incoming.recalled_tokens`
	}
	_, err = db.ExecContext(ctx, statement, values...)
	return err
}

func (r StatisticsRepository) RecallUsage(
	ctx context.Context,
	db DBTX,
	scopeID string,
	startDate, endDate time.Time,
	profile inference.TokenEstimatorProfile,
) (result []stats.StoredRecallTokenUsage, returnErr error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	start, end, err := requireDateRange(startDate, endDate)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT usage_date, preparations, ready_preparations,
        comparable_preparations, baseline_tokens, recalled_tokens
        FROM pc_recall_token_daily WHERE scope_id = ? AND usage_date >= ? AND usage_date <= ?
          AND estimator_id = ? AND estimator_version = ? ORDER BY usage_date`,
		scopeID, start, end, profile.EstimatorID(), profile.Version())
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	result = make([]stats.StoredRecallTokenUsage, 0)
	for rows.Next() {
		var dateValue, preparations, ready, comparable, baseline, recalled any
		if err := rows.Scan(&dateValue, &preparations, &ready, &comparable, &baseline, &recalled); err != nil {
			return nil, err
		}
		date, err := storedDate(dateValue)
		if err != nil {
			return nil, err
		}
		values := make([]int64, 5)
		for index, raw := range []any{preparations, ready, comparable, baseline, recalled} {
			value, ok := integer(raw)
			if !ok {
				return nil, &InvalidStoredColumnError{Column: "recall usage", Expected: "integer aggregate values"}
			}
			values[index] = value
		}
		result = append(result, stats.StoredRecallTokenUsage{
			UsageDate: date, Preparations: values[0], ReadyPreparations: values[1],
			ComparablePreparations: values[2], BaselineTokens: values[3], RecalledTokens: values[4],
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func requireDate(value time.Time) (string, error) {
	if value.IsZero() {
		return "", &InvalidRepositoryArgumentError{Field: "usage_date", Detail: "must be configured"}
	}
	return value.UTC().Format(time.DateOnly), nil
}

func requireDateRange(start, end time.Time) (string, string, error) {
	startDate, err := requireDate(start)
	if err != nil {
		return "", "", err
	}
	endDate, err := requireDate(end)
	if err != nil {
		return "", "", err
	}
	if startDate > endDate {
		return "", "", &InvalidRepositoryArgumentError{Field: "period", Detail: "start_date must not follow end_date"}
	}
	return startDate, endDate, nil
}

func storedDate(value any) (time.Time, error) {
	switch typed := value.(type) {
	case time.Time:
		return time.Date(typed.Year(), typed.Month(), typed.Day(), 0, 0, 0, 0, time.UTC), nil
	case string:
		parsed, err := time.Parse(time.DateOnly, typed)
		if err == nil {
			return parsed, nil
		}
	case []byte:
		parsed, err := time.Parse(time.DateOnly, string(typed))
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, &InvalidStoredColumnError{Column: "usage_date", Expected: "an ISO date"}
}

func boolean(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case int64:
		return typed != 0, typed == 0 || typed == 1
	case int:
		return typed != 0, typed == 0 || typed == 1
	case []byte:
		parsed, err := strconv.ParseBool(string(typed))
		if err == nil {
			return parsed, true
		}
	case string:
		parsed, err := strconv.ParseBool(typed)
		if err == nil {
			return parsed, true
		}
	}
	return false, false
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func closeRows(rows *sql.Rows) error {
	closeErr := rows.Close()
	return errors.Join(rows.Err(), closeErr)
}
