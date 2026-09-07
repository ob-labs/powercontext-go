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

package server

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestInferenceConfigRemainsComparableWithWorkloadConfiguration(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL", "openai-chat:generator")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_HEADERS", `{"Authorization":"generation-secret"}`)

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	assertComparable(config.Inference)
	snapshot := config.Inference
	if config.Inference != snapshot {
		t.Fatal("an InferenceConfig must compare equal to itself")
	}
}

func TestDefaultInferenceWorkloadPointersRemainAbsent(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Inference.Generation != nil || config.Inference.Embedding != nil || config.Inference.Rerank != nil {
		t.Fatalf("default inference workloads = %#v", config.Inference)
	}
	assertComparable(config.Inference)
	snapshot := config.Inference
	if config.Inference != snapshot {
		t.Fatal("a default InferenceConfig must compare equal to itself")
	}
	_ = fmt.Sprintf("%#v", config.Inference)
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("inference", "config", config.Inference)
}

func assertComparable[T comparable](T) {}

func TestLoadConfigParsesIndependentInferenceWorkloadsAndRedactsHeaders(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL", " openai-chat:generator ")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_BASE_URL", " https://generation.test/v1 ")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_HEADERS", `{"X-Workload":"generation-secret"}`)
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL_SETTINGS", `{"max_tokens":256}`)
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_MODEL", "openai:embedding")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_BASE_URL", "https://embedding.test/v1")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_HEADERS", `{"X-Workload":"embedding-secret"}`)
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_MODEL_SETTINGS", `{"dimensions":3}`)
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_PROFILE_ID", "embedding-v1")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_DIMENSION", "3")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_RERANK_MODEL", "openai-chat:reranker")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_RERANK_BASE_URL", "https://rerank.test/v1")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_RERANK_HEADERS", `{"X-Workload":"rerank-secret"}`)
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_RERANK_MODEL_SETTINGS", `{"top_p":0.25}`)
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_RERANK_TIMEOUT_SECONDS", "8.5")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_RERANK_MAX_REQUESTS", "3")

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Inference.Generation == nil || config.Inference.Embedding == nil || config.Inference.Rerank == nil ||
		config.Inference.Rerank.Workload == nil ||
		config.Inference.Generation.BaseURL != "https://generation.test/v1" ||
		config.Inference.Embedding.BaseURL != "https://embedding.test/v1" ||
		config.Inference.Rerank.Workload.BaseURL != "https://rerank.test/v1" ||
		config.Inference.Rerank.Model != "openai-chat:reranker" {
		t.Fatalf("workload endpoints = %#v", config.Inference)
	}
	for _, test := range []struct {
		name     string
		headers  InferenceHeaders
		settings map[string]any
		secret   string
	}{
		{name: "generation", headers: config.Inference.Generation.Headers, settings: config.Inference.Generation.ModelSettings, secret: "generation-secret"},
		{name: "embedding", headers: config.Inference.Embedding.Headers, settings: config.Inference.Embedding.ModelSettings, secret: "embedding-secret"},
		{name: "rerank", headers: config.Inference.Rerank.Workload.Headers, settings: config.Inference.Rerank.Workload.ModelSettings, secret: "rerank-secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, found := test.headers.Value("X-Workload")
			if !found || value != test.secret {
				t.Fatalf("header = %q, found = %t", value, found)
			}
			if len(test.settings) != 1 {
				t.Fatalf("model settings = %#v", test.settings)
			}
		})
	}
	if config.Inference.Rerank.Timeout == nil || config.Inference.Rerank.MaxRequests == nil ||
		config.Inference.Rerank.Timeout.String() != "8.5s" || *config.Inference.Rerank.MaxRequests != 3 {
		t.Fatalf("rerank limits = %#v", config.Inference.Rerank)
	}

	representations := []string{fmt.Sprintf("%v", config.Inference), fmt.Sprintf("%#v", config.Inference)}
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("inference", "config", config.Inference)
	representations = append(representations, logged.String())
	for _, secret := range []string{"generation-secret", "embedding-secret", "rerank-secret"} {
		for _, representation := range representations {
			if strings.Contains(representation, secret) {
				t.Fatalf("inference configuration leaked a header secret in %q", representation)
			}
		}
	}
}

