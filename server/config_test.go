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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/memory"
)

func TestLoadConfigMatchesFrozenServerEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv(PowerContextHomeEnv, home)
	t.Setenv("POWERCONTEXT_SERVER_EXTERNAL_SKILLS_HOST_ID", "")
	t.Setenv("POWERCONTEXT_SERVER_EXTERNAL_SKILLS_TARGETS", "")
	t.Setenv("POWERCONTEXT_SERVER_EXTERNAL_SKILLS_CODEX_ROOTS", "")
	t.Setenv("POWERCONTEXT_SERVER_HTTP_HOST", "127.0.0.2")
	t.Setenv("POWERCONTEXT_SERVER_HTTP_PORT", "9000")
	t.Setenv("POWERCONTEXT_SERVER_DATABASE_URL", "sqlite+aiosqlite:////var/lib/powercontext/test.db")
	t.Setenv("POWERCONTEXT_SERVER_RUNTIME_SCOPE_CACHE_SIZE", "64")
	t.Setenv("POWERCONTEXT_SERVER_RUNTIME_SOURCE_WINDOW_LIMIT", "25")
	t.Setenv("POWERCONTEXT_SERVER_RUNTIME_MEMORY_EXTRACTION_PROFILE", "conversation")
	t.Setenv("POWERCONTEXT_SERVER_RUNTIME_MEMORY_RERANK_ENABLED", "true")
	t.Setenv("POWERCONTEXT_SERVER_RUNTIME_MEMORY_RERANK_CANDIDATE_LIMIT", "40")
	t.Setenv("POWERCONTEXT_SERVER_RUNTIME_EXPERIENCE_SCHEDULE_SECONDS", "45")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_MODEL", " test ")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_TIMEOUT_SECONDS", "12.5")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_GENERATION_MAX_REQUESTS", "4")
	t.Setenv("POWERCONTEXT_SERVER_MCP_ENABLED", "false")
	t.Setenv("POWERCONTEXT_SERVER_MCP_PATH", "/context/")
	t.Setenv("POWERCONTEXT_SERVER_EXTERNAL_SKILLS", `{
		"host_id":"workstation-1",
		"future_compatible":true,
		"targets":[{
			"target_id":"codex-project",
			"agent_kind":"codex",
			"installation_scope":"project",
			"path":"/srv/project/.agents/skills",
			"allow_managed_publish":true,
			"future_compatible":true
		},{
			"target_id":"claude-user",
			"agent_kind":"claude_code",
			"installation_scope":"user",
			"path":"/home/example/.claude/skills"
		}]
	}`)

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	resolvedHome, err := absoluteExpandedPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if config.HTTP.Host != "127.0.0.2" || config.HTTP.Port != 9000 {
		t.Fatalf("HTTP config = %#v", config.HTTP)
	}
	if config.Database.Kind != "sqlite" || config.Database.SQLite.URL != "sqlite+aiosqlite:////var/lib/powercontext/test.db" {
		t.Fatalf("database config = %#v", config.Database)
	}
	if config.Runtime.SourceWindowLimit != 25 || config.Runtime.MemoryExtractionProfile != memory.ConversationProfile ||
		config.Runtime.ScopeCacheSize != 64 ||
		!config.Runtime.MemoryRerankEnabled || config.Runtime.MemoryRerankCandidateLimit != 40 ||
		config.Runtime.ExperienceIncubationInterval == nil || *config.Runtime.ExperienceIncubationInterval != 45*time.Second {
		t.Fatalf("Runtime config = %#v", config.Runtime)
	}
	if config.Inference.GenerationModel != "test" || config.Inference.GenerationTimeout != 12500*time.Millisecond ||
		config.Inference.GenerationMaxRequests != 4 {
		t.Fatalf("generation config = %#v", config.Inference)
	}
	if config.MCP.Enabled || config.MCP.Path != "/context" {
		t.Fatalf("MCP config = %#v", config.MCP)
	}
	if config.ExternalSkills.HostID != "workstation-1" || len(config.ExternalSkills.Targets) != 2 {
		t.Fatalf("external Skill config = %#v", config.ExternalSkills)
	}
	target := config.ExternalSkills.Targets[0]
	if target.TargetID != "codex-project" || target.AgentKind != "codex" || target.InstallationScope != "project" ||
		target.Path != "/srv/project/.agents/skills" || !target.AllowManagedPublish {
		t.Fatalf("external Skill target = %#v", target)
	}
	if config.ExternalSkills.Targets[1].AgentKind != "claude_code" ||
		config.ExternalSkills.Targets[1].Path != "/home/example/.claude/skills" {
		t.Fatalf("Claude Code target = %#v", config.ExternalSkills.Targets[1])
	}
	if config.SchedulerPath != filepath.Join(resolvedHome, "scheduler.db") {
		t.Fatalf("scheduler path = %q", config.SchedulerPath)
	}
	if !config.Dashboard.Enabled || len(config.Dashboard.Scopes) != 0 || !config.HandoffReport.Enabled {
		t.Fatalf("optional product defaults = Dashboard %#v, Handoff Report %#v", config.Dashboard, config.HandoffReport)
	}
}

