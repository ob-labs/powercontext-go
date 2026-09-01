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
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ob-labs/powercontext-go/inference"
)

const (
	DefaultReadinessProbeTimeout = 2 * time.Second
	DefaultReadinessCacheTTL     = 5 * time.Minute
	TransientReadinessCacheTTL   = 30 * time.Second
)

type CheckStatus string

const (
	CheckReady         CheckStatus = "ready"
	CheckUnavailable   CheckStatus = "unavailable"
	CheckTimeout       CheckStatus = "timeout"
	CheckMisconfigured CheckStatus = "misconfigured"
)

func (s CheckStatus) valid() bool {
	return s == CheckReady || s == CheckUnavailable || s == CheckTimeout || s == CheckMisconfigured ||
		isProviderRejectedCheckStatus(s)
}

func providerRejectedCheckStatus(configuration *inference.ConfigurationError) (CheckStatus, bool) {
	if configuration == nil || configuration.Code() != "provider-rejected" {
		return "", false
	}
	detail := configuration.Detail()
	if len(detail) != len("HTTP 000") || !strings.HasPrefix(detail, "HTTP ") {
		return "", false
	}
	for _, digit := range detail[len("HTTP "):] {
		if digit < '0' || digit > '9' {
			return "", false
		}
	}
	status, err := strconv.Atoi(detail[len("HTTP "):])
	if err != nil || status < http.StatusBadRequest || status >= http.StatusInternalServerError ||
		status == http.StatusRequestTimeout || status == http.StatusConflict ||
		status == http.StatusTooEarly || status == http.StatusTooManyRequests {
		return "", false
	}
	return CheckStatus("misconfigured: provider-rejected (" + detail + ")"), true
}

func isProviderRejectedCheckStatus(status CheckStatus) bool {
	const prefix = "misconfigured: provider-rejected ("
	value := string(status)
	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, ")") {
		return false
	}
	detail := value[len(prefix) : len(value)-1]
	_, ok := providerRejectedCheckStatus(inference.NewConfigurationError("provider-rejected", detail))
	return ok
}

type ReadinessStatus string

const (
	Ready    ReadinessStatus = "ready"
	Degraded ReadinessStatus = "degraded"
	NotReady ReadinessStatus = "not_ready"
)

type Probe func(context.Context) (CheckStatus, error)

type ProbeDefinition struct {
	Name     string
	Probe    Probe
	Blocking bool
}

type Readiness struct {
	status ReadinessStatus
	checks map[string]CheckStatus
	order  []string
}

func (r Readiness) Status() ReadinessStatus { return r.status }
func (r Readiness) Ready() bool             { return r.status == Ready }

func (r Readiness) Checks() map[string]CheckStatus {
	result := make(map[string]CheckStatus, len(r.checks))
	for key, value := range r.checks {
		result[key] = value
	}
	return result
}

func (r Readiness) CheckOrder() []string { return slices.Clone(r.order) }

type ReadinessChecks struct {
	definitions []ProbeDefinition
}

