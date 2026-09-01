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
	"errors"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/memory"
)

func TestParseSQLiteVecProjectionDimensionAcceptsOwnedSchema(t *testing.T) {
	t.Parallel()
	dimension, ok := parseSQLiteVecProjectionDimension(
		"CREATE VIRTUAL TABLE pc_memory_entry_vec USING vec0(embedding float[1536])",
	)
	if !ok || dimension != 1536 {
		t.Fatalf("dimension, ok = %d, %t", dimension, ok)
	}
}

func TestParseSQLiteVecProjectionDimensionRejectsForeignSchema(t *testing.T) {
	t.Parallel()
	for _, schema := range []string{
		"CREATE TABLE pc_memory_entry_vec (embedding BLOB)",
		"CREATE VIRTUAL TABLE pc_memory_entry_vec USING vec0(other float[3])",
		"CREATE VIRTUAL TABLE pc_memory_entry_vec USING vec0(embedding float[0])",
		"CREATE VIRTUAL TABLE another_vec USING vec0(embedding float[3])",
		"untrusted DDL with /private/path and embedding float[3]",
	} {
		if dimension, ok := parseSQLiteVecProjectionDimension(schema); ok || dimension != 0 {
			t.Fatalf("accepted schema %q as %d", schema, dimension)
		}
	}
}

func TestSQLiteVecCapabilityFailureMatchesSafeCapabilityAndRootCauses(t *testing.T) {
	t.Parallel()
	queryCause := errors.New("query cause /private/db API_KEY=secret")
	cleanupCause := errors.New("cleanup cause Memory private text")
	err := newSQLiteVecCapabilityFailure("sqlite-vec probe failed", queryCause, cleanupCause)

	capability, ok := errors.AsType[*memory.CapabilityNotSupportedError](err)
	if !ok || capability.Capability != "vector" {
		t.Fatalf("capability = %#v, err = %v", capability, err)
	}
	if !errors.Is(err, queryCause) || !errors.Is(err, cleanupCause) {
		t.Fatalf("root causes are not matchable: %v", err)
	}
	for _, secret := range []string{"/private/db", "API_KEY=secret", "Memory private text"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("safe error leaked %q: %v", secret, err)
		}
	}
}

func TestSQLiteVecProjectionDimensionMismatchDetail(t *testing.T) {
	t.Parallel()
	err := sqliteVecDimensionMismatchError(1536, 768)
	if got, want := err.Error(), "memory capability is not supported: vector (sqlite-vec projection dimension 1536 does not match configured dimension 768; rebuild the projection)"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}
