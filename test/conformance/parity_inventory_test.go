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

package conformance_test

import (
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	upstreamMasterDiscoveryCommit = "74b961fbb07165595314726715d412a3d0d90589"
	upstreamMasterNodeIDCount     = 1345
)

type upstreamMasterNodeIDs struct {
	SchemaVersion  int      `json:"schema_version"`
	UpstreamCommit string   `json:"upstream_commit"`
	NodeIDs        []string `json:"node_ids"`
}

type upstreamMasterDiscovery struct {
	SchemaVersion int `json:"schema_version"`
	Upstream      struct {
		Repository string `json:"repository"`
		Branch     string `json:"branch"`
		Commit     string `json:"commit"`
	} `json:"upstream"`
	Collection struct {
		Command       []string          `json:"command"`
		Environment   map[string]string `json:"environment"`
		ExitCode      int               `json:"exit_code"`
		TestCaseCount int               `json:"test_case_count"`
	} `json:"collection"`
	LockedEnvironment struct {
		PythonVersion string `json:"python_version"`
		PytestVersion string `json:"pytest_version"`
		UVLockSHA256  string `json:"uv_lock_sha256"`
	} `json:"locked_environment"`
	Evidence struct {
		RawStdoutSHA256    string `json:"raw_stdout_sha256"`
		NodeManifestSHA256 string `json:"node_manifest_sha256"`
	} `json:"evidence"`
	NormalizedNodeIDs struct {
		State         string `json:"state"`
		Count         int    `json:"count"`
		RequiredInput string `json:"required_input"`
		Scope         string `json:"scope"`
	} `json:"normalized_node_ids"`
}

type upstreamMasterScope struct {
	SchemaVersion     int                       `json:"schema_version"`
	Databases         []string                  `json:"databases"`
	ExternalAgents    []string                  `json:"external_agents"`
	OutOfScopeReasons []string                  `json:"out_of_scope_reasons"`
	Cases             map[string]jsontext.Value `json:"cases"`
}

func TestUpstreamMasterDiscoveryRejectsAmbiguousOrWrongEvidence(t *testing.T) {
	contents, err := os.ReadFile("upstream-master-discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutant string
	}{
		{
			name:   "wrong upstream commit",
			mutant: strings.Replace(string(contents), upstreamMasterDiscoveryCommit, "0000000000000000000000000000000000000000", 1),
		},
		{
			name:   "wrong collection count",
			mutant: strings.Replace(string(contents), `"test_case_count": 1345`, `"test_case_count": 1344`, 1),
		},
		{
			name:   "unknown member",
			mutant: strings.Replace(string(contents), `"schema_version": 1,`, `"schema_version": 1, "future_semantics": true,`, 1),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.mutant == string(contents) {
				t.Fatal("mutant construction did not change upstream-master discovery")
			}
			if _, err := decodeUpstreamMasterDiscovery([]byte(test.mutant)); err == nil {
				t.Error("accepted invalid upstream-master discovery evidence")
			}
		})
	}
}

func TestUpstreamMasterDiscoveryIdentityAndCollection(t *testing.T) {
	contents, err := os.ReadFile("upstream-master-discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := decodeUpstreamMasterDiscovery(contents)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(repositoryRoot(t), filepath.FromSlash(discovery.NormalizedNodeIDs.RequiredInput))
	manifestContents, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(manifestContents)); got != discovery.Evidence.NodeManifestSHA256 {
		t.Fatalf("latest-master node manifest SHA-256 = %s, want %s", got, discovery.Evidence.NodeManifestSHA256)
	}
	nodeIDs := readUpstreamMasterNodeIDs(t, discovery.NormalizedNodeIDs.RequiredInput)
	scopeContents, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(discovery.NormalizedNodeIDs.Scope)))
	if err != nil {
		t.Fatal(err)
	}
	scope, err := decodeUpstreamMasterScope(scopeContents)
	if err != nil {
		t.Fatalf("decode latest-master scope: %v", err)
	}
	if scope.SchemaVersion != 1 || !slices.Equal(scope.Databases, []string{"sqlite"}) ||
		!slices.Equal(scope.ExternalAgents, []string{"codex", "workbuddy"}) {
		t.Fatalf("latest-master scope must plan exactly SQLite, Codex, and WorkBuddy: %#v", scope)
	}
	assertUpstreamMasterScopeMatchesNodeIDs(t, scope, nodeIDs)
}

