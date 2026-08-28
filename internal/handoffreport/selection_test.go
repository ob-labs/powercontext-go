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

package handoffreport_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/internal/handoffreport"
	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
)

func TestOptimisticSelectionFreezesStableHeadsInScopeOrder(t *testing.T) {
	t.Parallel()
	a, b := reportHandoff(t, 2), reportHandoff(t, 4)
	reader := newSelectionReader(map[string][]*handoff.Handoff{
		"scope-a": {&a},
		"scope-b": {&b},
	})
	selected, err := handoffreport.SelectOptimisticStable(
		context.Background(),
		reader,
		[]handoffreport.WorkstreamDescriptor{
			selectionWorkstream(t, "scope-b", 3),
			selectionWorkstream(t, "scope-a", 2),
		},
		handoffreport.DefaultSelectionAttempts,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reader.calls, []string{"scope-a", "scope-b", "scope-a", "scope-b"}) {
		t.Fatalf("read order = %v", reader.calls)
	}
	if len(selected) != 2 || selected[0].ScopeID() != "scope-a" || selected[1].ScopeID() != "scope-b" ||
		selected[0].WorkstreamRevision() != 2 || selected[1].WorkstreamRevision() != 3 ||
		selected[0].HandoffRef() == nil || selected[0].HandoffRef().Revision() != 2 ||
		selected[1].HandoffRef() == nil || selected[1].HandoffRef().Revision() != 4 {
		t.Fatalf("selected = %#v", selected)
	}
}

