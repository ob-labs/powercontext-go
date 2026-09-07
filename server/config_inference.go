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
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var inferenceHTTPFieldNamePattern = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")

// InferenceHeaders holds static provider headers without rendering their
// values in text, Go-syntax, or structured-log representations.
type InferenceHeaders struct {
	values map[string]string
}

// Value returns one configured header value. Provider composition must treat
// the result as sensitive and attach it only to the selected workload client.
func (h InferenceHeaders) Value(name string) (string, bool) {
	value, found := h.values[name]
	return value, found
}

// Values returns an isolated copy for the provider client that owns the
// workload request. The copy prevents callers from mutating ProcessConfig.
func (h InferenceHeaders) Values() map[string]string { return maps.Clone(h.values) }

func (h InferenceHeaders) String() string   { return h.redactedString() }
func (h InferenceHeaders) GoString() string { return h.redactedString() }

func (h InferenceHeaders) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("configured", len(h.values) > 0),
		slog.Int("count", len(h.values)),
	)
}

func (h InferenceHeaders) redactedString() string {
	return fmt.Sprintf("{Configured:%t Count:%d}", len(h.values) > 0, len(h.values))
}

// InferenceWorkloadConfig contains the provider-specific endpoint, static
// headers, and model settings for one workload. Model identifiers remain in
// InferenceConfig's existing generation and embedding fields for compatibility.
type InferenceWorkloadConfig struct {
	BaseURL       string
	Headers       InferenceHeaders
	ModelSettings map[string]any
}

func (c *InferenceWorkloadConfig) String() string   { return c.redactedString() }
func (c *InferenceWorkloadConfig) GoString() string { return c.redactedString() }

func (c *InferenceWorkloadConfig) LogValue() slog.Value {
	if c == nil {
		return slog.GroupValue(slog.Bool("configured", false))
	}
	return slog.GroupValue(
		slog.Bool("base_url_configured", c.BaseURL != ""),
		slog.Any("headers", c.Headers),
		slog.Int("model_settings_count", len(c.ModelSettings)),
	)
}

func (c *InferenceWorkloadConfig) redactedString() string {
	if c == nil {
		return "{Configured:false}"
	}
	return fmt.Sprintf(
		"{BaseURLConfigured:%t Headers:%s ModelSettingsConfigured:%t ModelSettingsCount:%d}",
		c.BaseURL != "", c.Headers, len(c.ModelSettings) > 0, len(c.ModelSettings),
	)
}

// RerankInferenceConfig carries an optional model and per-workload overrides.
// An empty Model allows headers and model settings to inherit the configured
// generation model. A distinct BaseURL always requires a distinct rerank model.
type RerankInferenceConfig struct {
	Workload    *InferenceWorkloadConfig
	Model       string
	Timeout     *time.Duration
	MaxRequests *int
}

func (c *RerankInferenceConfig) String() string   { return c.redactedString() }
func (c *RerankInferenceConfig) GoString() string { return c.redactedString() }

func (c *RerankInferenceConfig) LogValue() slog.Value {
	if c == nil {
		return slog.GroupValue(slog.Bool("configured", false))
	}
	return slog.GroupValue(
		slog.Bool("model_configured", c.Model != ""),
		slog.Bool("timeout_configured", c.Timeout != nil),
		slog.Bool("max_requests_configured", c.MaxRequests != nil),
		slog.Any("workload", c.Workload),
	)
}

func (c *RerankInferenceConfig) redactedString() string {
	if c == nil {
		return "{Configured:false}"
	}
	return fmt.Sprintf(
		"{ModelConfigured:%t TimeoutConfigured:%t MaxRequestsConfigured:%t Workload:%s}",
		c.Model != "", c.Timeout != nil, c.MaxRequests != nil, c.Workload,
	)
}

func (c InferenceConfig) String() string   { return c.redactedString() }
func (c InferenceConfig) GoString() string { return c.redactedString() }

func (c InferenceConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("generation_model_configured", c.GenerationModel != ""),
		slog.Any("generation", c.Generation),
		slog.Bool("embedding_model_configured", c.EmbeddingModel != ""),
		slog.Any("embedding", c.Embedding),
		slog.Bool("embedding_profile_configured", c.EmbeddingProfileID != ""),
		slog.Bool("embedding_dimension_configured", c.EmbeddingDimension != 0),
		slog.Any("rerank", c.Rerank),
	)
}

func (c InferenceConfig) redactedString() string {
	return fmt.Sprintf(
		"{GenerationModelConfigured:%t Generation:%s EmbeddingModelConfigured:%t Embedding:%s EmbeddingProfileConfigured:%t EmbeddingDimensionConfigured:%t Rerank:%s}",
		c.GenerationModel != "", c.Generation, c.EmbeddingModel != "", c.Embedding,
		c.EmbeddingProfileID != "", c.EmbeddingDimension != 0, c.Rerank,
	)
}

