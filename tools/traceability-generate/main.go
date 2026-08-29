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

// Command traceability-generate expands the frozen Python test inventory into
// a case-by-case implementation evidence table. Evidence references are
// resolved against real test declarations before output is accepted.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const oracleCommit = "3a6cb0151670eaff7dc0293466edd673124e80da"

type oracleManifest struct {
	OracleCommit  string       `json:"oracle_commit"`
	TestFileCount int          `json:"test_file_count"`
	TestCaseCount int          `json:"test_case_count"`
	Tests         []pythonTest `json:"tests"`
}

type pythonTest struct {
	File string `json:"file"`
	Name string `json:"name"`
	Line int    `json:"line"`
}

type ruleSet struct {
	SchemaVersion               int             `json:"schema_version"`
	CaseSpecificEvidenceMinimum int             `json:"case_specific_evidence_minimum"`
	Files                       map[string]rule `json:"files"`
}

type rule struct {
	Mode               string              `json:"mode"`
	SupportingEvidence []string            `json:"supporting_evidence,omitempty"`
	CaseEvidence       []string            `json:"case_evidence,omitempty"`
	Cases              map[string][]string `json:"cases,omitempty"`
}

type table struct {
	SchemaVersion               int     `json:"schema_version"`
	OracleCommit                string  `json:"oracle_commit"`
	PythonTestFileCount         int     `json:"python_test_file_count"`
	PythonTestCaseCount         int     `json:"python_test_case_count"`
	CaseSpecificEvidenceCount   int     `json:"case_specific_evidence_count"`
	FileSupportingEvidenceCount int     `json:"file_supporting_evidence_count"`
	CaseSpecificEvidenceMinimum int     `json:"case_specific_evidence_minimum"`
	Entries                     []entry `json:"entries"`
}

type entry struct {
	Python        pythonTest `json:"python"`
	Mode          string     `json:"mode"`
	EvidenceLevel string     `json:"evidence_level"`
	Evidence      []evidence `json:"evidence"`
}

type evidence struct {
	Kind string `json:"kind"`
	File string `json:"file"`
	Test string `json:"test"`
}

var (
	goTestDeclaration = regexp.MustCompile(`^func\s+(Test[A-Za-z0-9_]+)\s*\(`)
	pyTestDeclaration = regexp.MustCompile(`^\s*(?:async\s+)?def\s+(test_[A-Za-z0-9_]+)\s*\(`)
)

const (
	caseSpecificEvidence   = "case-specific"
	fileSupportingEvidence = "file-supporting"
)

func main() {
	manifestPath := flag.String("manifest", "test/conformance/testdata/python-v0.0.2/manifest.json", "frozen Oracle manifest")
	rulesPath := flag.String("rules", "test/conformance/traceability-rules.json", "traceability rules")
	outputPath := flag.String("output", "test/conformance/traceability.json", "generated traceability table")
	root := flag.String("root", ".", "Go repository root")
	check := flag.Bool("check", false, "verify generated output without rewriting")
	flag.Parse()
	if err := run(*root, *manifestPath, *rulesPath, *outputPath, *check); err != nil {
		fmt.Fprintln(os.Stderr, "traceability-generate:", err)
		os.Exit(1)
	}
}

