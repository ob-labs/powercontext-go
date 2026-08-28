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

package locomo

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/ob-labs/powercontext-go/artifact/memory/prompts"
	benchmarkprompts "github.com/ob-labs/powercontext-go/internal/benchmark/locomo/prompts"
)

const BenchmarkTemperature = 0.0

var invalidRunID = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

type RerankMode string

const (
	RerankNone RerankMode = "none"
	RerankLLM  RerankMode = "llm"
)

// PublicConfiguration deliberately has no database URL or credential field.
type PublicConfiguration struct {
	DatabaseKind                 string `json:"database_kind"`
	GenerationModel              string `json:"generation_model"`
	EmbeddingModel               string `json:"embedding_model"`
	EmbeddingProfileID           string `json:"embedding_profile_id"`
	EmbeddingDimension           int    `json:"embedding_dimension"`
	EmbeddingNormalization       string `json:"embedding_normalization"`
	EmbeddingBatchSize           int    `json:"embedding_batch_size"`
	MemoryExtractionProfile      string `json:"memory_extraction_profile"`
	MemoryExtractionInstructions string `json:"memory_extraction_instructions"`
}

type RunOptions struct {
	RunID                          string
	OutputDirectory                string
	TopK                           int
	AnswerK                        *int
	RerankMode                     RerankMode
	AnswerSourceContent            bool
	AnswerInferenceAware           bool
	AnswerUnknownFallbackInference bool
	JudgeProfile                   benchmarkprompts.JudgeProfile
	Categories                     []int
	ConversationLimit              *int
	QuestionLimit                  *int
	OperationRetries               int
	Configuration                  PublicConfiguration
}

type RunManifest struct {
	Schema                         string                        `json:"schema"`
	RunID                          string                        `json:"run_id"`
	DatasetPath                    string                        `json:"dataset_path"`
	DatasetSHA256                  string                        `json:"dataset_sha256"`
	DatasetConversationCount       int                           `json:"dataset_conversation_count"`
	DatasetSessionCount            int                           `json:"dataset_session_count"`
	DatasetQuestionCount           int                           `json:"dataset_question_count"`
	SelectedConversationCount      int                           `json:"selected_conversation_count"`
	SelectedQuestionCount          int                           `json:"selected_question_count"`
	Categories                     []int                         `json:"categories"`
	TopK                           int                           `json:"top_k"`
	CandidateK                     int                           `json:"candidate_k"`
	AnswerK                        int                           `json:"answer_k"`
	RerankMode                     RerankMode                    `json:"rerank_mode"`
	RerankInstructions             *string                       `json:"rerank_instructions"`
	AnswerSourceContent            bool                          `json:"answer_source_content"`
	AnswerInferenceAware           bool                          `json:"answer_inference_aware,omitempty"`
	AnswerUnknownFallbackInference bool                          `json:"answer_unknown_fallback_inference,omitempty"`
	AnswerFallbackTrigger          string                        `json:"answer_fallback_trigger,omitempty"`
	DirectAnswerInstructions       string                        `json:"direct_answer_instructions,omitempty"`
	FallbackAnswerInstructions     string                        `json:"fallback_answer_instructions,omitempty"`
	ConversationLimit              *int                          `json:"conversation_limit"`
	QuestionLimit                  *int                          `json:"question_limit"`
	OperationRetries               int                           `json:"operation_retries"`
	GenerationTemperature          float64                       `json:"generation_temperature"`
	Ingestion                      string                        `json:"ingestion"`
	RetrievalMode                  string                        `json:"retrieval_mode"`
	AnswerInstructions             string                        `json:"answer_instructions"`
	JudgeProfile                   benchmarkprompts.JudgeProfile `json:"judge_profile"`
	JudgeInstructions              string                        `json:"judge_instructions"`
	Configuration                  PublicConfiguration           `json:"configuration"`
}

