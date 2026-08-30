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

package downstream_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/client"
)

const (
	downstreamScope           = "project:downstream-consumer"
	downstreamSourceID        = "downstream-work-boundary"
	downstreamReceiptSourceID = "downstream-receipt"
	maximumServerLogBytes     = 32 * 1024
)

func TestPublicClientCompletesCurrentWorkHandoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	serverURL := startServer(t, ctx)
	api, err := client.New(serverURL, client.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("create public client: %v", err)
	}

	if _, createErr := api.CreateWorkContract(ctx, &v1.CreateWorkContractRequest{
		ScopeID:  downstreamScope,
		SourceID: "downstream-contract",
		Contract: v1.WorkContract{
			Schema:             v1.WorkContractSchemaPowercontextWorkContractV1,
			Trust:              v1.WorkContractTrustUntrustedInput,
			Objective:          "Prove that an external Go module can complete a public Handoff.",
			Facts:              []v1.WorkClaim{},
			InScope:            []string{"exercise the public Go client"},
			Exclusions:         []string{},
			CompletionCriteria: []string{"acknowledge the committed Handoff"},
			AuthorizationNotes: []string{},
			OpenQuestions:      []string{},
		},
	}); createErr != nil {
		t.Fatalf("create work contract through public client: %v", createErr)
	}
	preparedResult, err := api.HandoffCurrentWork(ctx, currentWorkHandoffRequest())
	if err != nil {
		t.Fatalf("prepare current-work Handoff through public client: %v", err)
	}
	prepared, ok := preparedResult.(*v1.PreparedWorkHandoffHeaders)
	if !ok {
		t.Fatalf("prepare current-work Handoff response = %T", preparedResult)
	}

	committedResult, err := api.CommitHandoff(ctx, &v1.CommitHandoffRequest{
		ScopeID: downstreamScope,
		Handoff: prepared.Response.Handoff,
	})
	if err != nil {
		t.Fatalf("commit Handoff through public client: %v", err)
	}
	committed, ok := committedResult.(*v1.CommittedHandoffHeaders)
	if !ok {
		t.Fatalf("commit Handoff response = %T", committedResult)
	}

	acknowledgedResult, err := api.AcknowledgeHandoff(ctx, &v1.AcknowledgeHandoffRequest{
		ScopeID:   downstreamScope,
		SourceID:  downstreamReceiptSourceID,
		Receiver:  "downstream-consumer",
		Status:    v1.HandoffReceiptStatusAccepted,
		Selection: v1.HandoffAcknowledgementSelectionExact,
		ReceiverChecks: v1.NewOptNilReceiverChecks(v1.ReceiverChecks{
			LiveState:     v1.LiveStateCheckStatusConfirmed,
			Capability:    v1.ReceiverReadinessCheckStatusConfirmed,
			Authorization: v1.ReceiverReadinessCheckStatusConfirmed,
		}),
		Revision: v1.NewOptNilArtifactReference(committed.Response.Reference),
	})
	if err != nil {
		t.Fatalf("acknowledge Handoff through public client: %v", err)
	}
	if _, ok := acknowledgedResult.(*v1.HandoffAcknowledgementHeaders); !ok {
		t.Fatalf("acknowledge Handoff response = %T", acknowledgedResult)
	}
}

func TestCurrentWorkHandoffRequestUsesPublicContract(t *testing.T) {
	if err := currentWorkHandoffRequest().Validate(); err != nil {
		t.Fatalf("current-work Handoff request violates the public contract: %v", err)
	}
}

