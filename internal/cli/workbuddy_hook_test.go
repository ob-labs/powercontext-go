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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

const workBuddyHookEmptyResponse = `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":""}}` + "\n"

func TestWorkBuddyHookCommandWritesStableEmptyContext(t *testing.T) {
	home := filepath.Join(t.TempDir(), "workbuddy")
	t.Setenv(workBuddyHomeEnv, home)
	t.Setenv(clientURLVar, "://invalid-client-url")
	t.Setenv(clientTimeoutVar, "invalid-client-timeout")
	writeWorkBuddyHookConfiguration(t, home)

	var stdout, stderr bytes.Buffer
	command := newCommandWithAllDependencies(
		VersionInfo{Version: "test"}, &stdout, &stderr, nil, nil, &scriptedSystemCommands{t: t},
	)
	command.SetIn(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"secret-prompt","cwd":"C:/workspace"}`))
	command.SetArgs([]string{"hook", "workbuddy"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
	if got := stdout.String(); got != workBuddyHookEmptyResponse {
		t.Fatalf("hook stdout = %q, want %q", got, workBuddyHookEmptyResponse)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("hook diagnostics = %q, want empty", got)
	}
}

func TestWorkBuddyHookRecallCaptureFlush(t *testing.T) {
	var paths []string
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		switch request.URL.Path {
		case "/v1/context/prepare":
			var body struct {
				ScopeID  string `json:"scope_id"`
				MaxBytes int    `json:"max_bytes"`
			}
			if err := json.UnmarshalRead(request.Body, &body); err != nil {
				t.Fatal(err)
			}
			if body.ScopeID != "workbuddy:agent" || body.MaxBytes != 8000 {
				t.Fatalf("prepare = %#v", body)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"schema":"powercontext.prepared-context.v1","status":"ready","content":"remembered","content_bytes":10}`))
		case "/v1/sources/content":
			type captureBody struct {
				ScopeID  string            `json:"scope_id"`
				SourceID string            `json:"source_id"`
				Content  string            `json:"content"`
				Metadata map[string]string `json:"metadata"`
			}
			var body captureBody
			if err := json.UnmarshalRead(request.Body, &body); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte("workbuddy:agent\x00session\x00request\x00hello"))
			want := captureBody{
				ScopeID: "workbuddy:agent", SourceID: "workbuddy-user-prompt:" + hex.EncodeToString(sum[:]), Content: "hello",
				Metadata: map[string]string{
					"origin": "workbuddy", "event": "user_prompt_submit", "cwd": "C:/workspace",
					"session_id": "session", "prompt_id": "request",
				},
			}
			if diff := cmp.Diff(want, body); diff != "" {
				t.Fatalf("capture body (-want +got):\n%s", diff)
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusAccepted)
			_, _ = writer.Write([]byte(`{"status":"accepted","source":{"name":"content","source_id":"x"},"position":2}`))
		case "/v1/memory/flush":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"status":"processed","previous_cursor":0,"current_cursor":2,"high_watermark":2,"processed_source_count":1}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})
	client := &http.Client{Transport: workBuddyHookRoundTripper(func(request *http.Request) (*http.Response, error) {
		writer := httptest.NewRecorder()
		handler.ServeHTTP(writer, request)
		response := writer.Result()
		response.Request = request
		return response, nil
	})}
	t.Setenv("POWERCONTEXT_WORKBUDDY_CAPTURE_PROMPTS", "true")
	t.Setenv("POWERCONTEXT_WORKBUDDY_FLUSH_ON_CAPTURE", "true")
	var output, diagnostics bytes.Buffer
	err := runWorkBuddyHook(t.Context(), strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"hello","cwd":"C:/workspace","session_id":"session","request_id":"request"}`), &output, &diagnostics, workBuddyHookRuntime{configuration: workBuddyConfiguration{
		Schema: workBuddyConfigSchema, ServerURL: "http://127.0.0.1:8000", ScopeMode: "agent", AuthorizationEnvironment: workBuddyDefaultAuthorizationEnvironment,
		RequestTimeoutSeconds: 1, RequestBudgetSeconds: 2, PrepareMaxBytes: 8000, SourceMaxBytes: 16384,
	}, httpClient: client})
	if err != nil {
		t.Fatalf("runWorkBuddyHook() error = %v", err)
	}
	if got, want := output.String(), `{"hookSpecificOutput":{"hookEventName":"UserPromptSubmit","additionalContext":"remembered"}}`+"\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	wantPaths := []string{"/v1/context/prepare", "/v1/sources/content", "/v1/memory/flush"}
	if diff := cmp.Diff(wantPaths, paths); diff != "" {
		t.Fatalf("request paths (-want +got):\n%s", diff)
	}
}

func TestWorkBuddyHookEncodedResponseOverLimitWritesEmptyContext(t *testing.T) {
	content := strings.Repeat("\x01", workBuddyMaximumPrepareBytes)
	prepared := &v1.PreparedContextHeaders{Response: v1.PreparedContext{
		Schema:  v1.PreparedContextSchemaPowercontextPreparedContextV1,
		Status:  v1.PreparedContextStatusReady,
		Content: v1.NewNilString(content), ContentBytes: len(content),
	}}
	additionalContext := preparedWorkBuddyContext(prepared, workBuddyMaximumPrepareBytes)
	if additionalContext != content {
		t.Fatal("valid prepared context was rejected before protocol encoding")
	}

	var output bytes.Buffer
	if err := writeWorkBuddyHookResponse(&output, additionalContext); err != nil {
		t.Fatalf("writeWorkBuddyHookResponse() error = %v", err)
	}
	if got := output.String(); got != workBuddyHookEmptyResponse {
		t.Fatalf("output = %q, want exact empty-context response", got)
	}
}

func TestWorkBuddyHookPrepareFailureStillCapturesAndFlushes(t *testing.T) {
	var paths []string
	client := &http.Client{Transport: workBuddyHookRoundTripper(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Path)
		writer := httptest.NewRecorder()
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/context/prepare":
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":{"code":"unavailable","message":"try later"}}`))
		case "/v1/sources/content":
			writer.WriteHeader(http.StatusAccepted)
			_, _ = writer.Write([]byte(`{"status":"accepted","source":{"name":"content","source_id":"x"},"position":3}`))
		case "/v1/memory/flush":
			_, _ = writer.Write([]byte(`{"status":"processed","previous_cursor":0,"current_cursor":3,"high_watermark":3,"processed_source_count":1}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
		response := writer.Result()
		response.Request = request
		return response, nil
	})}
	t.Setenv("POWERCONTEXT_WORKBUDDY_CAPTURE_PROMPTS", "true")
	t.Setenv("POWERCONTEXT_WORKBUDDY_FLUSH_ON_CAPTURE", "true")
	var output, diagnostics bytes.Buffer
	err := runWorkBuddyHook(t.Context(), strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"hello","cwd":"C:/workspace","session_id":"session","prompt_id":"prompt"}`), &output, &diagnostics, workBuddyHookRuntime{configuration: workBuddyConfiguration{
		Schema: workBuddyConfigSchema, ServerURL: "http://127.0.0.1:8000", ScopeMode: "agent", AuthorizationEnvironment: workBuddyDefaultAuthorizationEnvironment,
		RequestTimeoutSeconds: 1, RequestBudgetSeconds: 2, PrepareMaxBytes: 8000, SourceMaxBytes: 16384,
	}, httpClient: client})
	if err != nil {
		t.Fatalf("runWorkBuddyHook() error = %v", err)
	}
	if got := output.String(); got != workBuddyHookEmptyResponse {
		t.Fatalf("output = %q, want empty recalled context", got)
	}
	wantPaths := []string{"/v1/context/prepare", "/v1/sources/content", "/v1/memory/flush"}
	if diff := cmp.Diff(wantPaths, paths); diff != "" {
		t.Fatalf("request paths (-want +got):\n%s", diff)
	}
}

