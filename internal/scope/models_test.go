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

package scope

import (
	"slices"
	"testing"
)

func TestNewDraftNormalizesContextReferencesAndCopiesValues(t *testing.T) {
	reference, err := NewExternalReference("repository", "ob-labs/powercontext-go")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := NewDraft(
		"PowerContext", "Repository context", "parent", []string{"scope-z", "scope-a"},
		[]ExternalReference{reference}, "powercontext-create",
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := draft.ContextReferences(); !slices.Equal(got, []string{"scope-a", "scope-z"}) {
		t.Fatalf("context references = %v", got)
	}
	contextReferences := draft.ContextReferences()
	contextReferences[0] = "mutated"
	if got := draft.ContextReferences(); !slices.Equal(got, []string{"scope-a", "scope-z"}) {
		t.Fatalf("context references changed through accessor: %v", got)
	}
	externalReferences := draft.ExternalReferences()
	externalReferences[0] = ExternalReference{}
	if got := draft.ExternalReferences(); !slices.Equal(got, []ExternalReference{reference}) {
		t.Fatalf("external references changed through accessor: %v", got)
	}
}

func TestScopeValuesRejectInvalidIdentityAndSelectionShapes(t *testing.T) {
	for _, input := range []struct {
		name string
		call func() error
	}{
		{"blank external reference kind", func() error { _, err := NewExternalReference("", "value"); return err }},
		{"padded binding integration", func() error { _, err := NewBindingKey(" codex", "project", "repo"); return err }},
		{"duplicate draft context", func() error {
			_, err := NewDraft("title", "summary", "", []string{"scope", "scope"}, nil, "key")
			return err
		}},
		{"exact selection is empty", func() error { _, err := NewExactSelection(nil); return err }},
		{"exact selection is duplicate", func() error { _, err := NewExactSelection([]string{"scope", "scope"}); return err }},
		{"subtree selection is blank", func() error { _, err := NewSubtreeSelection(""); return err }},
	} {
		t.Run(input.name, func(t *testing.T) {
			if err := input.call(); err == nil {
				t.Fatal("invalid Scope value was accepted")
			}
		})
	}
}

func TestScopeSelectionsAreCanonicalAndImmutable(t *testing.T) {
	exact, err := NewExactSelection([]string{"scope-z", "scope-a"})
	if err != nil {
		t.Fatal(err)
	}
	if exact.Mode() != SelectionExact || !slices.Equal(exact.ScopeIDs(), []string{"scope-a", "scope-z"}) || exact.RootScopeID() != "" {
		t.Fatalf("exact selection = %#v", exact)
	}
	ids := exact.ScopeIDs()
	ids[0] = "mutated"
	if got := exact.ScopeIDs(); !slices.Equal(got, []string{"scope-a", "scope-z"}) {
		t.Fatalf("exact selection changed through accessor: %v", got)
	}

	subtree, err := NewSubtreeSelection("scope-root")
	if err != nil {
		t.Fatal(err)
	}
	if subtree.Mode() != SelectionSubtree || subtree.RootScopeID() != "scope-root" || len(subtree.ScopeIDs()) != 0 {
		t.Fatalf("subtree selection = %#v", subtree)
	}
	if all := AllSelection(); all.Mode() != SelectionAll || all.RootScopeID() != "" || len(all.ScopeIDs()) != 0 {
		t.Fatalf("all selection = %#v", all)
	}
}

func TestDescriptorMutationAndBindingPreserveScopeContracts(t *testing.T) {
	reference, err := NewExternalReference("repository", "ob-labs/powercontext-go")
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := NewDescriptor(
		"scope-a", "title", "summary", "", []string{"scope-z", "scope-b"}, []ExternalReference{reference}, 3,
	)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.ID() != "scope-a" || descriptor.Version() != 3 ||
		!slices.Equal(descriptor.ContextReferences(), []string{"scope-b", "scope-z"}) {
		t.Fatalf("descriptor = %#v", descriptor)
	}
	mutation, err := NewMutation(3, "next title", "next summary", "", []string{"scope-b"}, []ExternalReference{reference})
	if err != nil || mutation.ExpectedVersion() != 3 || mutation.Title() != "next title" {
		t.Fatalf("mutation = %#v, %v", mutation, err)
	}
	key, err := NewBindingKey("codex", "project", "ob-labs/powercontext-go")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewBinding(key, "scope-a")
	if err != nil || binding.Key() != key || binding.ScopeID() != "scope-a" {
		t.Fatalf("binding = %#v, %v", binding, err)
	}
	for _, call := range []func() error{
		func() error { _, err := NewDescriptor("scope", "title", "summary", "", nil, nil, 0); return err },
		func() error { _, err := NewMutation(0, "title", "summary", "", nil, nil); return err },
		func() error { _, err := NewBinding(key, ""); return err },
	} {
		if err := call(); err == nil {
			t.Fatal("invalid Scope repository value was accepted")
		}
	}
}