func TestEnvExampleLoadsServerSettings(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(payload), "\n") {
		assignment := strings.TrimSpace(line)
		if assignment == "" || strings.HasPrefix(assignment, "#") {
			continue
		}
		name, value, found := strings.Cut(assignment, "=")
		if !found || name == "" {
			t.Fatalf("invalid example assignment %q", line)
		}
		t.Setenv(name, value)
	}

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := SQLiteDSN(config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	if config.Database.Kind != "sqlite" || dsn != filepath.Join(".powercontext", "powercontext.db") {
		t.Fatalf("example database = %#v, DSN %q", config.Database, dsn)
	}
	if config.Inference.EmbeddingDimension != 2560 || config.Inference.EmbeddingBatchSize != 10 {
		t.Fatalf("example inference = %#v", config.Inference)
	}
}

func TestDefaultConfigUsesPersistentUserStorage(t *testing.T) {
	home := filepath.Join(t.TempDir(), "powercontext-data")
	t.Setenv(PowerContextHomeEnv, home)

	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	resolvedHome, err := absoluteExpandedPath(home)
	if err != nil {
		t.Fatal(err)
	}
	wantDatabase := sqliteURL(filepath.Join(resolvedHome, "powercontext.db"))
	if config.Database.Kind != "sqlite" || config.Database.SQLite.URL != wantDatabase {
		t.Fatalf("database config = %#v, want SQLite %q", config.Database, wantDatabase)
	}
	if config.SchedulerPath != filepath.Join(resolvedHome, "scheduler.db") {
		t.Fatalf("scheduler path = %q", config.SchedulerPath)
	}
}

func TestLoadConfigSelectsOceanBase(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	const databaseURL = "mysql+aoceanbase://root:test@127.0.0.1:2881/powercontext?charset=utf8mb4"
	t.Setenv("POWERCONTEXT_SERVER_DATABASE_KIND", "oceanbase")
	t.Setenv("POWERCONTEXT_SERVER_DATABASE_URL", databaseURL)
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Database.Kind != "oceanbase" || config.Database.OceanBase.URL != databaseURL {
		t.Fatalf("database config = %#v", config.Database)
	}
}

func TestLoadConfigSelectsEmbeddedSeekDB(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "powercontext-data")
	t.Setenv(PowerContextHomeEnv, dataDir)
	t.Setenv("POWERCONTEXT_SERVER_DATABASE_KIND", "seekdb")

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	resolvedDataDir, err := absoluteExpandedPath(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if config.Database.Kind != "seekdb" || config.Database.SeekDB.Path != filepath.Join(resolvedDataDir, "seekdb") ||
		config.Database.SeekDB.Database != "test" {
		t.Fatalf("embedded seekDB config = %#v", config.Database.SeekDB)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configuration unexpectedly created the data directory: %v", err)
	}
}

