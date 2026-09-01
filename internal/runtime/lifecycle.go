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
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

const MaxScopeIDLength = 256

type StateError struct{ Code string }

func (e *StateError) Error() string {
	switch e.Code {
	case "closed":
		return "Built-in Runtime is closed"
	case "scheduler":
		return "Built-in Runtime scheduler is already started"
	case "experience-incubation":
		return "Experience incubation is not configured"
	}
	return "Built-in Runtime is unavailable: " + e.Code
}

type InvalidScopeError struct{ Detail string }

func (e *InvalidScopeError) Error() string { return "invalid scope_id: " + e.Detail }

// ValidateScopeID validates the opaque runtime partition. Like the frozen
// Python runtime it accepts nonblank leading/trailing whitespace; individual
// persistence adapters may impose their stricter storage identity contract.
func ValidateScopeID(scopeID string) (string, error) {
	if !utf8.ValidString(scopeID) || strings.TrimSpace(scopeID) == "" {
		return "", &InvalidScopeError{Detail: "must not be empty"}
	}
	if utf8.RuneCountInString(scopeID) > MaxScopeIDLength {
		return "", &InvalidScopeError{Detail: fmt.Sprintf("must not exceed %d characters", MaxScopeIDLength)}
	}
	return scopeID, nil
}

// Runtime owns operation admission and per-scope write serialization. Domain
// services remain lifecycle-free and are invoked inside these boundaries by
// endpoint-facing application methods.
type Runtime struct {
	closeMu sync.Mutex
	stateMu sync.Mutex
	closing bool
	closed  bool
	active  int
	drained chan struct{}

	scopes     *scopeCache
	background semaphore
	tracing    StageTracing

	scheduler  SchedulerLifecycle
	resources  []Resource
	modelUsage ModelUsageRecorder
}

// Resource is an explicitly owned Runtime dependency closed after scheduled
// work and admitted operations have drained.
type Resource interface {
	Close(context.Context) error
}

// SchedulerLifecycle is the consumer-shaped shutdown surface required by the
// Runtime. Pause prevents new scheduled dispatch; Close drains its loop.
type SchedulerLifecycle interface {
	Pause()
	Close(context.Context) error
}

func New(resources ...Resource) *Runtime {
	return NewWithModelUsageRecorder(nil, resources...)
}

// NewWithModelUsageRecorder constructs a Runtime whose admitted operations can
// attribute successful inference calls. The recorder is deliberately
// best-effort and does not participate in Runtime resource ownership.
func NewWithModelUsageRecorder(recorder ModelUsageRecorder, resources ...Resource) *Runtime {
	runtime, err := NewConfigured(RuntimeOptions{}, recorder, resources...)
	if err != nil {
		panic(err)
	}
	return runtime
}

// NewConfigured constructs a Runtime with bounded Scope retention and optional
// low-cardinality observation. A zero ScopeCacheSize selects the current
// compatibility default.
func NewConfigured(
	options RuntimeOptions,
	recorder ModelUsageRecorder,
	resources ...Resource,
) (*Runtime, error) {
	capacity := options.ScopeCacheSize
	if capacity == 0 {
		capacity = DefaultScopeCacheSize
	}
	if capacity < 1 {
		return nil, errors.New("runtime: scope_cache_size must be positive")
	}
	return &Runtime{
		background: newSemaphore(),
		scopes:     newScopeCache(capacity, options.ScopeEvictor, options.ScopeObserver),
		tracing:    options.Tracing,
		resources:  append([]Resource(nil), resources...),
		modelUsage: recorder,
	}, nil
}

