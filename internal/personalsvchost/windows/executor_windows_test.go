// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package windows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

const helperProcessEnvironment = "POWERCONTEXT_WINDOWS_EXECUTOR_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperProcessEnvironment) != "" {
		runExecutorHelperProcess()
		return
	}
	m.Run()
}

func TestExecutorPassesArgumentsWithoutShell(t *testing.T) {
	argumentsPath := filepath.Join(t.TempDir(), "arguments.txt")
	t.Setenv(helperProcessEnvironment, "record-arguments")
	t.Setenv("POWERCONTEXT_WINDOWS_EXECUTOR_ARGUMENTS", argumentsPath)
	executor := executorForTest(t)

	result, err := executor.Execute(t.Context(), "schtasks.exe", []string{
		"/Query",
		"/TN",
		`\PowerContext Personal Server`,
		"argument with spaces",
		"&whoami",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("Execute() exit code = %#x, want 0", result.ExitCode)
	}
	if result.Output != nil {
		t.Fatalf("Execute() output = %q, want nil", result.Output)
	}

	recorded, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/Query", "/TN", `\PowerContext Personal Server`, "argument with spaces", "&whoami"}
	if got := strings.Split(string(recorded), "\n"); !slices.Equal(got, want) {
		t.Fatalf("helper arguments = %#v, want %#v", got, want)
	}
}

func TestExecutorReturnsOnlyBoundedStructuredQueryOutput(t *testing.T) {
	t.Setenv(helperProcessEnvironment, "structured-output")
	executor := executorForTest(t)

	for _, test := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{
			name:      "XML",
			arguments: []string{"/Query", "/TN", `\PowerContext Personal Server`, "/XML", "/HRESULT"},
			want:      "<Task />",
		},
		{
			name:      "CSV status",
			arguments: []string{"/Query", "/TN", `\PowerContext Personal Server`, "/FO", "CSV", "/NH", "/V", "/HRESULT"},
			want:      "structured,status",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := executor.Execute(t.Context(), "schtasks.exe", test.arguments)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(result.Output); got != test.want {
				t.Fatalf("Execute() output = %q, want %q", got, test.want)
			}
			if strings.Contains(string(result.Output), "secret stderr") {
				t.Fatal("Execute() returned native stderr")
			}
		})
	}
}

func TestExecutorPreservesExitCodeBitPattern(t *testing.T) {
	for _, test := range []struct {
		name string
		code uint32
	}{
		{name: "ordinary nonzero", code: 5},
		{name: "HRESULT with signed process representation", code: 0x80070002},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(helperProcessEnvironment, "exit")
			t.Setenv("POWERCONTEXT_WINDOWS_EXECUTOR_EXIT_CODE", strconv.FormatUint(uint64(test.code), 10))
			executor := executorForTest(t)

			result, err := executor.Execute(t.Context(), "schtasks.exe", []string{"/Query"})
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode != test.code {
				t.Fatalf("Execute() exit code = %#x, want %#x", result.ExitCode, test.code)
			}
			if result.Output != nil {
				t.Fatalf("Execute() output = %q, want nil", result.Output)
			}
		})
	}
}

func TestExecutorCancellationTerminatesCommand(t *testing.T) {
	startedPath := filepath.Join(t.TempDir(), "started")
	t.Setenv(helperProcessEnvironment, "block")
	t.Setenv("POWERCONTEXT_WINDOWS_EXECUTOR_STARTED", startedPath)
	executor := executorForTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	resultChannel := make(chan executeResult, 1)
	go func() {
		result, err := executor.Execute(ctx, "schtasks.exe", []string{"/Query"})
		resultChannel <- executeResult{result: result, err: err}
	}()

	waitForHelperStart(t, startedPath)
	cancel()

	select {
	case got := <-resultChannel:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("Execute() error = %v, want context cancellation", got.err)
		}
		assertExecutorResultRedacted(t, got.result, got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("Execute() did not return after cancellation")
	}
}

func TestExecutorMissingExecutableReturnsRedactedFailure(t *testing.T) {
	executor := newExecutor(filepath.Join(t.TempDir(), "missing-schtasks.exe"))

	result, err := executor.Execute(t.Context(), "schtasks.exe", []string{
		"/Create", "/XML", `C:\Users\person\secret-task.xml`,
	})
	if !errors.Is(err, errExecutorUnavailable) {
		t.Fatalf("Execute() error = %v, want unavailable error", err)
	}
	assertExecutorResultRedacted(t, result, err)
}

func TestExecutorOutputLimitFailsClosed(t *testing.T) {
	t.Setenv(helperProcessEnvironment, "overflow")
	executor := executorForTest(t)

	result, err := executor.Execute(t.Context(), "schtasks.exe", []string{"/Query", "/XML"})
	if !errors.Is(err, errExecutorOutputLimit) {
		t.Fatalf("Execute() error = %v, want output-limit error", err)
	}
	assertExecutorResultRedacted(t, result, err)
}

