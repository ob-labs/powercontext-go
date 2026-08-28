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

package source_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/source"
)

type input struct{ name string }

type capturedSource struct{ name string }

func (s capturedSource) SourceName() string { return s.name }
func (capturedSource) SourceMaterialization() source.Materialization {
	return source.Captured
}
func (capturedSource) SourceDescription() (string, bool) { return "", false }

type referencedSource struct{ name string }

func (s referencedSource) SourceName() string { return s.name }
func (referencedSource) SourceMaterialization() source.Materialization {
	return source.Referenced
}
func (referencedSource) SourceDescription() (string, bool) { return "", false }

type adapter struct{}

func (adapter) Name() string { return "capture" }
func (a adapter) Resolve(_ context.Context, value input) (capturedSource, error) {
	return capturedSource(value), nil
}

func (adapter) Read(_ context.Context, value capturedSource) (string, error) {
	return "read:" + value.name, nil
}

type backend struct{ values []source.Value }

func (b *backend) Add(_ context.Context, value source.Value) (source.Value, error) {
	b.values = append(b.values, value)
	return value, nil
}

func (b *backend) Get(_ context.Context, value source.Value) (source.Value, error) {
	for _, candidate := range b.values {
		if candidate.SourceName() == value.SourceName() {
			return candidate, nil
		}
	}
	return nil, &source.NotFoundError{Source: value}
}

func (b *backend) List(context.Context) ([]source.Value, error) {
	return append([]source.Value(nil), b.values...), nil
}