func NewReadinessChecks(definitions []ProbeDefinition) (*ReadinessChecks, error) {
	seen := make(map[string]struct{}, len(definitions))
	cloned := slices.Clone(definitions)
	for _, definition := range cloned {
		if definition.Name == "" {
			return nil, errors.New("runtime: readiness check name must not be empty")
		}
		if definition.Probe == nil {
			return nil, fmt.Errorf("runtime: readiness check %q has no probe", definition.Name)
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return nil, fmt.Errorf("runtime: duplicate readiness check %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
	}
	return &ReadinessChecks{definitions: cloned}, nil
}

// Run evaluates independent dependencies concurrently. A dependency panic or
// ordinary error is isolated as unavailable; caller cancellation still aborts
// the aggregate operation.
func (c *ReadinessChecks) Run(ctx context.Context) (Readiness, error) {
	if c == nil {
		return Readiness{}, errors.New("runtime: readiness checks must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return Readiness{}, err
	}
	results := make([]CheckStatus, len(c.definitions))
	errorsByIndex := make([]error, len(c.definitions))
	var group sync.WaitGroup
	for index, definition := range c.definitions {
		index, definition := index, definition
		group.Go(func() {
			results[index], errorsByIndex[index] = runProbe(ctx, definition.Probe)
		})
	}
	group.Wait()
	if err := ctx.Err(); err != nil {
		return Readiness{}, err
	}

	checks := make(map[string]CheckStatus, len(c.definitions))
	order := make([]string, len(c.definitions))
	status := Ready
	for index, definition := range c.definitions {
		value := results[index]
		if errorsByIndex[index] != nil || !value.valid() {
			value = CheckUnavailable
		}
		checks[definition.Name] = value
		order[index] = definition.Name
		if value != CheckReady {
			if definition.Blocking {
				status = NotReady
			} else if status == Ready {
				status = Degraded
			}
		}
	}
	return Readiness{status: status, checks: checks, order: order}, nil
}

func runProbe(ctx context.Context, probe Probe) (status CheckStatus, err error) {
	defer func() {
		if recover() != nil {
			status, err = CheckUnavailable, nil
		}
	}()
	return probe(ctx)
}

type DependencyOperation func(context.Context) error

// DependencyProbe bounds one dependency call and maps only safe readiness
// categories; provider messages and credentials never escape into checks.
func DependencyProbe(operation DependencyOperation, timeout time.Duration) Probe {
	if timeout <= 0 {
		timeout = DefaultReadinessProbeTimeout
	}
	return func(ctx context.Context) (CheckStatus, error) {
		if operation == nil {
			return CheckUnavailable, nil
		}
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err := operation(probeCtx)
		if err == nil {
			return CheckReady, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if configuration, ok := errors.AsType[*inference.ConfigurationError](err); ok {
			if status, ok := providerRejectedCheckStatus(configuration); ok {
				return status, nil
			}
			return CheckMisconfigured, nil
		}
		var inferenceTimeout *inference.TimeoutError
		if errors.As(err, &inferenceTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return CheckTimeout, nil
		}
		return CheckUnavailable, nil
	}
}

type Clock func() time.Time

// CachedProbe caches stable and transient outcomes with separate TTLs and
// collapses concurrent refreshes without holding a mutex during I/O.
type CachedProbe struct {
	probe        Probe
	ttl          time.Duration
	transientTTL time.Duration
	clock        Clock

	mu         sync.Mutex
	hasResult  bool
	result     CheckStatus
	expiresAt  time.Time
	refreshing chan struct{}
}

func NewCachedProbe(probe Probe, ttl, transientTTL time.Duration, clock Clock) (*CachedProbe, error) {
	if probe == nil {
		return nil, errors.New("runtime: cached readiness probe must not be nil")
	}
	if ttl <= 0 {
		ttl = DefaultReadinessCacheTTL
	}
	if transientTTL <= 0 {
		transientTTL = TransientReadinessCacheTTL
	}
	if clock == nil {
		clock = time.Now
	}
	return &CachedProbe{probe: probe, ttl: ttl, transientTTL: transientTTL, clock: clock}, nil
}

func (p *CachedProbe) Probe(ctx context.Context) (CheckStatus, error) {
	if p == nil {
		return CheckUnavailable, errors.New("runtime: cached readiness probe must not be nil")
	}
	for {
		now := p.clock()
		p.mu.Lock()
		if p.hasResult && now.Before(p.expiresAt) {
			result := p.result
			p.mu.Unlock()
			return result, nil
		}
		if refreshing := p.refreshing; refreshing != nil {
			p.mu.Unlock()
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-refreshing:
				continue
			}
		}
		refreshing := make(chan struct{})
		p.refreshing = refreshing
		p.mu.Unlock()

		result, err := runProbe(ctx, p.probe)
		p.mu.Lock()
		if err == nil {
			ttl := p.ttl
			if result == CheckUnavailable || result == CheckTimeout {
				ttl = p.transientTTL
			}
			p.result = result
			p.hasResult = true
			p.expiresAt = p.clock().Add(ttl)
		}
		p.refreshing = nil
		close(refreshing)
		p.mu.Unlock()
		return result, err
	}
}
