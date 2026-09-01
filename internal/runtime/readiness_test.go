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
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/inference"
)

func TestReadinessChecksAggregateAndIsolate(t *testing.T) {
	t.Parallel()
	checks, err := NewReadinessChecks([]ProbeDefinition{
		{Name: "database", Blocking: true, Probe: fixedProbe(CheckReady)},
		{Name: "inference.embedding", Probe: fixedProbe(CheckMisconfigured)},
		{Name: "panics", Probe: func(context.Context) (CheckStatus, error) { panic("secret") }},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := checks.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status() != Degraded {
		t.Fatalf("status = %q, want degraded", result.Status())
	}
	want := map[string]CheckStatus{
		"database": CheckReady, "inference.embedding": CheckMisconfigured, "panics": CheckUnavailable,
	}
	if got := result.Checks(); !maps.Equal(got, want) {
		t.Fatalf("checks = %#v, want %#v", got, want)
	}

	blocking, err := NewReadinessChecks([]ProbeDefinition{{
		Name: "database", Blocking: true, Probe: fixedProbe(CheckUnavailable),
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err = blocking.Run(context.Background())
	if err != nil || result.Status() != NotReady {
		t.Fatalf("blocking result = %#v, %v", result, err)
	}
}

func TestReadinessChecksRunConcurrently(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	probe := func(ctx context.Context) (CheckStatus, error) {
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-release:
			return CheckReady, nil
		}
	}
	checks, err := NewReadinessChecks([]ProbeDefinition{{Name: "a", Probe: probe}, {Name: "b", Probe: probe}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := checks.Run(context.Background())
		done <- runErr
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("probes did not start concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDependencyProbeClassification(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		want CheckStatus
	}{
		{name: "ready", want: CheckReady},
		{name: "configuration", err: inference.NewConfigurationError("model", "secret"), want: CheckMisconfigured},
		{name: "timeout", err: inference.NewTimeoutError("embedding", time.Second), want: CheckTimeout},
		{name: "unavailable", err: errors.New("secret provider response"), want: CheckUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			probe := DependencyProbe(func(context.Context) error { return test.err }, time.Second)
			got, err := probe(context.Background())
			if err != nil || got != test.want {
				t.Fatalf("probe = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestDependencyProbePublishesOnlyAllowlistedProviderRejectionReason(t *testing.T) {
	t.Parallel()
	safe := inference.WrapConfigurationError(
		"provider-rejected", "HTTP 400", errors.New("API_KEY=secret https://credential@example.invalid private Memory"),
	)
	status, err := DependencyProbe(func(context.Context) error { return safe }, time.Second)(t.Context())
	if err != nil || status != "misconfigured: provider-rejected (HTTP 400)" {
		t.Fatalf("status = %q, err = %v", status, err)
	}

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "arbitrary detail", err: inference.NewConfigurationError("provider-rejected", "credential rejected")},
		{name: "transient status", err: inference.NewConfigurationError("provider-rejected", "HTTP 429")},
		{name: "lowercase status", err: inference.NewConfigurationError("provider-rejected", "http 400")},
		{name: "malformed status", err: inference.NewConfigurationError("provider-rejected", "HTTP four")},
		{name: "trailing text", err: inference.NewConfigurationError("provider-rejected", "HTTP 400 retry later")},
		{name: "plain configuration", err: inference.NewConfigurationError("model", "HTTP 400")},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, err := DependencyProbe(func(context.Context) error { return test.err }, time.Second)(t.Context())
			if err != nil || status != CheckMisconfigured {
				t.Fatalf("status = %q, err = %v; want %q", status, err, CheckMisconfigured)
			}
		})
	}
}

func TestCheckStatusValidProviderRejectionGrammar(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status CheckStatus
		want   bool
	}{
		{name: "allowlisted", status: "misconfigured: provider-rejected (HTTP 400)", want: true},
		{name: "wrong prefix", status: "misconfigured: provider response (HTTP 400)"},
		{name: "lowercase detail", status: "misconfigured: provider-rejected (http 400)"},
		{name: "trailing content", status: "misconfigured: provider-rejected (HTTP 400) retry"},
		{name: "transient status", status: "misconfigured: provider-rejected (HTTP 429)"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.status.valid(); got != test.want {
				t.Fatalf("valid(%q) = %t, want %t", test.status, got, test.want)
			}
		})
	}
}

func TestCachedProbeCollapsesRefreshAndUsesTransientTTL(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(duration time.Duration) {
		clockMu.Lock()
		now = now.Add(duration)
		clockMu.Unlock()
	}

	var calls atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{})
	cached, err := NewCachedProbe(func(context.Context) (CheckStatus, error) {
		call := calls.Add(1)
		if call == 1 {
			close(started)
			<-release
			return CheckUnavailable, nil
		}
		return CheckReady, nil
	}, time.Hour, time.Minute, clock)
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	results := make(chan CheckStatus, callers)
	for range callers {
		go func() {
			value, _ := cached.Probe(context.Background())
			results <- value
		}()
	}
	<-started
	close(release)
	for range callers {
		if got := <-results; got != CheckUnavailable {
			t.Fatalf("cached result = %q", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}

	advance(59 * time.Second)
	if got, _ := cached.Probe(context.Background()); got != CheckUnavailable || calls.Load() != 1 {
		t.Fatalf("fresh transient cache = %q, calls=%d", got, calls.Load())
	}
	advance(2 * time.Second)
	if got, _ := cached.Probe(context.Background()); got != CheckReady || calls.Load() != 2 {
		t.Fatalf("refreshed cache = %q, calls=%d", got, calls.Load())
	}
}

func TestReadinessCancellationPropagates(t *testing.T) {
	t.Parallel()
	checks, err := NewReadinessChecks([]ProbeDefinition{{
		Name: "blocked",
		Probe: func(ctx context.Context) (CheckStatus, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := checks.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want cancellation", err)
	}
}

func fixedProbe(status CheckStatus) Probe {
	return func(context.Context) (CheckStatus, error) { return status, nil }
}
