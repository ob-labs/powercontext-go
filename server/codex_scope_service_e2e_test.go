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

//go:build sqlite_fts5

package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ob-labs/powercontext-go/internal/scope"
)

func init() {
	if os.Getenv("POWERCONTEXT_TEST_INVALID_UTF8_GIT") == "1" {
		_, _ = os.Stdout.Write([]byte{0xff})
		os.Exit(0)
	}
}

func TestCodexPluginServerScopePrioritizesSessionAndHonorsExplicitScope(t *testing.T) {
	for _, test := range []struct {
		name          string
		authorization string
	}{
		{name: "public"},
		{name: "bearer", authorization: "Bearer codex-server-scope-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv(PowerContextHomeEnv, filepath.Join(home, "powercontext"))
			application, service, counter := openCodexScopeService(t, test.authorization)
			defaultScope, err := application.scopes.Resolve(t.Context(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			draft, err := scope.NewDraft("Codex session", "Codex session context", "", nil, nil, "codex-server-scope-e2e")
			if err != nil {
				t.Fatal(err)
			}
			sessionScope, err := application.scopes.Create(t.Context(), draft)
			if err != nil {
				t.Fatal(err)
			}
			explicitDraft, err := scope.NewDraft("Codex explicit", "Codex explicit context", "", nil, nil, "codex-server-explicit-scope-e2e")
			if err != nil {
				t.Fatal(err)
			}
			explicitScope, err := application.scopes.Create(t.Context(), explicitDraft)
			if err != nil {
				t.Fatal(err)
			}
			if explicitScope.ID() == defaultScope.ID() || explicitScope.ID() == sessionScope.ID() {
				t.Fatalf("explicit Scope must differ from default and session Scopes: explicit=%q default=%q session=%q", explicitScope.ID(), defaultScope.ID(), sessionScope.ID())
			}
			const defaultMemory = "default Codex scope memory sentinel"
			const sessionMemory = "session Codex scope memory sentinel"
			const explicitMemory = "explicit Codex scope memory sentinel"
			seedCodexScopeMemory(t, application, defaultScope.ID(), defaultMemory, test.authorization)
			seedCodexScopeMemory(t, application, sessionScope.ID(), sessionMemory, test.authorization)
			seedCodexScopeMemory(t, application, explicitScope.ID(), explicitMemory, test.authorization)

			workspace := filepath.Join(home, "workspace")
			if err := os.MkdirAll(workspace, 0o755); err != nil {
				t.Fatal(err)
			}
			plugin := copyCodexPluginForServiceTest(t, home)
			writeCodexMCPConfiguration(t, plugin, service.URL)

			mcpSession := connectCodexScopeMCP(t, service, test.authorization)
			workspaceDigest := sha256.Sum256([]byte(workspace))
			setCodexScopeBinding(t, mcpSession, map[string]any{
				"integration": "codex", "kind": "workspace", "external_id": hex.EncodeToString(workspaceDigest[:]),
				"scope_id": defaultScope.ID(),
			})
			const sessionID = "codex-server-scope-session"
			setCodexScopeBinding(t, mcpSession, map[string]any{
				"integration": "codex", "kind": "session", "external_id": sessionID,
				"scope_id": sessionScope.ID(),
			})
			if err := mcpSession.Close(); err != nil {
				t.Fatal(err)
			}

			sessionRoutes := snapshotCodexRoutes(counter)
			sessionOutput, sessionStderr := runCodexRecall(t, plugin, map[string]any{
				"hook_event_name": "UserPromptSubmit", "prompt": "session scope memory", "cwd": workspace,
				"session_id": sessionID,
			}, test.authorization, nil)
			assertCodexHookMCPDelta(t, sessionRoutes, counter)
			assertCodexRecallContainsOnly(t, sessionOutput, sessionMemory, defaultMemory, explicitMemory)
			if len(bytes.TrimSpace(sessionStderr)) != 0 {
				t.Fatalf("session recall stderr = %s", sessionStderr)
			}

			workspaceRoutes := snapshotCodexRoutes(counter)
			workspaceOutput, workspaceStderr := runCodexRecall(t, plugin, map[string]any{
				"hook_event_name": "UserPromptSubmit", "prompt": "workspace scope memory", "cwd": workspace,
			}, test.authorization, nil)
			assertCodexHookMCPDelta(t, workspaceRoutes, counter)
			assertCodexRecallContainsOnly(t, workspaceOutput, defaultMemory, sessionMemory, explicitMemory)
			if len(bytes.TrimSpace(workspaceStderr)) != 0 {
				t.Fatalf("workspace recall stderr = %s", workspaceStderr)
			}

			explicitRoutes := snapshotCodexRoutes(counter)
			explicitOutput, explicitStderr := runCodexRecall(t, plugin, map[string]any{
				"hook_event_name": "UserPromptSubmit", "prompt": "explicit scope memory", "cwd": workspace,
				"session_id": sessionID,
			}, test.authorization, []string{"POWERCONTEXT_CODEX_SCOPE_ID=" + explicitScope.ID()})
			assertCodexHookMCPDelta(t, explicitRoutes, counter)
			assertCodexRecallContainsOnly(t, explicitOutput, explicitMemory, defaultMemory, sessionMemory)
			if len(bytes.TrimSpace(explicitStderr)) != 0 {
				t.Fatalf("explicit recall stderr = %s", explicitStderr)
			}

			dataPlaneBeforeUnknown := counter.rest.Load()
			unknownRoutes := snapshotCodexRoutes(counter)
			unknownScope := "scope-codex-server-scope-unknown"
			unknownOutput, unknownStderr := runCodexRecall(t, plugin, map[string]any{
				"hook_event_name": "UserPromptSubmit", "prompt": "must not reach the data plane", "cwd": workspace,
				"session_id": sessionID,
			}, test.authorization, []string{"POWERCONTEXT_CODEX_SCOPE_ID=" + unknownScope})
			if len(bytes.TrimSpace(unknownOutput)) != 0 || !strings.Contains(string(unknownStderr), "scope_resolution_failed") {
				t.Fatalf("unknown explicit Scope output=%q stderr=%q", unknownOutput, unknownStderr)
			}
			if counter.rest.Load() != dataPlaneBeforeUnknown {
				t.Fatalf("unknown explicit Scope reached REST data plane: before=%d after=%d", dataPlaneBeforeUnknown, counter.rest.Load())
			}
			assertCodexHookMCPDelta(t, unknownRoutes, counter)
			if strings.Contains(string(unknownStderr), unknownScope) {
				t.Fatalf("unknown explicit Scope leaked in stderr: %s", unknownStderr)
			}
			for _, override := range []string{"", "  "} {
				t.Run("blank explicit Scope "+fmt.Sprintf("%q", override), func(t *testing.T) {
					dataPlaneBeforeBlank := counter.rest.Load()
					blankRoutes := snapshotCodexRoutes(counter)
					blankOutput, blankStderr := runCodexRecall(t, plugin, map[string]any{
						"hook_event_name": "UserPromptSubmit", "prompt": "must not use session fallback", "cwd": workspace,
						"session_id": sessionID,
					}, test.authorization, []string{"POWERCONTEXT_CODEX_SCOPE_ID=" + override})
					if len(bytes.TrimSpace(blankOutput)) != 0 || !strings.Contains(string(blankStderr), "scope_resolution_failed") {
						t.Fatalf("blank explicit Scope output=%q stderr=%q", blankOutput, blankStderr)
					}
					if counter.rest.Load() != dataPlaneBeforeBlank {
						t.Fatalf("blank explicit Scope reached REST data plane: before=%d after=%d", dataPlaneBeforeBlank, counter.rest.Load())
					}
					assertCodexHookMCPDelta(t, blankRoutes, counter)
					assertCodexOutputHasNoLocalPath(t, blankOutput, blankStderr, workspace, home)
				})
			}
			assertCodexMCPRouteLifecycle(t, counter)
		})
	}
}

func TestCodexPluginServerScopeInvalidUTF8GitFailsClosedBeforeNetwork(t *testing.T) {
	home := t.TempDir()
	t.Setenv(PowerContextHomeEnv, filepath.Join(home, "powercontext"))
	_, service, counter := openCodexScopeService(t, "")
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	plugin := copyCodexPluginForServiceTest(t, home)
	writeCodexMCPConfiguration(t, plugin, service.URL)
	gitDirectory := installInvalidUTF8Git(t)
	output, stderr := runCodexRecall(t, plugin, map[string]any{
		"hook_event_name": "UserPromptSubmit", "prompt": "must not reach the service", "cwd": workspace,
	}, "", []string{
		"PATH=" + gitDirectory + string(os.PathListSeparator) + os.Getenv("PATH"),
		"POWERCONTEXT_TEST_INVALID_UTF8_GIT=1",
	})
	if counter.root.Load() != 0 || counter.slash.Load() != 0 || counter.rest.Load() != 0 {
		t.Fatalf("invalid UTF-8 git result reached service: root=%d slash=%d rest=%d", counter.root.Load(), counter.slash.Load(), counter.rest.Load())
	}
	if bytes.Contains(output, []byte(workspace)) || bytes.Contains(stderr, []byte(workspace)) ||
		bytes.Contains(output, []byte(home)) || bytes.Contains(stderr, []byte(home)) {
		t.Fatalf("invalid UTF-8 git result leaked local path: stdout=%q stderr=%q", output, stderr)
	}
	if len(bytes.TrimSpace(output)) != 0 || len(bytes.TrimSpace(stderr)) != 0 {
		t.Fatalf("invalid UTF-8 git result emitted output: stdout=%q stderr=%q", output, stderr)
	}
}

type codexServiceCounter struct {
	root       atomic.Int64
	slash      atomic.Int64
	deletes    atomic.Int64
	sessionIDs atomic.Int64
	rest       atomic.Int64
}

func openCodexScopeService(t *testing.T, authorization string) (*Application, *httptest.Server, *codexServiceCounter) {
	t.Helper()
	config := applicationTestConfig(t)
	config.Dashboard.Enabled = false
	config.HandoffReport.Enabled = false
	config.Logging.Access = false
	config.Metrics.Enabled = false
	config.MCP.Enabled = true
	config.MCP.Path = DefaultMCPPath
	if authorization != "" {
		config.Auth.Enabled = true
		config.Auth.Token = strings.TrimPrefix(authorization, "Bearer ")
	}
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	counter := &codexServiceCounter{}
	service := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/mcp":
			counter.root.Add(1)
		case "/mcp/":
			counter.slash.Add(1)
			if request.Method == http.MethodDelete {
				counter.deletes.Add(1)
			}
			if request.Header.Get("Mcp-Session-Id") != "" {
				counter.sessionIDs.Add(1)
			}
		default:
			if strings.HasPrefix(request.URL.Path, "/v1/") {
				counter.rest.Add(1)
			}
		}
		handler.ServeHTTP(response, request)
	}))
	t.Cleanup(service.Close)
	return application, service, counter
}