// AttachScheduler transfers lifecycle ownership after a scheduler has been
// opened with handlers that dispatch through this Runtime.
func (r *Runtime) AttachScheduler(scheduler SchedulerLifecycle) error {
	if scheduler == nil {
		return errors.New("runtime: scheduler must not be nil")
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.closing || r.closed {
		return &StateError{Code: "closed"}
	}
	if r.scheduler != nil {
		return &StateError{Code: "scheduler"}
	}
	r.scheduler = scheduler
	return nil
}

// Operation admits one unscoped read/use case and tracks it through close.
func (r *Runtime) Operation(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return errors.New("runtime: operation callback must not be nil")
	}
	release, err := r.admit(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn(ctx)
}

// ScopedRead admits one scoped operation without serializing it against other
// reads. It is still drained during shutdown.
func (r *Runtime) ScopedRead(ctx context.Context, scopeID string, fn func(context.Context, string) error) error {
	if fn == nil {
		return errors.New("runtime: scoped read callback must not be nil")
	}
	scope, err := ValidateScopeID(scopeID)
	if err != nil {
		return err
	}
	return r.Operation(ctx, func(ctx context.Context) error {
		_, release := r.scopes.lease(scope)
		defer release()
		if err := r.resolveScope(ctx); err != nil {
			return err
		}
		return fn(ctx, scope)
	})
}

// ScopedWrite admits one operation then acquires the reference-counted lock
// for its exact Scope. Waiting writers are active operations, so Close waits
// until each either runs or observes cancellation.
func (r *Runtime) ScopedWrite(ctx context.Context, scopeID string, fn func(context.Context, string) error) error {
	if fn == nil {
		return errors.New("runtime: scoped write callback must not be nil")
	}
	scope, err := ValidateScopeID(scopeID)
	if err != nil {
		return err
	}
	return r.Operation(ctx, func(ctx context.Context) error {
		lease, releaseLease := r.scopes.lease(scope)
		defer releaseLease()
		if err := r.resolveScope(ctx); err != nil {
			return err
		}
		var release func()
		err := r.runStage(ctx, "scope.lock", map[string]TraceAttribute{
			"powercontext.scope.lock.contended": lease.contended(),
		}, func(stageContext context.Context, _ StageSpan) error {
			var acquireErr error
			release, acquireErr = lease.acquire(stageContext)
			return acquireErr
		})
		if err != nil {
			return err
		}
		defer release()
		return fn(ctx, scope)
	})
}

// Background serializes scheduled processors across all Scopes while keeping
// their execution visible to lifecycle admission.
func (r *Runtime) Background(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return errors.New("runtime: background callback must not be nil")
	}
	return r.Operation(ctx, func(ctx context.Context) error {
		release, err := r.background.acquire(ctx)
		if err != nil {
			return err
		}
		defer release()
		return fn(ctx)
	})
}

// BackgroundOperation serializes one named background operation and lets the
// owning transport trace it as a root. The callback returns its observable
// outcome separately because scheduled scope failures may be isolated rather
// than returned as the dispatch error.
func (r *Runtime) BackgroundOperation(
	ctx context.Context,
	name string,
	fn func(context.Context) (string, error),
) (err error) {
	if name == "" {
		return errors.New("runtime: background operation name must not be empty")
	}
	if fn == nil {
		return errors.New("runtime: background operation callback must not be nil")
	}
	backgroundCtx, span := safelyStartBackground(ctx, r.tracing, name, nil)
	outcome := "failure"
	defer func() { safelyFinishStage(span, outcome, err) }()
	err = r.Background(backgroundCtx, func(operationCtx context.Context) error {
		outcome, err = fn(operationCtx)
		return err
	})
	if outcome == "" {
		outcome = "failure"
		if err == nil {
			outcome = "success"
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || context.Cause(backgroundCtx) != nil {
		outcome = "cancelled"
	}
	return err
}

// Close atomically rejects new operations and waits for every admitted read,
// writer, lock waiter, and background processor. If ctx is canceled before the
// drain completes, admission is restored so the owner can retry shutdown.
func (r *Runtime) Close(ctx context.Context) error {
	r.closeMu.Lock()
	defer r.closeMu.Unlock()

	r.stateMu.Lock()
	if r.closed {
		r.stateMu.Unlock()
		return nil
	}
	if !r.closing {
		r.closing = true
		r.drained = make(chan struct{})
		if r.active == 0 {
			close(r.drained)
		}
	}
	drained := r.drained
	r.stateMu.Unlock()

	select {
	case <-drained:
	case <-ctx.Done():
		r.stateMu.Lock()
		r.closing = false
		r.drained = nil
		r.stateMu.Unlock()
		return context.Cause(ctx)
	}

	// No accepted operation remains. Future scheduler dispatch attempts are
	// already rejected by closing=true; pause it before closing dependencies.
	if r.scheduler != nil {
		r.scheduler.Pause()
		if err := r.scheduler.Close(ctx); err != nil {
			return err
		}
	}
	if err := r.scopes.clear(); err != nil {
		return err
	}
	for index := len(r.resources) - 1; index >= 0; index-- {
		if r.resources[index] == nil {
			continue
		}
		if err := r.resources[index].Close(ctx); err != nil {
			return err
		}
	}
	r.stateMu.Lock()
	r.closed = true
	r.closing = false
	r.drained = nil
	r.stateMu.Unlock()
	return nil
}

func (r *Runtime) admit(ctx context.Context) (func(), error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	r.stateMu.Lock()
	if r.closing || r.closed {
		r.stateMu.Unlock()
		return nil, &StateError{Code: "closed"}
	}
	r.active++
	r.stateMu.Unlock()
	var once sync.Once
	return func() { once.Do(r.release) }, nil
}

func (r *Runtime) release() {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	r.active--
	if r.active == 0 && r.closing && r.drained != nil {
		close(r.drained)
	}
}

type semaphore struct{ token chan struct{} }

func newSemaphore() semaphore {
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return semaphore{token: token}
}

func (s *semaphore) acquire(ctx context.Context) (func(), error) {
	select {
	case <-s.token:
		var once sync.Once
		return func() { once.Do(func() { s.token <- struct{}{} }) }, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}