func TestPublicClientMemoryPersistsAcrossGracefulServerRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	home := t.TempDir()
	first, firstURL := startServerWithHome(t, ctx, home)
	firstClient, err := client.New(firstURL, client.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("create first public client: %v", err)
	}
	if _, rememberErr := firstClient.RememberMemory(ctx, &v1.RememberMemoryRequest{}); rememberErr == nil {
		t.Fatal("empty Memory request was accepted")
	} else {
		var validationErr *client.RequestValidationError
		if !errors.As(rememberErr, &validationErr) || validationErr.Path != "/v1/memory/remember" {
			t.Fatalf("empty Memory request error = %#v", rememberErr)
		}
	}

	rememberedResult, err := firstClient.RememberMemory(ctx, &v1.RememberMemoryRequest{
		ScopeID: downstreamScope,
		Kind:    "fact",
		Text:    "A public client persisted this Memory entry.",
	})
	if err != nil {
		t.Fatalf("remember Memory through public client: %v", err)
	}
	if _, ok := rememberedResult.(*v1.MemoryMutationResponseHeaders); !ok {
		t.Fatalf("remember Memory response = %T", rememberedResult)
	}
	stopServer(t, first)

	second, secondURL := startServerWithHome(t, ctx, home)
	t.Cleanup(func() { stopServer(t, second) })
	secondClient, err := client.New(secondURL, client.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("create restarted public client: %v", err)
	}
	listedResult, err := secondClient.ListMemoryEntries(ctx, &v1.ListMemoryEntriesRequest{ScopeID: downstreamScope})
	if err != nil {
		t.Fatalf("list Memory through restarted public client: %v", err)
	}
	listed, ok := listedResult.(*v1.ListMemoryEntriesResponseHeaders)
	if !ok {
		t.Fatalf("list Memory response = %T", listedResult)
	}
	if len(listed.Response.Entries) != 1 || listed.Response.Entries[0].Text != "A public client persisted this Memory entry." {
		t.Fatalf("Memory entries after restart = %#v", listed.Response.Entries)
	}
}

func TestServerStartupReportsEarlyChildExit(t *testing.T) {
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("locate Go toolchain for short-lived child fixture: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	process, err := startServerProcess(ctx, goBinary, t.TempDir(), 1)
	if err != nil {
		t.Fatalf("start short-lived child fixture: %v", err)
	}
	t.Cleanup(func() { stopServer(t, process) })

	started := time.Now()
	err = waitForServer(ctx, process, "http://127.0.0.1:1")
	if err == nil {
		t.Fatal("short-lived child fixture unexpectedly became ready")
	}
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("early child exit took %s to report: %v", elapsed, err)
	}
	if !strings.Contains(err.Error(), "exited before readiness") {
		t.Fatalf("early child failure is missing its exit state: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("early child failure is missing its bounded log: %v", err)
	}
}

func TestServerLogBufferRetainsLatestBoundedOutput(t *testing.T) {
	var logs serverLogBuffer
	marker := []byte("latest-server-failure")
	_, _ = logs.Write(bytes.Repeat([]byte("x"), maximumServerLogBytes+1024))
	_, _ = logs.Write(marker)

	if logs.Len() > maximumServerLogBytes {
		t.Fatalf("server log retained %d bytes, want at most %d", logs.Len(), maximumServerLogBytes)
	}
	if !bytes.Contains(logs.Bytes(), marker) {
		t.Fatal("server log dropped the latest failure output")
	}
}

func currentWorkHandoffRequest() *v1.HandoffCurrentWorkRequest {
	return &v1.HandoffCurrentWorkRequest{
		ScopeID:  downstreamScope,
		SourceID: downstreamSourceID,
		Handoff: v1.CurrentWorkHandoff{
			Schema:      v1.CurrentWorkHandoffSchemaPowercontextCurrentWorkHandoffV1,
			Trust:       v1.CurrentWorkHandoffTrustUntrustedInput,
			Objective:   "Validate the isolated downstream consumer.",
			State:       []v1.WorkClaim{{Text: "The public HTTP client is available.", Basis: v1.WorkClaimBasisDeclared, Evidence: []v1.HandoffCitation{}}},
			Disposition: v1.HandoffDispositionContinuable,
			NextAction:  v1.NilWorkClaim{Null: true},
			Omissions:   []string{},
		},
	}
}

func startServer(t *testing.T, ctx context.Context) string {
	t.Helper()
	process, serverURL := startServerWithHome(t, ctx, t.TempDir())
	t.Cleanup(func() { stopServer(t, process) })
	return serverURL
}

func startServerWithHome(t *testing.T, ctx context.Context, home string) (*serverProcess, string) {
	t.Helper()
	binary := os.Getenv("POWERCONTEXT_DOWNSTREAM_BINARY")
	if binary == "" {
		t.Fatal("POWERCONTEXT_DOWNSTREAM_BINARY must name the prebuilt PowerContext Server")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if closeErr := listener.Close(); closeErr != nil {
		t.Fatalf("release loopback port: %v", closeErr)
	}

	process, err := startServerProcess(ctx, binary, home, port)
	if err != nil {
		t.Fatalf("start prebuilt Server: %v", err)
	}

	serverURL := "http://127.0.0.1:" + strconv.Itoa(port)
	if err := waitForServer(ctx, process, serverURL); err != nil {
		t.Fatal(err)
	}
	return process, serverURL
}

type serverLogBuffer struct {
	mu        sync.Mutex
	contents  []byte
	truncated bool
}

func (b *serverLogBuffer) Write(value []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(value)
	if len(value) >= maximumServerLogBytes {
		b.truncated = b.truncated || len(b.contents) > 0 || len(value) > maximumServerLogBytes
		b.contents = append(b.contents[:0], value[len(value)-maximumServerLogBytes:]...)
		return original, nil
	}
	combinedSize := len(b.contents) + len(value)
	if combinedSize > maximumServerLogBytes {
		discarded := combinedSize - maximumServerLogBytes
		copy(b.contents, b.contents[discarded:])
		b.contents = b.contents[:len(b.contents)-discarded]
		b.truncated = true
	}
	b.contents = append(b.contents, value...)
	return original, nil
}

func (b *serverLogBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.contents)
}

func (b *serverLogBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.contents)
}

