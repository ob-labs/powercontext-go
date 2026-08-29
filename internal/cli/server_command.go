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

package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	"github.com/ob-labs/powercontext-go/server"
)

type serverCommandRunner func(context.Context, *commandState, server.ProcessConfig) error

func newServerCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{Use: "server", Short: "Run a configured PowerContext service."}
	command.AddCommand(newServerRunCommand(state))
	return command
}

func newServerRunCommand(state *commandState) *cobra.Command {
	var host string
	var port int
	command := &cobra.Command{
		Use: "run", Short: "Run the HTTP and MCP service in the foreground.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			config, err := server.LoadConfig()
			if err != nil {
				return err
			}
			if command.Flags().Changed("host") {
				config.HTTP.Host = host
			}
			if command.Flags().Changed("port") {
				config.HTTP.Port = port
			}
			if err := config.Validate(); err != nil {
				return err
			}
			runner := state.serverRun
			if runner == nil {
				runner = runServer
			}
			return runner(command.Context(), state, config)
		},
	}
	command.Flags().StringVar(&host, "host", "", "Address to bind.")
	command.Flags().IntVar(&port, "port", 0, "Port to bind.")
	return command
}

func runServer(parent context.Context, state *commandState, config server.ProcessConfig) error {
	logger, err := serverlogging.New(serverlogging.Config{
		Level: loggingLevel(config.Logging.Level), Format: serverlogging.Format(config.Logging.Format), Writer: state.stdout,
	})
	if err != nil {
		return err
	}
	lifecycleLogger := serverlogging.Named(logger, "powercontext.server.factory")
	serverlogging.LogLifecycle(parent, lifecycleLogger, "server.starting", "PowerContext Server is starting")
	application, err := server.OpenApplication(parent, config, server.Dependencies{Logger: logger})
	if err != nil {
		return err
	}
	handler, err := application.HTTPHandler()
	if err != nil {
		_ = application.Close(context.Background())
		return err
	}
	httpServer := &http.Server{
		Addr: config.HTTP.Address(), Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	serveError := make(chan error, 1)
	go func() {
		serveError <- httpServer.ListenAndServe()
	}()
	var serveErr error
	select {
	case err := <-serveError:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = fmt.Errorf("server: listen: %w", err)
		}
	case <-ctx.Done():
	}
	serverlogging.LogLifecycle(context.Background(), lifecycleLogger, "server.stopping", "PowerContext Server is stopping")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	shutdownErr := httpServer.Shutdown(shutdownCtx)
	runtimeErr := application.Close(shutdownCtx)
	serverlogging.LogLifecycle(context.Background(), lifecycleLogger, "server.stopped", "PowerContext Server stopped")
	if parent.Err() != nil && !errors.Is(parent.Err(), context.Canceled) {
		return errors.Join(parent.Err(), serveErr, shutdownErr, runtimeErr)
	}
	return errors.Join(serveErr, shutdownErr, runtimeErr)
}

func loggingLevel(value string) slog.Level {
	switch value {
	case "DEBUG":
		return slog.LevelDebug
	case "WARNING":
		return slog.LevelWarn
	case "ERROR", "CRITICAL":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
