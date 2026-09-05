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
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ghodss/yaml"
)

const (
	upstreamMasterCommit = "74b961fbb07165595314726715d412a3d0d90589"
	goMainCommit         = "e9dce330e58c4acbcfb1c0c5f0a4c9df581750e9"
)

type upstreamMasterOpenAPILedger struct {
	SchemaVersion    int                      `json:"schema_version"`
	Upstream         openAPILedgerRepository  `json:"upstream"`
	Go               openAPILedgerRepository  `json:"go"`
	OperationCounts  openAPIOperationCounts   `json:"operation_counts"`
	EndpointCounts   openAPIEndpointCounts    `json:"endpoint_counts"`
	MethodMigrations []openAPIMethodMigration `json:"method_migrations"`
	UpstreamOnly     []upstreamOnlyOperation  `json:"upstream_only"`
	GoOnly           []goOnlyOperation        `json:"go_only"`
}

type openAPILedgerRepository struct {
	Repository string `json:"repository"`
	Branch     string `json:"branch,omitempty"`
	Commit     string `json:"commit"`
}

type openAPIOperationCounts struct {
	Common       int `json:"common"`
	UpstreamOnly int `json:"upstream_only"`
	GoOnly       int `json:"go_only"`
}

type openAPIEndpointCounts struct {
	UpstreamOnly int `json:"upstream_only"`
	GoOnly       int `json:"go_only"`
}

type openAPIEndpoint struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

type openAPIMethodMigration struct {
	OperationID string          `json:"operation_id"`
	Go          openAPIEndpoint `json:"go"`
	Upstream    openAPIEndpoint `json:"upstream"`
}

type upstreamOnlyOperation struct {
	OperationID string `json:"operation_id"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Cluster     string `json:"cluster"`
}

type goOnlyOperation struct {
	OperationID string `json:"operation_id"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Status      string `json:"status"`
}

func TestUpstreamMasterOpenAPILedgerMatchesBothContracts(t *testing.T) {
	ledger := readUpstreamMasterOpenAPILedger(t)
	upstream := readUpstreamMasterOpenAPIOperations(t)
	current := readGoBaselineOpenAPIOperations(t)
	validateUpstreamMasterOpenAPILedger(t, ledger, upstream, current)
}

func TestUpstreamMasterCheckoutRejectsUnexpectedHEAD(t *testing.T) {
	if err := validateUpstreamCheckoutHEAD("not-the-pinned-upstream-commit"); err == nil {
		t.Fatal("accepted an upstream checkout that is not the pinned master commit")
	}
}

func TestPinnedOpenAPIReadIgnoresDirtyCheckoutFile(t *testing.T) {
	checkout := t.TempDir()
	runGit(t, checkout, "init", "--quiet")
	runGit(t, checkout, "config", "user.email", "conformance@example.invalid")
	runGit(t, checkout, "config", "user.name", "Conformance")
	openAPI := filepath.Join(checkout, "openapi", "powercontext.yaml")
	if err := os.MkdirAll(filepath.Dir(openAPI), 0o755); err != nil {
		t.Fatal("create temporary OpenAPI directory")
	}
	if err := os.WriteFile(openAPI, []byte("openapi: 3.0.3\npaths:\n  /health/live:\n    get:\n      operationId: get_liveness\n"), 0o600); err != nil {
		t.Fatal("write committed temporary OpenAPI")
	}
	runGit(t, checkout, "add", "openapi/powercontext.yaml")
	runGit(t, checkout, "commit", "--quiet", "-m", "add OpenAPI")
	commit := strings.TrimSpace(string(runGit(t, checkout, "rev-parse", "HEAD")))
	if err := os.WriteFile(openAPI, []byte("not: valid OpenAPI\n"), 0o600); err != nil {
		t.Fatal("replace temporary working-tree OpenAPI")
	}
	operations := readOpenAPIOperationsFromGit(t, checkout, commit)
	if got := operations["get_liveness"]; got != (openAPIEndpoint{Method: "get", Path: "/health/live"}) {
		t.Fatalf("pinned OpenAPI operation = %#v", got)
	}
}