func TestWorkBuddyHookOverLimitPrepareResponseFailsOpenBeforeDecode(t *testing.T) {
	const prepared = `{"schema":"powercontext.prepared-context.v1","status":"ready","content":"remembered","content_bytes":10}`
	body := &workBuddyHookTrackedBody{Reader: strings.NewReader(prepared + strings.Repeat(" ", 8*1024))}
	client := &http.Client{Transport: workBuddyHookRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Header:        http.Header{"Content-Type": []string{"application/json"}},
			ContentLength: int64(len(prepared) + 8*1024),
			Body:          body,
			Request:       request,
		}, nil
	})}
	getenv := func(name string) string {
		if name == "POWERCONTEXT_WORKBUDDY_CAPTURE_PROMPTS" {
			return "false"
		}
		return ""
	}
	var output, diagnostics bytes.Buffer
	err := runWorkBuddyHook(t.Context(), strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"hello","cwd":"C:/workspace"}`), &output, &diagnostics, workBuddyHookRuntime{
		configuration: workBuddyConfiguration{
			Schema: workBuddyConfigSchema, ServerURL: "http://127.0.0.1:8000", ScopeMode: "agent", AuthorizationEnvironment: workBuddyDefaultAuthorizationEnvironment,
			RequestTimeoutSeconds: 1, RequestBudgetSeconds: 2, PrepareMaxBytes: 8000, SourceMaxBytes: 64,
		},
		getenv: getenv, httpClient: client,
	})
	if err != nil {
		t.Fatalf("runWorkBuddyHook() error = %v", err)
	}
	if got := output.String(); got != workBuddyHookEmptyResponse {
		t.Fatalf("output = %q, want empty recalled context", got)
	}
	if body.read > 0 {
		t.Fatalf("generated client read %d over-limit response bytes", body.read)
	}
	if body.closes != 1 {
		t.Fatalf("response body close calls = %d, want 1", body.closes)
	}
}

func TestWorkBuddyHookResponseLimitPreservesErrorAndCloseMatching(t *testing.T) {
	body := &workBuddyHookTrackedBody{Reader: strings.NewReader(strings.Repeat("x", 8001))}
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(time.Second))
	defer cancel()
	client, ok := newWorkBuddyHookClient(ctx, workBuddyConfiguration{
		Schema: workBuddyConfigSchema, ServerURL: "http://127.0.0.1:8000", ScopeMode: "agent", AuthorizationEnvironment: workBuddyDefaultAuthorizationEnvironment,
		RequestTimeoutSeconds: 1, RequestBudgetSeconds: 2, PrepareMaxBytes: 8000, SourceMaxBytes: 64,
	}, func(string) string { return "" }, &http.Client{Transport: workBuddyHookRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, ContentLength: 8001, Body: body, Request: request,
		}, nil
	})}, time.Now)
	if !ok {
		t.Fatal("newWorkBuddyHookClient() did not construct a client")
	}
	_, err := client.PrepareContext(t.Context(), &v1.PrepareContextRequest{ScopeID: "scope", Query: "query"})
	if !errors.Is(err, errWorkBuddyHookResponseTooLarge) {
		t.Fatalf("PrepareContext() error = %v, want response limit error", err)
	}
	if body.read != 0 || body.closes != 1 {
		t.Fatalf("over-limit body reads/closes = %d/%d, want 0/1", body.read, body.closes)
	}
}

func TestWorkBuddyHookUnknownLengthResponseStopsAtLimitBeforeDecode(t *testing.T) {
	const prepared = `{"schema":"powercontext.prepared-context.v1","status":"ready","content":"remembered","content_bytes":10}`
	body := &workBuddyHookTrackedBody{Reader: strings.NewReader(prepared + strings.Repeat(" ", 16*1024))}
	client := &http.Client{Transport: workBuddyHookRoundTripper(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, ContentLength: -1, Body: body, Request: request,
		}, nil
	})}
	var output, diagnostics bytes.Buffer
	err := runWorkBuddyHook(t.Context(), strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"hello","cwd":"C:/workspace"}`), &output, &diagnostics, workBuddyHookRuntime{
		configuration: workBuddyConfiguration{
			Schema: workBuddyConfigSchema, ServerURL: "http://127.0.0.1:8000", ScopeMode: "agent", AuthorizationEnvironment: workBuddyDefaultAuthorizationEnvironment,
			RequestTimeoutSeconds: 1, RequestBudgetSeconds: 2, PrepareMaxBytes: 8000, SourceMaxBytes: 64,
		},
		getenv: func(name string) string {
			if name == "POWERCONTEXT_WORKBUDDY_CAPTURE_PROMPTS" {
				return "false"
			}
			return ""
		},
		httpClient: client,
	})
	if err != nil {
		t.Fatalf("runWorkBuddyHook() error = %v", err)
	}
	if got := output.String(); got != workBuddyHookEmptyResponse {
		t.Fatalf("output = %q, want empty recalled context", got)
	}
	if want := workBuddyHookResponseLimit(workBuddyConfiguration{PrepareMaxBytes: 8000, SourceMaxBytes: 64}) + 1; body.read != want {
		t.Fatalf("response body reads = %d, want one bounded probe after %d bytes", body.read, want-1)
	}
	if body.closes != 1 {
		t.Fatalf("response body close calls = %d, want 1", body.closes)
	}
}

