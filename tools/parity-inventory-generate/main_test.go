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

func TestLoadLatestNodeManifestRejectsInvalidDocuments(t *testing.T) {
	const upstreamCommit = "74b961fbb07165595314726715d412a3d0d90589"
	valid := fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":["tests/test_alpha.py::test_alpha","tests/test_beta.py::test_beta"]}`, upstreamCommit)
	tests := []struct {
		name     string
		contents string
		want     []string
	}{
		{name: "valid", contents: valid, want: []string{"tests/test_alpha.py::test_alpha", "tests/test_beta.py::test_beta"}},
		{name: "unknown member", contents: fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":["tests/test_alpha.py::test_alpha"],"future":true}`, upstreamCommit)},
		{name: "duplicate member", contents: fmt.Sprintf(`{"schema_version":1,"schema_version":1,"upstream_commit":%q,"node_ids":["tests/test_alpha.py::test_alpha"]}`, upstreamCommit)},
		{name: "trailing value", contents: valid + ` {}`},
		{name: "nested node object", contents: fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":[{"id":"tests/test_alpha.py::test_alpha","future":true}]}`, upstreamCommit)},
		{name: "wrong schema", contents: fmt.Sprintf(`{"schema_version":2,"upstream_commit":%q,"node_ids":["tests/test_alpha.py::test_alpha"]}`, upstreamCommit)},
		{name: "wrong commit", contents: `{"schema_version":1,"upstream_commit":"not-the-pinned-commit","node_ids":["tests/test_alpha.py::test_alpha"]}`},
		{name: "empty list", contents: fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":[]}`, upstreamCommit)},
		{name: "empty node ID", contents: fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":[""]}`, upstreamCommit)},
		{name: "absolute node path", contents: fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":["/tests/test_alpha.py::test_alpha"]}`, upstreamCommit)},
		{name: "parent node path", contents: fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":["../tests/test_alpha.py::test_alpha"]}`, upstreamCommit)},
		{name: "non tests node path", contents: fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":["integrations/test_alpha.py::test_alpha"]}`, upstreamCommit)},
		{name: "duplicate node ID", contents: fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":["tests/test_alpha.py::test_alpha","tests/test_alpha.py::test_alpha"]}`, upstreamCommit)},
		{name: "unsorted node IDs", contents: fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":["tests/test_beta.py::test_beta","tests/test_alpha.py::test_alpha"]}`, upstreamCommit)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "upstream-master-node-ids.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := loadLatestNodeManifest(path)
			if test.want == nil {
				if err == nil {
					t.Fatal("loadLatestNodeManifest() accepted an invalid manifest")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got, want := strings.Join(got, "\n"), strings.Join(test.want, "\n"); got != want {
				t.Fatalf("loadLatestNodeManifest() = %q, want %q", got, want)
			}
		})
	}
}

func TestLoadParityScopeRejectsAmbiguousDocuments(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "unknown member", contents: `{"schema_version":1,"databases":["sqlite"],"external_agents":["codex","workbuddy"],"out_of_scope_reasons":["unsupported-database","unsupported-agent","non-product"],"cases":{},"future":true}`},
		{name: "duplicate member", contents: `{"schema_version":1,"schema_version":1,"databases":["sqlite"],"external_agents":["codex","workbuddy"],"out_of_scope_reasons":["unsupported-database","unsupported-agent","non-product"],"cases":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parity-scope.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadParityScope(path); err == nil {
				t.Fatal("loadParityScope() accepted an ambiguous document")
			}
		})
	}
}

func TestLoadParityScopeRejectsAmbiguousNestedCaseRules(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "unknown nested reason member", contents: `{"schema_version":1,"databases":["sqlite"],"external_agents":["codex","workbuddy"],"out_of_scope_reasons":["unsupported-database","unsupported-agent","non-product"],"cases":{"tests/test_scope.py::test_case":{"classification":"out_of_scope","database":"seekdb","out_of_scope_reason":"unsupported-database","reason":"The upstream case requires seekDB.","factual_reason":"unknown"}}}`},
		{name: "duplicate nested reason member", contents: `{"schema_version":1,"databases":["sqlite"],"external_agents":["codex","workbuddy"],"out_of_scope_reasons":["unsupported-database","unsupported-agent","non-product"],"cases":{"tests/test_scope.py::test_case":{"classification":"out_of_scope","database":"seekdb","out_of_scope_reason":"unsupported-database","reason":"The upstream case requires seekDB.","reason":"A conflicting rationale."}}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parity-scope.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadParityScope(path); err == nil {
				t.Fatal("loadParityScope() accepted an ambiguous nested case rule")
			}
		})
	}
}

func TestLoadParityScopeValidatesMixedRules(t *testing.T) {
	tests := []struct {
		name    string
		rule    string
		wantErr bool
	}{
		{name: "multi agent", rule: `{"classification":"mixed","supported_surfaces":["core","codex","workbuddy"],"unsupported_databases":[],"unsupported_external_agents":["claude-code","opencode"],"reason":"The case covers supported and unsupported external agents."}`},
		{name: "database and agent", rule: `{"classification":"mixed","supported_surfaces":["sqlite","codex"],"unsupported_databases":["seekdb"],"unsupported_external_agents":["claude-code"],"reason":"The case combines SQLite and Codex coverage with unsupported dependencies."}`},
		{name: "missing support", rule: `{"classification":"mixed","supported_surfaces":[],"unsupported_databases":["seekdb"],"unsupported_external_agents":[],"reason":"The case has an unsupported backend."}`, wantErr: true},
		{name: "bad support", rule: `{"classification":"mixed","supported_surfaces":["seekdb"],"unsupported_databases":[],"unsupported_external_agents":["claude-code"],"reason":"The case has unsupported coverage."}`, wantErr: true},
		{name: "duplicate support", rule: `{"classification":"mixed","supported_surfaces":["codex","codex"],"unsupported_databases":[],"unsupported_external_agents":["claude-code"],"reason":"The case repeats a supported surface."}`, wantErr: true},
		{name: "no unsupported", rule: `{"classification":"mixed","supported_surfaces":["core"],"unsupported_databases":[],"unsupported_external_agents":[],"reason":"The case has no unsupported dependency."}`, wantErr: true},
		{name: "supported database in unsupported databases", rule: `{"classification":"mixed","supported_surfaces":["core"],"unsupported_databases":["sqlite"],"unsupported_external_agents":[],"reason":"The case puts SQLite in the unsupported list."}`, wantErr: true},
		{name: "supported agent in unsupported agents", rule: `{"classification":"mixed","supported_surfaces":["core"],"unsupported_databases":[],"unsupported_external_agents":["codex"],"reason":"The case puts Codex in the unsupported list."}`, wantErr: true},
		{name: "unsupported database in support", rule: `{"classification":"mixed","supported_surfaces":["seekdb"],"unsupported_databases":[],"unsupported_external_agents":["claude-code"],"reason":"The case puts seekDB in the supported list."}`, wantErr: true},
		{name: "unsupported agent in support", rule: `{"classification":"mixed","supported_surfaces":["claude-code"],"unsupported_databases":[],"unsupported_external_agents":["seekdb"],"reason":"The case puts Claude Code in the supported list."}`, wantErr: true},
		{name: "duplicate unsupported database", rule: `{"classification":"mixed","supported_surfaces":["core"],"unsupported_databases":["seekdb","seekdb"],"unsupported_external_agents":[],"reason":"The case repeats an unsupported database."}`, wantErr: true},
		{name: "whitespace unsupported agent", rule: `{"classification":"mixed","supported_surfaces":["core"],"unsupported_databases":[],"unsupported_external_agents":[" claude-code"],"reason":"The case space-pads an unsupported agent."}`, wantErr: true},
		{name: "legacy fields", rule: `{"classification":"mixed","database":"sqlite","supported_surfaces":["core"],"unsupported_databases":["seekdb"],"unsupported_external_agents":[],"reason":"The case retains a legacy field."}`, wantErr: true},
		{name: "missing reason", rule: `{"classification":"mixed","supported_surfaces":["core"],"unsupported_databases":["seekdb"],"unsupported_external_agents":[]}`, wantErr: true},
		{name: "blank reason", rule: `{"classification":"mixed","supported_surfaces":["core"],"unsupported_databases":["seekdb"],"unsupported_external_agents":[],"reason":" \t "}`, wantErr: true},
		{name: "unknown classification", rule: `{"classification":"other","reason":"The case has an unknown classification."}`, wantErr: true},
		{name: "unknown nested member", rule: `{"classification":"mixed","supported_surfaces":["core"],"unsupported_databases":["seekdb"],"unsupported_external_agents":[],"reason":"The case has an unknown field.","future":true}`, wantErr: true},
		{name: "duplicate nested member", rule: `{"classification":"mixed","supported_surfaces":["core"],"supported_surfaces":["codex"],"unsupported_databases":["seekdb"],"unsupported_external_agents":[],"reason":"The case has duplicate support."}`, wantErr: true},
		{name: "mixed arrays on in scope", rule: `{"classification":"in_scope","supported_surfaces":["core"],"unsupported_databases":["seekdb"]}`, wantErr: true},
		{name: "mixed arrays on out of scope", rule: `{"classification":"out_of_scope","out_of_scope_reason":"unsupported-database","database":"seekdb","supported_surfaces":["core"],"unsupported_databases":["seekdb"],"reason":"The case has incompatible mixed fields."}`, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parity-scope.json")
			if err := os.WriteFile(path, []byte(mixedScopeDocument(test.rule)), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := loadParityScope(path)
			if test.wantErr && err == nil {
				t.Fatal("loadParityScope() accepted an invalid mixed rule")
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func mixedScopeDocument(rule string) string {
	return fmt.Sprintf(`{"schema_version":1,"databases":["sqlite"],"external_agents":["codex","workbuddy"],"out_of_scope_reasons":["unsupported-database","unsupported-agent","non-product"],"cases":{"tests/test_mixed.py::test_case":%s}}`, rule)
}

func TestValidateParityScopeRequiresFactualOutOfScopeReason(t *testing.T) {
	valid := parityScope{
		SchemaVersion:     1,
		Databases:         []string{"sqlite"},
		ExternalAgents:    []string{"codex", "workbuddy"},
		OutOfScopeReasons: []string{"unsupported-database", "unsupported-agent", "non-product"},
		Cases:             map[string]caseScopeRule{},
	}
	tests := []struct {
		name string
		rule caseScopeRule
	}{
		{name: "missing", rule: caseScopeRule{Classification: "out_of_scope", Database: "seekdb", OutOfScopeReason: "unsupported-database"}},
		{name: "empty", rule: caseScopeRule{Classification: "out_of_scope", Database: "seekdb", OutOfScopeReason: "unsupported-database", Reason: ""}},
		{name: "whitespace", rule: caseScopeRule{Classification: "out_of_scope", Database: "seekdb", OutOfScopeReason: "unsupported-database", Reason: " \t "}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope := valid
			scope.Cases = map[string]caseScopeRule{"tests/test_seekdb.py::test_query": test.rule}
			if err := validateParityScope(scope); err == nil {
				t.Fatal("validateParityScope() accepted an out_of_scope case without a factual reason")
			}
		})
	}

	valid.Cases = map[string]caseScopeRule{
		"tests/test_sqlite.py::test_round_trip": {Classification: "in_scope", Database: "sqlite"},
	}
	if err := validateParityScope(valid); err != nil {
		t.Fatalf("in_scope case without a reason: %v", err)
	}
}

func TestValidateLatestScopeFileRequiresExactCaseCoverage(t *testing.T) {
	root := t.TempDir()
	scopePath := filepath.Join(root, "parity-scope.json")
	nodeManifestPath := filepath.Join(root, "upstream-master-node-ids.json")
	scope := `{
  "schema_version": 1,
  "databases": ["sqlite"],
  "external_agents": ["codex", "workbuddy"],
  "out_of_scope_reasons": ["unsupported-database", "unsupported-agent", "non-product"],
  "cases": {
    "tests/test_sqlite.py::test_round_trip": {
      "classification": "in_scope",
      "database": "sqlite"
    }
  }
}`
	if err := os.WriteFile(scopePath, []byte(scope), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodeManifestPath, []byte(fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":["tests/test_sqlite.py::test_round_trip"]}`, latestNodeManifestCommit)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateLatestScopeFile(scopePath, nodeManifestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodeManifestPath, []byte(fmt.Sprintf(`{"schema_version":1,"upstream_commit":%q,"node_ids":["tests/test_sqlite.py::test_unclassified"]}`, latestNodeManifestCommit)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateLatestScopeFile(scopePath, nodeManifestPath); err == nil {
		t.Fatal("validateLatestScopeFile() accepted an unclassified latest case")
	}
}

func TestValidateParityScopeRejectsUnsupportedOrDuplicateValues(t *testing.T) {
	valid := parityScope{
		SchemaVersion:     1,
		Databases:         []string{"sqlite"},
		ExternalAgents:    []string{"codex", "workbuddy"},
		OutOfScopeReasons: []string{"unsupported-database", "unsupported-agent", "non-product"},
		Cases:             map[string]caseScopeRule{},
	}
	tests := []struct {
		name  string
		scope parityScope
	}{
		{name: "unknown database", scope: func() parityScope { scope := valid; scope.Databases = []string{"oceanbase"}; return scope }()},
		{name: "duplicate external agent", scope: func() parityScope {
			scope := valid
			scope.ExternalAgents = []string{"codex", "codex", "workbuddy"}
			return scope
		}()},
		{name: "missing exclusion reason", scope: func() parityScope {
			scope := valid
			scope.OutOfScopeReasons = []string{"unsupported-database", "unsupported-agent"}
			return scope
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateParityScope(test.scope); err == nil {
				t.Fatal("validateParityScope() accepted an unsupported or duplicate value")
			}
		})
	}
}

func TestValidateParityScopeAcceptsInScopeAndExplicitExclusions(t *testing.T) {
	scope := parityScope{
		SchemaVersion:     1,
		Databases:         []string{"sqlite"},
		ExternalAgents:    []string{"codex", "workbuddy"},
		OutOfScopeReasons: []string{"unsupported-database", "unsupported-agent", "non-product"},
		Cases: map[string]caseScopeRule{
			"tests/test_sqlite.py::test_round_trip": {Classification: "in_scope", Database: "sqlite"},
			"tests/test_codex.py::test_setup":       {Classification: "in_scope", ExternalAgent: "codex"},
			"tests/test_seekdb.py::test_query":      {Classification: "out_of_scope", Database: "seekdb", OutOfScopeReason: "unsupported-database", Reason: "The upstream case requires seekDB."},
			"tests/test_claude.py::test_setup":      {Classification: "out_of_scope", ExternalAgent: "claude-code", OutOfScopeReason: "unsupported-agent", Reason: "The upstream case exercises Claude Code."},
			"tests/test_devtool.py::test_tool":      {Classification: "out_of_scope", OutOfScopeReason: "non-product", Reason: "The upstream case verifies a development-only tool."},
		},
	}
	if err := validateParityScope(scope); err != nil {
		t.Fatal(err)
	}
}

func TestValidateParityScopeRejectsIncompleteOrInvalidCaseExclusions(t *testing.T) {
	valid := parityScope{
		SchemaVersion:     1,
		Databases:         []string{"sqlite"},
		ExternalAgents:    []string{"codex", "workbuddy"},
		OutOfScopeReasons: []string{"unsupported-database", "unsupported-agent", "non-product"},
		Cases:             map[string]caseScopeRule{},
	}
	tests := []struct {
		name string
		rule caseScopeRule
	}{
		{name: "unsupported in-scope database", rule: caseScopeRule{Classification: "in_scope", Database: "oceanbase"}},
		{name: "missing excluded database", rule: caseScopeRule{Classification: "out_of_scope", OutOfScopeReason: "unsupported-database", Reason: "The upstream case requires an omitted backend."}},
		{name: "supported excluded agent", rule: caseScopeRule{Classification: "out_of_scope", ExternalAgent: "codex", OutOfScopeReason: "unsupported-agent", Reason: "The fixture intentionally names a supported agent."}},
		{name: "non-product with dimensions", rule: caseScopeRule{Classification: "out_of_scope", Database: "sqlite", OutOfScopeReason: "non-product", Reason: "The fixture intentionally mixes classification dimensions."}},
		{name: "unclassified case", rule: caseScopeRule{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope := valid
			scope.Cases = map[string]caseScopeRule{"tests/test_scope.py::test_case": test.rule}
			if err := validateParityScope(scope); err == nil {
				t.Fatal("validateParityScope() accepted an invalid case scope")
			}
		})
	}
}

func TestValidateLatestCaseScopeRequiresExactDiscoveredCoverage(t *testing.T) {
	valid := parityScope{
		SchemaVersion:     1,
		Databases:         []string{"sqlite"},
		ExternalAgents:    []string{"codex", "workbuddy"},
		OutOfScopeReasons: []string{"unsupported-database", "unsupported-agent", "non-product"},
		Cases: map[string]caseScopeRule{
			"tests/test_sqlite.py::test_round_trip": {Classification: "in_scope", Database: "sqlite"},
			"tests/test_seekdb.py::test_query":      {Classification: "out_of_scope", Database: "seekdb", OutOfScopeReason: "unsupported-database", Reason: "The upstream case requires seekDB."},
		},
	}
	discovered := []string{
		"tests/test_sqlite.py::test_round_trip",
		"tests/test_seekdb.py::test_query",
	}
	if err := validateLatestCaseScope(valid, discovered); err != nil {
		t.Fatal(err)
	}

	missing := valid
	missing.Cases = map[string]caseScopeRule{
		"tests/test_sqlite.py::test_round_trip": {Classification: "in_scope", Database: "sqlite"},
	}
	if err := validateLatestCaseScope(missing, discovered); err == nil {
		t.Fatal("validateLatestCaseScope() accepted an unclassified discovered case")
	}

	extra := valid
	extra.Cases = map[string]caseScopeRule{
		"tests/test_sqlite.py::test_round_trip": {Classification: "in_scope", Database: "sqlite"},
		"tests/test_removed.py::test_old":       {Classification: "out_of_scope", OutOfScopeReason: "non-product", Reason: "The case is absent from the discovered latest inventory."},
	}
	if err := validateLatestCaseScope(extra, discovered[:1]); err == nil {
		t.Fatal("validateLatestCaseScope() accepted a scope entry for an unknown case")
	}

	if err := validateLatestCaseScope(valid, append(discovered, discovered[0])); err == nil {
		t.Fatal("validateLatestCaseScope() accepted duplicate discovered cases")
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
	contract := fmt.Sprintf(`{"schema_version":3,"release_target":{"commit":%q}}`, releaseCommit)
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