func run(root, manifestPath, rulesPath, outputPath string, check bool) error {
	var manifest oracleManifest
	if err := readJSONLoose(manifestPath, &manifest); err != nil {
		return fmt.Errorf("read Oracle manifest: %w", err)
	}
	if manifest.OracleCommit != oracleCommit || len(manifest.Tests) != manifest.TestCaseCount {
		return fmt.Errorf("unexpected Oracle test inventory identity")
	}
	var rules ruleSet
	if err := readJSON(rulesPath, &rules); err != nil {
		return fmt.Errorf("read rules: %w", err)
	}
	if rules.SchemaVersion != 2 {
		return fmt.Errorf("unsupported rules schema %d", rules.SchemaVersion)
	}
	if rules.CaseSpecificEvidenceMinimum < 0 || rules.CaseSpecificEvidenceMinimum > manifest.TestCaseCount {
		return fmt.Errorf("case-specific evidence minimum %d is outside the Oracle inventory", rules.CaseSpecificEvidenceMinimum)
	}
	sourceFiles := make(map[string]struct{}, manifest.TestFileCount)
	entries := make([]entry, 0, len(manifest.Tests))
	caseSpecificCount := 0
	for _, test := range manifest.Tests {
		sourceFiles[test.File] = struct{}{}
		fileRule, ok := rules.Files[test.File]
		if !ok {
			return fmt.Errorf("no traceability rule for %s", test.File)
		}
		if fileRule.Mode != "go-port" && fileRule.Mode != "retained-host" && fileRule.Mode != "cross-layer" {
			return fmt.Errorf("%s has invalid mode %q", test.File, fileRule.Mode)
		}
		references := fileRule.SupportingEvidence
		evidenceLevel := fileSupportingEvidence
		if len(fileRule.CaseEvidence) != 0 {
			references = fileRule.CaseEvidence
			evidenceLevel = caseSpecificEvidence
		}
		if override, ok := fileRule.Cases[test.Name]; ok {
			references = override
			evidenceLevel = caseSpecificEvidence
		}
		if len(references) == 0 {
			return fmt.Errorf("%s#%s has no implementation evidence", test.File, test.Name)
		}
		resolved := make([]evidence, 0, len(references))
		for _, reference := range references {
			reference = strings.ReplaceAll(reference, "{python_test}", test.Name)
			value, err := resolveEvidence(root, reference)
			if err != nil {
				return fmt.Errorf("%s#%s: %w", test.File, test.Name, err)
			}
			resolved = append(resolved, value)
		}
		if evidenceLevel == caseSpecificEvidence {
			caseSpecificCount++
		}
		entries = append(entries, entry{
			Python: test, Mode: fileRule.Mode, EvidenceLevel: evidenceLevel, Evidence: resolved,
		})
	}
	if len(sourceFiles) != manifest.TestFileCount {
		return fmt.Errorf("Oracle file inventory = %d, want %d", len(sourceFiles), manifest.TestFileCount)
	}
	for file, rule := range rules.Files {
		if _, ok := sourceFiles[file]; !ok {
			return fmt.Errorf("traceability rules contain unknown Oracle file %s", file)
		}
		for _, reference := range rule.SupportingEvidence {
			if strings.Contains(reference, "{python_test}") {
				return fmt.Errorf("%s supporting evidence must not use {python_test}; declare case_evidence instead", file)
			}
		}
		for _, reference := range rule.CaseEvidence {
			if !strings.Contains(reference, "{python_test}") {
				return fmt.Errorf("%s case evidence %q must use {python_test}", file, reference)
			}
		}
		caseNames := make(map[string]struct{})
		for _, test := range manifest.Tests {
			if test.File == file {
				caseNames[test.Name] = struct{}{}
			}
		}
		for name := range rule.Cases {
			if _, ok := caseNames[name]; !ok {
				return fmt.Errorf("%s has override for unknown test %s", file, name)
			}
		}
	}
	if caseSpecificCount != rules.CaseSpecificEvidenceMinimum {
		return fmt.Errorf(
			"case-specific evidence count = %d, declared checkpoint = %d; update mappings and checkpoint together",
			caseSpecificCount, rules.CaseSpecificEvidenceMinimum,
		)
	}
	value := table{
		SchemaVersion: 2, OracleCommit: manifest.OracleCommit,
		PythonTestFileCount: len(sourceFiles), PythonTestCaseCount: len(entries),
		CaseSpecificEvidenceCount:   caseSpecificCount,
		FileSupportingEvidenceCount: len(entries) - caseSpecificCount,
		CaseSpecificEvidenceMinimum: rules.CaseSpecificEvidenceMinimum,
		Entries:                     entries,
	}
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	current, err := os.ReadFile(outputPath)
	if err == nil && bytes.Equal(current, payload) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if check {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("traceability table %s does not exist", outputPath)
		}
		return fmt.Errorf("traceability table %s is stale", outputPath)
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outputPath, payload, 0o644)
}

func resolveEvidence(root, reference string) (evidence, error) {
	kind, remainder, ok := strings.Cut(reference, ":")
	if !ok || (kind != "go" && kind != "py" && kind != "ts") {
		return evidence{}, fmt.Errorf("invalid evidence reference %q", reference)
	}
	path, testName, ok := strings.Cut(remainder, "#")
	if !ok || path == "" || testName == "" || strings.Contains(testName, "#") {
		return evidence{}, fmt.Errorf("invalid evidence reference %q", reference)
	}
	clean := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return evidence{}, fmt.Errorf("evidence path escapes repository: %q", path)
	}
	contents, err := os.ReadFile(filepath.Join(root, clean))
	if err != nil {
		return evidence{}, fmt.Errorf("read evidence %s: %w", path, err)
	}
	if !declaresTest(kind, contents, testName) {
		return evidence{}, fmt.Errorf("%s does not declare %s test %q", path, kind, testName)
	}
	return evidence{Kind: kind, File: filepath.ToSlash(clean), Test: testName}, nil
}

func declaresTest(kind string, contents []byte, name string) bool {
	if kind == "ts" {
		text := string(contents)
		return strings.Contains(text, "it('"+name+"'") || strings.Contains(text, `it("`+name+`"`)
	}
	pattern := goTestDeclaration
	if kind == "py" {
		pattern = pyTestDeclaration
	}
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	for scanner.Scan() {
		match := pattern.FindStringSubmatch(scanner.Text())
		if len(match) == 2 && match[1] == name {
			return true
		}
	}
	return false
}

func readJSON(path string, target any) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func readJSONLoose(path string, target any) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(contents, target)
}