func assertUpstreamMasterScopeMatchesNodeIDs(t *testing.T, scope upstreamMasterScope, nodeIDs []string) {
	t.Helper()
	seen := make(map[string]struct{}, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		seen[nodeID] = struct{}{}
	}
	if len(scope.Cases) != len(seen) {
		t.Fatalf("latest-master scope cases = %d, want one classification for each of %d normalized node IDs", len(scope.Cases), len(seen))
	}
	for nodeID := range seen {
		if _, ok := scope.Cases[nodeID]; !ok {
			t.Fatalf("latest-master node ID %q has no parity-scope classification", nodeID)
		}
	}
	for nodeID := range scope.Cases {
		if _, ok := seen[nodeID]; !ok {
			t.Fatalf("parity-scope classification %q does not name a normalized latest-master node ID", nodeID)
		}
	}
}

func TestUpstreamMasterNodeIDsRejectInvalidManifest(t *testing.T) {
	validIDs := make([]string, upstreamMasterNodeIDCount)
	for index := range validIDs {
		validIDs[index] = fmt.Sprintf("tests/fixture/test_%04d.py::test_case", index)
	}
	for _, test := range []struct {
		name      string
		commit    string
		ids       []string
		duplicate bool
		unknown   bool
	}{
		{name: "wrong upstream commit", commit: "0000000000000000000000000000000000000000", ids: validIDs},
		{name: "wrong node count", commit: upstreamMasterDiscoveryCommit, ids: validIDs[:upstreamMasterNodeIDCount-1]},
		{name: "duplicate node ID", commit: upstreamMasterDiscoveryCommit, ids: replaceNodeID(validIDs, 1, validIDs[0])},
		{name: "out of order node ID", commit: upstreamMasterDiscoveryCommit, ids: replaceNodeID(validIDs, 1, "tests/fixture/test_0000-a.py::test_case")},
		{name: "absolute test path", commit: upstreamMasterDiscoveryCommit, ids: replaceNodeID(validIDs, 1, "/tests/fixture/test_0001.py::test_case")},
		{name: "parent escape test path", commit: upstreamMasterDiscoveryCommit, ids: replaceNodeID(validIDs, 1, "tests/../outside.py::test_case")},
		{name: "unknown member", commit: upstreamMasterDiscoveryCommit, ids: validIDs, unknown: true},
		{name: "duplicate member", commit: upstreamMasterDiscoveryCommit, ids: validIDs, duplicate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			contents, err := json.Marshal(upstreamMasterNodeIDs{
				SchemaVersion:  1,
				UpstreamCommit: test.commit,
				NodeIDs:        test.ids,
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.unknown {
				contents = append(contents[:len(contents)-1], []byte(`,"future_semantics":true}`)...)
			}
			if test.duplicate {
				contents = append(contents[:len(contents)-1], []byte(`,"schema_version":1}`)...)
			}
			if _, err := decodeUpstreamMasterNodeIDs(contents); err == nil {
				t.Error("accepted invalid upstream-master node-ID manifest")
			}
		})
	}
}

func replaceNodeID(nodeIDs []string, index int, replacement string) []string {
	result := slices.Clone(nodeIDs)
	result[index] = replacement
	return result
}

func readUpstreamMasterNodeIDs(t *testing.T, relativePath string) []string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), filepath.FromSlash(relativePath)))
	if err != nil {
		t.Fatalf("read upstream-master node-ID manifest %s: %v", relativePath, err)
	}
	manifest, err := decodeUpstreamMasterNodeIDs(contents)
	if err != nil {
		t.Fatalf("decode upstream-master node-ID manifest %s: %v", relativePath, err)
	}
	return manifest.NodeIDs
}