func TestWorkBuddyHookPrepareFailuresStillCapture(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*http.Request) (*http.Response, error)
	}{
		{
			name: "unauthorized",
			prepare: func(request *http.Request) (*http.Response, error) {
				return workBuddyHookJSONResponse(request, http.StatusUnauthorized, `{"error":{"code":"unauthorized","message":"denied"}}`), nil
			},
		},
		{
			name: "not found",
			prepare: func(request *http.Request) (*http.Response, error) {
				return workBuddyHookJSONResponse(request, http.StatusNotFound, `{"error":{"code":"not_found","message":"missing"}}`), nil
			},
		},
		{
			name: "unavailable",
			prepare: func(request *http.Request) (*http.Response, error) {
				return workBuddyHookJSONResponse(request, http.StatusServiceUnavailable, `{"error":{"code":"unavailable","message":"later"}}`), nil
			},
		},
		{
			name: "invalid prepared body",
			prepare: func(request *http.Request) (*http.Response, error) {
				return workBuddyHookJSONResponse(request, http.StatusOK, `{}`), nil
			},
		},
		{
			name: "request timeout",
			prepare: func(*http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var paths []string
			client := &http.Client{Transport: workBuddyHookRoundTripper(func(request *http.Request) (*http.Response, error) {
				paths = append(paths, request.URL.Path)
				switch request.URL.Path {
				case "/v1/context/prepare":
					return test.prepare(request)
				case "/v1/sources/content":
					if err := request.Context().Err(); err != nil {
						t.Fatalf("capture context error = %v, want time remaining", err)
					}
					return workBuddyHookJSONResponse(request, http.StatusAccepted, `{"status":"accepted","source":{"name":"content","source_id":"x"},"position":1}`), nil
				default:
					return workBuddyHookJSONResponse(request, http.StatusNotFound, ``), nil
				}
			})}
			var output, diagnostics bytes.Buffer
			err := runWorkBuddyHook(t.Context(), strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"hello","cwd":"C:/workspace"}`), &output, &diagnostics, workBuddyHookRuntime{
				configuration: workBuddyConfiguration{
					Schema: workBuddyConfigSchema, ServerURL: "http://127.0.0.1:8000", ScopeMode: "agent", AuthorizationEnvironment: workBuddyDefaultAuthorizationEnvironment,
					RequestTimeoutSeconds: 1, RequestBudgetSeconds: 2, PrepareMaxBytes: 8000, SourceMaxBytes: 16384,
				},
				getenv: func(name string) string {
					if name == "POWERCONTEXT_WORKBUDDY_CAPTURE_PROMPTS" {
						return "true"
					}
					return ""
				},
				httpClient: client,
			})
			if err != nil {
				t.Fatalf("runWorkBuddyHook() error = %v", err)
			}
			if got := output.String(); got != workBuddyHookEmptyResponse {
				t.Fatalf("output = %q, want empty recalled context", got)
			}
			if diff := cmp.Diff([]string{"/v1/context/prepare", "/v1/sources/content"}, paths); diff != "" {
				t.Fatalf("request paths (-want +got):\n%s", diff)
			}
		})
	}
}

func workBuddyHookJSONResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}

func TestResolveWorkBuddyHookScope(t *testing.T) {
	getenv := func(string) string { return "" }

	for _, test := range []struct {
		name   string
		mode   string
		getenv func(string) string
		setup  func(*testing.T, string)
		want   func(*testing.T, string) string
	}{
		{
			name: "explicit scope retains 256 byte boundary",
			mode: "project",
			getenv: func(name string) string {
				if name == "POWERCONTEXT_WORKBUDDY_SCOPE_ID" {
					return strings.Repeat("e", 256)
				}
				return ""
			},
			want: func(*testing.T, string) string { return strings.Repeat("e", 256) },
		},
		{
			name: "explicit scope hashes above 256 byte boundary",
			mode: "project",
			getenv: func(name string) string {
				if name == "POWERCONTEXT_WORKBUDDY_SCOPE_ID" {
					return strings.Repeat("e", 257)
				}
				return ""
			},
			want: func(*testing.T, string) string {
				sum := sha256.Sum256([]byte(strings.Repeat("e", 257)))
				return "sha256:" + hex.EncodeToString(sum[:])
			},
		},
		{name: "agent mode", mode: "agent", getenv: getenv, want: func(*testing.T, string) string { return "workbuddy:agent" }},
		{
			name:   "git private binding",
			mode:   "project",
			getenv: getenv,
			setup: func(t *testing.T, project string) {
				gitDirectory := workBuddyHookGitOutput(t, project, "rev-parse", "--absolute-git-dir")
				writeTestFile(t, filepath.Join(gitDirectory, "powercontext", "codex-workspace.json"), `{"schema":"powercontext.codex-workspace.v1","scope_id":"bound-scope"}`)
			},
			want: func(*testing.T, string) string { return "bound-scope" },
		},
		{
			name:   "normalized remote",
			mode:   "project",
			getenv: getenv,
			setup: func(t *testing.T, project string) {
				workBuddyHookGit(t, project, "config", "remote.origin.url", "ssh://user@GitHub.COM:8443/ob-labs/powercontext-go.git")
			},
			want: func(*testing.T, string) string { return "git:github.com:8443/ob-labs/powercontext-go" },
		},
		{
			name:   "local path fallback",
			mode:   "project",
			getenv: getenv,
			want:   workBuddyHookLocalScope,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := newWorkBuddyHookGitProject(t)
			if test.setup != nil {
				test.setup(t, project)
			}
			got, ok := resolveWorkBuddyHookScope(t.Context(), project, test.mode, test.getenv)
			if want := test.want(t, project); !ok || got != want {
				t.Fatalf("resolveWorkBuddyHookScope() = %q, %t, want %q, true", got, ok, want)
			}
		})
	}
}

func TestResolveWorkBuddyHookScopeUTF8ByteBoundaries(t *testing.T) {
	exactly256 := strings.Repeat("界", 85) + "a"
	over256 := exactly256 + "b"
	if got := len([]byte(exactly256)); got != 256 {
		t.Fatalf("exactly256 byte length = %d, want 256", got)
	}
	if got := len([]byte(over256)); got != 257 {
		t.Fatalf("over256 byte length = %d, want 257", got)
	}

	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "explicit exact boundary", value: exactly256, want: exactly256},
		{
			name:  "explicit above boundary",
			value: over256,
			want: func() string {
				sum := sha256.Sum256([]byte(over256))
				return "sha256:" + hex.EncodeToString(sum[:])
			}(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := resolveWorkBuddyHookScope(t.Context(), t.TempDir(), "project", func(name string) string {
				if name == workBuddyHookScopeEnvironment {
					return test.value
				}
				return ""
			})
			if !ok || got != test.want {
				t.Fatalf("resolveWorkBuddyHookScope() = %q, %t, want %q, true", got, ok, test.want)
			}
		})
	}

	project := newWorkBuddyHookGitProject(t)
	gitDirectory := workBuddyHookGitOutput(t, project, "rev-parse", "--absolute-git-dir")
	writeTestFile(t, filepath.Join(gitDirectory, "powercontext", "codex-workspace.json"), `{"schema":"powercontext.codex-workspace.v1","scope_id":`+strconv.Quote(exactly256)+`}`)
	got, ok := resolveWorkBuddyHookScope(t.Context(), project, "project", func(string) string { return "" })
	if !ok || got != exactly256 {
		t.Fatalf("Git-private UTF-8 boundary scope = %q, %t, want %q, true", got, ok, exactly256)
	}
}

func TestResolveWorkBuddyHookScopeInvalidGitPrivateBindingFallsThrough(t *testing.T) {
	remote := "git@GitHub.COM:ob-labs/powercontext-go.git"
	const wantRemote = "git:github.com/ob-labs/powercontext-go"
	over256 := strings.Repeat("界", 85) + "ab"

	for _, test := range []struct {
		name    string
		payload string
		remote  bool
		want    func(*testing.T, string) string
	}{
		{name: "wrong schema", payload: `{"schema":"wrong","scope_id":"bound"}`, remote: true, want: func(*testing.T, string) string { return wantRemote }},
		{name: "empty scope", payload: `{"schema":"powercontext.codex-workspace.v1","scope_id":""}`, remote: true, want: func(*testing.T, string) string { return wantRemote }},
		{name: "whitespace padded scope", payload: `{"schema":"powercontext.codex-workspace.v1","scope_id":" bound "}`, remote: true, want: func(*testing.T, string) string { return wantRemote }},
		{name: "malformed JSON", payload: `{`, remote: true, want: func(*testing.T, string) string { return wantRemote }},
		{name: "UTF-8 scope above boundary", payload: `{"schema":"powercontext.codex-workspace.v1","scope_id":` + strconv.Quote(over256) + `}`, remote: true, want: func(*testing.T, string) string { return wantRemote }},
		{name: "no remote uses local fallback", payload: `{`, want: workBuddyHookLocalScope},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := newWorkBuddyHookGitProject(t)
			gitDirectory := workBuddyHookGitOutput(t, project, "rev-parse", "--absolute-git-dir")
			writeTestFile(t, filepath.Join(gitDirectory, "powercontext", "codex-workspace.json"), test.payload)
			if test.remote {
				workBuddyHookGit(t, project, "config", "remote.origin.url", remote)
			}
			got, ok := resolveWorkBuddyHookScope(t.Context(), project, "project", func(string) string { return "" })
			if want := test.want(t, project); !ok || got != want {
				t.Fatalf("resolveWorkBuddyHookScope() = %q, %t, want %q, true", got, ok, want)
			}
		})
	}
}

func TestWorkBuddyHookRequestTimeoutUsesRemainingBudget(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	var requestDeadline time.Time
	client, ok := newWorkBuddyHookClient(ctx, workBuddyConfiguration{
		ServerURL: "http://127.0.0.1:8000", AuthorizationEnvironment: workBuddyDefaultAuthorizationEnvironment,
		RequestTimeoutSeconds: 30,
	}, func(string) string { return "" }, &http.Client{Transport: workBuddyHookRoundTripper(func(request *http.Request) (*http.Response, error) {
		requestDeadline, _ = request.Context().Deadline()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"schema":"powercontext.prepared-context.v1","status":"ready","content":"ok","content_bytes":2}`)),
			Request:    request,
		}, nil
	})}, func() time.Time {
		return deadline.Add(-250 * time.Millisecond)
	})
	if !ok {
		t.Fatal("newWorkBuddyHookClient() did not construct a client")
	}
	if _, err := client.PrepareContext(ctx, &v1.PrepareContextRequest{ScopeID: "scope", Query: "query"}); err != nil {
		t.Fatalf("PrepareContext() error = %v", err)
	}
	remaining := time.Until(requestDeadline)
	if remaining < 100*time.Millisecond || remaining > 500*time.Millisecond {
		t.Fatalf("request deadline remaining = %s, want timeout capped near 250ms", remaining)
	}
}

