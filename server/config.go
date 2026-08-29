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
	"net"
	"strconv"
	"time"

	"github.com/ob-labs/powercontext-go/artifact/memory"
)

const (
	serverEnvironmentPrefix = "POWERCONTEXT_SERVER_"
	PowerContextHomeEnv     = "POWERCONTEXT_HOME"
	DefaultMCPPath          = "/mcp"
)

// ProcessConfig is the validated process-owned configuration. Domain packages
// intentionally never read it or inspect the environment.
type ProcessConfig struct {
	HTTP                            HTTPConfig
	MCP                             MCPConfig
	Auth                            AuthConfig
	AllowUnauthenticatedNonLoopback bool
	Dashboard                       DashboardConfig
	Logging                         LoggingConfig
	Metrics                         MetricsConfig
	Tracing                         TracingConfig
	Runtime                         RuntimeConfig
	Database                        DatabaseConfig
	HandoffReport                   HandoffReportConfig
	Inference                       InferenceConfig
	ExternalSkills                  ExternalSkillsConfig
	SchedulerPath                   string
}

type HTTPConfig struct {
	Host string
	Port int
}

// HTTPConfigOverride contains only command-line values that were explicitly
// provided and must be merged before ProcessConfig validation.
type HTTPConfigOverride struct {
	Host *string
	Port *int
}

func (c HTTPConfig) Address() string { return net.JoinHostPort(c.Host, strconv.Itoa(c.Port)) }

type MCPConfig struct {
	Enabled bool
	Path    string
}

type AuthConfig struct {
	Enabled bool
	Token   string `json:"-"`
}

func (c AuthConfig) String() string   { return c.redactedString() }
func (c AuthConfig) GoString() string { return c.redactedString() }

func (c AuthConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("enabled", c.Enabled),
		slog.Bool("token_configured", c.Token != ""),
	)
}

func (c AuthConfig) redactedString() string {
	token := "<unset>"
	if c.Token != "" {
		token = "<redacted>"
	}
	return fmt.Sprintf("{Enabled:%t Token:%s}", c.Enabled, token)
}

type DashboardScope struct {
	ScopeID     string `json:"scope_id"`
	DisplayName string `json:"display_name"`
}

type DashboardConfig struct {
	Enabled bool
	Scopes  []DashboardScope
}

type LoggingConfig struct {
	Level  string
	Format string
	Access bool
}

type (
	MetricsConfig struct{ Enabled bool }
	TracingConfig struct{ Enabled bool }
)

type RuntimeConfig struct {
	ScopeCacheSize               int
	SourceWindowLimit            int64
	MemoryExtractionProfile      memory.ExtractionProfile
	MemoryRerankEnabled          bool
	MemoryRerankCandidateLimit   int
	SourceWindowInterval         *time.Duration
	ExperienceIncubationInterval *time.Duration
}

type DatabaseConfig struct {
	Kind      string
	SQLite    SQLiteDatabaseConfig
	OceanBase OceanBaseDatabaseConfig
	SeekDB    SeekDBDatabaseConfig
}

type SQLiteDatabaseConfig struct {
	URL             string
	BusyTimeout     time.Duration
	JournalMode     string
	ForeignKeys     bool
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

type OceanBaseDatabaseConfig struct {
	URL          string
	MaxOpenConns int
	MaxIdleConns int
	MaxLifetime  time.Duration
}

type SeekDBDatabaseConfig struct {
	Path         string
	Database     string
	LibraryPath  string
	MaxOpenConns int
	MaxIdleConns int
	MaxLifetime  time.Duration
}

type HandoffReportConfig struct{ Enabled bool }

type InferenceConfig struct {
	GenerationModel        string
	GenerationTimeout      time.Duration
	GenerationMaxRequests  int
	EmbeddingModel         string
	EmbeddingProfileID     string
	EmbeddingDimension     int
	EmbeddingNormalization string
	EmbeddingTimeout       time.Duration
	EmbeddingBatchSize     int
}

type ExternalSkillRoot struct {
	RootID              string `json:"root_id"`
	InstallationScope   string `json:"installation_scope"`
	Path                string `json:"path"`
	AllowManagedPublish bool   `json:"allow_managed_publish"`
}

type ExternalSkillTarget struct {
	TargetID            string `json:"target_id"`
	AgentKind           string `json:"agent_kind"`
	InstallationScope   string `json:"installation_scope"`
	Path                string `json:"path"`
	AllowManagedPublish bool   `json:"allow_managed_publish"`
}

type ExternalSkillsConfig struct {
	HostID     string
	Targets    []ExternalSkillTarget
	CodexRoots []ExternalSkillRoot
}
