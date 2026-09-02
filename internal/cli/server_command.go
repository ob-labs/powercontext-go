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
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	"github.com/ob-labs/powercontext-go/server"
)

type serverCommandRunner func(context.Context, *commandState, server.ProcessConfig) error

var serverEnvironmentScopeMu sync.Mutex

type serverEnvironmentValue struct {
	value   string
	present bool
}

func newServerCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{Use: "server", Short: "Run a configured PowerContext service."}
	command.AddCommand(newServerRunCommand(state))
	return command
}

func newServerRunCommand(state *commandState) *cobra.Command {
	var envFile string
	var host string
	var port int
	command := &cobra.Command{
		Use: "run", Short: "Run the HTTP and MCP service in the foreground.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			run := func() error {
				override := server.HTTPConfigOverride{}
				if command.Flags().Changed("host") {
					trimmedHost := strings.TrimSpace(host)
					if trimmedHost == "" || host != trimmedHost {
						return usageError(errors.New("server: --host must be a non-empty trimmed value"))
					}
					override.Host = new(host)
				}
				if command.Flags().Changed("port") {
					override.Port = new(port)
				}
				config, err := server.LoadConfigWithHTTPOverride(override)
				if err != nil {
					if _, ok := errors.AsType[*server.UnauthenticatedNonLoopbackBindError](err); ok {
						return usageError(err)
					}
					if _, ok := errors.AsType[*server.AuthenticationTokenRequiredError](err); ok {
						return usageError(fmt.Errorf(
							"server: set POWERCONTEXT_SERVER_AUTH_TOKEN or POWERCONTEXT_SERVER_AUTH_ENABLED=false: %w",
							err,
						))
					}
					return err
				}
				runner := state.serverRun
				if runner == nil {
					runner = runServer
				}
				return runner(command.Context(), state, config)
			}
			if !command.Flags().Changed("env-file") {
				return run()
			}
			values, err := loadServerEnvironmentFile(envFile)
			if err != nil {
				return err
			}
			return withServerEnvironment(values, run)
		},
	}
	command.Flags().StringVar(&envFile, "env-file", "", "Load Server and provider settings from this environment file.")
	command.Flags().StringVar(&host, "host", "", "Address to bind.")
	command.Flags().IntVar(&port, "port", 0, "Port to bind.")
	return command
}

func loadServerEnvironmentFile(path string) (map[string]string, error) {
	content, exists, err := readConfigDocument(path)
	if err != nil {
		return nil, usageError(fmt.Errorf("server: invalid --env-file: %w", err))
	}
	if !exists {
		return nil, usageError(errors.New("server: --env-file does not exist"))
	}
	values, err := parseConfigEnvironment(content)
	if err != nil {
		return nil, usageError(fmt.Errorf("server: invalid --env-file: %w", err))
	}
	return values, nil
}

func withServerEnvironment(values map[string]string, run func() error) (err error) {
	serverEnvironmentScopeMu.Lock()
	defer serverEnvironmentScopeMu.Unlock()

	loadedBefore := make(map[string]serverEnvironmentValue, len(values))
	for name := range values {
		value, present := os.LookupEnv(name)
		loadedBefore[name] = serverEnvironmentValue{value: value, present: present}
	}
	serverBefore := make(map[string]string)
	for _, item := range os.Environ() {
		name, value, found := strings.Cut(item, "=")
		if found && strings.HasPrefix(name, "POWERCONTEXT_SERVER_") {
			serverBefore[name] = value
		}
	}
	defer func() {
		err = errors.Join(err, restoreServerEnvironment(loadedBefore, serverBefore))
	}()

	for name := range serverBefore {
		if unsetErr := os.Unsetenv(name); unsetErr != nil {
			return fmt.Errorf("server: clear environment: %w", unsetErr)
		}
	}
	for name, value := range values {
		if setErr := os.Setenv(name, value); setErr != nil {
			return fmt.Errorf("server: load environment: %w", setErr)
		}
	}
	return run()
}

func restoreServerEnvironment(loadedBefore map[string]serverEnvironmentValue, serverBefore map[string]string) error {
	var err error
	for name, original := range loadedBefore {
		if original.present {
			err = errors.Join(err, os.Setenv(name, original.value))
		} else {
			err = errors.Join(err, os.Unsetenv(name))
		}
	}
	for name, value := range serverBefore {
		if _, loaded := loadedBefore[name]; !loaded {
			err = errors.Join(err, os.Setenv(name, value))
		}
	}
	return err
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
