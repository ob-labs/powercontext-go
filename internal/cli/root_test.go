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
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCommandTreeContainsProductCommands(t *testing.T) {
	t.Parallel()

	root := newCommand(VersionInfo{Version: "test"}, &bytes.Buffer{}, &bytes.Buffer{})
	want := []string{
		"candidate", "capabilities", "config", "doctor", "experience", "external-skill",
		"live", "ready", "server", "setup", "skill", "stats",
	}
	got := make([]string, 0, len(root.Commands()))
	for _, command := range root.Commands() {
		got = append(got, command.Name())
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("root commands = %v, want %v", got, want)
	}
}

func TestCapabilitiesUsesEnvironmentAndWritesHumanOutput(t *testing.T) {
	var authorization string
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"source_types":["content"],
			"artifact_families":["experience","skill"],
			"memory_extraction":true,
			"experience_generation":true,
			"managed_skill_generation":false,
			"external_skill_registry":false,
			"handoff_generation":true,
			"search_modes":["fts"],
			"context_versions":["powercontext.prepared-context.v1"]
		}`))
	})

	t.Setenv(clientURLVar, "https://powercontext.test")
	t.Setenv(clientTokenVar, "secret-token")
	t.Setenv(clientTimeoutVar, "2.5")
	var stdout, stderr bytes.Buffer
	root := newCommandWithHTTPClient(VersionInfo{Version: "test"}, &stdout, &stderr, httpClient)
	root.SetArgs([]string{"capabilities"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("ExecuteContext() error = %v; stderr = %s", err, stderr.String())
	}
	if authorization != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", authorization)
	}
	for _, fragment := range []string{"Source types: content", "Artifact families: experience, skill", "Memory extraction: enabled"} {
		if !strings.Contains(stdout.String(), fragment) {
			t.Errorf("output %q does not contain %q", stdout.String(), fragment)
		}
	}
}

func TestCapabilitiesFormatsServerErrorWithRequestID(t *testing.T) {
	httpClient := fakeHTTPClient(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-PowerContext-Request-ID", "request-42")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"code":"unauthorized","message":"authentication failed","details":null}}`))
	})

	var stdout, stderr bytes.Buffer
	root := newCommandWithHTTPClient(VersionInfo{Version: "test"}, &stdout, &stderr, httpClient)
	root.SetArgs([]string{"--server-url", "https://powercontext.test", "capabilities"})
	err := root.ExecuteContext(context.Background())
	if err == nil || err.Error() != "PowerContext Server returned HTTP 401 (unauthorized) (request ID: request-42)" {
		t.Fatalf("ExecuteContext() error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func fakeHTTPClient(handler http.HandlerFunc) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := newResponseRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.response(request), nil
	})}
}

type responseRecorder struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newResponseRecorder() *responseRecorder { return &responseRecorder{header: make(http.Header)} }

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(status int) { r.status = status }

func (r *responseRecorder) Write(value []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(value)
}

func (r *responseRecorder) response(request *http.Request) *http.Response {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     r.header.Clone(),
		Body:       io.NopCloser(bytes.NewReader(r.body.Bytes())),
		Request:    request,
	}
}

func TestRepeatedReferenceFlagPreservesComma(t *testing.T) {
	var stdout, stderr bytes.Buffer
	root := newCommand(VersionInfo{Version: "test"}, &stdout, &stderr)
	command, _, err := root.Find([]string{"experience", "generate"})
	if err != nil {
		t.Fatal(err)
	}
	if parseErr := command.ParseFlags([]string{"--source-ref", "content/a,b"}); parseErr != nil {
		t.Fatal(parseErr)
	}
	values, err := command.Flags().GetStringArray("source-ref")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || values[0] != "content/a,b" {
		t.Fatalf("source-ref = %q", values)
	}
}
