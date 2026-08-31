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

// Command process-smoke verifies a built PowerContext release through only its
// public process interfaces. It deliberately does not construct server packages
// in-process: the executable, configuration, HTTP transport, MCP transport,
// SQLite restart path, and signal handling are all part of the release contract.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	pcclient "github.com/ob-labs/powercontext-go/client"
)

const (
	smokeScope = "release-verification-private-scope"
	smokeText  = "PowerContext release verification stores and retrieves this exact private memory."
	smokeToken = "release-verification-private-token"
)

var baseToolNames = []string{
	"acknowledge_handoff",
	"activate_handoff",
	"approve_artifact_candidate",
	"capture_content_source",
	"commit_handoff",
	"continue_handoff",
	"create_work_contract",
	"finalize_handoff",
	"get_artifact_candidate",
	"get_memory_entry",
	"handoff_current_work",
	"list_artifact_candidates",
	"list_memory_entries",
	"reject_artifact_candidate",
	"record_task_outcome",
	"remember_memory",
	"retire_memory_entry",
	"revise_artifact_candidate",
	"revise_memory_entry",
	"search_memory",
}

type options struct {
	binary  string
	envFile string
	version string
	timeout time.Duration
}

func main() {
	binary := flag.String("binary", "bin/powercontext", "built PowerContext executable")
	envFile := flag.String("env-file", "", "released .env.example with security-sensitive defaults")
	version := flag.String("version", "devel", "exact version reported by the executable")
	timeout := flag.Duration("timeout", 60*time.Second, "maximum duration of each server phase")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*(*timeout))
	defer cancel()
	if err := run(ctx, options{binary: *binary, envFile: *envFile, version: *version, timeout: *timeout}); err != nil {
		fmt.Fprintln(os.Stderr, "process-smoke:", err)
		os.Exit(1)
	}
	fmt.Printf(
		"Verified PowerContext %s CLI, HTTP, authenticated Dashboard, MCP 20/24-tool surfaces, SQLite restart persistence, and graceful shutdown.\n",
		*version,
	)
}

