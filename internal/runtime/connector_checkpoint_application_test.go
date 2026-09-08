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
	"testing"

	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/source"
)

func TestConnectorCheckpointAdmissionPrecedesPersistenceAndLease(t *testing.T) {
	for _, operation := range []string{"get", "commit"} {
		for _, state := range []string{"unknown scope", "closed", "invalid binding", "canceled"} {
			t.Run(operation+"/"+state, func(t *testing.T) {
				reads, leases := 0, 0
				lifecycle, err := NewConfigured(RuntimeOptions{
					ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
						reads++
						return scope.Descriptor{}, false, nil
					}),
					ScopeObserver: func(_, active int) { leases += active },
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				store := &checkpointApplicationStore{
					load: func(context.Context, source.ConnectorBinding) (source.ConnectorCheckpoint, error) {
						t.Fatal("unadmitted operation reached checkpoint storage")
						return source.NoConnectorCheckpoint(), nil
					},
					save: func(context.Context, source.ConnectorBinding, source.ConnectorCheckpoint, source.ConnectorCheckpoint) error {
						t.Fatal("unadmitted operation wrote a checkpoint")
						return nil
					},
				}
				application, err := NewConnectorCheckpointApplication(lifecycle, store)
				if err != nil {
					t.Fatal(err)
				}
				binding := lifecycleBinding(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				switch state {
				case "closed":
					if closeErr := lifecycle.Close(t.Context()); closeErr != nil {
						t.Fatal(closeErr)
					}
				case "invalid binding":
					binding = source.ConnectorBinding{}
				case "canceled":
					cancel()
				}
				var result source.ConnectorCheckpoint
				if operation == "get" {
					result, err = application.Get(ctx, binding)
				} else {
					// Unknown Scope must dominate even malformed checkpoint JSON.
					result, err = application.Commit(ctx, binding, jsontext.Value("invalid"), jsontext.Value("invalid"))
				}
				if err == nil || result.Found() || leases != 0 {
					t.Fatalf("unadmitted result: found=%t err=%v leases=%d", result.Found(), err, leases)
				}
				if state == "unknown scope" {
					if _, ok := errors.AsType[*scope.NotFoundError](err); !ok || reads != 1 {
						t.Fatalf("unknown Scope error=%v reads=%d", err, reads)
					}
				} else if reads != 0 {
					t.Fatalf("%s reached ScopeReader %d times", state, reads)
				}
				if state == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled operation = %v", err)
				}
				if state == "closed" {
					if closed, ok := errors.AsType[*StateError](err); !ok || closed.Code != "closed" {
						t.Fatalf("closed Runtime error = %T %v", err, err)
					}
				}
				if state == "invalid binding" {
					if _, ok := errors.AsType[*source.InvalidConnectorBindingError](err); !ok {
						t.Fatalf("invalid binding error = %T %v", err, err)
					}
				}
			})
		}
	}
}

func TestConnectorCheckpointCommitValidatesJSONBeforeStorage(t *testing.T) {
	for _, test := range []struct{ name, next, expected string }{
		{name: "missing next", expected: "null"},
		{name: "missing expected", next: "null"},
		{name: "invalid next", next: "{", expected: "null"},
		{name: "invalid expected", next: "null", expected: "["},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &checkpointApplicationStore{load: func(context.Context, source.ConnectorBinding) (source.ConnectorCheckpoint, error) {
				t.Fatal("invalid checkpoint reached storage")
				return source.NoConnectorCheckpoint(), nil
			}}
			application, err := NewConnectorCheckpointApplication(New(), store)
			if err != nil {
				t.Fatal(err)
			}
			_, err = application.Commit(t.Context(), lifecycleBinding(t), jsontext.Value(test.next), jsontext.Value(test.expected))
			if _, ok := errors.AsType[*source.InvalidConnectorRunError](err); !ok {
				t.Fatalf("invalid JSON error = %T %v", err, err)
			}
		})
	}
}

func TestConnectorCheckpointCancellationDominatesStorageCompletion(t *testing.T) {
	for _, operation := range []string{"get", "commit load", "commit save"} {
		for _, test := range []struct {
			name string
			err  error
		}{
			{name: "success"},
			{name: "storage failure", err: errors.New("storage failure")},
			{name: "conflict", err: &ConnectorCheckpointConflictError{}},
		} {
			t.Run(operation+"/"+test.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				store := &checkpointApplicationStore{
					load: func(context.Context, source.ConnectorBinding) (source.ConnectorCheckpoint, error) {
						if operation != "commit save" {
							cancel()
							return source.NoConnectorCheckpoint(), test.err
						}
						return source.NoConnectorCheckpoint(), nil
					},
					save: func(context.Context, source.ConnectorBinding, source.ConnectorCheckpoint, source.ConnectorCheckpoint) error {
						if operation != "commit save" {
							t.Fatal("canceled load advanced the checkpoint")
						}
						cancel()
						return test.err
					},
				}
				application, err := NewConnectorCheckpointApplication(New(), store)
				if err != nil {
					t.Fatal(err)
				}
				var result source.ConnectorCheckpoint
				if operation == "get" {
					result, err = application.Get(ctx, lifecycleBinding(t))
				} else {
					result, err = application.Commit(ctx, lifecycleBinding(t), jsontext.Value("null"), jsontext.Value("null"))
				}
				if !errors.Is(err, context.Canceled) || result.Found() {
					t.Fatalf("canceled completion returned found=%t err=%v", result.Found(), err)
				}
			})
		}
	}
}

type checkpointApplicationStore struct {
	load func(context.Context, source.ConnectorBinding) (source.ConnectorCheckpoint, error)
	save func(context.Context, source.ConnectorBinding, source.ConnectorCheckpoint, source.ConnectorCheckpoint) error
}

func (s *checkpointApplicationStore) Load(ctx context.Context, binding source.ConnectorBinding) (source.ConnectorCheckpoint, error) {
	return s.load(ctx, binding)
}

func (s *checkpointApplicationStore) Save(ctx context.Context, binding source.ConnectorBinding, next, expected source.ConnectorCheckpoint) error {
	return s.save(ctx, binding, next, expected)
}