func TestUpstreamMasterOpenAPILedgerRejectsAmbiguousJSON(t *testing.T) {
	contents, err := os.ReadFile("upstream-master-openapi-ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutant string
	}{
		{
			name: "unknown top-level member",
			mutant: strings.Replace(string(contents), `"schema_version": 1,`,
				`"schema_version": 1, "future_semantics": true,`, 1),
		},
		{
			name: "duplicate top-level member",
			mutant: strings.Replace(string(contents), `"schema_version": 1,`,
				`"schema_version": 999, "schema_version": 1,`, 1),
		},
		{
			name: "unknown nested member",
			mutant: strings.Replace(string(contents), `"cluster": "scope"`,
				`"cluster": "scope", "future_cluster": true`, 1),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.mutant == string(contents) {
				t.Fatal("mutant construction did not change the ledger")
			}
			if _, err := decodeUpstreamMasterOpenAPILedger([]byte(test.mutant)); err == nil {
				t.Fatal("accepted ambiguous ledger JSON")
			}
		})
	}
}

func TestUpstreamMasterOpenAPILedgerRejectsSemanticMutants(t *testing.T) {
	ledger := readUpstreamMasterOpenAPILedger(t)
	upstream := readUpstreamMasterOpenAPIOperations(t)
	current := readGoBaselineOpenAPIOperations(t)

	for _, test := range []struct {
		name   string
		mutate func(*upstreamMasterOpenAPILedger)
	}{
		{
			name: "wrong upstream commit",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.Upstream.Commit = "0000000000000000000000000000000000000000"
			},
		},
		{
			name: "wrong Go baseline commit",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.Go.Commit = "0000000000000000000000000000000000000000"
			},
		},
		{
			name: "wrong operation summary",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.OperationCounts.UpstreamOnly++
			},
		},
		{
			name: "duplicate upstream operation",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.UpstreamOnly = append(ledger.UpstreamOnly, ledger.UpstreamOnly[0])
			},
		},
		{
			name: "missing upstream operation",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.UpstreamOnly = ledger.UpstreamOnly[1:]
			},
		},
		{
			name: "wrong upstream endpoint",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.UpstreamOnly[0].Path = "/v1/not-the-contract"
			},
		},
		{
			name: "unknown upstream cluster",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.UpstreamOnly[0].Cluster = "future-cluster"
			},
		},
		{
			name: "missing retained Handoff Report extension",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.GoOnly = ledger.GoOnly[1:]
			},
		},
		{
			name: "wrong retained extension status",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.GoOnly[0].Status = "pending-removal"
			},
		},
		{
			name: "missing stats migration",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.MethodMigrations = nil
			},
		},
		{
			name: "wrong stats migration",
			mutate: func(ledger *upstreamMasterOpenAPILedger) {
				ledger.MethodMigrations[0].Upstream.Method = "get"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutant := cloneUpstreamMasterOpenAPILedger(t, ledger)
			test.mutate(&mutant)
			if err := validateUpstreamMasterOpenAPILedgerError(mutant, upstream, current); err == nil {
				t.Fatal("accepted invalid semantic ledger")
			}
		})
	}
}

