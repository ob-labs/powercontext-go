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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/ob-labs/powercontext-go/artifact/memory"
)

type environmentConfig struct {
	HTTPHost string `env:"HTTP_HOST"`
	HTTPPort int    `env:"HTTP_PORT"`

	MCPEnabled bool   `env:"MCP_ENABLED"`
	MCPPath    string `env:"MCP_PATH"`

	AuthEnabled                     bool   `env:"AUTH_ENABLED"`
	AuthToken                       string `env:"AUTH_TOKEN"`
	AllowUnauthenticatedNonLoopback bool   `env:"ALLOW_UNAUTHENTICATED_NON_LOOPBACK"`

	DashboardEnabled bool   `env:"DASHBOARD_ENABLED"`
	DashboardScopes  string `env:"DASHBOARD_SCOPES"`

	LoggingLevel   string `env:"LOGGING_LEVEL"`
	LoggingFormat  string `env:"LOGGING_FORMAT"`
	LoggingAccess  bool   `env:"LOGGING_ACCESS"`
	MetricsEnabled bool   `env:"METRICS_ENABLED"`
	TracingEnabled bool   `env:"TRACING_ENABLED"`

	ScopeCacheSize             int    `env:"RUNTIME_SCOPE_CACHE_SIZE"`
	SourceWindowLimit          int64  `env:"RUNTIME_SOURCE_WINDOW_LIMIT"`
	MemoryExtractionProfile    string `env:"RUNTIME_MEMORY_EXTRACTION_PROFILE"`
	MemoryRerankEnabled        bool   `env:"RUNTIME_MEMORY_RERANK_ENABLED"`
	MemoryRerankCandidateLimit int    `env:"RUNTIME_MEMORY_RERANK_CANDIDATE_LIMIT"`
	ScheduleSeconds            string `env:"RUNTIME_SCHEDULE_SECONDS"`
	ExperienceScheduleSeconds  string `env:"RUNTIME_EXPERIENCE_SCHEDULE_SECONDS"`

	DatabaseKind          string `env:"DATABASE_KIND"`
	DatabaseURL           string `env:"DATABASE_URL"`
	DatabasePath          string `env:"DATABASE_PATH"`
	DatabaseName          string `env:"DATABASE_DATABASE"`
	DatabaseLibraryPath   string `env:"DATABASE_LIBRARY_PATH"`
	DatabaseBusyTimeoutMS int64  `env:"DATABASE_BUSY_TIMEOUT_MS"`
	DatabaseJournalMode   string `env:"DATABASE_JOURNAL_MODE"`
	DatabaseForeignKeys   bool   `env:"DATABASE_FOREIGN_KEYS"`
	DatabaseMaxOpenConns  int    `env:"DATABASE_MAX_OPEN_CONNS"`
	DatabaseMaxIdleConns  int    `env:"DATABASE_MAX_IDLE_CONNS"`
	DatabaseMaxLifetime   string `env:"DATABASE_MAX_LIFETIME"`

	HandoffReportEnabled bool `env:"HANDOFF_REPORT_ENABLED"`

	GenerationModel          string `env:"INFERENCE_GENERATION_MODEL"`
	GenerationTimeoutSeconds string `env:"INFERENCE_GENERATION_TIMEOUT_SECONDS"`
	GenerationMaxRequests    int    `env:"INFERENCE_GENERATION_MAX_REQUESTS"`
	EmbeddingModel           string `env:"INFERENCE_EMBEDDING_MODEL"`
	EmbeddingProfileID       string `env:"INFERENCE_EMBEDDING_PROFILE_ID"`
	EmbeddingDimension       int    `env:"INFERENCE_EMBEDDING_DIMENSION"`
	EmbeddingNormalization   string `env:"INFERENCE_EMBEDDING_NORMALIZATION"`
	EmbeddingTimeoutSeconds  string `env:"INFERENCE_EMBEDDING_TIMEOUT_SECONDS"`
	EmbeddingBatchSize       int    `env:"INFERENCE_EMBEDDING_BATCH_SIZE"`
	ExternalSkills           string `env:"EXTERNAL_SKILLS"`
	ExternalSkillHostID      string `env:"EXTERNAL_SKILLS_HOST_ID"`
	ExternalSkillTargets     string `env:"EXTERNAL_SKILLS_TARGETS"`
	ExternalSkillRoots       string `env:"EXTERNAL_SKILLS_CODEX_ROOTS"`
	SchedulerPath            string `env:"SCHEDULER_PATH"`
}

type externalSkillsEnvironment struct {
	HostID     string                `json:"host_id"`
	Targets    []ExternalSkillTarget `json:"targets"`
	CodexRoots []ExternalSkillRoot   `json:"codex_roots"`
}

// LoadConfig overlays POWERCONTEXT_SERVER_* values on the frozen Python
// defaults. Nested object fields use the same one-level underscore spelling;
// slices use JSON, matching pydantic-settings' environment representation.
func LoadConfig() (ProcessConfig, error) {
	return loadConfig(HTTPConfigOverride{})
}

