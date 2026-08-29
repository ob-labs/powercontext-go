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
	"strings"
	"sync"

	"github.com/knights-analytics/hugot"
	"github.com/knights-analytics/hugot/options"

	"github.com/ob-labs/powercontext-go/inference"
)

const (
	localEmbeddingCacheEnvironment = "POWERCONTEXT_LOCAL_EMBEDDINGS_CACHE"
	localEmbeddingONNXEnvironment  = "POWERCONTEXT_LOCAL_EMBEDDINGS_ONNX_FILE"
	localEmbeddingORTEnvironment   = "POWERCONTEXT_ONNXRUNTIME_LIBRARY_DIR"
	localEmbeddingPipelineName     = "powercontext-sentence-transformers"
)

type localEmbeddingRun func([]string) ([][]float32, error)

type lazyLocalEmbeddingEngine struct {
	model  string
	lookup EnvLookup

	mu      sync.Mutex
	run     localEmbeddingRun
	destroy func() error
}

// sentenceTransformersTransport owns the process-global ONNX Runtime session.
// Hugot v0.7.0 has no context-aware feature-extraction API, so each invocation
// is tracked until the native call really returns even when its caller stops
// waiting. Calls are serialized because Hugot's tokenizer/pipeline is mutable.
type sentenceTransformersTransport struct {
	run     localEmbeddingRun
	destroy func() error
	gate    chan struct{}

	stateMu sync.Mutex
	closeMu sync.Mutex
	active  sync.WaitGroup
	closing bool
	closed  bool
}

type localEmbeddingCall struct {
	vectors [][]float32
	err     error
}

func newSentenceTransformersTransport(route Route, lookup EnvLookup) (inference.EmbeddingTransport, error) {
	engine := &lazyLocalEmbeddingEngine{model: route.model, lookup: lookup}
	return newLocalEmbeddingTransport(
		engine.embed,
		engine.close,
	), nil
}

func (e *lazyLocalEmbeddingEngine) embed(inputs []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.run == nil {
		if err := e.initialize(); err != nil {
			return nil, err
		}
	}
	return e.run(inputs)
}

// initialize mirrors Pydantic AI's lazy SentenceTransformer construction.
// The caller runs this method inside sentenceTransformersTransport's tracked
// worker, so a timed-out request can return while shutdown still accounts for
// the native/download work that has not actually stopped yet.
func (e *lazyLocalEmbeddingEngine) initialize() error {
	modelPath, onnxFilename, err := resolveLocalEmbeddingModel(e.model, e.lookup)
	if err != nil {
		return err
	}
	var sessionOptions []options.WithOption
	if libraryDirectory := lookupTrimmed(e.lookup, localEmbeddingORTEnvironment); libraryDirectory != "" {
		sessionOptions = append(sessionOptions, options.WithOnnxLibraryPath(libraryDirectory))
	}
	session, err := hugot.NewORTSession(sessionOptions...)
	if err != nil {
		return inference.NewConfigurationError("embedding-model", "ONNX Runtime could not be initialized")
	}
	pipeline, err := hugot.NewPipeline(session, hugot.FeatureExtractionConfig{
		ModelPath: modelPath, Name: localEmbeddingPipelineName, OnnxFilename: onnxFilename,
	})
	if err != nil {
		_ = session.Destroy()
		return inference.NewConfigurationError("embedding-model", "the local ONNX embedding model is invalid")
	}
	e.run = func(inputs []string) ([][]float32, error) {
		output, runErr := pipeline.RunPipeline(inputs)
		if runErr != nil {
			return nil, runErr
		}
		if output == nil {
			return nil, errors.New("empty local embedding output")
		}
		return output.Embeddings, nil
	}
	e.destroy = session.Destroy
	return nil
}

func (e *lazyLocalEmbeddingEngine) close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.destroy == nil {
		return nil
	}
	destroy := e.destroy
	e.run = nil
	e.destroy = nil
	return destroyLocalEmbeddingRuntime(destroy)
}