func TestLoadConfigDefaultsBlankAndAcceptsExplicitSeekDBPath(t *testing.T) {
	for _, configured := range []string{"", "   ", filepath.Join(t.TempDir(), "custom-seekdb")} {
		t.Run(fmt.Sprintf("path-%q", configured), func(t *testing.T) {
			dataDir := filepath.Join(t.TempDir(), "powercontext-data")
			t.Setenv(PowerContextHomeEnv, dataDir)
			t.Setenv("POWERCONTEXT_SERVER_DATABASE_KIND", "seekdb")
			t.Setenv("POWERCONTEXT_SERVER_DATABASE_PATH", configured)
			config, err := LoadConfig()
			if err != nil {
				t.Fatal(err)
			}
			want := configured
			if strings.TrimSpace(want) == "" {
				want, err = absoluteExpandedPath(filepath.Join(dataDir, "seekdb"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if config.Database.SeekDB.Path != want {
				t.Fatalf("seekDB path = %q, want %q", config.Database.SeekDB.Path, want)
			}
		})
	}
}

func TestLoadConfigRejectsCustomSeekDBDatabase(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_DATABASE_KIND", "seekdb")
	t.Setenv("POWERCONTEXT_SERVER_DATABASE_DATABASE", "custom")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "must be test") {
		t.Fatalf("custom embedded seekDB database error = %v", err)
	}
}

func TestLoadConfigEmbeddingProfileAndDefaultsMatchPython(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_MODEL", " test-provider:test-model ")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_PROFILE_ID", " test-profile-v1 ")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_DIMENSION", "3")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_NORMALIZATION", " unit ")
	t.Setenv("POWERCONTEXT_SERVER_INFERENCE_EMBEDDING_BATCH_SIZE", "7")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Inference.EmbeddingModel != "test-provider:test-model" ||
		config.Inference.EmbeddingProfileID != "test-profile-v1" || config.Inference.EmbeddingDimension != 3 ||
		config.Inference.EmbeddingNormalization != "unit" || config.Inference.EmbeddingBatchSize != 7 {
		t.Fatalf("embedding config = %#v", config.Inference)
	}

	defaults, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Inference.EmbeddingNormalization != "unit" || defaults.Inference.EmbeddingBatchSize != 10 {
		t.Fatalf("embedding defaults = %#v", defaults.Inference)
	}
}

func TestProcessConfigRejectsInvalidAndPartialEmbeddingProfiles(t *testing.T) {
	for _, mutate := range []func(*ProcessConfig){
		func(config *ProcessConfig) { config.Inference.EmbeddingNormalization = "provider-default" },
		func(config *ProcessConfig) { config.Inference.EmbeddingModel = "provider:model" },
		func(config *ProcessConfig) {
			config.Inference.EmbeddingModel = "provider:model"
			config.Inference.EmbeddingProfileID = "profile-v1"
		},
	} {
		config, err := DefaultConfig()
		if err != nil {
			t.Fatal(err)
		}
		mutate(&config)
		if err := config.Validate(); err == nil {
			t.Fatalf("invalid embedding config was accepted: %#v", config.Inference)
		}
	}
}

func TestComponentConfigHasNoLegacySQLitePathSurface(t *testing.T) {
	t.Parallel()
	typeOfSQLite := reflect.TypeOf(SQLiteDatabaseConfig{})
	if _, found := typeOfSQLite.FieldByName("LegacyPath"); found {
		t.Fatal("SQLiteConfig exposes the removed legacy_path component setting")
	}
	if _, found := typeOfSQLite.FieldByName("Path"); found {
		t.Fatal("SQLiteConfig exposes a second path authority beside URL")
	}
}

func TestLoadConfigRejectsAmbiguousExternalSkillEnvironment(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_EXTERNAL_SKILLS", `{"host_id":"host","codex_roots":[]}`)
	t.Setenv("POWERCONTEXT_SERVER_EXTERNAL_SKILLS_HOST_ID", "other-host")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("mixed external Skill environment was accepted")
	}
}

