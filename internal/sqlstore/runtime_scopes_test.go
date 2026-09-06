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
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/runtime"
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
	created, err := store.Create(t.Context(), "scope-runtime", draft, func([]scope.Descriptor) error { return nil })
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

func TestScopeApplicationSQLiteValidatesRelationshipsBeforeMetadataCAS(t *testing.T) {
	application := scopeApplication(t, openTestDatabase(t), "scope")
	root := createScope(t, application, "root", "", nil)
	child := createScope(t, application, "child", root.ID(), nil)
	for _, test := range []struct {
		name       string
		parent     string
		references []string
		notFound   bool
	}{
		{name: "self parent", parent: root.ID()},
		{name: "parent cycle", parent: child.ID()},
		{name: "missing parent", parent: "missing-secret", notFound: true},
		{name: "self reference", references: []string{root.ID()}},
		{name: "missing reference", references: []string{"missing-secret"}, notFound: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutation := scopeMutation(t, root.Version(), test.parent, test.references)
			_, updateErr := application.Update(t.Context(), root.ID(), mutation)
			if test.notFound {
				if _, found := errors.AsType[*scope.NotFoundError](updateErr); !found {
					t.Fatalf("missing relationship error = %T %v", updateErr, updateErr)
				}
			} else if _, relationship := errors.AsType[*scope.RelationshipError](updateErr); !relationship {
				t.Fatalf("relationship error = %T %v", updateErr, updateErr)
			}
			if strings.Contains(fmt.Sprintf("%v %#v", updateErr, updateErr), "missing-secret") {
				t.Fatal("relationship error exposed Scope identity")
			}
			loaded, getErr := application.Get(t.Context(), root.ID())
			if getErr != nil || !reflect.DeepEqual(loaded, root) {
				t.Fatalf("rejected mutation changed Scope: %#v, %v", loaded, getErr)
			}
		})
	}
	updated, err := application.Update(t.Context(), child.ID(), scopeMutation(t, child.Version(), "", []string{root.ID()}))
	if err != nil || updated.Version() != 2 || updated.ParentScopeID() != "" || !slices.Equal(updated.ContextReferences(), []string{root.ID()}) {
		t.Fatalf("valid metadata replacement = %#v, %v", updated, err)
	}
	_, err = application.Update(t.Context(), child.ID(), scopeMutation(t, child.Version(), "missing-secret", nil))
	conflict, stale := errors.AsType[*scope.VersionConflictError](err)
	if !stale || conflict.Expected != 1 || conflict.Actual != 2 {
		t.Fatalf("stale metadata error = %T %v", err, err)
	}
}

