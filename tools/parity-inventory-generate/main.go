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

// Command parity-inventory-generate builds the 812-case Python test
// inventory at the active parity target recorded in
// test/conformance/parity-contract.json. It walks an upstream checkout that
// must sit exactly at the target commit, merges the frozen Oracle mappings
// inherited from traceability.json with the delta mode assignments from
// parity-inventory-rules.json, and emits a deterministic
// test/conformance/parity-inventory.json. Evidence references are resolved
// against real test declarations before output is accepted.
package main

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type parityContract struct {
	SchemaVersion int `json:"schema_version"`
	Upstream      struct {
		Repository    string `json:"repository"`
		URL           string `json:"url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"upstream"`
	FrozenOracle struct {
		Commit            string `json:"commit"`
		Baseline          string `json:"baseline"`
		EvidenceDirectory string `json:"evidence_directory"`
	} `json:"frozen_release_oracle"`
	ReleaseTarget struct {
		Tag           string `json:"tag"`
		Version       string `json:"version"`
		Commit        string `json:"commit"`
		TestCaseCount int    `json:"test_case_count"`
		TestFileCount int    `json:"test_file_count"`
		Wheel         struct {
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
		} `json:"wheel"`
		Sdist struct {
			Filename string `json:"filename"`
			SHA256   string `json:"sha256"`
		} `json:"sdist"`
	} `json:"release_target"`
	ExactTargetSHA      string `json:"exact_target_sha"`
	TargetTestCaseCount int    `json:"target_test_case_count"`
	ActiveParityTarget  string `json:"active_parity_target"`
}

type traceabilityEntry struct {
	Python   pythonTest `json:"python"`
	Mode     string     `json:"mode"`
	Evidence []evidence `json:"evidence"`
}

type traceabilityTable struct {
	OracleCommit string              `json:"oracle_commit"`
	Entries      []traceabilityEntry `json:"entries"`
}

type pythonTest struct {
	File string `json:"file"`
	Name string `json:"name"`
	Line int    `json:"line"`
}

type evidence struct {
	Kind string `json:"kind"`
	File string `json:"file"`
	Test string `json:"test"`
}

type ruleSet struct {
	SchemaVersion int                 `json:"schema_version"`
	Comment       string              `json:"comment"`
	Files         map[string]fileRule `json:"files"`
}

type fileRule struct {
	Mode   string              `json:"mode"`
	Reason string              `json:"reason"`
	Cases  map[string]caseRule `json:"cases,omitempty"`
}

type caseRule struct {
	Mode     string   `json:"mode,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Evidence []string `json:"evidence,omitempty"`
}

type inventoryEntry struct {
	Python   pythonTest `json:"python"`
	Mode     string     `json:"mode"`
	Status   string     `json:"status"`
	Source   string     `json:"source"`
	Evidence []evidence `json:"evidence,omitempty"`
}

type inventory struct {
	SchemaVersion       int              `json:"schema_version"`
	TargetCommit        string           `json:"target_commit"`
	OracleCommit        string           `json:"oracle_commit"`
	PythonTestFileCount int              `json:"python_test_file_count"`
	PythonTestCaseCount int              `json:"python_test_case_count"`
	MappedCaseCount     int              `json:"mapped_case_count"`
	PendingCaseCount    int              `json:"pending_case_count"`
	Entries             []inventoryEntry `json:"entries"`
}

var (
	goTestDeclaration = regexp.MustCompile(`^func\s+(Test[A-Za-z0-9_]+)\s*\(`)
	pyTestDeclaration = regexp.MustCompile(`^\s*(?:async\s+)?def\s+(test_[A-Za-z0-9_]+)\s*\(`)
)

const (
	modeGoPort       = "go-port"
	modeRetainedHost = "retained-host"
	modeCrossLayer   = "cross-layer"
	statusMapped     = "mapped"
	statusPending    = "pending"
	sourceOracle     = "oracle-traceability"
	sourceRules      = "rules"
)

func main() {
	contractPath := flag.String("contract", "test/conformance/parity-contract.json", "parity contract")
	traceabilityPath := flag.String("traceability", "test/conformance/traceability.json", "frozen Oracle traceability table")
	rulesPath := flag.String("rules", "test/conformance/parity-inventory-rules.json", "parity inventory rules")
	outputPath := flag.String("output", "test/conformance/parity-inventory.json", "generated parity inventory")
	upstream := flag.String("upstream", "", "upstream Python checkout pinned at the contract target SHA (required)")
	previousUpstream := flag.String("previous-upstream", "", "previous Python target checkout used to verify the reviewed target delta")
	releaseUpstream := flag.String("release-upstream", "", "release Python target checkout used to verify the reviewed target delta")
	deltaLedgerPath := flag.String("delta-ledger", "test/conformance/target-delta.json", "reviewed target delta ledger")
	checkDelta := flag.Bool("check-delta", false, "verify the reviewed previous-to-release target delta")
	root := flag.String("root", ".", "Go repository root")
	check := flag.Bool("check", false, "verify generated output without rewriting")
	flag.Parse()
	cleanRoot := filepath.Clean(*root)
	if err := run(cleanRoot, *contractPath, *traceabilityPath, *rulesPath, *outputPath, *upstream, *check); err != nil {
		fmt.Fprintln(os.Stderr, "parity-inventory-generate:", err)
		os.Exit(1)
	}
	if *checkDelta {
		if *previousUpstream == "" || *releaseUpstream == "" {
			fmt.Fprintln(os.Stderr, "parity-inventory-generate: -check-delta requires -previous-upstream and -release-upstream")
			os.Exit(1)
		}
		if err := checkTargetDelta(
			cleanRoot,
			resolveOutputPath(cleanRoot, *contractPath),
			resolveOutputPath(cleanRoot, *deltaLedgerPath),
			filepath.Clean(*previousUpstream),
			filepath.Clean(*releaseUpstream),
		); err != nil {
			fmt.Fprintln(os.Stderr, "parity-inventory-generate:", err)
			os.Exit(1)
		}
	}
}

func run(root, contractPath, traceabilityPath, rulesPath, outputPath, upstream string, check bool) error {
	var contract parityContract
	if err := readJSON(filepath.Join(root, contractPath), &contract); err != nil {
		return fmt.Errorf("read parity contract: %w", err)
	}
	if contract.SchemaVersion != 2 || contract.ExactTargetSHA == "" || contract.FrozenOracle.Commit == "" {
		return fmt.Errorf("parity contract is missing the target SHA or frozen Oracle commit")
	}
	if upstream == "" {
		return errors.New("-upstream is required: pass an upstream checkout pinned at the contract target SHA")
	}
	head, err := upstreamHead(upstream)
	if err != nil {
		return err
	}
	if head != contract.ExactTargetSHA {
		return fmt.Errorf("upstream checkout HEAD = %s, want contract target %s", head, contract.ExactTargetSHA)
	}

	var trace traceabilityTable
	if loadErr := readJSONLoose(filepath.Join(root, traceabilityPath), &trace); loadErr != nil {
		return fmt.Errorf("read traceability table: %w", loadErr)
	}
	if trace.OracleCommit != contract.FrozenOracle.Commit {
		return fmt.Errorf("traceability Oracle commit = %s, contract frozen Oracle = %s", trace.OracleCommit, contract.FrozenOracle.Commit)
	}
	inherited := make(map[string]traceabilityEntry, len(trace.Entries))
	for _, entry := range trace.Entries {
		inherited[entry.Python.File+"#"+entry.Python.Name] = entry
	}

	var rules ruleSet
	if loadErr := readJSON(filepath.Join(root, rulesPath), &rules); loadErr != nil {
		return fmt.Errorf("read inventory rules: %w", loadErr)
	}
	if rules.SchemaVersion != 1 {
		return fmt.Errorf("unsupported rules schema %d", rules.SchemaVersion)
	}

	cases, err := extractUpstreamCases(upstream)
	if err != nil {
		return err
	}
	if contract.TargetTestCaseCount != 0 && len(cases) != contract.TargetTestCaseCount {
		return fmt.Errorf("upstream target inventory = %d cases, contract records %d; update the parity contract deliberately", len(cases), contract.TargetTestCaseCount)
	}

	entries := make([]inventoryEntry, 0, len(cases))
	sourceFiles := make(map[string]struct{})
	mapped, pending := 0, 0
	usedRules := make(map[string]struct{})
	for _, test := range cases {
		sourceFiles[test.File] = struct{}{}
		key := test.File + "#" + test.Name
		if frozen, ok := inherited[key]; ok {
			entries = append(entries, inventoryEntry{
				Python: test, Mode: frozen.Mode, Status: statusMapped,
				Source: sourceOracle, Evidence: frozen.Evidence,
			})
			mapped++
			continue
		}
		fileRule, ok := rules.Files[test.File]
		if !ok {
			return fmt.Errorf("no inventory rule for delta file %s (case %s)", test.File, test.Name)
		}
		usedRules[test.File] = struct{}{}
		mode := fileRule.Mode
		var references []string
		if override, ok := fileRule.Cases[test.Name]; ok {
			if override.Mode != "" {
				mode = override.Mode
			}
			references = override.Evidence
		}
		if !validMode(mode) {
			return fmt.Errorf("%s has invalid mode %q", key, mode)
		}
		status := statusPending
		resolved := make([]evidence, 0, len(references))
		for _, reference := range references {
			reference = strings.ReplaceAll(reference, "{python_test}", test.Name)
			value, resolveErr := resolveEvidence(root, reference)
			if resolveErr != nil {
				return fmt.Errorf("%s: %w", key, resolveErr)
			}
			resolved = append(resolved, value)
		}
		if len(resolved) != 0 {
			status = statusMapped
			mapped++
		} else {
			pending++
		}
		entries = append(entries, inventoryEntry{
			Python: test, Mode: mode, Status: status,
			Source: sourceRules, Evidence: resolved,
		})
	}

	for file, fileRule := range rules.Files {
		if _, ok := usedRules[file]; !ok {
			return fmt.Errorf("inventory rules contain unknown target file %s", file)
		}
		caseNames := make(map[string]struct{})
		for _, test := range cases {
			if test.File == file {
				caseNames[test.Name] = struct{}{}
			}
		}
		for name := range fileRule.Cases {
			if _, ok := caseNames[name]; !ok {
				return fmt.Errorf("%s has an override for unknown target case %s", file, name)
			}
			if _, frozen := inherited[file+"#"+name]; frozen {
				return fmt.Errorf("%s#%s is a frozen Oracle case and must not appear in the inventory rules", file, name)
			}
		}
	}

	value := inventory{
		SchemaVersion: 1, TargetCommit: contract.ExactTargetSHA, OracleCommit: contract.FrozenOracle.Commit,
		PythonTestFileCount: len(sourceFiles), PythonTestCaseCount: len(entries),
		MappedCaseCount: mapped, PendingCaseCount: pending,
		Entries: entries,
	}
	payload, err := json.Marshal(&value, json.Deterministic(true), jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	fullOutput := resolveOutputPath(root, outputPath)
	current, err := os.ReadFile(fullOutput)
	if err == nil && bytes.Equal(current, payload) {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if check {
		return fmt.Errorf("%s is stale; regenerate with tools/parity-inventory-generate", outputPath)
	}
	return os.WriteFile(fullOutput, payload, 0o644)
}

func resolveOutputPath(root, output string) string {
	if filepath.IsAbs(output) {
		return output
	}
	return filepath.Join(root, output)
}

func validMode(mode string) bool {
	return mode == modeGoPort || mode == modeRetainedHost || mode == modeCrossLayer
}

func upstreamHead(upstream string) (string, error) {
	cmd := exec.Command("git", "-C", upstream, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve upstream checkout HEAD: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// extractUpstreamCases walks tests/, integrations/, and e2e/ of the upstream
// checkout and returns every Python test case in deterministic order,
// matching the counting convention used for the frozen Oracle manifest.
func extractUpstreamCases(upstream string) ([]pythonTest, error) {
	var cases []pythonTest
	for _, base := range []string{"tests", "integrations", "e2e"} {
		basePath := filepath.Join(upstream, base)
		err := filepath.WalkDir(basePath, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if entry.Name() == "__pycache__" || entry.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(entry.Name(), ".py") {
				return nil
			}
			rel, err := filepath.Rel(upstream, path)
			if err != nil {
				return fmt.Errorf("resolve upstream path %s: %w", path, err)
			}
			found, err := scanPythonCases(path, filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			cases = append(cases, found...)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk upstream %s: %w", base, err)
		}
	}
	slices.SortFunc(cases, func(left, right pythonTest) int {
		if order := cmp.Compare(left.File, right.File); order != 0 {
			return order
		}
		if order := cmp.Compare(left.Line, right.Line); order != 0 {
			return order
		}
		return cmp.Compare(left.Name, right.Name)
	})
	if len(cases) == 0 {
		return nil, fmt.Errorf("upstream checkout %s yielded no Python test cases", upstream)
	}
	return cases, nil
}

func scanPythonCases(path, rel string) ([]pythonTest, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read upstream test %s: %w", rel, err)
	}
	var found []pythonTest
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		match := pyTestDeclaration.FindStringSubmatch(scanner.Text())
		if len(match) == 2 {
			found = append(found, pythonTest{File: rel, Name: match[1], Line: line})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan upstream test %s: %w", rel, err)
	}
	return found, nil
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
		scanner := bufio.NewScanner(bytes.NewReader(contents))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "it('"+name+"'") || strings.HasPrefix(line, `it("`+name+`"`) {
				return true
			}
		}
		return false
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
	return json.Unmarshal(contents, target, json.RejectUnknownMembers(true))
}

func readJSONLoose(path string, target any) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(contents, target)
}
