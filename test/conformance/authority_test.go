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
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

const (
	authoritySHA256 = "6fbd885d4f38cfe2dca39029185db8277b5c764c6d10dfeb8e0bb01d72e129c6"
	oracleScope     = "project:python-oracle"
)

func TestPythonSQLiteAuthorityCanBeReadAndExtendedByGo(t *testing.T) {
	fixture := filepath.Join("testdata", "python-v0.0.2", "authority.db")
	contents, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(contents)); got != authoritySHA256 {
		t.Fatalf("authority fixture SHA-256 = %s, want %s", got, authoritySHA256)
	}
	working := filepath.Join(t.TempDir(), "authority.db")
	if writeErr := os.WriteFile(working, contents, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	ctx := context.Background()
	database, err := sqlstore.OpenSQLite(ctx, sqlstore.DefaultSQLiteConfig(working))
	if err != nil {
		t.Fatal(err)
	}
	sources, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := sqlstore.NewArtifactRepository(
		sqlstore.SQLiteDialect,
		sqlstore.MemoryArtifactCodec(),
		sqlstore.ExperienceArtifactCodec(),
		sqlstore.SkillArtifactCodec(),
		sqlstore.HandoffArtifactCodec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	pythonRef, _ := source.NewRef("content", "capture-python-1")
	var stored sqlstore.StoredSource
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		var getErr error
		stored, getErr = sources.Get(ctx, tx, oracleScope, pythonRef)
		return getErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	pythonSource, ok := stored.Value.(source.ContentSource)
	if !ok || pythonSource.Content() != "Use one atomic café composition boundary." || stored.JournalPosition != 1 {
		t.Fatalf("Python Source = %#v", stored)
	}

	memoryRepository, err := sqlstore.NewMemoryRepository(database, oracleScope, artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	head, err := memoryRepository.Latest(ctx, "memory")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := memoryRepository.Entries(ctx, head.Ref())
	if err != nil {
		t.Fatal(err)
	}
	if head.Revision() != 1 || len(entries) != 1 || entries[0].Text != "Use one atomic café composition boundary." {
		t.Fatalf("Python Memory = %#v, entries = %#v", head, entries)
	}

	capture, err := source.NewContentCapture(
		"capture-go-1",
		"Go wrote this row into the Python authority database.",
		map[string]any{"origin": "go"},
	)
	if err != nil {
		t.Fatal(err)
	}
	goSource, err := (source.ContentAdapter{}).Resolve(ctx, capture)
	if err != nil {
		t.Fatal(err)
	}
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		value, addErr := sources.Add(ctx, tx, oracleScope, goSource)
		if addErr == nil && value.JournalPosition != 2 {
			return fmt.Errorf("Go Source journal position = %d, want 2", value.JournalPosition)
		}
		return addErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if closeErr := database.Close(ctx); closeErr != nil {
		t.Fatal(closeErr)
	}

	python := os.Getenv("POWERCONTEXT_ORACLE_PYTHON")
	if python == "" {
		t.Log("POWERCONTEXT_ORACLE_PYTHON is unset; Python back-read is exercised by the Oracle CI job")
		return
	}
	command := exec.Command(python, "python_oracle_fixture.py", "verify", working)
	command.Dir = "."
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Python could not read the Go-extended database: %v\n%s", err, output)
	}
}
