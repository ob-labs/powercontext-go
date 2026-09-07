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

	"github.com/ob-labs/powercontext-go/source"
)

func (b *RuntimeSourceBackend) CreateResource(ctx context.Context, scopeID string, value source.ContentSource) (source.ContentSource, int64, error) {
	var stored StoredSource
	err := b.database.Transaction(ctx, func(tx DBTX) error {
		var addErr error
		stored, addErr = b.repository.Add(ctx, tx, scopeID, value)
		return addErr
	})
	if err != nil {
		if _, ok := errors.AsType[*StoredPayloadConflictError](err); ok {
			return source.ContentSource{}, 0, &source.ResourceConflictError{}
		}
		return source.ContentSource{}, 0, err
	}
	content, ok := stored.Value.(source.ContentSource)
	if !ok {
		return source.ContentSource{}, 0, &source.ResourceNotFoundError{}
	}
	return content, stored.JournalPosition, nil
}

func (b *RuntimeSourceBackend) GetResource(ctx context.Context, scopeID string, ref source.Ref) (source.ContentSource, int64, error) {
	var stored StoredSource
	err := b.database.Transaction(ctx, func(tx DBTX) error {
		var getErr error
		stored, getErr = b.repository.Get(ctx, tx, scopeID, ref)
		return getErr
	})
	if err != nil {
		if _, ok := errors.AsType[*RepositoryNotFoundError](err); ok {
			return source.ContentSource{}, 0, &source.ResourceNotFoundError{}
		}
		return source.ContentSource{}, 0, err
	}
	content, ok := stored.Value.(source.ContentSource)
	if !ok {
		return source.ContentSource{}, 0, &source.ResourceNotFoundError{}
	}
	return content, stored.JournalPosition, nil
}