func TestExecutorOutputLimitTerminatesCommand(t *testing.T) {
	t.Setenv(helperProcessEnvironment, "overflow-block")
	executor := executorForTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	result, err := executor.Execute(ctx, "schtasks.exe", []string{"/Query", "/XML"})
	if !errors.Is(err, errExecutorOutputLimit) {
		t.Fatalf("Execute() error = %v, want output-limit error", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Execute() waited for caller cancellation: %v", ctx.Err())
	}
	assertExecutorResultRedacted(t, result, err)
}

func TestExecutorCombinedOutputLimitFailsClosed(t *testing.T) {
	t.Setenv(helperProcessEnvironment, "split-overflow")
	executor := executorForTest(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	result, err := executor.Execute(ctx, "schtasks.exe", []string{"/Query", "/XML"})
	if !errors.Is(err, errExecutorOutputLimit) {
		t.Fatalf("Execute() error = %v, want output-limit error", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Execute() waited for caller cancellation: %v", ctx.Err())
	}
	assertExecutorResultRedacted(t, result, err)
}

func TestExecutorAllowsExactCombinedOutputLimit(t *testing.T) {
	t.Setenv(helperProcessEnvironment, "split-at-limit")
	executor := executorForTest(t)

	result, err := executor.Execute(t.Context(), "schtasks.exe", []string{"/Query", "/XML"})
	if err != nil {
		t.Fatalf("Execute() error = %v, want nil at the combined output limit", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("Execute() exit code = %#x, want 0", result.ExitCode)
	}
	assertExecutorResultRedacted(t, result, err)
}

func TestExecutorInvalidBoundaryFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name     string
		executor Executor
		ctx      context.Context
		program  string
	}{
		{name: "zero value", ctx: t.Context(), program: "schtasks.exe"},
		{name: "nil context", executor: executorForTest(t), program: "schtasks.exe"},
		{name: "unexpected program", executor: executorForTest(t), ctx: t.Context(), program: "cmd.exe"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := test.executor.Execute(test.ctx, test.program, []string{"/Query"})
			if !errors.Is(err, errExecutorInvalid) {
				t.Fatalf("Execute() error = %v, want invalid-boundary error", err)
			}
			assertExecutorResultRedacted(t, result, err)
		})
	}
}

func TestNewExecutorIgnoresEnvironmentExecutableOverride(t *testing.T) {
	overrideRoot := t.TempDir()
	overrideDirectory := filepath.Join(overrideRoot, "System32")
	if err := os.Mkdir(overrideDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	overrideExecutable := filepath.Join(overrideDirectory, "schtasks.exe")
	if err := os.WriteFile(overrideExecutable, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SystemRoot", overrideRoot)

	executor, err := NewExecutor()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(executor.executable) {
		t.Fatal("NewExecutor() executable is not absolute")
	}
	if strings.EqualFold(executor.executable, overrideExecutable) {
		t.Fatal("NewExecutor() trusted the environment executable override")
	}
	if !strings.EqualFold(filepath.Base(executor.executable), "schtasks.exe") {
		t.Fatal("NewExecutor() executable has an unexpected basename")
	}
	rendered := fmt.Sprintf("%v\n%+v\n%#v", executor, executor, executor)
	if strings.Contains(rendered, executor.executable) {
		t.Fatal("formatted Executor disclosed its executable path")
	}
}

type executeResult struct {
	result personalsvc.TaskSchedulerResult
	err    error
}

func executorForTest(t *testing.T) Executor {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return newExecutor(executable)
}

func waitForHelperStart(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper process did not report startup")
}

func assertExecutorResultRedacted(t *testing.T, result personalsvc.TaskSchedulerResult, err error) {
	t.Helper()
	rendered := fmt.Sprintf("%v\n%+v\n%#v\n%#v", err, err, err, result)
	for _, protected := range []string{
		`C:\Users\person`,
		"secret-task.xml",
		"http://127.0.0.1:8123",
		"<Task",
		"PowerContext Personal Server",
	} {
		if strings.Contains(rendered, protected) {
			t.Fatalf("executor result contains protected value %q", protected)
		}
	}
	if result.Output != nil {
		t.Fatalf("Execute() output = %q, want nil", result.Output)
	}
}

func runExecutorHelperProcess() {
	switch os.Getenv(helperProcessEnvironment) {
	case "record-arguments":
		if err := os.WriteFile(os.Getenv("POWERCONTEXT_WINDOWS_EXECUTOR_ARGUMENTS"), []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
			os.Exit(120)
		}
	case "exit":
		code, err := strconv.ParseUint(os.Getenv("POWERCONTEXT_WINDOWS_EXECUTOR_EXIT_CODE"), 10, 32)
		if err != nil {
			os.Exit(121)
		}
		os.Exit(int(uint32(code)))
	case "block":
		if err := os.WriteFile(os.Getenv("POWERCONTEXT_WINDOWS_EXECUTOR_STARTED"), nil, 0o600); err != nil {
			os.Exit(122)
		}
		time.Sleep(24 * time.Hour)
	case "overflow":
		_, _ = os.Stdout.Write(bytesOf('x', maxCommandOutputBytes+1))
		_, _ = os.Stderr.WriteString(`C:\Users\person\secret-task.xml http://127.0.0.1:8123 <Task>`)
	case "overflow-block":
		_, _ = os.Stdout.Write(bytesOf('x', maxCommandOutputBytes+1))
		time.Sleep(24 * time.Hour)
	case "split-overflow":
		_, _ = os.Stdout.Write(bytesOf('x', maxCommandOutputBytes/2+1))
		_, _ = os.Stderr.Write(bytesOf('y', maxCommandOutputBytes/2+1))
	case "split-at-limit":
		_, _ = os.Stdout.Write(bytesOf('x', maxCommandOutputBytes/2))
		_, _ = os.Stderr.Write(bytesOf('y', maxCommandOutputBytes/2))
	case "structured-output":
		if slices.Contains(os.Args, "/XML") {
			_, _ = os.Stdout.WriteString("<Task />")
		} else {
			_, _ = os.Stdout.WriteString("structured,status")
		}
		_, _ = os.Stderr.WriteString("secret stderr")
	default:
		os.Exit(123)
	}
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}
