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

package backendauditledger

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type parityInventory struct {
	TargetCommit string `json:"target_commit"`
	Entries      []struct {
		Python struct {
			File string `json:"file"`
			Name string `json:"name"`
		} `json:"python"`
	} `json:"entries"`
}

type backendAuditLedger struct {
	SchemaVersion int                `json:"schema_version"`
	TargetCommit  string             `json:"target_commit"`
	Cases         []backendAuditCase `json:"cases"`
}

type backendAuditCase struct {
	File           string `json:"file"`
	Name           string `json:"name"`
	Classification string `json:"classification"`
}

func TestBackendAuditLedgerClassifiesEveryV010PersistenceCase(t *testing.T) {
	inventory := readParityInventory(t)
	ledger := readBackendAuditLedger(t)
	if err := validateBackendAuditLedger(ledger, inventory); err != nil {
		t.Fatal(err)
	}
}

func TestBackendAuditLedgerRejectsUnknownClassification(t *testing.T) {
	inventory := readParityInventory(t)
	ledger := readBackendAuditLedger(t)
	ledger.Cases = append([]backendAuditCase(nil), ledger.Cases...)
	ledger.Cases[0].Classification = "unreviewed"
	if err := validateBackendAuditLedger(ledger, inventory); err == nil {
		t.Fatal("backend audit ledger accepted an unknown classification")
	}
}

func readParityInventory(t *testing.T) parityInventory {
	t.Helper()
	contents := readConformanceFile(t, "parity-inventory.json")
	var inventory parityInventory
	if err := json.Unmarshal(contents, &inventory); err != nil {
		t.Fatal(err)
	}
	return inventory
}

func readBackendAuditLedger(t *testing.T) backendAuditLedger {
	t.Helper()
	contents := readConformanceFile(t, "backend-audit-ledger.json")
	var ledger backendAuditLedger
	if err := json.Unmarshal(contents, &ledger, json.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	return ledger
}

func readConformanceFile(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join(repositoryRoot(t), "test", "conformance", name)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func validateBackendAuditLedger(ledger backendAuditLedger, inventory parityInventory) error {
	if ledger.SchemaVersion != 1 {
		return fmt.Errorf("backend audit ledger schema version = %d", ledger.SchemaVersion)
	}
	if ledger.TargetCommit != inventory.TargetCommit {
		return fmt.Errorf("backend audit target = %s, inventory target = %s", ledger.TargetCommit, inventory.TargetCommit)
	}
	want := make(map[string]struct{})
	for _, entry := range inventory.Entries {
		if !strings.HasPrefix(entry.Python.File, "tests/builtin/persistence/") {
			continue
		}
		want[entry.Python.File+"#"+entry.Python.Name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(ledger.Cases))
	for _, item := range ledger.Cases {
		key := item.File + "#" + item.Name
		if _, exists := seen[key]; exists {
			return fmt.Errorf("backend audit ledger duplicates %s", key)
		}
		seen[key] = struct{}{}
		if _, found := want[key]; !found {
			return fmt.Errorf("backend audit ledger records unknown persistence case %s", key)
		}
		switch item.Classification {
		case "common", "covered-current", "seekdb-specific", "oceanbase-specific":
		default:
			return fmt.Errorf("backend audit ledger has invalid classification %q for %s", item.Classification, key)
		}
	}
	if len(seen) != len(want) {
		for key := range want {
			if _, found := seen[key]; !found {
				return fmt.Errorf("backend audit ledger omits %s", key)
			}
		}
		return fmt.Errorf("backend audit cases = %d, want %d", len(seen), len(want))
	}
	return nil
}