func newLocalEmbeddingTransport(run localEmbeddingRun, destroy func() error) *sentenceTransformersTransport {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &sentenceTransformersTransport{run: run, destroy: destroy, gate: gate}
}

func (t *sentenceTransformersTransport) Embed(
	ctx context.Context,
	inputs []string,
	inputType inference.EmbeddingInputType,
) (inference.ProviderEmbeddingResult, error) {
	if err := context.Cause(ctx); err != nil {
		return inference.ProviderEmbeddingResult{}, err
	}
	if inputType != inference.EmbeddingDocument && inputType != inference.EmbeddingQuery {
		return inference.ProviderEmbeddingResult{}, inference.NewConfigurationError("request-rejected", "invalid embedding input type")
	}
	if len(inputs) == 0 {
		return inference.NewProviderEmbeddingResult(inputs, inputType, nil, inference.Usage{})
	}
	if !t.admit() {
		return inference.ProviderEmbeddingResult{}, inference.NewUnavailableError("embed")
	}

	call := make(chan localEmbeddingCall, 1)
	resultInputs := slices.Clone(inputs)
	runInputs := slices.Clone(inputs)
	go func() {
		defer t.active.Done()
		vectors, err := t.runSafely(ctx, runInputs)
		call <- localEmbeddingCall{vectors: vectors, err: err}
	}()

	select {
	case <-ctx.Done():
		return inference.ProviderEmbeddingResult{}, context.Cause(ctx)
	case result := <-call:
		if result.err != nil {
			return inference.ProviderEmbeddingResult{}, result.err
		}
		vectors := make([][]float64, len(result.vectors))
		for index, vector := range result.vectors {
			vectors[index] = make([]float64, len(vector))
			for dimension, value := range vector {
				vectors[index][dimension] = float64(value)
			}
		}
		return inference.NewProviderEmbeddingResult(resultInputs, inputType, vectors, inference.Usage{})
	}
}

func (t *sentenceTransformersTransport) admit() bool {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if t.closing || t.closed || t.run == nil {
		return false
	}
	t.active.Add(1)
	return true
}

func (t *sentenceTransformersTransport) runSafely(ctx context.Context, inputs []string) (vectors [][]float32, err error) {
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case <-t.gate:
	}
	defer func() { t.gate <- struct{}{} }()
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	defer func() {
		if recover() != nil {
			vectors = nil
			err = inference.NewUnavailableError("embed")
		}
	}()
	vectors, err = t.run(inputs)
	if err != nil {
		var configuration *inference.ConfigurationError
		var unavailable *inference.UnavailableError
		var timeout *inference.TimeoutError
		var invalid *inference.InvalidOutputError
		if errors.As(err, &configuration) || errors.As(err, &unavailable) ||
			errors.As(err, &timeout) || errors.As(err, &invalid) {
			return nil, err
		}
		return nil, inference.WrapUnavailableError("embed", err)
	}
	return vectors, nil
}

func (t *sentenceTransformersTransport) Close(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.closeMu.Lock()
	defer t.closeMu.Unlock()

	t.stateMu.Lock()
	if t.closed {
		t.stateMu.Unlock()
		return nil
	}
	t.closing = true
	t.stateMu.Unlock()

	drained := make(chan struct{})
	go func() {
		t.active.Wait()
		close(drained)
	}()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-drained:
	}

	var destroyErr error
	if t.destroy != nil {
		destroyErr = destroyLocalEmbeddingRuntime(t.destroy)
	}
	t.stateMu.Lock()
	t.closed = true
	t.run = nil
	t.destroy = nil
	t.stateMu.Unlock()
	if destroyErr != nil {
		return errors.New("local embedding runtime could not be closed")
	}
	return nil
}

func destroyLocalEmbeddingRuntime(destroy func() error) (err error) {
	if destroy == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			err = errors.New("local embedding runtime could not be closed")
		}
	}()
	return destroy()
}

