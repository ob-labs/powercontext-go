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
	"testing"

	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestRuntimeScopeStoreOwnsScopeTransactions(t *testing.T) {
	store, err := sqlstore.NewRuntimeScopeStore(openTestDatabase(t), sqlstore.ScopeRepository{})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := scope.NewDraft("title", "summary", "", nil, nil, "runtime-create")
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create(t.Context(), "scope-runtime", draft)
	if err != nil || created.ID() != "scope-runtime" {
		t.Fatalf("created Scope = %#v, %v", created, err)
	}
	if _, setDefaultErr := store.SetDefault(t.Context(), created.ID()); setDefaultErr != nil {
		t.Fatal(setDefaultErr)
	}
	key, err := scope.NewBindingKey("codex", "project", "repository")
	if err != nil {
		t.Fatal(err)
	}
	if _, bindErr := store.SetBinding(t.Context(), key, created.ID()); bindErr != nil {
		t.Fatal(bindErr)
	}
	loaded, found, err := store.Get(t.Context(), created.ID())
	if err != nil || !found || loaded.ID() != created.ID() {
		t.Fatalf("loaded Scope = %#v, %t, %v", loaded, found, err)
	}
	defaultScope, defaultFound, err := store.Default(t.Context())
	if err != nil || !defaultFound || defaultScope.ID() != created.ID() {
		t.Fatalf("default Scope = %#v, %t, %v", defaultScope, defaultFound, err)
	}
	binding, bindingFound, err := store.Binding(t.Context(), key)
	if err != nil || !bindingFound || binding.ScopeID() != created.ID() {
		t.Fatalf("binding = %#v, %t, %v", binding, bindingFound, err)
	}
}