func readUpstreamMasterOpenAPILedger(t *testing.T) upstreamMasterOpenAPILedger {
	t.Helper()
	contents, err := os.ReadFile("upstream-master-openapi-ledger.json")
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := decodeUpstreamMasterOpenAPILedger(contents)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func decodeUpstreamMasterOpenAPILedger(contents []byte) (upstreamMasterOpenAPILedger, error) {
	var ledger upstreamMasterOpenAPILedger
	if err := json.Unmarshal(contents, &ledger, json.RejectUnknownMembers(true)); err != nil {
		return upstreamMasterOpenAPILedger{}, err
	}
	return ledger, nil
}

func cloneUpstreamMasterOpenAPILedger(t *testing.T, ledger upstreamMasterOpenAPILedger) upstreamMasterOpenAPILedger {
	t.Helper()
	contents, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	clone, err := decodeUpstreamMasterOpenAPILedger(contents)
	if err != nil {
		t.Fatal(err)
	}
	return clone
}

func validateUpstreamMasterOpenAPILedger(t *testing.T, ledger upstreamMasterOpenAPILedger, upstream, current map[string]openAPIEndpoint) {
	t.Helper()
	if err := validateUpstreamMasterOpenAPILedgerError(ledger, upstream, current); err != nil {
		t.Fatal(err)
	}
}

func validateUpstreamMasterOpenAPILedgerError(ledger upstreamMasterOpenAPILedger, upstream, current map[string]openAPIEndpoint) error {
	if ledger.SchemaVersion != 1 {
		return fmt.Errorf("schema version = %d, want 1", ledger.SchemaVersion)
	}
	if ledger.Upstream.Repository != "oceanbase/powercontext" || ledger.Upstream.Branch != "master" || ledger.Upstream.Commit != upstreamMasterCommit {
		return fmt.Errorf("upstream identity = %#v", ledger.Upstream)
	}
	if ledger.Go.Repository != "ob-labs/powercontext-go" || ledger.Go.Commit != goMainCommit {
		return fmt.Errorf("Go identity = %#v", ledger.Go)
	}

	common, upstreamOnly, goOnly := operationDelta(upstream, current)
	if ledger.OperationCounts != (openAPIOperationCounts{Common: len(common), UpstreamOnly: len(upstreamOnly), GoOnly: len(goOnly)}) {
		return fmt.Errorf("operation counts = %#v, want common=%d upstream-only=%d go-only=%d", ledger.OperationCounts, len(common), len(upstreamOnly), len(goOnly))
	}
	upstreamEndpoints, currentEndpoints := endpointDelta(upstream, current)
	if ledger.EndpointCounts != (openAPIEndpointCounts{UpstreamOnly: len(upstreamEndpoints), GoOnly: len(currentEndpoints)}) {
		return fmt.Errorf("endpoint counts = %#v, want upstream-only=%d go-only=%d", ledger.EndpointCounts, len(upstreamEndpoints), len(currentEndpoints))
	}
	if err := validateUpstreamOnlyOperations(ledger.UpstreamOnly, upstreamOnly, upstreamEndpoints); err != nil {
		return err
	}
	if err := validateGoOnlyOperations(ledger.GoOnly, goOnly, currentEndpoints); err != nil {
		return err
	}
	if err := validateMethodMigrations(ledger.MethodMigrations, upstream, current); err != nil {
		return err
	}
	return nil
}

func validateUpstreamOnlyOperations(entries []upstreamOnlyOperation, want map[string]openAPIEndpoint, endpoints map[string]string) error {
	clusters := map[string]int{"scope": 10, "source": 6, "artifact": 6, "managed-skill": 6, "remote-skill": 10}
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, exists := seen[entry.OperationID]; exists {
			return fmt.Errorf("duplicate upstream-only operation %q", entry.OperationID)
		}
		seen[entry.OperationID] = struct{}{}
		endpoint, found := want[entry.OperationID]
		if !found {
			return fmt.Errorf("ledger records non-upstream-only operation %q", entry.OperationID)
		}
		if endpoint != (openAPIEndpoint{Method: entry.Method, Path: entry.Path}) {
			return fmt.Errorf("upstream-only %q endpoint = %#v, want %#v", entry.OperationID, openAPIEndpoint{Method: entry.Method, Path: entry.Path}, endpoint)
		}
		remaining, found := clusters[entry.Cluster]
		if !found {
			return fmt.Errorf("upstream-only %q has unknown cluster %q", entry.OperationID, entry.Cluster)
		}
		clusters[entry.Cluster] = remaining - 1
		if endpoints[endpointKey(endpoint)] != entry.OperationID {
			return fmt.Errorf("upstream-only %q endpoint is not exclusive", entry.OperationID)
		}
	}
	if len(seen) != len(want) {
		return fmt.Errorf("upstream-only ledger records %d operations, want %d", len(seen), len(want))
	}
	for cluster, remaining := range clusters {
		if remaining != 0 {
			return fmt.Errorf("upstream cluster %q has %d unaccounted operations", cluster, remaining)
		}
	}
	return nil
}

