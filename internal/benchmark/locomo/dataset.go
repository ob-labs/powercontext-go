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

// Package locomo owns the deterministic, credential-free boundary of the
// LoCoMo benchmark. Model calls and PowerContext operations remain in their
// respective runtime packages.
package locomo

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var (
	sessionKeyPattern = regexp.MustCompile(`^session_(\d+)$`)
	dialogueIDPattern = regexp.MustCompile(`^D(\d+):(\d+)$`)
	evidencePattern   = regexp.MustCompile(`D(\d+):(\d+)`)
	evidenceTypo      = regexp.MustCompile(`^D:(\d+):(\d+)$`)
)

// Turn is one dialogue turn with the dataset's stable evidence identity.
type Turn struct {
	dialogueID   string
	speaker      string
	text         string
	imageCaption *string
}

func (t Turn) DialogueID() string    { return t.dialogueID }
func (t Turn) Speaker() string       { return t.speaker }
func (t Turn) Text() string          { return t.text }
func (t Turn) ImageCaption() *string { return cloneString(t.imageCaption) }

// Session is one timestamped conversation session.
type Session struct {
	id       string
	number   int
	dateTime string
	turns    []Turn
}

func (s Session) ID() string       { return s.id }
func (s Session) Number() int      { return s.number }
func (s Session) DateTime() string { return s.dateTime }
func (s Session) Turns() []Turn    { return cloneTurns(s.turns) }

// Question is one LoCoMo QA item and its exact evidence pointers.
type Question struct {
	id                string
	sampleID          string
	question          string
	answer            string
	category          int
	evidenceRaw       []string
	evidence          []string
	adversarialAnswer *string
}

func (q Question) ID() string                 { return q.id }
func (q Question) SampleID() string           { return q.sampleID }
func (q Question) Text() string               { return q.question }
func (q Question) Answer() string             { return q.answer }
func (q Question) Category() int              { return q.category }
func (q Question) EvidenceRaw() []string      { return slices.Clone(q.evidenceRaw) }
func (q Question) Evidence() []string         { return slices.Clone(q.evidence) }
func (q Question) AdversarialAnswer() *string { return cloneString(q.adversarialAnswer) }

func (q Question) EvidenceSessions() []string {
	seen := make(map[string]struct{}, len(q.evidence))
	result := make([]string, 0, len(q.evidence))
	for _, reference := range q.evidence {
		session, _, _ := strings.Cut(reference, ":")
		if _, ok := seen[session]; ok {
			continue
		}
		seen[session] = struct{}{}
		result = append(result, session)
	}
	return result
}

// Conversation is one two-speaker LoCoMo sample.
type Conversation struct {
	sampleID  string
	speakerA  string
	speakerB  string
	sessions  []Session
	questions []Question
}

func (c Conversation) SampleID() string      { return c.sampleID }
func (c Conversation) SpeakerA() string      { return c.speakerA }
func (c Conversation) SpeakerB() string      { return c.speakerB }
func (c Conversation) Sessions() []Session   { return cloneSessions(c.sessions) }
func (c Conversation) Questions() []Question { return cloneQuestions(c.questions) }

// Dataset is validated benchmark input plus its content fingerprint.
type Dataset struct {
	path          string
	sha256        string
	conversations []Conversation
}

func (d Dataset) Path() string                  { return d.path }
func (d Dataset) SHA256() string                { return d.sha256 }
func (d Dataset) Conversations() []Conversation { return cloneConversations(d.conversations) }

func (d Dataset) Sessions() []Session {
	var result []Session
	for _, conversation := range d.conversations {
		result = append(result, cloneSessions(conversation.sessions)...)
	}
	return result
}

func (d Dataset) Questions() []Question {
	var result []Question
	for _, conversation := range d.conversations {
		result = append(result, cloneQuestions(conversation.questions)...)
	}
	return result
}

type Selection struct {
	Categories        []int
	ConversationLimit *int
	QuestionLimit     *int
}

