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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

var externalSkillTargetIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// UnauthenticatedNonLoopbackBindError reports a Server bind that would expose
// unauthenticated HTTP or MCP traffic outside the local machine.
type UnauthenticatedNonLoopbackBindError struct{}

func (*UnauthenticatedNonLoopbackBindError) Error() string {
	return "server: refusing to bind an unauthenticated Server to a non-loopback address; " +
		"enable authentication, keep the bind on loopback, or set " +
		"POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK=true to opt in"
}

func (c ProcessConfig) Validate() error {
	if strings.TrimSpace(c.HTTP.Host) == "" || c.HTTP.Port < 1 || c.HTTP.Port > 65_535 {
		return errors.New("server: HTTP host and port are invalid")
	}
	if _, err := normalizeMCPPath(c.MCP.Path); err != nil {
		return err
	}
	if c.Auth.Enabled && c.Auth.Token == "" {
		return errors.New("server: bearer token is required when authentication is enabled")
	}
	if !c.Auth.Enabled && !c.AllowUnauthenticatedNonLoopback && !transportpolicy.IsLoopbackHost(c.HTTP.Host) {
		return &UnauthenticatedNonLoopbackBindError{}
	}
	if err := validateDashboard(c.Dashboard); err != nil {
		return err
	}
	switch c.Logging.Level {
	case "DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL":
	default:
		return errors.New("server: logging level is invalid")
	}
	if c.Logging.Format != "console" && c.Logging.Format != "json" {
		return errors.New("server: logging format must be console or json")
	}
	if c.Runtime.ScopeCacheSize < 1 || c.Runtime.SourceWindowLimit < 1 ||
		c.Runtime.MemoryRerankCandidateLimit < 1 || c.Runtime.MemoryRerankCandidateLimit > 100 {
		return errors.New("server: Runtime limits are invalid")
	}
	if _, err := memory.ExtractionInstructions(c.Runtime.MemoryExtractionProfile); err != nil {
		return fmt.Errorf("server: %w", err)
	}
	if c.Database.Kind != "sqlite" && c.Database.Kind != "oceanbase" && c.Database.Kind != "seekdb" {
		return errors.New("server: database kind must be sqlite, oceanbase, or seekdb")
	}
	if c.Database.Kind == "sqlite" {
		if _, err := SQLiteDSN(c.Database.SQLite.URL); err != nil {
			return err
		}
		if c.Database.SQLite.BusyTimeout < 0 || c.Database.SQLite.MaxOpenConns < 1 || c.Database.SQLite.MaxIdleConns < 0 {
			return errors.New("server: SQLite connection settings are invalid")
		}
		switch c.Database.SQLite.JournalMode {
		case "WAL", "DELETE", "MEMORY":
		default:
			return errors.New("server: SQLite journal mode is invalid")
		}
	} else if c.Database.Kind == "oceanbase" {
		if err := sqlstore.ValidateOceanBaseURL(c.Database.OceanBase.URL); err != nil {
			return fmt.Errorf("server: %w", err)
		}
	} else {
		if strings.TrimSpace(c.Database.SeekDB.Path) == "" || c.Database.SeekDB.Path != strings.TrimSpace(c.Database.SeekDB.Path) {
			return errors.New("server: embedded seekDB path must be a non-empty trimmed path")
		}
		if c.Database.SeekDB.Database != "test" {
			return errors.New("server: embedded seekDB database must be test")
		}
		if c.Database.SeekDB.MaxOpenConns < 1 || c.Database.SeekDB.MaxIdleConns < 0 ||
			c.Database.SeekDB.MaxIdleConns > c.Database.SeekDB.MaxOpenConns || c.Database.SeekDB.MaxLifetime < 0 {
			return errors.New("server: embedded seekDB connection pool limits are invalid")
		}
	}
	if c.Inference.GenerationTimeout <= 0 || c.Inference.GenerationMaxRequests < 1 || c.Inference.EmbeddingTimeout <= 0 || c.Inference.EmbeddingBatchSize < 1 {
		return errors.New("server: inference limits are invalid")
	}
	if c.Inference.EmbeddingNormalization != "none" && c.Inference.EmbeddingNormalization != "unit" {
		return errors.New("server: embedding normalization must be none or unit")
	}
	embeddingConfigured := []bool{c.Inference.EmbeddingModel != "", c.Inference.EmbeddingProfileID != "", c.Inference.EmbeddingDimension != 0}
	if (embeddingConfigured[0] || embeddingConfigured[1] || embeddingConfigured[2]) && !(embeddingConfigured[0] && embeddingConfigured[1] && embeddingConfigured[2]) {
		return errors.New("server: embedding model, profile ID, and dimension must be configured together")
	}
	if c.Inference.EmbeddingDimension < 0 || (c.Inference.EmbeddingModel != "" && c.Inference.EmbeddingDimension < 1) {
		return errors.New("server: embedding dimension must be positive")
	}
	if c.Runtime.MemoryRerankEnabled && c.Inference.GenerationModel == "" {
		return errors.New("server: Memory reranking requires a generation model")
	}
	if c.Runtime.SourceWindowInterval != nil && c.Inference.GenerationModel == "" {
		return errors.New("server: scheduled Source processing requires a generation model")
	}
	if c.Runtime.ExperienceIncubationInterval != nil && c.Inference.GenerationModel == "" {
		return errors.New("server: scheduled Experience incubation requires a generation model")
	}
	if err := validateExternalSkills(c.ExternalSkills); err != nil {
		return err
	}
	if (c.Runtime.SourceWindowInterval != nil || c.Runtime.ExperienceIncubationInterval != nil) && strings.TrimSpace(c.SchedulerPath) == "" {
		return errors.New("server: scheduler path is required when schedules are enabled")
	}
	return nil
}

