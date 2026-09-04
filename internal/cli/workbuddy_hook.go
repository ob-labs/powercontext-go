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

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-faster/jx"
	"github.com/spf13/cobra"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	pcclient "github.com/ob-labs/powercontext-go/client"
	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

const (
	workBuddyHookMaximumInputBytes  = 64 * 1024
	workBuddyHookMaximumOutputBytes = 64 * 1024
	workBuddyHookMaximumScopeBytes  = 256
)

var (
	errWorkBuddyHookPlaintextNonLoopback = errors.New("WorkBuddy Hook refuses plaintext HTTP to a non-loopback host")
	errWorkBuddyHookResponseTooLarge     = errors.New("WorkBuddy Hook response exceeds the configured byte limit")
)

const (
	workBuddyHookScopeEnvironment = "POWERCONTEXT_WORKBUDDY_SCOPE_ID"
	workBuddyHookWorkspaceSchema  = "powercontext.codex-workspace.v1"
)

var workBuddyHookSCPRemote = regexp.MustCompile(`^(?:[^@/\s]+@)?([^:/\s]+):(.+)$`)

type workBuddyHookPayload struct {
	HookEventName string `json:"hook_event_name"`
	Prompt        string `json:"prompt"`
	UserPrompt    string `json:"user_prompt"`
	CWD           string `json:"cwd"`
	SessionID     string `json:"session_id"`
	PromptID      string `json:"prompt_id"`
	RequestID     string `json:"request_id"`
}

type workBuddyHookResponse struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

type workBuddyHookRuntime struct {
	configuration workBuddyConfiguration
	getenv        func(string) string
	httpClient    *http.Client
	now           func() time.Time
}

func newHookCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{
		Use:   "hook",
		Short: "Run PowerContext host integration hooks.",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(newWorkBuddyHookCommand(state))
	return command
}

func newWorkBuddyHookCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use:   "workbuddy",
		Short: "Handle one WorkBuddy hook payload.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			configuration := workBuddyHookConfiguration()
			return runWorkBuddyHook(command.Context(), command.InOrStdin(), state.stdout, state.stderr, workBuddyHookRuntime{
				configuration: configuration,
			})
		},
	}
}

func workBuddyHookConfiguration() workBuddyConfiguration {
	home, err := workBuddyHome()
	if err != nil {
		return workBuddyConfiguration{}
	}
	configuration, present, err := readWorkBuddyConfiguration(filepath.Join(home, workBuddyConfigFilename))
	if err != nil || !present {
		return workBuddyConfiguration{}
	}
	return configuration
}

// runWorkBuddyHook implements the process-level fail-open contract. Scope
func runWorkBuddyHook(
	ctx context.Context,
	input io.Reader,
	output, diagnostics io.Writer,
	runtime workBuddyHookRuntime,
) error {
	_ = diagnostics

	payload, err := decodeWorkBuddyHookPayload(input)
	if err != nil {
		return writeWorkBuddyHookResponse(output, "")
	}
	if payload.HookEventName != "UserPromptSubmit" {
		return nil
	}
	if validateWorkBuddyConfiguration(runtime.configuration) != nil || strings.TrimSpace(workBuddyHookPrompt(payload)) == "" || payload.CWD == "" {
		return writeWorkBuddyHookResponse(output, "")
	}

	getenv := runtime.getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	now := runtime.now
	if now == nil {
		now = time.Now
	}
	budget := time.Duration(runtime.configuration.RequestBudgetSeconds * float64(time.Second))
	operationContext, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	scope, ok := resolveWorkBuddyHookScope(operationContext, payload.CWD, runtime.configuration.ScopeMode, getenv)
	if !ok {
		return writeWorkBuddyHookResponse(output, "")
	}
	client, ok := newWorkBuddyHookClient(operationContext, runtime.configuration, getenv, runtime.httpClient, now)
	if !ok {
		return writeWorkBuddyHookResponse(output, "")
	}

	additionalContext := recallWorkBuddyContext(operationContext, client, payload, scope, runtime.configuration)
	prompt := workBuddyHookPrompt(payload)
	if workBuddyHookBool(getenv, "POWERCONTEXT_WORKBUDDY_CAPTURE_PROMPTS", true) && len([]byte(prompt)) <= runtime.configuration.SourceMaxBytes {
		position := captureWorkBuddyPrompt(operationContext, client, payload, prompt, scope)
		if workBuddyHookBool(getenv, "POWERCONTEXT_WORKBUDDY_FLUSH_ON_CAPTURE", false) {
			flushWorkBuddyPrompt(operationContext, client, scope, position, workBuddyHookFlushMaxCalls(getenv))
		}
	}
	return writeWorkBuddyHookResponse(output, additionalContext)
}

