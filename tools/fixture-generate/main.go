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

// Command fixture-generate freezes deterministic metadata from the Python
// PowerContext oracle. It is a development tool and is never linked into the
// production binary.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const oracleCommit = "3a6cb0151670eaff7dc0293466edd673124e80da"

var (
	testPattern   = regexp.MustCompile(`^(?:async\s+)?def\s+(test_[A-Za-z0-9_]+)\s*\(`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

var renderedPromptSpecs = []renderedPromptSpec{
	{"powercontext.memory.extract.v1", "memory/prompts.py", "MEMORY_EXTRACTION_INSTRUCTIONS_VERSION", "MEMORY_EXTRACTION_INSTRUCTIONS"},
	{"powercontext.memory.extract.conversation.v1", "memory/prompts.py", "CONVERSATION_MEMORY_EXTRACTION_INSTRUCTIONS_VERSION", "CONVERSATION_MEMORY_EXTRACTION_INSTRUCTIONS"},
	{"powercontext.memory.rerank.listwise.v1", "memory/reranking.py", "MEMORY_RERANK_INSTRUCTIONS_VERSION", "MEMORY_RERANK_INSTRUCTIONS"},
	{"powercontext.experience.incubate.v1", "experience/prompts.py", "EXPERIENCE_INCUBATION_INSTRUCTIONS_VERSION", "EXPERIENCE_INCUBATION_INSTRUCTIONS"},
	{"powercontext.experience.generate.v1", "experience/prompts.py", "EXPERIENCE_GENERATION_INSTRUCTIONS_VERSION", "EXPERIENCE_GENERATION_INSTRUCTIONS"},
	{"powercontext.skill.generate.v2", "skill/prompts.py", "SKILL_GENERATION_INSTRUCTIONS_VERSION", "SKILL_GENERATION_INSTRUCTIONS"},
	{"powercontext.handoff.generate.v1", "handoff/prompts.py", "HANDOFF_GENERATION_INSTRUCTIONS_VERSION", "HANDOFF_GENERATION_INSTRUCTIONS"},
}

type fixtureConfig struct {
	oracleCommit string
}

type manifest struct {
	SchemaVersion        int               `json:"schema_version"`
	OracleCommit         string            `json:"oracle_commit"`
	OpenAPISHA256        string            `json:"openapi_sha256"`
	SQLiteSchemaSHA256   string            `json:"sqlite_schema_sha256"`
	FixtureSHA256        map[string]string `json:"fixture_sha256"`
	PromptSHA256         map[string]string `json:"prompt_sha256"`
	RenderedPromptSHA256 map[string]string `json:"rendered_prompt_sha256"`
	Tests                []testCase        `json:"tests"`
	TestFileCount        int               `json:"test_file_count"`
	TestCaseCount        int               `json:"test_case_count"`
}

type testCase struct {
	File string `json:"file"`
	Name string `json:"name"`
	Line int    `json:"line"`
}

func main() {
	pythonRoot := flag.String("python", "../powercontext", "path to the frozen Python repository")
	output := flag.String(
		"output",
		"test/conformance/testdata/python-v0.0.2/manifest.json",
		"manifest output path",
	)
	check := flag.Bool("check", false, "verify the committed manifest without rewriting it")
	commit := flag.String("oracle-commit", oracleCommit, "exact Python Oracle commit")
	flag.Parse()

	if err := run(filepath.Clean(*pythonRoot), filepath.Clean(*output), *check, fixtureConfig{oracleCommit: *commit}); err != nil {
		fmt.Fprintln(os.Stderr, "fixture-generate:", err)
		os.Exit(1)
	}
}

func run(pythonRoot, output string, check bool, config fixtureConfig) error {
	if !commitPattern.MatchString(config.oracleCommit) {
		return fmt.Errorf("Oracle commit %q is not a 40-character lowercase SHA-1", config.oracleCommit)
	}
	commit, err := gitOutput(pythonRoot, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if commit != config.oracleCommit {
		return fmt.Errorf("oracle commit is %s, want %s", commit, config.oracleCommit)
	}

	openapiHash, err := fileHash(filepath.Join(pythonRoot, "openapi", "powercontext.yaml"))
	if err != nil {
		return fmt.Errorf("hash OpenAPI: %w", err)
	}
	prompts, err := promptHashes(pythonRoot)
	if err != nil {
		return err
	}
	renderedPrompts, err := renderedPromptHashes(pythonRoot)
	if err != nil {
		return err
	}
	tests, files, err := discoverTests(pythonRoot)
	if err != nil {
		return err
	}
	schemaHash, err := sqliteSchemaHash(filepath.Join(filepath.Dir(output), "authority-rows.json"))
	if err != nil {
		return err
	}
	fixtures, err := fixtureHashes(filepath.Dir(output))
	if err != nil {
		return err
	}

	value := manifest{
		SchemaVersion:        3,
		OracleCommit:         config.oracleCommit,
		OpenAPISHA256:        openapiHash,
		SQLiteSchemaSHA256:   schemaHash,
		FixtureSHA256:        fixtures,
		PromptSHA256:         prompts,
		RenderedPromptSHA256: renderedPrompts,
		Tests:                tests,
		TestFileCount:        files,
		TestCaseCount:        len(tests),
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	if mkdirErr := os.MkdirAll(filepath.Dir(output), 0o755); mkdirErr != nil {
		return fmt.Errorf("create output directory: %w", mkdirErr)
	}
	current, err := os.ReadFile(output)
	if err == nil && bytes.Equal(current, encoded) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing manifest: %w", err)
	}
	if check {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("manifest %s does not exist", output)
		}
		return fmt.Errorf("manifest %s differs from the frozen Oracle", output)
	}
	if err := os.WriteFile(output, encoded, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	return nil
}

func sqliteSchemaHash(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read SQLite authority snapshot: %w", err)
	}
	var snapshot struct {
		SchemaSHA256 string `json:"schema_sha256"`
	}
	if err := json.Unmarshal(contents, &snapshot); err != nil {
		return "", fmt.Errorf("decode SQLite authority snapshot: %w", err)
	}
	if matched, _ := regexp.MatchString(`^[0-9a-f]{64}$`, snapshot.SchemaSHA256); !matched {
		return "", errors.New("SQLite authority snapshot has no valid schema fingerprint")
	}
	return snapshot.SchemaSHA256, nil
}

func fixtureHashes(root string) (map[string]string, error) {
	names := []string{
		"authority-rows.json",
		"authority.db",
		"domain-contract.json",
		"handoff-report-digests.json",
		"provider-matrix.json",
		"scheduler.db",
	}
	result := make(map[string]string, len(names))
	for _, name := range names {
		digest, err := fileHash(filepath.Join(root, name))
		if err != nil {
			return nil, fmt.Errorf("hash fixture %s: %w", name, err)
		}
		result[name] = digest
	}
	return result, nil
}

func gitOutput(root string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func promptHashes(root string) (map[string]string, error) {
	prompts := make(map[string]string)
	sourceRoot := filepath.Join(root, "src", "powercontext", "builtin", "artifacts")
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "prompts.py" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hash, err := fileHash(path)
		if err != nil {
			return err
		}
		prompts[filepath.ToSlash(relative)] = hash
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("discover prompts: %w", err)
	}
	return prompts, nil
}

type renderedPromptSpec struct {
	key         string
	path        string
	versionName string
	promptName  string
}

// renderedPromptHashes extracts the exact runtime values of the frozen
// repository's deliberately simple versioned f-string declarations. This
// narrow parser avoids importing the Python package (and therefore avoids
// making the Oracle depend on a mutable local Python environment).
func renderedPromptHashes(root string) (map[string]string, error) {
	base := filepath.Join(root, "src", "powercontext", "builtin", "artifacts")
	result := make(map[string]string, len(renderedPromptSpecs))
	for _, spec := range renderedPromptSpecs {
		contents, err := os.ReadFile(filepath.Join(base, filepath.FromSlash(spec.path)))
		if err != nil {
			return nil, fmt.Errorf("read rendered prompt %s: %w", spec.key, err)
		}
		versionPattern := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(spec.versionName) + `\s*=\s*"([^"]+)"\s*$`)
		versionMatch := versionPattern.FindSubmatch(contents)
		if len(versionMatch) != 2 || string(versionMatch[1]) != spec.key {
			return nil, fmt.Errorf("rendered prompt %s has an unexpected version declaration", spec.key)
		}
		promptPattern := regexp.MustCompile(`(?s)` + regexp.QuoteMeta(spec.promptName) + `\s*=\s*f"""(.*?)"""\.strip\(\)`)
		promptMatch := promptPattern.FindSubmatch(contents)
		if len(promptMatch) != 2 {
			return nil, fmt.Errorf("rendered prompt %s declaration was not found", spec.key)
		}
		placeholder := "{" + spec.versionName + "}"
		rendered := strings.TrimSpace(strings.ReplaceAll(string(promptMatch[1]), placeholder, spec.key))
		if strings.Contains(rendered, "{") || strings.Contains(rendered, "}") {
			return nil, fmt.Errorf("rendered prompt %s contains an unsupported interpolation", spec.key)
		}
		digest := sha256.Sum256([]byte(rendered))
		result[spec.key] = hex.EncodeToString(digest[:])
	}
	return result, nil
}

func discoverTests(root string) ([]testCase, int, error) {
	var tests []testCase
	files := 0
	for _, relativeRoot := range []string{"tests", "integrations", "e2e"} {
		searchRoot := filepath.Join(root, relativeRoot)
		err := filepath.WalkDir(searchRoot, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".py") {
				return nil
			}
			found, err := testsInFile(root, path)
			if err != nil {
				return err
			}
			if len(found) > 0 {
				files++
				tests = append(tests, found...)
			}
			return nil
		})
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, 0, fmt.Errorf("discover tests in %s: %w", relativeRoot, err)
		}
	}
	slices.SortFunc(tests, func(left, right testCase) int {
		if order := strings.Compare(left.File, right.File); order != 0 {
			return order
		}
		if left.Line < right.Line {
			return -1
		}
		if left.Line > right.Line {
			return 1
		}
		return strings.Compare(left.Name, right.Name)
	})
	return tests, files, nil
}

func testsInFile(root, path string) ([]testCase, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	var tests []testCase
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		match := testPattern.FindStringSubmatch(strings.TrimSpace(scanner.Text()))
		if len(match) == 2 {
			tests = append(tests, testCase{File: filepath.ToSlash(relative), Name: match[1], Line: line})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return tests, nil
}

func fileHash(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:]), nil
}
