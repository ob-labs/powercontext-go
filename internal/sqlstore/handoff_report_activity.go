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
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ob-labs/powercontext-go/internal/handoffreport"
)

func (s *HandoffReportStore) RecordActivity(ctx context.Context, event handoffreport.ActivityEvent) (handoffreport.StoredActivity, error) {
	if err := event.Validate(); err != nil {
		return handoffreport.StoredActivity{}, err
	}
	var stored handoffreport.StoredActivity
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		if _, err := s.getProject(ctx, tx, event.ProjectID()); err != nil {
			return err
		}
		var err error
		stored, err = s.recordActivity(ctx, tx, event)
		return err
	})
	return stored, err
}

func (s *HandoffReportStore) ListActivities(ctx context.Context, projectID string, periodStart, periodEnd *time.Time, sources []handoffreport.ActivitySource, afterCursor int64, throughCursor *int64, limit int) (handoffreport.ActivityPage, error) {
	var page handoffreport.ActivityPage
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		if limit < 1 || limit > handoffreport.MaxCatalogPageSize {
			return &handoffreport.InvalidActivityRepositoryArgumentError{Field: "limit", Detail: "must be between 1 and 100"}
		}
		if _, err := s.getProject(ctx, tx, projectID); err != nil {
			return err
		}
		high, err := s.activityHighWatermark(ctx, tx, projectID)
		if err != nil {
			return err
		}
		through := throughCursor
		if through == nil {
			through = &high
		}
		stored, err := s.listActivityRows(ctx, tx, projectID, periodStart, periodEnd, sources, afterCursor, through, limit+1)
		if err != nil {
			return err
		}
		selected := stored
		var next *int64
		if len(selected) > limit {
			selected = selected[:limit]
			if len(selected) > 0 {
				value := selected[len(selected)-1].Cursor
				next = &value
			}
		}
		items := make([]handoffreport.ActivityEvent, len(selected))
		for index, item := range selected {
			items[index] = item.Event
		}
		page = handoffreport.ActivityPage{Items: items, NextCursor: next, HighWatermark: high}
		return nil
	})
	return page, err
}

func (s *HandoffReportStore) PurgeActivities(ctx context.Context, projectID string, observedBefore time.Time) (int64, error) {
	var deleted int64
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		if _, err := s.getProject(ctx, tx, projectID); err != nil {
			return err
		}
		var err error
		deleted, err = s.purgeActivityRows(ctx, tx, projectID, observedBefore)
		return err
	})
	return deleted, err
}

