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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ob-labs/powercontext-go/inference"
	benchmarkprompts "github.com/ob-labs/powercontext-go/internal/benchmark/locomo/prompts"
)

type RejudgeRunOptions struct {
	SourceDirectory  string
	OutputDirectory  string
	RunID            string
	JudgeModel       string
	JudgeProfile     benchmarkprompts.JudgeProfile
	Concurrency      int
	OperationRetries int
	RetryErrors      bool
	JudgeGenerator   inference.StructuredGenerator[JudgeInput, JudgeOutput]
	Clock            func() time.Time
	Progress         Progress
}

type SourceObservationReference struct {
	RunID            string   `json:"run_id"`
	Schema           string   `json:"schema"`
	PreviousLLMJudge *float64 `json:"previous_llm_judge"`
}

type JudgeRecord struct {
	Model        string                        `json:"model"`
	Profile      benchmarkprompts.JudgeProfile `json:"profile"`
	Instructions string                        `json:"instructions"`
	Label        string                        `json:"label"`
}

type RejudgeObservation struct {
	Schema            string                     `json:"schema"`
	QuestionID        string                     `json:"question_id"`
	SampleID          string                     `json:"sample_id"`
	Category          int                        `json:"category"`
	Question          string                     `json:"question"`
	GoldAnswer        string                     `json:"gold_answer"`
	GeneratedAnswer   *string                    `json:"generated_answer"`
	SourceObservation SourceObservationReference `json:"source_observation"`
	Status            string                     `json:"status"`
	Metrics           map[string]float64         `json:"metrics,omitempty"`
	Judge             *JudgeRecord               `json:"judge,omitempty"`
	LatencyMS         map[string]float64         `json:"latency_ms"`
	Usage             map[string]Usage           `json:"usage,omitempty"`
	TransientRetries  map[string]int             `json:"transient_retries,omitempty"`
	ErrorType         string                     `json:"error_type,omitempty"`
	ErrorStage        string                     `json:"error_stage,omitempty"`
}

func (r RejudgeObservation) summaryObservation() Observation {
	generatedAnswer := ""
	if r.GeneratedAnswer != nil {
		generatedAnswer = *r.GeneratedAnswer
	}
	return Observation{
		Category: r.Category, Status: r.Status, GeneratedAnswer: generatedAnswer,
		Metrics: r.Metrics, LatencyMS: r.LatencyMS, Usage: r.Usage, TransientRetries: r.TransientRetries,
	}
}

type RejudgeReport struct {
	Schema            string                        `json:"schema"`
	RunID             string                        `json:"run_id"`
	CompletedAt       string                        `json:"completed_at"`
	QuestionCount     int                           `json:"question_count"`
	Source            RejudgeSource                 `json:"source"`
	JudgeModel        string                        `json:"judge_model"`
	JudgeTemperature  float64                       `json:"judge_temperature"`
	JudgeProfile      benchmarkprompts.JudgeProfile `json:"judge_profile"`
	JudgeInstructions string                        `json:"judge_instructions"`
	Metrics           map[string]Summary            `json:"metrics"`
	Diagnostics       Diagnostics                   `json:"diagnostics"`
	MetricNotes       map[string]string             `json:"metric_notes"`
}

