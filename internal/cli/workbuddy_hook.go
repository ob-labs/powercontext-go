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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-faster/jx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	pcclient "github.com/ob-labs/powercontext-go/client"
	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

const (
	workBuddyHookMaximumInputBytes  = 64 * 1024
	workBuddyHookMaximumOutputBytes = 64 * 1024
	workBuddyHookCloseTimeout       = 50 * time.Millisecond
)

var (
	errWorkBuddyHookPlaintextNonLoopback = errors.New("WorkBuddy Hook refuses plaintext HTTP to a non-loopback host")
	errWorkBuddyHookResponseTooLarge     = errors.New("WorkBuddy Hook response exceeds the configured byte limit")
)

const (
	workBuddyHookScopeEnvironment = "POWERCONTEXT_WORKBUDDY_SCOPE_ID"
)

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
	lookupEnv     func(string) (string, bool)
	httpClient    *http.Client
	now           func() time.Time
	scopeResolver workBuddyHookScopeResolver
}

type workBuddyHookScopeBindingKey struct {
	Integration string `json:"integration"`
	Kind        string `json:"kind"`
	ExternalID  string `json:"external_id"`
}

type workBuddyHookScopeResolver interface {
	Resolve(context.Context, *string, []workBuddyHookScopeBindingKey) (string, bool)
}

type workBuddyHookScopeResolverFunc func(context.Context, *string, []workBuddyHookScopeBindingKey) (string, bool)

func (resolve workBuddyHookScopeResolverFunc) Resolve(ctx context.Context, explicit *string, keys []workBuddyHookScopeBindingKey) (string, bool) {
	return resolve(ctx, explicit, keys)
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
	lookupEnv := runtime.lookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	now := runtime.now
	if now == nil {
		now = time.Now
	}
	budget := time.Duration(runtime.configuration.RequestBudgetSeconds * float64(time.Second))
	operationContext, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	client, ok := newWorkBuddyHookClient(operationContext, runtime.configuration, getenv, runtime.httpClient, now)
	if !ok {
		return writeWorkBuddyHookResponse(output, "")
	}
	resolver := runtime.scopeResolver
	if resolver == nil {
		resolver, ok = newWorkBuddyHookMCPScopeResolver(operationContext, runtime.configuration, getenv, runtime.httpClient, now)
		if !ok {
			return writeWorkBuddyHookResponse(output, "")
		}
	}
	scope, ok := resolver.Resolve(operationContext, workBuddyHookExplicitScope(lookupEnv), workBuddyHookScopeBindingKeys(operationContext, payload))
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
	timeout, ok := workBuddyHookRequestTimeout(ctx, configuration, now)
	if !ok {
		return nil, false
	}
	result, err := pcclient.New(configuration.ServerURL, pcclient.Options{BearerToken: workBuddyHookBearerToken(getenv(configuration.AuthorizationEnvironment)), Timeout: timeout, HTTPClient: workBuddyHookHTTPClient(supplied, workBuddyHookResponseLimit(configuration))})
	return result, err == nil
}

func workBuddyHookRequestTimeout(ctx context.Context, configuration workBuddyConfiguration, now func() time.Time) (time.Duration, bool) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, false
	}
	remaining := deadline.Sub(now())
	if remaining <= 0 {
		return 0, false
	}
	timeout := time.Duration(configuration.RequestTimeoutSeconds * float64(time.Second))
	return min(timeout, remaining), true
}

type workBuddyHookMCPScopeResolver struct {
	endpoint   string
	client     *mcp.Client
	httpClient *http.Client
	now        func() time.Time
}

