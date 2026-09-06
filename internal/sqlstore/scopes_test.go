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
	"slices"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestScopeRepositoryPersistsMetadataDefaultAndBinding(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.ScopeRepository{}
	reference, err := scope.NewExternalReference("repository", "ob-labs/powercontext-go")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := scope.NewDraft(
		"PowerContext", "Repository context", "", nil, []scope.ExternalReference{reference}, "scope-create",
	)
	if err != nil {
		t.Fatal(err)
	}
	var created scope.Descriptor
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var createErr error
		created, createErr = repository.Create(t.Context(), tx, "scope-a", draft)
		return createErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	if created.ID() != "scope-a" || created.Version() != 1 ||
		!slices.Equal(created.ContextReferences(), []string{}) {
		t.Fatalf("created Scope = %#v", created)
	}

	key, err := scope.NewBindingKey("codex", "project", "ob-labs/powercontext-go")
	if err != nil {
		t.Fatal(err)
	}
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		if err := repository.SetDefault(t.Context(), tx, created.ID()); err != nil {
			return err
		}
		_, bindErr := repository.SetBinding(t.Context(), tx, key, created.ID())
		return bindErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}

	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		listed, listErr := repository.List(t.Context(), tx)
		if listErr != nil {
			return listErr
		}
		if len(listed) != 1 || listed[0].ID() != created.ID() {
			t.Fatalf("listed scopes = %#v", listed)
		}
		defaultScope, found, defaultErr := repository.Default(t.Context(), tx)
		if defaultErr != nil || !found || defaultScope.ID() != created.ID() {
			t.Fatalf("default Scope = %#v, %t, %v", defaultScope, found, defaultErr)
		}
		binding, bindingFound, bindingErr := repository.Binding(t.Context(), tx, key)
		if bindingErr != nil || !bindingFound || binding.ScopeID() != created.ID() {
			t.Fatalf("binding = %#v, %t, %v", binding, bindingFound, bindingErr)
		}
		return nil
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
}

func TestScopeRepositoryHonorsIdempotentCreation(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.ScopeRepository{}
	draft, err := scope.NewDraft("title", "summary", "", nil, nil, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	create := func(id string, value scope.Draft) (scope.Descriptor, error) {
		var result scope.Descriptor
		transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
			var createErr error
			result, createErr = repository.Create(t.Context(), tx, id, value)
			return createErr
		})
		return result, transactionErr
	}
	first, err := create("scope-first", draft)
	if err != nil {
		t.Fatal(err)
	}
	second, err := create("scope-second", draft)
	if err != nil || second.ID() != first.ID() {
		t.Fatalf("idempotent creation = %#v, %v", second, err)
	}
	changed, err := scope.NewDraft("changed", "summary", "", nil, nil, "same-key")
	if err != nil {
		t.Fatal(err)
	}
	_, err = create("scope-third", changed)
	var conflict *scope.IdempotencyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflicting idempotency error = %T %v", err, err)
	}
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		listed, listErr := repository.List(t.Context(), tx)
		if listErr != nil {
			return listErr
		}
		if len(listed) != 1 || listed[0].ID() != first.ID() {
			t.Fatalf("scopes after idempotency conflict = %#v", listed)
		}
		return nil
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
}

func TestScopeRepositoryUpdatesMetadataWithVersionCAS(t *testing.T) {
	database := openTestDatabase(t)
	repository := sqlstore.ScopeRepository{}
	createDraft, err := scope.NewDraft("title", "summary", "", nil, nil, "create-key")
	if err != nil {
		t.Fatal(err)
	}
	var created scope.Descriptor
	if transactionErr := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var createErr error
		created, createErr = repository.Create(t.Context(), tx, "scope-a", createDraft)
		return createErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	mutation, err := scope.NewMutation(1, "updated", "updated summary", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var updated scope.Descriptor
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		var updateErr error
		updated, updateErr = repository.Update(t.Context(), tx, created.ID(), mutation)
		return updateErr
	}); err != nil {
		t.Fatal(err)
	}
	if updated.Version() != 2 || updated.Title() != "updated" || updated.Summary() != "updated summary" {
		t.Fatalf("updated Scope = %#v", updated)
	}
	if err := database.Transaction(t.Context(), func(tx sqlstore.DBTX) error {
		_, updateErr := repository.Update(t.Context(), tx, created.ID(), mutation)
		var conflict *scope.VersionConflictError
		if !errors.As(updateErr, &conflict) {
			t.Fatalf("stale update error = %T %v", updateErr, updateErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
