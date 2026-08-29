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
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPythonSidecarCanBeRewrittenByGoAndRestoredByAPScheduler(t *testing.T) {
	python := os.Getenv("POWERCONTEXT_APSCHEDULER_PYTHON")
	if python == "" {
		t.Skip("set POWERCONTEXT_APSCHEDULER_PYTHON to an APScheduler 3.11.3 environment")
	}
	fixture := filepath.Join("..", "..", "test", "conformance", "testdata", "python-v0.0.2", "scheduler.db")
	contents, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	working := filepath.Join(t.TempDir(), "scheduler.db")
	if err := os.WriteFile(working, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", working)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close scheduler database: %v", err)
		}
	})
	rows, err := database.QueryContext(t.Context(), `SELECT id, next_run_time, job_state
		FROM powercontext_scheduler_jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	type replacement struct {
		id   string
		next float64
		blob []byte
	}
	var replacements []replacement
	for rows.Next() {
		var row storedJobRow
		if err := rows.Scan(&row.id, &row.next, &row.blob); err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				t.Errorf("close scheduler rows: %v", closeErr)
			}
			t.Fatal(err)
		}
		decoded, err := decodeJobState(row.blob, row.id, "/powercontext-fixtures/scheduler.db", row.next)
		if err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				t.Errorf("close scheduler rows: %v", closeErr)
			}
			t.Fatal(err)
		}
		rewritten, err := NewJob(decoded.Kind(), working, decoded.Interval(), decoded.StartDate(), decoded.NextRunTime())
		if err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				t.Errorf("close scheduler rows: %v", closeErr)
			}
			t.Fatal(err)
		}
		blob, err := encodeJobState(rewritten)
		if err != nil {
			if closeErr := rows.Close(); closeErr != nil {
				t.Errorf("close scheduler rows: %v", closeErr)
			}
			t.Fatal(err)
		}
		replacements = append(replacements, replacement{
			id: rewritten.ID(), next: unixTimestamp(rewritten.NextRunTime()), blob: blob,
		})
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range replacements {
		if _, err := transaction.ExecContext(t.Context(), `UPDATE powercontext_scheduler_jobs
			SET next_run_time = ?, job_state = ? WHERE id = ?`, value.next, value.blob, value.id); err != nil {
			_ = transaction.Rollback()
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join("..", "..", "test", "conformance", "python_scheduler_fixture.py")
	command := exec.Command(python, script, "verify", working, "--runtime-path", working)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Python rejected the Go-rewritten scheduler sidecar: %v\n%s", err, output)
	}
}

func TestGoPickleIsLoadableByFrozenPythonAPScheduler(t *testing.T) {
	python := os.Getenv("POWERCONTEXT_APSCHEDULER_PYTHON")
	if python == "" {
		t.Skip("set POWERCONTEXT_APSCHEDULER_PYTHON to an APScheduler 3.11.3 environment")
	}
	start := time.Date(2026, 8, 17, 1, 2, 3, 456_789_000, time.UTC)
	job, err := NewJob(SourceWindow, "/tmp/scheduler.db", 1500*time.Millisecond, start, start.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := encodeJobState(job)
	if err != nil {
		t.Fatal(err)
	}
	script := `
import base64, datetime, pickle, sys
import apscheduler
state = pickle.loads(base64.b64decode(sys.argv[1]))
assert state["version"] == 1
assert state["id"] == "powercontext.memory.source-window.v1"
assert state["func"] == "powercontext.builtin.runtime.scheduler:dispatch_source_windows"
assert state["args"] == ("/tmp/scheduler.db",)
assert state["kwargs"] == {}
assert state["misfire_grace_time"] is None
assert state["coalesce"] is True and state["max_instances"] == 1
assert state["trigger"].__getstate__() == {
    "version": 2,
    "timezone": datetime.timezone.utc,
    "start_date": datetime.datetime(2026, 8, 17, 1, 2, 3, 456789, tzinfo=datetime.timezone.utc),
    "end_date": None,
    "interval": datetime.timedelta(seconds=1, microseconds=500000),
    "jitter": None,
}
assert state["next_run_time"] == datetime.datetime(2026, 8, 17, 1, 2, 6, 456789, tzinfo=datetime.timezone.utc)
`
	command := exec.Command(python, "-c", script, base64.StdEncoding.EncodeToString(blob))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Python rejected Go Pickle: %v\n%s", err, output)
	}
}