func DefaultSelection() Selection { return Selection{Categories: []int{1, 2, 3, 4}} }

func (d Dataset) SelectedQuestions(selection Selection) []Question {
	categories := selection.Categories
	if categories == nil {
		categories = []int{1, 2, 3, 4}
	}
	allowed := make(map[int]struct{}, len(categories))
	for _, category := range categories {
		allowed[category] = struct{}{}
	}
	conversationCount := len(d.conversations)
	if selection.ConversationLimit != nil && *selection.ConversationLimit < conversationCount {
		conversationCount = max(*selection.ConversationLimit, 0)
	}
	result := make([]Question, 0)
	for _, conversation := range d.conversations[:conversationCount] {
		for _, question := range conversation.questions {
			if _, ok := allowed[question.category]; ok {
				result = append(result, cloneQuestion(question))
			}
		}
	}
	if selection.QuestionLimit != nil && *selection.QuestionLimit < len(result) {
		result = result[:max(*selection.QuestionLimit, 0)]
	}
	return result
}

// Load reads and strictly validates a LoCoMo dataset.
func Load(path string) (Dataset, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return Dataset{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var raw []map[string]any
	if decodeErr := decoder.Decode(&raw); decodeErr != nil {
		return Dataset{}, fmt.Errorf("decode LoCoMo dataset: %w", decodeErr)
	}
	if len(raw) == 0 {
		return Dataset{}, fmt.Errorf("LoCoMo dataset must be a non-empty JSON array")
	}
	conversations := make([]Conversation, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for index, item := range raw {
		conversation, loadErr := loadConversation(item, index)
		if loadErr != nil {
			return Dataset{}, loadErr
		}
		if _, ok := seen[conversation.sampleID]; ok {
			return Dataset{}, fmt.Errorf("LoCoMo sample IDs must be unique")
		}
		seen[conversation.sampleID] = struct{}{}
		conversations = append(conversations, conversation)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Dataset{}, fmt.Errorf("resolve LoCoMo dataset path: %w", err)
	}
	digest := sha256.Sum256(payload)
	return Dataset{path: absolute, sha256: hex.EncodeToString(digest[:]), conversations: conversations}, nil
}

func RenderSession(conversation Conversation, session Session) string {
	lines := []string{
		fmt.Sprintf("LoCoMo conversation %s, session %s", conversation.sampleID, session.id),
		"Date and time: " + session.dateTime,
		fmt.Sprintf("Speakers: %s and %s", conversation.speakerA, conversation.speakerB),
		"Dialogue:",
	}
	for _, turn := range session.turns {
		lines = append(lines, fmt.Sprintf("[%s] %s: %s", turn.dialogueID, turn.speaker, turn.text))
		if turn.imageCaption != nil {
			lines = append(lines, fmt.Sprintf("[%s] Image caption: %s", turn.dialogueID, *turn.imageCaption))
		}
	}
	return strings.Join(lines, "\n")
}

func loadConversation(raw map[string]any, index int) (Conversation, error) {
	sampleID, err := requiredText(raw["sample_id"], fmt.Sprintf("item %d sample_id", index))
	if err != nil {
		return Conversation{}, err
	}
	conversationRaw, ok := raw["conversation"].(map[string]any)
	if !ok {
		return Conversation{}, fmt.Errorf("LoCoMo item %s has no conversation object", sampleID)
	}
	speakerA, err := requiredText(conversationRaw["speaker_a"], sampleID+" speaker_a")
	if err != nil {
		return Conversation{}, err
	}
	speakerB, err := requiredText(conversationRaw["speaker_b"], sampleID+" speaker_b")
	if err != nil {
		return Conversation{}, err
	}
	type sessionKey struct {
		number int
		key    string
	}
	keys := make([]sessionKey, 0)
	for key := range conversationRaw {
		match := sessionKeyPattern.FindStringSubmatch(key)
		if match == nil {
			continue
		}
		number, _ := strconv.Atoi(match[1])
		keys = append(keys, sessionKey{number: number, key: key})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].number < keys[j].number })
	if len(keys) == 0 {
		return Conversation{}, fmt.Errorf("LoCoMo item %s has no sessions", sampleID)
	}
	sessions := make([]Session, 0, len(keys))
	dialogueIDs := make(map[string]struct{})
	for _, key := range keys {
		session, err := loadSession(conversationRaw, key.key, key.number, sampleID)
		if err != nil {
			return Conversation{}, err
		}
		for _, turn := range session.turns {
			dialogueIDs[turn.dialogueID] = struct{}{}
		}
		sessions = append(sessions, session)
	}
	questionsRaw, ok := raw["qa"].([]any)
	if !ok {
		return Conversation{}, fmt.Errorf("LoCoMo item %s has no qa array", sampleID)
	}
	questions := make([]Question, 0, len(questionsRaw))
	for questionIndex, item := range questionsRaw {
		questionRaw, ok := item.(map[string]any)
		if !ok {
			return Conversation{}, fmt.Errorf("LoCoMo %s question %d must be an object", sampleID, questionIndex)
		}
		question, err := loadQuestion(questionRaw, sampleID, questionIndex, dialogueIDs)
		if err != nil {
			return Conversation{}, err
		}
		questions = append(questions, question)
	}
	return Conversation{sampleID: sampleID, speakerA: speakerA, speakerB: speakerB, sessions: sessions, questions: questions}, nil
}