func decodeUpstreamMasterNodeIDs(contents []byte) (upstreamMasterNodeIDs, error) {
	var manifest upstreamMasterNodeIDs
	if err := json.Unmarshal(contents, &manifest, json.RejectUnknownMembers(true)); err != nil {
		return upstreamMasterNodeIDs{}, err
	}
	if manifest.SchemaVersion != 1 || manifest.UpstreamCommit != upstreamMasterDiscoveryCommit {
		return upstreamMasterNodeIDs{}, fmt.Errorf("upstream-master node-ID manifest identity = schema %d commit %s", manifest.SchemaVersion, manifest.UpstreamCommit)
	}
	if len(manifest.NodeIDs) != upstreamMasterNodeIDCount {
		return upstreamMasterNodeIDs{}, fmt.Errorf("upstream-master node-ID manifest has %d IDs, want %d", len(manifest.NodeIDs), upstreamMasterNodeIDCount)
	}
	for index, nodeID := range manifest.NodeIDs {
		if err := validateUpstreamMasterNodeID(nodeID); err != nil {
			return upstreamMasterNodeIDs{}, err
		}
		if index > 0 {
			previous := manifest.NodeIDs[index-1]
			if nodeID == previous {
				return upstreamMasterNodeIDs{}, fmt.Errorf("upstream-master node-ID manifest contains duplicate node ID %q", nodeID)
			}
			if nodeID < previous {
				return upstreamMasterNodeIDs{}, fmt.Errorf("upstream-master node-ID manifest is not strictly ordered at %q", nodeID)
			}
		}
	}
	return manifest, nil
}

func validateUpstreamMasterNodeID(nodeID string) error {
	testPath, testName, found := strings.Cut(nodeID, "::")
	if !found || testName == "" {
		return fmt.Errorf("upstream-master node ID %q has no test selector", nodeID)
	}
	if strings.ContainsRune(testPath, '\\') || path.IsAbs(testPath) || filepath.IsAbs(testPath) ||
		!strings.HasPrefix(testPath, "tests/") || path.Clean(testPath) != testPath {
		return fmt.Errorf("upstream-master node ID %q is not a safe tests/ relative path", nodeID)
	}
	return nil
}

func decodeUpstreamMasterScope(contents []byte) (upstreamMasterScope, error) {
	var scope upstreamMasterScope
	if err := json.Unmarshal(contents, &scope, json.RejectUnknownMembers(true)); err != nil {
		return upstreamMasterScope{}, err
	}
	if scope.SchemaVersion != 1 {
		return upstreamMasterScope{}, fmt.Errorf("unsupported upstream-master scope schema %d", scope.SchemaVersion)
	}
	return scope, nil
}

func decodeUpstreamMasterDiscovery(contents []byte) (upstreamMasterDiscovery, error) {
	var discovery upstreamMasterDiscovery
	if err := json.Unmarshal(contents, &discovery, json.RejectUnknownMembers(true)); err != nil {
		return upstreamMasterDiscovery{}, err
	}
	if discovery.SchemaVersion != 1 {
		return upstreamMasterDiscovery{}, fmt.Errorf("unsupported upstream-master discovery schema %d", discovery.SchemaVersion)
	}
	if discovery.Upstream.Repository != "oceanbase/powercontext" || discovery.Upstream.Branch != "master" || discovery.Upstream.Commit != upstreamMasterDiscoveryCommit {
		return upstreamMasterDiscovery{}, fmt.Errorf("upstream-master discovery identity = %s@%s/%s, want oceanbase/powercontext@master/%s", discovery.Upstream.Repository, discovery.Upstream.Branch, discovery.Upstream.Commit, upstreamMasterDiscoveryCommit)
	}
	wantCommand := []string{"python", "-m", "pytest", "--collect-only", "-q", "--doctest-modules", "-p", "no:cacheprovider"}
	if !slices.Equal(discovery.Collection.Command, wantCommand) {
		return upstreamMasterDiscovery{}, fmt.Errorf("upstream-master collection command = %q, want %q", discovery.Collection.Command, wantCommand)
	}
	if len(discovery.Collection.Environment) != 2 || discovery.Collection.Environment["PYTHONDONTWRITEBYTECODE"] != "1" || discovery.Collection.Environment["PYTEST_DISABLE_PLUGIN_AUTOLOAD"] != "1" {
		return upstreamMasterDiscovery{}, fmt.Errorf("upstream-master collection environment must disable bytecode and plugin autoload")
	}
	if discovery.Collection.ExitCode != 0 || discovery.Collection.TestCaseCount != upstreamMasterNodeIDCount {
		return upstreamMasterDiscovery{}, fmt.Errorf("upstream-master collection exit/count = %d/%d, want 0/%d", discovery.Collection.ExitCode, discovery.Collection.TestCaseCount, upstreamMasterNodeIDCount)
	}
	if discovery.LockedEnvironment.PythonVersion != "3.11.15" || discovery.LockedEnvironment.PytestVersion != "9.1.1" ||
		discovery.LockedEnvironment.UVLockSHA256 != "268a9cdb9e570cd933e999fe947b45ebf8e49b09c6c6c76c71a0c4edeac6b467" {
		return upstreamMasterDiscovery{}, fmt.Errorf("upstream-master locked collection environment is not the recorded Python, pytest, and uv.lock identity")
	}
	if discovery.Evidence.RawStdoutSHA256 != "284a43727a937bc91f34e9315f673bb386e28affab6368e01a5b8a27ef480a6c" ||
		len(discovery.Evidence.NodeManifestSHA256) != sha256.Size*2 {
		return upstreamMasterDiscovery{}, fmt.Errorf("upstream-master collection evidence hashes are incomplete")
	}
	if discovery.NormalizedNodeIDs.State != "required" || discovery.NormalizedNodeIDs.Count != discovery.Collection.TestCaseCount ||
		discovery.NormalizedNodeIDs.RequiredInput != "test/conformance/upstream-master-node-ids.json" || discovery.NormalizedNodeIDs.Scope != "test/conformance/parity-scope.json" {
		return upstreamMasterDiscovery{}, fmt.Errorf("upstream-master discovery must require the normalized node list and parity-scope linkage")
	}
	return discovery, nil
}