func recallWorkBuddyContext(ctx context.Context, client *pcclient.Client, payload workBuddyHookPayload, scope string, configuration workBuddyConfiguration) string {
	prompt := workBuddyHookPrompt(payload)
	prepared, err := client.PrepareContext(ctx, &v1.PrepareContextRequest{ScopeID: scope, Query: prompt, MaxBytes: v1.OptInt{Value: configuration.PrepareMaxBytes, Set: true}})
	if err != nil {
		return ""
	}
	return preparedWorkBuddyContext(prepared, configuration.PrepareMaxBytes)
}

func newWorkBuddyHookClient(ctx context.Context, configuration workBuddyConfiguration, getenv func(string) string, supplied *http.Client, now func() time.Time) (*pcclient.Client, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, false
	}
	remaining := deadline.Sub(now())
	if remaining <= 0 {
		return nil, false
	}
	timeout := time.Duration(configuration.RequestTimeoutSeconds * float64(time.Second))
	if remaining < timeout {
		timeout = remaining
	}
	result, err := pcclient.New(configuration.ServerURL, pcclient.Options{BearerToken: workBuddyHookBearerToken(getenv(configuration.AuthorizationEnvironment)), Timeout: timeout, HTTPClient: workBuddyHookHTTPClient(supplied, workBuddyHookResponseLimit(configuration))})
	return result, err == nil
}

func workBuddyHookBearerToken(authorization string) string {
	value := strings.TrimSpace(authorization)
	token, hasBearerPrefix := strings.CutPrefix(value, "Bearer ")
	if !hasBearerPrefix {
		return value
	}
	return strings.TrimSpace(token)
}

func workBuddyHookResponseLimit(configuration workBuddyConfiguration) int {
	return max(configuration.PrepareMaxBytes, configuration.SourceMaxBytes)
}

func workBuddyHookHTTPClient(supplied *http.Client, responseLimit ...int) *http.Client {
	var result http.Client
	if supplied != nil {
		result = *supplied
	}
	next := result.Transport
	transport, ok := next.(*http.Transport)
	if ok || next == nil {
		if !ok {
			transport = http.DefaultTransport.(*http.Transport)
		}
		transport = transport.Clone()
		proxy := transport.Proxy
		if proxy == nil {
			proxy = http.ProxyFromEnvironment
		}
		transport.Proxy = func(request *http.Request) (*url.URL, error) {
			if transportpolicy.IsLoopbackHost(request.URL.Hostname()) {
				return nil, nil
			}
			return proxy(request)
		}
		next = transport
	}
	maximum := workBuddyHookMaximumOutputBytes
	if len(responseLimit) > 0 && responseLimit[0] > 0 {
		maximum = responseLimit[0]
	}
	result.Transport = workBuddyHookResponseRoundTripper{next: next, maximum: int64(maximum)}
	return &result
}

type workBuddyHookResponseRoundTripper struct {
	next    http.RoundTripper
	maximum int64
}

func (transport workBuddyHookResponseRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transportpolicy.IsPlaintextNonLoopback(request.URL) {
		return nil, errWorkBuddyHookPlaintextNonLoopback
	}
	response, err := transport.next.RoundTrip(request)
	if err != nil || response == nil || response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices || response.Body == nil {
		return response, err
	}
	if response.ContentLength > transport.maximum {
		return nil, errors.Join(errWorkBuddyHookResponseTooLarge, response.Body.Close())
	}
	response.Body = &workBuddyHookResponseBody{body: response.Body, remaining: transport.maximum}
	return response, nil
}

type workBuddyHookResponseBody struct {
	body      io.ReadCloser
	remaining int64
	exceeded  bool
}

func (body *workBuddyHookResponseBody) Read(buffer []byte) (int, error) {
	if body.exceeded {
		return 0, errWorkBuddyHookResponseTooLarge
	}
	if body.remaining == 0 {
		var probe [1]byte
		count, err := body.body.Read(probe[:])
		if count > 0 {
			body.exceeded = true
			return 0, errors.Join(errWorkBuddyHookResponseTooLarge, err)
		}
		return 0, err
	}
	if int64(len(buffer)) > body.remaining {
		buffer = buffer[:body.remaining]
	}
	count, err := body.body.Read(buffer)
	body.remaining -= int64(count)
	return count, err
}