func resolveLocalEmbeddingModel(model string, lookup EnvLookup) (string, string, error) {
	onnxPath := lookupTrimmed(lookup, localEmbeddingONNXEnvironment)
	onnxFilename := filepath.Base(onnxPath)
	if onnxFilename == "." {
		onnxFilename = ""
	}
	if info, err := os.Stat(model); err == nil {
		if !info.IsDir() {
			return "", "", inference.NewConfigurationError("embedding-model", "local embedding model path must be a directory")
		}
		return model, onnxFilename, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", inference.NewConfigurationError("embedding-model", "local embedding model path is not accessible")
	}
	if !validHuggingFaceModelName(model) {
		return "", "", inference.NewConfigurationError("embedding-model", "invalid Hugging Face model name")
	}

	cacheRoot := lookupTrimmed(lookup, localEmbeddingCacheEnvironment)
	if cacheRoot == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", "", inference.NewConfigurationError("embedding-model", "local embedding cache is unavailable")
		}
		cacheRoot = filepath.Join(userCache, "powercontext", "models")
	}
	if err := os.MkdirAll(cacheRoot, 0o750); err != nil {
		return "", "", inference.NewConfigurationError("embedding-model", "local embedding cache is unavailable")
	}
	modelBase, revision, _ := strings.Cut(model, ":")
	downloadRoot := cacheRoot
	if revision != "" {
		// Revisions need independent materialized directories. Reusing the main
		// cache path would silently run stale weights after a configuration change.
		downloadRoot = filepath.Join(cacheRoot, ".revisions", revision)
	}
	cachedPath := filepath.Join(cacheRoot, strings.ReplaceAll(modelBase, "/", "_"))
	if revision != "" {
		cachedPath = filepath.Join(downloadRoot, strings.ReplaceAll(modelBase, "/", "_"))
	}
	if localEmbeddingModelComplete(cachedPath, onnxFilename) {
		return cachedPath, onnxFilename, nil
	}
	if err := os.MkdirAll(downloadRoot, 0o750); err != nil {
		return "", "", inference.NewConfigurationError("embedding-model", "local embedding cache is unavailable")
	}

	downloadOptions := hugot.NewDownloadOptions()
	downloadOptions.AuthToken = lookupTrimmed(lookup, "HF_TOKEN")
	downloadOptions.OnnxFilePath = onnxPath
	if revision != "" {
		downloadOptions.Branch = revision
	}
	downloadedPath, err := hugot.DownloadModel(modelBase, downloadRoot, downloadOptions)
	if err != nil {
		return "", "", inference.NewConfigurationError("embedding-model", "local embedding model could not be downloaded")
	}
	return downloadedPath, onnxFilename, nil
}

func localEmbeddingModelComplete(modelPath, onnxFilename string) bool {
	if info, err := os.Stat(filepath.Join(modelPath, "tokenizer.json")); err != nil || info.IsDir() {
		return false
	}
	count := 0
	selected := onnxFilename == ""
	err := filepath.WalkDir(modelPath, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".onnx") {
			count++
			if entry.Name() == onnxFilename {
				selected = true
			}
		}
		return nil
	})
	if err != nil || count == 0 || !selected {
		return false
	}
	return onnxFilename != "" || count == 1
}

func validHuggingFaceModelName(value string) bool {
	base, revision, hasRevision := strings.Cut(value, ":")
	if hasRevision {
		if revision == "" || strings.ContainsRune(revision, ':') || !validHuggingFaceNamePart(revision) {
			return false
		}
	}
	parts := strings.Split(base, "/")
	if len(parts) < 1 || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if !validHuggingFaceNamePart(part) {
			return false
		}
	}
	return true
}

func validHuggingFaceNamePart(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func lookupTrimmed(lookup EnvLookup, name string) string {
	value, ok := lookup(name)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

var (
	_ inference.EmbeddingTransport              = (*sentenceTransformersTransport)(nil)
	_ interface{ Close(context.Context) error } = (*sentenceTransformersTransport)(nil)
)
