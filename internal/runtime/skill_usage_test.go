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

	"github.com/ob-labs/powercontext-go/internal/scope"
	"github.com/ob-labs/powercontext-go/source"
)

type skillUsageBackendFunc func(context.Context, string, source.SkillUsageCapture) (source.Ref, int64, error)

func (function skillUsageBackendFunc) Record(
	ctx context.Context,
	scopeID string,
	capture source.SkillUsageCapture,
) (source.Ref, int64, error) {
	return function(ctx, scopeID, capture)
}

func TestSkillUsageAdmissionPrecedesSQLiteRecorder(t *testing.T) {
	called := false
	backend := skillUsageBackendFunc(func(context.Context, string, source.SkillUsageCapture) (source.Ref, int64, error) {
		called = true
		return source.Ref{}, 0, nil
	})
	lifecycle, err := NewConfigured(RuntimeOptions{ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
		return scope.Descriptor{}, false, nil
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewSkillUsageApplication(lifecycle, backend)
	if err != nil {
		t.Fatal(err)
	}
	skillRef, err := source.NewSkillUsageArtifactReference("skill", "package", 1)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := source.NewSkillUsageCapture(
		"usage", skillRef, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "codex-target", true,
		source.ObservedInvocationTrue, source.ObservedValidationPassed, source.ObservedOutcomeSuccess, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, recordErr := application.Record(t.Context(), "unknown-scope", capture); recordErr == nil {
		t.Fatal("unknown Scope accepted")
	} else if _, ok := errors.AsType[*scope.NotFoundError](recordErr); !ok {
		t.Fatalf("unknown Scope = %T %v", recordErr, recordErr)
	}
	if called {
		t.Fatal("unadmitted Skill usage reached the SQLite recorder")
	}
}
