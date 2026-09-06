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

package contextpack

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/memory"
)

func testMemoryRef(t *testing.T, revision int64) artifact.Ref {
	t.Helper()
	ref, err := artifact.NewRef(memory.Family, "memory", revision)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func testHit(ref artifact.Ref, entryID, text string) memory.Hit {
	return memory.Hit{
		MemoryRef: ref, EntryID: entryID, EntryVersionID: entryID + "-v1",
		Text: text, Score: 1,
	}
}

func testExperienceHit(t *testing.T, artifactID string, revision int64) experience.SearchHit {
	t.Helper()
	ref, err := artifact.NewRef(experience.Family, artifactID, revision)
	if err != nil {
		t.Fatal(err)
	}
	content, err := experience.NewContent(
		"The generated API client is stale after an OpenAPI change.",
		"Regenerate the client before running contract tests.",
		"The checked-in transport matches the public contract.",
		"Regenerate and inspect the client before contract tests.",
	)
	if err != nil {
		t.Fatal(err)
	}
	return experience.SearchHit{ArtifactRef: ref, Content: content}
}

func testRequest(t *testing.T, query string, maxBytes int) Request {
	t.Helper()
	request, err := NewRequest(query, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func renderedItems(t *testing.T, prepared Prepared) []map[string]any {
	t.Helper()
	content := prepared.Content()
	if content == nil {
		t.Fatal("prepared context has no content")
	}
	lines := strings.Split(*content, "\n")
	if len(lines) < 2 {
		t.Fatalf("invalid prepared context: %q", *content)
	}
	var envelope struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-2]), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Items
}

func TestBuilderPreservesOrderAndFiltersDuplicateOrInvalidHits(t *testing.T) {
	ref := testMemoryRef(t, 3)
	first := testHit(ref, "first", "First entry")
	prepared, err := (Builder{}).Build(
		testRequest(t, "entry", 0), &ref,
		[]memory.Hit{
			first,
			first.Clone(),
			testHit(ref, "invalid-text", "   "),
			testHit(ref, "", "Missing entry ID"),
			testHit(ref, "later", "Later entry"),
		}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status() != Ready {
		t.Fatalf("status = %q, want %q", prepared.Status(), Ready)
	}
	items := renderedItems(t, prepared)
	got := make([]string, len(items))
	for index, item := range items {
		got[index] = item["citation"].(map[string]any)["entry_id"].(string)
	}
	if want := []string{"first", "later"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("entry IDs = %#v, want %#v", got, want)
	}
	if content := prepared.Content(); content == nil || prepared.ContentBytes() != len([]byte(*content)) {
		t.Fatal("content byte count is inconsistent")
	}
}

func TestBuilderTruncatesUnicodeDeterministicallyWithinFinalBudget(t *testing.T) {
	ref := testMemoryRef(t, 3)
	request := testRequest(t, "记忆", 800)
	hit := testHit(ref, "unicode", strings.Repeat("记忆🙂é", 400))
	first, err := (Builder{}).Build(request, &ref, []memory.Hit{hit}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := (Builder{}).Build(request, &ref, []memory.Hit{hit}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same input produced different prepared contexts")
	}
	if first.Status() != Ready || first.ContentBytes() > request.MaxBytes() {
		t.Fatalf("prepared context = (%q, %d bytes)", first.Status(), first.ContentBytes())
	}
	item := renderedItems(t, first)[0]
	if item["truncated"] != true || !strings.HasSuffix(item["content"].(string), ellipsis) {
		t.Fatalf("item was not truncated as expected: %#v", item)
	}
}

func TestBuilderRejectsTruncatedUnicodeBelowMinimumByteSize(t *testing.T) {
	ref := testMemoryRef(t, 3)
	hit := testHit(ref, "emoji", strings.Repeat("🙂", 200))
	tooSmall, err := (Builder{}).Build(testRequest(t, "emoji", 590), &ref, []memory.Hit{hit}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if tooSmall.Status() != Empty {
		t.Fatalf("590-byte result status = %q, want empty", tooSmall.Status())
	}
	largeEnough, err := (Builder{}).Build(testRequest(t, "emoji", 594), &ref, []memory.Hit{hit}, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := renderedItems(t, largeEnough)[0]
	if got := len([]byte(item["content"].(string))); got < minTruncatedContentBytes {
		t.Fatalf("truncated content = %d bytes, want at least %d", got, minTruncatedContentBytes)
	}
}

func TestBuilderSkipsUnfittableEntryAndKeepsLaterShortEntry(t *testing.T) {
	ref := testMemoryRef(t, 3)
	prepared, err := (Builder{}).Build(
		testRequest(t, "entry", 620), &ref,
		[]memory.Hit{
			testHit(ref, strings.Repeat("a", 128), strings.Repeat("long content ", 200)),
			testHit(ref, "short", "small"),
		}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	items := renderedItems(t, prepared)
	if len(items) != 1 || items[0]["citation"].(map[string]any)["entry_id"] != "short" {
		t.Fatalf("items = %#v, want only short entry", items)
	}
	if prepared.ContentBytes() > 620 {
		t.Fatalf("content = %d bytes, exceeds budget", prepared.ContentBytes())
	}
}

func TestBuilderRejectsHitFromDifferentMemoryHead(t *testing.T) {
	ref := testMemoryRef(t, 3)
	other := testMemoryRef(t, 4)
	_, err := (Builder{}).Build(testRequest(t, "head", 0), &ref, []memory.Hit{testHit(other, "other", "Other head")}, nil)
	var invariant *InvariantError
	if !errors.As(err, &invariant) || invariant.Code != "memory-ref-mismatch" {
		t.Fatalf("error = %v, want memory-ref-mismatch", err)
	}
}

func TestBuilderScopesPreservesReferencedProvenanceAndRoundRobinOrder(t *testing.T) {
	currentRef := testMemoryRef(t, 1)
	referencedRef := testMemoryRef(t, 2)
	build, err := (Builder{}).BuildScopesResult(
		testRequest(t, "scope", 0), "current",
		[]MemoryCandidates{
			{ScopeID: "current", MemoryRef: &currentRef, Hits: []memory.Hit{testHit(currentRef, "current-memory", "Current memory")}},
			{ScopeID: "referenced", MemoryRef: &referencedRef, Hits: []memory.Hit{testHit(referencedRef, "referenced-memory", "Referenced memory")}},
		},
		[]ExperienceCandidates{
			{ScopeID: "current", Hits: []experience.SearchHit{testExperienceHit(t, "current-experience", 1)}},
			{ScopeID: "referenced", Hits: []experience.SearchHit{testExperienceHit(t, "referenced-experience", 1)}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	items := renderedItems(t, build.Context)
	if len(items) != 4 {
		t.Fatalf("rendered item count = %d, want 4", len(items))
	}
	firstMemory := items[0]["citation"].(map[string]any)
	if firstMemory["entry_id"] != "current-memory" || firstMemory["memory_ref"] == nil {
		t.Fatalf("current memory citation = %#v", firstMemory)
	}
	firstExperience := items[1]["citation"].(map[string]any)
	if firstExperience["artifact_ref"] == nil {
		t.Fatalf("current Experience citation = %#v", firstExperience)
	}
	referencedMemory := items[2]["citation"].(map[string]any)
	address, ok := referencedMemory["memory"].(map[string]any)
	if !ok || address["scope_id"] != "referenced" {
		t.Fatalf("referenced memory citation = %#v", referencedMemory)
	}
	referencedExperience := items[3]["citation"].(map[string]any)
	experienceAddress, ok := referencedExperience["artifact"].(map[string]any)
	if !ok || experienceAddress["scope_id"] != "referenced" {
		t.Fatalf("referenced Experience citation = %#v", referencedExperience)
	}
	if len(build.Origins) != len(items) || build.Origins[2].ScopedMemory == nil || build.Origins[3].ScopedArtifact == nil {
		t.Fatalf("origins do not preserve referenced Scope provenance: %#v", build.Origins)
	}
}

func TestBuilderPreparesExperienceWithoutMemoryHead(t *testing.T) {
	prepared, err := (Builder{}).Build(
		testRequest(t, "Regenerate client contract tests", 0), nil, nil,
		[]experience.SearchHit{testExperienceHit(t, "experience-1", 1)},
	)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Status() != Ready || prepared.Schema() != SchemaV1 {
		t.Fatalf("prepared context = (%q, %q)", prepared.Status(), prepared.Schema())
	}
	content := prepared.Content()
	if content == nil || !strings.Contains(*content, beginMarker) {
		t.Fatal("prepared context is missing the v1 boundary")
	}
	item := renderedItems(t, prepared)[0]
	if item["kind"] != "experience" {
		t.Fatalf("kind = %#v", item["kind"])
	}
	citation := item["citation"].(map[string]any)["artifact_ref"].(map[string]any)
	if citation["family"] != "experience" || citation["artifact_id"] != "experience-1" || citation["revision"] != float64(1) {
		t.Fatalf("citation = %#v", citation)
	}
	if !strings.HasSuffix(item["content"].(string), "Lesson: Regenerate and inspect the client before contract tests.") {
		t.Fatalf("content = %q", item["content"])
	}
}

func TestBuilderKeepsMemoryPrimaryAndBoundsExperienceShare(t *testing.T) {
	ref := testMemoryRef(t, 3)
	experiences := make([]experience.SearchHit, 4)
	for index := range experiences {
		experiences[index] = testExperienceHit(t, "experience-"+string(rune('1'+index)), 1)
	}
	prepared, err := (Builder{}).Build(
		testRequest(t, "client", 0), &ref,
		[]memory.Hit{testHit(ref, "first", "First Memory entry"), testHit(ref, "second", "Second Memory entry")},
		experiences,
	)
	if err != nil {
		t.Fatal(err)
	}
	items := renderedItems(t, prepared)
	got := make([]string, len(items))
	for index, item := range items {
		got[index] = "memory"
		if kind, ok := item["kind"].(string); ok {
			got[index] = kind
		}
	}
	if want := []string{"memory", "experience", "memory", "experience"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kinds = %#v, want %#v", got, want)
	}
}

func TestBuilderRejectsNonExperienceRecallHit(t *testing.T) {
	hit := testExperienceHit(t, "experience-1", 1)
	ref, err := artifact.NewRef("skill", "skill-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	hit.ArtifactRef = ref
	_, err = (Builder{}).Build(testRequest(t, "client", 0), nil, nil, []experience.SearchHit{hit})
	var invariant *InvariantError
	if !errors.As(err, &invariant) || invariant.Code != "experience-family-mismatch" {
		t.Fatalf("error = %v, want experience-family-mismatch", err)
	}
}

func TestRequestValidationAndEmptyContext(t *testing.T) {
	for _, test := range []struct {
		query    string
		maxBytes int
	}{
		{query: "   "},
		{query: "query", maxBytes: 511},
		{query: "query", maxBytes: 32_769},
		{query: strings.Repeat("x", MaxQueryRunes+1)},
	} {
		if _, err := NewRequest(test.query, test.maxBytes); err == nil {
			t.Fatalf("NewRequest(%q, %d) succeeded", test.query, test.maxBytes)
		}
	}
	prepared := (Builder{}).Empty()
	if prepared.Status() != Empty || prepared.Content() != nil || prepared.ContentBytes() != 0 {
		t.Fatalf("empty context = %#v", prepared)
	}
	if _, err := (Builder{}).Build(Request{}, nil, nil, nil); err == nil {
		t.Fatal("zero request bypassed validation")
	}
}

func TestRenderMatchesEnsureASCIIFalseForLineSeparators(t *testing.T) {
	ref := testMemoryRef(t, 3)
	text := "before\u2028middle\u2029after literal \\u2028"
	prepared, err := (Builder{}).Build(testRequest(t, "separator", 0), &ref, []memory.Hit{testHit(ref, "separator", text)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	content := *prepared.Content()
	if !strings.Contains(content, "before\u2028middle\u2029after") {
		t.Fatal("actual line separators were escaped")
	}
	if !strings.Contains(content, `literal \\u2028`) {
		t.Fatalf("literal escape changed meaning: %q", content)
	}
	if !utf8.ValidString(content) {
		t.Fatal("rendered content is not valid UTF-8")
	}
}
