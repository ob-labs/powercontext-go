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

// Package scope owns immutable Scope identity, organization, and selection values.
package scope

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxIDLength                     = 256
	MaxTitleLength                  = 256
	MaxSummaryLength                = 2_000
	MaxExternalReferenceKindLength  = 128
	MaxExternalReferenceValueLength = 2_000
	MaxIdempotencyKeyLength         = 256
	MaxBindingIntegrationLength     = 128
	MaxBindingKindLength            = 64
	MaxBindingExternalIDLength      = 256
)

// IdempotencyConflictError reports a Scope creation key reused with different input.
type IdempotencyConflictError struct{}

func (*IdempotencyConflictError) Error() string {
	return "Scope creation key was reused with different parameters"
}

// VersionConflictError reports a Scope mutation against a stale revision.
type VersionConflictError struct {
	Expected int64
	Actual   int64
}

func (*VersionConflictError) Error() string {
	return "Scope metadata changed since it was read"
}

// ExternalReference identifies an external system value associated with a Scope.
type ExternalReference struct {
	kind  string
	value string
}

// NewExternalReference validates one opaque external Scope reference.
func NewExternalReference(kind, value string) (ExternalReference, error) {
	if err := requiredText("external reference kind", kind, MaxExternalReferenceKindLength); err != nil {
		return ExternalReference{}, err
	}
	if err := requiredText("external reference value", value, MaxExternalReferenceValueLength); err != nil {
		return ExternalReference{}, err
	}
	return ExternalReference{kind: kind, value: value}, nil
}

func (r ExternalReference) Kind() string  { return r.kind }
func (r ExternalReference) Value() string { return r.value }

// Draft is the immutable input for one idempotent Scope creation.
type Draft struct {
	title              string
	summary            string
	parentScopeID      string
	contextReferences  []string
	externalReferences []ExternalReference
	idempotencyKey     string
}

// NewDraft validates and canonicalizes Scope creation input.
func NewDraft(
	title, summary, parentScopeID string,
	contextReferences []string, externalReferences []ExternalReference, idempotencyKey string,
) (Draft, error) {
	metadata, err := newMetadata(title, summary, parentScopeID, contextReferences, externalReferences)
	if err != nil {
		return Draft{}, err
	}
	if err := requiredText("idempotency key", idempotencyKey, MaxIdempotencyKeyLength); err != nil {
		return Draft{}, err
	}
	return Draft{
		title: metadata.title, summary: metadata.summary, parentScopeID: metadata.parentScopeID,
		contextReferences: metadata.contextReferences, externalReferences: metadata.externalReferences, idempotencyKey: idempotencyKey,
	}, nil
}

func (d Draft) Title() string                           { return d.title }
func (d Draft) Summary() string                         { return d.summary }
func (d Draft) ParentScopeID() string                   { return d.parentScopeID }
func (d Draft) ContextReferences() []string             { return slices.Clone(d.contextReferences) }
func (d Draft) ExternalReferences() []ExternalReference { return slices.Clone(d.externalReferences) }
func (d Draft) IdempotencyKey() string                  { return d.idempotencyKey }

// BindingKey identifies a durable external-to-Scope binding.
type BindingKey struct {
	integration string
	kind        string
	externalID  string
}

// NewBindingKey validates one external Scope binding identity.
func NewBindingKey(integration, kind, externalID string) (BindingKey, error) {
	if err := requiredText("binding integration", integration, MaxBindingIntegrationLength); err != nil {
		return BindingKey{}, err
	}
	if err := requiredText("binding kind", kind, MaxBindingKindLength); err != nil {
		return BindingKey{}, err
	}
	if err := requiredText("binding external ID", externalID, MaxBindingExternalIDLength); err != nil {
		return BindingKey{}, err
	}
	return BindingKey{integration: integration, kind: kind, externalID: externalID}, nil
}

func (k BindingKey) Integration() string { return k.integration }
func (k BindingKey) Kind() string        { return k.kind }
func (k BindingKey) ExternalID() string  { return k.externalID }

// Descriptor is one immutable persisted Scope revision.
type Descriptor struct {
	id                 string
	title              string
	summary            string
	parentScopeID      string
	contextReferences  []string
	externalReferences []ExternalReference
	version            int64
}

// NewDescriptor validates one persisted Scope view.
func NewDescriptor(
	id, title, summary, parentScopeID string,
	contextReferences []string, externalReferences []ExternalReference, version int64,
) (Descriptor, error) {
	if err := validateID(id); err != nil {
		return Descriptor{}, err
	}
	metadata, err := newMetadata(title, summary, parentScopeID, contextReferences, externalReferences)
	if err != nil {
		return Descriptor{}, err
	}
	if version < 1 {
		return Descriptor{}, fmt.Errorf("Scope version must be positive")
	}
	return Descriptor{
		id: id, title: metadata.title, summary: metadata.summary, parentScopeID: metadata.parentScopeID,
		contextReferences: metadata.contextReferences, externalReferences: metadata.externalReferences, version: version,
	}, nil
}

func (d Descriptor) ID() string                  { return d.id }
func (d Descriptor) Title() string               { return d.title }
func (d Descriptor) Summary() string             { return d.summary }
func (d Descriptor) ParentScopeID() string       { return d.parentScopeID }
func (d Descriptor) ContextReferences() []string { return slices.Clone(d.contextReferences) }
func (d Descriptor) ExternalReferences() []ExternalReference {
	return slices.Clone(d.externalReferences)
}
func (d Descriptor) Version() int64 { return d.version }

// Mutation is one complete compare-and-swap Scope metadata replacement.
type Mutation struct {
	expectedVersion    int64
	title              string
	summary            string
	parentScopeID      string
	contextReferences  []string
	externalReferences []ExternalReference
}