func seedCodexScopeMemory(t *testing.T, application *Application, scopeID, text, authorization string) {
	t.Helper()
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"scope_id": scopeID, "kind": "fact", "text": text})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/memory/remember", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("remember memory = %d: %s", response.Code, response.Body.String())
	}
}

func copyCodexPluginForServiceTest(t *testing.T, home string) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Codex plugin source")
	}
	source := filepath.Join(filepath.Dir(sourceFile), "..", "integrations", "codex", "plugins", "powercontext")
	destination := filepath.Join(home, "codex-plugin")
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, contents, 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	return destination
}

func writeCodexMCPConfiguration(t *testing.T, plugin, serviceURL string) {
	t.Helper()
	configuration, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"powercontext": map[string]any{
		"type": "http", "url": serviceURL + "/mcp", "required": false,
		"env_http_headers": map[string]string{"Authorization": "POWERCONTEXT_CODEX_AUTHORIZATION"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, ".mcp.json"), append(configuration, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func connectCodexScopeMCP(t *testing.T, service *httptest.Server, authorization string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "codex-server-scope-e2e", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint: service.URL + "/mcp/",
		HTTPClient: &http.Client{Transport: codexAuthorizationTransport{
			base: service.Client().Transport, authorization: authorization,
		}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func setCodexScopeBinding(t *testing.T, session *mcp.ClientSession, arguments map[string]any) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "scope_binding_set", Arguments: arguments})
	if err != nil || result.IsError {
		t.Fatalf("set Codex Scope binding = %#v, %v", result, err)
	}
}

func runCodexRecall(
	t *testing.T,
	plugin string,
	payload map[string]any,
	authorization string,
	extraEnvironment []string,
) ([]byte, []byte) {
	t.Helper()
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), codexRecallSubprocessTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "uv", "run", "--frozen", "--quiet", "--project", plugin, "python", filepath.Join(plugin, "hooks", "recall.py"))
	command.Env = append(os.Environ(),
		"POWERCONTEXT_CODEX_AUTHORIZATION="+authorization,
		"POWERCONTEXT_CODEX_CAPTURE_PROMPTS=false",
	)
	command.Env = append(command.Env, extraEnvironment...)
	command.Stdin = bytes.NewReader(encoded)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		exitCode := -1
		if exitError, ok := errors.AsType[*exec.ExitError](err); ok {
			exitCode = exitError.ExitCode()
		}
		t.Fatalf("run real Codex recall hook: %v\ncontext cause=%v\nexit code=%d\nstdout=%s\nstderr=%s", err, context.Cause(ctx), exitCode, stdout.Bytes(), stderr.Bytes())
	}
	return stdout.Bytes(), stderr.Bytes()
}