func (b *serverLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	output := string(b.contents)
	if b.truncated {
		return fmt.Sprintf("%s\n[server logs truncated at %d bytes]", output, maximumServerLogBytes)
	}
	return output
}

type serverProcess struct {
	command *exec.Cmd
	logs    serverLogBuffer
	done    chan struct{}
	waitErr error
}

func startServerProcess(ctx context.Context, binary, home string, port int) (*serverProcess, error) {
	command := exec.CommandContext(ctx, binary, "server", "run", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	command.Env = append(os.Environ(), "POWERCONTEXT_HOME="+home)
	process := &serverProcess{command: command, done: make(chan struct{})}
	command.Stdout = &process.logs
	command.Stderr = &process.logs
	if err := command.Start(); err != nil {
		return nil, err
	}
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
	}()
	return process, nil
}

func waitForServer(ctx context.Context, process *serverProcess, serverURL string) error {
	readinessCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	go func() {
		select {
		case <-process.done:
			cancel(process.exitError())
		case <-readinessCtx.Done():
		}
	}()

	client := &http.Client{Timeout: time.Second}
	for {
		request, err := http.NewRequestWithContext(readinessCtx, http.MethodGet, serverURL+"/health/live", nil)
		if err != nil {
			return fmt.Errorf("create Server readiness request: %w", err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusServiceUnavailable {
				return nil
			}
		}
		if cause := context.Cause(readinessCtx); cause != nil {
			return process.startupFailure(cause)
		}
		select {
		case <-readinessCtx.Done():
			return process.startupFailure(context.Cause(readinessCtx))
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *serverProcess) exitError() error {
	if p.waitErr != nil {
		return fmt.Errorf("prebuilt Server exited before readiness: %w", p.waitErr)
	}
	if p.command.ProcessState != nil {
		return fmt.Errorf("prebuilt Server exited before readiness with exit code %d", p.command.ProcessState.ExitCode())
	}
	return errors.New("prebuilt Server exited before readiness")
}

func (p *serverProcess) startupFailure(cause error) error {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		cause = fmt.Errorf("prebuilt Server did not become ready: %w", cause)
	}
	return fmt.Errorf("%w\nServer log:\n%s", cause, p.logs.String())
}

func stopServer(t *testing.T, process *serverProcess) {
	t.Helper()
	if process.command.Process == nil {
		return
	}
	select {
	case <-process.done:
		return
	default:
	}
	if err := process.command.Process.Signal(os.Interrupt); err != nil {
		_ = process.command.Process.Kill()
	}
	select {
	case <-process.done:
	case <-time.After(3 * time.Second):
		_ = process.command.Process.Kill()
		<-process.done
		t.Fatalf("prebuilt Server did not stop within the bounded shutdown window\nServer log:\n%s", process.logs.String())
	}
}