func run(ctx context.Context, opts options) error {
	if opts.timeout <= 0 {
		return errors.New("timeout must be positive")
	}
	if err := verifySecurityDefaults(opts.envFile); err != nil {
		return err
	}
	binary, err := filepath.Abs(opts.binary)
	if err != nil {
		return fmt.Errorf("resolve binary: %w", err)
	}
	if versionErr := verifyVersion(ctx, binary, opts.version); versionErr != nil {
		return versionErr
	}

	root, err := os.MkdirTemp("", "powercontext-release-")
	if err != nil {
		return fmt.Errorf("create smoke directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(root) }() // best-effort cleanup of isolated, disposable state

	baseEnvironment := isolatedEnvironment(os.Environ(), filepath.Join(root, "home"))
	firstLog := filepath.Join(root, "server-public.log")
	if err := runPhase(ctx, binary, root, firstLog, baseEnvironment, opts.timeout, func(baseURL string) error {
		if err := exerciseCLI(ctx, binary, baseEnvironment, baseURL, ""); err != nil {
			return err
		}
		if err := exerciseMemory(ctx, baseURL, "", true); err != nil {
			return err
		}
		if err := exerciseMCP(ctx, baseURL, "", true); err != nil {
			return err
		}
		response, err := request(ctx, http.MethodPost, baseURL+"/v1/handoff-reports/projects/list", "", `{}`)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("default Handoff Report route returned HTTP %d", response.StatusCode)
		}
		return nil
	}); err != nil {
		return withLog(err, firstLog)
	}
	if err := verifyPrivateLog(firstLog); err != nil {
		return err
	}

	secureEnvironment := append(slices.Clone(baseEnvironment),
		"POWERCONTEXT_SERVER_AUTH_ENABLED=true",
		"POWERCONTEXT_SERVER_AUTH_TOKEN="+smokeToken,
		"POWERCONTEXT_SERVER_DASHBOARD_ENABLED=true",
		`POWERCONTEXT_SERVER_DASHBOARD_SCOPES=[{"scope_id":"`+smokeScope+`","display_name":"Release Verification"}]`,
		"POWERCONTEXT_SERVER_HANDOFF_REPORT_ENABLED=true",
	)
	secondLog := filepath.Join(root, "server-secure.log")
	if err := runPhase(ctx, binary, root, secondLog, secureEnvironment, opts.timeout, func(baseURL string) error {
		if err := exerciseSecureHTTP(ctx, baseURL); err != nil {
			return err
		}
		if err := exerciseCLI(ctx, binary, secureEnvironment, baseURL, smokeToken); err != nil {
			return err
		}
		if err := exerciseMemory(ctx, baseURL, smokeToken, false); err != nil {
			return err
		}
		return exerciseMCP(ctx, baseURL, smokeToken, true)
	}); err != nil {
		return withLog(err, secondLog)
	}
	if err := verifyPrivateLog(secondLog); err != nil {
		return err
	}

	disabledEnvironment := append(
		slices.Clone(baseEnvironment),
		"POWERCONTEXT_SERVER_HANDOFF_REPORT_ENABLED=false",
	)
	thirdLog := filepath.Join(root, "server-reports-disabled.log")
	if err := runPhase(ctx, binary, root, thirdLog, disabledEnvironment, opts.timeout, func(baseURL string) error {
		if err := exerciseMCP(ctx, baseURL, "", false); err != nil {
			return err
		}
		response, err := request(ctx, http.MethodPost, baseURL+"/v1/handoff-reports/projects/list", "", `{}`)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusNotFound {
			return fmt.Errorf("disabled Handoff Report route returned HTTP %d", response.StatusCode)
		}
		return nil
	}); err != nil {
		return withLog(err, thirdLog)
	}
	if err := verifyPrivateLog(thirdLog); err != nil {
		return err
	}
	return nil
}

func verifySecurityDefaults(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("released security defaults file is required")
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		return errors.New("read released security defaults")
	}
	values := make(map[string]string)
	for line := range strings.SplitSeq(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if exported, ok := strings.CutPrefix(line, "export"); ok &&
			len(exported) > 0 && (exported[0] == ' ' || exported[0] == '\t') {
			line = strings.TrimSpace(exported)
		}
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || !validEnvironmentName(name) {
			return errors.New("released security defaults contain an invalid assignment")
		}
		if _, exists := values[name]; exists {
			return fmt.Errorf("released security default %s is declared more than once", name)
		}
		values[name] = strings.TrimSpace(value)
	}
	for name, expected := range map[string]string{
		"POWERCONTEXT_SERVER_HTTP_HOST":                          "127.0.0.1",
		"POWERCONTEXT_SERVER_AUTH_ENABLED":                       "false",
		"POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK": "false",
		"POWERCONTEXT_CLIENT_SERVER_URL":                         "http://127.0.0.1:8000",
	} {
		value, exists := values[name]
		if !exists {
			return fmt.Errorf("released security default %s is missing", name)
		}
		if value != expected {
			return fmt.Errorf("released security default %s does not match the release policy", name)
		}
	}
	if _, exists := values["POWERCONTEXT_SERVER_AUTH_TOKEN"]; exists {
		return errors.New("released security default POWERCONTEXT_SERVER_AUTH_TOKEN must remain commented")
	}
	return nil
}

func validEnvironmentName(name string) bool {
	for index, character := range name {
		if character == '_' ||
			character >= 'A' && character <= 'Z' ||
			character >= 'a' && character <= 'z' ||
			index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return name != ""
}

func verifyVersion(ctx context.Context, binary, expected string) error {
	if strings.TrimSpace(expected) == "" {
		return errors.New("expected version must not be empty")
	}
	command := exec.CommandContext(ctx, binary, "--version")
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("execute version check: %w", err)
	}
	if actual := strings.TrimSpace(string(output)); actual != expected {
		return fmt.Errorf("binary version is %q, want %q", actual, expected)
	}
	return nil
}