func PrepareRun(dataset Dataset, options RunOptions) (RunManifest, error) {
	if options.TopK < 1 || options.TopK > 50 {
		return RunManifest{}, fmt.Errorf("top_k must be between 1 and 50")
	}
	answerK := options.TopK
	if options.AnswerK != nil {
		answerK = *options.AnswerK
	}
	if answerK < 1 || answerK > options.TopK {
		return RunManifest{}, fmt.Errorf("answer_k must be between 1 and top_k")
	}
	if options.OperationRetries < 1 {
		return RunManifest{}, fmt.Errorf("operation_retries must be positive")
	}
	if options.RerankMode == "" {
		options.RerankMode = RerankNone
	}
	if options.RerankMode != RerankNone && options.RerankMode != RerankLLM {
		return RunManifest{}, fmt.Errorf("unsupported LoCoMo rerank mode %q", options.RerankMode)
	}
	if options.JudgeProfile == "" {
		options.JudgeProfile = benchmarkprompts.StrictJudge
	}
	answerVersion, err := benchmarkprompts.AnswerPolicyVersion(
		options.AnswerSourceContent, options.AnswerInferenceAware, options.AnswerUnknownFallbackInference,
	)
	if err != nil {
		return RunManifest{}, err
	}
	_, judgeVersion, err := benchmarkprompts.JudgeInstructions(options.JudgeProfile)
	if err != nil {
		return RunManifest{}, err
	}
	runID, err := NormalizeRunID(options.RunID)
	if err != nil {
		return RunManifest{}, err
	}
	categories := slices.Clone(options.Categories)
	if categories == nil {
		categories = []int{1, 2, 3, 4}
	}
	selection := Selection{
		Categories: categories, ConversationLimit: cloneInt(options.ConversationLimit), QuestionLimit: cloneInt(options.QuestionLimit),
	}
	selectedQuestions := dataset.SelectedQuestions(selection)
	selectedConversations := len(dataset.conversations)
	if options.ConversationLimit != nil && *options.ConversationLimit < selectedConversations {
		selectedConversations = max(*options.ConversationLimit, 0)
	}
	schema := "powercontext.benchmark.locomo.run.v5"
	if options.AnswerInferenceAware {
		schema = "powercontext.benchmark.locomo.run.v6"
	}
	if options.AnswerUnknownFallbackInference {
		schema = "powercontext.benchmark.locomo.run.v7"
	}
	var rerankInstructions *string
	if options.RerankMode == RerankLLM {
		version := prompts.RerankVersion
		rerankInstructions = &version
	}
	manifest := RunManifest{
		Schema: schema, RunID: runID, DatasetPath: dataset.path, DatasetSHA256: dataset.sha256,
		DatasetConversationCount: len(dataset.conversations), DatasetSessionCount: len(dataset.Sessions()),
		DatasetQuestionCount: len(dataset.Questions()), SelectedConversationCount: selectedConversations,
		SelectedQuestionCount: len(selectedQuestions), Categories: categories, TopK: options.TopK,
		CandidateK: options.TopK, AnswerK: answerK, RerankMode: options.RerankMode,
		RerankInstructions: rerankInstructions, AnswerSourceContent: options.AnswerSourceContent,
		AnswerInferenceAware:           options.AnswerInferenceAware,
		AnswerUnknownFallbackInference: options.AnswerUnknownFallbackInference,
		ConversationLimit:              cloneInt(options.ConversationLimit), QuestionLimit: cloneInt(options.QuestionLimit),
		OperationRetries: options.OperationRetries, GenerationTemperature: BenchmarkTemperature,
		Ingestion: "source-capture-and-memory-extraction", RetrievalMode: "hybrid",
		AnswerInstructions: answerVersion, JudgeProfile: options.JudgeProfile,
		JudgeInstructions: judgeVersion, Configuration: options.Configuration,
	}
	if options.AnswerUnknownFallbackInference {
		manifest.AnswerFallbackTrigger = "normalized-answer-equals-unknown"
		manifest.DirectAnswerInstructions = benchmarkprompts.AnswerSourceVersion
		manifest.FallbackAnswerInstructions = benchmarkprompts.AnswerSourceInferenceVersion
	}
	if err := writeOrCompareJSON(filepath.Join(options.OutputDirectory, "run.json"), manifest, "run manifest does not match requested benchmark"); err != nil {
		return RunManifest{}, err
	}
	return cloneRunManifest(manifest), nil
}

type RejudgeOptions struct {
	SourceDirectory  string
	OutputDirectory  string
	RunID            string
	JudgeModel       string
	JudgeProfile     benchmarkprompts.JudgeProfile
	OperationRetries int
}

type RejudgeSource struct {
	Directory                  string         `json:"directory"`
	RunID                      any            `json:"run_id"`
	ObservationsSHA256         string         `json:"observations_sha256"`
	ObservationCount           int            `json:"observation_count"`
	SuccessfulObservationCount int            `json:"successful_observation_count"`
	AnswerModel                string         `json:"answer_model"`
	AnswerContract             map[string]any `json:"answer_contract"`
	MemoryConfiguration        any            `json:"memory_configuration"`
	PreviousJudgeProfile       any            `json:"previous_judge_profile"`
	PreviousJudgeInstructions  any            `json:"previous_judge_instructions"`
}

