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

package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ob-labs/powercontext-go/server"
)

func TestWorkBuddyHookAndMCPShareOneGoServiceConfiguration(t *testing.T) {
	checkoutRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	releaseRoot, binary := buildWorkBuddyReleaseRoot(t, checkoutRoot)

	for _, test := range []struct {
		name          string
		authenticated bool
	}{
		{name: "public"},
		{name: "authenticated", authenticated: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("WORKBUDDY_HOME", filepath.Join(home, "workbuddy"))
			t.Setenv(server.PowerContextHomeEnv, filepath.Join(home, "powercontext"))
			unsetWorkBuddyScopeOverride(t)
			t.Setenv("POWERCONTEXT_WORKBUDDY_FLUSH_ON_CAPTURE", "true")
			t.Setenv("WORKBUDDY_SERVICE_TOKEN", "")

			config, configErr := server.DefaultConfig()
			if configErr != nil {
				t.Fatal(configErr)
			}
			config.Dashboard.Enabled = false
			config.HandoffReport.Enabled = false
			config.Logging.Access = false
			config.Metrics.Enabled = false
			config.MCP.Enabled = true
			config.MCP.Path = server.DefaultMCPPath
			if test.authenticated {
				config.Auth.Enabled = true
				config.Auth.Token = "workbuddy-service-token"
				t.Setenv("WORKBUDDY_SERVICE_TOKEN", "Bearer "+config.Auth.Token)
			}

			application, openErr := server.OpenApplication(t.Context(), config, server.Dependencies{
				MemoryCandidates: fixedMemoryCandidatePipeline{},
			})
			if openErr != nil {
				t.Fatal(openErr)
			}
			t.Cleanup(func() {
				closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if closeErr := application.Close(closeContext); closeErr != nil {
					t.Error(closeErr)
				}
			})
			handler, handlerErr := application.HTTPHandler()
			if handlerErr != nil {
				t.Fatal(handlerErr)
			}
			trace := &workBuddyServiceTrace{handler: handler}
			service := httptest.NewServer(trace)
			t.Cleanup(service.Close)
			assertWorkBuddyServiceReady(t, service)
			scope := seedWorkBuddyWorkspaceBinding(t, service, config.Auth.Token, releaseRoot)
			trace.reset(scope)

			setupWorkBuddy(t, service.URL, binary, releaseRoot)
			assertWorkBuddyServerConfiguration(t, service.URL)
			assertWorkBuddyHookRegistration(t, binary, checkoutRoot, config.Auth.Token)
			assertWorkBuddyServiceReady(t, service)
			first := runWorkBuddyHook(t, binary, releaseRoot, "Remember this WorkBuddy service chain.", "prompt-1")
			if first != "" {
				t.Fatalf("first hook context = %q, want empty before capture", first)
			}
			trace.assertFirstHook(t)
			assertWorkBuddyMemorySearch(t, service, config.Auth.Token, scope)
			trace.reset(scope)
			second := runWorkBuddyHook(t, binary, releaseRoot, "Which WorkBuddy service chain should I use?", "prompt-2")
			trace.assertSecondHook(t)
			if !strings.Contains(second, "Remember this WorkBuddy service chain.") {
				t.Fatalf("recalled context = %q, want captured WorkBuddy content", second)
			}

			mcpURL, authorization := workBuddyMCPConnection(t, service.URL)
			mcpClient := mcp.NewClient(&mcp.Implementation{Name: "workbuddy-e2e", Version: "test"}, nil)
			session, connectErr := mcpClient.Connect(t.Context(), &mcp.StreamableClientTransport{
				Endpoint:             mcpURL,
				HTTPClient:           &http.Client{Transport: workBuddyAuthorizationTransport{base: service.Client().Transport, authorization: authorization}},
				DisableStandaloneSSE: true,
			}, nil)
			if connectErr != nil {
				t.Fatal(connectErr)
			}
			t.Cleanup(func() { _ = session.Close() })
			result, callErr := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "search_memory",
				Arguments: map[string]any{
					"scope_id": scope,
					"query":    "WorkBuddy service chain",
				},
			})
			if callErr != nil {
				t.Fatal(callErr)
			}
			if result.IsError {
				t.Fatalf("search_memory returned an MCP error: %#v", result.Content)
			}
			structured, ok := result.StructuredContent.(map[string]any)
			if !ok {
				t.Fatalf("search_memory structured content = %T, want object", result.StructuredContent)
			}
			hits, ok := structured["hits"].([]any)
			if !ok || len(hits) == 0 {
				t.Fatalf("search_memory hits = %#v, want captured WorkBuddy content", structured["hits"])
			}
			hit, ok := hits[0].(map[string]any)
			if !ok || hit["text"] != "Remember this WorkBuddy service chain." {
				t.Fatalf("search_memory first hit = %#v", hits[0])
			}
		})
	}
}