func TestCatalogRoutesByExactConcreteType(t *testing.T) {
	var registry source.Registry
	if err := source.Register(&registry, adapter{}); err != nil {
		t.Fatal(err)
	}
	catalog, err := source.NewCatalog(&backend{}, registry)
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := catalog.Resolve(context.Background(), input{name: "one"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := catalog.Ref(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if ref.Type() != "capture" || ref.ID() != "one" {
		t.Fatalf("unexpected ref: %v", ref)
	}
	read, err := catalog.Read(context.Background(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	if read != "read:one" {
		t.Fatalf("unexpected read value: %v", read)
	}

	_, err = catalog.Resolve(context.Background(), &input{name: "one"})
	var notFound *source.AdapterNotFoundError
	if !errors.As(err, &notFound) || notFound.Route != "input" {
		t.Fatalf("expected exact input adapter error, got %v", err)
	}
	_, err = catalog.Ref(referencedSource{name: "one"})
	if !errors.As(err, &notFound) || notFound.Route != "source" {
		t.Fatalf("expected exact source adapter error, got %v", err)
	}
}

func TestCatalogRejectsDuplicateRoutes(t *testing.T) {
	var registry source.Registry
	if err := source.Register(&registry, adapter{}); err != nil {
		t.Fatal(err)
	}
	err := source.Register(&registry, adapter{})
	var conflict *source.ConflictError
	if !errors.As(err, &conflict) || conflict.Field != "input_type" {
		t.Fatalf("expected input conflict, got %v", err)
	}
}

func TestCatalogGetRequiresExactPersistedValue(t *testing.T) {
	var registry source.Registry
	if err := source.Register(&registry, adapter{}); err != nil {
		t.Fatal(err)
	}
	catalog, err := source.NewCatalog(&backend{values: []source.Value{capturedSource{name: "stored"}}}, registry)
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Get(context.Background(), capturedSource{name: "requested"})
	var notFound *source.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestCatalogRejectsInvalidBackendResults(t *testing.T) {
	var registry source.Registry
	if err := source.Register(&registry, adapter{}); err != nil {
		t.Fatal(err)
	}
	invalid := (*capturedSource)(nil)
	catalog, err := source.NewCatalog(&backend{values: []source.Value{invalid}}, registry)
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.List(context.Background())
	var entry *source.InvalidEntryError
	if !errors.As(err, &entry) {
		t.Fatalf("expected invalid entry, got %v", err)
	}
}

func TestSourceServiceSupportsReadOnlyUsageFlow(t *testing.T) {
	var registry source.Registry
	if err := source.Register(&registry, adapter{}); err != nil {
		t.Fatal(err)
	}
	store := &backend{}
	catalog, err := source.NewCatalog(store, registry)
	if err != nil {
		t.Fatal(err)
	}
	service, err := source.NewService(catalog, store)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	resolved, err := service.Resolve(ctx, input{name: "session-42"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := service.Add(ctx, resolved)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(ctx)
	if err != nil || len(listed) != 1 || listed[0] != stored {
		t.Fatalf("listed = %#v, %v", listed, err)
	}
	exact, err := service.Get(ctx, stored)
	if err != nil || exact != stored {
		t.Fatalf("exact = %#v, %v", exact, err)
	}
	read, err := service.Read(ctx, exact)
	if err != nil || read != "read:session-42" {
		t.Fatalf("read = %#v, %v", read, err)
	}
	resolvedOnly, err := service.Resolve(ctx, input{name: "session-43"})
	if err != nil || resolvedOnly.SourceName() != "session-43" {
		t.Fatalf("resolved = %#v, %v", resolvedOnly, err)
	}
	listed, err = service.List(ctx)
	if err != nil || len(listed) != 1 {
		t.Fatalf("resolve changed storage: %#v, %v", listed, err)
	}
}

func TestCatalogRejectsPersistedSourcesWithoutAdapter(t *testing.T) {
	stored := capturedSource{name: "captured-event"}
	catalog, err := source.NewCatalog(&backend{values: []source.Value{stored}}, source.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.List(context.Background())
	var missing *source.AdapterNotFoundError
	if !errors.As(err, &missing) || missing.Route != "source" {
		t.Fatalf("list error = %v", err)
	}
	_, err = catalog.Get(context.Background(), stored)
	if !errors.As(err, &missing) || missing.Route != "source" {
		t.Fatalf("get error = %v", err)
	}
}

type invalidValueAdapter struct{}

func (invalidValueAdapter) Name() string { return "capture" }
func (invalidValueAdapter) Resolve(context.Context, input) (capturedSource, error) {
	return capturedSource{}, nil
}
func (invalidValueAdapter) Read(context.Context, capturedSource) (string, error) { return "", nil }

func TestAdapterResultsAndDeclarationsAreCheckedAtBoundary(t *testing.T) {
	var registry source.Registry
	if err := source.Register(&registry, invalidValueAdapter{}); err != nil {
		t.Fatal(err)
	}
	catalog, err := source.NewCatalog(&backend{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Resolve(context.Background(), input{name: "session-42"})
	var invalid *source.InvalidReferenceError
	if !errors.As(err, &invalid) || invalid.Field != "source_id" {
		t.Fatalf("invalid resolved Source error = %v", err)
	}

	var nilAdapter *pointerAdapter
	if err := source.Register(&source.Registry{}, nilAdapter); err == nil {
		t.Fatal("typed nil adapter declaration was accepted")
	}
}

type pointerAdapter struct{}

func (*pointerAdapter) Name() string { return "pointer" }
func (*pointerAdapter) Resolve(context.Context, input) (capturedSource, error) {
	return capturedSource{name: "source"}, nil
}
func (*pointerAdapter) Read(context.Context, capturedSource) (string, error) { return "", nil }

func TestSourceCompositionRejectsUnregisteredValuesBeforeStorage(t *testing.T) {
	store := &backend{}
	catalog, err := source.NewCatalog(store, source.Registry{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := source.NewService(catalog, store)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Add(context.Background(), referencedSource{name: "unknown"})
	var missing *source.AdapterNotFoundError
	if !errors.As(err, &missing) || len(store.values) != 0 {
		t.Fatalf("add error = %v, stored = %#v", err, store.values)
	}
}

func TestNewRefMatchesUnicodeCharacterLimits(t *testing.T) {
	identifier := "界"
	for range source.MaxIDLength - 1 {
		identifier += "界"
	}
	if _, err := source.NewRef("content", identifier); err != nil {
		t.Fatalf("valid rune length rejected: %v", err)
	}
	if _, err := source.NewRef("content", identifier+"界"); err == nil {
		t.Fatal("expected overlong identifier to be rejected")
	}
}

func TestNewRefRejectsFrozenPythonInformationSeparatorWhitespace(t *testing.T) {
	for _, value := range []string{"\u001c", "\u001ccontent", "content\u001f"} {
		if _, err := source.NewRef(value, "source-id"); err == nil {
			t.Fatalf("source type %q was accepted", value)
		}
		if _, err := source.NewRef("content", value); err == nil {
			t.Fatalf("source ID %q was accepted", value)
		}
	}
}
