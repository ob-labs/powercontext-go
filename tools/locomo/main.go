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

// Command locomo runs the long-lived, credentialed LoCoMo evaluation outside
// the production PowerContext binary. It composes the same Server Runtime and
// provider adapters while keeping benchmark concerns out of the public CLI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/inference"
	"github.com/ob-labs/powercontext-go/internal/benchmark/locomo"
	benchmarkprompts "github.com/ob-labs/powercontext-go/internal/benchmark/locomo/prompts"
)

const defaultDataset = "internal/benchmark/locomo/testdata/locomo10.json"

func main() {
	if err := execute(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "locomo:", err)
		os.Exit(1)
	}
}

func execute(ctx context.Context, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: go run ./tools/locomo <inspect|run|rejudge> [flags]")
	}
	switch arguments[0] {
	case "inspect":
		return inspectCommand(arguments[1:])
	case "run":
		return runCommand(ctx, arguments[1:])
	case "rejudge":
		return rejudgeCommand(ctx, arguments[1:])
	default:
		return fmt.Errorf("unknown LoCoMo command %q", arguments[0])
	}
}

func inspectCommand(arguments []string) error {
	flags := flag.NewFlagSet("locomo inspect", flag.ContinueOnError)
	datasetPath := flags.String("dataset", defaultDataset, "LoCoMo dataset path")
	environmentPath := flags.String("env-file", ".env", "PowerContext environment file")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("inspect accepts no positional arguments")
	}
	dataset, err := locomo.Load(*datasetPath)
	if err != nil {
		return err
	}
	config, err := loadConfiguration(*environmentPath)
	if err != nil {
		return err
	}
	configuration, err := publicConfiguration(config)
	if err != nil {
		return err
	}
	categories := map[string]int{"1": 0, "2": 0, "3": 0, "4": 0, "5": 0}
	turns := 0
	for _, conversation := range dataset.Conversations() {
		for _, session := range conversation.Sessions() {
			turns += len(session.Turns())
		}
	}
	for _, question := range dataset.Questions() {
		categories[strconv.Itoa(question.Category())]++
	}
	return writeStdoutJSON(map[string]any{
		"dataset": map[string]any{
			"path": dataset.Path(), "sha256": dataset.SHA256(),
			"conversations": len(dataset.Conversations()), "sessions": len(dataset.Sessions()),
			"turns": turns, "questions": len(dataset.Questions()),
			"scored_questions":      len(dataset.SelectedQuestions(locomo.DefaultSelection())),
			"questions_by_category": categories,
		},
		"configuration": configuration,
	})
}

type runFlags struct {
	datasetPath                    string
	environmentPath                string
	runID                          string
	outputDirectory                string
	topK                           int
	answerK                        int
	rerankMode                     string
	answerSourceContent            bool
	answerInferenceAware           bool
	answerUnknownFallbackInference bool
	judgeProfile                   string
	memoryExtractionProfile        string
	categories                     string
	conversationLimit              int
	questionLimit                  int
	ingestConcurrency              int
	evaluateConcurrency            int
	operationRetries               int
	skipIngestion                  bool
	skipEvaluation                 bool
	keepErrors                     bool
}