func (c InferenceConfig) validate() error {
	if c.GenerationTimeout <= 0 || c.GenerationMaxRequests < 1 || c.EmbeddingTimeout <= 0 || c.EmbeddingBatchSize < 1 {
		return fmt.Errorf("server: inference limits are invalid")
	}
	if c.EmbeddingNormalization != "none" && c.EmbeddingNormalization != "unit" {
		return fmt.Errorf("server: embedding normalization must be none or unit")
	}
	if err := validateInferenceIdentifier("generation", c.GenerationModel); err != nil {
		return err
	}
	if err := validateInferenceIdentifier("embedding", c.EmbeddingModel); err != nil {
		return err
	}
	if err := validateInferenceIdentifier("embedding profile", c.EmbeddingProfileID); err != nil {
		return err
	}
	if err := validateInferenceWorkload("generation", c.Generation); err != nil {
		return err
	}
	if err := validateInferenceWorkload("embedding", c.Embedding); err != nil {
		return err
	}
	if c.Rerank != nil {
		if err := validateInferenceIdentifier("rerank", c.Rerank.Model); err != nil {
			return err
		}
		if err := validateInferenceWorkload("rerank", c.Rerank.Workload); err != nil {
			return err
		}
		if c.Rerank.Timeout != nil && *c.Rerank.Timeout <= 0 {
			return fmt.Errorf("server: rerank timeout must be positive")
		}
		if c.Rerank.MaxRequests != nil && *c.Rerank.MaxRequests < 1 {
			return fmt.Errorf("server: rerank max requests must be positive")
		}
	}

	embeddingConfigured := []bool{c.EmbeddingModel != "", c.EmbeddingProfileID != "", c.EmbeddingDimension != 0}
	if (embeddingConfigured[0] || embeddingConfigured[1] || embeddingConfigured[2]) && !(embeddingConfigured[0] && embeddingConfigured[1] && embeddingConfigured[2]) {
		return fmt.Errorf("server: embedding model, profile ID, and dimension must be configured together")
	}
	if c.EmbeddingDimension < 0 || (c.EmbeddingModel != "" && c.EmbeddingDimension < 1) {
		return fmt.Errorf("server: embedding dimension must be positive")
	}
	if c.GenerationModel == "" && c.Generation.hasOverrides() {
		return fmt.Errorf("server: generation overrides require generation model")
	}
	if c.EmbeddingModel == "" && c.Embedding.hasOverrides() {
		return fmt.Errorf("server: embedding overrides require a complete embedding profile")
	}
	if c.Rerank != nil {
		if c.Rerank.Workload != nil && c.Rerank.Workload.BaseURL != "" && c.Rerank.Model == "" {
			return fmt.Errorf("server: rerank base URL requires rerank model")
		}
		if c.Rerank.Model == "" && c.GenerationModel == "" && c.Rerank.Workload.hasOverrides() {
			return fmt.Errorf("server: rerank overrides require rerank model or generation model")
		}
	}
	return nil
}

func (c *InferenceWorkloadConfig) hasOverrides() bool {
	if c == nil {
		return false
	}
	return c.BaseURL != "" || c.HeadersConfigured() || len(c.ModelSettings) > 0
}

func (c *InferenceWorkloadConfig) HeadersConfigured() bool {
	return c != nil && len(c.Headers.values) > 0
}

func validateInferenceIdentifier(workload, value string) error {
	if value != "" && (value != strings.TrimSpace(value) || strings.TrimSpace(value) == "") {
		return fmt.Errorf("server: %s model identifier is invalid", workload)
	}
	return nil
}

func validateInferenceWorkload(workload string, config *InferenceWorkloadConfig) error {
	if config == nil {
		return nil
	}
	if config.BaseURL != "" {
		parsed, err := url.ParseRequestURI(config.BaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("server: %s base URL is invalid", workload)
		}
	}
	if err := validateInferenceHeaders(workload, config.Headers); err != nil {
		return err
	}
	if _, found := config.ModelSettings["extra_headers"]; found {
		return fmt.Errorf("server: %s model settings must use the dedicated headers field", workload)
	}
	return nil
}

func validateInferenceHeaders(workload string, headers InferenceHeaders) error {
	seen := make(map[string]struct{}, len(headers.values))
	for name, value := range headers.values {
		if !inferenceHTTPFieldNamePattern.MatchString(name) {
			return fmt.Errorf("server: %s header names must be non-empty HTTP field names", workload)
		}
		canonical := strings.ToLower(name)
		if _, duplicate := seen[canonical]; duplicate {
			return fmt.Errorf("server: %s header names must be unique ignoring case", workload)
		}
		if value == "" {
			return fmt.Errorf("server: %s header values must not be empty", workload)
		}
		seen[canonical] = struct{}{}
	}
	return nil
}