func TestAuthenticationConfigurationRedactsTokenAcrossRepresentations(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_AUTH_ENABLED", "true")
	t.Setenv("POWERCONTEXT_SERVER_AUTH_TOKEN", "server-secret")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	auth := config.Auth
	if !auth.Enabled || auth.Token != "server-secret" {
		t.Fatalf("authentication config = %#v", auth)
	}
	for _, rendered := range []string{
		fmt.Sprintf("%v", auth),
		fmt.Sprintf("%+v", auth),
		fmt.Sprintf("%#v", auth),
		fmt.Sprintf("%v", ProcessConfig{Auth: auth}),
		fmt.Sprintf("%+v", ProcessConfig{Auth: auth}),
		fmt.Sprintf("%#v", ProcessConfig{Auth: auth}),
	} {
		if strings.Contains(rendered, auth.Token) {
			t.Fatalf("formatted Auth config leaked token: %s", rendered)
		}
	}
	encoded, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), auth.Token) || strings.Contains(string(encoded), "Token") {
		t.Fatalf("JSON Auth config leaked token: %s", encoded)
	}
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	logger.Info("configured", slog.Any("auth", auth))
	if strings.Contains(output.String(), auth.Token) || !strings.Contains(output.String(), `"token_configured":true`) {
		t.Fatalf("structured Auth config was not safely represented: %s", output.String())
	}
}

func TestLoadConfigRequiresBearerTokenWhenEnabled(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_AUTH_ENABLED", "true")
	t.Setenv("POWERCONTEXT_SERVER_AUTH_TOKEN", "")
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "bearer token is required") {
		t.Fatalf("authentication error = %v", err)
	}
}

func TestLoadConfigRejectsUnauthenticatedNonLoopbackBind(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_HTTP_HOST", "0.0.0.0")
	t.Setenv("POWERCONTEXT_SERVER_AUTH_ENABLED", "false")
	t.Setenv("POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK", "false")
	_, err := LoadConfig()
	if _, ok := errors.AsType[*UnauthenticatedNonLoopbackBindError](err); !ok {
		t.Fatalf("LoadConfig() error = %v, want UnauthenticatedNonLoopbackBindError", err)
	}
}

func TestLoadConfigAllowsAuthenticatedNonLoopbackBind(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_HTTP_HOST", "0.0.0.0")
	t.Setenv("POWERCONTEXT_SERVER_AUTH_ENABLED", "true")
	t.Setenv("POWERCONTEXT_SERVER_AUTH_TOKEN", "server-secret")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.HTTP.Host != "0.0.0.0" {
		t.Fatalf("HTTP host = %q", config.HTTP.Host)
	}
}

func TestLoadConfigAllowsExplicitUnauthenticatedNonLoopbackBind(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_HTTP_HOST", "0.0.0.0")
	t.Setenv("POWERCONTEXT_SERVER_AUTH_ENABLED", "false")
	t.Setenv("POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK", "true")
	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !config.AllowUnauthenticatedNonLoopback {
		t.Fatal("explicit unauthenticated non-loopback opt-in was not retained")
	}
}

func TestLoadConfigWithHTTPOverrideValidatesFinalBind(t *testing.T) {
	t.Run("safe override repairs unsafe environment", func(t *testing.T) {
		t.Setenv(PowerContextHomeEnv, t.TempDir())
		t.Setenv("POWERCONTEXT_SERVER_HTTP_HOST", "0.0.0.0")
		t.Setenv("POWERCONTEXT_SERVER_AUTH_ENABLED", "false")
		t.Setenv("POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK", "false")
		config, err := LoadConfigWithHTTPOverride(HTTPConfigOverride{Host: new("127.0.0.2")})
		if err != nil {
			t.Fatal(err)
		}
		if config.HTTP.Host != "127.0.0.2" {
			t.Fatalf("HTTP host = %q, want loopback override", config.HTTP.Host)
		}
	})

	t.Run("unsafe override replaces safe environment", func(t *testing.T) {
		t.Setenv(PowerContextHomeEnv, t.TempDir())
		t.Setenv("POWERCONTEXT_SERVER_HTTP_HOST", "127.0.0.1")
		t.Setenv("POWERCONTEXT_SERVER_AUTH_ENABLED", "false")
		t.Setenv("POWERCONTEXT_SERVER_ALLOW_UNAUTHENTICATED_NON_LOOPBACK", "false")
		_, err := LoadConfigWithHTTPOverride(HTTPConfigOverride{Host: new("0.0.0.0")})
		if _, ok := errors.AsType[*UnauthenticatedNonLoopbackBindError](err); !ok {
			t.Fatalf("LoadConfigWithHTTPOverride() error = %v, want UnauthenticatedNonLoopbackBindError", err)
		}
	})
}

