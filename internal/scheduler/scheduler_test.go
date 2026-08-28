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

package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func TestSchedulerPersistsOneStableSourceWindowJob(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runtime.db")
	interval := time.Hour
	now := time.Date(2026, time.August, 17, 1, 2, 3, 456000000, time.UTC)
	config := Config{
		Path: path, SourceWindowInterval: &interval, SourceWindow: noOpProcessor,
		Clock: func() time.Time { return now },
	}
	first, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := storedRows(t, path)
	if len(before) != 1 || before[0].id != SourceWindowJobID {
		t.Fatalf("stored jobs = %#v", before)
	}

	restored, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := storedRows(t, path)
	if len(after) != 1 || after[0].id != before[0].id || after[0].next != before[0].next ||
		string(after[0].blob) != string(before[0].blob) {
		t.Fatalf("stable source-window job changed across restart: before=%#v after=%#v", before, after)
	}
}

func TestSchedulerCreatesMissingDatabaseParentDirectory(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "missing", "nested")
	path := filepath.Join(parent, "scheduler.db")
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("parent unexpectedly exists: %v", err)
	}
	interval := time.Hour
	scheduler, err := Open(context.Background(), Config{
		Path: path, SourceWindowInterval: &interval, SourceWindow: noOpProcessor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("scheduler sidecar = %#v, %v", info, err)
	}
	if err := scheduler.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerPersistsStableJobAndReconcilesDisabledJob(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runtime.db")
	hour, twoHours := time.Hour, 2*time.Hour
	first, err := Open(context.Background(), Config{
		Path: path, SourceWindowInterval: &hour, ExperienceIncubationInterval: &twoHours,
		SourceWindow: noOpProcessor, ExperienceIncubation: noOpProcessor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := storedRows(t, path)
	if len(before) != 2 {
		t.Fatalf("stored jobs = %d", len(before))
	}

	restored, err := Open(context.Background(), Config{
		Path: path, SourceWindowInterval: &hour, ExperienceIncubationInterval: &twoHours,
		SourceWindow: noOpProcessor, ExperienceIncubation: noOpProcessor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restored.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := storedRows(t, path)
	if len(after) != len(before) {
		t.Fatalf("stable restore changed row count")
	}
	for index := range before {
		if before[index].id != after[index].id || before[index].next != after[index].next || string(before[index].blob) != string(after[index].blob) {
			t.Fatalf("unchanged schedule was rewritten: %s", before[index].id)
		}
	}

	experienceOnly, err := Open(context.Background(), Config{
		Path: path, ExperienceIncubationInterval: &twoHours, ExperienceIncubation: noOpProcessor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := experienceOnly.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := storedRows(t, path)
	if len(rows) != 1 || rows[0].id != ExperienceIncubationJobID {
		t.Fatalf("disabled job was not removed: %#v", rows)
	}
}

func TestSchedulerEnforcesOneLiveOwner(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runtime.db")
	interval := time.Hour
	_, err := Open(context.Background(), Config{
		Path: ":memory:", SourceWindowInterval: &interval, SourceWindow: noOpProcessor,
	})
	var configuration *ConfigurationError
	if !errors.As(err, &configuration) || configuration.Field != "scheduler_path" {
		t.Fatalf("expected file-backed scheduler error, got %v", err)
	}
	first, err := Open(context.Background(), Config{Path: path, SourceWindowInterval: &interval, SourceWindow: noOpProcessor})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Open(context.Background(), Config{Path: path, SourceWindowInterval: &interval, SourceWindow: noOpProcessor})
	var state *StateError
	if !errors.As(err, &state) || state.Code != "duplicate" {
		t.Fatalf("expected duplicate owner, got %v", err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), Config{Path: path, SourceWindowInterval: &interval, SourceWindow: noOpProcessor})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerActivatesExperienceIncubationInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	synctest.Test(t, func(t *testing.T) {
		interval := 5 * time.Second
		var calls atomic.Int64
		scheduler, err := Open(t.Context(), Config{
			Path: path, ExperienceIncubationInterval: &interval,
			ExperienceIncubation: func(context.Context) error {
				calls.Add(1)
				return nil
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("Experience processor ran before interval")
		}
		time.Sleep(interval)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatalf("Experience processor calls = %d, want 1", calls.Load())
		}
		if err := scheduler.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCorruptStateFailsOpenWithoutDeletingOrRewritingRow(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "runtime.db")
	interval := time.Hour
	scheduler, err := Open(context.Background(), Config{Path: path, SourceWindowInterval: &interval, SourceWindow: noOpProcessor})
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	db := openSidecar(t, path)
	malicious := []byte{0x80, 5, 0x8c, 5, 'p', 'o', 's', 'i', 'x', 0x8c, 6, 's', 'y', 's', 't', 'e', 'm', 0x93, ')', 'R', '.'}
	if _, err := db.Exec(`UPDATE powercontext_scheduler_jobs SET job_state = ? WHERE id = ?`, malicious, SourceWindowJobID); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = Open(context.Background(), Config{Path: path, SourceWindowInterval: &interval, SourceWindow: noOpProcessor})
	var invalid *InvalidJobStateError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected invalid state, got %v", err)
	}
	rows := storedRows(t, path)
	if len(rows) != 1 || string(rows[0].blob) != string(malicious) {
		t.Fatalf("corrupt state was changed during failed startup")
	}
}

func TestSchedulerTimingAndHandlerFailureIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.db")
	synctest.Test(t, func(t *testing.T) {
		interval := 5 * time.Second
		var calls, reported atomic.Int64
		scheduler, err := Open(t.Context(), Config{
			Path: path, SourceWindowInterval: &interval,
			SourceWindow: func(context.Context) error {
				current := calls.Add(1)
				if current == 1 {
					return errors.New("isolated")
				}
				return nil
			},
			OnError: func(value RunError) {
				if value.Kind == SourceWindow {
					reported.Add(1)
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if calls.Load() != 0 {
			t.Fatal("processor ran before interval")
		}
		time.Sleep(interval)
		synctest.Wait()
		if calls.Load() != 1 || reported.Load() != 1 {
			t.Fatalf("first run = calls:%d errors:%d", calls.Load(), reported.Load())
		}
		time.Sleep(interval)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("failure stopped later run: %d", calls.Load())
		}
		if err := scheduler.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

type storedJobRow struct {
	id   string
	next float64
	blob []byte
}

func storedRows(t *testing.T, path string) []storedJobRow {
	t.Helper()
	db := openSidecar(t, path)
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close scheduler database: %v", err)
		}
	}()
	rows, err := db.Query(`SELECT id, next_run_time, job_state FROM powercontext_scheduler_jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf("close scheduler rows: %v", err)
		}
	}()
	var result []storedJobRow
	for rows.Next() {
		var row storedJobRow
		if err := rows.Scan(&row.id, &row.next, &row.blob); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func openSidecar(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func noOpProcessor(context.Context) error { return nil }