func TestOptimisticSelectionPreservesExplicitNoHandoff(t *testing.T) {
	t.Parallel()
	reader := newSelectionReader(map[string][]*handoff.Handoff{"scope-empty": {nil}})
	selected, err := handoffreport.SelectOptimisticStable(
		context.Background(), reader,
		[]handoffreport.WorkstreamDescriptor{selectionWorkstream(t, "scope-empty", 1)},
		handoffreport.DefaultSelectionAttempts,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Status() != handoffreport.SelectionNoHandoff || selected[0].HandoffRef() != nil {
		t.Fatalf("selected = %#v", selected)
	}
}

func TestOptimisticSelectionRetriesChangeThenFreezesStableHeads(t *testing.T) {
	t.Parallel()
	one, two := reportHandoff(t, 1), reportHandoff(t, 2)
	reader := newSelectionReader(map[string][]*handoff.Handoff{
		"scope-a": {&one, &two, &two, &two},
	})
	selected, err := handoffreport.SelectOptimisticStable(
		context.Background(), reader,
		[]handoffreport.WorkstreamDescriptor{selectionWorkstream(t, "scope-a", 1)},
		handoffreport.DefaultSelectionAttempts,
	)
	if err != nil {
		t.Fatal(err)
	}
	want := two.Ref()
	if !slices.Equal(reader.calls, []string{"scope-a", "scope-a", "scope-a", "scope-a"}) ||
		len(selected) != 1 || selected[0].HandoffRef() == nil || *selected[0].HandoffRef() != want {
		t.Fatalf("calls = %v, selected = %#v", reader.calls, selected)
	}
}

func TestOptimisticSelectionRequiresStrictBoundedAttemptCount(t *testing.T) {
	t.Parallel()
	value := reportHandoff(t, 1)
	for _, attempts := range []int{-1, 0, handoffreport.MaxSelectionAttempts + 1} {
		reader := newSelectionReader(map[string][]*handoff.Handoff{"scope-a": {&value}})
		if _, err := handoffreport.SelectOptimisticStable(
			context.Background(), reader,
			[]handoffreport.WorkstreamDescriptor{selectionWorkstream(t, "scope-a", 1)}, attempts,
		); err == nil {
			t.Fatalf("attempts %d was accepted", attempts)
		}
		if len(reader.calls) != 0 {
			t.Fatalf("reader called for invalid attempts %d", attempts)
		}
	}
}

func TestRuntimeHandoffReportReaderMapsOnlyExistingReadBehaviors(t *testing.T) {
	t.Parallel()
	value := reportHandoff(t, 2)
	backend := &selectionBackend{value: value}
	service, err := handoff.NewService("scope-a", "handoff", backend, selectionResolver{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var scopes []string
	reader, err := pcruntime.NewHandoffReportReader(func(scope string) (*handoff.Service, error) {
		scopes = append(scopes, scope)
		return service, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	latest, found, err := reader.Latest(ctx, "scope-a")
	if err != nil || !found || latest.Ref() != value.Ref() {
		t.Fatalf("latest = %#v, found = %t, error = %v", latest, found, err)
	}
	exact, err := reader.Get(ctx, "scope-a", value.Ref())
	if err != nil || exact.Ref() != value.Ref() {
		t.Fatalf("exact = %#v, error = %v", exact, err)
	}
	revisions, err := reader.Revisions(ctx, "scope-a")
	if err != nil || len(revisions) != 1 || revisions[0].Ref() != value.Ref() {
		t.Fatalf("revisions = %#v, error = %v", revisions, err)
	}
	_, err = reader.CheckEvidence(ctx, "scope-a", value.Ref())
	var unavailable *handoffreport.EvidenceCheckUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("check evidence error = %#v", err)
	}
	if !slices.Equal(scopes, []string{"scope-a", "scope-a", "scope-a"}) ||
		len(backend.references) != 1 || backend.references[0] != value.Ref() {
		t.Fatalf("scopes = %v, exact references = %v", scopes, backend.references)
	}
}

type selectionReader struct {
	values  map[string][]*handoff.Handoff
	indexes map[string]int
	calls   []string
}

func newSelectionReader(values map[string][]*handoff.Handoff) *selectionReader {
	return &selectionReader{values: values, indexes: make(map[string]int)}
}

func (r *selectionReader) Latest(_ context.Context, scope string) (handoff.Handoff, bool, error) {
	r.calls = append(r.calls, scope)
	values := r.values[scope]
	index := r.indexes[scope]
	if index < len(values)-1 {
		r.indexes[scope] = index + 1
	}
	if len(values) == 0 || values[index] == nil {
		return handoff.Handoff{}, false, nil
	}
	return *values[index], true, nil
}

func (*selectionReader) Get(context.Context, string, artifact.Ref) (handoff.Handoff, error) {
	panic("unexpected exact read")
}

func (*selectionReader) Revisions(context.Context, string) ([]handoff.Handoff, error) {
	panic("unexpected revisions read")
}

func (*selectionReader) CheckEvidence(context.Context, string, artifact.Ref) ([]handoff.EvidenceCheck, error) {
	panic("unexpected evidence read")
}

type selectionBackend struct {
	value      handoff.Handoff
	references []artifact.Ref
}

func (*selectionBackend) Create(context.Context, string, handoff.ArtifactDraft) (handoff.Handoff, error) {
	panic("unexpected create")
}

func (*selectionBackend) Revise(context.Context, handoff.Handoff, handoff.ArtifactDraft) (handoff.Handoff, error) {
	panic("unexpected revise")
}

func (b *selectionBackend) Get(_ context.Context, ref artifact.Ref) (handoff.Handoff, error) {
	b.references = append(b.references, ref)
	return b.value, nil
}

func (b *selectionBackend) Latest(context.Context, string) (handoff.Handoff, bool, error) {
	return b.value, true, nil
}

func (b *selectionBackend) Revisions(context.Context, string) ([]handoff.Handoff, error) {
	return []handoff.Handoff{b.value}, nil
}

type selectionResolver struct{}

func (selectionResolver) Resolve(context.Context, handoff.Citation) (handoff.Evidence, error) {
	panic("unexpected resolve")
}

func (selectionResolver) Validate(context.Context, handoff.Citation) error {
	panic("unexpected validation")
}

func selectionWorkstream(t *testing.T, scope string, version int) handoffreport.WorkstreamDescriptor {
	t.Helper()
	value, err := handoffreport.NewWorkstreamDescriptor(
		scope, "prj-1", nil, "Workstream "+scope, handoffreport.WorkstreamFeature,
		handoffreport.CatalogIncluded, nil, nil, version,
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
