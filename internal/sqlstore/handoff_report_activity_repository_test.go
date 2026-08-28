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
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/handoffreport"
)

func TestActivityRepositoryIsIdempotentAndRejectsPayloadConflicts(t *testing.T) {
	t.Parallel()
	database, store := openActivityRepository(t)
	now := activityRepositoryTime()
	occurred := now.Add(-time.Hour)
	event := activityRepositoryEvent(t, "event-1", "project-1", handoffreport.ActivityGitCommit, "event-1", now, &occurred, handoffreport.TimeSourceReported, nil)

	var first, repeated handoffreport.StoredActivity
	err := database.Transaction(t.Context(), func(tx DBTX) error {
		var err error
		first, err = store.recordActivity(t.Context(), tx, event)
		if err != nil {
			return err
		}
		repeated, err = store.recordActivity(t.Context(), tx, event)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Cursor != 1 || repeated.Cursor != first.Cursor || repeated.Event.EventID() != "event-1" {
		t.Fatalf("idempotent records = first:%#v repeated:%#v", first, repeated)
	}
	const pythonPayload = `{"agent":null,"event_id":"event-1","evidence_refs":[],"observed_at":"2026-08-05T10:00:00Z","occurred_at":"2026-08-05T09:00:00Z","project_id":"project-1","schema":"powercontext.handoff-report-activity.v1","scope_id":null,"session_id":null,"source":"git_commit","source_event_id":"event-1","source_ref":null,"summary":null,"time_basis":"source_reported","title":null,"trust":"untrusted_observation","vcs_context":null}`
	var payload string
	if err := database.SQLDB().QueryRowContext(t.Context(), "SELECT payload FROM pc_handoff_report_activities WHERE event_id = ?", "event-1").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != pythonPayload {
		t.Fatalf("stored payload = %s\nwant Python payload = %s", payload, pythonPayload)
	}

	different := "different"
	conflicting := activityRepositoryEvent(t, "event-1", "project-1", handoffreport.ActivityGitCommit, "event-1", now, &occurred, handoffreport.TimeSourceReported, &different)
	err = database.Transaction(t.Context(), func(tx DBTX) error {
		_, err := store.recordActivity(t.Context(), tx, conflicting)
		return err
	})
	var conflict *handoffreport.ActivityEventConflictError
	if !errors.As(err, &conflict) || conflict.Source != handoffreport.ActivityGitCommit || conflict.SourceEventID != "event-1" {
		t.Fatalf("conflict error = %#v", err)
	}
}

func TestActivityRepositoryIdempotentRetryIgnoresOnlyServerOwnedIdentityAndObservationTime(t *testing.T) {
	t.Parallel()
	database, store := openActivityRepository(t)
	now := activityRepositoryTime()
	occurred := now.Add(-time.Hour)
	title := "Implemented report storage"
	firstEvent := activityRepositoryEvent(t, "server-event-1", "project-1", handoffreport.ActivityGitCommit, "git:stable", now, &occurred, handoffreport.TimeSourceReported, &title)
	retry := activityRepositoryEvent(t, "server-event-2", "project-1", handoffreport.ActivityGitCommit, "git:stable", now.Add(5*time.Minute), &occurred, handoffreport.TimeSourceReported, &title)

	var first, repeated handoffreport.StoredActivity
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		var err error
		first, err = store.recordActivity(t.Context(), tx, firstEvent)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		var err error
		repeated, err = store.recordActivity(t.Context(), tx, retry)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if repeated.Cursor != first.Cursor || repeated.Event.EventID() != "server-event-1" || !repeated.Event.ObservedAt().Equal(now) {
		t.Fatalf("idempotent retry = %#v", repeated)
	}

	differentTitle := "Different activity"
	differentOccurred := now.Add(-2 * time.Hour)
	semanticChanges := []handoffreport.ActivityEvent{
		activityRepositoryEvent(t, "server-event-2", "project-1", handoffreport.ActivityGitCommit, "git:stable", now.Add(5*time.Minute), &occurred, handoffreport.TimeSourceReported, &differentTitle),
		activityRepositoryEvent(t, "server-event-2", "project-1", handoffreport.ActivityGitCommit, "git:stable", now.Add(5*time.Minute), &differentOccurred, handoffreport.TimeSourceReported, &title),
	}
	for _, changed := range semanticChanges {
		err := database.Transaction(t.Context(), func(tx DBTX) error {
			_, err := store.recordActivity(t.Context(), tx, changed)
			return err
		})
		var conflict *handoffreport.ActivityEventConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("semantic change error = %v", err)
		}
	}
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		high, err := store.activityHighWatermark(t.Context(), tx, "project-1")
		if err == nil && high != 1 {
			t.Fatalf("high watermark = %d", high)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestActivityRepositoryRevalidatesConstructedPayloadBeforeIndexing(t *testing.T) {
	t.Parallel()
	database, store := openActivityRepository(t)
	err := database.Transaction(t.Context(), func(tx DBTX) error {
		_, err := store.recordActivity(t.Context(), tx, handoffreport.ActivityEvent{})
		return err
	})
	var invalid *handoffreport.InvalidActivityEventError
	if !errors.As(err, &invalid) {
		t.Fatalf("invalid event error = %v", err)
	}
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		var heads, activities int
		if err := tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_handoff_report_activity_heads").Scan(&heads); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pc_handoff_report_activities").Scan(&activities); err != nil {
			return err
		}
		if heads != 0 || activities != 0 {
			t.Fatalf("invalid event changed store: heads=%d activities=%d", heads, activities)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestActivityRepositorySerializesConcurrentSQLiteRecords(t *testing.T) {
	t.Parallel()
	database, store := openActivityRepository(t)
	now := activityRepositoryTime()
	const count = 12
	events := make([]handoffreport.ActivityEvent, count)
	for index := range events {
		occurred := now.Add(time.Duration(index) * time.Second)
		events[index] = activityRepositoryEvent(t, "event-"+activityDecimal(index), "project-1", handoffreport.ActivityGitCommit, "event-"+activityDecimal(index), now, &occurred, handoffreport.TimeSourceReported, nil)
	}
	type result struct {
		stored handoffreport.StoredActivity
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, count)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, event := range events {
		go func(value handoffreport.ActivityEvent) {
			<-start
			var stored handoffreport.StoredActivity
			err := database.Transaction(ctx, func(tx DBTX) error {
				var err error
				stored, err = store.recordActivity(ctx, tx, value)
				return err
			})
			results <- result{stored: stored, err: err}
		}(event)
	}
	close(start)
	cursors := make([]int64, 0, count)
	for range count {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		cursors = append(cursors, result.stored.Cursor)
	}
	slices.Sort(cursors)
	for index, cursor := range cursors {
		if cursor != int64(index+1) {
			t.Fatalf("cursors = %v", cursors)
		}
	}
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		high, err := store.activityHighWatermark(t.Context(), tx, "project-1")
		if err != nil {
			return err
		}
		items, err := store.listActivityRows(t.Context(), tx, "project-1", nil, nil, nil, 0, &high, handoffreport.MaxReportActivities+1)
		if err == nil && (high != count || len(items) != count) {
			t.Fatalf("repository state = high:%d items:%d", high, len(items))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestActivityRepositoryListsByProjectPeriodSourceAndFrozenCursor(t *testing.T) {
	t.Parallel()
	database, store := openActivityRepository(t)
	now := activityRepositoryTime()
	old := now.Add(-8 * 24 * time.Hour)
	periodGit := now.Add(-2 * 24 * time.Hour)
	periodSessionObserved := now.Add(-24 * time.Hour)
	otherOccurred := now.Add(-24 * time.Hour)
	events := []handoffreport.ActivityEvent{
		activityRepositoryEvent(t, "old", "project-1", handoffreport.ActivityGitCommit, "old", now, &old, handoffreport.TimeSourceReported, nil),
		activityRepositoryEvent(t, "period-git", "project-1", handoffreport.ActivityGitCommit, "period-git", now, &periodGit, handoffreport.TimeSourceReported, nil),
		activityRepositoryEvent(t, "period-session", "project-1", handoffreport.ActivityCodingSession, "period-session", periodSessionObserved, nil, handoffreport.TimeHostObserved, nil),
		activityRepositoryEvent(t, "current", "project-1", handoffreport.ActivityGitWorktree, "current", now, nil, handoffreport.TimeCurrentOnly, nil),
		activityRepositoryEvent(t, "other-project", "project-2", handoffreport.ActivityGitCommit, "other-project", now, &otherOccurred, handoffreport.TimeSourceReported, nil),
	}
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		for _, event := range events {
			if _, err := store.recordActivity(t.Context(), tx, event); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		frozen, err := store.activityHighWatermark(t.Context(), tx, "project-1")
		if err != nil {
			return err
		}
		start, end := now.Add(-7*24*time.Hour), now.Add(time.Second)
		page, err := store.listActivityRows(t.Context(), tx, "project-1", &start, &end, []handoffreport.ActivitySource{handoffreport.ActivityGitCommit, handoffreport.ActivityCodingSession}, 0, &frozen, handoffreport.MaxReportActivities+1)
		if err != nil {
			return err
		}
		cursorPage, err := store.listActivityRows(t.Context(), tx, "project-1", nil, nil, nil, 2, &frozen, 1)
		if err != nil {
			return err
		}
		if frozen != 4 || activityStoredIDs(page) != "period-git,period-session" || activityStoredCursors(page) != "2,3" || activityStoredIDs(cursorPage) != "period-session" {
			t.Fatalf("frozen=%d page=%#v cursorPage=%#v", frozen, page, cursorPage)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestActivityRepositoryRequiresStrictCursorAndLimitValues(t *testing.T) {
	t.Parallel()
	database, store := openActivityRepository(t)
	negative := int64(-1)
	cases := []struct {
		name, field string
		after       int64
		through     *int64
		limit       int
		sources     []handoffreport.ActivitySource
	}{
		{name: "after cursor", field: "after_cursor", after: -1, limit: 1},
		{name: "through cursor", field: "through_cursor", through: &negative, limit: 1},
		{name: "zero limit", field: "limit", limit: 0},
		{name: "large limit", field: "limit", limit: handoffreport.MaxReportActivities + 2},
		{name: "empty source", field: "source", limit: 1, sources: []handoffreport.ActivitySource{""}},
		{name: "untrimmed source", field: "source", limit: 1, sources: []handoffreport.ActivitySource{" git_commit"}},
		{name: "long source", field: "source", limit: 1, sources: []handoffreport.ActivitySource{handoffreport.ActivitySource(strings.Repeat("s", handoffreport.MaxReportActivitySourceLength+1))}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := database.Transaction(t.Context(), func(tx DBTX) error {
				_, err := store.listActivityRows(t.Context(), tx, "project-1", nil, nil, test.sources, test.after, test.through, test.limit)
				return err
			})
			var invalid *handoffreport.InvalidActivityRepositoryArgumentError
			if !errors.As(err, &invalid) || invalid.Field != test.field {
				t.Fatalf("error = %#v, want field %q", err, test.field)
			}
		})
	}
}

func TestActivityRepositoryRetentionPurgeIsProjectScopedAndCursorMonotonic(t *testing.T) {
	t.Parallel()
	database, store := openActivityRepository(t)
	now := activityRepositoryTime()
	expired := now.Add(-91 * 24 * time.Hour)
	events := []handoffreport.ActivityEvent{
		activityRepositoryEvent(t, "expired", "project-1", handoffreport.ActivityGitCommit, "expired", expired, &expired, handoffreport.TimeSourceReported, nil),
		activityRepositoryEvent(t, "kept", "project-1", handoffreport.ActivityGitCommit, "kept", now, &now, handoffreport.TimeSourceReported, nil),
		activityRepositoryEvent(t, "other-expired", "project-2", handoffreport.ActivityGitCommit, "other-expired", expired, &expired, handoffreport.TimeSourceReported, nil),
	}
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		for _, event := range events {
			if _, err := store.recordActivity(t.Context(), tx, event); err != nil {
				return err
			}
		}
		deleted, err := store.purgeActivityRows(t.Context(), tx, "project-1", now.Add(-90*24*time.Hour))
		if err != nil {
			return err
		}
		high, err := store.activityHighWatermark(t.Context(), tx, "project-1")
		if err != nil {
			return err
		}
		remaining, err := store.listActivityRows(t.Context(), tx, "project-1", nil, nil, nil, 0, &high, 50)
		if err != nil {
			return err
		}
		otherHigh, err := store.activityHighWatermark(t.Context(), tx, "project-2")
		if err != nil {
			return err
		}
		other, err := store.listActivityRows(t.Context(), tx, "project-2", nil, nil, nil, 0, &otherHigh, 50)
		if err == nil && (deleted != 1 || high != 2 || activityStoredIDs(remaining) != "kept" || activityStoredIDs(other) != "other-expired") {
			t.Fatalf("purge=%d high=%d remaining=%#v other=%#v", deleted, high, remaining, other)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHandoffReportTableNamesUseIsolatedPrefix(t *testing.T) {
	t.Parallel()
	database, _ := openActivityRepository(t)
	declared := make([]string, 0)
	for _, statement := range handoffReportSchema {
		fields := strings.Fields(statement)
		if len(fields) < 6 || fields[0] != "CREATE" || fields[1] != "TABLE" {
			continue
		}
		name := strings.TrimSuffix(fields[5], "(")
		if !strings.HasPrefix(name, "pc_handoff_report_") {
			t.Fatalf("declared table %q escaped the isolated prefix", name)
		}
		declared = append(declared, name)
	}
	if len(declared) == 0 {
		t.Fatal("Handoff Report schema declares no tables")
	}
	rows, err := database.SQLDB().QueryContext(t.Context(), "SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'pc_handoff_report_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close Handoff Report schema rows: %v", err)
		}
	}()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(declared)
	if !slices.Equal(names, declared) {
		t.Fatalf("created tables = %v, declared tables = %v", names, declared)
	}
}

func TestActivityApplicationStillRequiresCatalogProject(t *testing.T) {
	t.Parallel()
	_, store := openActivityRepository(t)
	now := activityRepositoryTime()
	event := activityRepositoryEvent(t, "event-1", "missing-project", handoffreport.ActivityGitCommit, "event-1", now, &now, handoffreport.TimeSourceReported, nil)
	_, recordErr := store.RecordActivity(t.Context(), event)
	_, listErr := store.ListActivities(t.Context(), "missing-project", nil, nil, nil, 0, nil, 50)
	_, purgeErr := store.PurgeActivities(t.Context(), "missing-project", now)
	for operation, err := range map[string]error{"record": recordErr, "list": listErr, "purge": purgeErr} {
		var missing *handoffreport.ProjectNotFoundError
		if !errors.As(err, &missing) {
			t.Fatalf("%s error = %v", operation, err)
		}
	}
}

func TestActivityRepositoryRejectsCorruptIndexedProjection(t *testing.T) {
	t.Parallel()
	database, store := openActivityRepository(t)
	now := activityRepositoryTime()
	if err := database.Transaction(t.Context(), func(tx DBTX) error {
		event := activityRepositoryEvent(t, "event-1", "project-1", handoffreport.ActivityGitCommit, "event-1", now, &now, handoffreport.TimeSourceReported, nil)
		_, err := store.recordActivity(t.Context(), tx, event)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SQLDB().ExecContext(t.Context(), "UPDATE pc_handoff_report_activities SET observed_at = ? WHERE event_id = ?", handoffreport.UTCText(now.Add(time.Hour)), "event-1"); err != nil {
		t.Fatal(err)
	}
	err := database.Transaction(t.Context(), func(tx DBTX) error {
		_, err := store.listActivityRows(t.Context(), tx, "project-1", nil, nil, nil, 0, nil, 50)
		return err
	})
	var invalid *handoffreport.InvalidStoredCatalogError
	if !errors.As(err, &invalid) || !strings.Contains(invalid.Kind, "Activity Event") {
		t.Fatalf("corrupt projection error = %v", err)
	}
}

func openActivityRepository(t *testing.T) (*Database, *HandoffReportStore) {
	t.Helper()
	config := DefaultSQLiteConfig(filepath.Join(t.TempDir(), "activity.db"))
	database, err := OpenSQLite(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	store, err := NewHandoffReportStore(database, SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	return database, store
}

func activityRepositoryEvent(
	t *testing.T,
	eventID, projectID string,
	source handoffreport.ActivitySource,
	sourceEventID string,
	observed time.Time,
	occurred *time.Time,
	basis handoffreport.TimeBasis,
	title *string,
) handoffreport.ActivityEvent {
	t.Helper()
	value, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{
		EventID: eventID, ProjectID: projectID, Source: source, SourceEventID: sourceEventID,
		OccurredAt: occurred, ObservedAt: observed.UTC(), TimeBasis: basis, Title: title,
	})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func activityRepositoryTime() time.Time {
	return time.Date(2026, time.August, 5, 10, 0, 0, 0, time.UTC)
}

func activityStoredIDs(items []handoffreport.StoredActivity) string {
	values := make([]string, len(items))
	for index, item := range items {
		values[index] = item.Event.EventID()
	}
	return strings.Join(values, ",")
}

func activityStoredCursors(items []handoffreport.StoredActivity) string {
	values := make([]string, len(items))
	for index, item := range items {
		values[index] = activityDecimal(int(item.Cursor))
	}
	return strings.Join(values, ",")
}

func activityDecimal(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