func Rejudge(ctx context.Context, dataset Dataset, options RejudgeRunOptions) (RejudgeReport, error) {
	if options.Concurrency < 1 || options.OperationRetries < 1 {
		return RejudgeReport{}, fmt.Errorf("rejudge concurrency and operation_retries must be positive")
	}
	manifest, err := PrepareRejudge(dataset, RejudgeOptions{
		SourceDirectory: options.SourceDirectory, OutputDirectory: options.OutputDirectory,
		RunID: options.RunID, JudgeModel: options.JudgeModel,
		JudgeProfile: options.JudgeProfile, OperationRetries: options.OperationRetries,
	})
	if err != nil {
		return RejudgeReport{}, err
	}
	sourceManifest, err := readJSONObject(filepath.Join(options.SourceDirectory, "run.json"))
	if err != nil {
		return RejudgeReport{}, err
	}
	selected, err := selectedFromSourceManifest(dataset, sourceManifest)
	if err != nil {
		return RejudgeReport{}, err
	}
	source, err := readObservationRecords(filepath.Join(options.SourceDirectory, "observations.jsonl"))
	if err != nil {
		return RejudgeReport{}, err
	}
	observationsPath := filepath.Join(options.OutputDirectory, "observations.jsonl")
	observed, err := readRejudgeObservations(observationsPath)
	if err != nil {
		return RejudgeReport{}, err
	}
	pending := make([]Question, 0, len(selected))
	for _, question := range selected {
		value, exists := observed[question.ID()]
		if !exists || (options.RetryErrors && value.Status != "ok") {
			pending = append(pending, question)
		}
	}
	progress := options.Progress
	if progress == nil {
		progress = func(string) {}
	}
	progress(fmt.Sprintf("[rejudge] selected=%d resumed=%d pending=%d", len(selected), len(selected)-len(pending), len(pending)))
	if len(pending) == 0 {
		encoded, readErr := os.ReadFile(filepath.Join(options.OutputDirectory, "summary.json"))
		if readErr == nil {
			var completed RejudgeReport
			if err := json.Unmarshal(encoded, &completed); err != nil {
				return RejudgeReport{}, fmt.Errorf("decode completed rejudge summary: %w", err)
			}
			return completed, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return RejudgeReport{}, readErr
		}
	}
	if len(pending) > 0 {
		if options.JudgeGenerator == nil {
			return RejudgeReport{}, fmt.Errorf("judge generator must not be nil while questions are pending")
		}
		values, err := parallelRejudge(ctx, pending, options.Concurrency, func(ctx context.Context, question Question) RejudgeObservation {
			return rejudgeQuestion(ctx, source[question.ID()], fmt.Sprint(sourceManifest["run_id"]), options)
		})
		if err != nil {
			return RejudgeReport{}, err
		}
		for _, value := range values {
			if err := appendJSONLine(observationsPath, value); err != nil {
				return RejudgeReport{}, err
			}
			observed[value.QuestionID] = value
			progress(fmt.Sprintf("[rejudge] recorded %s status=%s", value.QuestionID, value.Status))
		}
	}
	summaryValues := make([]Observation, 0, len(selected))
	for _, question := range selected {
		value, ok := observed[question.ID()]
		if !ok {
			return RejudgeReport{}, fmt.Errorf("rejudge did not produce every selected observation")
		}
		summaryValues = append(summaryValues, value.summaryObservation())
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	_, instructions, err := benchmarkprompts.JudgeInstructions(options.JudgeProfile)
	if err != nil {
		return RejudgeReport{}, err
	}
	report := RejudgeReport{
		Schema: "powercontext.benchmark.locomo.rejudge.summary.v1", RunID: manifest.RunID,
		CompletedAt: pythonUTC(clock()), QuestionCount: len(selected), Source: manifest.Source,
		JudgeModel: options.JudgeModel, JudgeTemperature: BenchmarkTemperature,
		JudgeProfile: options.JudgeProfile, JudgeInstructions: instructions,
		Metrics: SummarizeObservations(summaryValues), Diagnostics: DiagnoseObservations(summaryValues),
		MetricNotes: map[string]string{
			"llm_judge": "Frozen answers are graded by the independently configured judge model.",
			"errors":    "Failed judge requests remain in the denominator and score zero.",
			"latency":   "Only the new independent-judge request is timed; source retrieval and answer latency are excluded.",
		},
	}
	if err := writeJSON(filepath.Join(options.OutputDirectory, "summary.json"), report); err != nil {
		return RejudgeReport{}, err
	}
	if err := writeFileAtomic(filepath.Join(options.OutputDirectory, "summary.md"), []byte(RenderRejudgeSummary(report))); err != nil {
		return RejudgeReport{}, err
	}
	return report, nil
}

func rejudgeQuestion(
	ctx context.Context, source ObservationRecord, sourceRunID string, options RejudgeRunOptions,
) RejudgeObservation {
	started := time.Now()
	var previous *float64
	if value, ok := source.Metrics["llm_judge"]; ok {
		previous = &value
	}
	var generatedAnswer *string
	if source.Status == "ok" || source.GeneratedAnswer != "" {
		value := source.GeneratedAnswer
		generatedAnswer = &value
	}
	base := RejudgeObservation{
		Schema: "powercontext.benchmark.locomo.rejudge.observation.v1", QuestionID: source.QuestionID,
		SampleID: source.SampleID, Category: source.Category, Question: source.Question,
		GoldAnswer: source.GoldAnswer, GeneratedAnswer: generatedAnswer,
		SourceObservation: SourceObservationReference{RunID: sourceRunID, Schema: source.Schema, PreviousLLMJudge: previous},
		Status:            "error", LatencyMS: map[string]float64{},
	}
	if source.Status != "ok" {
		base.ErrorType = "SourceObservationError"
		base.ErrorStage = "source"
		base.LatencyMS["total"] = milliseconds(time.Since(started))
		return base
	}
	result, retries, err := retryTransient(ctx, options.OperationRetries, func(ctx context.Context) (inference.GenerationResult[JudgeOutput], error) {
		return options.JudgeGenerator.Generate(ctx, JudgeInput{
			Question: source.Question, GoldAnswer: source.GoldAnswer, GeneratedAnswer: source.GeneratedAnswer,
		})
	})
	if err != nil {
		base.ErrorType = errorType(err)
		base.ErrorStage = "judge"
		base.LatencyMS["total"] = milliseconds(time.Since(started))
		return base
	}
	latency := milliseconds(time.Since(started))
	metrics := make(map[string]float64, len(source.Metrics))
	for name, value := range source.Metrics {
		metrics[name] = value
	}
	metrics["llm_judge"] = 0
	if result.Output.Label == "CORRECT" {
		metrics["llm_judge"] = 1
	}
	_, instructions, _ := benchmarkprompts.JudgeInstructions(options.JudgeProfile)
	base.Status = "ok"
	base.Metrics = metrics
	base.Judge = &JudgeRecord{
		Model: options.JudgeModel, Profile: options.JudgeProfile, Instructions: instructions, Label: result.Output.Label,
	}
	base.LatencyMS = map[string]float64{"judge": latency, "total": latency}
	base.Usage = map[string]Usage{"judge": benchmarkUsage(result.Usage)}
	base.TransientRetries = map[string]int{"judge": retries}
	return base
}

func readRejudgeObservations(path string) (map[string]RejudgeObservation, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]RejudgeObservation{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	result := make(map[string]RejudgeObservation)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), maxObservationBytes)
	for line := 1; scanner.Scan(); line++ {
		var value RejudgeObservation
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return nil, fmt.Errorf("decode rejudge observation line %d: %w", line, err)
		}
		if value.QuestionID == "" {
			return nil, fmt.Errorf("rejudge observation line %d has no question_id", line)
		}
		result[value.QuestionID] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func parallelRejudge(
	ctx context.Context, values []Question, concurrency int,
	operation func(context.Context, Question) RejudgeObservation,
) ([]RejudgeObservation, error) {
	type job struct {
		index int
		value Question
	}
	jobs := make(chan job)
	result := make([]RejudgeObservation, len(values))
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var workers sync.WaitGroup
	for range min(concurrency, max(len(values), 1)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					cancel(fmt.Errorf("LoCoMo rejudge worker panicked: %v", recovered))
				}
			}()
			for item := range jobs {
				result[item.index] = operation(ctx, item.value)
			}
		}()
	}