func runCommand(ctx context.Context, arguments []string) error {
	flags, err := parseRunFlags(arguments)
	if err != nil {
		return err
	}
	if flags.skipIngestion && flags.skipEvaluation {
		return errors.New("cannot skip both ingestion and evaluation")
	}
	dataset, err := locomo.Load(flags.datasetPath)
	if err != nil {
		return err
	}
	config, err := loadConfiguration(flags.environmentPath)
	if err != nil {
		return err
	}
	if flags.memoryExtractionProfile != "" {
		config.Runtime.MemoryExtractionProfile = memory.ExtractionProfile(flags.memoryExtractionProfile)
	}
	configuration, err := publicConfiguration(config)
	if err != nil {
		return err
	}
	runID := flags.runID
	if runID == "" {
		runID = time.Now().UTC().Format("locomo-20060102T150405Z")
	}
	runID, err = locomo.NormalizeRunID(runID)
	if err != nil {
		return err
	}
	outputDirectory := flags.outputDirectory
	if outputDirectory == "" {
		outputDirectory = filepath.Join("benchmark", "locomo", "results", runID)
	}
	outputDirectory, err = filepath.Abs(outputDirectory)
	if err != nil {
		return err
	}
	categories, err := parseCategories(flags.categories)
	if err != nil {
		return err
	}
	answerK := flags.answerK
	if answerK == 0 {
		answerK = flags.topK
	}
	rerank := locomo.RerankMode(flags.rerankMode)
	judgeProfile := benchmarkprompts.JudgeProfile(flags.judgeProfile)
	conversationLimit := optionalPositive(flags.conversationLimit)
	questionLimit := optionalPositive(flags.questionLimit)
	manifest, err := locomo.PrepareRun(dataset, locomo.RunOptions{
		RunID: runID, OutputDirectory: outputDirectory, TopK: flags.topK, AnswerK: &answerK,
		RerankMode: rerank, AnswerSourceContent: flags.answerSourceContent,
		AnswerInferenceAware:           flags.answerInferenceAware,
		AnswerUnknownFallbackInference: flags.answerUnknownFallbackInference,
		JudgeProfile:                   judgeProfile, Categories: categories,
		ConversationLimit: conversationLimit, QuestionLimit: questionLimit,
		OperationRetries: flags.operationRetries, Configuration: configuration,
	})
	if err != nil {
		return err
	}
	if writeErr := writeStdoutJSON(map[string]any{"run": manifest, "output_directory": outputDirectory}); writeErr != nil {
		return writeErr
	}
	evaluationOptions := locomo.EvaluateOptions{
		RunID: runID, OutputDirectory: outputDirectory, TopK: flags.topK, AnswerK: answerK,
		RerankMode: rerank, AnswerSourceContent: flags.answerSourceContent,
		AnswerInferenceAware:           flags.answerInferenceAware,
		AnswerUnknownFallbackInference: flags.answerUnknownFallbackInference,
		JudgeProfile:                   judgeProfile, Categories: categories,
		ConversationLimit: conversationLimit, QuestionLimit: questionLimit,
		Concurrency: flags.evaluateConcurrency, OperationRetries: flags.operationRetries,
		RetryErrors: !flags.keepErrors,
	}
	if flags.skipIngestion && !flags.skipEvaluation {
		pending, pendingErr := locomo.PendingEvaluationCount(
			dataset, outputDirectory, categories, conversationLimit, questionLimit, !flags.keepErrors,
		)
		if pendingErr != nil {
			return pendingErr
		}
		if pending == 0 {
			report, evaluateErr := locomo.Evaluate(ctx, dataset, nil, evaluationOptions)
			if evaluateErr != nil {
				return evaluateErr
			}
			return writeStdoutJSON(map[string]any{"summary": report})
		}
	}

	applicationRerank := rerank
	if flags.skipEvaluation {
		applicationRerank = locomo.RerankNone
	}
	application, err := openBenchmarkApplication(ctx, config, applicationRerank, flags.topK, !flags.skipEvaluation)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = application.Close(closeCtx)
	}()
	progress := func(value string) { fmt.Fprintln(os.Stderr, value) }
	if !flags.skipIngestion {
		report, ingestErr := locomo.Ingest(ctx, dataset, application.operations, locomo.IngestOptions{
			RunID: runID, OutputDirectory: outputDirectory, DatabaseKind: config.Database.Kind,
			ConversationLimit: conversationLimit, Concurrency: flags.ingestConcurrency,
			OperationRetries: flags.operationRetries, Progress: progress,
		})
		if ingestErr != nil {
			return ingestErr
		}
		if writeErr := writeStdoutJSON(map[string]any{"ingestion": report}); writeErr != nil {
			return writeErr
		}
	}
	if flags.skipEvaluation {
		return nil
	}
	limits, err := inference.NewLimits(config.Inference.GenerationTimeout, config.Inference.GenerationMaxRequests)
	if err != nil {
		return err
	}
	answerGenerator, err := locomo.NewAnswerGenerator(
		application.model, &limits, flags.answerSourceContent, flags.answerInferenceAware,
	)
	if err != nil {
		return err
	}
	var fallbackGenerator inference.StructuredGenerator[locomo.AnswerInput, locomo.AnswerOutput]
	if flags.answerUnknownFallbackInference {
		fallbackGenerator, err = locomo.NewAnswerGenerator(application.model, &limits, true, true)
		if err != nil {
			return err
		}
	}
	judgeGenerator, err := locomo.NewJudgeGenerator(application.model, &limits, judgeProfile)
	if err != nil {
		return err
	}
	evaluationOptions.AnswerGenerator = answerGenerator
	evaluationOptions.FallbackAnswerGenerator = fallbackGenerator
	evaluationOptions.JudgeGenerator = judgeGenerator
	evaluationOptions.Progress = progress
	report, err := locomo.Evaluate(ctx, dataset, application.operations, evaluationOptions)
	if err != nil {
		return err
	}
	return writeStdoutJSON(map[string]any{"summary": report})
}

