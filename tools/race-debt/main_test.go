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

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandChecksRaceDebtEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		contents   string
		wantErr    bool
		wantOutput string
	}{
		{
			name: "empty allowlist succeeds",
			contents: `{
  "version": 1,
  "exclusions": []
}`,
		},
		{
			name: "missing issue is rejected",
			contents: `{
  "version": 1,
  "exclusions": [{
    "package": "./internal/runtime",
    "test": "TestShutdown",
    "owner": "@powercontext-maintainers",
    "reason": "bounded reproduction",
    "added": "2026-08-31",
    "removal_condition": "replace the synchronization with a deterministic barrier",
    "expires": "2999-12-31"
  }]
}`,
			wantErr:    true,
			wantOutput: "issue",
		},
		{
			name: "test selector is rejected",
			contents: `{
  "version": 1,
  "exclusions": [{
    "package": "./internal/runtime",
    "test": "Test.*",
    "issue": "https://github.com/ob-labs/powercontext-go/issues/3",
    "owner": "@powercontext-maintainers",
    "reason": "bounded reproduction",
    "added": "2026-08-31",
    "removal_condition": "replace the synchronization with a deterministic barrier",
    "expires": "2999-12-31"
  }]
}`,
			wantErr:    true,
			wantOutput: "test function",
		},
		{
			name: "complete temporary exclusion succeeds",
			contents: `{
  "version": 1,
  "exclusions": [{
    "package": "./internal/runtime",
    "test": "TestShutdown",
    "issue": "https://github.com/ob-labs/powercontext-go/issues/3",
    "owner": "@powercontext-maintainers",
    "reason": "bounded reproduction",
    "added": "2026-08-31",
    "removal_condition": "replace the synchronization with a deterministic barrier",
    "expires": "2999-12-31"
  }]
}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "race-debt.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}

			command := exec.CommandContext(t.Context(), "go", "run", ".", "-file", path)
			output, err := command.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Fatalf("go run error = %v, want error %t\n%s", err, test.wantErr, output)
			}
			if test.wantOutput != "" && !strings.Contains(strings.ToLower(string(output)), test.wantOutput) {
				t.Fatalf("go run output = %q, want substring %q", output, test.wantOutput)
			}
		})
	}
}

func TestCommandAppliesTheMonotonicBaselinePolicy(t *testing.T) {
	const emptyLedger = `{
  "version": 1,
  "exclusions": []
}`
	const exclusionLedger = `{
  "version": 1,
  "exclusions": [{
    "package": "./internal/runtime",
    "test": "TestShutdown",
    "issue": "https://github.com/ob-labs/powercontext-go/issues/3",
    "owner": "@powercontext-maintainers",
    "reason": "bounded reproduction",
    "added": "2026-08-31",
    "removal_condition": "replace the synchronization with a deterministic barrier",
    "expires": "2999-12-31"
  }]
	}`
	tests := []struct {
		name       string
		baseline   string
		ledger     string
		wantErr    bool
		wantOutput string
	}{
		{
			name:       "rejects a new exclusion",
			baseline:   emptyLedger,
			ledger:     exclusionLedger,
			wantErr:    true,
			wantOutput: "new temporary exclusion",
		},
		{
			name:     "allows removing a baseline exclusion",
			baseline: exclusionLedger,
			ledger:   emptyLedger,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			baseline := filepath.Join(root, "baseline.json")
			if err := os.WriteFile(baseline, []byte(test.baseline), 0o600); err != nil {
				t.Fatal(err)
			}
			ledger := filepath.Join(root, "race-debt.json")
			if err := os.WriteFile(ledger, []byte(test.ledger), 0o600); err != nil {
				t.Fatal(err)
			}

			command := exec.CommandContext(t.Context(), "go", "run", ".", "-file", ledger, "-baseline", baseline)
			output, err := command.CombinedOutput()
			if (err != nil) != test.wantErr {
				t.Fatalf("race-debt check error = %v, want error %t\n%s", err, test.wantErr, output)
			}
			if test.wantOutput != "" && !strings.Contains(string(output), test.wantOutput) {
				t.Fatalf("race-debt check output = %q, want substring %q", output, test.wantOutput)
			}
		})
	}
}

func TestCommandExercisesTemporaryExclusionsWithoutTheRaceDetector(t *testing.T) {
	ledger := filepath.Join(t.TempDir(), "race-debt.json")
	if err := os.WriteFile(ledger, []byte(`{
  "version": 1,
  "exclusions": [{
    "package": "./tools/race-debt",
    "test": "TestCommandChecksRaceDebtEntries",
    "issue": "https://github.com/ob-labs/powercontext-go/issues/3",
    "owner": "@powercontext-maintainers",
    "reason": "exercise the non-race coverage path",
    "added": "2026-08-31",
    "removal_condition": "replace the synchronization with a deterministic barrier",
    "expires": "2999-12-31"
  }]
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	command := exec.CommandContext(t.Context(), "go", "run", "./tools/race-debt", "-file", ledger, "-exercise")
	command.Dir = repository
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("race-debt non-race exercise failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "github.com/ob-labs/powercontext-go/tools/race-debt") {
		t.Fatalf("race-debt non-race exercise output = %q", output)
	}
}
