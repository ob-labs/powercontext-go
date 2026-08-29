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

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/internal/stats"
	"github.com/ob-labs/powercontext-go/source"
	"github.com/ob-labs/powercontext-go/trigger"
)

const DefaultHandoffArtifactID = "handoff"

type HandoffServiceFactory func(string) (*handoff.Service, error)

// HandoffActivationBackend brackets generation with short transactional
// boundary reads and cursor CAS writes.
type HandoffActivationBackend interface {
	LoadBoundary(context.Context, string, source.Ref, string) (int64, source.Cursor, *int64, error)
	SaveBoundary(context.Context, string, string, source.Cursor, *int64) error
}

type HandoffActivationStatus = handoff.ActivationStatus

const (
	HandoffGenerated HandoffActivationStatus = handoff.ActivationGenerated
	HandoffIgnored   HandoffActivationStatus = handoff.ActivationIgnored
)

type HandoffActivationResult = handoff.Activation

type HandoffApplication struct {
	runtime    *Runtime
	services   HandoffServiceFactory
	activation HandoffActivationBackend
}

func NewHandoffApplication(
	runtime *Runtime,
	services HandoffServiceFactory,
	activation HandoffActivationBackend,
) (*HandoffApplication, error) {
	if runtime == nil || services == nil || activation == nil {
		return nil, errors.New("runtime: Handoff application dependencies must not be nil")
	}
	return &HandoffApplication{runtime: runtime, services: services, activation: activation}, nil
}

func (a *HandoffApplication) Activate(
	ctx context.Context,
	scopeID string,
	activation handoff.Activate,
) (HandoffActivationResult, error) {
	var result HandoffActivationResult
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		ctx = a.runtime.withModelUsage(ctx, scope, stats.HandoffGeneration, "")
		position, state, generation, err := a.activation.LoadBoundary(
			ctx, scope, activation.BoundarySource(), trigger.HandoffBoundaryName,
		)
		if err != nil {
			return err
		}
		boundary, err := trigger.NewHandoffBoundary(position, activation)
		if err != nil {
			return err
		}
		transition := (trigger.HandoffBoundaryPolicy{}).Activate(boundary, state)
		actions := transition.Actions()
		if len(actions) == 0 {
			result, err = handoff.NewActivation(
				handoff.ActivationIgnored,
				activation.BoundarySource(),
				state.Sequence(),
				state.Sequence(),
				nil,
			)
			return err
		}
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		draft, err := service.Prepare(ctx, actions[0])
		if err != nil {
			return err
		}
		if saveErr := a.activation.SaveBoundary(
			ctx, scope, trigger.HandoffBoundaryName, transition.State(), generation,
		); saveErr != nil {
			return saveErr
		}
		result, err = handoff.NewActivation(
			handoff.ActivationGenerated,
			activation.BoundarySource(),
			state.Sequence(),
			transition.State().Sequence(),
			&draft,
		)
		return err
	})
	return result, err
}

func (a *HandoffApplication) Prepare(
	ctx context.Context,
	scopeID string,
	action handoff.Prepare,
) (handoff.Draft, error) {
	var result handoff.Draft
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		ctx = a.runtime.withModelUsage(ctx, scope, stats.HandoffGeneration, "")
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.Prepare(ctx, action)
		return err
	})
	return result, err
}

func (a *HandoffApplication) Finalize(
	ctx context.Context,
	scopeID string,
	draft handoff.Draft,
) (handoff.Prepared, error) {
	var result handoff.Prepared
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.Finalize(ctx, draft)
		return err
	})
	return result, err
}

func (a *HandoffApplication) Commit(
	ctx context.Context,
	scopeID string,
	prepared handoff.Prepared,
) (handoff.Handoff, error) {
	var result handoff.Handoff
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.Commit(ctx, prepared)
		return err
	})
	return result, err
}

func (a *HandoffApplication) ContinuePrepared(
	ctx context.Context,
	scopeID string,
	prepared handoff.Prepared,
) (handoff.Resolution, error) {
	return a.continueRead(ctx, scopeID, func(ctx context.Context, service *handoff.Service) (handoff.Resolution, error) {
		return service.ContinueFromPrepared(ctx, prepared)
	})
}

func (a *HandoffApplication) ContinueRevision(
	ctx context.Context,
	scopeID string,
	ref artifact.Ref,
) (handoff.Resolution, error) {
	return a.continueRead(ctx, scopeID, func(ctx context.Context, service *handoff.Service) (handoff.Resolution, error) {
		return service.ContinueFromRevision(ctx, ref)
	})
}

func (a *HandoffApplication) ContinueLatest(
	ctx context.Context,
	scopeID string,
) (handoff.Resolution, error) {
	return a.continueRead(ctx, scopeID, func(ctx context.Context, service *handoff.Service) (handoff.Resolution, error) {
		return service.ContinueLatest(ctx)
	})
}

func (a *HandoffApplication) continueRead(
	ctx context.Context,
	scopeID string,
	operation func(context.Context, *handoff.Service) (handoff.Resolution, error),
) (handoff.Resolution, error) {
	var result handoff.Resolution
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = operation(ctx, service)
		return err
	})
	return result, err
}

func (a *HandoffApplication) service(scope string) (*handoff.Service, error) {
	service, err := a.services(scope)
	if err != nil {
		return nil, err
	}
	if service == nil {
		return nil, &StateError{Code: "handoff"}
	}
	return service, nil
}
