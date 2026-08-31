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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadJSONRejectsAmbiguousDocuments(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "duplicate member", contents: `{"schema_version": 999, "schema_version": 1}`},
		{name: "unknown member", contents: `{"schema_version": 1, "future_semantics": true}`},
		{name: "trailing value", contents: `{"schema_version": 1} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "document.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			var document struct {
				SchemaVersion int `json:"schema_version"`
			}
			if err := readJSON(path, &document); err == nil {
				t.Error("accepted ambiguous JSON document")
			}
		})
	}
}

func TestDeclaresTestRequiresTypeScriptCall(t *testing.T) {
	tests := []struct {
		name     string
		contents string
		want     bool
	}{
		{name: "single quoted call", contents: "  it('records evidence', () => {})", want: true},
		{name: "double quoted call", contents: `it("records evidence", () => {})`, want: true},
		{name: "comment", contents: "// it('records evidence', () => {})", want: false},
		{name: "disabled alias", contents: "xit('records evidence', () => {})", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := declaresTest("ts", []byte(test.contents), "records evidence"); got != test.want {
				t.Errorf("declaresTest() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestResolveOutputPathKeepsAbsoluteAndRootsRelative(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repository")
	absolute := filepath.Join(t.TempDir(), "parity-inventory.json")
	for _, test := range []struct {
		name   string
		output string
		want   string
	}{
		{name: "absolute", output: absolute, want: absolute},
		{name: "relative", output: filepath.Join("test", "parity-inventory.json"), want: filepath.Join(root, "test", "parity-inventory.json")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveOutputPath(root, test.output); got != test.want {
				t.Fatalf("resolveOutputPath(%q, %q) = %q, want %q", root, test.output, got, test.want)
			}
		})
	}
}

func TestValidateTargetDeltaAcceptsExactReviewedLedger(t *testing.T) {
	root := t.TempDir()
	writeDeltaEvidence(t, root)
	previous := []pythonTest{
		{File: "tests/test_api.py", Name: "test_kept"},
		{File: "tests/test_api.py", Name: "test_removed"},
		{File: "tests/test_api.py", Name: "test_renamed_old"},
	}
	release := []pythonTest{
		{File: "tests/test_api.py", Name: "test_kept"},
		{File: "tests/test_api.py", Name: "test_added"},
		{File: "tests/test_api.py", Name: "test_renamed_new"},
	}
	ledger := targetDeltaLedger{
		SchemaVersion: 1,
		FromCommit:    "previous",
		ToCommit:      "release",
		Added: []caseIdentity{
			{File: "tests/test_api.py", Name: "test_added"},
			{File: "tests/test_api.py", Name: "test_renamed_new"},
		},
		Removed: []removedCaseDisposition{
			{
				Case:        caseIdentity{File: "tests/test_api.py", Name: "test_removed"},
				Disposition: "removed",
				Reason:      "The release removed an implementation-detail assertion.",
				Evidence:    []string{"go:evidence_test.go#TestSurvivingBehavior"},
			},
			{
				Case:         caseIdentity{File: "tests/test_api.py", Name: "test_renamed_old"},
				Disposition:  "renamed",
				Reason:       "The release renamed the observable behavior test.",
				Replacements: []caseIdentity{{File: "tests/test_api.py", Name: "test_renamed_new"}},
			},
		},
	}

	if err := validateTargetDelta(ledger, previous, release, "previous", "release", root); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTargetDeltaRejectsCaseSetDrift(t *testing.T) {
	ledger := targetDeltaLedger{
		SchemaVersion: 1,
		FromCommit:    "previous",
		ToCommit:      "release",
		Added:         []caseIdentity{{File: "tests/test_api.py", Name: "test_wrong"}},
		Removed: []removedCaseDisposition{{
			Case:         caseIdentity{File: "tests/test_api.py", Name: "test_removed"},
			Disposition:  "superseded",
			Reason:       "A release test replaces the old case.",
			Replacements: []caseIdentity{{File: "tests/test_api.py", Name: "test_added"}},
		}},
	}
	previous := []pythonTest{{File: "tests/test_api.py", Name: "test_removed"}}
	release := []pythonTest{{File: "tests/test_api.py", Name: "test_added"}}

	err := validateTargetDelta(ledger, previous, release, "previous", "release", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "added case set") {
		t.Fatalf("validateTargetDelta error = %v", err)
	}
}

func TestValidateTargetDeltaRejectsMissingReplacement(t *testing.T) {
	ledger := targetDeltaLedger{
		SchemaVersion: 1,
		FromCommit:    "previous",
		ToCommit:      "release",
		Added:         []caseIdentity{{File: "tests/test_api.py", Name: "test_added"}},
		Removed: []removedCaseDisposition{{
			Case:         caseIdentity{File: "tests/test_api.py", Name: "test_removed"},
			Disposition:  "renamed",
			Reason:       "The release renamed the case.",
			Replacements: []caseIdentity{{File: "tests/test_api.py", Name: "test_missing"}},
		}},
	}
	previous := []pythonTest{{File: "tests/test_api.py", Name: "test_removed"}}
	release := []pythonTest{{File: "tests/test_api.py", Name: "test_added"}}

	err := validateTargetDelta(ledger, previous, release, "previous", "release", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "replacement") {
		t.Fatalf("validateTargetDelta error = %v", err)
	}
}

func TestValidateTargetDeltaRejectsUnresolvedEvidence(t *testing.T) {
	ledger := targetDeltaLedger{
		SchemaVersion: 1,
		FromCommit:    "previous",
		ToCommit:      "release",
		Removed: []removedCaseDisposition{{
			Case:        caseIdentity{File: "tests/test_api.py", Name: "test_removed"},
			Disposition: "removed",
			Reason:      "The release removed an implementation-detail assertion.",
			Evidence:    []string{"go:missing_test.go#TestMissing"},
		}},
	}
	previous := []pythonTest{{File: "tests/test_api.py", Name: "test_removed"}}

	err := validateTargetDelta(ledger, previous, nil, "previous", "release", t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("validateTargetDelta error = %v", err)
	}
}

func writeDeltaEvidence(t *testing.T, root string) {
	t.Helper()
	contents := []byte("package evidence\n\nfunc TestSurvivingBehavior(t *testing.T) {}\n")
	if err := os.WriteFile(filepath.Join(root, "evidence_test.go"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckTargetDeltaRejectsCheckoutIdentityDrift(t *testing.T) {
	root := t.TempDir()
	writeDeltaEvidence(t, root)
	previous := writeDeltaCheckout(t, map[string]string{
		"tests/test_api.py": "def test_kept(): pass\ndef test_removed(): pass\n",
	})
	release := writeDeltaCheckout(t, map[string]string{
		"tests/test_api.py": "def test_kept(): pass\ndef test_added(): pass\n",
	})
	previousCommit := gitOutputForTest(t, previous, "rev-parse", "HEAD")
	releaseCommit := gitOutputForTest(t, release, "rev-parse", "HEAD")
	contractPath := filepath.Join(root, "parity-contract.json")
	contract := fmt.Sprintf(`{"schema_version":2,"release_target":{"commit":%q}}`, releaseCommit)
	if err := os.WriteFile(contractPath, []byte(contract), 0o600); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(root, "target-delta.json")
	ledger := fmt.Sprintf(`{
  "schema_version": 1,
  "from_commit": %q,
  "to_commit": %q,
  "added": [{"file":"tests/test_api.py","name":"test_added"}],
  "removed": [{
    "case":{"file":"tests/test_api.py","name":"test_removed"},
    "disposition":"removed",
    "reason":"The release removed an implementation-detail assertion.",
    "evidence":["go:evidence_test.go#TestSurvivingBehavior"]
  }]
}
`, previousCommit, releaseCommit)
	if err := os.WriteFile(ledgerPath, []byte(ledger), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := checkTargetDelta(root, contractPath, ledgerPath, previous, release); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(release, "tests", "new_test.py"), []byte("def test_new(): pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, release, "add", ".")
	runGitForTest(t, release, "commit", "-m", "drift")

	err := checkTargetDelta(root, contractPath, ledgerPath, previous, release)
	if err == nil || !strings.Contains(err.Error(), "release checkout HEAD") {
		t.Fatalf("checkTargetDelta error = %v", err)
	}
}

func writeDeltaCheckout(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	runGitForTest(t, root, "init")
	for _, directory := range []string{"tests", "integrations", "e2e"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, directory, ".gitkeep"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runGitForTest(t, root, "add", ".")
	runGitForTest(t, root, "commit", "-m", "fixture")
	return root
}

func runGitForTest(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "user.name=PowerContext", "-c", "user.email=powercontext@example.invalid", "-C", root}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutputForTest(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}