func TestNormalizeWorkBuddyHookGitRemote(t *testing.T) {
	for _, test := range []struct {
		name   string
		remote string
		want   string
	}{
		{name: "scp", remote: "git@GitHub.COM:ob-labs/powercontext-go.git", want: "github.com/ob-labs/powercontext-go"},
		{name: "http strips credentials and retains port", remote: "http://user:secret@GitHub.COM:8443/ob-labs\\powercontext-go.git", want: "github.com:8443/ob-labs/powercontext-go"},
		{name: "https", remote: "https://GitHub.COM/ob-labs/powercontext-go.git", want: "github.com/ob-labs/powercontext-go"},
		{name: "ssh", remote: "ssh://git@GitHub.COM/ob-labs/powercontext-go.git", want: "github.com/ob-labs/powercontext-go"},
		{name: "git", remote: "git://GitHub.COM/ob-labs/powercontext-go.git", want: "github.com/ob-labs/powercontext-go"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeWorkBuddyHookGitRemote(test.remote); got != test.want {
				t.Fatalf("normalizeWorkBuddyHookGitRemote() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveWorkBuddyHookScopeHashesLongRemote(t *testing.T) {
	project := newWorkBuddyHookGitProject(t)
	path := strings.Repeat("x", 300)
	workBuddyHookGit(t, project, "config", "remote.origin.url", "git@github.com:"+path+".git")

	got, ok := resolveWorkBuddyHookScope(t.Context(), project, "project", func(string) string { return "" })
	sum := sha256.Sum256([]byte("github.com/" + path))
	want := "git:sha256:" + hex.EncodeToString(sum[:])
	if !ok || got != want {
		t.Fatalf("resolveWorkBuddyHookScope() = %q, %t, want %q, true", got, ok, want)
	}
}

func newWorkBuddyHookGitProject(t *testing.T) string {
	t.Helper()
	project := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	workBuddyHookGit(t, project, "init")
	return project
}

func workBuddyHookGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func workBuddyHookGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", arguments...)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func workBuddyHookLocalScope(t *testing.T, project string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(resolved))
	return "local:" + hex.EncodeToString(sum[:])
}

type workBuddyHookRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip workBuddyHookRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type workBuddyHookTrackedBody struct {
	io.Reader
	read   int
	closes int
}

func (body *workBuddyHookTrackedBody) Read(buffer []byte) (int, error) {
	count, err := body.Reader.Read(buffer)
	body.read += count
	return count, err
}

func (body *workBuddyHookTrackedBody) Close() error {
	body.closes++
	return nil
}

func TestWorkBuddyHookTransportPolicies(t *testing.T) {
	proxyURL, err := url.Parse("http://proxy.invalid:8080")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		endpoint   string
		wantProxy  bool
		wantRefuse bool
	}{
		{name: "complete IPv4 loopback range bypasses proxy", endpoint: "http://127.0.0.2:8000/v1/context/prepare"},
		{name: "IPv6 loopback bypasses proxy", endpoint: "http://[::1]:8000/v1/context/prepare"},
		{name: "remote HTTPS delegates proxy", endpoint: "https://memory.example/v1/context/prepare", wantProxy: true},
		{name: "remote plaintext is refused before dispatch", endpoint: "http://memory.example/v1/context/prepare", wantRefuse: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, test.endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantRefuse {
				dispatches := 0
				client := workBuddyHookHTTPClient(&http.Client{Transport: workBuddyHookRoundTripper(func(*http.Request) (*http.Response, error) {
					dispatches++
					return nil, errors.New("unexpected transport dispatch")
				})})
				response, roundTripErr := client.Transport.RoundTrip(request)
				if response != nil && response.Body != nil {
					_ = response.Body.Close()
				}
				if !errors.Is(roundTripErr, errWorkBuddyHookPlaintextNonLoopback) || dispatches != 0 {
					t.Fatalf("remote plaintext error/dispatches = %v/%d, want refusal before dispatch", roundTripErr, dispatches)
				}
				return
			}
			proxyCalls := 0
			client := workBuddyHookHTTPClient(&http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) {
				proxyCalls++
				return proxyURL, nil
			}}})
			bounded := client.Transport.(workBuddyHookResponseRoundTripper)
			transport := bounded.next.(*http.Transport)
			got, err := transport.Proxy(request)
			wantCalls := 0
			if test.wantProxy {
				wantCalls = 1
			}
			if err != nil || (got != nil) != test.wantProxy || proxyCalls != wantCalls {
				t.Fatalf("proxy/result calls = %v/%v/%d, want proxy=%t", got, err, proxyCalls, test.wantProxy)
			}
			if test.wantProxy && got.String() != proxyURL.String() {
				t.Fatalf("remote HTTPS proxy = %v, want %v", got, proxyURL)
			}
		})
	}
}