func assertCodexRecallContainsOnly(t *testing.T, output []byte, required string, forbidden ...string) {
	t.Helper()
	var value struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(output, &value); err != nil {
		t.Fatalf("decode Codex recall output %q: %v", output, err)
	}
	if !strings.Contains(value.HookSpecificOutput.AdditionalContext, required) {
		t.Fatalf("Codex recall context = %q, require %q", value.HookSpecificOutput.AdditionalContext, required)
	}
	for _, excluded := range forbidden {
		if strings.Contains(value.HookSpecificOutput.AdditionalContext, excluded) {
			t.Fatalf("Codex recall context = %q unexpectedly contains %q", value.HookSpecificOutput.AdditionalContext, excluded)
		}
	}
}

type codexRouteSnapshot struct {
	root, slash, deletes, sessionIDs int64
}

func snapshotCodexRoutes(counter *codexServiceCounter) codexRouteSnapshot {
	return codexRouteSnapshot{
		root: counter.root.Load(), slash: counter.slash.Load(), deletes: counter.deletes.Load(),
		sessionIDs: counter.sessionIDs.Load(),
	}
}

func assertCodexHookMCPDelta(t *testing.T, before codexRouteSnapshot, counter *codexServiceCounter) {
	t.Helper()
	after := snapshotCodexRoutes(counter)
	if after.root != before.root || after.slash <= before.slash ||
		after.sessionIDs <= before.sessionIDs || after.deletes <= before.deletes {
		t.Fatalf("Codex hook MCP lifecycle delta: before=%#v after=%#v", before, after)
	}
}