func unsetWorkBuddyScopeOverride(t *testing.T) {
	t.Helper()
	const name = "POWERCONTEXT_WORKBUDDY_SCOPE_ID"
	value, present := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv(name, value)
			return
		}
		_ = os.Unsetenv(name)
	})
}

type workBuddyServiceTrace struct {
	handler  http.Handler
	mu       sync.Mutex
	scope    string
	resolve  int
	prepare  int
	capture  int
	flush    int
	mismatch bool
}

func (trace *workBuddyServiceTrace) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	request.Body = io.NopCloser(bytes.NewReader(body))
	trace.record(request.URL.Path, body)
	trace.handler.ServeHTTP(writer, request)
}

func (trace *workBuddyServiceTrace) record(path string, body []byte) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if path == "/mcp/" && bytes.Contains(body, []byte(`"scope_binding_resolve"`)) {
		trace.resolve++
		return
	}
	if path != "/v1/context/prepare" && path != "/v1/sources/content" && path != "/v1/memory/flush" {
		return
	}
	var value struct {
		ScopeID string `json:"scope_id"`
	}
	if json.Unmarshal(body, &value) != nil || value.ScopeID != trace.scope {
		trace.mismatch = true
	}
	switch path {
	case "/v1/context/prepare":
		trace.prepare++
	case "/v1/sources/content":
		trace.capture++
	case "/v1/memory/flush":
		trace.flush++
	}
}

func (trace *workBuddyServiceTrace) reset(scope string) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.scope, trace.resolve, trace.prepare, trace.capture, trace.flush, trace.mismatch = scope, 0, 0, 0, 0, false
}

func (trace *workBuddyServiceTrace) assertFirstHook(t *testing.T) {
	t.Helper()
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.resolve != 1 || trace.prepare != 1 || trace.capture != 1 || trace.flush < 1 || trace.mismatch {
		t.Fatalf("first hook stages resolver=%d prepare=%d capture=%d flush=%d scope_mismatch=%t", trace.resolve, trace.prepare, trace.capture, trace.flush, trace.mismatch)
	}
}

func (trace *workBuddyServiceTrace) assertSecondHook(t *testing.T) {
	t.Helper()
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.resolve != 1 || trace.prepare != 1 || trace.capture != 1 || trace.flush < 1 || trace.mismatch {
		t.Fatalf("second hook stages resolver=%d prepare=%d capture=%d flush=%d scope_mismatch=%t", trace.resolve, trace.prepare, trace.capture, trace.flush, trace.mismatch)
	}
}

func seedWorkBuddyWorkspaceBinding(t *testing.T, service *httptest.Server, token, workspace string) string {
	t.Helper()
	authorization := ""
	if token != "" {
		authorization = "Bearer " + token
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "workbuddy-scope-seed", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             service.URL + "/mcp/",
		HTTPClient:           &http.Client{Transport: workBuddyAuthorizationTransport{base: service.Client().Transport, authorization: authorization}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	resolved, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "scope_binding_resolve", Arguments: map[string]any{}})
	if err != nil || resolved.IsError {
		t.Fatalf("resolve default Scope binding = %#v, %v", resolved, err)
	}
	content, ok := resolved.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("resolved Scope structured content = %T", resolved.StructuredContent)
	}
	scope, ok := content["scope_id"].(string)
	if !ok || scope == "" {
		t.Fatalf("resolved Scope ID = %#v", content["scope_id"])
	}
	sum := sha256.Sum256([]byte(workspace))
	set, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "scope_binding_set", Arguments: map[string]any{
		"integration": "workbuddy", "kind": "workspace", "external_id": hex.EncodeToString(sum[:]), "scope_id": scope,
	}})
	if err != nil || set.IsError {
		t.Fatalf("set WorkBuddy workspace binding = %#v, %v", set, err)
	}
	return scope
}