func validateDashboard(config DashboardConfig) error {
	if len(config.Scopes) > 100 {
		return errors.New("server: Dashboard supports at most 100 scopes")
	}
	seen := make(map[string]struct{}, len(config.Scopes))
	for _, scope := range config.Scopes {
		id, name := strings.TrimSpace(scope.ScopeID), strings.TrimSpace(scope.DisplayName)
		if id == "" || name == "" || id != scope.ScopeID || name != scope.DisplayName || len([]rune(id)) > 255 || len([]rune(name)) > 80 {
			return errors.New("server: Dashboard scope values are invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("server: Dashboard scope IDs must be unique")
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateExternalSkills(config ExternalSkillsConfig) error {
	targetCount := len(config.Targets) + len(config.CodexRoots)
	if targetCount > 0 && (strings.TrimSpace(config.HostID) == "" || config.HostID != strings.TrimSpace(config.HostID)) {
		return errors.New("server: external Skill Agent targets require a trimmed host identity")
	}
	if len([]rune(config.HostID)) > 128 {
		return errors.New("server: external Skill host identity is too long")
	}
	seen := make(map[string]struct{}, targetCount)
	validateTarget := func(id, agentKind, installationScope, path string) error {
		if len(id) < 1 || len(id) > 64 || !externalSkillTargetIDPattern.MatchString(id) || path == "" {
			return errors.New("server: external Skill Agent target is incomplete")
		}
		if _, duplicate := seen[id]; duplicate {
			return errors.New("server: external Skill Agent target IDs must be unique")
		}
		seen[id] = struct{}{}
		switch agentKind {
		case "codex", "claude_code":
		default:
			return errors.New("server: external Skill Agent kind is invalid")
		}
		switch installationScope {
		case "user", "project", "plugin":
		default:
			return errors.New("server: external Skill installation scope is invalid")
		}
		return nil
	}
	for _, target := range config.Targets {
		if err := validateTarget(target.TargetID, target.AgentKind, target.InstallationScope, target.Path); err != nil {
			return err
		}
	}
	for _, root := range config.CodexRoots {
		if err := validateTarget(root.RootID, "codex", root.InstallationScope, root.Path); err != nil {
			return err
		}
	}
	return nil
}

func positiveSeconds(field, value string) (time.Duration, error) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("server: %s must be positive", field)
	}
	duration := time.Duration(parsed * float64(time.Second))
	if duration <= 0 || duration%time.Microsecond != 0 {
		return 0, fmt.Errorf("server: %s must have microsecond precision", field)
	}
	return duration, nil
}

func optionalPositiveSeconds(field, value string) (*time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	duration, err := positiveSeconds(field, value)
	if err != nil {
		return nil, err
	}
	return &duration, nil
}

func optionalDuration(field, value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 {
		return 0, fmt.Errorf("server: %s is invalid", field)
	}
	return duration, nil
}

func decodeJSONArray(encoded string, target any) error {
	if !strings.HasPrefix(strings.TrimSpace(encoded), "[") {
		return errors.New("expected JSON array")
	}
	return decodeJSONValue(encoded, target)
}

func decodeJSONObject(encoded string, target any) error {
	if !strings.HasPrefix(strings.TrimSpace(encoded), "{") {
		return errors.New("expected JSON object")
	}
	return decodeJSONValue(encoded, target)
}

func decodeJSONValue(encoded string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(encoded))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
