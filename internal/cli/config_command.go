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
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ob-labs/powercontext-go/server"
)

const (
	configManagedBegin = "# >>> powercontext managed configuration >>>"
	configManagedEnd   = "# <<< powercontext managed configuration <<<"
)

var configEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func newConfigCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{Use: "config", Short: "Create, inspect, and validate PowerContext environment files.", Args: cobra.NoArgs}
	command.AddCommand(newConfigInitCommand(state), newConfigShowCommand(state), newConfigValidateCommand(state))
	return command
}

func newConfigInitCommand(state *commandState) *cobra.Command {
	var output string
	var force, nonInteractive bool
	command := &cobra.Command{
		Use: "init", Short: "Create a managed PowerContext environment file.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			path, err := configOutputPath(output)
			if err != nil {
				return usageError(err)
			}
			content, exists, err := readConfigDocument(path)
			if err != nil {
				return err
			}
			if exists && !force {
				return usageError(errors.New("configuration file already exists; use --force to replace the managed block"))
			}
			values, err := defaultConfigEnvironment(nonInteractive)
			if err != nil {
				return err
			}
			credential, err := promptConfigCredential(command.InOrStdin(), state.stderr, nonInteractive)
			if err != nil {
				return err
			}
			if credential != "" {
				values["OPENROUTER_API_KEY"] = credential
			}
			document, err := updateConfigDocument(content, values)
			if err != nil {
				return usageError(err)
			}
			if writeErr := writeFileAtomically(path, []byte(document), 0o600); writeErr != nil {
				return errors.New("cannot write configuration file")
			}
			if state.json {
				return writeJSON(state.stdout, map[string]string{"path": path, "status": "initialized"})
			}
			_, err = fmt.Fprintln(state.stdout, "PowerContext configuration initialized.")
			return err
		},
	}
	command.Flags().StringVarP(&output, "output", "o", ".env", "Environment file to create.")
	command.Flags().BoolVar(&force, "force", false, "Replace the existing PowerContext managed block.")
	command.Flags().BoolVar(&nonInteractive, "non-interactive", false, "Generate defaults without prompting for a credential.")
	return command
}

func newConfigShowCommand(state *commandState) *cobra.Command {
	var envFile string
	command := &cobra.Command{
		Use: "show", Short: "Print environment assignments with credentials redacted.", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			content, exists, err := readConfigDocument(envFile)
			if err != nil {
				return err
			}
			if !exists {
				return usageError(errors.New("configuration file does not exist"))
			}
			values, err := parseConfigEnvironment(content)
			if err != nil {
				return usageError(err)
			}
			credentials, err := managedCredentialNames(content)
			if err != nil {
				return usageError(err)
			}
			keys := slices.Sorted(maps.Keys(values))
			if state.json {
				view := make(map[string]string, len(values))
				for _, key := range keys {
					view[key] = redactedConfigValue(key, values[key], credentials)
				}
				return writeJSON(state.stdout, view)
			}
			for _, key := range keys {
				if _, err := fmt.Fprintf(state.stdout, "%s=%s\n", key, redactedConfigValue(key, values[key], credentials)); err != nil {
					return err
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&envFile, "env-file", ".env", "Environment file to inspect.")
	return command
}

func newConfigValidateCommand(state *commandState) *cobra.Command {
	var envFile string
	command := &cobra.Command{
		Use: "validate", Short: "Validate environment syntax, persistence paths, and Server settings.", Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			content, exists, err := readConfigDocument(envFile)
			if err != nil {
				return err
			}
			if !exists {
				return usageError(errors.New("configuration file does not exist"))
			}
			values, err := parseConfigEnvironment(content)
			if err != nil {
				return usageError(err)
			}
			if persistenceErr := validateConfigPersistence(values); persistenceErr != nil {
				return usageError(persistenceErr)
			}
			if serverValidationErr := validateServerEnvironment(values); serverValidationErr != nil {
				return usageError(serverValidationErr)
			}
			if state.json {
				return writeJSON(state.stdout, map[string]string{"status": "valid"})
			}
			_, err = fmt.Fprintln(state.stdout, "PowerContext configuration is valid.")
			return err
		},
	}
	command.Flags().StringVar(&envFile, "env-file", ".env", "Environment file to validate.")
	return command
}

func configOutputPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("configuration output path must not be blank")
	}
	return filepath.Abs(value)
}

func defaultConfigEnvironment(_ bool) (map[string]string, error) {
	defaults, err := server.DefaultConfig()
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"POWERCONTEXT_SERVER_HTTP_HOST":                   defaults.HTTP.Host,
		"POWERCONTEXT_SERVER_HTTP_PORT":                   strconv.Itoa(defaults.HTTP.Port),
		"POWERCONTEXT_SERVER_MCP_ENABLED":                 strconv.FormatBool(defaults.MCP.Enabled),
		"POWERCONTEXT_SERVER_MCP_PATH":                    defaults.MCP.Path,
		"POWERCONTEXT_SERVER_AUTH_ENABLED":                strconv.FormatBool(defaults.Auth.Enabled),
		"POWERCONTEXT_SERVER_DATABASE_KIND":               "sqlite",
		"POWERCONTEXT_SERVER_DATABASE_URL":                defaults.Database.SQLite.URL,
		"POWERCONTEXT_SERVER_RUNTIME_SOURCE_WINDOW_LIMIT": strconv.FormatInt(defaults.Runtime.SourceWindowLimit, 10),
		"OPENROUTER_API_KEY":                              "",
	}, nil
}

