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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	servermetrics "github.com/ob-labs/powercontext-go/internal/observability/metrics"
	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/internal/scheduler"
)

func configuredMetrics(enabled bool) (*servermetrics.Server, error) {
	if !enabled {
		return nil, nil
	}
	return servermetrics.New()
}

func warnIfEphemeralMainDatabase(ctx context.Context, config ProcessConfig, logger *slog.Logger) {
	if logger == nil || config.Database.Kind != "sqlite" {
		return
	}
	dsn, err := SQLiteDSN(config.Database.SQLite.URL)
	if err != nil || (dsn != ":memory:" && !strings.Contains(dsn, "mode=memory")) {
		return
	}
	serverlogging.LogSafely(
		ctx,
		namedLogger(logger, "powercontext.server.factory"),
		slog.LevelWarn,
		"PowerContext is using an in-memory main database; all main database data will be lost when the process stops",
		slog.String("event", "database.ephemeral"),
		slog.String("outcome", "warning"),
		slog.String("unit", "database"),
	)
}

func scopedIDFactory(kind string) (string, error) {
	if kind == memory.Family {
		return pcruntime.DefaultMemoryArtifactID, nil
	}
	prefixes := map[string]string{
		"candidate": "cand", "entry": "mem_ent", "version": "mem_ver",
		experience.Family: "exp", skill.Family: "skill",
	}
	prefix, ok := prefixes[kind]
	if !ok {
		return "", fmt.Errorf("server: unsupported identity kind %q", kind)
	}
	return prefix + "_" + strings.ReplaceAll(uuid.NewString(), "-", ""), nil
}

func scheduledObserver(logger *slog.Logger) pcruntime.ScheduledObserver {
	if logger == nil {
		return nil
	}
	return func(ctx context.Context, value pcruntime.ScheduledObservation) {
		level, message := slog.LevelInfo, "Scheduled background processing completed"
		if value.Err != nil {
			level, message = slog.LevelError, "Scheduled background processing failed"
		}
		attributes := []slog.Attr{
			slog.String("event", "background.operation.completed"),
			slog.String("operation", value.Operation),
			slog.String("outcome", value.Outcome),
			slog.String("unit", "background"),
			slog.Float64("duration_ms", float64(value.Duration)/float64(time.Millisecond)),
			slog.Int("source_count", value.SourceCount),
		}
		if value.CandidateCount != nil {
			attributes = append(attributes, slog.Int("candidate_count", *value.CandidateCount))
		}
		serverlogging.LogSafely(ctx, logger, level, message, attributes...)
	}
}

func scheduledRunErrorObserver(logger *slog.Logger) func(scheduler.RunError) {
	if logger == nil {
		return nil
	}
	return func(value scheduler.RunError) {
		if errors.Is(value.Err, context.Canceled) {
			return
		}
		serverlogging.LogSafely(
			context.Background(), logger, slog.LevelError, "Scheduled background dispatch failed",
			slog.String("event", "background.dispatch.failed"), slog.String("operation", string(value.Kind)),
			slog.String("outcome", "failure"), slog.String("unit", "background"), slog.String("error_code", "scheduler"),
		)
	}
}

func namedLogger(logger *slog.Logger, name string) *slog.Logger {
	if logger == nil {
		return nil
	}
	return serverlogging.Named(logger, name)
}
