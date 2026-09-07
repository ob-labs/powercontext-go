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
	"encoding/hex"
	"errors"
	"uuid"

	"github.com/ob-labs/powercontext-go/source"
)

type SourceResourceBackend interface {
	CreateResource(context.Context, string, source.ContentSource) (source.ContentSource, int64, error)
	GetResource(context.Context, string, source.Ref) (source.ContentSource, int64, error)
}

// SourceRecord binds one immutable Source to its admitted Scope and journal.
type SourceRecord struct {
	ScopeID  string
	Value    source.ContentSource
	Position int64
}

func (a *SourceApplication) CreateResource(ctx context.Context, scopeID string, content []byte) (SourceRecord, error) {
	backend, ok := a.backend.(SourceResourceBackend)
	if !ok {
		return SourceRecord{}, errors.New("runtime: Source resources are unavailable")
	}
	var result SourceRecord
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		identity := uuid.NewV4()
		value, err := source.NewContentResource("src_"+hex.EncodeToString(identity[:]), content)
		if err != nil {
			return err
		}
		stored, position, createErr := backend.CreateResource(ctx, scope, value)
		if createErr != nil {
			return createErr
		}
		result = SourceRecord{ScopeID: scope, Value: stored, Position: position}
		return nil
	})
	return result, err
}

func (a *SourceApplication) GetResource(ctx context.Context, scopeID, sourceType, sourceID string) (SourceRecord, error) {
	backend, ok := a.backend.(SourceResourceBackend)
	if !ok {
		return SourceRecord{}, errors.New("runtime: Source resources are unavailable")
	}
	var result SourceRecord
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		if sourceType != source.ContentType {
			return &source.InvalidReferenceError{Field: "source_type", Detail: "must be content"}
		}
		ref, err := source.NewRef(sourceType, sourceID)
		if err != nil {
			return err
		}
		value, position, getErr := backend.GetResource(ctx, scope, ref)
		if getErr != nil {
			return getErr
		}
		result = SourceRecord{ScopeID: scope, Value: value, Position: position}
		return nil
	})
	return result, err
}