func assertWorkBuddyMemorySearch(t *testing.T, service *httptest.Server, token, scope string) {
	t.Helper()
	authorization := ""
	if token != "" {
		authorization = "Bearer " + token
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "workbuddy-memory-check", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             service.URL + "/mcp/",
		HTTPClient:           &http.Client{Transport: workBuddyAuthorizationTransport{base: service.Client().Transport, authorization: authorization}},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "search_memory", Arguments: map[string]any{
		"scope_id": scope, "query": "WorkBuddy service chain",
	}})
	if err != nil || result.IsError {
		t.Fatalf("first-hook memory search failed: result_error=%t call_error=%t", result.IsError, err != nil)
	}
	content, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("first-hook memory search structured result is invalid")
	}
	hits, ok := content["hits"].([]any)
	if !ok || len(hits) == 0 {
		t.Fatalf("first hook did not materialize searchable Memory")
	}
}

func assertWorkBuddyServiceReady(t *testing.T, service *httptest.Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		response, err := service.Client().Get(service.URL + "/health/ready")
		if err == nil {
			closeErr := response.Body.Close()
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			if response.StatusCode == http.StatusOK {
				return
			}
			lastErr = fmt.Errorf("Go Server readiness status = %d", response.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("reach Go Server readiness before deadline: %v", lastErr)
}

func buildWorkBuddyReleaseRoot(t *testing.T, source string) (string, string) {
	t.Helper()
	releaseRoot := filepath.Join(t.TempDir(), "powercontext-release")
	binary := filepath.Join(releaseRoot, "bin", "powercontext")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), "go", "build", "-tags", "sqlite_fts5", "-o", binary, "./cmd/powercontext")
	command.Dir = source
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build WorkBuddy release binary: %v\n%s", err, output)
	}
	if err := os.WriteFile(filepath.Join(releaseRoot, "BUILD-INFO.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".env.example", filepath.Join("openapi", "powercontext.yaml")} {
		copyWorkBuddyReleaseFile(t, filepath.Join(source, path), filepath.Join(releaseRoot, path))
	}
	copyWorkBuddyReleaseTree(t, filepath.Join(source, "integrations", "workbuddy"), filepath.Join(releaseRoot, "integrations", "workbuddy"))
	return releaseRoot, binary
}

func copyWorkBuddyReleaseFile(t *testing.T, source, destination string) {
	t.Helper()
	payload, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, payload, info.Mode()); err != nil {
		t.Fatal(err)
	}
}

func copyWorkBuddyReleaseTree(t *testing.T, source, destination string) {
	t.Helper()
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
		if !entry.Type().IsRegular() {
			return fmt.Errorf("WorkBuddy release entry %q is not a regular file", path)
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.WriteFile(target, payload, info.Mode())
	}); err != nil {
		t.Fatal(err)
	}
}

func setupWorkBuddy(t *testing.T, serverURL, binary, releaseRoot string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binary,
		"setup", "workbuddy", "--source", releaseRoot, "--server-url", serverURL,
		"--authorization-environment", "WORKBUDDY_SERVICE_TOKEN",
	)
	command.Dir = releaseRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("setup WorkBuddy from release archive: %v\n%s", err, output)
	}
}

