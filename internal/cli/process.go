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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

const maximumCommandOutput = 1 << 20

// systemCommandExecutor is the narrow process boundary used by setup and
// doctor. Keeping lookup and execution together lets tests prove exact argv
// contracts without teaching the rest of the CLI about subprocesses.
type systemCommandExecutor interface {
	LookPath(string) (string, error)
	Run(context.Context, string, ...string) ([]byte, error)
	RunEnv(context.Context, map[string]string, string, ...string) ([]byte, error)
	RunTimeout(context.Context, time.Duration, string, ...string) ([]byte, error)
}

type processCommandExecutor struct{}

func (processCommandExecutor) LookPath(name string) (string, error) { return exec.LookPath(name) }

func (processCommandExecutor) Run(ctx context.Context, executable string, arguments ...string) ([]byte, error) {
	return runProcessCommandWithEnvironment(ctx, nil, executable, arguments...)
}

func (processCommandExecutor) RunEnv(
	ctx context.Context,
	environment map[string]string,
	executable string,
	arguments ...string,
) ([]byte, error) {
	return runProcessCommandWithEnvironment(ctx, environment, executable, arguments...)
}

func (processCommandExecutor) RunTimeout(
	ctx context.Context,
	timeout time.Duration,
	executable string,
	arguments ...string,
) ([]byte, error) {
	return runProcessCommandWithOptions(ctx, timeout, nil, executable, arguments...)
}

func runJSONCommand(
	ctx context.Context,
	commands systemCommandExecutor,
	executable string,
	arguments ...string,
) (map[string]any, error) {
	output, err := commands.Run(ctx, executable, append(arguments, "--json")...)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if json.Unmarshal(output, &result) != nil || result == nil {
		return nil, errors.New("external command returned invalid JSON")
	}
	return result, nil
}

func runProcessCommandWithEnvironment(
	parent context.Context,
	environment map[string]string,
	executable string,
	arguments ...string,
) ([]byte, error) {
	return runProcessCommandWithOptions(parent, 120*time.Second, environment, executable, arguments...)
}

func runProcessCommandWithOptions(
	parent context.Context,
	timeout time.Duration,
	environment map[string]string,
	executable string,
	arguments ...string,
) ([]byte, error) {
	if timeout <= 0 {
		return nil, errors.New("external command timeout must be positive")
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, arguments...)
	if len(environment) > 0 {
		keys := make([]string, 0, len(environment))
		for key := range environment {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		command.Env = os.Environ()
		for _, key := range keys {
			if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(environment[key], '\x00') {
				return nil, errors.New("invalid external command environment")
			}
			command.Env = append(command.Env, key+"="+environment[key])
		}
	}
	var stdout, stderr boundedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	if stdout.exceeded || stderr.exceeded {
		return nil, errors.New("external command output exceeded 1 MiB")
	}
	if runErr != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			if ctx.Err() != nil {
				detail = ctx.Err().Error()
			} else {
				detail = runErr.Error()
			}
		}
		return nil, fmt.Errorf("`%s` failed: %s", strings.Join(append([]string{executable}, arguments...), " "), detail)
	}
	return stdout.Bytes(), nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	remaining := maximumCommandOutput - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = true
		return original, nil
	}
	if len(value) > remaining {
		value = value[:remaining]
		b.exceeded = true
	}
	_, _ = b.buffer.Write(value)
	return original, nil
}

func (b *boundedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *boundedBuffer) String() string { return b.buffer.String() }

func requiredJSONText(value map[string]any, name string) (string, error) {
	result, ok := value[name].(string)
	if !ok || result == "" {
		return "", fmt.Errorf("external command did not return %s", name)
	}
	return result, nil
}
