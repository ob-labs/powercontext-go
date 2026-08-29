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

package artifact_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/source"
)

func TestArtifactCopiesLineageAndRetainsOrder(t *testing.T) {
	firstSource, _ := source.NewRef("content", "one")
	secondSource, _ := source.NewRef("content", "two")
	parent, _ := artifact.NewRef("memory", "parent", 2)
	sources := []source.Ref{firstSource, secondSource}
	parents := []artifact.Ref{parent}
	draft, err := artifact.NewDraft("memory", "body", sources, parents)
	if err != nil {
		t.Fatal(err)
	}
	sources[0], _ = source.NewRef("content", "changed")
	parents[0], _ = artifact.NewRef("memory", "changed", 1)

	value, err := artifact.New("memory-id", 1, draft)
	if err != nil {
		t.Fatal(err)
	}
	lineage := value.Lineage()
	if got := lineage.Sources()[0].ID(); got != "one" {
		t.Fatalf("source alias leaked: %s", got)
	}
	if got := lineage.Artifacts()[0].ID(); got != "parent" {
		t.Fatalf("artifact alias leaked: %s", got)
	}
	returned := lineage.Sources()
	returned[0], _ = source.NewRef("content", "mutated")
	if got := value.Lineage().Sources()[0].ID(); got != "one" {
		t.Fatalf("returned lineage mutated aggregate: %s", got)
	}
}

type memoryCatalog struct{}

func (memoryCatalog) Get(_ context.Context, value artifact.Artifact[string]) (artifact.Artifact[string], error) {
	return value, nil
}

func (memoryCatalog) Latest(_ context.Context, value artifact.Artifact[string]) (artifact.Artifact[string], error) {
	return value, nil
}

func (memoryCatalog) Revisions(_ context.Context, value artifact.Artifact[string]) ([]artifact.Artifact[string], error) {
	return []artifact.Artifact[string]{value}, nil
}

type memoryStore struct{}

func (memoryStore) Add(_ context.Context, draft artifact.Draft[string]) (artifact.Artifact[string], error) {
	return artifact.New("new", 1, draft)
}

func (memoryStore) Revise(_ context.Context, current artifact.Artifact[string], draft artifact.Draft[string]) (artifact.Artifact[string], error) {
	return artifact.New(current.ID(), current.Revision()+1, draft)
}

func TestServiceRejectsCrossFamilyRevision(t *testing.T) {
	memoryDraft, _ := artifact.NewDraft("memory", "one", nil, nil)
	memory, _ := artifact.New("id", 1, memoryDraft)
	experienceDraft, _ := artifact.NewDraft("experience", "two", nil, nil)
	service := artifact.NewService[string](memoryCatalog{}, memoryStore{})

	_, err := service.Revise(context.Background(), memory, experienceDraft)
	var mismatch *artifact.FamilyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected family mismatch, got %v", err)
	}
}

func TestRefRejectsWhitespaceAndZeroRevision(t *testing.T) {
	for _, test := range []struct {
		family   string
		id       string
		revision int64
	}{
		{family: " memory", id: "id", revision: 1},
		{family: "\u001cmemory", id: "id", revision: 1},
		{family: "memory", id: "", revision: 1},
		{family: "memory", id: "id", revision: 0},
	} {
		if _, err := artifact.NewRef(test.family, test.id, test.revision); err == nil {
			t.Fatalf("expected invalid ref for %#v", test)
		}
	}
}

func TestArtifactReferenceRejectsInvalidIdentity(t *testing.T) {
	tests := []struct {
		family, artifactID string
		revision           int64
	}{
		{"", "artifact", 1},
		{" family", "artifact", 1},
		{strings.Repeat("x", artifact.MaxFamilyLength+1), "artifact", 1},
		{"memory", "", 1},
		{"memory", " artifact", 1},
		{"memory", strings.Repeat("x", artifact.MaxIDLength+1), 1},
		{"memory", "artifact", 0},
		{"memory", "artifact", -1},
	}
	for _, test := range tests {
		if _, err := artifact.NewRef(test.family, test.artifactID, test.revision); err == nil {
			t.Fatalf("invalid identity was accepted: %#v", test)
		}
	}
}

func TestArtifactDomainValuesRejectInvalidIdentityAndLineage(t *testing.T) {
	if _, err := artifact.New("", 1, artifact.Draft[string]{}); err == nil {
		t.Fatal("zero Draft produced an Artifact")
	}
	if _, err := artifact.NewDraft("memory", "content", []source.Ref{{}}, nil); err == nil {
		t.Fatal("zero Source lineage was accepted")
	}
	if _, err := artifact.NewDraft("memory", "content", nil, []artifact.Ref{{}}); err == nil {
		t.Fatal("zero Artifact lineage was accepted")
	}
	if _, err := artifact.Restore(artifact.Ref{}, "content", artifact.Lineage{}); err == nil {
		t.Fatal("zero Artifact identity was restored")
	}
}