send:
	for index, value := range values {
		select {
		case jobs <- job{index: index, value: value}:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	workers.Wait()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func RenderRejudgeSummary(report RejudgeReport) string {
	overall := report.Metrics["overall"]
	answerContract := report.Source.AnswerContract
	lines := []string{
		"# PowerContext LoCoMo independent-judge result", "",
		fmt.Sprintf("- Frozen answer model: `%s`", report.Source.AnswerModel),
		fmt.Sprintf("- Independent judge model: `%s`", report.JudgeModel),
		fmt.Sprintf("- Judge profile: `%s` (`%s`)", report.JudgeProfile, report.JudgeInstructions),
		fmt.Sprintf("- Questions / completed / errors: `%d` / `%d` / `%d`", overall.QuestionCount, overall.CompletedCount, overall.ErrorCount),
		fmt.Sprintf("- LLM-judge accuracy: `%.4f`", overall.Metrics["llm_judge"]),
		fmt.Sprintf("- Exact match / token F1 / reference-set F1: `%.4f` / `%.4f` / `%.4f`", overall.Metrics["exact_match"], overall.Metrics["token_f1"], overall.Metrics["reference_set_f1"]),
		fmt.Sprintf("- Evidence hit / recall: `%.4f` / `%.4f`", overall.Metrics["evidence_hit"], overall.Metrics["evidence_recall"]),
		fmt.Sprintf("- Top K / Answer K / Source expansion: `%v` / `%v` / `%s`", answerContract["top_k"], answerContract["answer_k"], formatPythonBoolValue(answerContract["answer_source_content"])),
		fmt.Sprintf("- Judge latency p50 / p95: `%s` / `%s`", formatLatency(overall.LatencyP50["judge"]), formatLatency(overall.LatencyP95["judge"])),
		"", "| Category | Questions | Judge accuracy | Token F1 | Evidence hit |",
		"| --- | ---: | ---: | ---: | ---: |",
	}
	for _, name := range sortedMetricGroups(report.Metrics) {
		value := report.Metrics[name]
		lines = append(lines, fmt.Sprintf("| %s | %d | %.4f | %.4f | %.4f |",
			name[len("category_"):], value.QuestionCount, value.Metrics["llm_judge"], value.Metrics["token_f1"], value.Metrics["evidence_hit"]))
	}
	lines = append(lines, "", "- "+report.MetricNotes["llm_judge"], "- "+report.MetricNotes["errors"], "")
	return strings.Join(lines, "\n")
}

func formatPythonBoolValue(value any) string {
	if boolean, ok := value.(bool); ok {
		return pythonBool(boolean)
	}
	return fmt.Sprint(value)
}

func formatLatency(value *float64) string {
	if value == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f ms", *value)
}
