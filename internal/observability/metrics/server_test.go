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

package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/ogen-go/ogen/middleware"
)

func TestMetricsFailureDoesNotChangeApplicationBehavior(t *testing.T) {
	// A zero-value Server deliberately makes every Prometheus collector panic.
	// The observability boundary must isolate those failures from application
	// behavior just as it would isolate a faulty collector at runtime.
	broken := &Server{}
	want := &struct{ value string }{value: "application response"}
	called := false

	response, err := broken.HTTPMiddleware(middleware.Request{
		Context: context.Background(), OperationID: "get_capabilities",
	}, func(request middleware.Request) (middleware.Response, error) {
		called = true
		if request.OperationID != "get_capabilities" {
			t.Fatalf("operation ID = %q", request.OperationID)
		}
		return middleware.Response{Type: want}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called || response.Type != want {
		t.Fatalf("application result = %#v, called = %t", response.Type, called)
	}

	// Readiness observation uses the same failure-isolation boundary.
	broken.SetReady(true)
	broken.SetRuntimeScopes(3, 1)
}

func TestRuntimeScopeMetricsHaveOnlyBoundedStateLabels(t *testing.T) {
	t.Parallel()
	server, err := New()
	if err != nil {
		t.Fatal(err)
	}
	server.SetRuntimeScopes(3, 1)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	text := response.Body.String()
	for _, want := range []string{
		`powercontext_server_runtime_scopes{state="active"} 1`,
		`powercontext_server_runtime_scopes{state="cached"} 3`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("metrics do not contain %q:\n%s", want, text)
		}
	}
}

func TestServerHandlerIncludesPrivateProcessAndGoRuntimeCollectors(t *testing.T) {
	t.Parallel()
	server, err := New()
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status = %d: %s", response.Code, response.Body.String())
	}
	exposition := response.Body.String()
	for _, name := range []string{
		"process_cpu_seconds_total",
		"process_resident_memory_bytes",
		"go_goroutines",
	} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + ` [0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`).MatchString(exposition) {
			t.Fatalf("metrics do not contain an unlabelled %s sample:\n%s", name, exposition)
		}
	}
}