func TestScopeApplicationSQLiteSelectsHierarchyWithoutImplicitSharing(t *testing.T) {
	application := scopeApplication(t, openTestDatabase(t), "scope")
	root := createScope(t, application, "root", "", nil)
	referenced := createScope(t, application, "referenced", "", nil)
	child := createScope(t, application, "child", root.ID(), []string{referenced.ID()})
	grandchild := createScope(t, application, "grandchild", child.ID(), nil)
	sibling := createScope(t, application, "sibling", root.ID(), nil)
	exact, err := scope.NewExactSelection([]string{sibling.ID(), child.ID()})
	if err != nil {
		t.Fatal(err)
	}
	subtree, err := scope.NewSubtreeSelection(root.ID())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		selection scope.Selection
		want      []string
	}{
		{name: "all", selection: scope.AllSelection(), want: []string{root.ID(), referenced.ID(), child.ID(), grandchild.ID(), sibling.ID()}},
		{name: "exact", selection: exact, want: []string{child.ID(), sibling.ID()}},
		{name: "subtree breadth first", selection: subtree, want: []string{root.ID(), child.ID(), sibling.ID(), grandchild.ID()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected, selectionErr := application.ResolveSelection(t.Context(), test.selection)
			if selectionErr != nil || !slices.Equal(scopeIDs(selected), test.want) {
				t.Fatalf("selection = %v, %v", scopeIDs(selected), selectionErr)
			}
		})
	}
	if !slices.Equal(child.ContextReferences(), []string{referenced.ID()}) || len(sibling.ContextReferences()) != 0 {
		t.Fatal("parent relationship implicitly shared context")
	}
	missingExact, err := scope.NewExactSelection([]string{root.ID(), "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	missingSubtree, err := scope.NewSubtreeSelection("unknown")
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range []scope.Selection{missingExact, missingSubtree} {
		selected, selectionErr := application.ResolveSelection(t.Context(), selection)
		if _, missing := errors.AsType[*scope.NotFoundError](selectionErr); !missing || selected != nil {
			t.Fatalf("partial or missing selection = %v, %v", selected, selectionErr)
		}
	}
	if _, selectionErr := application.ResolveSelection(t.Context(), scope.Selection{}); selectionErr == nil {
		t.Fatal("zero selection was accepted")
	}
}

func TestScopeApplicationSQLiteResolveHonorsExplicitPresenceAndCase(t *testing.T) {
	application := scopeApplication(t, openTestDatabase(t), "Scope")
	defaultScope, err := application.BootstrapDefault(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	bound := createScope(t, application, "bound", "", nil)
	explicit := createScope(t, application, "explicit", "", nil)
	key, err := scope.NewBindingKey("codex", "project", "repository")
	if err != nil {
		t.Fatal(err)
	}
	missingKey, err := scope.NewBindingKey("workbuddy", "project", "unbound")
	if err != nil {
		t.Fatal(err)
	}
	if _, bindErr := application.Bind(t.Context(), key, bound.ID()); bindErr != nil {
		t.Fatal(bindErr)
	}
	for _, test := range []struct {
		name     string
		explicit *string
		keys     []scope.BindingKey
		want     string
	}{
		{name: "explicit wins", explicit: new(explicit.ID()), keys: []scope.BindingKey{key}, want: explicit.ID()},
		{name: "first existing binding", keys: []scope.BindingKey{missingKey, key}, want: bound.ID()},
		{name: "default fallback", keys: []scope.BindingKey{missingKey}, want: defaultScope.ID()},
		{name: "explicit blank", explicit: new(""), keys: []scope.BindingKey{key}},
		{name: "explicit missing", explicit: new("missing-secret"), keys: []scope.BindingKey{key}},
		{name: "case distinct", explicit: new(strings.ToLower(explicit.ID())), keys: []scope.BindingKey{key}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, resolveErr := application.Resolve(t.Context(), test.explicit, test.keys)
			if test.want != "" {
				if resolveErr != nil || value.ID() != test.want {
					t.Fatalf("resolved Scope = %#v, %v", value, resolveErr)
				}
			} else if _, missing := errors.AsType[*scope.NotFoundError](resolveErr); !missing {
				t.Fatalf("explicit Scope silently fell back: %#v, %v", value, resolveErr)
			}
		})
	}
}

func TestScopeApplicationSQLiteConcurrentParentUpdatesRemainAcyclic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hierarchy.db")
	first := scopeApplication(t, openScopeDatabase(t, path), "first")
	second := scopeApplication(t, openScopeDatabase(t, path), "second")
	left := createScope(t, first, "left", "", nil)
	right := createScope(t, second, "right", "", nil)
	mutations := []scope.Mutation{scopeMutation(t, 1, right.ID(), nil), scopeMutation(t, 1, left.ID(), nil)}
	applications := []*runtime.ScopeApplication{first, second}
	ids := []string{left.ID(), right.ID()}
	results := make([]error, 2)
	start := make(chan struct{})
	var group sync.WaitGroup
	ctx := t.Context()
	for index, application := range applications {
		group.Go(func() {
			<-start
			_, results[index] = application.Update(ctx, ids[index], mutations[index])
		})
	}
	close(start)
	group.Wait()
	var succeeded, rejected int
	for _, err := range results {
		if err == nil {
			succeeded++
			continue
		}
		if _, relationship := errors.AsType[*scope.RelationshipError](err); !relationship {
			t.Fatalf("concurrent parent error = %T %v", err, err)
		}
		rejected++
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("parent update results = success:%d rejected:%d", succeeded, rejected)
	}
	currentLeft, err := first.Get(t.Context(), left.ID())
	if err != nil {
		t.Fatal(err)
	}
	currentRight, err := second.Get(t.Context(), right.ID())
	if err != nil {
		t.Fatal(err)
	}
	if currentLeft.ParentScopeID() == right.ID() && currentRight.ParentScopeID() == left.ID() {
		t.Fatal("concurrent writes persisted a parent cycle")
	}
}

