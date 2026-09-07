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
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

type artifactReaderFunc func(context.Context, string, string, string, int64) (artifact.Snapshot, error)

func (f artifactReaderFunc) ReadArtifact(ctx context.Context, scopeID, family, id string, revision int64) (artifact.Snapshot, error) {
	return f(ctx, scopeID, family, id, revision)
}

func TestArtifactResourceAdmissionPrecedesPersistence(t *testing.T) {
	reader := artifactReaderFunc(func(context.Context, string, string, string, int64) (artifact.Snapshot, error) {
		t.Fatal("unadmitted read reached persistence")
		return nil, nil
	})
	lifecycle, err := NewConfigured(RuntimeOptions{ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) { return scope.Descriptor{}, false, nil })}, nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewArtifactResourceApplication(lifecycle, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, operationErr := application.GetArtifact(t.Context(), "missing", "experience", "read", 0); operationErr == nil {
		t.Fatal("unknown Scope accepted")
	} else if _, ok := errors.AsType[*scope.NotFoundError](operationErr); !ok {
		t.Fatalf("unknown Scope = %v", operationErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, operationErr := application.GetArtifact(ctx, "missing", "experience", "read", 1); !errors.Is(operationErr, context.Canceled) {
		t.Fatalf("canceled read = %v", operationErr)
	}
	if closeErr := lifecycle.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, operationErr := application.GetArtifact(t.Context(), "missing", "experience", "read", 0); operationErr == nil {
		t.Fatal("closed Runtime accepted read")
	}
}

func TestArtifactResourceCancellationDominatesCompletedReader(t *testing.T) {
	lifecycle, err := NewConfigured(RuntimeOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader := artifactReaderFunc(func(context.Context, string, string, string, int64) (artifact.Snapshot, error) {
		cancel()
		return nil, nil
	})
	application, err := NewArtifactResourceApplication(lifecycle, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, operationErr := application.GetArtifact(ctx, "scope", "experience", "read", 1); !errors.Is(operationErr, context.Canceled) {
		t.Fatalf("ignored cancellation = %v", operationErr)
	}
}
