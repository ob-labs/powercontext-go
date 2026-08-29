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
	"path/filepath"
	"testing"
)

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

// TestParityInventoryMatchesContract guards the 759-case active parity target
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
	if inherited != len(trace.Entries) {
		t.Fatalf("inventory inherited %d frozen Oracle cases, traceability declares %d", inherited, len(trace.Entries))
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