func validateGoOnlyOperations(entries []goOnlyOperation, want map[string]openAPIEndpoint, endpoints map[string]string) error {
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, exists := seen[entry.OperationID]; exists {
			return fmt.Errorf("duplicate Go-only operation %q", entry.OperationID)
		}
		seen[entry.OperationID] = struct{}{}
		endpoint, found := want[entry.OperationID]
		if !found {
			return fmt.Errorf("ledger records non-Go-only operation %q", entry.OperationID)
		}
		if endpoint != (openAPIEndpoint{Method: entry.Method, Path: entry.Path}) {
			return fmt.Errorf("Go-only %q endpoint = %#v, want %#v", entry.OperationID, openAPIEndpoint{Method: entry.Method, Path: entry.Path}, endpoint)
		}
		if entry.Status != "retained-go-extension" || !strings.HasPrefix(entry.Path, "/v1/handoff-reports/") {
			return fmt.Errorf("Go-only %q is not a retained Handoff Report extension", entry.OperationID)
		}
		if endpoints[endpointKey(endpoint)] != entry.OperationID {
			return fmt.Errorf("Go-only %q endpoint is not exclusive", entry.OperationID)
		}
	}
	if len(seen) != len(want) {
		return fmt.Errorf("Go-only ledger records %d operations, want %d", len(seen), len(want))
	}
	return nil
}

func validateMethodMigrations(entries []openAPIMethodMigration, upstream, current map[string]openAPIEndpoint) error {
	if len(entries) != 1 {
		return fmt.Errorf("method migrations = %d, want 1", len(entries))
	}
	migration := entries[0]
	if migration.OperationID != "get_stats" || migration.Go != current[migration.OperationID] || migration.Upstream != upstream[migration.OperationID] ||
		migration.Go.Path != migration.Upstream.Path || migration.Go.Method == migration.Upstream.Method {
		return fmt.Errorf("invalid get_stats method migration: %#v", migration)
	}
	for operationID, upstreamEndpoint := range upstream {
		if currentEndpoint, found := current[operationID]; found && currentEndpoint != upstreamEndpoint && operationID != migration.OperationID {
			return fmt.Errorf("unrecorded endpoint migration for %q", operationID)
		}
	}
	return nil
}

func operationDelta(upstream, current map[string]openAPIEndpoint) (common, upstreamOnly, goOnly map[string]openAPIEndpoint) {
	common = make(map[string]openAPIEndpoint)
	upstreamOnly = make(map[string]openAPIEndpoint)
	goOnly = make(map[string]openAPIEndpoint)
	for operationID, endpoint := range upstream {
		if _, found := current[operationID]; found {
			common[operationID] = endpoint
			continue
		}
		upstreamOnly[operationID] = endpoint
	}
	for operationID, endpoint := range current {
		if _, found := upstream[operationID]; !found {
			goOnly[operationID] = endpoint
		}
	}
	return common, upstreamOnly, goOnly
}

func endpointDelta(upstream, current map[string]openAPIEndpoint) (upstreamOnly, currentOnly map[string]string) {
	upstreamOnly = make(map[string]string)
	currentOnly = make(map[string]string)
	for operationID, endpoint := range upstream {
		key := endpointKey(endpoint)
		if _, found := findOperationByEndpoint(current, key); !found {
			upstreamOnly[key] = operationID
		}
	}
	for operationID, endpoint := range current {
		key := endpointKey(endpoint)
		if _, found := findOperationByEndpoint(upstream, key); !found {
			currentOnly[key] = operationID
		}
	}
	return upstreamOnly, currentOnly
}

