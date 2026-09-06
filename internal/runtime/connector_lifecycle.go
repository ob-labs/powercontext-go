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

	"github.com/ob-labs/powercontext-go/source"
)

// AcceptedObservationSubmitter is the narrow durable Source path a Connector
// session consumes. It only accepts observations already validated against the
// Connector's declared Definition.
type AcceptedObservationSubmitter interface {
	SubmitAccepted(context.Context, string, *source.AdmittedObservation) (SourceReceipt, error)
}

// ConnectorCheckpointStore owns short, optimistic durable checkpoint writes.
// Load preserves the distinction between an absent row and JSON null.
type ConnectorCheckpointStore interface {
	Load(context.Context, source.ConnectorBinding) (source.ConnectorCheckpoint, error)
	Save(context.Context, source.ConnectorBinding, source.ConnectorCheckpoint, source.ConnectorCheckpoint) error
}

// ConnectorLifecycleApplication runs external Connectors without holding a
// database transaction or a per-Scope Runtime lock across provider work. Each
// item submit independently takes the RemoteIngestion scoped durable path.
type ConnectorLifecycleApplication struct {
	runtime     *Runtime
	ingestion   AcceptedObservationSubmitter
	checkpoints ConnectorCheckpointStore
}

func NewConnectorLifecycleApplication(
	runtime *Runtime,
	ingestion AcceptedObservationSubmitter,
	checkpoints ConnectorCheckpointStore,
) (*ConnectorLifecycleApplication, error) {
	if runtime == nil || ingestion == nil || checkpoints == nil {
		return nil, errors.New("runtime: Connector lifecycle dependencies must not be nil")
	}
	return &ConnectorLifecycleApplication{
		runtime: runtime, ingestion: ingestion, checkpoints: checkpoints,
	}, nil
}

// Run executes provider work outside SQL and scope-lock ownership. Accepted
// Source writes happen before their accepted item outcomes, and a checkpoint
// advances only after a complete run with no rejected or failed item.
func (a *ConnectorLifecycleApplication) Run(
	ctx context.Context,
	connector source.Connector,
	binding source.ConnectorBinding,
) (result source.ConnectorRunResult, err error) {
	err = a.runtime.Operation(ctx, func(ctx context.Context) error {
		if bindingErr := binding.Validate(); bindingErr != nil {
			return bindingErr
		}
		if _, scopeErr := a.runtime.resolveScope(ctx, binding.ScopeID()); scopeErr != nil {
			return scopeErr
		}
		definitions, validationErr := source.ValidateConnector(connector, binding)
		if validationErr != nil {
			return validationErr
		}
		previous, loadErr := a.checkpoints.Load(ctx, binding)
		if loadErr != nil {
			return loadErr
		}
		session, sessionErr := source.NewConnectorRunSession(
			binding, previous, definitions, connectorIngestionSink{ingestion: a.ingestion},
		)
		if sessionErr != nil {
			return sessionErr
		}
		completion, runErr := connector.Run(ctx, session)
		drainErr := session.Finalize(ctx)
		if runErr != nil {
			return runErr
		}
		if drainErr != nil {
			return drainErr
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if submissionErr := session.Failure(); submissionErr != nil {
			return submissionErr
		}
		if completionErr := completion.Validate(); completionErr != nil {
			return completionErr
		}

		proposed, checkpointErr := source.NewConnectorCheckpoint(completion.Checkpoint())
		if checkpointErr != nil {
			return checkpointErr
		}
		items := session.Outcomes()
		unsafe := false
		for _, item := range items {
			if item.Status() == source.ConnectorSubmissionRejected || item.Status() == source.ConnectorSubmissionFailed {
				unsafe = true
				break
			}
		}
		status := completion.Status()
		committed := previous
		if unsafe {
			status = source.ConnectorRunIncomplete
		} else if status == source.ConnectorRunComplete {
			equal, compareErr := source.EqualConnectorCheckpoints(previous, proposed)
			if compareErr != nil {
				return compareErr
			}
			if !equal {
				if saveErr := a.checkpoints.Save(ctx, binding, proposed, previous); saveErr != nil {
					return saveErr
				}
				committed = proposed
			}
		}
		built, resultErr := source.NewConnectorRunResult(binding, status, previous, proposed, committed, items)
		if resultErr != nil {
			return resultErr
		}
		result = built
		return nil
	})
	return result, err
}

// connectorIngestionSink is the only bridge from Connector code to Runtime.
// It turns well-formed but inadmissible worker observations into visible
// rejections while preserving cancellation and infrastructure errors.
type connectorIngestionSink struct{ ingestion AcceptedObservationSubmitter }

func (s connectorIngestionSink) Submit(
	ctx context.Context,
	binding source.ConnectorBinding,
	_ string,
	definitionName string,
	observation *source.AdmittedObservation,
) (source.Ref, error) {
	if err := binding.Validate(); err != nil {
		return source.Ref{}, err
	}
	if validationErr := observation.Validate(); validationErr != nil {
		return source.Ref{}, validationErr
	}
	if observation.Observation().Ref().Type() != definitionName {
		return source.Ref{}, &source.InvalidConnectorRunError{
			Field: "source_ref", Detail: "must match the declared Source Definition",
		}
	}
	receipt, err := s.ingestion.SubmitAccepted(ctx, binding.ScopeID(), observation)
	if err != nil {
		if isConnectorSubmissionRejection(err) {
			rejection, rejectionErr := source.NewConnectorSubmissionRejectedError("durable Source acceptance rejected")
			if rejectionErr != nil {
				return source.Ref{}, rejectionErr
			}
			return source.Ref{}, rejection
		}
		return source.Ref{}, err
	}
	if receipt.Ref != observation.Observation().Ref() {
		return source.Ref{}, &source.InvalidConnectorRunError{
			Field: "source_ref", Detail: "durable acceptance returned a different Source reference",
		}
	}
	return receipt.Ref, nil
}

func isConnectorSubmissionRejection(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if _, ok := errors.AsType[*source.InvalidSourceObservationError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*source.DefinitionNotFoundError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*source.DefinitionConflictError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*source.ObservationConflictError](err); ok {
		return true
	}
	if _, ok := errors.AsType[*source.UnacceptedObservationError](err); ok {
		return true
	}
	return false
}