func TestLoadConfigAllowsRerankOverridesToInheritGeneration(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL", "openai-chat:generator")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_HEADERS", `{"X-Generation":"generation-secret"}`)
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL_SETTINGS", `{"temperature":0.1}`)
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_RERANK_HEADERS", `{"X-Rerank":"rerank-secret"}`)
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_RERANK_MODEL_SETTINGS", `{"top_p":0.25}`)

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Inference.Rerank == nil || config.Inference.Rerank.Workload == nil || config.Inference.Rerank.Model != "" {
		t.Fatalf("rerank = %#v, want generation-backed inheritance", config.Inference.Rerank)
	}
	if value, found := config.Inference.Rerank.Workload.Headers.Value("X-Rerank"); !found || value != "rerank-secret" {
		t.Fatalf("rerank headers = %#v", config.Inference.Rerank.Workload.Headers)
	}
	if config.Inference.Rerank.Workload.ModelSettings["top_p"] != 0.25 {
		t.Fatalf("rerank settings = %#v", config.Inference.Rerank.Workload.ModelSettings)
	}
}

func TestLoadConfigRejectsUnsafeWorkloadOverridesWithoutLeakingHeaderValues(t *testing.T) {
	for _, test := range []struct {
		name   string
		setenv map[string]string
		secret string
	}{
		{
			name:   "generation base URL without model",
			setenv: map[string]string{"POWERCONTEXT_SERVER_INFERENCE_GENERATION_BASE_URL": "https://generation.test/v1"},
		},
		{
			name: "unsupported base URL scheme",
			setenv: map[string]string{
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL":    "openai-chat:generator",
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_BASE_URL": "ftp://generation.test/v1",
			},
		},
		{
			name:   "generation headers without model",
			setenv: map[string]string{"POWERCONTEXT_SERVER_INFERENCE_GENERATION_HEADERS": `{"Authorization":"generation-secret"}`},
			secret: "generation-secret",
		},
		{
			name:   "embedding settings without complete profile",
			setenv: map[string]string{"POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_MODEL_SETTINGS": `{"dimensions":3}`},
		},
		{
			name: "rerank base URL without separate model",
			setenv: map[string]string{
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL": "openai-chat:generator",
				"POWERCONTEXT_SERVER_INFERENCE_RERANK_BASE_URL":  "https://rerank.test/v1",
			},
		},
		{
			name:   "rerank headers without a source model",
			setenv: map[string]string{"POWERCONTEXT_SERVER_INFERENCE_RERANK_HEADERS": `{"Authorization":"rerank-secret"}`},
			secret: "rerank-secret",
		},
		{
			name: "invalid header field name",
			setenv: map[string]string{
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL":   "openai-chat:generator",
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_HEADERS": `{"Bad Header":"header-secret"}`,
			},
			secret: "header-secret",
		},
		{
			name: "empty header value",
			setenv: map[string]string{
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL":   "openai-chat:generator",
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_HEADERS": `{"Authorization":""}`,
			},
		},
		{
			name: "case-insensitive duplicate header names",
			setenv: map[string]string{
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL":   "openai-chat:generator",
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_HEADERS": `{"X-Workload":"first-secret","x-workload":"second-secret"}`,
			},
			secret: "first-secret",
		},
		{
			name: "duplicate JSON header name",
			setenv: map[string]string{
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL":   "openai-chat:generator",
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_HEADERS": `{"X-Workload":"first-secret","X-Workload":"second-secret"}`,
			},
			secret: "first-secret",
		},
		{
			name: "model settings cannot carry headers",
			setenv: map[string]string{
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL":          "openai-chat:generator",
				"POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL_SETTINGS": `{"extra_headers":{"Authorization":"settings-secret"}}`,
			},
			secret: "settings-secret",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(PowerContextHomeEnv, t.TempDir())
			for name, value := range test.setenv {
				t.Setenv(name, value)
			}
			_, err := LoadConfig()
			if err == nil {
				t.Fatal("LoadConfig accepted an invalid workload override")
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("configuration error leaked a header secret: %v", err)
			}
		})
	}
}