func runPhase(
	ctx context.Context,
	binary, root, logPath string,
	environment []string,
	timeout time.Duration,
	exercise func(string) error,
) error {
	port, err := availablePort()
	if err != nil {
		return err
	}
	baseURL := "http://127.0.0.1:" + strconv.Itoa(port)
	process, err := startServer(binary, root, logPath, environment, port)
	if err != nil {
		return err
	}
	phaseCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := waitUntilReady(phaseCtx, process, baseURL); err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCancel()
		_ = process.stop(stopCtx)
		return err
	}
	exerciseErr := exercise(baseURL)
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer stopCancel()
	stopErr := process.stop(stopCtx)
	if stopErr != nil {
		return errors.Join(exerciseErr, stopErr)
	}
	return errors.Join(exerciseErr, process.stop(stopCtx))
}

func availablePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve HTTP port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return 0, fmt.Errorf("release HTTP port: %w", err)
	}
	return port, nil
}

type serverProcess struct {
	command *exec.Cmd
	log     *os.File
	done    chan struct{}
	mu      sync.Mutex
	waitErr error
	closed  sync.Once
}

func startServer(binary, root, logPath string, environment []string, port int) (*serverProcess, error) {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open server log: %w", err)
	}
	command := exec.Command(binary, "server", "run", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	command.Dir = root
	command.Env = environment
	command.Stdout = logFile
	command.Stderr = logFile
	process := &serverProcess{command: command, log: logFile, done: make(chan struct{})}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, fmt.Errorf("start PowerContext Server: %w", err)
	}
	go func() {
		err := command.Wait()
		process.mu.Lock()
		process.waitErr = err
		process.mu.Unlock()
		close(process.done)
	}()
	return process, nil
}

func (p *serverProcess) stop(ctx context.Context) error {
	select {
	case <-p.done:
		return p.finish()
	default:
	}
	if err := p.command.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		_ = p.command.Process.Kill()
		<-p.done
		return errors.Join(fmt.Errorf("signal PowerContext Server: %w", err), p.finish())
	}
	select {
	case <-p.done:
		return p.finish()
	case <-ctx.Done():
		killErr := p.command.Process.Kill()
		<-p.done
		return errors.Join(errors.New("PowerContext Server did not stop gracefully"), killErr, p.finish())
	}
}

func (p *serverProcess) finish() error {
	p.closed.Do(func() { _ = p.log.Close() })
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.waitErr != nil {
		return fmt.Errorf("PowerContext Server exited: %w", p.waitErr)
	}
	return nil
}