func TestLoadConfigNormalizesLoggingSettings(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_LOGGING_LEVEL", "warning")
	t.Setenv("POWERCONTEXT_SERVER_LOGGING_FORMAT", "json")
	t.Setenv("POWERCONTEXT_SERVER_LOGGING_ACCESS", "false")

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Logging != (LoggingConfig{Level: "WARNING", Format: "json", Access: false}) {
		t.Fatalf("logging config = %#v", config.Logging)
	}
}

func TestLoadConfigNormalizesDashboardScopesLikeFrozenPydanticModel(t *testing.T) {
	t.Setenv(PowerContextHomeEnv, t.TempDir())
	t.Setenv("POWERCONTEXT_SERVER_DASHBOARD_SCOPES", `[
		{"scope_id":" scope-a ","display_name":" Primary "},
		{"scope_id":"scope-b","display_name":"Secondary"}
	]`)

	config, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := []DashboardScope{{ScopeID: "scope-a", DisplayName: "Primary"}, {ScopeID: "scope-b", DisplayName: "Secondary"}}
	if !reflect.DeepEqual(config.Dashboard.Scopes, want) {
		t.Fatalf("Dashboard scopes = %#v, want %#v", config.Dashboard.Scopes, want)
	}

	t.Setenv("POWERCONTEXT_SERVER_DASHBOARD_SCOPES", `[{"scope_id":"`+strings.Repeat("x", 256)+`","display_name":"name"}]`)
	if _, err := LoadConfig(); err == nil {
		t.Fatal("Dashboard accepted an input longer than the frozen pre-normalization limit")
	}
}

func TestProcessConfigEnforcesTrustAndInferenceBoundariesWithoutSecrets(t *testing.T) {
	t.Parallel()
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Database.SQLite.URL = sqliteURL(filepath.Join(t.TempDir(), "powercontext.db"))
	config.Dashboard.Enabled = true
	config.Dashboard.Scopes = nil
	if err := config.Validate(); err != nil {
		t.Fatalf("default public Dashboard was rejected: %v", err)
	}
	config.Auth = AuthConfig{Enabled: true, Token: "secret"}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.Inference.EmbeddingModel = "openai:text-embedding-3-small"
	if err := config.Validate(); err == nil {
		t.Fatal("partial embedding profile was accepted")
	}
}

func TestScheduledExperienceIncubationRequiresGenerationModel(t *testing.T) {
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	interval := time.Minute
	config.Runtime.ExperienceIncubationInterval = &interval
	config.Inference.GenerationModel = ""
	err = config.Validate()
	if err == nil || !strings.Contains(err.Error(), "scheduled Experience incubation requires a generation model") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestSQLiteDSNPreservesFrozenAbsoluteAndRelativePaths(t *testing.T) {
	t.Parallel()
	want := filepath.Join(t.TempDir(), "powercontext.db")
	got, err := SQLiteDSN(sqliteURL(want))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("DSN = %q, want %q", got, want)
	}
	relative, err := SQLiteDSN("sqlite+aiosqlite:///.powercontext/powercontext.db")
	if err != nil {
		t.Fatal(err)
	}
	if wantRelative := filepath.Join(".powercontext", "powercontext.db"); relative != wantRelative {
		t.Fatalf("relative DSN = %q, want %q", relative, wantRelative)
	}
	for _, invalid := range []string{"sqlite:///tmp/a.db", "sqlite+aiosqlite:///", ""} {
		if _, err := SQLiteDSN(invalid); err == nil {
			t.Fatalf("accepted invalid SQLite URL %q", invalid)
		}
	}
}

func TestProcessConfigValidatesOceanBaseURLWithoutLeakingPassword(t *testing.T) {
	t.Parallel()
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Database.Kind = "oceanbase"
	config.Database.OceanBase.URL = "mysql+pymysql://root:do-not-leak@127.0.0.1:2881/powercontext?charset=utf8mb4"
	err = config.Validate()
	if err == nil {
		t.Fatal("non-official OceanBase URL was accepted")
	}
	if strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("configuration error leaked password: %v", err)
	}
	config.Database.OceanBase.URL = "mysql+aoceanbase://root%40tenant:secret@127.0.0.1:2881/powercontext?charset=utf8mb4"
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}
