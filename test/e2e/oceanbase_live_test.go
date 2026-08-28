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

package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"

	"github.com/ob-labs/powercontext-go/internal/handoffreport"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestLiveOceanBaseProfileSmoke(t *testing.T) {
	url := os.Getenv("POWERCONTEXT_TEST_OCEANBASE_URL")
	if url == "" {
		t.Skip("set POWERCONTEXT_TEST_OCEANBASE_URL to a dedicated OceanBase MySQL-mode database")
	}
	config := sqlstore.OceanBaseConfig{URL: url, MaxOpenConns: 2, MaxIdleConns: 1}
	database, err := sqlstore.OpenOceanBase(context.Background(), config)
	if err != nil {
		var databaseError *mysql.MySQLError
		if errors.As(err, &databaseError) {
			t.Fatalf("OceanBase profile did not open: MySQL error %d (%s): %s", databaseError.Number, databaseError.SQLState, databaseError.Message)
		}
		t.Fatalf("OceanBase profile did not open: %T", err)
	}
	t.Cleanup(func() { _ = database.Close(context.Background()) })
	if err := database.Ping(context.Background()); err != nil {
		t.Fatal("OceanBase profile did not answer the compatibility probe")
	}

	ctx := t.Context()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	scopeID := "oceanbase-live-" + suffix
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		cursors := sqlstore.SourceCursorRepository{}
		stored, err := cursors.Save(ctx, tx, scopeID, "source_window", source.NewCursor(1), nil)
		if err != nil {
			return err
		}
		expected := stored.Generation
		_, err = cursors.Save(ctx, tx, scopeID, "source_window", source.NewCursor(2), &expected)
		return err
	}); err != nil {
		t.Fatalf("OceanBase Source cursor CAS failed: %T", err)
	}

	sourceRepository, err := sqlstore.NewSourceRepository(sqlstore.MySQLDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	content := func(id, text string) (source.ContentSource, error) {
		capture, buildErr := source.NewContentCapture(id, text, nil)
		if buildErr != nil {
			return source.ContentSource{}, buildErr
		}
		return (source.ContentAdapter{}).Resolve(ctx, capture)
	}
	if err := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		upperScope := "PC-" + suffix + "-Alpha"
		upper, buildErr := content("Turn-1", "uppercase turn")
		if buildErr != nil {
			return buildErr
		}
		if _, addErr := sourceRepository.Add(ctx, tx, upperScope, upper); addErr != nil {
			return addErr
		}
		lower, buildErr := content("turn-1", "lowercase turn")
		if buildErr != nil {
			return buildErr
		}
		second, addErr := sourceRepository.Add(ctx, tx, upperScope, lower)
		if addErr != nil {
			return addErr
		}
		if second.JournalPosition != 2 {
			return fmt.Errorf("case-variant Source position = %d, want 2", second.JournalPosition)
		}
		accent, buildErr := content("turn", "accent scope")
		if buildErr != nil {
			return buildErr
		}
		if _, addErr = sourceRepository.Add(ctx, tx, suffix+"-café", accent); addErr != nil {
			return addErr
		}
		for _, otherScope := range []string{strings.ToLower(upperScope), suffix + "-cafe"} {
			items, listErr := sourceRepository.List(ctx, tx, otherScope, 0, nil)
			if listErr != nil {
				return listErr
			}
			if len(items) != 0 {
				return fmt.Errorf("identity-variant scope %q leaked %d Sources", otherScope, len(items))
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("OceanBase binary identity collation failed: %T: %v", err, err)
	}

	reports, err := sqlstore.NewHandoffReportStore(database, sqlstore.MySQLDialect)
	if err != nil {
		t.Fatal(err)
	}
	if err := reports.EnsureSchema(ctx); err != nil {
		t.Fatalf("OceanBase Handoff Report schema failed: %T", err)
	}
	projectID := "project-" + suffix
	project, err := handoffreport.NewProjectDescriptor(
		projectID, "live-"+suffix, "OceanBase live verification", nil,
		handoffreport.LocaleEnglish, "UTC", handoffreport.CatalogIncluded, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := reports.CreateProject(ctx, project, now); err != nil {
		t.Fatalf("OceanBase Handoff Report project failed: %T", err)
	}
	event, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{
		EventID: "event-" + suffix, ProjectID: projectID,
		Source: handoffreport.ActivityOther, SourceEventID: "source-event-" + suffix,
		ObservedAt: now, TimeBasis: handoffreport.TimeHostObserved,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := reports.RecordActivity(ctx, event)
	if err != nil {
		t.Fatalf("OceanBase Handoff Report Activity failed: %T", err)
	}
	page, err := reports.ListActivities(ctx, projectID, nil, nil, nil, 0, nil, 10)
	if err != nil {
		t.Fatalf("OceanBase Handoff Report Activity page failed: %T", err)
	}
	if stored.Cursor != 1 || page.HighWatermark != 1 || len(page.Items) != 1 || page.Items[0].EventID() != event.EventID() {
		t.Fatalf("unexpected OceanBase Activity state: cursor=%d high=%d items=%d", stored.Cursor, page.HighWatermark, len(page.Items))
	}
}
