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

//go:build local_embeddings && cgo && (ORT || ALL)

package modelprovider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/ob-labs/powercontext-go/inference"
)

func TestLocalEmbeddingTransportPreservesInputsAndVectors(t *testing.T) {
	transport := newLocalEmbeddingTransport(func(inputs []string) ([][]float32, error) {
		if !slices.Equal(inputs, []string{"alpha", "beta"}) {
			t.Fatalf("inputs = %v", inputs)
		}
		inputs[0] = "mutated"
		return [][]float32{{1.25, -2.5}, {3, 4}}, nil
	}, func() error { return nil })

	inputs := []string{"alpha", "beta"}
	result, err := transport.Embed(t.Context(), embeddingRequestForProviderTest(t, inputs, inference.EmbeddingDocument, 3))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(inputs, []string{"alpha", "beta"}) ||
		!slices.Equal(result.Inputs(), []string{"alpha", "beta"}) ||
		!slices.Equal(result.Embeddings()[0], []float64{1.25, -2.5}) ||
		result.InputType() != inference.EmbeddingDocument {
		t.Fatalf("result = inputs %v, vectors %v, type %q", result.Inputs(), result.Embeddings(), result.InputType())
	}
	if result.Usage().Requests != 0 || result.Usage().InputTokens != nil || result.Usage().OutputTokens != nil {
		t.Fatalf("usage = %#v", result.Usage())
	}
}

func TestSentenceTransformersFactoryIsLazy(t *testing.T) {
	factory, err := NewFactory(MilestoneB, testEnvironment{}.lookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := factory.EmbeddingTransport("sentence-transformers:owner/model-that-is-not-downloaded")
	if err != nil {
		t.Fatalf("factory performed eager model I/O: %v", err)
	}
	resource, ok := transport.(interface{ Close(context.Context) error })
	if !ok {
		t.Fatal("local transport does not expose owned lifecycle")
	}
	if err := resource.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalEmbeddingTransportCancellationAndCloseDrainNativeCall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		var destroyed atomic.Int32
		transport := newLocalEmbeddingTransport(func([]string) ([][]float32, error) {
			close(started)
			<-release
			return [][]float32{{1}}, nil
		}, func() error {
			destroyed.Add(1)
			return nil
		})

		callContext, cancelCall := context.WithCancel(t.Context())
		callDone := make(chan error, 1)
		go func() {
			_, err := transport.Embed(callContext, embeddingRequestForProviderTest(t, []string{"alpha"}, inference.EmbeddingDocument, 3))
			callDone <- err
		}()
		<-started
		cancelCall()
		if err := <-callDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("embed error = %v", err)
		}

		closeDone := make(chan error, 1)
		go func() { closeDone <- transport.Close(t.Context()) }()
		synctest.Wait()

		lateContext, cancelLate := context.WithCancel(t.Context())
		defer cancelLate()
		lateDone := make(chan error, 1)
		go func() {
			_, err := transport.Embed(lateContext, embeddingRequestForProviderTest(t, []string{"late"}, inference.EmbeddingDocument, 3))
			lateDone <- err
		}()
		synctest.Wait()
		select {
		case err := <-lateDone:
			var unavailable *inference.UnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("late embed error = %v", err)
			}
		default:
			cancelLate()
			close(release)
			if err := <-lateDone; !errors.Is(err, context.Canceled) {
				t.Fatalf("late admitted embed error = %v", err)
			}
			if err := <-closeDone; err != nil {
				t.Fatal(err)
			}
			t.Fatal("late embed was admitted while Close was draining")
		}
		select {
		case err := <-closeDone:
			t.Fatalf("close returned before native call drained: %v", err)
		default:
		}
		close(release)
		if err := <-closeDone; err != nil {
			t.Fatal(err)
		}
		if destroyed.Load() != 1 {
			t.Fatalf("destroy calls = %d", destroyed.Load())
		}
		if err := transport.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if destroyed.Load() != 1 {
			t.Fatalf("idempotent destroy calls = %d", destroyed.Load())
		}
	})
}

