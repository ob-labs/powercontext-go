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
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/source"
)

type artifactReaderFunc func(context.Context, string, string, string, int64) (artifact.Snapshot, error)

func (f artifactReaderFunc) ReadArtifact(ctx context.Context, scopeID, family, id string, revision int64) (artifact.Snapshot, error) {
	return f(ctx, scopeID, family, id, revision)
}

func (f artifactReaderFunc) ReadArtifactPage(ctx context.Context, scopeID, family, after string, _ int) ([]artifact.Snapshot, bool, error) {
	_, err := f(ctx, scopeID, family, after, 0)
	return nil, false, err
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
	application, err := NewArtifactResourceApplication(lifecycle, reader, [32]byte{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, operationErr := application.GetArtifact(t.Context(), "missing", "experience", "read", 0); operationErr == nil {
		t.Fatal("unknown Scope accepted")
	} else if _, ok := errors.AsType[*scope.NotFoundError](operationErr); !ok {
		t.Fatalf("unknown Scope = %v", operationErr)
	}
	if _, operationErr := application.ListArtifacts(t.Context(), "missing", "invalid-family", 0, new("invalid")); operationErr == nil {
		t.Fatal("unknown Scope list accepted")
	} else if _, ok := errors.AsType[*scope.NotFoundError](operationErr); !ok {
		t.Fatalf("list validation preceded Scope admission: %v", operationErr)
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
	application, err := NewArtifactResourceApplication(lifecycle, reader, [32]byte{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, operationErr := application.GetArtifact(ctx, "scope", "experience", "read", 1); !errors.Is(operationErr, context.Canceled) {
		t.Fatalf("ignored cancellation = %v", operationErr)
	}
}

type artifactPageReaderFunc func(context.Context, string, string, string, int) ([]artifact.Snapshot, bool, error)

func (f artifactPageReaderFunc) ReadArtifactPage(ctx context.Context, scopeID, family, after string, limit int) ([]artifact.Snapshot, bool, error) {
	return f(ctx, scopeID, family, after, limit)
}

func (artifactPageReaderFunc) ReadArtifact(context.Context, string, string, string, int64) (artifact.Snapshot, error) {
	panic("unexpected exact-revision read")
}

func TestArtifactListValidationAndCancellation(t *testing.T) {
	lifecycle, err := NewConfigured(RuntimeOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reader := artifactPageReaderFunc(func(context.Context, string, string, string, int) ([]artifact.Snapshot, bool, error) {
		t.Fatal("invalid list reached persistence")
		return nil, false, nil
	})
	a, err := NewArtifactResourceApplication(lifecycle, reader, [32]byte{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, listErr := a.ListArtifacts(t.Context(), "scope", "experience", limit, nil); listErr == nil {
			t.Fatalf("accepted limit %d", limit)
		}
	}
	if _, listErr := a.ListArtifacts(t.Context(), "scope", "unknown", 10, nil); listErr == nil {
		t.Fatal("accepted unsupported family")
	}
	_, cursorErr := a.ListArtifacts(t.Context(), "scope", "experience", 10, new(""))
	assertInvalidArtifactCursor(t, cursorErr)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, listErr := a.ListArtifacts(ctx, "scope", "experience", 10, nil); !errors.Is(listErr, context.Canceled) {
		t.Fatalf("canceled list = %v", listErr)
	}
	for _, returnedErr := range []error{nil, errors.New("storage diagnostic")} {
		ctx, cancel := context.WithCancel(t.Context())
		reader := artifactPageReaderFunc(func(context.Context, string, string, string, int) ([]artifact.Snapshot, bool, error) {
			cancel()
			return nil, false, returnedErr
		})
		a, err := NewArtifactResourceApplication(lifecycle, reader, [32]byte{1}, nil)
		if err != nil {
			t.Fatal(err)
		}
		page, listErr := a.ListArtifacts(ctx, "scope", "experience", 10, nil)
		cancel()
		if !errors.Is(listErr, context.Canceled) || len(page.Items) != 0 || page.NextCursor != nil {
			t.Fatalf("cancellation did not dominate reader completion: %+v %v", page, listErr)
		}
	}
}

func TestArtifactListContinuationPreservesRecordsAndAllowsNewLimit(t *testing.T) {
	lifecycle, err := NewConfigured(RuntimeOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	content, err := experience.NewContent("s", "a", "o", "l")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := artifact.NewRef("experience", "Z", 3)
	if err != nil {
		t.Fatal(err)
	}
	unknownSource, err := source.NewRef("future-source", "preserve-for-projection")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := experience.NewDraft(content, []source.Ref{unknownSource}, nil)
	if err != nil {
		t.Fatal(err)
	}
	value, err := artifact.New("Z", 3, draft)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	reader := artifactPageReaderFunc(func(_ context.Context, scopeID, family, after string, limit int) ([]artifact.Snapshot, bool, error) {
		calls++
		if scopeID != "scope" || family != "experience" {
			t.Fatalf("read binding = %q %q", scopeID, family)
		}
		if calls == 1 {
			if after != "" || limit != 1 {
				t.Fatalf("first read = %q %d", after, limit)
			}
			return []artifact.Snapshot{value}, true, nil
		}
		if after != "Z" || limit != 100 {
			t.Fatalf("continued read = %q %d", after, limit)
		}
		return nil, false, nil
	})
	a, err := NewArtifactResourceApplication(lifecycle, reader, [32]byte{1}, func() time.Time { return time.Unix(1000, 0) })
	if err != nil {
		t.Fatal(err)
	}
	page, err := a.ListArtifacts(t.Context(), "scope", "experience", 1, nil)
	if err != nil || len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatalf("first page = %+v %v", page, err)
	}
	if page.Items[0].ScopeID != "scope" || page.Items[0].Value.Ref() != ref || page.Items[0].Value.Lineage().Sources()[0] != unknownSource {
		t.Fatal("list lost durable revision or source lineage")
	}
	last, err := a.ListArtifacts(t.Context(), "scope", "experience", 100, page.NextCursor)
	if err != nil || last.Items == nil || len(last.Items) != 0 || last.NextCursor != nil {
		t.Fatalf("empty last page = %+v %v", last, err)
	}
}