func waitUntilReady(ctx context.Context, process *serverProcess, baseURL string) error {
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: noRedirect}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error = errors.New("server did not respond")
	for {
		select {
		case <-process.done:
			return errors.Join(errors.New("PowerContext Server exited before readiness"), process.finish())
		case <-ctx.Done():
			return fmt.Errorf("PowerContext Server was not ready: %w", lastErr)
		case <-ticker.C:
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health/ready", nil)
			if err != nil {
				return err
			}
			response, err := client.Do(request)
			if err != nil {
				lastErr = err
				continue
			}
			var body struct {
				Status string `json:"status"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK && decodeErr == nil && (body.Status == "ready" || body.Status == "degraded") {
				return nil
			}
			lastErr = fmt.Errorf("readiness returned HTTP %d with status %q", response.StatusCode, body.Status)
		}
	}
}

func exerciseCLI(ctx context.Context, binary string, environment []string, baseURL, token string) error {
	environment = append(slices.Clone(environment), "POWERCONTEXT_CLIENT_SERVER_URL="+baseURL)
	if token != "" {
		environment = append(environment, "POWERCONTEXT_CLIENT_API_TOKEN="+token)
	}
	command := exec.CommandContext(ctx, binary, "--json", "ready")
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("execute CLI readiness: %w", err)
	}
	var readiness v1.ReadinessResponse
	if err := json.Unmarshal(output, &readiness); err != nil {
		return fmt.Errorf("decode CLI readiness: %w", err)
	}
	if readiness.Status != v1.ReadinessStatusReady && readiness.Status != v1.ReadinessStatusDegraded {
		return fmt.Errorf("CLI readiness status is %q", readiness.Status)
	}
	return nil
}

func exerciseMemory(ctx context.Context, baseURL, token string, remember bool) error {
	api, err := pcclient.New(baseURL, pcclient.Options{BearerToken: token, Timeout: 10 * time.Second})
	if err != nil {
		return err
	}
	readiness, err := api.GetReadiness(ctx)
	if err != nil {
		return fmt.Errorf("read readiness through Go Client: %w", err)
	}
	ready, ok := readiness.(*v1.GetReadinessOK)
	if !ok || (ready.Response.Status != v1.ReadinessStatusReady && ready.Response.Status != v1.ReadinessStatusDegraded) {
		return fmt.Errorf("Go Client readiness response is %T", readiness)
	}
	if remember {
		result, rememberErr := api.RememberMemory(ctx, &v1.RememberMemoryRequest{
			ScopeID: smokeScope, Kind: "fact", Text: smokeText,
		})
		if rememberErr != nil {
			return fmt.Errorf("remember through Go Client: %w", rememberErr)
		}
		mutation, mutationOK := result.(*v1.MemoryMutationResponseHeaders)
		if !mutationOK {
			return fmt.Errorf("remember response is %T", result)
		}
		entry, entryOK := mutation.Response.Entry.Get()
		if !entryOK || entry.Text != smokeText {
			return errors.New("remember did not return the exact Memory entry")
		}
	}
	result, err := api.SearchMemory(ctx, &v1.SearchMemoryRequest{
		ScopeID: smokeScope,
		Query:   "release verification retrieves private memory",
		Mode:    v1.NewOptMemorySearchMode(v1.MemorySearchModeFts),
	})
	if err != nil {
		return fmt.Errorf("search through Go Client: %w", err)
	}
	search, ok := result.(*v1.SearchMemoryResponseHeaders)
	if !ok {
		return fmt.Errorf("search response is %T", result)
	}
	if len(search.Response.Hits) != 1 || search.Response.Hits[0].Text != smokeText {
		return fmt.Errorf("search returned %d unexpected hits", len(search.Response.Hits))
	}
	return nil
}

func exerciseMCP(ctx context.Context, baseURL, token string, reports bool) (returnErr error) {
	httpClient := &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect}
	if token != "" {
		httpClient.Transport = bearerTransport{token: token, next: http.DefaultTransport}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "powercontext-process-smoke", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: baseURL + "/mcp/", HTTPClient: httpClient, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		return fmt.Errorf("connect MCP: %w", err)
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close MCP session: %w", closeErr))
		}
	}()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("list MCP tools: %w", err)
	}
	actual := make([]string, len(tools.Tools))
	for index, tool := range tools.Tools {
		actual[index] = tool.Name
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			return fmt.Errorf("MCP tool %q has no complete schema", tool.Name)
		}
	}
	expected := slices.Clone(baseToolNames)
	if reports {
		expected = append(
			expected,
			"get_handoff_report",
			"get_handoff_report_workspace",
			"list_handoff_report_known_scopes",
			"select_handoff_workstream",
		)
	}
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("MCP tools are %v, want %v", actual, expected)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "list_memory_entries", Arguments: map[string]any{"scope_id": smokeScope},
	})
	if err != nil {
		return fmt.Errorf("call MCP tool: %w", err)
	}
	if result.IsError || result.StructuredContent == nil {
		return errors.New("MCP list_memory_entries did not return structured content")
	}
	resources, err := session.ListResources(ctx, nil)
	if err != nil || len(resources.Resources) != 0 {
		return fmt.Errorf("MCP resources are not the frozen empty surface: %w", err)
	}
	prompts, err := session.ListPrompts(ctx, nil)
	if err != nil || len(prompts.Prompts) != 0 {
		return fmt.Errorf("MCP prompts are not the frozen empty surface: %w", err)
	}
	return nil
}

func exerciseSecureHTTP(ctx context.Context, baseURL string) error {
	checks := []struct {
		method, path, token, body string
		status                    int
	}{
		{http.MethodGet, "/health/live", "", "", http.StatusOK},
		{http.MethodGet, "/v1/capabilities", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/metrics", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/dashboard/scopes", "", "", http.StatusUnauthorized},
		{http.MethodPost, "/v1/handoff-reports/projects/list", smokeToken, `{}`, http.StatusOK},
	}
	for _, check := range checks {
		response, err := request(ctx, check.method, baseURL+check.path, check.token, check.body)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
		if response.StatusCode != check.status {
			return fmt.Errorf("%s %s returned HTTP %d, want %d", check.method, check.path, response.StatusCode, check.status)
		}
	}

	home, err := request(ctx, http.MethodGet, baseURL+"/", "", "")
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(home.Body, 1<<20))
	_ = home.Body.Close()
	if home.StatusCode != http.StatusOK || home.Header.Get("Content-Security-Policy") == "" || !strings.Contains(home.Header.Get("Cache-Control"), "no-store") {
		return fmt.Errorf("Dashboard home security headers are incomplete: HTTP %d", home.StatusCode)
	}

	scopes, err := request(ctx, http.MethodGet, baseURL+"/dashboard/scopes", smokeToken, "")
	if err != nil {
		return err
	}
	scopeBody, readErr := io.ReadAll(io.LimitReader(scopes.Body, 1<<20))
	_ = scopes.Body.Close()
	if readErr != nil || scopes.StatusCode != http.StatusOK || !bytes.Contains(scopeBody, []byte(smokeScope)) {
		return fmt.Errorf("authenticated Dashboard scopes failed: HTTP %d: %w", scopes.StatusCode, readErr)
	}

	metrics, err := request(ctx, http.MethodGet, baseURL+"/metrics", smokeToken, "")
	if err != nil {
		return err
	}
	metricBody, readErr := io.ReadAll(io.LimitReader(metrics.Body, 4<<20))
	_ = metrics.Body.Close()
	if readErr != nil || metrics.StatusCode != http.StatusOK || !bytes.Contains(metricBody, []byte("powercontext_server_runtime_ready 1")) {
		return fmt.Errorf("authenticated metrics failed: HTTP %d: %w", metrics.StatusCode, readErr)
	}
	if bytes.Contains(metricBody, []byte(smokeScope)) || bytes.Contains(metricBody, []byte(smokeText)) || bytes.Contains(metricBody, []byte(smokeToken)) {
		return errors.New("metrics leaked private smoke values")
	}
	return nil
}

func request(ctx context.Context, method, endpoint, token, body string) (*http.Response, error) {
	var input io.Reader
	if body != "" {
		input = strings.NewReader(body)
	}
	value, err := http.NewRequestWithContext(ctx, method, endpoint, input)
	if err != nil {
		return nil, err
	}
	if body != "" {
		value.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		value.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: noRedirect}).Do(value)
	if err != nil {
		return nil, fmt.Errorf("%s request failed: %w", method, err)
	}
	return response, nil
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (t bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	return t.next.RoundTrip(cloned)
}

func isolatedEnvironment(source []string, home string) []string {
	result := make([]string, 0, len(source)+3)
	for _, item := range source {
		name, _, found := strings.Cut(item, "=")
		if !found || name == "POWERCONTEXT_HOME" || strings.HasPrefix(name, "POWERCONTEXT_SERVER_") || strings.HasPrefix(name, "POWERCONTEXT_CLIENT_") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"POWERCONTEXT_HOME="+home,
		"POWERCONTEXT_SERVER_LOGGING_FORMAT=json",
		"POWERCONTEXT_SERVER_LOGGING_ACCESS=true",
	)
}

func verifyPrivateLog(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read server log: %w", err)
	}
	for _, required := range []string{`"event":"server.ready"`, `"event":"server.stopping"`, `"event":"server.stopped"`} {
		if !bytes.Contains(contents, []byte(required)) {
			return fmt.Errorf("server log does not contain %s", required)
		}
	}
	for _, private := range []string{smokeScope, smokeText, smokeToken} {
		if bytes.Contains(contents, []byte(private)) {
			return errors.New("server log leaked a private smoke value")
		}
	}
	return nil
}

func withLog(cause error, path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("read failed server log: %w", err))
	}
	return fmt.Errorf("%w\nPowerContext Server log:\n%s", cause, contents)
}