func (s *HandoffReportStore) recordActivity(ctx context.Context, tx DBTX, event handoffreport.ActivityEvent) (handoffreport.StoredActivity, error) {
	if err := event.Validate(); err != nil {
		return handoffreport.StoredActivity{}, err
	}
	payload, err := marshalJSON(event)
	if err != nil {
		return handoffreport.StoredActivity{}, err
	}
	if reserveErr := s.reserveActivityWriter(ctx, tx, event.ProjectID()); reserveErr != nil {
		return handoffreport.StoredActivity{}, reserveErr
	}
	existing, found, err := s.findActivityByIdentity(ctx, tx, event.Source(), event.SourceEventID())
	if err != nil {
		return handoffreport.StoredActivity{}, err
	}
	if found {
		return idempotentActivity(existing, payload)
	}
	result, err := tx.ExecContext(ctx, quoteCursorIdentifier("UPDATE pc_handoff_report_activity_heads SET cursor = cursor + 1 WHERE project_id = ?"), event.ProjectID())
	if err != nil {
		return handoffreport.StoredActivity{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return handoffreport.StoredActivity{}, err
	}
	if affected != 1 {
		return handoffreport.StoredActivity{}, fmt.Errorf("Handoff Report Activity cursor allocator is missing")
	}
	var cursor int64
	if scanErr := tx.QueryRowContext(ctx, quoteCursorIdentifier("SELECT cursor FROM pc_handoff_report_activity_heads WHERE project_id = ?"), event.ProjectID()).Scan(&cursor); scanErr != nil {
		return handoffreport.StoredActivity{}, scanErr
	}
	if cursor < 1 {
		return handoffreport.StoredActivity{}, fmt.Errorf("invalid Handoff Report Activity cursor")
	}
	var occurred any
	if value := event.OccurredAt(); value != nil {
		occurred = handoffreport.UTCText(*value)
	}
	var period any
	if value := event.EffectivePeriodTime(); value != nil {
		period = handoffreport.UTCText(*value)
	}
	_, err = tx.ExecContext(ctx, quoteCursorIdentifier(`INSERT INTO pc_handoff_report_activities (
        project_id, cursor, event_id, scope_id, source, source_event_id,
        occurred_at, observed_at, period_at, time_basis, payload
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), event.ProjectID(), cursor, event.EventID(), reportNullableString(event.ScopeID()), event.Source(), event.SourceEventID(), occurred, handoffreport.UTCText(event.ObservedAt()), period, event.TimeBasis(), string(payload))
	if err != nil {
		existing, retryFound, findErr := s.findActivityByIdentity(ctx, tx, event.Source(), event.SourceEventID())
		if findErr == nil && retryFound {
			return idempotentActivity(existing, payload)
		}
		return handoffreport.StoredActivity{}, err
	}
	inserted, found, err := s.findActivityByProjectCursor(ctx, tx, event.ProjectID(), cursor)
	if err != nil {
		return handoffreport.StoredActivity{}, err
	}
	if !found {
		return handoffreport.StoredActivity{}, &handoffreport.InvalidStoredCatalogError{
			Kind: "Activity Event", Detail: "inserted row is not readable",
		}
	}
	return decodeActivityRow(inserted)
}

func (s *HandoffReportStore) reserveActivityWriter(ctx context.Context, tx DBTX, projectID string) error {
	statement := quoteCursorIdentifier("INSERT INTO pc_handoff_report_activity_heads (project_id, cursor) VALUES (?, 0) ON CONFLICT(project_id) DO UPDATE SET cursor = cursor")
	if s.dialect == MySQLDialect {
		statement = quoteCursorIdentifier("INSERT INTO pc_handoff_report_activity_heads (project_id, cursor) VALUES (?, 0) ON DUPLICATE KEY UPDATE cursor = cursor")
	}
	_, err := tx.ExecContext(ctx, statement, projectID)
	return err
}

type activityRow struct {
	projectID     string
	cursor        int64
	eventID       string
	scope         sql.NullString
	source        handoffreport.ActivitySource
	sourceEventID string
	occurred      sql.NullString
	observed      string
	period        sql.NullString
	basis         handoffreport.TimeBasis
	payload       []byte
}

func (s *HandoffReportStore) findActivityByIdentity(ctx context.Context, tx DBTX, source handoffreport.ActivitySource, eventID string) (activityRow, bool, error) {
	row, err := scanActivity(tx.QueryRowContext(ctx, quoteCursorIdentifier("SELECT project_id, cursor, event_id, scope_id, source, source_event_id, occurred_at, observed_at, period_at, time_basis, payload FROM pc_handoff_report_activities WHERE source = ? AND source_event_id = ?"), source, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return activityRow{}, false, nil
	}
	return row, err == nil, err
}

func (s *HandoffReportStore) findActivityByProjectCursor(ctx context.Context, tx DBTX, projectID string, cursor int64) (activityRow, bool, error) {
	row, err := scanActivity(tx.QueryRowContext(ctx, quoteCursorIdentifier("SELECT project_id, cursor, event_id, scope_id, source, source_event_id, occurred_at, observed_at, period_at, time_basis, payload FROM pc_handoff_report_activities WHERE project_id = ? AND cursor = ?"), projectID, cursor))
	if errors.Is(err, sql.ErrNoRows) {
		return activityRow{}, false, nil
	}
	return row, err == nil, err
}

type rowScanner interface{ Scan(...any) error }

func scanActivity(scanner rowScanner) (activityRow, error) {
	var row activityRow
	var payload any
	err := scanner.Scan(&row.projectID, &row.cursor, &row.eventID, &row.scope, &row.source, &row.sourceEventID, &row.occurred, &row.observed, &row.period, &row.basis, &payload)
	if err != nil {
		return row, err
	}
	row.payload, err = reportPayload(payload)
	return row, err
}

func decodeActivityRow(row activityRow) (handoffreport.StoredActivity, error) {
	if row.cursor < 1 {
		return handoffreport.StoredActivity{}, invalidStoredActivity("cursor", "must be a positive integer")
	}
	if !storedActivityTimeBasis(row.basis) {
		return handoffreport.StoredActivity{}, invalidStoredActivity("time_basis", "has an unsupported value")
	}
	var event handoffreport.ActivityEvent
	if err := unmarshalJSON(row.payload, &event); err != nil {
		return handoffreport.StoredActivity{}, invalidStoredActivity("payload", "does not match its schema")
	}
	scope := event.ScopeID()
	if event.ProjectID() != row.projectID || event.EventID() != row.eventID || event.Source() != row.source || event.SourceEventID() != row.sourceEventID || (scope == nil) != (!row.scope.Valid) || (scope != nil && *scope != row.scope.String) {
		return handoffreport.StoredActivity{}, invalidStoredActivity("identity", "does not match indexed columns")
	}
	if event.TimeBasis() != row.basis || handoffreport.UTCText(event.ObservedAt()) != row.observed || !nullableActivityTimeEqual(event.OccurredAt(), row.occurred) || !nullableActivityTimeEqual(event.EffectivePeriodTime(), row.period) {
		return handoffreport.StoredActivity{}, invalidStoredActivity("projection", "does not match indexed columns")
	}
	return handoffreport.StoredActivity{Event: event, Cursor: row.cursor}, nil
}

func idempotentActivity(row activityRow, candidate []byte) (handoffreport.StoredActivity, error) {
	existingSemantic, err := semanticActivityPayload(row.payload)
	if err != nil {
		return handoffreport.StoredActivity{}, err
	}
	candidateSemantic, err := semanticActivityPayload(candidate)
	if err != nil {
		return handoffreport.StoredActivity{}, err
	}
	if !bytes.Equal(existingSemantic, candidateSemantic) {
		return handoffreport.StoredActivity{}, &handoffreport.ActivityEventConflictError{Source: row.source, SourceEventID: row.sourceEventID}
	}
	return decodeActivityRow(row)
}

func (s *HandoffReportStore) activityHighWatermark(ctx context.Context, tx DBTX, projectID string) (int64, error) {
	if err := reportIdentifier("project_id", projectID, handoffreport.MaxReportIDLength); err != nil {
		return 0, activityRepositoryError(err)
	}
	var value int64
	err := tx.QueryRowContext(ctx, quoteCursorIdentifier("SELECT cursor FROM pc_handoff_report_activity_heads WHERE project_id = ?"), projectID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, invalidStoredActivity("cursor", "must be non-negative")
	}
	return value, nil
}

func (s *HandoffReportStore) listActivityRows(ctx context.Context, tx DBTX, projectID string, start, end *time.Time, sources []handoffreport.ActivitySource, after int64, through *int64, limit int) (result []handoffreport.StoredActivity, returnErr error) {
	if err := reportIdentifier("project_id", projectID, handoffreport.MaxReportIDLength); err != nil {
		return nil, activityRepositoryError(err)
	}
	if after < 0 {
		return nil, &handoffreport.InvalidActivityRepositoryArgumentError{Field: "after_cursor", Detail: "must be at least 0"}
	}
	if through != nil && *through < 0 {
		return nil, &handoffreport.InvalidActivityRepositoryArgumentError{Field: "through_cursor", Detail: "must be at least 0"}
	}
	if limit < 1 || limit > handoffreport.MaxReportActivities+1 {
		return nil, &handoffreport.InvalidActivityRepositoryArgumentError{Field: "limit", Detail: fmt.Sprintf("must be between 1 and %d", handoffreport.MaxReportActivities+1)}
	}
	if start != nil && end != nil && !start.Before(*end) {
		return nil, &handoffreport.InvalidActivityRepositoryArgumentError{Field: "period", Detail: "start must be before end"}
	}
	sources = duplicateStrings(sources)
	for _, source := range sources {
		if err := reportIdentifier("source", string(source), handoffreport.MaxReportActivitySourceLength); err != nil {
			return nil, activityRepositoryError(err)
		}
	}
	if sources != nil && len(sources) == 0 {
		return []handoffreport.StoredActivity{}, nil
	}
	query := quoteCursorIdentifier("SELECT project_id, cursor, event_id, scope_id, source, source_event_id, occurred_at, observed_at, period_at, time_basis, payload FROM pc_handoff_report_activities WHERE project_id = ? AND cursor > ?")
	args := []any{projectID, after}
	if through != nil {
		query += " AND `cursor` <= ?"
		args = append(args, *through)
	}
	if start != nil {
		query += " AND period_at >= ?"
		args = append(args, handoffreport.UTCText(*start))
	}
	if end != nil {
		query += " AND period_at < ?"
		args = append(args, handoffreport.UTCText(*end))
	}
	if sources != nil {
		query += " AND source IN (" + strings.TrimSuffix(strings.Repeat("?,", len(sources)), ",") + ")"
		for _, source := range sources {
			args = append(args, source)
		}
	}
	query += " ORDER BY `cursor` LIMIT ?"
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	result = []handoffreport.StoredActivity{}
	for rows.Next() {
		row, err := scanActivity(rows)
		if err != nil {
			return nil, err
		}
		stored, err := decodeActivityRow(row)
		if err != nil {
			return nil, err
		}
		result = append(result, stored)
	}
	return result, rows.Err()
}

func (s *HandoffReportStore) purgeActivityRows(ctx context.Context, tx DBTX, projectID string, observedBefore time.Time) (int64, error) {
	if err := reportIdentifier("project_id", projectID, handoffreport.MaxReportIDLength); err != nil {
		return 0, activityRepositoryError(err)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM pc_handoff_report_activities WHERE project_id = ? AND observed_at < ?", projectID, handoffreport.UTCText(observedBefore))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func storedActivityTimeBasis(value handoffreport.TimeBasis) bool {
	switch value {
	case handoffreport.TimeSourceReported, handoffreport.TimeHostObserved, handoffreport.TimeFirstSeen,
		handoffreport.TimeCurrentOnly, handoffreport.TimeUnknown:
		return true
	default:
		return false
	}
}

func nullableActivityTimeEqual(value *time.Time, column sql.NullString) bool {
	if value == nil {
		return !column.Valid
	}
	return column.Valid && handoffreport.UTCText(*value) == column.String
}

func invalidStoredActivity(field, detail string) error {
	return &handoffreport.InvalidStoredCatalogError{Kind: "Activity Event " + field, Detail: detail}
}

func activityRepositoryError(err error) error {
	var argument *handoffreport.CatalogArgumentError
	if errors.As(err, &argument) {
		return &handoffreport.InvalidActivityRepositoryArgumentError{Field: argument.Field, Detail: argument.Detail}
	}
	return err
}
