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
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"net/url"

	"github.com/go-faster/jx"
	canonicalsource "github.com/ob-labs/powercontext-go/api/canonical/sources"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/source"
)

// SourceResourceOperations is the Runtime-owned surface of the Source sidecar.
type SourceResourceOperations interface {
	CreateResource(context.Context, string, []byte) (runtime.SourceRecord, error)
	GetResource(context.Context, string, string, string) (runtime.SourceRecord, error)
}

// SourceIngestionOperations admits remote Source Definitions and observations.
type SourceIngestionOperations interface {
	Register(context.Context, source.DefinitionManifest) (source.DefinitionManifest, error)
	Submit(context.Context, string, source.SourceObservation) (runtime.SourceReceipt, error)
}

// ConnectorCheckpointOperations owns Scope-admitted Connector checkpoints.
type ConnectorCheckpointOperations interface {
	Get(context.Context, source.ConnectorBinding) (source.ConnectorCheckpoint, error)
	Commit(context.Context, source.ConnectorBinding, jsontext.Value, jsontext.Value) (source.ConnectorCheckpoint, error)
}

type CanonicalSourceHandler struct {
	operations  SourceResourceOperations
	ingestion   SourceIngestionOperations
	checkpoints ConnectorCheckpointOperations
}

var _ canonicalsource.Handler = (*CanonicalSourceHandler)(nil)

func NewCanonicalSourceHandler(
	operations SourceResourceOperations,
	ingestion SourceIngestionOperations,
	checkpoints ConnectorCheckpointOperations,
) *CanonicalSourceHandler {
	return &CanonicalSourceHandler{operations: operations, ingestion: ingestion, checkpoints: checkpoints}
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

func (h *CanonicalSourceHandler) RegisterSourceDefinition(
	ctx context.Context,
	request *canonicalsource.RegisterSourceDefinitionRequest,
) (canonicalsource.RegisterSourceDefinitionRes, error) {
	if h == nil || h.ingestion == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return nil, &InvalidRequestError{}
	}
	manifest, err := canonicalSourceDefinition(request.Manifest)
	if err != nil {
		return nil, err
	}
	registered, err := h.ingestion.Register(ctx, manifest)
	if err != nil {
		return nil, err
	}
	response, err := canonicalSourceDefinitionResponse(registered)
	if err != nil {
		return nil, err
	}
	return &response, nil
}

func (h *CanonicalSourceHandler) SubmitSourceObservation(
	ctx context.Context,
	request *canonicalsource.SubmitSourceObservationRequest,
) (canonicalsource.SubmitSourceObservationRes, error) {
	if h == nil || h.ingestion == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return nil, &InvalidRequestError{}
	}
	observation, err := canonicalSourceObservation(request.Observation)
	if err != nil {
		return nil, err
	}
	receipt, err := h.ingestion.Submit(ctx, request.ScopeID, observation)
	if err != nil {
		return nil, err
	}
	position := int(receipt.Sequence)
	if receipt.Sequence < 1 || int64(position) != receipt.Sequence {
		return nil, &RuntimeNotReadyError{}
	}
	return &canonicalsource.SourceObservationReceipt{
		Source: canonicalsource.SourceReference{Name: receipt.Ref.Type(), SourceID: receipt.Ref.ID()}, Position: position,
	}, nil
}

func (h *CanonicalSourceHandler) GetConnectorCheckpoint(
	ctx context.Context,
	request *canonicalsource.GetConnectorCheckpointRequest,
) (canonicalsource.GetConnectorCheckpointRes, error) {
	if h == nil || h.checkpoints == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return nil, &InvalidRequestError{}
	}
	binding, err := canonicalConnectorBinding(request.Binding)
	if err != nil {
		return nil, err
	}
	checkpoint, err := h.checkpoints.Get(ctx, binding)
	if err != nil {
		return nil, err
	}
	return canonicalConnectorCheckpointState(request.Binding, checkpoint), nil
}

func (h *CanonicalSourceHandler) CommitConnectorCheckpoint(
	ctx context.Context,
	request *canonicalsource.CommitConnectorCheckpointRequest,
) (canonicalsource.CommitConnectorCheckpointRes, error) {
	if h == nil || h.checkpoints == nil {
		return nil, &RuntimeNotReadyError{}
	}
	if request == nil {
		return nil, &InvalidRequestError{}
	}
	binding, err := canonicalConnectorBinding(request.Binding)
	if err != nil {
		return nil, err
	}
	checkpoint, err := h.checkpoints.Commit(
		ctx, binding, jsontext.Value(bytes.Clone(request.Checkpoint)), jsontext.Value(bytes.Clone(request.Expected)),
	)
	if err != nil {
		return nil, err
	}
	return canonicalConnectorCheckpointState(request.Binding, checkpoint), nil
}

func canonicalSourceDefinition(value canonicalsource.SourceDefinitionManifest) (source.DefinitionManifest, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return source.DefinitionManifest{}, &source.InvalidDefinitionManifestError{Field: "manifest", Detail: "must be valid JSON"}
	}
	return source.ParseDefinitionManifest(payload)
}

func canonicalSourceDefinitionResponse(value source.DefinitionManifest) (canonicalsource.SourceDefinitionManifest, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return canonicalsource.SourceDefinitionManifest{}, err
	}
	var response canonicalsource.SourceDefinitionManifest
	if err := json.Unmarshal(payload, &response); err != nil {
		return canonicalsource.SourceDefinitionManifest{}, err
	}
	return response, nil
}

func canonicalSourceObservation(value canonicalsource.SourceObservation) (source.SourceObservation, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return source.SourceObservation{}, &source.InvalidSourceObservationError{Field: "observation", Detail: "must be valid JSON"}
	}
	return source.ParseSourceObservation(payload)
}

func canonicalConnectorBinding(value canonicalsource.ConnectorBinding) (source.ConnectorBinding, error) {
	return source.NewConnectorBinding(value.ScopeID, value.BindingID, value.ConnectorName, value.ConnectorVersion)
}

func canonicalConnectorCheckpointState(
	binding canonicalsource.ConnectorBinding,
	checkpoint source.ConnectorCheckpoint,
) *canonicalsource.ConnectorCheckpointState {
	value, found := checkpoint.Value()
	if !found {
		value = jsontext.Value("null")
	}
	return &canonicalsource.ConnectorCheckpointState{
		Binding: binding, Checkpoint: jx.Raw(bytes.Clone(value)),
	}
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
