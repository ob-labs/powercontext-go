// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build sqlite_fts5

package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/benchmark/locomo"
	"github.com/ob-labs/powercontext-go/server"
)

func TestOpenBenchmarkApplicationRegistersDurableConversationScopes(t *testing.T) {
	config, err := server.DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Database.SQLite.URL = "sqlite+aiosqlite:///" + filepath.ToSlash(filepath.Join(t.TempDir(), "locomo.db"))
	config.Dashboard.Enabled = false
	config.HandoffReport.Enabled = false
	config.MCP.Enabled = false
	config.Metrics.Enabled = false

	const scopeID = "benchmark:locomo:scope-admission:sample-1"
	application, err := openBenchmarkApplication(
		t.Context(), config, locomo.RerankNone, 1, false, []string{scopeID},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if closeErr := application.Close(closeCtx); closeErr != nil {
			t.Error(closeErr)
		}
	})

	if _, err := application.operations.Capture(t.Context(), scopeID, "source-1", "benchmark capture", nil); err != nil {
		t.Fatalf("capture in registered benchmark Scope: %v", err)
	}
}
