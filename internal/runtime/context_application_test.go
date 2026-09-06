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

package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/contextpack"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

func TestContextApplicationRecallsExperienceWithinOneScopedOperation(t *testing.T) {
	lifecycle := New()
	t.Cleanup(func() {
		if err := lifecycle.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ref, err := artifact.NewRef(experience.Family, "experience-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	content, err := experience.NewContent("situation", "action", "outcome", "lesson")
	if err != nil {
		t.Fatal(err)
	}
	called := false
	recall := ExperienceRecallFunc(func(_ context.Context, scopeID, query string, limit int) ([]experience.SearchHit, error) {
		called = true
		if scopeID != "scope-context" || query != "what happened" || limit != contextpack.ExperienceCandidateLimit {
			t.Fatalf("recall arguments = (%q, %q, %d)", scopeID, query, limit)
		}
		return []experience.SearchHit{{ArtifactRef: ref, Content: content}}, nil
	})
	application, err := NewContextApplication(lifecycle, nil, recall)
	if err != nil {
		t.Fatal(err)
	}
	request, err := contextpack.NewRequest("what happened", 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := application.Prepare(context.Background(), "scope-context", request)
	if err != nil {
		t.Fatal(err)
	}
	if !called || prepared.Status() != contextpack.Ready {
		t.Fatalf("recall called = %t, status = %q", called, prepared.Status())
	}
	if value := prepared.Content(); value == nil || !strings.Contains(*value, `"kind":"experience"`) {
		t.Fatalf("prepared content = %v", value)
	}
}

func TestContextApplicationRequiresSharedRuntime(t *testing.T) {
	first := New()
	second := New()
	memoryApplication, err := NewMemoryApplication(first, func(string) (*memory.Service, error) {
		return nil, nil
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewContextApplication(second, memoryApplication, nil); err == nil {
		t.Fatal("Context application accepted a Memory application owned by another Runtime")
	}
}

func TestContextApplicationRecallsCurrentAndDirectReferencesWithProvenance(t *testing.T) {
	t.Parallel()
	current, err := scope.NewDescriptor("current", "current", "summary", "parent", []string{"direct-a", "direct-b"}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	directA, err := scope.NewDescriptor("direct-a", "direct-a", "summary", "", []string{"indirect"}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	directB, err := scope.NewDescriptor("direct-b", "direct-b", "summary", "", nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]scope.Descriptor{
		"current": current, "direct-a": directA, "direct-b": directB,
	}
	var lookedUp, recalled []string
	lifecycle, err := NewConfigured(RuntimeOptions{
		ScopeReader: ScopeReaderFunc(func(_ context.Context, id string) (scope.Descriptor, bool, error) {
			lookedUp = append(lookedUp, id)
			value, found := scopes[id]
			return value, found, nil
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	recall := ExperienceRecallFunc(func(_ context.Context, scopeID, _ string, _ int) ([]experience.SearchHit, error) {
		recalled = append(recalled, scopeID)
		return []experience.SearchHit{testContextExperienceHit(t, scopeID)}, nil
	})
	application, err := NewContextApplication(lifecycle, nil, recall)
	if err != nil {
		t.Fatal(err)
	}
	request, err := contextpack.NewRequest("direct references", 0)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := application.Prepare(t.Context(), "current", request)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"current", "direct-a", "direct-b"}; !reflect.DeepEqual(lookedUp, want) {
		t.Fatalf("Scope lookups = %v, want %v", lookedUp, want)
	}
	if want := []string{"current", "direct-a", "direct-b"}; !reflect.DeepEqual(recalled, want) {
		t.Fatalf("recalled Scopes = %v, want %v", recalled, want)
	}
	content := prepared.Content()
	if content == nil || !strings.Contains(*content, `"artifact_ref":{"family":"experience","artifact_id":"current","revision":1}`) ||
		!strings.Contains(*content, `"artifact":{"scope_id":"direct-a","artifact":{"family":"experience","artifact_id":"direct-a","revision":1}}`) {
		t.Fatalf("prepared Context did not preserve current and referenced provenance: %v", content)
	}
	if strings.Contains(*content, "parent") || strings.Contains(*content, "indirect") {
		t.Fatalf("prepared Context traversed a parent or indirect reference: %q", *content)
	}
}

func TestContextApplicationRejectsMissingDirectReferenceBeforeRecall(t *testing.T) {
	t.Parallel()
	current, err := scope.NewDescriptor("current", "current", "summary", "", []string{"direct", "missing"}, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := scope.NewDescriptor("direct", "direct", "summary", "", nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewConfigured(RuntimeOptions{
		ScopeReader: ScopeReaderFunc(func(_ context.Context, id string) (scope.Descriptor, bool, error) {
			if id == current.ID() {
				return current, true, nil
			}
			if id == direct.ID() {
				return direct, true, nil
			}
			return scope.Descriptor{}, false, nil
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	application, err := NewContextApplication(lifecycle, nil, ExperienceRecallFunc(func(context.Context, string, string, int) ([]experience.SearchHit, error) {
		called = true
		return nil, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, err := contextpack.NewRequest("missing reference", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.Prepare(t.Context(), current.ID(), request)
	var missing *scope.NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want Scope NotFoundError", err)
	}
	if called {
		t.Fatal("missing direct Scope reached recall")
	}
}

func testContextExperienceHit(t *testing.T, id string) experience.SearchHit {
	t.Helper()
	ref, err := artifact.NewRef(experience.Family, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	content, err := experience.NewContent("situation", "action", "outcome", "lesson")
	if err != nil {
		t.Fatal(err)
	}
	return experience.SearchHit{ArtifactRef: ref, Content: content}
}