type parityInventoryEntry struct {
	Python   tracedPythonTest `json:"python"`
	Mode     string           `json:"mode"`
	Status   string           `json:"status"`
	Source   string           `json:"source"`
	Evidence []traceEvidence  `json:"evidence"`
}

type parityInventory struct {
	SchemaVersion       int                    `json:"schema_version"`
	TargetCommit        string                 `json:"target_commit"`
	OracleCommit        string                 `json:"oracle_commit"`
	PythonTestFileCount int                    `json:"python_test_file_count"`
	PythonTestCaseCount int                    `json:"python_test_case_count"`
	MappedCaseCount     int                    `json:"mapped_case_count"`
	PendingCaseCount    int                    `json:"pending_case_count"`
	Entries             []parityInventoryEntry `json:"entries"`
}

// TestParityInventoryMatchesContract guards the 812-case active parity target
// inventory: it must agree with parity-contract.json on identity, keep every
// frozen Oracle mapping verbatim, assign a mode to every delta case, resolve
// every mapped evidence reference, and pin the mapped/pending split so silent
// regressions fail.
func TestParityInventoryMatchesContract(t *testing.T) {
	var contract struct {
		Upstream struct {
			Repository string `json:"repository"`
		} `json:"upstream"`
		FrozenOracle struct {
			Commit string `json:"commit"`
		} `json:"frozen_release_oracle"`
		ExactTargetSHA      string `json:"exact_target_sha"`
		TargetTestCaseCount int    `json:"target_test_case_count"`
	}
	decodeJSONFile(t, "parity-contract.json", &contract)
	var inventory parityInventory
	decodeJSONFile(t, "parity-inventory.json", &inventory)
	var trace traceTable
	decodeJSONFile(t, "traceability.json", &trace)
	var delta struct {
		Removed []struct {
			Case tracedPythonTest `json:"case"`
		} `json:"removed"`
	}
	decodeJSONFile(t, "target-delta.json", &delta)

	if inventory.SchemaVersion != 1 {
		t.Fatalf("unsupported parity inventory schema %d", inventory.SchemaVersion)
	}
	if inventory.TargetCommit != contract.ExactTargetSHA {
		t.Fatalf("inventory target = %s, contract exact target SHA = %s", inventory.TargetCommit, contract.ExactTargetSHA)
	}
	if inventory.OracleCommit != contract.FrozenOracle.Commit || inventory.OracleCommit != trace.OracleCommit {
		t.Fatalf("inventory Oracle = %s, contract frozen Oracle = %s, traceability Oracle = %s",
			inventory.OracleCommit, contract.FrozenOracle.Commit, trace.OracleCommit)
	}
	if inventory.PythonTestCaseCount != contract.TargetTestCaseCount || len(inventory.Entries) != contract.TargetTestCaseCount {
		t.Fatalf("inventory cases = %d entries/%d declared, contract records %d",
			len(inventory.Entries), inventory.PythonTestCaseCount, contract.TargetTestCaseCount)
	}
	if inventory.MappedCaseCount+inventory.PendingCaseCount != inventory.PythonTestCaseCount {
		t.Fatalf("mapped %d + pending %d != %d cases",
			inventory.MappedCaseCount, inventory.PendingCaseCount, inventory.PythonTestCaseCount)
	}

	frozen := make(map[string]traceEntry, len(trace.Entries))
	for _, entry := range trace.Entries {
		frozen[entry.Python.File+"#"+entry.Python.Name] = entry
	}
	removedFrozen := make(map[string]struct{})
	for _, disposition := range delta.Removed {
		key := disposition.Case.File + "#" + disposition.Case.Name
		if _, ok := frozen[key]; ok {
			removedFrozen[key] = struct{}{}
		}
	}

	root := filepath.Join("..", "..")
	seen := make(map[string]struct{}, len(inventory.Entries))
	files := make(map[string]struct{})
	mapped, pending, inherited := 0, 0, 0
	for index, entry := range inventory.Entries {
		key := entry.Python.File + "#" + entry.Python.Name
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate inventory entry %s", key)
		}
		seen[key] = struct{}{}
		files[entry.Python.File] = struct{}{}
		if index > 0 {
			previous := inventory.Entries[index-1].Python
			if lessPythonTest(entry.Python, previous) {
				t.Fatalf("inventory entries are not deterministically ordered at %s", key)
			}
		}
		if entry.Mode != "go-port" && entry.Mode != "retained-host" && entry.Mode != "cross-layer" {
			t.Fatalf("%s has invalid mode %q", key, entry.Mode)
		}
		switch entry.Status {
		case "mapped":
			mapped++
			if len(entry.Evidence) == 0 {
				t.Fatalf("%s is mapped but carries no evidence", key)
			}
		case "pending":
			pending++
			if len(entry.Evidence) != 0 {
				t.Fatalf("%s is pending but carries evidence", key)
			}
		default:
			t.Fatalf("%s has invalid status %q", key, entry.Status)
		}
		if frozenEntry, ok := frozen[key]; ok {
			inherited++
			if entry.Source != "oracle-traceability" {
				t.Fatalf("%s is a frozen Oracle case but has source %q", key, entry.Source)
			}
			if entry.Status != "mapped" || entry.Mode != frozenEntry.Mode {
				t.Fatalf("%s lost its frozen mapping: mode %q (frozen %q), status %q",
					key, entry.Mode, frozenEntry.Mode, entry.Status)
			}
			if !sameEvidence(entry.Evidence, frozenEntry.Evidence) {
				t.Fatalf("%s changed its frozen evidence set", key)
			}
		} else if entry.Source != "rules" {
			t.Fatalf("%s is a delta case but has source %q", key, entry.Source)
		}
		for _, evidence := range entry.Evidence {
			assertEvidenceResolves(t, root, key, evidence)
		}
	}
	for key := range removedFrozen {
		if _, present := seen[key]; present {
			t.Fatalf("ledger-removed frozen Oracle case %s remains in the release inventory", key)
		}
	}
	if want := len(trace.Entries) - len(removedFrozen); inherited != want {
		t.Fatalf("inventory inherited %d frozen Oracle cases, want %d after %d reviewed removals", inherited, want, len(removedFrozen))
	}
	if mapped != inventory.MappedCaseCount || pending != inventory.PendingCaseCount {
		t.Fatalf("counted mapped %d/pending %d, summary says %d/%d",
			mapped, pending, inventory.MappedCaseCount, inventory.PendingCaseCount)
	}
	if len(files) != inventory.PythonTestFileCount {
		t.Fatalf("inventory covers %d files, summary says %d", len(files), inventory.PythonTestFileCount)
	}
}

func lessPythonTest(left, right tracedPythonTest) bool {
	if left.File != right.File {
		return left.File < right.File
	}
	if left.Line != right.Line {
		return left.Line < right.Line
	}
	return left.Name < right.Name
}

func sameEvidence(actual, expected []traceEvidence) bool {
	if len(actual) != len(expected) {
		return false
	}
	remaining := make(map[traceEvidence]int, len(expected))
	for _, item := range expected {
		remaining[item]++
	}
	for _, item := range actual {
		remaining[item]--
		if remaining[item] < 0 {
			return false
		}
	}
	return true
}
