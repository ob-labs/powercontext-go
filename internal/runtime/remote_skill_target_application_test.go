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

	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/scope"
)

// This catches a regression where secret material is issued before durable
// Scope admission has established the caller may operate on that Scope.
func TestRemoteSkillTargetCreateRejectsUnknownScopeBeforeStoreIDOrSecret(t *testing.T) {
	reads, creates, ids, secrets := 0, 0, 0, 0
	lifecycle, err := NewConfigured(RuntimeOptions{ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
		reads++
		return scope.Descriptor{}, false, nil
	})}, nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewRemoteSkillTargetApplication(lifecycle, &remoteSkillTargetStoreFake{
		create: func(context.Context, skill.RemoteTarget) (skill.RemoteTarget, error) {
			creates++
			return skill.RemoteTarget{}, nil
		},
	}, RemoteSkillTargetApplicationOptions{
		Clock:     func() time.Time { return time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC) },
		NewID:     func() string { ids++; return "codex-target" },
		NewSecret: func() string { secrets++; return "enrollment-code" },
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.Create(t.Context(), RemoteSkillTargetCreateInput{
		ScopeID: "missing", DisplayName: "Remote", AgentKind: skill.CodexAgent,
	})
	if _, ok := errors.AsType[*scope.NotFoundError](err); !ok {
		t.Fatalf("Create error = %T %v, want scope not found", err, err)
	}
	if reads != 1 || creates != 0 || ids != 0 || secrets != 0 {
		t.Fatalf("reads=%d creates=%d ids=%d secrets=%d, want 1,0,0,0", reads, creates, ids, secrets)
	}
}

func TestRemoteSkillTargetEnrollInvalidCodeReturnsOneRedactedRefusal(t *testing.T) {
	application, err := NewRemoteSkillTargetApplication(New(), &remoteSkillTargetStoreFake{
		find: func(context.Context, string) (skill.RemoteTarget, bool, error) {
			return skill.RemoteTarget{}, false, nil
		},
	}, RemoteSkillTargetApplicationOptions{
		Clock: func() time.Time { return time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC) },
		NewID: func() string { return "unused" },
		NewSecret: func() string {
			t.Fatal("invalid enrollment issued a credential")
			return "credential"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.Enroll(t.Context(), RemoteSkillTargetEnrollInput{EnrollmentCode: "invalid-code"})
	if _, ok := errors.AsType[*RemoteSkillTargetEnrollmentRefusedError](err); !ok {
		t.Fatalf("Enroll error = %T %v, want redacted enrollment refusal", err, err)
	}
	if err.Error() != "remote Skill target enrollment was refused" {
		t.Fatalf("Enroll error = %q, unexpectedly exposes enrollment state", err)
	}
}

// This catches the former redaction of a storage failure as if the enrollment
// code were absent. The SQL boundary has already removed storage diagnostics.
func TestRemoteSkillTargetEnrollReturnsLookupStorageError(t *testing.T) {
	lookupErr := errors.New("remote Skill target storage operation failed")
	application, err := NewRemoteSkillTargetApplication(New(), &remoteSkillTargetStoreFake{
		find: func(context.Context, string) (skill.RemoteTarget, bool, error) {
			return skill.RemoteTarget{}, false, lookupErr
		},
	}, RemoteSkillTargetApplicationOptions{
		Clock:     func() time.Time { return time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC) },
		NewID:     func() string { return "unused" },
		NewSecret: func() string { t.Fatal("storage failure issued a credential"); return "unused" },
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = application.Enroll(t.Context(), RemoteSkillTargetEnrollInput{EnrollmentCode: "private-enrollment-code"})
	if !errors.Is(err, lookupErr) {
		t.Fatalf("Enroll error = %T %v, want original lookup storage error", err, err)
	}
}

func TestRemoteSkillTargetScopedReadAndMutationsRejectUnknownScopeBeforeStore(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*RemoteSkillTargetApplication) error
	}{
		{
			name: "list",
			run: func(application *RemoteSkillTargetApplication) error {
				_, err := application.List(t.Context(), "missing")
				return err
			},
		},
		{
			name: "rename",
			run: func(application *RemoteSkillTargetApplication) error {
				_, err := application.Rename(t.Context(), RemoteSkillTargetRenameInput{ScopeID: "missing", TargetID: "target", DisplayName: "Remote"})
				return err
			},
		},
		{
			name: "revoke",
			run: func(application *RemoteSkillTargetApplication) error {
				_, err := application.Revoke(t.Context(), RemoteSkillTargetRevokeInput{ScopeID: "missing", TargetID: "target"})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			storeCalls := 0
			lifecycle, err := NewConfigured(RuntimeOptions{ScopeReader: ScopeReaderFunc(func(context.Context, string) (scope.Descriptor, bool, error) {
				return scope.Descriptor{}, false, nil
			})}, nil)
			if err != nil {
				t.Fatal(err)
			}
			application, err := NewRemoteSkillTargetApplication(lifecycle, &remoteSkillTargetStoreFake{
				list: func(context.Context, string) ([]skill.RemoteTarget, error) {
					storeCalls++
					return nil, nil
				},
			}, RemoteSkillTargetApplicationOptions{
				Clock:     func() time.Time { return time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC) },
				NewID:     func() string { return "unused" },
				NewSecret: func() string { return "unused" },
			})
			if err != nil {
				t.Fatal(err)
			}

			err = test.run(application)
			if _, ok := errors.AsType[*scope.NotFoundError](err); !ok {
				t.Fatalf("operation error = %T %v, want scope not found", err, err)
			}
			if storeCalls != 0 {
				t.Fatalf("store calls = %d, want 0", storeCalls)
			}
		})
	}
}

type remoteSkillTargetStoreFake struct {
	create  func(context.Context, skill.RemoteTarget) (skill.RemoteTarget, error)
	list    func(context.Context, string) ([]skill.RemoteTarget, error)
	find    func(context.Context, string) (skill.RemoteTarget, bool, error)
	consume func(context.Context, skill.RemoteTarget, string, time.Time) (skill.RemoteTarget, bool, error)
	replace func(context.Context, skill.RemoteTarget, int) (skill.RemoteTarget, error)
}

func (s *remoteSkillTargetStoreFake) Create(ctx context.Context, target skill.RemoteTarget) (skill.RemoteTarget, error) {
	return s.create(ctx, target)
}

func (s *remoteSkillTargetStoreFake) List(ctx context.Context, scopeID string) ([]skill.RemoteTarget, error) {
	return s.list(ctx, scopeID)
}

func (s *remoteSkillTargetStoreFake) FindByEnrollmentDigest(ctx context.Context, digest string) (skill.RemoteTarget, bool, error) {
	return s.find(ctx, digest)
}

func (s *remoteSkillTargetStoreFake) ConsumePending(ctx context.Context, target skill.RemoteTarget, digest string, now time.Time) (skill.RemoteTarget, bool, error) {
	return s.consume(ctx, target, digest, now)
}

func (s *remoteSkillTargetStoreFake) Replace(ctx context.Context, target skill.RemoteTarget, generation int) (skill.RemoteTarget, error) {
	return s.replace(ctx, target, generation)
}