func TestLocalEmbeddingTransportSerializesPipeline(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{}, 2)
	var active atomic.Int32
	var maximum atomic.Int32
	transport := newLocalEmbeddingTransport(func([]string) ([][]float32, error) {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return [][]float32{{1}}, nil
	}, func() error { return nil })

	done := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := transport.Embed(t.Context(), embeddingRequestForProviderTest(t, []string{"text"}, inference.EmbeddingDocument, 3))
			done <- err
		}()
	}
	<-started
	select {
	case <-started:
		t.Fatal("second pipeline invocation overlapped the first")
	default:
	}
	release <- struct{}{}
	<-started
	release <- struct{}{}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent pipeline calls = %d", maximum.Load())
	}
	if err := transport.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalEmbeddingTransportContainsNativeFailures(t *testing.T) {
	for name, run := range map[string]localEmbeddingRun{
		"error": func([]string) ([][]float32, error) { return nil, errors.New("secret model path") },
		"panic": func([]string) ([][]float32, error) { panic("secret model contents") },
	} {
		t.Run(name, func(t *testing.T) {
			transport := newLocalEmbeddingTransport(run, func() error { return nil })
			_, err := transport.Embed(t.Context(), embeddingRequestForProviderTest(t, []string{"secret input"}, inference.EmbeddingDocument, 3))
			var unavailable *inference.UnavailableError
			if !errors.As(err, &unavailable) || err.Error() != "inference is temporarily unavailable for embed" {
				t.Fatalf("error = %v", err)
			}
			if err := transport.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLocalEmbeddingTransportPreservesStableInferenceFailures(t *testing.T) {
	configuration := inference.NewConfigurationError("embedding-model", "model download failed")
	transport := newLocalEmbeddingTransport(
		func([]string) ([][]float32, error) { return nil, configuration },
		func() error { return nil },
	)
	_, err := transport.Embed(t.Context(), embeddingRequestForProviderTest(t, []string{"text"}, inference.EmbeddingDocument, 3))
	var observed *inference.ConfigurationError
	if !errors.As(err, &observed) || observed != configuration {
		t.Fatalf("stable inference error was reclassified: %v", err)
	}
	if err := transport.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalEmbeddingTransportContainsDestroyPanic(t *testing.T) {
	transport := newLocalEmbeddingTransport(
		func([]string) ([][]float32, error) { return [][]float32{{1}}, nil },
		func() error { panic("secret native state") },
	)
	err := transport.Close(t.Context())
	if err == nil || err.Error() != "local embedding runtime could not be closed" {
		t.Fatalf("close error = %v", err)
	}
	if err := transport.Close(t.Context()); err != nil {
		t.Fatalf("second close = %v", err)
	}
}

func TestLocalEmbeddingModelResolutionUsesCompleteCache(t *testing.T) {
	cache := t.TempDir()
	modelPath := filepath.Join(cache, "sentence-transformers_all-MiniLM-L6-v2")
	if err := os.Mkdir(modelPath, 0o750); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"tokenizer.json": "{}", "model.onnx": "fixture"} {
		if err := os.WriteFile(filepath.Join(modelPath, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lookup := testEnvironment{localEmbeddingCacheEnvironment: cache}.lookup
	resolved, filename, err := resolveLocalEmbeddingModel("sentence-transformers/all-MiniLM-L6-v2", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != modelPath || filename != "" {
		t.Fatalf("resolved = %q, filename = %q", resolved, filename)
	}
}

func TestHuggingFaceModelNameValidation(t *testing.T) {
	for _, valid := range []string{"all-MiniLM-L6-v2", "sentence-transformers/all-MiniLM-L6-v2", "owner/model:revision"} {
		if !validHuggingFaceModelName(valid) {
			t.Errorf("valid model rejected: %q", valid)
		}
	}
	for _, invalid := range []string{
		"", "/absolute", "../escape", "owner/../escape", "a/b/c", "owner/model name",
		"owner/model:", "owner/model:../escape", "owner/model:feature/branch", "owner/model:one:two",
	} {
		if validHuggingFaceModelName(invalid) {
			t.Errorf("invalid model accepted: %q", invalid)
		}
	}
}