func loadSession(raw map[string]any, key string, number int, sampleID string) (Session, error) {
	turnsRaw, ok := raw[key].([]any)
	if !ok || len(turnsRaw) == 0 {
		return Session{}, fmt.Errorf("LoCoMo %s %s must be a non-empty array", sampleID, key)
	}
	turns := make([]Turn, 0, len(turnsRaw))
	for turnIndex, item := range turnsRaw {
		turnRaw, ok := item.(map[string]any)
		if !ok {
			return Session{}, fmt.Errorf("LoCoMo %s %s turn %d must be an object", sampleID, key, turnIndex)
		}
		dialogueID, err := requiredText(turnRaw["dia_id"], sampleID+" "+key+" dialogue ID")
		if err != nil {
			return Session{}, err
		}
		match := dialogueIDPattern.FindStringSubmatch(dialogueID)
		if match == nil || match[1] != strconv.Itoa(number) {
			return Session{}, fmt.Errorf("LoCoMo dialogue ID %s does not belong to D%d", dialogueID, number)
		}
		speaker, err := requiredText(turnRaw["speaker"], dialogueID+" speaker")
		if err != nil {
			return Session{}, err
		}
		text, err := requiredText(turnRaw["text"], dialogueID+" text")
		if err != nil {
			return Session{}, err
		}
		var caption *string
		if value, exists := turnRaw["blip_caption"]; exists && value != nil {
			captionText, err := requiredText(value, dialogueID+" image caption")
			if err != nil {
				return Session{}, err
			}
			caption = &captionText
		}
		turns = append(turns, Turn{dialogueID: dialogueID, speaker: speaker, text: text, imageCaption: caption})
	}
	dateTime, err := requiredText(raw[key+"_date_time"], sampleID+" "+key+" date_time")
	if err != nil {
		return Session{}, err
	}
	return Session{id: fmt.Sprintf("D%d", number), number: number, dateTime: dateTime, turns: turns}, nil
}