// LoadConfigWithHTTPOverride overlays explicitly provided HTTP command-line
// values before running the same final validation used by LoadConfig.
func LoadConfigWithHTTPOverride(override HTTPConfigOverride) (ProcessConfig, error) {
	return loadConfig(override)
}

func loadConfig(override HTTPConfigOverride) (ProcessConfig, error) {
	defaults, err := defaultEnvironmentConfig()
	if err != nil {
		return ProcessConfig{}, err
	}
	if err := env.ParseWithOptions(&defaults, env.Options{Prefix: serverEnvironmentPrefix}); err != nil {
		return ProcessConfig{}, fmt.Errorf("server: invalid environment configuration: %w", err)
	}
	return buildProcessConfig(defaults, override)
}

// DefaultConfig returns the same validated defaults used by LoadConfig,
// without consulting POWERCONTEXT_SERVER_* overrides.
func DefaultConfig() (ProcessConfig, error) {
	defaults, err := defaultEnvironmentConfig()
	if err != nil {
		return ProcessConfig{}, err
	}
	return buildProcessConfig(defaults, HTTPConfigOverride{})
}

func defaultEnvironmentConfig() (environmentConfig, error) {
	databasePath, err := DefaultDatabasePath()
	if err != nil {
		return environmentConfig{}, err
	}
	schedulerPath, err := DefaultSchedulerPath()
	if err != nil {
		return environmentConfig{}, err
	}
	seekDBPath, err := DefaultSeekDBPath()
	if err != nil {
		return environmentConfig{}, err
	}
	return environmentConfig{
		HTTPHost: "127.0.0.1", HTTPPort: 8000,
		MCPEnabled: true, MCPPath: DefaultMCPPath,
		DashboardEnabled: true, HandoffReportEnabled: true,
		LoggingLevel: "INFO", LoggingFormat: "console", LoggingAccess: true,
		MetricsEnabled: true,
		ScopeCacheSize: 128, SourceWindowLimit: 100, MemoryExtractionProfile: string(memory.CodingProfile),
		MemoryRerankCandidateLimit: 30,
		DatabaseKind:               "sqlite", DatabaseURL: sqliteURL(databasePath),
		DatabasePath: seekDBPath, DatabaseName: "test",
		DatabaseBusyTimeoutMS: 5_000, DatabaseJournalMode: "WAL", DatabaseForeignKeys: true,
		DatabaseMaxOpenConns: 8, DatabaseMaxIdleConns: 8,
		GenerationTimeoutSeconds: "30", GenerationMaxRequests: 2,
		EmbeddingNormalization: "unit", EmbeddingTimeoutSeconds: "30", EmbeddingBatchSize: 10,
		SchedulerPath: schedulerPath,
	}, nil
}