// Regression for #199: WorkBuddy stores an Authorization header while pcclient
// accepts the bearer token without its scheme prefix.
func TestWorkBuddyHookClientSendsConfiguredBearerHeaderOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	const authorization = "Bearer workbuddy-service-token"
	var receivedAuthorization string
	client, ok := newWorkBuddyHookClient(ctx, workBuddyConfiguration{
		ServerURL:                "http://127.0.0.1:8000",
		AuthorizationEnvironment: "WORKBUDDY_SERVICE_TOKEN",
		RequestTimeoutSeconds:    1,
	}, func(name string) string {
		if name == "WORKBUDDY_SERVICE_TOKEN" {
			return authorization
		}
		return ""
	}, &http.Client{Transport: workBuddyHookRoundTripper(func(request *http.Request) (*http.Response, error) {
		receivedAuthorization = request.Header.Get("Authorization")
		return workBuddyHookJSONResponse(request, http.StatusOK, `{"schema":"powercontext.prepared-context.v1","status":"ready","content":"ok","content_bytes":2}`), nil
	})}, time.Now)
	if !ok {
		t.Fatal("newWorkBuddyHookClient() did not construct a client")
	}
	if _, err := client.PrepareContext(ctx, &v1.PrepareContextRequest{ScopeID: "scope", Query: "query"}); err != nil {
		t.Fatalf("PrepareContext() error = %v", err)
	}
	if receivedAuthorization != authorization {
		t.Fatalf("Authorization = %q, want %q", receivedAuthorization, authorization)
	}
}

