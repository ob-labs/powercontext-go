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
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

func TestScopedWriteSerializesExactScopeAndRetainsBoundedGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runtime := New()
		entered := make(chan struct{})
		release := make(chan struct{})
		firstDone := make(chan error, 1)
		go func() {
			firstDone <- runtime.ScopedWrite(t.Context(), "scope-a", func(context.Context, string) error {
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered

		var secondEntered atomic.Bool
		secondDone := make(chan error, 1)
		go func() {
			secondDone <- runtime.ScopedWrite(t.Context(), "scope-a", func(context.Context, string) error {
				secondEntered.Store(true)
				return nil
			})
		}()
		// The second writer must be blocked on the exact scope gate before the
		// first writer is released; this is not a scheduler-yield guess.
		synctest.Wait()
		if secondEntered.Load() {
			close(release)
			if err := <-firstDone; err != nil {
				t.Fatal(err)
			}
			if err := <-secondDone; err != nil {
				t.Fatal(err)
			}
			t.Fatal("same-Scope writer overlapped")
		}
		close(release)
		if err := <-firstDone; err != nil {
			t.Fatal(err)
		}
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}

		runtime.scopes.mu.Lock()
		remaining := len(runtime.scopes.entries)
		runtime.scopes.mu.Unlock()
		if remaining != 1 {
			t.Fatalf("cached Scope gates = %d, want 1", remaining)
		}
	})
}