func buildProcessConfig(value environmentConfig, override HTTPConfigOverride) (ProcessConfig, error) {
	mcpPath, err := normalizeMCPPath(value.MCPPath)
	if err != nil {
		return ProcessConfig{}, err
	}
	generationTimeout, err := positiveSeconds("inference.generation_timeout_seconds", value.GenerationTimeoutSeconds)
	if err != nil {
		return ProcessConfig{}, err
	}
	embeddingTimeout, err := positiveSeconds("inference.embedding_timeout_seconds", value.EmbeddingTimeoutSeconds)
	if err != nil {
		return ProcessConfig{}, err
	}
	sourceSchedule, err := optionalPositiveSeconds("runtime.schedule_seconds", value.ScheduleSeconds)
	if err != nil {
		return ProcessConfig{}, err
	}
	experienceSchedule, err := optionalPositiveSeconds("runtime.experience_schedule_seconds", value.ExperienceScheduleSeconds)
	if err != nil {
		return ProcessConfig{}, err
	}
	maxLifetime, err := optionalDuration("database.max_lifetime", value.DatabaseMaxLifetime)
	if err != nil {
		return ProcessConfig{}, err
	}
	seekDBPath := strings.TrimSpace(value.DatabasePath)
	if seekDBPath == "" {
		seekDBPath, err = DefaultSeekDBPath()
		if err != nil {
			return ProcessConfig{}, err
		}
	}

	var scopes []DashboardScope
	if strings.TrimSpace(value.DashboardScopes) != "" {
		if err := decodeJSONArray(value.DashboardScopes, &scopes); err != nil {
			return ProcessConfig{}, fmt.Errorf("server: dashboard.scopes must be a JSON array: %w", err)
		}
		for index := range scopes {
			// Pydantic applies the declared length bounds to the input and then
			// strips these two fields in its after-validator.
			if len([]rune(scopes[index].ScopeID)) > 255 || len([]rune(scopes[index].DisplayName)) > 80 {
				return ProcessConfig{}, errors.New("server: Dashboard scope values are invalid")
			}
			scopes[index].ScopeID = strings.TrimSpace(scopes[index].ScopeID)
			scopes[index].DisplayName = strings.TrimSpace(scopes[index].DisplayName)
		}
	}
	externalHostID := value.ExternalSkillHostID
	var targets []ExternalSkillTarget
	var roots []ExternalSkillRoot
	if strings.TrimSpace(value.ExternalSkills) != "" {
		if value.ExternalSkillHostID != "" || strings.TrimSpace(value.ExternalSkillTargets) != "" ||
			strings.TrimSpace(value.ExternalSkillRoots) != "" {
			return ProcessConfig{}, errors.New("server: external_skills JSON cannot be combined with split external Skill settings")
		}
		var external externalSkillsEnvironment
		if err := decodeJSONObject(value.ExternalSkills, &external); err != nil {
			return ProcessConfig{}, fmt.Errorf("server: external_skills must be a JSON object: %w", err)
		}
		externalHostID, targets, roots = external.HostID, external.Targets, external.CodexRoots
	} else {
		if strings.TrimSpace(value.ExternalSkillTargets) != "" {
			if err := decodeJSONArray(value.ExternalSkillTargets, &targets); err != nil {
				return ProcessConfig{}, fmt.Errorf("server: external_skills.targets must be a JSON array: %w", err)
			}
		}
		if strings.TrimSpace(value.ExternalSkillRoots) != "" {
			if err := decodeJSONArray(value.ExternalSkillRoots, &roots); err != nil {
				return ProcessConfig{}, fmt.Errorf("server: external_skills.codex_roots must be a JSON array: %w", err)
			}
		}
	}

	config := ProcessConfig{
		HTTP:                            HTTPConfig{Host: value.HTTPHost, Port: value.HTTPPort},
		MCP:                             MCPConfig{Enabled: value.MCPEnabled, Path: mcpPath},
		Auth:                            AuthConfig{Enabled: value.AuthEnabled, Token: value.AuthToken},
		AllowUnauthenticatedNonLoopback: value.AllowUnauthenticatedNonLoopback,
		Dashboard:                       DashboardConfig{Enabled: value.DashboardEnabled, Scopes: scopes},
		Logging:                         LoggingConfig{Level: strings.ToUpper(value.LoggingLevel), Format: value.LoggingFormat, Access: value.LoggingAccess},
		Metrics:                         MetricsConfig{Enabled: value.MetricsEnabled}, Tracing: TracingConfig{Enabled: value.TracingEnabled},
		Runtime: RuntimeConfig{
			ScopeCacheSize: value.ScopeCacheSize, SourceWindowLimit: value.SourceWindowLimit,
			MemoryExtractionProfile: memory.ExtractionProfile(value.MemoryExtractionProfile),
			MemoryRerankEnabled:     value.MemoryRerankEnabled, MemoryRerankCandidateLimit: value.MemoryRerankCandidateLimit,
			SourceWindowInterval: sourceSchedule, ExperienceIncubationInterval: experienceSchedule,
		},
		Database: DatabaseConfig{
			Kind: value.DatabaseKind,
			SQLite: SQLiteDatabaseConfig{
				URL: value.DatabaseURL, BusyTimeout: time.Duration(value.DatabaseBusyTimeoutMS) * time.Millisecond,
				JournalMode: value.DatabaseJournalMode, ForeignKeys: value.DatabaseForeignKeys,
				MaxOpenConns: value.DatabaseMaxOpenConns,
				MaxIdleConns: value.DatabaseMaxIdleConns, ConnMaxLifetime: maxLifetime,
			},
			OceanBase: OceanBaseDatabaseConfig{
				URL: value.DatabaseURL, MaxOpenConns: value.DatabaseMaxOpenConns,
				MaxIdleConns: value.DatabaseMaxIdleConns, MaxLifetime: maxLifetime,
			},
			SeekDB: SeekDBDatabaseConfig{
				Path: seekDBPath, Database: value.DatabaseName,
				LibraryPath:  strings.TrimSpace(value.DatabaseLibraryPath),
				MaxOpenConns: value.DatabaseMaxOpenConns, MaxIdleConns: value.DatabaseMaxIdleConns,
				MaxLifetime: maxLifetime,
			},
		},
		HandoffReport: HandoffReportConfig{Enabled: value.HandoffReportEnabled},
		Inference: InferenceConfig{
			GenerationModel: strings.TrimSpace(value.GenerationModel), GenerationTimeout: generationTimeout,
			GenerationMaxRequests: value.GenerationMaxRequests, EmbeddingModel: strings.TrimSpace(value.EmbeddingModel),
			EmbeddingProfileID: strings.TrimSpace(value.EmbeddingProfileID), EmbeddingDimension: value.EmbeddingDimension,
			EmbeddingNormalization: strings.TrimSpace(value.EmbeddingNormalization), EmbeddingTimeout: embeddingTimeout,
			EmbeddingBatchSize: value.EmbeddingBatchSize,
		},
		ExternalSkills: ExternalSkillsConfig{HostID: externalHostID, Targets: targets, CodexRoots: roots},
		SchedulerPath:  value.SchedulerPath,
	}
	if override.Host != nil {
		config.HTTP.Host = *override.Host
	}
	if override.Port != nil {
		config.HTTP.Port = *override.Port
	}
	if err := config.Validate(); err != nil {
		return ProcessConfig{}, err
	}
	return config, nil
}
