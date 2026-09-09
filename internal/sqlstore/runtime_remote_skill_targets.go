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

package sqlstore

import (
	"context"
	"errors"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/skill"
)

// RuntimeRemoteSkillTargetStore gives the runtime one short SQLite transaction
// per remote-target operation without coupling sqlstore to runtime.
type RuntimeRemoteSkillTargetStore struct {
	database   *Database
	repository RemoteSkillTargetRepository
}

func NewRuntimeRemoteSkillTargetStore(
	database *Database,
	repository RemoteSkillTargetRepository,
) (*RuntimeRemoteSkillTargetStore, error) {
	if database == nil {
		return nil, errors.New("sqlstore: remote Skill target database must not be nil")
	}
	return &RuntimeRemoteSkillTargetStore{database: database, repository: repository}, nil
}

func (s *RuntimeRemoteSkillTargetStore) Create(
	ctx context.Context,
	target skill.RemoteTarget,
) (result skill.RemoteTarget, returnErr error) {
	if s == nil || s.database == nil {
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	return result, s.database.Transaction(ctx, func(tx DBTX) error {
		result, returnErr = s.repository.Create(ctx, tx, target)
		return returnErr
	})
}

func (s *RuntimeRemoteSkillTargetStore) List(
	ctx context.Context,
	scopeID string,
) (result []skill.RemoteTarget, returnErr error) {
	if s == nil || s.database == nil {
		return nil, &RemoteSkillTargetStorageError{}
	}
	return result, s.database.Transaction(ctx, func(tx DBTX) error {
		result, returnErr = s.repository.List(ctx, tx, scopeID)
		return returnErr
	})
}

func (s *RuntimeRemoteSkillTargetStore) FindByEnrollmentDigest(
	ctx context.Context,
	digest string,
) (result skill.RemoteTarget, found bool, returnErr error) {
	if s == nil || s.database == nil {
		return skill.RemoteTarget{}, false, &RemoteSkillTargetStorageError{}
	}
	return result, found, s.database.Transaction(ctx, func(tx DBTX) error {
		result, found, returnErr = s.repository.FindByEnrollmentDigest(ctx, tx, digest)
		return returnErr
	})
}

func (s *RuntimeRemoteSkillTargetStore) ConsumePending(
	ctx context.Context,
	target skill.RemoteTarget,
	digest string,
	now time.Time,
) (result skill.RemoteTarget, consumed bool, returnErr error) {
	if s == nil || s.database == nil {
		return skill.RemoteTarget{}, false, &RemoteSkillTargetStorageError{}
	}
	return result, consumed, s.database.Transaction(ctx, func(tx DBTX) error {
		result, consumed, returnErr = s.repository.ConsumePending(ctx, tx, target, digest, now)
		return returnErr
	})
}

func (s *RuntimeRemoteSkillTargetStore) Replace(
	ctx context.Context,
	target skill.RemoteTarget,
	expectedGeneration int,
) (result skill.RemoteTarget, returnErr error) {
	if s == nil || s.database == nil {
		return skill.RemoteTarget{}, &RemoteSkillTargetStorageError{}
	}
	return result, s.database.Transaction(ctx, func(tx DBTX) error {
		result, returnErr = s.repository.Replace(ctx, tx, target, expectedGeneration)
		return returnErr
	})
}