type RejudgeManifest struct {
	Schema                string                        `json:"schema"`
	RunID                 string                        `json:"run_id"`
	DatasetPath           string                        `json:"dataset_path"`
	DatasetSHA256         string                        `json:"dataset_sha256"`
	SelectedQuestionCount int                           `json:"selected_question_count"`
	Source                RejudgeSource                 `json:"source"`
	JudgeModel            string                        `json:"judge_model"`
	JudgeTemperature      float64                       `json:"judge_temperature"`
	JudgeProfile          benchmarkprompts.JudgeProfile `json:"judge_profile"`
	JudgeInstructions     string                        `json:"judge_instructions"`
	OperationRetries      int                           `json:"operation_retries"`
}

func PrepareRejudge(dataset Dataset, options RejudgeOptions) (RejudgeManifest, error) {
	if strings.TrimSpace(options.JudgeModel) == "" {
		return RejudgeManifest{}, fmt.Errorf("judge_model must not be empty")
	}
	if options.OperationRetries < 1 {
		return RejudgeManifest{}, fmt.Errorf("operation_retries must be positive")
	}
	if options.JudgeProfile == "" {
		options.JudgeProfile = benchmarkprompts.TopicalJudge
	}
	_, judgeVersion, err := benchmarkprompts.JudgeInstructions(options.JudgeProfile)
	if err != nil {
		return RejudgeManifest{}, err
	}
	sourceManifestPath := filepath.Join(options.SourceDirectory, "run.json")
	sourceObservationsPath := filepath.Join(options.SourceDirectory, "observations.jsonl")
	sourceManifest, err := readJSONObject(sourceManifestPath)
	if err != nil {
		return RejudgeManifest{}, err
	}
	if sourceManifest["dataset_sha256"] != dataset.sha256 {
		return RejudgeManifest{}, fmt.Errorf("source run dataset does not match the requested dataset")
	}
	selected, err := selectedFromSourceManifest(dataset, sourceManifest)
	if err != nil {
		return RejudgeManifest{}, err
	}
	observations, err := readObservations(sourceObservationsPath)
	if err != nil {
		return RejudgeManifest{}, err
	}
	if err := validateSourceObservations(selected, observations); err != nil {
		return RejudgeManifest{}, err
	}
	configuration, ok := sourceManifest["configuration"].(map[string]any)
	if !ok {
		return RejudgeManifest{}, fmt.Errorf("source run does not identify its answer model")
	}
	answerModel, ok := configuration["generation_model"].(string)
	if !ok || answerModel == "" {
		return RejudgeManifest{}, fmt.Errorf("source run does not identify its answer model")
	}
	runID, err := NormalizeRunID(options.RunID)
	if err != nil {
		return RejudgeManifest{}, err
	}
	sourceDirectory, err := filepath.Abs(options.SourceDirectory)
	if err != nil {
		return RejudgeManifest{}, fmt.Errorf("resolve source directory: %w", err)
	}
	answerContract := make(map[string]any)
	for _, key := range []string{
		"top_k", "candidate_k", "answer_k", "rerank_mode", "rerank_instructions", "answer_source_content",
		"answer_inference_aware", "answer_unknown_fallback_inference", "answer_fallback_trigger",
		"direct_answer_instructions", "fallback_answer_instructions", "answer_instructions", "categories",
		"conversation_limit", "question_limit",
	} {
		if value, exists := sourceManifest[key]; exists {
			answerContract[key] = cloneJSONValue(value)
		}
	}
	digest, err := fileSHA256(sourceObservationsPath)
	if err != nil {
		return RejudgeManifest{}, err
	}
	successful := 0
	for _, observation := range observations {
		if observation["status"] == "ok" {
			successful++
		}
	}
	manifest := RejudgeManifest{
		Schema: "powercontext.benchmark.locomo.rejudge.run.v1", RunID: runID,
		DatasetPath: dataset.path, DatasetSHA256: dataset.sha256, SelectedQuestionCount: len(selected),
		Source: RejudgeSource{
			Directory: sourceDirectory, RunID: cloneJSONValue(sourceManifest["run_id"]), ObservationsSHA256: digest,
			ObservationCount: len(observations), SuccessfulObservationCount: successful, AnswerModel: answerModel,
			AnswerContract: answerContract, MemoryConfiguration: cloneJSONValue(sourceManifest["configuration"]),
			PreviousJudgeProfile:      cloneJSONValue(sourceManifest["judge_profile"]),
			PreviousJudgeInstructions: cloneJSONValue(sourceManifest["judge_instructions"]),
		},
		JudgeModel: options.JudgeModel, JudgeTemperature: BenchmarkTemperature, JudgeProfile: options.JudgeProfile,
		JudgeInstructions: judgeVersion, OperationRetries: options.OperationRetries,
	}
	if err := writeOrCompareJSON(filepath.Join(options.OutputDirectory, "run.json"), manifest, "rejudge manifest does not match requested benchmark"); err != nil {
		return RejudgeManifest{}, err
	}
	return manifest, nil
}

