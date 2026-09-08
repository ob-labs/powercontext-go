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
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

type skillPackageReaderFunc func(context.Context, string, artifact.Ref) (skill.PackageSnapshot, error)

func (f skillPackageReaderFunc) ReadSkillPackage(ctx context.Context, scopeID string, ref artifact.Ref) (skill.PackageSnapshot, error) {
	return f(ctx, scopeID, ref)
}

func TestSkillPackageResourceAdmissionPrecedesReader(t *testing.T) {
	reader := skillPackageReaderFunc(func(context.Context, string, artifact.Ref) (skill.PackageSnapshot, error) {
		t.Fatal("unadmitted read reached persistence")
		return skill.PackageSnapshot{}, nil
	})
	lifecycle, err := NewConfigured(RuntimeOptions{ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
		return scope.Descriptor{}, false, nil
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewSkillPackageResourceApplication(lifecycle, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, readErr := application.ReadSkillPackage(t.Context(), "missing", artifact.Ref{}); readErr == nil {
		t.Fatal("unknown Scope accepted")
	} else if _, ok := errors.AsType[*scope.NotFoundError](readErr); !ok {
		t.Fatalf("Scope admission did not precede ref validation: %v", readErr)
	}
	if _, readErr := application.ReadSkillPackage(t.Context(), "", artifact.Ref{}); readErr == nil {
		t.Fatal("empty Scope accepted")
	} else if _, ok := errors.AsType[*InvalidScopeError](readErr); !ok {
		t.Fatalf("invalid Scope = %v", readErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, readErr := application.ReadSkillPackage(ctx, "missing", artifact.Ref{}); !errors.Is(readErr, context.Canceled) {
		t.Fatalf("canceled read = %v", readErr)
	}
	if closeErr := lifecycle.Close(t.Context()); closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, readErr := application.ReadSkillPackage(t.Context(), "missing", artifact.Ref{}); readErr == nil {
		t.Fatal("closed Runtime accepted read")
	} else if state, ok := errors.AsType[*StateError](readErr); !ok || state.Code != "closed" {
		t.Fatalf("closed Runtime = %v", readErr)
	}
}

func TestSkillPackageResourceRequiresExactRefAndPreservesFamily(t *testing.T) {
	lifecycle, err := NewConfigured(RuntimeOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	readerFailure := errors.New("reader failure")
	var want artifact.Ref
	reader := skillPackageReaderFunc(func(_ context.Context, scopeID string, ref artifact.Ref) (skill.PackageSnapshot, error) {
		if want.IsZero() {
			t.Fatal("invalid exact ref reached persistence")
		}
		if scopeID != "admitted" || ref != want {
			t.Fatalf("reader binding = %q %v, want admitted %v", scopeID, ref, want)
		}
		return skill.PackageSnapshot{}, readerFailure
	})
	application, err := NewSkillPackageResourceApplication(lifecycle, reader)
	if err != nil {
		t.Fatal(err)
	}
	if _, readErr := application.ReadSkillPackage(t.Context(), "admitted", artifact.Ref{}); readErr == nil {
		t.Fatal("zero reference selected a current revision")
	} else if _, ok := errors.AsType[*artifact.InvalidReferenceError](readErr); !ok {
		t.Fatalf("invalid exact ref = %v", readErr)
	}
	for _, family := range []string{"skill", "experience", "future.family"} {
		want, err = artifact.NewRef(family, "exact-id", 3)
		if err != nil {
			t.Fatal(err)
		}
		if _, readErr := application.ReadSkillPackage(t.Context(), "admitted", want); !errors.Is(readErr, readerFailure) {
			t.Fatalf("valid family %q did not reach reader: %v", family, readErr)
		}
	}
}

func TestSkillPackageResourceCancellationDominatesReaderCompletion(t *testing.T) {
	for _, readerFailure := range []error{nil, errors.New("reader failure")} {
		lifecycle, err := NewConfigured(RuntimeOptions{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		reader := skillPackageReaderFunc(func(context.Context, string, artifact.Ref) (skill.PackageSnapshot, error) {
			cancel()
			return skill.PackageSnapshot{}, readerFailure
		})
		application, err := NewSkillPackageResourceApplication(lifecycle, reader)
		if err != nil {
			t.Fatal(err)
		}
		ref, err := artifact.NewRef("skill", "exact-id", 1)
		if err != nil {
			t.Fatal(err)
		}
		result, readErr := application.ReadSkillPackage(ctx, "admitted", ref)
		cancel()
		if !errors.Is(readErr, context.Canceled) || len(result.Archive()) != 0 {
			t.Fatalf("cancellation did not dominate reader completion: %v", readErr)
		}
	}
}