func (body *workBuddyHookResponseBody) Close() error {
	return body.body.Close()
}

func resolveWorkBuddyHookScope(ctx context.Context, cwd, mode string, getenv func(string) string) (string, bool) {
	if explicit := strings.TrimSpace(getenv(workBuddyHookScopeEnvironment)); explicit != "" {
		return boundedWorkBuddyScope("", explicit), true
	}
	if mode == "agent" {
		return "workbuddy:agent", true
	}
	if mode != "project" {
		return "", false
	}
	if gitDirectory := workBuddyHookGitValue(ctx, cwd, "rev-parse", "--absolute-git-dir"); gitDirectory != "" {
		if scope, ok := readWorkBuddyHookBoundScope(filepath.Join(gitDirectory, "powercontext", "codex-workspace.json")); ok {
			return scope, true
		}
	}
	projectRoot := workBuddyHookProjectRoot(cwd)
	if root := workBuddyHookGitValue(ctx, cwd, "rev-parse", "--show-toplevel"); root != "" {
		projectRoot = workBuddyHookProjectRoot(root)
	}
	if remote := workBuddyHookGitValue(ctx, projectRoot, "config", "--get", "remote.origin.url"); remote != "" {
		if normalized := normalizeWorkBuddyHookGitRemote(remote); normalized != "" {
			return boundedWorkBuddyScope("git", normalized), true
		}
	}
	sum := sha256.Sum256([]byte(projectRoot))
	return "local:" + fmtHex(sum[:]), true
}

func boundedWorkBuddyScope(prefix, value string) string {
	candidate := value
	if prefix != "" {
		candidate = prefix + ":" + value
	}
	if len(candidate) <= workBuddyHookMaximumScopeBytes {
		return candidate
	}
	sum := sha256.Sum256([]byte(value))
	if prefix == "" {
		return "sha256:" + fmtHex(sum[:])
	}
	return prefix + ":sha256:" + fmtHex(sum[:])
}

func readWorkBuddyHookBoundScope(path string) (string, bool) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var state struct {
		Schema  string `json:"schema"`
		ScopeID string `json:"scope_id"`
	}
	if err := json.Unmarshal(payload, &state); err != nil || state.Schema != workBuddyHookWorkspaceSchema || strings.TrimSpace(state.ScopeID) != state.ScopeID || state.ScopeID == "" || len(state.ScopeID) > workBuddyHookMaximumScopeBytes {
		return "", false
	}
	return state.ScopeID, true
}

func workBuddyHookGitValue(ctx context.Context, cwd string, arguments ...string) string {
	executable, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	commandContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, executable, arguments...)
	command.Dir = cwd
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func workBuddyHookProjectRoot(cwd string) string {
	root, err := filepath.Abs(cwd)
	if err != nil {
		return filepath.Clean(cwd)
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		return resolved
	}
	return filepath.Clean(root)
}

func normalizeWorkBuddyHookGitRemote(remote string) string {
	value := strings.TrimSpace(remote)
	if value == "" {
		return ""
	}
	if matched := workBuddyHookSCPRemote.FindStringSubmatch(value); matched != nil && !strings.Contains(value, "://") {
		if path := normalizeWorkBuddyHookGitPath(matched[2]); path != "" {
			return strings.ToLower(matched[1]) + "/" + path
		}
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "ssh" && parsed.Scheme != "git") || parsed.Hostname() == "" {
		return ""
	}
	path := normalizeWorkBuddyHookGitPath(parsed.Path)
	if path == "" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if port := parsed.Port(); port != "" {
		host += ":" + port
	}
	return host + "/" + path
}