func assertCodexOutputHasNoLocalPath(t *testing.T, output, stderr []byte, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if bytes.Contains(output, []byte(path)) || bytes.Contains(stderr, []byte(path)) {
			t.Fatalf("Codex hook leaked local path %q: stdout=%q stderr=%q", path, output, stderr)
		}
	}
}

func assertCodexMCPRouteLifecycle(t *testing.T, counter *codexServiceCounter) {
	t.Helper()
	if counter.root.Load() != 0 || counter.slash.Load() == 0 || counter.deletes.Load() == 0 {
		t.Fatalf("MCP routes root=%d slash=%d deletes=%d", counter.root.Load(), counter.slash.Load(), counter.deletes.Load())
	}
}

// codexRecallSubprocessTimeout bounds one cold uv/plugin/MCP lifecycle without
// relying on the suite timeout. Full macOS cold startup exceeded the hook's
// nominal ten seconds after producing its response, so this includes measured
// process shutdown headroom while retaining a finite diagnostic boundary.
const codexRecallSubprocessTimeout = 30 * time.Second

func installInvalidUTF8Git(t *testing.T) string {
	t.Helper()
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(testBinary)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(directory, name), contents, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

type codexAuthorizationTransport struct {
	base          http.RoundTripper
	authorization string
}

func (t codexAuthorizationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if t.authorization != "" {
		request.Header.Set("Authorization", t.authorization)
	}
	return t.base.RoundTrip(request)
}