func TestWorkBuddyHookFailOpen(t *testing.T) {
	const authorization = "Bearer confidential-value"
	t.Setenv(clientURLVar, "")
	for _, test := range []struct {
		name  string
		input string
		setup func(*testing.T, string)
		want  string
	}{
		{
			name:  "unrelated event is silent",
			input: `{"hook_event_name":"SessionStart"}`,
			setup: writeWorkBuddyHookConfiguration,
		},
		{
			name:  "malformed input",
			input: `{"hook_event_name":`,
			setup: writeWorkBuddyHookConfiguration,
			want:  workBuddyHookEmptyResponse,
		},
		{
			name:  "duplicate payload field",
			input: `{"hook_event_name":"SessionStart","hook_event_name":"SessionStart"}`,
			setup: writeWorkBuddyHookConfiguration,
			want:  workBuddyHookEmptyResponse,
		},
		{
			name:  "unknown payload field",
			input: `{"hook_event_name":"SessionStart","unexpected":true}`,
			setup: writeWorkBuddyHookConfiguration,
			want:  workBuddyHookEmptyResponse,
		},
		{
			name:  "oversized input",
			input: `{"hook_event_name":"UserPromptSubmit","prompt":"` + strings.Repeat("x", 64*1024) + `","cwd":"C:/workspace"}`,
			setup: writeWorkBuddyHookConfiguration,
			want:  workBuddyHookEmptyResponse,
		},
		{
			name:  "missing configuration",
			input: `{"hook_event_name":"UserPromptSubmit","prompt":"secret-prompt","cwd":"C:/workspace"}`,
			setup: func(*testing.T, string) {},
			want:  workBuddyHookEmptyResponse,
		},
		{
			name:  "invalid configuration",
			input: `{"hook_event_name":"UserPromptSubmit","prompt":"secret-prompt","cwd":"C:/workspace"}`,
			setup: func(t *testing.T, home string) {
				t.Helper()
				writeTestFile(t, filepath.Join(home, workBuddyConfigFilename), `{"schema":0}`)
			},
			want: workBuddyHookEmptyResponse,
		},
		{
			name:  "blank prompt",
			input: `{"hook_event_name":"UserPromptSubmit","prompt":"  ","cwd":"C:/workspace"}`,
			setup: writeWorkBuddyHookConfiguration,
			want:  workBuddyHookEmptyResponse,
		},
		{
			name:  "missing cwd",
			input: `{"hook_event_name":"UserPromptSubmit","prompt":"secret-prompt"}`,
			setup: writeWorkBuddyHookConfiguration,
			want:  workBuddyHookEmptyResponse,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "workbuddy")
			t.Setenv(workBuddyHomeEnv, home)
			t.Setenv(workBuddyDefaultAuthorizationEnvironment, authorization)
			test.setup(t, home)

			var stdout, stderr bytes.Buffer
			command := newCommand(VersionInfo{Version: "test"}, &stdout, &stderr)
			command.SetIn(strings.NewReader(test.input))
			command.SetArgs([]string{"hook", "workbuddy"})
			if err := command.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("ExecuteContext() error = %v", err)
			}
			if got := stdout.String(); got != test.want {
				t.Fatalf("hook stdout = %q, want %q", got, test.want)
			}
			for _, protected := range []string{"secret-prompt", authorization} {
				if strings.Contains(stdout.String(), protected) || strings.Contains(stderr.String(), protected) {
					t.Fatalf("hook output disclosed protected input %q: stdout=%q stderr=%q", protected, stdout.String(), stderr.String())
				}
			}
		})
	}
}

func writeWorkBuddyHookConfiguration(t *testing.T, home string) {
	t.Helper()
	configuration, err := newWorkBuddyConfiguration("http://127.0.0.1:8000", "project", workBuddyDefaultAuthorizationEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkBuddyJSON(filepath.Join(home, workBuddyConfigFilename), map[string]any{
		"schema":                    configuration.Schema,
		"server_url":                configuration.ServerURL,
		"scope_mode":                configuration.ScopeMode,
		"authorization_environment": configuration.AuthorizationEnvironment,
		"request_timeout_seconds":   configuration.RequestTimeoutSeconds,
		"request_budget_seconds":    configuration.RequestBudgetSeconds,
		"prepare_max_bytes":         configuration.PrepareMaxBytes,
		"source_max_bytes":          configuration.SourceMaxBytes,
	}); err != nil {
		t.Fatal(err)
	}
}
