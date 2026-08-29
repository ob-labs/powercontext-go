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
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/client"
)

const (
	downstreamScope           = "project:downstream-consumer"
	downstreamSourceID        = "downstream-work-boundary"
	downstreamReceiptSourceID = "downstream-receipt"
)

func TestPublicClientCompletesCurrentWorkHandoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	serverURL := startServer(t, ctx)
	api, err := client.New(serverURL, client.Options{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("create public client: %v", err)
	}

	if _, err := api.CreateWorkContract(ctx, &v1.CreateWorkContractRequest{
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
	}); err != nil {
		t.Fatalf("create work contract through public client: %v", err)
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

// Regression for #42: HandoffCurrentWork captures its boundary source, while
// AcknowledgeHandoff captures a handoff receipt at its own source ID.
func TestAcknowledgementUsesDistinctReceiptSource(t *testing.T) {
	if downstreamReceiptSourceID == downstreamSourceID {
		t.Fatal("acknowledgement must not reuse the current-work boundary source ID")
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
	binary := os.Getenv("POWERCONTEXT_DOWNSTREAM_BINARY")
	if binary == "" {
		t.Fatal("POWERCONTEXT_DOWNSTREAM_BINARY must name the prebuilt PowerContext Server")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}

	home := t.TempDir()
	command := exec.CommandContext(ctx, binary, "server", "run", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	command.Env = append(os.Environ(), "POWERCONTEXT_HOME="+home)
	var logs bytes.Buffer
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		t.Fatalf("start prebuilt Server: %v", err)
	}
	t.Cleanup(func() { stopServer(t, command, &logs) })

	serverURL := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Timeout: time.Second}
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/health/live", nil)
		if err != nil {
			t.Fatalf("create Server readiness request: %v", err)
		}
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusServiceUnavailable {
				return serverURL
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("prebuilt Server did not become ready: %v\nServer log:\n%s", ctx.Err(), logs.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func stopServer(t *testing.T, command *exec.Cmd, logs *bytes.Buffer) {
	t.Helper()
	if command.Process == nil {
		return
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		_ = command.Process.Kill()
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = command.Process.Kill()
		<-done
		t.Fatalf("prebuilt Server did not stop within the bounded shutdown window\nServer log:\n%s", logs.String())
	}
}
