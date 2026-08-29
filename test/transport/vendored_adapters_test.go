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

package transport_test

import (
	"bytes"
	json "encoding/json/v2"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

// The retained Python adapters (Codex, Claude Code) ship isolated: they cannot
// import the shared policy, so each vendors a copy of the loopback contract.
// These guards pin every vendored copy to the shared vectors so drift in any
// one implementation fails here instead of silently weakening the policy. The
// Pi adapter is pinned the same way by its own Vitest suite
// (integrations/pi/plugins/powercontext/tests/transport-policy.spec.ts), which
// consumes the same vectors file.
var vendoredPythonAdapters = map[string]string{
	"codex":       filepath.Join("..", "..", "integrations", "codex", "plugins", "powercontext", "settings.py"),
	"claude-code": filepath.Join("..", "..", "integrations", "claude-code", "plugins", "powercontext", "claude_code_settings.py"),
}

// vendoredAdapterProbe loads one vendored adapter by path and drives the
// shared host vectors through its production _http_base_url entry point. It
// exits 3 when the adapter cannot be imported (for example the Codex adapter's
// pydantic dependency is absent), which the Go side treats as a skip -- the
// same defensive semantics as the upstream Python drift guard's skipif.
const vendoredAdapterProbe = `
import importlib.util
import json
import sys

path = sys.argv[1]
hosts = json.load(sys.stdin)

module_name = "vendored_adapter_drift_guard"
spec = importlib.util.spec_from_file_location(module_name, path)
module = importlib.util.module_from_spec(spec)
# Register before executing: a slotted dataclass (the Claude Code adapter)
# resolves its own module via sys.modules during class creation.
sys.modules[module_name] = module
try:
    spec.loader.exec_module(module)
except Exception as error:
    print(repr(error), file=sys.stderr)
    sys.exit(3)

accepted = {}
for host in hosts:
    try:
        module._http_base_url(f"http://{host}:8000/mcp")
        accepted[host] = True
    except ValueError:
        accepted[host] = False

print(json.dumps({"loopback_hosts": sorted(module._LOOPBACK_HOSTS), "accepted": accepted}))
`

type vendoredAdapterVerdict struct {
	LoopbackHosts []string        `json:"loopback_hosts"`
	Accepted      map[string]bool `json:"accepted"`
}

func probeVendoredAdapter(t *testing.T, adapterPath string, hosts []string) vendoredAdapterVerdict {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable; skipping the behavioral drift guard (upstream skip semantics)")
	}
	payload, err := json.Marshal(hosts)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(python, "-c", vendoredAdapterProbe, adapterPath)
	command.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 3 {
		t.Skipf("vendored adapter cannot be imported in this environment (upstream skip semantics): %s",
			strings.TrimSpace(stderr.String()))
	}
	if err != nil {
		t.Fatalf("probing %s: %v\n%s", adapterPath, err, stderr.String())
	}
	var verdict vendoredAdapterVerdict
	if err := json.Unmarshal(stdout.Bytes(), &verdict); err != nil {
		t.Fatalf("decoding the probe output for %s: %v\n%s", adapterPath, err, stdout.String())
	}
	return verdict
}

// TestVendoredPythonAdaptersKeepTheSharedPolicyCheck is the always-on half of
// the guard: even where python3 or an adapter dependency is unavailable, the
// vendored source must keep the shared 127.0.0.0/8 check and must not revert
// to the pre-policy exact-match host list.
func TestVendoredPythonAdaptersKeepTheSharedPolicyCheck(t *testing.T) {
	for name, adapterPath := range vendoredPythonAdapters {
		t.Run(name, func(t *testing.T) {
			source, err := os.ReadFile(adapterPath)
			if err != nil {
				t.Fatalf("reading the vendored adapter: %v", err)
			}
			text := string(source)
			for _, required := range []string{
				"def _is_loopback_host(",
				"ipaddress.ip_address(",
				"not _is_loopback_host(parsed.hostname)",
			} {
				if !strings.Contains(text, required) {
					t.Errorf("vendored adapter is missing %q; it must mirror the shared 127.0.0.0/8 loopback contract", required)
				}
			}
			if strings.Contains(text, "hostname.lower() not in _LOOPBACK_HOSTS") {
				t.Error("vendored adapter reverted to the exact-match loopback check; the rest of 127.0.0.0/8 must stay trusted")
			}
		})
	}
}

func TestVendoredPythonAdaptersShareTheLoopbackHostSet(t *testing.T) {
	want := []string{"127.0.0.1", "::1", "localhost"}
	for name, adapterPath := range vendoredPythonAdapters {
		t.Run(name, func(t *testing.T) {
			verdict := probeVendoredAdapter(t, adapterPath, []string{"127.0.0.1"})
			if !slices.Equal(verdict.LoopbackHosts, want) {
				t.Fatalf("vendored _LOOPBACK_HOSTS = %v, want the shared set %v", verdict.LoopbackHosts, want)
			}
		})
	}
}

func TestVendoredPythonAdaptersMatchTheSharedPlaintextPolicy(t *testing.T) {
	vectors := loadLoopbackVectors(t)
	hosts := slices.Concat(vectors.Loopback, vectors.NonLoopback)
	for name, adapterPath := range vendoredPythonAdapters {
		t.Run(name, func(t *testing.T) {
			verdict := probeVendoredAdapter(t, adapterPath, hosts)
			for _, host := range hosts {
				sharedRejects := transportpolicy.IsPlaintextNonLoopback(&url.URL{Scheme: "http", Host: host + ":8000"})
				accepted, ok := verdict.Accepted[host]
				if !ok {
					t.Fatalf("the probe returned no verdict for host %q", host)
				}
				if accepted == sharedRejects {
					t.Errorf("host %q: vendored adapter accepted = %t, but the shared policy rejects = %t",
						host, accepted, sharedRejects)
				}
			}
		})
	}
}
