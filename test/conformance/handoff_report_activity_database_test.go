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

package conformance_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/handoffreport"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestPythonGoPythonHandoffReportActivityDatabaseCompatibility(t *testing.T) {
	python := os.Getenv("POWERCONTEXT_ORACLE_PYTHON")
	if python == "" {
		t.Skip("POWERCONTEXT_ORACLE_PYTHON is unset; bidirectional Activity compatibility runs in the Oracle CI job")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	databasePath := filepath.Join(t.TempDir(), "handoff-report-activity.db")
	runPythonActivityFixture(t, ctx, python, "create", databasePath)

	database, err := sqlstore.OpenSQLite(ctx, sqlstore.DefaultSQLiteConfig(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	if schemaErr := store.EnsureSchema(ctx); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	project, err := handoffreport.NewProjectDescriptor(
		"project-1", "project-one", "Project One", nil,
		handoffreport.LocaleEnglish, "UTC", handoffreport.CatalogIncluded, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, createErr := store.CreateProject(ctx, project, time.Date(2026, time.August, 5, 8, 0, 0, 0, time.UTC)); createErr != nil {
		t.Fatal(createErr)
	}

	page, err := store.ListActivities(ctx, "project-1", nil, nil, nil, 0, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.HighWatermark != 1 || len(page.Items) != 1 || page.Items[0].EventID() != "event-python" {
		t.Fatalf("Go read of Python activity = %#v", page)
	}

	pythonOccurred := time.Date(2026, time.August, 5, 9, 0, 0, 123000000, time.UTC)
	pythonRetryObserved := time.Date(2026, time.August, 5, 10, 5, 0, 456000000, time.UTC)
	pythonTitle := "Python café <capture>"
	pythonRetry, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{
		EventID: "event-go-retry-for-python", ProjectID: "project-1",
		Source: handoffreport.ActivityGitCommit, SourceEventID: "git:python-stable",
		OccurredAt: &pythonOccurred, ObservedAt: pythonRetryObserved,
		TimeBasis: handoffreport.TimeSourceReported, Title: &pythonTitle,
	})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := store.RecordActivity(ctx, pythonRetry)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Cursor != 1 || repeated.Event.EventID() != "event-python" ||
		!repeated.Event.ObservedAt().Equal(time.Date(2026, time.August, 5, 10, 0, 0, 456000000, time.UTC)) {
		t.Fatalf("Go retry of Python activity = %#v", repeated)
	}

	goObserved := time.Date(2026, time.August, 5, 12, 0, 0, 654321000, time.UTC)
	goTitle := "Go café <capture>"
	goEvent, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{
		EventID: "event-go", ProjectID: "project-1",
		Source: handoffreport.ActivityCodingSession, SourceEventID: "session:go-stable",
		ObservedAt: goObserved, TimeBasis: handoffreport.TimeHostObserved, Title: &goTitle,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.RecordActivity(ctx, goEvent)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Cursor != 2 || stored.Event.EventID() != "event-go" {
		t.Fatalf("Go append = %#v", stored)
	}
	if closeErr := database.Close(ctx); closeErr != nil {
		t.Fatal(closeErr)
	}

	runPythonActivityFixture(t, ctx, python, "verify", databasePath)

	database, err = sqlstore.OpenSQLite(ctx, sqlstore.DefaultSQLiteConfig(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := database.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	}()
	store, err = sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	page, err = store.ListActivities(ctx, "project-1", nil, nil, nil, 0, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.HighWatermark != 2 || len(page.Items) != 2 {
		t.Fatalf("Go read after Python retry = %#v", page)
	}
}

func runPythonActivityFixture(t *testing.T, ctx context.Context, python, mode, databasePath string) {
	t.Helper()
	command := exec.CommandContext(ctx, python, "python_handoff_report_activity_fixture.py", mode, databasePath)
	command.Dir = "."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python Handoff Report Activity fixture %s failed: %v\n%s", mode, err, output)
	}
}
