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
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
	golangwindows "golang.org/x/sys/windows"
)

const (
	taskSchedulerProgram  = "schtasks.exe"
	maxCommandOutputBytes = 1 << 20
)

var (
	errExecutorInvalid     = errors.New("Windows Task Scheduler executor is invalid")
	errExecutorUnavailable = errors.New("Windows Task Scheduler executable is unavailable")
	errExecutorOutputLimit = errors.New("Windows Task Scheduler command output exceeded the limit")
)

// Executor runs the fixed Task Scheduler program without a command shell.
// It must be constructed with NewExecutor.
type Executor struct {
	executable string
}

var _ personalsvc.TaskSchedulerExecutor = Executor{}

// String returns a content-free description of the executor.
func (Executor) String() string { return "Windows Task Scheduler executor" }

// GoString returns a content-free Go-syntax description of the executor.
func (Executor) GoString() string { return "windows.Executor{}" }

// NewExecutor resolves the Windows system copy of schtasks.exe to an absolute
// path before any command is run.
func NewExecutor() (Executor, error) {
	systemDirectory, err := golangwindows.GetSystemDirectory()
	if err != nil || !filepath.IsAbs(systemDirectory) {
		return Executor{}, errExecutorUnavailable
	}
	executable := filepath.Join(systemDirectory, taskSchedulerProgram)
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() {
		return Executor{}, errExecutorUnavailable
	}
	return newExecutor(executable), nil
}

func newExecutor(executable string) Executor {
	return Executor{executable: filepath.Clean(executable)}
}

// Execute runs one argv vector from the portable Task Scheduler contract.
// Command output is drained under a fixed combined limit but never returned.
func (e Executor) Execute(
	ctx context.Context,
	program string,
	arguments []string,
) (personalsvc.TaskSchedulerResult, error) {
	if ctx == nil || program != taskSchedulerProgram || !filepath.IsAbs(e.executable) {
		return personalsvc.TaskSchedulerResult{}, errExecutorInvalid
	}

	commandContext, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &boundedOutput{limit: maxCommandOutputBytes, cancel: cancel}
	command := exec.CommandContext(commandContext, e.executable, arguments...)
	command.Stdout = output
	command.Stderr = output
	runErr := command.Run()
	if contextErr := ctx.Err(); contextErr != nil {
		return personalsvc.TaskSchedulerResult{}, contextErr
	}
	if output.Exceeded() {
		return personalsvc.TaskSchedulerResult{}, errExecutorOutputLimit
	}
	if runErr == nil {
		return personalsvc.TaskSchedulerResult{}, nil
	}
	if exitError, matched := errors.AsType[*exec.ExitError](runErr); matched {
		return personalsvc.TaskSchedulerResult{ExitCode: uint32(exitError.ExitCode())}, nil
	}
	return personalsvc.TaskSchedulerResult{}, errExecutorUnavailable
}

type boundedOutput struct {
	mu       sync.Mutex
	limit    int
	written  int
	exceeded bool
	cancel   context.CancelFunc
}

func (w *boundedOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	remaining := max(0, w.limit-w.written)
	accepted := min(remaining, len(data))
	w.written += accepted
	exceeded := accepted != len(data)
	if exceeded {
		w.exceeded = true
	}
	w.mu.Unlock()
	if exceeded && w.cancel != nil {
		w.cancel()
	}
	return len(data), nil
}

func (w *boundedOutput) Exceeded() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.exceeded
}