func findOperationByEndpoint(operations map[string]openAPIEndpoint, key string) (string, bool) {
	for operationID, endpoint := range operations {
		if endpointKey(endpoint) == key {
			return operationID, true
		}
	}
	return "", false
}

func endpointKey(endpoint openAPIEndpoint) string {
	return endpoint.Method + " " + endpoint.Path
}

func readUpstreamMasterOpenAPIOperations(t *testing.T) map[string]openAPIEndpoint {
	t.Helper()
	return readOpenAPIOperationsFromGit(t, upstreamOpenAPICheckout(t), upstreamMasterCommit)
}

func upstreamOpenAPICheckout(t *testing.T) string {
	t.Helper()
	checkout := os.Getenv("POWERCONTEXT_UPSTREAM_CHECKOUT")
	if checkout == "" {
		t.Skip("upstream OpenAPI comparison runs in the dedicated upstream-master-discovery job")
	}
	info, err := os.Stat(checkout)
	if err != nil || !info.IsDir() {
		t.Fatal("POWERCONTEXT_UPSTREAM_CHECKOUT is not an upstream checkout directory")
	}
	output := runGit(t, checkout, "rev-parse", "HEAD")
	if err := validateUpstreamCheckoutHEAD(strings.TrimSpace(string(output))); err != nil {
		t.Fatal(err)
	}
	return checkout
}

func validateUpstreamCheckoutHEAD(head string) error {
	if head != upstreamMasterCommit {
		return fmt.Errorf("upstream checkout HEAD = %q, want %q", head, upstreamMasterCommit)
	}
	return nil
}

func readGoBaselineOpenAPIOperations(t *testing.T) map[string]openAPIEndpoint {
	t.Helper()
	root := repositoryRoot(t)
	return readOpenAPIOperationsFromGit(t, root, goMainCommit)
}

func readOpenAPIOperationsFromGit(t *testing.T, checkout, commit string) map[string]openAPIEndpoint {
	t.Helper()
	ref := commit + ":openapi/powercontext.yaml"
	return readOpenAPIOperationsBytes(t, ref, runGit(t, checkout, "show", ref))
}

func runGit(t *testing.T, directory string, arguments ...string) []byte {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(arguments, " "), err)
	}
	return output
}

func readOpenAPIOperationsBytes(t *testing.T, origin string, raw []byte) map[string]openAPIEndpoint {
	t.Helper()
	encoded, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatalf("convert %s YAML to JSON: %v", origin, err)
	}
	var document struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("parse %s OpenAPI: %v", origin, err)
	}
	operations := make(map[string]openAPIEndpoint)
	endpoints := make(map[string]string)
	for path, methods := range document.Paths {
		for method, operation := range methods {
			if !isOpenAPIHTTPMethod(method) {
				continue
			}
			if operation.OperationID == "" {
				t.Fatalf("%s %s in %s has no operationId", method, path, origin)
			}
			endpoint := openAPIEndpoint{Method: method, Path: path}
			if _, exists := operations[operation.OperationID]; exists {
				t.Fatalf("duplicate operationId %q in %s", operation.OperationID, path)
			}
			if previous, exists := endpoints[endpointKey(endpoint)]; exists {
				t.Fatalf("duplicate endpoint %s for operationIds %q and %q", endpointKey(endpoint), previous, operation.OperationID)
			}
			operations[operation.OperationID] = endpoint
			endpoints[endpointKey(endpoint)] = operation.OperationID
		}
	}
	return operations
}

func isOpenAPIHTTPMethod(method string) bool {
	return slices.Contains([]string{"get", "post", "put", "patch", "delete", "head", "options", "trace"}, method)
}