func promptConfigCredential(input io.Reader, output io.Writer, nonInteractive bool) (string, error) {
	if nonInteractive {
		return "", nil
	}
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", nil
	}
	if output == nil {
		output = os.Stderr
	}
	if _, err := fmt.Fprint(output, "OPENROUTER_API_KEY: "); err != nil {
		return "", err
	}
	value, err := term.ReadPassword(int(file.Fd()))
	if _, writeErr := fmt.Fprintln(output); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		return "", errors.New("cannot read credential input")
	}
	return strings.TrimSpace(string(value)), nil
}

func readConfigDocument(path string) (string, bool, error) {
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, errors.New("cannot read configuration file")
	}
	return string(payload), true, nil
}

func updateConfigDocument(content string, values map[string]string) (string, error) {
	beginCount := strings.Count(content, configManagedBegin)
	endCount := strings.Count(content, configManagedEnd)
	if beginCount != endCount || beginCount > 1 {
		return "", errors.New("environment contains mismatched or repeated PowerContext managed markers")
	}
	block := renderConfigBlock(values)
	if beginCount == 1 {
		begin := strings.Index(content, configManagedBegin)
		end := strings.Index(content[begin:], configManagedEnd)
		if end < 0 {
			return "", errors.New("environment contains mismatched PowerContext managed markers")
		}
		end += begin + len(configManagedEnd)
		return joinConfigDocument(content[:begin], block, content[end:]), nil
	}
	return joinConfigDocument(content, block), nil
}

func renderConfigBlock(values map[string]string) string {
	keys := slices.Sorted(maps.Keys(values))
	lines := []string{configManagedBegin, "# config-version=1", "# credentials=OPENROUTER_API_KEY"}
	for _, key := range keys {
		lines = append(lines, key+"="+strconv.Quote(values[key]))
	}
	lines = append(lines, configManagedEnd)
	return strings.Join(lines, "\n")
}

func joinConfigDocument(parts ...string) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.Trim(part, "\r\n"); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return strings.Join(values, "\n\n") + "\n"
}

func parseConfigEnvironment(content string) (map[string]string, error) {
	values := make(map[string]string)
	for lineNumber, raw := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || line == configManagedBegin || line == configManagedEnd {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, value, found := strings.Cut(line, "=")
		if !found || !configEnvironmentName.MatchString(name) {
			return nil, fmt.Errorf("invalid environment assignment at line %d", lineNumber+1)
		}
		if _, exists := values[name]; exists {
			return nil, fmt.Errorf("duplicate environment assignment: %s", name)
		}
		decoded, err := decodeConfigValue(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("invalid environment assignment at line %d", lineNumber+1)
		}
		values[name] = decoded
	}
	return values, nil
}

func decodeConfigValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "'") {
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", errors.New("unterminated single-quoted value")
		}
		return value[1 : len(value)-1], nil
	}
	if strings.HasPrefix(value, "\"") {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", err
		}
		return decoded, nil
	}
	if index := strings.Index(value, " #"); index >= 0 {
		value = strings.TrimSpace(value[:index])
	}
	return value, nil
}

func managedCredentialNames(content string) (map[string]bool, error) {
	begin := strings.Index(content, configManagedBegin)
	end := strings.Index(content, configManagedEnd)
	if begin < 0 && end < 0 {
		return map[string]bool{}, nil
	}
	if begin < 0 || end < begin {
		return nil, errors.New("environment contains mismatched PowerContext managed markers")
	}
	credentials := make(map[string]bool)
	for _, line := range strings.Split(content[begin:end], "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "# credentials=") {
			continue
		}
		for _, name := range strings.Split(strings.TrimPrefix(line, "# credentials="), ",") {
			if name = strings.TrimSpace(name); name != "" {
				credentials[name] = true
			}
		}
	}
	return credentials, nil
}

func redactedConfigValue(name, value string, credentials map[string]bool) string {
	upper := strings.ToUpper(name)
	if credentials[name] || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "KEY") || strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "SECRET") {
		return "<redacted>"
	}
	return value
}

func validateConfigPersistence(values map[string]string) error {
	if values["POWERCONTEXT_SERVER_DATABASE_KIND"] == "seekdb" {
		if path := strings.TrimSpace(values["POWERCONTEXT_SERVER_DATABASE_PATH"]); path != "" && !filepath.IsAbs(filepath.FromSlash(path)) {
			return errors.New("seekDB path must be absolute")
		}
		return nil
	}
	url := values["POWERCONTEXT_SERVER_DATABASE_URL"]
	const prefix = "sqlite+aiosqlite:///"
	if !strings.HasPrefix(url, prefix) {
		return errors.New("SQLite URL must use sqlite+aiosqlite")
	}
	path := strings.TrimPrefix(url, prefix)
	if path != ":memory:" && !filepath.IsAbs(filepath.FromSlash(path)) {
		return errors.New("SQLite URL must use an absolute database path")
	}
	return nil
}

func validateServerEnvironment(values map[string]string) error {
	previous := make(map[string]string)
	for _, item := range os.Environ() {
		name, value, found := strings.Cut(item, "=")
		if found && strings.HasPrefix(name, "POWERCONTEXT_SERVER_") {
			previous[name] = value
			_ = os.Unsetenv(name)
		}
	}
	defer func() {
		for name := range values {
			if strings.HasPrefix(name, "POWERCONTEXT_SERVER_") {
				_ = os.Unsetenv(name)
			}
		}
		for name, value := range previous {
			_ = os.Setenv(name, value)
		}
	}()
	for name, value := range values {
		if strings.HasPrefix(name, "POWERCONTEXT_SERVER_") {
			if err := os.Setenv(name, value); err != nil {
				return errors.New("cannot load configuration environment")
			}
		}
	}
	if _, err := server.LoadConfig(); err != nil {
		return errors.New("Server configuration is invalid")
	}
	return nil
}