func TestScopedWritesForDifferentScopesCanOverlap(t *testing.T) {
	t.Parallel()
	runtime := New()
	entered := make(chan string, 2)
	release := make(chan struct{})
	var group sync.WaitGroup
	for _, scope := range []string{"scope-a", "scope-b"} {
		scope := scope
		group.Add(1)
		go func() {
			defer group.Done()
			if err := runtime.ScopedWrite(context.Background(), scope, func(context.Context, string) error {
				entered <- scope
				<-release
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	<-entered
	<-entered
	close(release)
	group.Wait()
}

func TestScopedReadsForSameScopeCanOverlap(t *testing.T) {
	t.Parallel()
	runtime := New()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			errors <- runtime.ScopedRead(context.Background(), "scope-a", func(context.Context, string) error {
				entered <- struct{}{}
				<-release
				return nil
			})
		}()
	}

	// Both callbacks must enter before either is released. A keyed read lock or
	// accidental reuse of the write gate would deadlock this assertion.
	<-entered
	<-entered
	close(release)
	for range 2 {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseRejectsNewWorkAndWaitsForAdmittedOperation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runtime := New()
		entered := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- runtime.Operation(t.Context(), func(context.Context) error {
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered
		closed := make(chan error, 1)
		go func() { closed <- runtime.Close(t.Context()) }()
		synctest.Wait()
		err := runtime.Operation(t.Context(), func(context.Context) error { return nil })
		var state *StateError
		if !errors.As(err, &state) {
			t.Fatalf("expected closed StateError, got %v", err)
		}
		select {
		case err := <-closed:
			t.Fatalf("Close returned before drain: %v", err)
		default:
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
	})
}

func TestCanceledCloseRestoresAdmissionAndCanceledWaiterIsReclaimed(t *testing.T) {
	t.Parallel()
	runtime := New()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runtime.ScopedWrite(context.Background(), "scope-a", func(context.Context, string) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	waiterContext, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	var canceledWaiterEntered atomic.Bool
	go func() {
		waiterDone <- runtime.ScopedWrite(waiterContext, "scope-a", func(context.Context, string) error {
			canceledWaiterEntered.Store(true)
			return nil
		})
	}()
	cancelWaiter()
	if err := <-waiterDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error = %v", err)
	}
	if canceledWaiterEntered.Load() {
		t.Fatal("canceled waiter entered")
	}

	closeContext, cancelClose := context.WithCancel(context.Background())
	cancelClose()
	if err := runtime.Close(closeContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("close error = %v", err)
	}
	if err := runtime.ScopedRead(context.Background(), "scope-b", func(context.Context, string) error { return nil }); err != nil {
		t.Fatalf("admission was not restored: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	runtime.scopes.mu.Lock()
	remaining := len(runtime.scopes.entries)
	runtime.scopes.mu.Unlock()
	if remaining != 2 {
		t.Fatalf("cached Scope gates = %d, want two used Scopes without an extra waiter entry", remaining)
	}
}

func TestScopeCacheNeverEvictsLockWithHolderOrWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var evicted []string
		runtime, err := NewConfigured(RuntimeOptions{
			ScopeCacheSize: 1,
			ScopeEvictor: func(scope string) {
				mu.Lock()
				evicted = append(evicted, scope)
				mu.Unlock()
			},
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		firstEntered := make(chan struct{})
		releaseFirst := make(chan struct{})
		firstDone := make(chan error, 1)
		go func() {
			firstDone <- runtime.ScopedWrite(t.Context(), "same", func(context.Context, string) error {
				close(firstEntered)
				<-releaseFirst
				return nil
			})
		}()
		<-firstEntered

		secondStarted := make(chan struct{})
		secondDone := make(chan error, 1)
		go func() {
			close(secondStarted)
			secondDone <- runtime.ScopedWrite(t.Context(), "same", func(context.Context, string) error { return nil })
		}()
		<-secondStarted
		synctest.Wait()
		runtime.scopes.mu.Lock()
		entry := runtime.scopes.entries["same"]
		waiterLeased := entry != nil && entry.leases == 2
		runtime.scopes.mu.Unlock()
		if !waiterLeased {
			t.Fatal("same-Scope waiter did not acquire a cache lease")
		}
		if err := runtime.ScopedWrite(t.Context(), "other", func(context.Context, string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		gotEvicted := append([]string(nil), evicted...)
		mu.Unlock()
		if len(gotEvicted) != 1 || gotEvicted[0] != "other" {
			t.Fatalf("evicted while same Scope active = %v, want [other]", gotEvicted)
		}

		close(releaseFirst)
		if err := <-firstDone; err != nil {
			t.Fatal(err)
		}
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
		if err := runtime.ScopedRead(t.Context(), "replacement", func(context.Context, string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		gotEvicted = append([]string(nil), evicted...)
		mu.Unlock()
		if len(gotEvicted) != 2 || gotEvicted[1] != "same" {
			t.Fatalf("final evictions = %v, want [other same]", gotEvicted)
		}
	})
}

func TestScopeCacheObserverReportsDistinctActiveAndCachedScopes(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var snapshots [][2]int
	runtime, err := NewConfigured(RuntimeOptions{
		ScopeCacheSize: 3,
		ScopeObserver: func(cached, active int) {
			mu.Lock()
			snapshots = append(snapshots, [2]int{cached, active})
			mu.Unlock()
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ScopedRead(t.Context(), "one", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ScopedRead(t.Context(), "two", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(snapshots) == 0 || snapshots[0] != [2]int{0, 0} || snapshots[len(snapshots)-1] != [2]int{0, 0} {
		t.Fatalf("Scope observations = %v", snapshots)
	}
	foundCachedTwo := false
	for _, snapshot := range snapshots {
		if snapshot == [2]int{2, 0} {
			foundCachedTwo = true
		}
	}
	if !foundCachedTwo {
		t.Fatalf("Scope observations never reported two cached Scopes: %v", snapshots)
	}
}

func TestValidateScopeMatchesFrozenRuntimeBoundary(t *testing.T) {
	t.Parallel()
	if got, err := ValidateScopeID(" scope "); err != nil || got != " scope " {
		t.Fatalf("opaque untrimmed Scope changed: %q %v", got, err)
	}
	if _, err := ValidateScopeID(" \t "); err == nil {
		t.Fatal("blank Scope accepted")
	}
}

func TestCloseOrdersSchedulerBeforeOwnedResourcesInReverseOrder(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var events []string
	record := func(value string) { mu.Lock(); events = append(events, value); mu.Unlock() }
	runtime := New(
		closeRecorder{close: func(context.Context) error { record("resource-1"); return nil }},
		closeRecorder{close: func(context.Context) error { record("resource-2"); return nil }},
	)
	scheduler := &schedulerRecorder{
		pause: func() { record("scheduler-pause") },
		close: func(context.Context) error { record("scheduler-close"); return nil },
	}
	if err := runtime.AttachScheduler(scheduler); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"scheduler-pause", "scheduler-close", "resource-2", "resource-1"}
	if len(events) != len(want) {
		t.Fatalf("close events = %v", events)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("close events = %v", events)
		}
	}
	if err := runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(events) != len(want) {
		t.Fatalf("idempotent Close repeated work: %v", events)
	}
}

type closeRecorder struct{ close func(context.Context) error }

func (r closeRecorder) Close(ctx context.Context) error { return r.close(ctx) }

type schedulerRecorder struct {
	pause func()
	close func(context.Context) error
}

func (r *schedulerRecorder) Pause()                          { r.pause() }
func (r *schedulerRecorder) Close(ctx context.Context) error { return r.close(ctx) }
