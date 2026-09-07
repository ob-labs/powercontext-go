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

package sqlstore_test

import (
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestResourceSourceCodecPreservesJSONAcrossStoredReads(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`null`, `"null"`, `""`, `{"n":1}`, `9007199254740992.0`} {
		t.Run(input, func(t *testing.T) {
			value, err := source.NewContentResource("resource-"+input, []byte(input))
			if err != nil {
				t.Fatal(err)
			}
			var stored sqlstore.StoredSource
			err = database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
				added, addErr := repository.Add(t.Context(), tx, "scope", value)
				if addErr != nil {
					return addErr
				}
				var getErr error
				stored, getErr = repository.Get(t.Context(), tx, "scope", added.Ref)
				return getErr
			})
			if err != nil {
				t.Fatal(err)
			}
			got, ok := stored.Value.(source.ContentSource)
			if !ok {
				t.Fatalf("value = %T", stored.Value)
			}
			gotJSON, err := got.ContentJSON()
			if err != nil {
				t.Fatal(err)
			}
			wantJSON, err := value.ContentJSON()
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("stored JSON = %s, want %s", gotJSON, wantJSON)
			}
			wantDigest, _ := value.ContentDigest()
			gotDigest, digestErr := got.ContentDigest()
			if digestErr != nil || gotDigest != wantDigest {
				t.Fatalf("digest = %s, %v", gotDigest, digestErr)
			}
		})
	}
}

func TestSourceResourceBackendRollsBackJournalAndRedactsIdentityConflict(t *testing.T) {
	database := openTestDatabase(t)
	repository, err := sqlstore.NewSourceRepository(sqlstore.SQLiteDialect, sqlstore.ContentSourceCodec())
	if err != nil {
		t.Fatal(err)
	}
	backend, err := sqlstore.NewRuntimeSourceBackend(database, repository)
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.NewContentResource("private-id", []byte(`null`))
	if err != nil {
		t.Fatal(err)
	}
	_, position, err := backend.CreateResource(t.Context(), "private-scope", first)
	if err != nil || position != 1 {
		t.Fatalf("first = %d, %v", position, err)
	}
	other, err := source.NewContentResource("private-id", []byte(`true`))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = backend.CreateResource(t.Context(), "private-scope", other)
	if _, ok := errors.AsType[*source.ResourceConflictError](err); !ok {
		t.Fatalf("conflict = %T %v", err, err)
	}
	if _, triggerErr := database.SQLDB().ExecContext(t.Context(), `CREATE TRIGGER refuse_source BEFORE INSERT ON pc_sources BEGIN SELECT RAISE(ABORT, 'private-diagnostic'); END`); triggerErr != nil {
		t.Fatal(triggerErr)
	}
	next, err := source.NewContentResource("next", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, createErr := backend.CreateResource(t.Context(), "private-scope", next); createErr == nil {
		t.Fatal("trigger refusal lost")
	}
	if _, dropErr := database.SQLDB().ExecContext(t.Context(), "DROP TRIGGER refuse_source"); dropErr != nil {
		t.Fatal(dropErr)
	}
	_, position, err = backend.CreateResource(t.Context(), "private-scope", next)
	if err != nil || position != 2 {
		t.Fatalf("rollback left journal hole: %d, %v", position, err)
	}
}