func runWorkBuddyHook(t *testing.T, binary, root, prompt, promptID string) string {
	t.Helper()
	context, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	payload, err := json.Marshal(map[string]string{
		"hook_event_name": "UserPromptSubmit",
		"cwd":             root,
		"prompt":          prompt,
		"session_id":      "workbuddy-e2e-session",
		"prompt_id":       promptID,
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(context, binary, "hook", "workbuddy")
	command.Dir = filepath.Dir(filepath.Dir(binary))
	command.Env = workBuddyHookSubprocessEnv(t)
	command.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if err != nil {
		t.Fatalf("run WorkBuddy hook: %v: %s", err, stderr.String())
	}
	var result struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode WorkBuddy hook response %q: %v; stderr: %s", stdout.String(), err, stderr.String())
	}
	return result.HookSpecificOutput.AdditionalContext
}

func workBuddyHookSubprocessEnv(t *testing.T) []string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(os.Getenv("WORKBUDDY_HOME"), "powercontext.json"))
	if err != nil {
		t.Fatal(err)
	}
	var configuration struct {
		AuthorizationEnvironment string `json:"authorization_environment"`
	}
	if err := json.Unmarshal(payload, &configuration); err != nil {
		t.Fatal(err)
	}
	authorization, ok := os.LookupEnv(configuration.AuthorizationEnvironment)
	if !ok {
		t.Fatalf("WorkBuddy authorization environment %q is not set", configuration.AuthorizationEnvironment)
	}
	return append(os.Environ(), configuration.AuthorizationEnvironment+"="+authorization)
}

func assertWorkBuddyServerConfiguration(t *testing.T, serverURL string) {
	t.Helper()
	configuration, err := os.ReadFile(filepath.Join(os.Getenv("WORKBUDDY_HOME"), "powercontext.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		ServerURL string `json:"server_url"`
	}
	if err := json.Unmarshal(configuration, &value); err != nil {
		t.Fatal(err)
	}
	if value.ServerURL != serverURL {
		t.Fatalf("WorkBuddy Hook Server URL = %q, want %q", value.ServerURL, serverURL)
	}
}

func assertWorkBuddyHookRegistration(t *testing.T, binary, checkoutRoot, bearerToken string) {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(os.Getenv("WORKBUDDY_HOME"), "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(payload, &settings); err != nil {
		t.Fatal(err)
	}
	for _, matcher := range settings.Hooks["UserPromptSubmit"] {
		for _, hook := range matcher.Hooks {
			if hook.Type != "command" || !strings.HasSuffix(hook.Command, " hook workbuddy") {
				continue
			}
			if !strings.Contains(hook.Command, binary) {
				t.Fatalf("installed WorkBuddy command = %q, want released binary %q", hook.Command, binary)
			}
			for _, forbidden := range []string{"python", ".py", checkoutRoot, bearerToken} {
				if forbidden != "" && strings.Contains(hook.Command, forbidden) {
					t.Fatalf("installed WorkBuddy command contains forbidden value %q: %q", forbidden, hook.Command)
				}
			}
			return
		}
	}
	t.Fatalf("settings has no released WorkBuddy hook command: %s", payload)
}

func workBuddyMCPConnection(t *testing.T, serverURL string) (string, string) {
	t.Helper()
	configuration, err := os.ReadFile(filepath.Join(os.Getenv("WORKBUDDY_HOME"), "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		MCPServers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(configuration, &value); err != nil {
		t.Fatal(err)
	}
	entry, ok := value.MCPServers["powercontext"]
	if !ok {
		t.Fatal("installed WorkBuddy MCP configuration has no PowerContext entry")
	}
	if entry.URL != serverURL+"/mcp" {
		t.Fatalf("WorkBuddy MCP URL = %q, want %q", entry.URL, serverURL+"/mcp")
	}
	authorization := entry.Headers["Authorization"]
	if authorization == "${WORKBUDDY_SERVICE_TOKEN:-}" {
		authorization = os.Getenv("WORKBUDDY_SERVICE_TOKEN")
	}
	return entry.URL + "/", authorization
}

type workBuddyAuthorizationTransport struct {
	base          http.RoundTripper
	authorization string
}

func (t workBuddyAuthorizationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	if t.authorization == "" {
		return base.RoundTrip(request)
	}
	clone := request.Clone(request.Context())
	clone.Header.Set("Authorization", t.authorization)
	return base.RoundTrip(clone)
}