func TestScopeApplicationSQLiteConcurrentCreateAndBootstrap(t *testing.T) {
	for _, operation := range []string{"same draft", "conflicting draft", "bootstrap"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "creation.db")
			applications := []*runtime.ScopeApplication{
				scopeApplication(t, openScopeDatabase(t, path), "first"),
				scopeApplication(t, openScopeDatabase(t, path), "second"),
			}
			drafts := make([]scope.Draft, 2)
			for index := range drafts {
				title := "same"
				if operation == "conflicting draft" {
					title = fmt.Sprintf("title-%d", index)
				}
				var draftErr error
				drafts[index], draftErr = scope.NewDraft(title, "summary", "", nil, nil, "shared-key")
				if draftErr != nil {
					t.Fatal(draftErr)
				}
			}
			values := make([]scope.Descriptor, 2)
			results := make([]error, 2)
			start := make(chan struct{})
			var group sync.WaitGroup
			ctx := t.Context()
			for index, application := range applications {
				group.Go(func() {
					<-start
					if operation == "bootstrap" {
						values[index], results[index] = application.BootstrapDefault(ctx)
					} else {
						values[index], results[index] = application.Create(ctx, drafts[index])
					}
				})
			}
			close(start)
			group.Wait()
			var succeeded, conflicts int
			for _, result := range results {
				if result == nil {
					succeeded++
					continue
				}
				if _, conflict := errors.AsType[*scope.IdempotencyConflictError](result); !conflict {
					t.Fatalf("concurrent creation error = %T %v", result, result)
				}
				conflicts++
			}
			if operation == "conflicting draft" {
				if succeeded != 1 || conflicts != 1 {
					t.Fatalf("conflicting create results = success:%d conflict:%d", succeeded, conflicts)
				}
			} else if succeeded != 2 || values[0].ID() != values[1].ID() {
				t.Fatalf("idempotent create results = %#v, %v", values, results)
			}
			all, listErr := applications[0].ResolveSelection(t.Context(), scope.AllSelection())
			if listErr != nil || len(all) != 1 {
				t.Fatalf("creation produced duplicate Scopes = %#v, %v", all, listErr)
			}
			if operation == "bootstrap" {
				resolved, resolveErr := applications[1].Resolve(t.Context(), nil, nil)
				if resolveErr != nil || resolved.ID() != all[0].ID() || resolved.Title() != "Default" || resolved.Summary() != "Default context" {
					t.Fatalf("bootstrapped default = %#v, %v", resolved, resolveErr)
				}
			}
		})
	}
}

