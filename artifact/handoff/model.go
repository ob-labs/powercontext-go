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

package handoff

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/source"
)

const (
	Family                = "handoff"
	ContentSchemaVersion  = "powercontext.handoff.v1"
	PreparedSchemaVersion = "powercontext.prepared-handoff.v1"
	ResolutionTrust       = "untrusted_history"
	DefaultMaxBytes       = 8_000
	MaxBytes              = 32_768
	MinMaxBytes           = 512
	MaxCitations          = 32
	MaxOmissions          = 64
	MaxStateStatements    = 64
	MaxTextLength         = 8_192
)

func ValidateContentSchema(value string) error {
	if value != ContentSchemaVersion {
		return fmt.Errorf("unsupported Handoff schema %q", value)
	}
	return nil
}

func ValidatePreparedSchema(value string) error {
	if value != PreparedSchemaVersion {
		return fmt.Errorf("unsupported Prepared Handoff schema %q", value)
	}
	return nil
}

type Audience string

const (
	Human Audience = "human"
	Agent Audience = "agent"
)

type Disposition string

const (
	Continuable Disposition = "continuable"
	Blocked     Disposition = "blocked"
	Complete    Disposition = "complete"
)

type CitationKind string

const (
	SourceCitationKind   CitationKind = "source"
	ArtifactCitationKind CitationKind = "artifact"
	MemoryCitationKind   CitationKind = "memory"
)

type (
	Handoff       = artifact.Artifact[Content]
	ArtifactDraft = artifact.Draft[Content]
)

func NewArtifactDraft(content Content, sources []source.Ref, artifacts []artifact.Ref) (ArtifactDraft, error) {
	if err := content.Validate(); err != nil {
		return ArtifactDraft{}, err
	}
	return artifact.NewDraft(Family, cloneContent(content), sources, artifacts)
}

func validateText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must contain non-whitespace content", field)
	}
	if utf8.RuneCountInString(value) > MaxTextLength {
		return fmt.Errorf("%s must not exceed %d characters", field, MaxTextLength)
	}
	return nil
}

func validateBudget(value int) error {
	if value < MinMaxBytes || value > MaxBytes {
		return fmt.Errorf("Handoff max_bytes must be between %d and %d", MinMaxBytes, MaxBytes)
	}
	return nil
}

func validateCitations(values []Citation, unique bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateCitation(value); err != nil {
			return err
		}
		if unique {
			key := value.citationKey()
			if _, exists := seen[key]; exists {
				return fmt.Errorf("Handoff citations must be unique")
			}
			seen[key] = struct{}{}
		}
	}
	return nil
}

func validateCitation(value Citation) error {
	if value == nil {
		return fmt.Errorf("Handoff citation must not be nil")
	}
	switch citation := value.(type) {
	case SourceCitation:
		return citation.Validate()
	case ArtifactCitation:
		return citation.Validate()
	case MemoryCitation:
		return citation.Validate()
	default:
		return fmt.Errorf("unsupported Handoff citation %T", value)
	}
}

func validateEvidence(value Evidence) error {
	if value == nil {
		return fmt.Errorf("Handoff evidence must not be nil")
	}
	switch evidence := value.(type) {
	case SourceEvidence:
		return evidence.Validate()
	case ArtifactEvidence:
		return evidence.Validate()
	case MemoryEvidence:
		return evidence.Validate()
	default:
		return fmt.Errorf("unsupported Handoff evidence %T", value)
	}
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func cloneStatement(value *Statement) *Statement {
	if value == nil {
		return nil
	}
	cloned := Statement{text: value.text, citations: slices.Clone(value.citations)}
	return &cloned
}

func cloneContent(value Content) Content {
	state := make([]Statement, len(value.state))
	for index := range value.state {
		state[index] = *cloneStatement(&value.state[index])
	}
	return Content{
		objective: value.objective, state: state, disposition: value.disposition,
		nextAction: cloneStatement(value.nextAction), omissions: slices.Clone(value.omissions),
	}
}

func cloneDraft(value *Draft) *Draft {
	if value == nil {
		return nil
	}
	cloned := Draft{content: cloneContent(value.content)}
	return &cloned
}

func cloneContentPointer(value *Content) *Content {
	if value == nil {
		return nil
	}
	cloned := cloneContent(*value)
	return &cloned
}
