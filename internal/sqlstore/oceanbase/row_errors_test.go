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

package oceanbase

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
)

var (
	errRowIteration = errors.New("row iteration failed")
	errRowsClose    = errors.New("rows close failed")
)

var registerRowErrorDriver = sync.OnceFunc(func() {
	sql.Register("powercontext-oceanbase-row-error", rowErrorDriver{})
})

type rowErrorDriver struct{}

func (rowErrorDriver) Open(scenario string) (driver.Conn, error) {
	return rowErrorConn{scenario: scenario}, nil
}

type rowErrorConn struct{ scenario string }

func (rowErrorConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (rowErrorConn) Close() error                        { return nil }
func (rowErrorConn) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (conn rowErrorConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &rowErrorRows{scenario: conn.scenario}, nil
}

type rowErrorRows struct {
	scenario string
	sent     bool
}

func (rowErrorRows) Columns() []string {
	return []string{"memory_artifact_id", "head_revision", "entry_id", "entry_version_id", "text"}
}

func (rows *rowErrorRows) Close() error {
	if rows.scenario == "close" {
		return errRowsClose
	}
	return nil
}

func (rows *rowErrorRows) Next(values []driver.Value) error {
	if rows.scenario == "iteration" {
		return errRowIteration
	}
	if rows.sent {
		return io.EOF
	}
	rows.sent = true
	values[0] = nil
	values[1] = int64(1)
	values[2] = "entry-1"
	values[3] = "version-1"
	values[4] = "text"
	return nil
}

func TestMemoryFTSSearchReportsIterationErrorOnce(t *testing.T) {
	database := openRowErrorDatabase(t, "iteration")
	_, err := (MemoryFTSIndex{}).Search(t.Context(), database, "scope", rowErrorSearchRequest(t))
	if !errors.Is(err, errRowIteration) {
		t.Fatalf("search error = %v", err)
	}
	if count := strings.Count(err.Error(), errRowIteration.Error()); count != 1 {
		t.Fatalf("iteration error appears %d times: %v", count, err)
	}
}

func TestMemoryFTSSearchPreservesRowsCloseError(t *testing.T) {
	database := openRowErrorDatabase(t, "close")
	_, err := (MemoryFTSIndex{}).Search(t.Context(), database, "scope", rowErrorSearchRequest(t))
	if !errors.Is(err, errRowsClose) {
		t.Fatalf("search error = %v", err)
	}
	if count := strings.Count(err.Error(), errRowsClose.Error()); count != 1 {
		t.Fatalf("rows close error appears %d times: %v", count, err)
	}
}

func openRowErrorDatabase(t *testing.T, scenario string) *sql.DB {
	t.Helper()
	registerRowErrorDriver()
	database, err := sql.Open("powercontext-oceanbase-row-error", scenario)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func rowErrorSearchRequest(t *testing.T) memory.SearchRequest {
	t.Helper()
	ref, err := artifact.NewRef(memory.Family, "memory-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	return memory.SearchRequest{
		AnalyzedQuery: "query", Memories: []artifact.Ref{ref}, CandidateLimit: 1, Mode: memory.SearchFTS,
	}
}