func TestScopeApplicationSQLiteBootstrapPreservesDefaultAndStableCreationKey(t *testing.T) {
	application := scopeApplication(t, openTestDatabase(t), "scope")
	if _, err := application.Resolve(t.Context(), nil, nil); err == nil {
		t.Fatal("empty database resolved a Scope")
	} else if _, missing := errors.AsType[*scope.BindingNotFoundError](err); !missing {
		t.Fatalf("no default error = %T %v", err, err)
	}
	draft, err := scope.NewDraft("Custom", "Customized context", "", nil, nil, "powercontext.default-scope.v1")
	if err != nil {
		t.Fatal(err)
	}
	custom, err := application.Create(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	if _, bootstrapErr := application.BootstrapDefault(t.Context()); bootstrapErr == nil {
		t.Fatal("bootstrap overwrote a conflicting reserved creation key")
	} else if _, conflict := errors.AsType[*scope.IdempotencyConflictError](bootstrapErr); !conflict {
		t.Fatalf("bootstrap conflict = %T %v", bootstrapErr, bootstrapErr)
	}
	if _, resolveErr := application.Resolve(t.Context(), nil, nil); resolveErr == nil {
		t.Fatal("conflicting bootstrap assigned a default")
	}
	if _, setErr := application.SetDefault(t.Context(), custom.ID()); setErr != nil {
		t.Fatal(setErr)
	}
	existing, err := application.BootstrapDefault(t.Context())
	if err != nil || !reflect.DeepEqual(existing, custom) {
		t.Fatalf("bootstrap replaced the existing default = %#v, %v", existing, err)
	}
	all, err := application.ResolveSelection(t.Context(), scope.AllSelection())
	if err != nil || len(all) != 1 {
		t.Fatalf("bootstrap created another Scope = %#v, %v", all, err)
	}
}

func TestScopeApplicationSQLiteBootstrapRollsBackCreationWhenDefaultWriteFails(t *testing.T) {
	database := openTestDatabase(t)
	application := scopeApplication(t, database, "scope")
	if _, err := database.SQLDB().ExecContext(t.Context(), `CREATE TRIGGER reject_scope_default
        BEFORE INSERT ON pc_scope_settings BEGIN SELECT RAISE(ABORT, 'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := application.BootstrapDefault(t.Context()); err == nil {
		t.Fatal("bootstrap ignored a failed default write")
	}
	all, err := application.ResolveSelection(t.Context(), scope.AllSelection())
	if err != nil || len(all) != 0 {
		t.Fatalf("failed bootstrap left its Scope behind = %#v, %v", all, err)
	}
	if _, dropErr := database.SQLDB().ExecContext(t.Context(), `DROP TRIGGER reject_scope_default`); dropErr != nil {
		t.Fatal(dropErr)
	}
	created, err := application.BootstrapDefault(t.Context())
	if err != nil {
		t.Fatalf("retry could not recover the creation key: %v", err)
	}
	resolved, err := application.Resolve(t.Context(), nil, nil)
	if err != nil || resolved.ID() != created.ID() {
		t.Fatalf("retried bootstrap = %#v, %v", resolved, err)
	}
}

func TestScopeApplicationSQLiteCancellationPreservesState(t *testing.T) {
	application := scopeApplication(t, openTestDatabase(t), "scope")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := application.BootstrapDefault(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled bootstrap error = %T %v", err, err)
	}
	all, err := application.ResolveSelection(t.Context(), scope.AllSelection())
	if err != nil || len(all) != 0 {
		t.Fatalf("canceled bootstrap changed state = %#v, %v", all, err)
	}
}

func scopeApplication(t *testing.T, database *sqlstore.Database, prefix string) *runtime.ScopeApplication {
	t.Helper()
	store, err := sqlstore.NewRuntimeScopeStore(database, sqlstore.ScopeRepository{})
	if err != nil {
		t.Fatal(err)
	}
	var sequence atomic.Int64
	application, err := runtime.NewScopeApplication(runtime.New(), store, func() string { return fmt.Sprintf("%s-%d", prefix, sequence.Add(1)) })
	if err != nil {
		t.Fatal(err)
	}
	return application
}

func createScope(t *testing.T, application *runtime.ScopeApplication, key, parent string, references []string) scope.Descriptor {
	t.Helper()
	draft, err := scope.NewDraft(key, "summary", parent, references, nil, key)
	if err != nil {
		t.Fatal(err)
	}
	created, err := application.Create(t.Context(), draft)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func scopeMutation(t *testing.T, version int64, parent string, references []string) scope.Mutation {
	t.Helper()
	mutation, err := scope.NewMutation(version, "Updated", "Updated summary", parent, references, nil)
	if err != nil {
		t.Fatal(err)
	}
	return mutation
}

func scopeIDs(values []scope.Descriptor) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.ID())
	}
	return result
}

func openScopeDatabase(t *testing.T, path string) *sqlstore.Database {
	t.Helper()
	database, err := sqlstore.OpenSQLite(t.Context(), sqlstore.DefaultSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	return database
}
