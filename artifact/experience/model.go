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

package experience

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/source"
)

const (
	Family         = "experience"
	MaxFieldLength = 8_000
)

type Content struct {
	situation string
	action    string
	outcome   string
	lesson    string
}

func NewContent(situation, action, outcome, lesson string) (Content, error) {
	for field, value := range map[string]string{
		"situation": situation, "action": action, "outcome": outcome, "lesson": lesson,
	} {
		if strings.TrimFunc(value, isPythonWhitespace) == "" {
			return Content{}, fmt.Errorf("Experience fields must not be blank: %s", field)
		}
		if utf8.RuneCountInString(value) > MaxFieldLength {
			return Content{}, fmt.Errorf("Experience field %s must not exceed %d characters", field, MaxFieldLength)
		}
	}
	return Content{situation: situation, action: action, outcome: outcome, lesson: lesson}, nil
}

// Python str.strip treats the four ASCII information separators as
// whitespace even though Go's Unicode White_Space table does not. Domain
// validation follows the frozen Python boundary exactly.
func isPythonWhitespace(character rune) bool {
	return unicode.IsSpace(character) || character >= '\u001c' && character <= '\u001f'
}

func (c Content) Situation() string { return c.situation }
func (c Content) Action() string    { return c.action }
func (c Content) Outcome() string   { return c.outcome }
func (c Content) Lesson() string    { return c.lesson }

type (
	Experience = artifact.Artifact[Content]
	Draft      = artifact.Draft[Content]
)

func NewDraft(content Content, sources []source.Ref, artifacts []artifact.Ref) (Draft, error) {
	return artifact.NewDraft(Family, content, sources, artifacts)
}

type SearchHit struct {
	ArtifactRef artifact.Ref
	Content     Content
}

func Render(content Content) string {
	return "Situation: " + content.situation + "\n" +
		"Action: " + content.action + "\n" +
		"Outcome: " + content.outcome + "\n" +
		"Lesson: " + content.lesson
}

func SearchText(content Content) string {
	return strings.Join([]string{content.situation, content.action, content.outcome, content.lesson}, "\n")
}

func SearchableText(content Content) string { return memory.AnalyzeText(SearchText(content)) }
