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
	json "encoding/json/v2"
	"errors"

	"github.com/ob-labs/powercontext-go/source"
)

const (
	maxDefinitionManifestBytes = 64 * 1024
	maxSourceObservationBytes  = 4 * 1024 * 1024
)

// RemoteIngestionBackend is the transaction-owning persistence contract for
// declarative worker Source ingestion. Implementations must not hold a SQL
// transaction while RemoteIngestionApplication validates a manifest or value.
type RemoteIngestionBackend interface {
	Register(context.Context, source.DefinitionManifest) (source.DefinitionManifest, error)
	Find(context.Context, source.DefinitionIdentity) (source.DefinitionManifest, bool, error)
	Add(context.Context, string, *source.AdmittedObservation) (source.Ref, int64, error)
	HasNativeDefinition(string) bool
}

// RemoteIngestionApplication validates remote Source declarations and
// observations before delegating their short durable writes to a backend.
type RemoteIngestionApplication struct {
	runtime *Runtime
	backend RemoteIngestionBackend
}

// NewRemoteIngestionApplication binds remote Source ingestion to Runtime
// lifecycle admission and per-Scope serialization.
func NewRemoteIngestionApplication(
	runtime *Runtime,
	backend RemoteIngestionBackend,
) (*RemoteIngestionApplication, error) {
	if runtime == nil || backend == nil {
		return nil, errors.New("runtime: Remote ingestion dependencies must not be nil")
	}
	return &RemoteIngestionApplication{runtime: runtime, backend: backend}, nil
}

// Register validates one immutable worker Definition before beginning its
// backend-owned transaction.
func (a *RemoteIngestionApplication) Register(
	ctx context.Context,
	manifest source.DefinitionManifest,
) (result source.DefinitionManifest, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		if validationErr := validateRemoteDefinition(manifest, a.backend); validationErr != nil {
			return validationErr
		}
		var registerErr error
		result, registerErr = a.backend.Register(ctx, manifest)
		return registerErr
	})
	return result, err
}

// Submit validates a worker observation against its durable Definition before
// asking the backend to append it in a short transaction.
func (a *RemoteIngestionApplication) Submit(
	ctx context.Context,
	scopeID string,
	observation source.SourceObservation,
) (result SourceReceipt, err error) {
	err = a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		if validationErr := validateRemoteObservationEnvelope(observation); validationErr != nil {
			return validationErr
		}
		identity, identityErr := source.NewDefinitionIdentity(observation.Ref().Type(), observation.DefinitionVersion())
		if identityErr != nil {
			return sourceObservationError("definition", "must identify a valid Source Definition")
		}
		manifest, found, findErr := a.backend.Find(ctx, identity)
		if findErr != nil {
			return findErr
		}
		if !found {
			return &source.DefinitionNotFoundError{}
		}
		if a.backend.HasNativeDefinition(observation.Ref().Type()) {
			return &source.DefinitionConflictError{}
		}
		accepted, acceptErr := source.AdmitObservation(manifest, observation)
		if acceptErr != nil {
			return acceptErr
		}
		ref, sequence, addErr := a.backend.Add(ctx, scope, accepted)
		if addErr != nil {
			return addErr
		}
		result = SourceReceipt{Ref: ref, Sequence: sequence}
		return nil
	})
	return result, err
}

// SubmitAccepted persists an observation already admitted by the Connector
// boundary. The persistence adapter repeats durable Definition admission in
// its own short transaction before writing the evidence marker. This method
// checks durable Definition existence and native ownership, but does not
// repeat schema admission while a Connector is running; that happens in the
// persistence transaction before the immutable AdmittedObservation is stored.
func (a *RemoteIngestionApplication) SubmitAccepted(
	ctx context.Context,
	scopeID string,
	accepted *source.AdmittedObservation,
) (result SourceReceipt, err error) {
	err = a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		if validationErr := accepted.Validate(); validationErr != nil {
			return validationErr
		}
		if envelopeErr := validateRemoteObservationEnvelope(accepted.Observation()); envelopeErr != nil {
			return envelopeErr
		}
		observation := accepted.Observation()
		identity, identityErr := source.NewDefinitionIdentity(observation.Ref().Type(), observation.DefinitionVersion())
		if identityErr != nil {
			return sourceObservationError("definition", "must identify a valid Source Definition")
		}
		_, found, findErr := a.backend.Find(ctx, identity)
		if findErr != nil {
			return findErr
		}
		if !found {
			return &source.DefinitionNotFoundError{}
		}
		if a.backend.HasNativeDefinition(observation.Ref().Type()) {
			return &source.DefinitionConflictError{}
		}
		ref, sequence, addErr := a.backend.Add(ctx, scope, accepted)
		if addErr != nil {
			return addErr
		}
		result = SourceReceipt{Ref: ref, Sequence: sequence}
		return nil
	})
	return result, err
}

func validateRemoteDefinition(manifest source.DefinitionManifest, backend RemoteIngestionBackend) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return definitionManifestError("manifest", "must be serializable")
	}
	if len(payload) > maxDefinitionManifestBytes {
		return definitionManifestError("manifest", "must not exceed 64 KiB")
	}
	if backend.HasNativeDefinition(manifest.Name()) {
		return definitionManifestError("name", "must not replace an active Source Definition")
	}
	if err := source.ValidateDefinitionManifestForAdmission(manifest); err != nil {
		return err
	}
	return nil
}

func validateRemoteObservationEnvelope(observation source.SourceObservation) error {
	if err := observation.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(observation)
	if err != nil {
		return sourceObservationError("value", "must be serializable")
	}
	if len(payload) > maxSourceObservationBytes {
		return sourceObservationError("size", "must not exceed 4 MiB")
	}
	return nil
}

func sourceObservationError(field, detail string) *source.InvalidSourceObservationError {
	return &source.InvalidSourceObservationError{Field: field, Detail: detail}
}

func definitionManifestError(field, detail string) *source.InvalidDefinitionManifestError {
	return &source.InvalidDefinitionManifestError{Field: field, Detail: detail}
}
