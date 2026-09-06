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
	"context"
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestRuntimeScopeReaderAdmitsOnlyPersistedScopesBeforeWriteCallback(t *testing.T) {
	database := openTestDatabase(t)
	store, err := sqlstore.NewRuntimeScopeStore(database, sqlstore.ScopeRepository{})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := scope.NewDraft("title", "summary", "", nil, nil, "scope-reader")
	if err != nil {
		t.Fatal(err)
	}
	if _, createErr := store.Create(t.Context(), "persisted-scope", draft, func([]scope.Descriptor) error { return nil }); createErr != nil {
		t.Fatal(createErr)
	}
	reader, err := sqlstore.NewRuntimeScopeReader(database, sqlstore.ScopeRepository{})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := runtime.NewConfigured(runtime.RuntimeOptions{ScopeReader: reader}, nil)
	if err != nil {
		t.Fatal(err)
	}

	called := false
	err = lifecycle.ScopedWrite(t.Context(), "missing-scope", func(context.Context, string) error {
		called = true
		return nil
	})
	var missing *scope.NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("missing Scope error = %v", err)
	}
	if called {
		t.Fatal("missing Scope reached the write callback")
	}

	if err := lifecycle.ScopedWrite(t.Context(), "persisted-scope", func(context.Context, string) error {
		called = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("persisted Scope did not reach the write callback")
	}
}