func normalizeWorkBuddyHookGitPath(path string) string {
	parts := strings.Split(strings.ReplaceAll(path, `\`, "/"), "/")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			normalized = append(normalized, part)
		}
	}
	result := strings.Join(normalized, "/")
	return strings.TrimSuffix(result, ".git")
}

func fmtHex(value []byte) string {
	const digits = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for index, item := range value {
		result[index*2] = digits[item>>4]
		result[index*2+1] = digits[item&15]
	}
	return string(result)
}

func preparedWorkBuddyContext(response v1.PrepareContextRes, maximum int) string {
	value, ok := response.(*v1.PreparedContextHeaders)
	if !ok || value.Response.Schema != v1.PreparedContextSchemaPowercontextPreparedContextV1 || value.Response.Status != v1.PreparedContextStatusReady || value.Response.ContentBytes < 0 || value.Response.ContentBytes > maximum {
		return ""
	}
	content, ok := value.Response.Content.Get()
	if !ok || len([]byte(content)) != value.Response.ContentBytes || len([]byte(content)) > maximum {
		return ""
	}
	return content
}

func captureWorkBuddyPrompt(ctx context.Context, client *pcclient.Client, payload workBuddyHookPayload, prompt, scope string) int {
	session := strings.TrimSpace(payload.SessionID)
	promptID := strings.TrimSpace(payload.PromptID)
	if promptID == "" {
		promptID = strings.TrimSpace(payload.RequestID)
	}
	sum := sha256.Sum256([]byte(scope + "\x00" + session + "\x00" + promptID + "\x00" + prompt))
	metadata, ok := workBuddyHookCaptureMetadata(payload.CWD, session, promptID)
	if !ok {
		return 0
	}
	response, err := client.CaptureContentSource(ctx, &v1.CaptureContentSourceRequest{
		ScopeID: scope, SourceID: "workbuddy-user-prompt:" + fmtHex(sum[:]), Content: prompt,
		Metadata: v1.NewOptNilCaptureContentSourceRequestMetadata(metadata),
	})
	value, ok := response.(*v1.CaptureContentSourceResponseHeaders)
	if err != nil || !ok || value.Response.Status != v1.CaptureStatusAccepted || value.Response.Position < 1 {
		return 0
	}
	return value.Response.Position
}

func workBuddyHookCaptureMetadata(cwd, sessionID, promptID string) (v1.CaptureContentSourceRequestMetadata, bool) {
	values := map[string]string{
		"origin": "workbuddy",
		"event":  "user_prompt_submit",
		"cwd":    cwd,
	}
	if sessionID != "" {
		values["session_id"] = sessionID
	}
	if promptID != "" {
		values["prompt_id"] = promptID
	}
	metadata := make(v1.CaptureContentSourceRequestMetadata, len(values))
	for name, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, false
		}
		metadata[name] = jx.Raw(encoded)
	}
	return metadata, true
}

func flushWorkBuddyPrompt(ctx context.Context, client *pcclient.Client, scope string, position, maximum int) {
	if position == 0 {
		return
	}
	for range maximum {
		response, err := client.FlushMemory(ctx, &v1.FlushMemoryRequest{ScopeID: scope})
		value, ok := response.(*v1.FlushMemoryResponseHeaders)
		if err != nil || !ok || value.Response.CurrentCursor >= position {
			return
		}
	}
}

func workBuddyHookBool(getenv func(string) string, name string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(getenv(name)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func workBuddyHookFlushMaxCalls(getenv func(string) string) int {
	value, err := strconv.Atoi(strings.TrimSpace(getenv("POWERCONTEXT_WORKBUDDY_FLUSH_MAX_CALLS")))
	if err != nil || value < 1 || value > 16 {
		return 4
	}
	return value
}

func decodeWorkBuddyHookPayload(input io.Reader) (workBuddyHookPayload, error) {
	payload, err := io.ReadAll(io.LimitReader(input, workBuddyHookMaximumInputBytes+1))
	if err != nil || len(payload) > workBuddyHookMaximumInputBytes || !utf8.Valid(payload) {
		return workBuddyHookPayload{}, io.ErrUnexpectedEOF
	}
	var decoded workBuddyHookPayload
	if err := json.Unmarshal(payload, &decoded, json.RejectUnknownMembers(true)); err != nil {
		return workBuddyHookPayload{}, err
	}
	return decoded, nil
}

func workBuddyHookPrompt(payload workBuddyHookPayload) string {
	if payload.Prompt != "" {
		return payload.Prompt
	}
	return payload.UserPrompt
}

func writeWorkBuddyHookResponse(output io.Writer, additionalContext string) error {
	var response workBuddyHookResponse
	response.HookSpecificOutput.HookEventName = "UserPromptSubmit"
	response.HookSpecificOutput.AdditionalContext = additionalContext
	payload, err := json.Marshal(response)
	if err != nil || len(payload)+1 > workBuddyHookMaximumOutputBytes {
		if additionalContext != "" {
			return writeWorkBuddyHookResponse(output, "")
		}
		return nil
	}
	_, _ = output.Write(append(payload, '\n'))
	return nil
}