// NewMutation validates one full Scope metadata replacement.
func NewMutation(
	expectedVersion int64, title, summary, parentScopeID string,
	contextReferences []string, externalReferences []ExternalReference,
) (Mutation, error) {
	if expectedVersion < 1 {
		return Mutation{}, fmt.Errorf("expected Scope version must be positive")
	}
	metadata, err := newMetadata(title, summary, parentScopeID, contextReferences, externalReferences)
	if err != nil {
		return Mutation{}, err
	}
	return Mutation{
		expectedVersion: expectedVersion, title: metadata.title, summary: metadata.summary,
		parentScopeID: metadata.parentScopeID, contextReferences: metadata.contextReferences, externalReferences: metadata.externalReferences,
	}, nil
}

func (m Mutation) ExpectedVersion() int64      { return m.expectedVersion }
func (m Mutation) Title() string               { return m.title }
func (m Mutation) Summary() string             { return m.summary }
func (m Mutation) ParentScopeID() string       { return m.parentScopeID }
func (m Mutation) ContextReferences() []string { return slices.Clone(m.contextReferences) }

func (m Mutation) ExternalReferences() []ExternalReference { return slices.Clone(m.externalReferences) }

// Binding pairs a durable external key with one Scope.
type Binding struct {
	key     BindingKey
	scopeID string
}

func NewBinding(key BindingKey, scopeID string) (Binding, error) {
	if key.integration == "" || key.kind == "" || key.externalID == "" {
		return Binding{}, fmt.Errorf("Scope binding key must be constructed")
	}
	if err := validateID(scopeID); err != nil {
		return Binding{}, err
	}
	return Binding{key: key, scopeID: scopeID}, nil
}

func (b Binding) Key() BindingKey { return b.key }
func (b Binding) ScopeID() string { return b.scopeID }

// SelectionMode defines a closed Scope selection shape.
type SelectionMode string

const (
	SelectionAll     SelectionMode = "all"
	SelectionExact   SelectionMode = "exact"
	SelectionSubtree SelectionMode = "subtree"
)

// Selection identifies all Scopes, an exact canonical set, or one subtree root.
type Selection struct {
	mode        SelectionMode
	scopeIDs    []string
	rootScopeID string
}

func AllSelection() Selection { return Selection{mode: SelectionAll} }

func NewExactSelection(scopeIDs []string) (Selection, error) {
	ids, err := canonicalScopeIDs(scopeIDs, true)
	if err != nil {
		return Selection{}, err
	}
	return Selection{mode: SelectionExact, scopeIDs: ids}, nil
}

func NewSubtreeSelection(rootScopeID string) (Selection, error) {
	if err := validateID(rootScopeID); err != nil {
		return Selection{}, err
	}
	return Selection{mode: SelectionSubtree, rootScopeID: rootScopeID}, nil
}

func (s Selection) Mode() SelectionMode { return s.mode }
func (s Selection) ScopeIDs() []string  { return slices.Clone(s.scopeIDs) }
func (s Selection) RootScopeID() string { return s.rootScopeID }

func canonicalScopeIDs(values []string, requireNonEmpty bool) ([]string, error) {
	if requireNonEmpty && len(values) == 0 {
		return nil, fmt.Errorf("exact Scope selection requires scope IDs")
	}
	result := slices.Clone(values)
	for _, value := range result {
		if err := validateID(value); err != nil {
			return nil, err
		}
	}
	slices.Sort(result)
	compacted := slices.Compact(result)
	if len(compacted) != len(values) {
		return nil, fmt.Errorf("Scope references must be unique")
	}
	return compacted, nil
}

type metadata struct {
	title              string
	summary            string
	parentScopeID      string
	contextReferences  []string
	externalReferences []ExternalReference
}

func newMetadata(title, summary, parentScopeID string, contextReferences []string, externalReferences []ExternalReference) (metadata, error) {
	if err := requiredText("title", title, MaxTitleLength); err != nil {
		return metadata{}, err
	}
	if err := requiredText("summary", summary, MaxSummaryLength); err != nil {
		return metadata{}, err
	}
	if parentScopeID != "" {
		if err := validateID(parentScopeID); err != nil {
			return metadata{}, err
		}
	}
	references, err := canonicalScopeIDs(contextReferences, false)
	if err != nil {
		return metadata{}, err
	}
	external := slices.Clone(externalReferences)
	if slices.ContainsFunc(external, func(reference ExternalReference) bool { return reference.kind == "" || reference.value == "" }) {
		return metadata{}, fmt.Errorf("Scope external references must be constructed values")
	}
	if hasDuplicateExternalReference(external) {
		return metadata{}, fmt.Errorf("Scope external references must be unique")
	}
	return metadata{title: title, summary: summary, parentScopeID: parentScopeID, contextReferences: references, externalReferences: external}, nil
}

func hasDuplicateExternalReference(values []ExternalReference) bool {
	seen := make(map[ExternalReference]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func validateID(value string) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" {
		return fmt.Errorf("scope ID must not be empty")
	}
	if utf8.RuneCountInString(value) > MaxIDLength {
		return fmt.Errorf("scope ID must not exceed %d characters", MaxIDLength)
	}
	return nil
}

func requiredText(field, value string, maximum int) error {
	trimmed := strings.TrimFunc(value, isPythonWhitespace)
	if trimmed == "" || value != trimmed {
		return fmt.Errorf("%s must be non-empty without surrounding whitespace", field)
	}
	if utf8.RuneCountInString(value) > maximum {
		return fmt.Errorf("%s must not exceed %d characters", field, maximum)
	}
	return nil
}

func isPythonWhitespace(value rune) bool {
	return unicode.IsSpace(value) || value >= '\u001c' && value <= '\u001f'
}
