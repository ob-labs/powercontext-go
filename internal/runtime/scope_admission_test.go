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

package runtime

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/source"
)

func TestScopedOperationsRejectUnregisteredScopeBeforeCallback(t *testing.T) {
	t.Parallel()
	lifecycle, err := NewConfigured(RuntimeOptions{
		ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
			return scope.Descriptor{}, false, nil
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, operation := range []struct {
		name string
		run  func(context.Context, string, func(context.Context, string) error) error
	}{
		{name: "read", run: lifecycle.ScopedRead},
		{name: "write", run: lifecycle.ScopedWrite},
	} {
		t.Run(operation.name, func(t *testing.T) {
			called := false
			err := operation.run(t.Context(), "unregistered-scope", func(context.Context, string) error {
				called = true
				return nil
			})
			var missing *scope.NotFoundError
			if !errors.As(err, &missing) {
				t.Fatalf("error = %v, want Scope NotFoundError", err)
			}
			if called {
				t.Fatal("unregistered Scope reached the operation callback")
			}
			if strings.Contains(err.Error(), "unregistered-scope") {
				t.Fatalf("Scope error exposed an opaque identifier: %q", err)
			}
		})
	}
}

func TestSourceCaptureRejectsUnregisteredScopeBeforePersistenceBackend(t *testing.T) {
	t.Parallel()
	lifecycle, err := NewConfigured(RuntimeOptions{
		ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
			return scope.Descriptor{}, false, nil
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	persisted := false
	application, err := NewSourceApplication(lifecycle, sourceCaptureBackendFunc(func(context.Context, string, source.ContentCapture) (source.Ref, int64, error) {
		persisted = true
		return source.Ref{}, 0, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.CaptureContent(t.Context(), "missing", "source-id", "content", nil)
	var missing *scope.NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want Scope NotFoundError", err)
	}
	if persisted {
		t.Fatal("unregistered Scope reached the Source persistence backend")
	}
}

func TestConnectorLifecycleRejectsUnregisteredScopeBeforeCheckpointOrProvider(t *testing.T) {
	t.Parallel()
	lifecycle, err := NewConfigured(RuntimeOptions{
		ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
			return scope.Descriptor{}, false, nil
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &lifecycleCheckpointStore{}
	providerRan := false
	application, err := NewConnectorLifecycleApplication(lifecycle, &lifecycleIngestion{}, store)
	if err != nil {
		t.Fatal(err)
	}
	connector := &countedLifecycleConnector{
		name: "connector", version: "v1", definitions: []string{"remote.note"},
		run: func(context.Context, *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			providerRan = true
			return source.ConnectorRunCompletion{}, nil
		},
	}

	_, err = application.Run(t.Context(), connector, lifecycleBinding(t))
	var missing *scope.NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("error = %v, want Scope NotFoundError", err)
	}
	if connector.nameCalls != 0 || connector.versionCalls != 0 || connector.definitionCalls != 0 || connector.runCalls != 0 {
		t.Fatalf(
			"unregistered Scope reached Connector declarations: name=%d version=%d definitions=%d run=%d",
			connector.nameCalls,
			connector.versionCalls,
			connector.definitionCalls,
			connector.runCalls,
		)
	}
	if len(store.events) != 0 || store.saves != 0 || providerRan {
		t.Fatalf("unregistered Scope reached checkpoint/provider side effects: events=%v saves=%d provider=%t", store.events, store.saves, providerRan)
	}
	if strings.Contains(err.Error(), "scope") {
		t.Fatalf("Scope error exposed an opaque identifier: %q", err)
	}
}

type countedLifecycleConnector struct {
	name            string
	version         string
	definitions     []string
	run             func(context.Context, *source.ConnectorRunSession) (source.ConnectorRunCompletion, error)
	nameCalls       int
	versionCalls    int
	definitionCalls int
	runCalls        int
}

func (c *countedLifecycleConnector) Name() string {
	c.nameCalls++
	return c.name
}

func (c *countedLifecycleConnector) Version() string {
	c.versionCalls++
	return c.version
}

func (c *countedLifecycleConnector) SourceDefinitions() []string {
	c.definitionCalls++
	return c.definitions
}

func (c *countedLifecycleConnector) Run(
	ctx context.Context,
	session *source.ConnectorRunSession,
) (source.ConnectorRunCompletion, error) {
	c.runCalls++
	return c.run(ctx, session)
}

func TestConnectorLifecycleRunsProviderOutsideScopeLockAfterAdmission(t *testing.T) {
	t.Parallel()
	registered, err := scope.NewDescriptor("scope", "title", "summary", "", nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewConfigured(RuntimeOptions{
		ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
			return registered, true, nil
		}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &lifecycleCheckpointStore{value: source.NoConnectorCheckpoint()}
	application, err := NewConnectorLifecycleApplication(lifecycle, &lifecycleIngestion{}, store)
	if err != nil {
		t.Fatal(err)
	}
	connector := lifecycleConnector{
		name: "connector", version: "v1", definitions: []string{"remote.note"},
		run: func(context.Context, *source.ConnectorRunSession) (source.ConnectorRunCompletion, error) {
			lifecycle.scopes.mu.Lock()
			cached, active := lifecycle.scopes.countsLocked()
			lifecycle.scopes.mu.Unlock()
			if cached != 0 || active != 0 {
				t.Fatalf("provider ran while Scope lock was leased: cached=%d active=%d", cached, active)
			}
			return source.NewConnectorRunCompletion(source.ConnectorRunComplete, jsontext.Value("null"))
		},
	}
	if _, err := application.Run(t.Context(), connector, lifecycleBinding(t)); err != nil {
		t.Fatal(err)
	}
}

type sourceCaptureBackendFunc func(context.Context, string, source.ContentCapture) (source.Ref, int64, error)

func (f sourceCaptureBackendFunc) Capture(ctx context.Context, scopeID string, capture source.ContentCapture) (source.Ref, int64, error) {
	return f(ctx, scopeID, capture)
}
