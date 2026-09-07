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

package endpoint

import (
	"context"
	"net/url"

	canonicalsource "github.com/ob-labs/powercontext-go/api/canonical/sources"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/source"
)

// SourceResourceOperations is the Runtime-owned surface of the Source sidecar.
type SourceResourceOperations interface {
	CreateResource(context.Context, string, []byte) (runtime.SourceRecord, error)
	GetResource(context.Context, string, string, string) (runtime.SourceRecord, error)
}

type CanonicalSourceHandler struct{ operations SourceResourceOperations }

var _ canonicalsource.Handler = (*CanonicalSourceHandler)(nil)

func NewCanonicalSourceHandler(operations SourceResourceOperations) *CanonicalSourceHandler {
	return &CanonicalSourceHandler{operations: operations}
}

func (h *CanonicalSourceHandler) CreateSource(ctx context.Context, request *canonicalsource.CreateSourceRequest, params canonicalsource.CreateSourceParams) (canonicalsource.CreateSourceRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return nil, &InvalidRequestError{}
	}
	if sourceType, exists := request.SourceType.Get(); exists && sourceType != canonicalsource.CreateSourceRequestSourceTypeContent {
		return nil, &InvalidRequestError{}
	}
	result, err := h.operations.CreateResource(ctx, params.ScopeID, request.Content)
	if err != nil {
		return nil, err
	}
	record, err := canonicalSourceRecord(result)
	if err != nil {
		return nil, err
	}
	return &canonicalsource.SourceRecordHeaders{
		Response:               record,
		Location:               canonicalsource.NewOptString("/v1/scopes/" + url.PathEscape(result.ScopeID) + "/sources/content/" + url.PathEscape(result.Value.SourceName())),
		XPowerContextRequestID: canonicalSourceRequestID(ctx),
	}, nil
}

func (h *CanonicalSourceHandler) GetSource(ctx context.Context, params canonicalsource.GetSourceParams) (canonicalsource.GetSourceRes, error) {
	if h == nil || h.operations == nil {
		return nil, &RuntimeNotReadyError{}
	}
	result, err := h.operations.GetResource(ctx, params.ScopeID, string(params.SourceType), params.SourceID)
	if err != nil {
		return nil, err
	}
	record, err := canonicalSourceRecord(result)
	if err != nil {
		return nil, err
	}
	return &canonicalsource.GetSourceOKHeaders{Response: record, XPowerContextRequestID: canonicalSourceRequestID(ctx)}, nil
}

func canonicalSourceRecord(result runtime.SourceRecord) (canonicalsource.SourceRecord, error) {
	content, err := result.Value.ContentJSON()
	if err != nil {
		return canonicalsource.SourceRecord{}, err
	}
	digest, err := result.Value.ContentDigest()
	if err != nil {
		return canonicalsource.SourceRecord{}, err
	}
	position := int(result.Position)
	if result.Position < 1 || int64(position) != result.Position {
		return canonicalsource.SourceRecord{}, &RuntimeNotReadyError{}
	}
	return canonicalsource.SourceRecord{
		ScopeID: result.ScopeID, SourceType: canonicalsource.SourceRecordSourceType(source.ContentType),
		SourceID: result.Value.SourceName(), Position: position, Content: []byte(content), ContentDigest: digest,
	}, nil
}

func canonicalSourceRequestID(ctx context.Context) canonicalsource.OptString {
	if id, exists := requestID(ctx).Get(); exists {
		return canonicalsource.NewOptString(id)
	}
	return canonicalsource.OptString{}
}