func loadQuestion(raw map[string]any, sampleID string, index int, dialogueIDs map[string]struct{}) (Question, error) {
	evidenceValues, exists := raw["evidence"]
	if !exists {
		evidenceValues = []any{}
	}
	evidenceList, ok := evidenceValues.([]any)
	if !ok {
		return Question{}, fmt.Errorf("LoCoMo %s question %d evidence must be an array", sampleID, index)
	}
	evidenceRaw := make([]string, 0, len(evidenceList))
	for _, value := range evidenceList {
		text, err := requiredText(value, fmt.Sprintf("%s question %d evidence", sampleID, index))
		if err != nil {
			return Question{}, err
		}
		evidenceRaw = append(evidenceRaw, text)
	}
	evidence := normalizeEvidence(evidenceRaw)
	knownSessions := make(map[string]struct{})
	for dialogueID := range dialogueIDs {
		session, _, _ := strings.Cut(dialogueID, ":")
		knownSessions[session] = struct{}{}
	}
	var unknown []string
	for _, reference := range evidence {
		session, _, _ := strings.Cut(reference, ":")
		if _, known := knownSessions[session]; !known {
			unknown = append(unknown, reference)
		}
	}
	if len(unknown) != 0 {
		return Question{}, fmt.Errorf("LoCoMo %s question %d cites unknown evidence sessions: %v", sampleID, index, unknown)
	}
	category, ok := raw["category"].(json.Number)
	if !ok {
		return Question{}, fmt.Errorf("LoCoMo %s question %d has an invalid category", sampleID, index)
	}
	categoryValue, err := strconv.Atoi(string(category))
	if err != nil || categoryValue < 1 || categoryValue > 5 {
		return Question{}, fmt.Errorf("LoCoMo %s question %d has an invalid category", sampleID, index)
	}
	question, err := requiredText(raw["question"], fmt.Sprintf("%s question %d", sampleID, index))
	if err != nil {
		return Question{}, err
	}
	answer := strings.TrimSpace(scalarText(raw["answer"]))
	var adversarial *string
	if value, exists := raw["adversarial_answer"]; exists && value != nil {
		text := strings.TrimSpace(scalarText(value))
		adversarial = &text
	}
	return Question{
		id: fmt.Sprintf("%s:q%03d", sampleID, index+1), sampleID: sampleID,
		question: question, answer: answer, category: categoryValue,
		evidenceRaw: evidenceRaw, evidence: evidence, adversarialAnswer: adversarial,
	}, nil
}

func normalizeEvidence(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		matches := evidencePattern.FindAllStringSubmatch(value, -1)
		if typo := evidenceTypo.FindStringSubmatch(value); typo != nil {
			matches = [][]string{typo}
		}
		for _, match := range matches {
			session, _ := strconv.Atoi(match[1])
			turn, _ := strconv.Atoi(match[2])
			reference := fmt.Sprintf("D%d:%d", session, turn)
			if _, ok := seen[reference]; ok {
				continue
			}
			seen[reference] = struct{}{}
			result = append(result, reference)
		}
	}
	return result
}

func requiredText(value any, field string) (string, error) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("LoCoMo %s must be non-empty text", field)
	}
	return strings.TrimSpace(text), nil
}

func scalarText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case json.Number:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneTurns(values []Turn) []Turn {
	result := slices.Clone(values)
	for i := range result {
		result[i].imageCaption = cloneString(result[i].imageCaption)
	}
	return result
}

func cloneSessions(values []Session) []Session {
	result := slices.Clone(values)
	for i := range result {
		result[i].turns = cloneTurns(result[i].turns)
	}
	return result
}

func cloneQuestion(value Question) Question {
	value.evidenceRaw = slices.Clone(value.evidenceRaw)
	value.evidence = slices.Clone(value.evidence)
	value.adversarialAnswer = cloneString(value.adversarialAnswer)
	return value
}

func cloneQuestions(values []Question) []Question {
	result := slices.Clone(values)
	for i := range result {
		result[i] = cloneQuestion(result[i])
	}
	return result
}

func cloneConversation(value Conversation) Conversation {
	value.sessions = cloneSessions(value.sessions)
	value.questions = cloneQuestions(value.questions)
	return value
}

func cloneConversations(values []Conversation) []Conversation {
	result := slices.Clone(values)
	for i := range result {
		result[i] = cloneConversation(result[i])
	}
	return result
}
