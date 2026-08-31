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

package locomo

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ob-labs/powercontext-go/inference"
)

func TestRetryTransientCancelsBeforeSecondAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	firstAttempt := make(chan struct{})
	result := make(chan error, 1)
	var calls atomic.Int64
	go func() {
		_, _, err := retryTransient(ctx, 2, func(context.Context) (struct{}, error) {
			if calls.Add(1) == 1 {
				close(firstAttempt)
			}
			return struct{}{}, inference.NewUnavailableError("retry-test")
		})
		result <- err
	}()
	<-firstAttempt
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want cancellation", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("retry attempts = %d, want 1 after cancellation", calls.Load())
	}
}