func newWorkBuddyHookMCPScopeResolver(ctx context.Context, configuration workBuddyConfiguration, getenv func(string) string, supplied *http.Client, now func() time.Time) (workBuddyHookScopeResolver, bool) {
	timeout, ok := workBuddyHookRequestTimeout(ctx, configuration, now)
	if !ok {
		return nil, false
	}
	serverURL, err := normalizeWorkBuddyServerURL(configuration.ServerURL)
	if err != nil {
		return nil, false
	}
	httpClient := workBuddyHookHTTPClient(supplied, workBuddyHookMaximumOutputBytes)
	httpClient.Timeout = timeout
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if token := workBuddyHookBearerToken(getenv(configuration.AuthorizationEnvironment)); token != "" {
		httpClient.Transport = workBuddyHookAuthorizationTransport{next: httpClient.Transport, authorization: "Bearer " + token}
	}
	return workBuddyHookMCPScopeResolver{
		endpoint: serverURL + "/mcp/", client: mcp.NewClient(&mcp.Implementation{Name: "powercontext-workbuddy-hook", Version: "1"}, nil),
		httpClient: httpClient, now: now,
	}, true
}

func (resolver workBuddyHookMCPScopeResolver) Resolve(ctx context.Context, explicit *string, keys []workBuddyHookScopeBindingKey) (string, bool) {
	session, err := resolver.client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: resolver.endpoint, HTTPClient: resolver.httpClient, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return "", false
	}
	defer resolver.close(ctx, session)
	arguments := map[string]any{"binding_keys": keys}
	if explicit != nil {
		arguments["explicit_scope_id"] = *explicit
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "scope_binding_resolve", Arguments: arguments})
	if err != nil || result.IsError {
		return "", false
	}
	content, ok := result.StructuredContent.(map[string]any)
	if !ok {
		return "", false
	}
	scope, ok := content["scope_id"].(string)
	if !ok || !validWorkBuddyHookScope(scope) {
		return "", false
	}
	return scope, true
}

func (resolver workBuddyHookMCPScopeResolver) close(ctx context.Context, session *mcp.ClientSession) {
	timeout := workBuddyHookCloseTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := deadline.Sub(resolver.now())
		if remaining > 0 {
			timeout = min(timeout, remaining)
		}
	}
	resolver.httpClient.Timeout = timeout
	_ = session.Close()
}

type workBuddyHookAuthorizationTransport struct {
	next          http.RoundTripper
	authorization string
}

func (transport workBuddyHookAuthorizationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", transport.authorization)
	return transport.next.RoundTrip(clone)
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

func workBuddyHookExplicitScope(lookupEnv func(string) (string, bool)) *string {
	value, present := lookupEnv(workBuddyHookScopeEnvironment)
	if !present {
		return nil
	}
	return &value
}

func workBuddyHookScopeBindingKeys(ctx context.Context, payload workBuddyHookPayload) []workBuddyHookScopeBindingKey {
	keys := make([]workBuddyHookScopeBindingKey, 0, 2)
	if session := strings.TrimSpace(payload.SessionID); session != "" {
		keys = append(keys, workBuddyHookScopeBindingKey{Integration: "workbuddy", Kind: "session", ExternalID: session})
	}
	keys = append(keys, workBuddyHookScopeBindingKey{Integration: "workbuddy", Kind: "workspace", ExternalID: workBuddyHookWorkspaceHash(workBuddyHookGitRoot(ctx, payload.CWD))})
	return keys
}

func workBuddyHookGitRoot(ctx context.Context, cwd string) string {
	executable, err := exec.LookPath("git")
	if err != nil {
		return cwd
	}
	commandContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	command := exec.CommandContext(commandContext, executable, "rev-parse", "--show-toplevel")
	command.Dir = cwd
	output, err := command.Output()
	if err != nil {
		return cwd
	}
	root := strings.TrimSuffix(strings.TrimSuffix(string(output), "\n"), "\r")
	if root == "" {
		return cwd
	}
	return root
}

func workBuddyHookWorkspaceHash(workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	return fmtHex(sum[:])
}

func validWorkBuddyHookScope(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 256
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
	metadata, ok := workBuddyHookCaptureMetadata(session, promptID)
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

func workBuddyHookCaptureMetadata(sessionID, promptID string) (v1.CaptureContentSourceRequestMetadata, bool) {
	values := map[string]string{
		"origin": "workbuddy",
		"event":  "user_prompt_submit",
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