func parseRunFlags(arguments []string) (runFlags, error) {
	var value runFlags
	flags := flag.NewFlagSet("locomo run", flag.ContinueOnError)
	flags.StringVar(&value.datasetPath, "dataset", defaultDataset, "LoCoMo dataset path")
	flags.StringVar(&value.environmentPath, "env-file", ".env", "PowerContext environment file")
	flags.StringVar(&value.runID, "run-id", "", "stable database and result namespace")
	flags.StringVar(&value.outputDirectory, "output-directory", "", "result directory")
	flags.IntVar(&value.topK, "top-k", 30, "coarse retrieval candidate count")
	flags.IntVar(&value.answerK, "answer-k", 0, "maximum answer-context memories; defaults to top-k")
	flags.StringVar(&value.rerankMode, "rerank-mode", string(locomo.RerankNone), "none or llm")
	flags.BoolVar(&value.answerSourceContent, "answer-source-content", false, "expand exact cited Source sessions")
	flags.BoolVar(&value.answerInferenceAware, "answer-inference-aware", false, "use inference-aware answers")
	flags.BoolVar(&value.answerUnknownFallbackInference, "answer-unknown-fallback-inference", false, "retry exact Unknown answers")
	flags.StringVar(&value.judgeProfile, "judge-profile", string(benchmarkprompts.StrictJudge), "strict or topical")
	flags.StringVar(&value.memoryExtractionProfile, "memory-extraction-profile", "", "coding or conversation")
	flags.StringVar(&value.categories, "categories", "1,2,3,4", "comma-separated categories")
	flags.IntVar(&value.conversationLimit, "conversation-limit", 0, "optional positive conversation limit")
	flags.IntVar(&value.questionLimit, "question-limit", 0, "optional positive question limit")
	flags.IntVar(&value.ingestConcurrency, "ingest-concurrency", 4, "ingestion workers")
	flags.IntVar(&value.evaluateConcurrency, "evaluate-concurrency", 8, "evaluation workers")
	flags.IntVar(&value.operationRetries, "operation-retries", 3, "transient inference attempts")
	flags.BoolVar(&value.skipIngestion, "skip-ingestion", false, "reuse an ingested namespace")
	flags.BoolVar(&value.skipEvaluation, "skip-evaluation", false, "ingest without evaluating")
	flags.BoolVar(&value.keepErrors, "keep-errors", false, "do not retry checkpointed errors")
	if err := flags.Parse(arguments); err != nil {
		return runFlags{}, err
	}
	if flags.NArg() != 0 {
		return runFlags{}, errors.New("run accepts no positional arguments")
	}
	explicit := make(map[string]bool)
	flags.Visit(func(item *flag.Flag) { explicit[item.Name] = true })
	if value.topK < 1 || value.topK > 50 || value.answerK < 0 || value.answerK > value.topK ||
		(explicit["answer-k"] && value.answerK == 0) {
		return runFlags{}, errors.New("top-k must be 1..50 and answer-k must not exceed it")
	}
	if value.conversationLimit < 0 || value.questionLimit < 0 || value.ingestConcurrency < 1 ||
		value.evaluateConcurrency < 1 || value.operationRetries < 1 ||
		(explicit["conversation-limit"] && value.conversationLimit == 0) ||
		(explicit["question-limit"] && value.questionLimit == 0) {
		return runFlags{}, errors.New("limits, concurrency, and operation-retries must be positive")
	}
	if value.answerInferenceAware && value.answerUnknownFallbackInference {
		return runFlags{}, errors.New("answer inference modes are mutually exclusive")
	}
	return value, nil
}

func parseCategories(value string) ([]int, error) {
	seen := make(map[int]struct{})
	result := make([]int, 0, 5)
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		category, err := strconv.Atoi(item)
		if err != nil || category < 1 || category > 5 {
			return nil, errors.New("categories must be selected from 1,2,3,4,5")
		}
		if _, exists := seen[category]; !exists {
			seen[category] = struct{}{}
			result = append(result, category)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("categories must not be empty")
	}
	return result, nil
}

func optionalPositive(value int) *int {
	if value == 0 {
		return nil
	}
	copy := value
	return &copy
}

func writeStdoutJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