func NormalizeRunID(value string) (string, error) {
	normalized := strings.Trim(invalidRunID.ReplaceAllString(strings.TrimSpace(value), "-"), "-")
	if normalized == "" {
		return "", fmt.Errorf("run_id must contain letters or digits")
	}
	if len(normalized) > 160 {
		return "", fmt.Errorf("run_id must not exceed 160 normalized characters")
	}
	return normalized, nil
}

func ScopeID(runID, sampleID string) (string, error) {
	normalized, err := NormalizeRunID(runID)
	if err != nil {
		return "", err
	}
	return "benchmark:locomo:" + normalized + ":" + sampleID, nil
}

func selectedFromSourceManifest(dataset Dataset, manifest map[string]any) ([]Question, error) {
	categories, err := intSlice(manifest["categories"])
	if err != nil {
		return nil, fmt.Errorf("source run categories: %w", err)
	}
	conversationLimit, err := optionalInt(manifest["conversation_limit"])
	if err != nil {
		return nil, fmt.Errorf("source run conversation_limit: %w", err)
	}
	questionLimit, err := optionalInt(manifest["question_limit"])
	if err != nil {
		return nil, fmt.Errorf("source run question_limit: %w", err)
	}
	selected := dataset.SelectedQuestions(Selection{
		Categories: categories, ConversationLimit: conversationLimit, QuestionLimit: questionLimit,
	})
	expected, err := requiredInt(manifest["selected_question_count"])
	if err != nil || expected != len(selected) {
		return nil, fmt.Errorf("source run selection does not match the requested dataset")
	}
	return selected, nil
}

func validateSourceObservations(selected []Question, observations map[string]map[string]any) error {
	for _, question := range selected {
		observation, ok := observations[question.id]
		if !ok {
			return fmt.Errorf("source run is missing question %s", question.id)
		}
		if observation["question"] != question.question || scalarText(observation["gold_answer"]) != question.answer {
			return fmt.Errorf("source observation does not match dataset question %s", question.id)
		}
	}
	return nil
}

func readObservations(path string) (map[string]map[string]any, error) {
	stream, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	result := make(map[string]map[string]any)
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 10*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.UseNumber()
		var value map[string]any
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("decode observation line %d: %w", lineNumber, err)
		}
		questionID, ok := value["question_id"].(string)
		if !ok {
			return nil, fmt.Errorf("observation line %d has no question ID", lineNumber)
		}
		result[questionID] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func readJSONObject(path string) (map[string]any, error) {
	stream, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = stream.Close() }()
	decoder := json.NewDecoder(stream)
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func writeOrCompareJSON(path string, value any, mismatch string) error {
	payload, err := stableJSON(value)
	if err != nil {
		return err
	}
	observed, err := os.ReadFile(path)
	if err == nil {
		equal, compareErr := equivalentJSON(observed, payload)
		if compareErr != nil {
			return compareErr
		}
		if !equal {
			return fmt.Errorf("%s: %s", mismatch, path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func stableJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var dynamic any
	if err := decoder.Decode(&dynamic); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(dynamic); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func equivalentJSON(left, right []byte) (bool, error) {
	decode := func(value []byte) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(value))
		decoder.UseNumber()
		var result any
		err := decoder.Decode(&result)
		return result, err
	}
	leftValue, err := decode(left)
	if err != nil {
		return false, err
	}
	rightValue, err := decode(right)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(leftValue, rightValue), nil
}

func fileSHA256(path string) (string, error) {
	stream, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = stream.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, stream); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func intSlice(value any) ([]int, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("must be an array")
	}
	result := make([]int, 0, len(values))
	for _, value := range values {
		integer, err := requiredInt(value)
		if err != nil {
			return nil, err
		}
		result = append(result, integer)
	}
	return result, nil
}

func optionalInt(value any) (*int, error) {
	if value == nil {
		return nil, nil
	}
	integer, err := requiredInt(value)
	if err != nil {
		return nil, err
	}
	return &integer, nil
}

func requiredInt(value any) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("must be an integer")
	}
	integer, err := number.Int64()
	if err != nil {
		return 0, fmt.Errorf("must be an integer")
	}
	return int(integer), nil
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneRunManifest(value RunManifest) RunManifest {
	value.Categories = slices.Clone(value.Categories)
	value.RerankInstructions = cloneString(value.RerankInstructions)
	value.ConversationLimit = cloneInt(value.ConversationLimit)
	value.QuestionLimit = cloneInt(value.QuestionLimit)
	return value
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case []any:
		result := make([]any, len(typed))
		for i := range typed {
			result[i] = cloneJSONValue(typed[i])
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = cloneJSONValue(item)
		}
		return result
	default:
		return typed
	}
}
