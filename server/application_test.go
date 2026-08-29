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

//go:build sqlite_fts5

package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	pcclient "github.com/ob-labs/powercontext-go/client"
	"github.com/ob-labs/powercontext-go/inference"
	serverlogging "github.com/ob-labs/powercontext-go/internal/observability/logging"
	"github.com/ob-labs/powercontext-go/internal/scheduler"
	"github.com/ob-labs/powercontext-go/source"
)

func TestOpenApplicationProvidesRunnableSQLiteVerticalSlice(t *testing.T) {
	t.Parallel()
	config := applicationTestConfig(t)
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := application.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}

	recorder := postApplicationJSON(t, handler, "/v1/sources/content", map[string]any{
		"scope_id": "project:application", "source_id": "source-1", "content": "captured",
	})
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("capture status = %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = postApplicationJSON(t, handler, "/v1/memory/remember", map[string]any{
		"scope_id": "project:application", "kind": "fact", "text": "Go server is running.",
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("remember status = %d: %s", recorder.Code, recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("capabilities status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var capabilities struct {
		SourceTypes      []string `json:"source_types"`
		SearchModes      []string `json:"search_modes"`
		MemoryExtraction bool     `json:"memory_extraction"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &capabilities); err != nil {
		t.Fatal(err)
	}
	if len(capabilities.SourceTypes) != 1 || capabilities.SourceTypes[0] != "content" ||
		len(capabilities.SearchModes) != 2 || capabilities.MemoryExtraction {
		t.Fatalf("unexpected capabilities: %#v", capabilities)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/health/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("readiness status = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestGoClientExercisesReviewHTTPRuntimeSQLiteVerticalSlice(t *testing.T) {
	t.Parallel()
	application, err := OpenApplication(t.Context(), applicationTestConfig(t), Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := application.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	inProcessHTTPClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	sdk, err := pcclient.New("http://powercontext.test", pcclient.Options{
		HTTPClient: inProcessHTTPClient, TrustTransportSecurity: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	const scope = "project:go-client-review"

	capturedResult, err := sdk.CaptureContentSource(ctx, &v1.CaptureContentSourceRequest{
		ScopeID: scope, SourceID: "task-1", Content: "Regeneration and contract validation passed.",
	})
	if err != nil {
		t.Fatal(err)
	}
	captured, ok := capturedResult.(*v1.CaptureContentSourceResponseHeaders)
	if !ok {
		t.Fatalf("capture result = %T", capturedResult)
	}
	sourceRef := captured.Response.Source
	proposal := v1.ExperienceProposal{
		Situation: "The public contract changed.", Action: "Regenerate and test the client.",
		Outcome: "The generated transport stayed aligned.", Lesson: "Regenerate before contract tests.",
	}
	proposedResult, err := sdk.ProposeExperience(ctx, &v1.ProposeExperienceRequest{
		ScopeID: scope, Proposal: proposal,
		SourceRefs: []v1.SourceReference{sourceRef}, ArtifactRefs: []v1.ArtifactReference{},
	})
	if err != nil {
		t.Fatal(err)
	}
	proposed, ok := proposedResult.(*v1.ArtifactCandidateHeaders)
	if !ok || proposed.Response.Status != v1.CandidateStatusPending || proposed.Response.Version != 1 {
		t.Fatalf("propose result = %T %#v", proposedResult, proposedResult)
	}
	candidateID := proposed.Response.CandidateID

	preparedResult, err := sdk.PrepareContext(ctx, &v1.PrepareContextRequest{
		ScopeID: scope, Query: "Regenerate before contract tests.",
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, ok := preparedResult.(*v1.PreparedContextHeaders)
	if !ok || prepared.Response.Status != v1.PreparedContextStatusEmpty {
		t.Fatalf("pending Candidate leaked into Context: %T %#v", preparedResult, preparedResult)
	}

	listedResult, err := sdk.ListArtifactCandidates(ctx, &v1.ListArtifactCandidatesRequest{
		ScopeID: scope, Status: v1.NewOptCandidateStatus(v1.CandidateStatusPending),
		Family: v1.NewOptNilCandidateFamily(v1.CandidateFamilyExperience), Limit: v1.NewOptInt(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, ok := listedResult.(*v1.ArtifactCandidatePageHeaders)
	if !ok || len(listed.Response.Candidates) != 1 || listed.Response.Candidates[0].CandidateID != candidateID {
		t.Fatalf("list result = %T %#v", listedResult, listedResult)
	}

	proposal.Action = "Regenerate, inspect, and test the client."
	proposal.Lesson = "Regenerate and inspect before contract tests."
	revisedResult, err := sdk.ReviseArtifactCandidate(ctx, &v1.ReviseArtifactCandidateRequest{
		ScopeID: scope, CandidateID: candidateID, ExpectedVersion: 1,
		Proposal:   v1.NewExperienceProposalReviseArtifactCandidateRequestProposal(proposal),
		SourceRefs: []v1.SourceReference{sourceRef}, ArtifactRefs: []v1.ArtifactReference{},
	})
	if err != nil {
		t.Fatal(err)
	}
	revised, ok := revisedResult.(*v1.ArtifactCandidateHeaders)
	if !ok || revised.Response.Version != 2 {
		t.Fatalf("revise result = %T %#v", revisedResult, revisedResult)
	}
	approvedResult, err := sdk.ApproveArtifactCandidate(ctx, &v1.ApproveArtifactCandidateRequest{
		ScopeID: scope, CandidateID: candidateID, ExpectedVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, ok := approvedResult.(*v1.ArtifactCandidateHeaders)
	if !ok || approved.Response.Status != v1.CandidateStatusApproved {
		t.Fatalf("approval result = %T %#v", approvedResult, approvedResult)
	}
	resultRef, ok := approved.Response.ResultArtifact.Get()
	if !ok || resultRef.Family != experience.Family || resultRef.Revision != 1 {
		t.Fatalf("approval result Artifact = %#v", approved.Response.ResultArtifact)
	}

	experienceResult, err := sdk.GetExperience(ctx, &v1.GetExperienceRequest{ScopeID: scope, Artifact: resultRef})
	if err != nil {
		t.Fatal(err)
	}
	storedExperience, ok := experienceResult.(*v1.ExperienceArtifactHeaders)
	if !ok || storedExperience.Response.Content != proposal ||
		len(storedExperience.Response.SourceRefs) != 1 || storedExperience.Response.SourceRefs[0] != sourceRef {
		t.Fatalf("Experience result = %T %#v", experienceResult, experienceResult)
	}
	preparedResult, err = sdk.PrepareContext(ctx, &v1.PrepareContextRequest{
		ScopeID: scope, Query: "Regenerate and inspect before contract tests.",
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, ok = preparedResult.(*v1.PreparedContextHeaders)
	if !ok || prepared.Response.Status != v1.PreparedContextStatusReady {
		t.Fatalf("approved Experience was not recallable: %T %#v", preparedResult, preparedResult)
	}

	skillResult, err := sdk.ProposeSkill(ctx, &v1.ProposeSkillRequest{
		ScopeID: scope,
		Proposal: v1.SkillProposal{
			Name: "review-http-contract", Description: "Use after changing the public contract.",
			Instructions: "Regenerate the client, inspect it, and run contract tests.",
			Validation:   []v1.SkillValidationItem{"contract tests pass"},
		},
		SourceRefs: []v1.SourceReference{}, ArtifactRefs: []v1.ArtifactReference{resultRef},
	})
	if err != nil {
		t.Fatal(err)
	}
	skillCandidate, ok := skillResult.(*v1.ArtifactCandidateHeaders)
	if !ok {
		t.Fatalf("Skill proposal result = %T", skillResult)
	}
	skillApprovalResult, err := sdk.ApproveArtifactCandidate(ctx, &v1.ApproveArtifactCandidateRequest{
		ScopeID: scope, CandidateID: skillCandidate.Response.CandidateID, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	skillApproval, ok := skillApprovalResult.(*v1.ArtifactCandidateHeaders)
	if !ok {
		t.Fatalf("Skill approval result = %T", skillApprovalResult)
	}
	skillRef, ok := skillApproval.Response.ResultArtifact.Get()
	if !ok || skillRef.Family != skill.Family {
		t.Fatalf("Skill approval result Artifact = %#v", skillApproval.Response.ResultArtifact)
	}
	storedSkillResult, err := sdk.GetSkill(ctx, &v1.GetSkillRequest{ScopeID: scope, Artifact: skillRef})
	if err != nil {
		t.Fatal(err)
	}
	storedSkill, ok := storedSkillResult.(*v1.SkillArtifactHeaders)
	if !ok || len(storedSkill.Response.ArtifactRefs) != 1 || storedSkill.Response.ArtifactRefs[0] != resultRef {
		t.Fatalf("Skill result = %T %#v", storedSkillResult, storedSkillResult)
	}

	secondResult, err := sdk.ProposeExperience(ctx, &v1.ProposeExperienceRequest{
		ScopeID: scope, Proposal: proposal,
		SourceRefs: []v1.SourceReference{sourceRef}, ArtifactRefs: []v1.ArtifactReference{},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, ok := secondResult.(*v1.ArtifactCandidateHeaders)
	if !ok {
		t.Fatalf("second proposal result = %T", secondResult)
	}
	rejectedResult, err := sdk.RejectArtifactCandidate(ctx, &v1.RejectArtifactCandidateRequest{
		ScopeID: scope, CandidateID: second.Response.CandidateID, ExpectedVersion: 1, Reason: "unsupported",
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected, ok := rejectedResult.(*v1.ArtifactCandidateHeaders)
	if !ok {
		t.Fatalf("rejection result = %T", rejectedResult)
	}
	decision, decisionSet := rejected.Response.DecisionReason.Get()
	if rejected.Response.Status != v1.CandidateStatusRejected || !decisionSet || decision != "unsupported" {
		t.Fatalf("rejection result = %T %#v", rejectedResult, rejectedResult)
	}
}

func TestGoClientGeneratesReviewedExperienceAndSkillCandidates(t *testing.T) {
	t.Parallel()
	application, err := OpenApplication(t.Context(), applicationTestConfig(t), Dependencies{
		ExperienceGenerator: fixedReviewExperienceGenerator{},
		SkillGenerator:      fixedReviewSkillGenerator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close(context.Background()) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder.Result(), nil
	})}
	sdk, err := pcclient.New("http://powercontext.test", pcclient.Options{
		HTTPClient: httpClient, TrustTransportSecurity: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	const scope = "project:go-client-generation"
	capturedResult, err := sdk.CaptureContentSource(ctx, &v1.CaptureContentSourceRequest{
		ScopeID: scope, SourceID: "task-1", Content: "The contract checks passed.",
	})
	if err != nil {
		t.Fatal(err)
	}
	capturedHeaders, ok := capturedResult.(*v1.CaptureContentSourceResponseHeaders)
	if !ok {
		t.Fatalf("capture result = %T", capturedResult)
	}
	captured := capturedHeaders.Response.Source

	generatedExperienceResult, err := sdk.GenerateExperience(ctx, &v1.GenerateExperienceRequest{
		ScopeID: scope, SourceRefs: []v1.SourceReference{captured}, ArtifactRefs: []v1.ArtifactReference{},
	})
	if err != nil {
		t.Fatal(err)
	}
	generatedExperience, ok := generatedExperienceResult.(*v1.GeneratedCandidateResponseHeaders)
	if !ok || generatedExperience.Response.Status != v1.GeneratedCandidateStatusPending {
		t.Fatalf("generated Experience = %T %#v", generatedExperienceResult, generatedExperienceResult)
	}
	experienceCandidate, ok := generatedExperience.Response.Candidate.Get()
	if !ok || experienceCandidate.Family != v1.CandidateFamilyExperience ||
		len(experienceCandidate.SourceRefs) != 1 || experienceCandidate.SourceRefs[0] != captured {
		t.Fatalf("generated Experience Candidate = %#v", generatedExperience.Response.Candidate)
	}
	approvedResult, err := sdk.ApproveArtifactCandidate(ctx, &v1.ApproveArtifactCandidateRequest{
		ScopeID: scope, CandidateID: experienceCandidate.CandidateID, ExpectedVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, ok := approvedResult.(*v1.ArtifactCandidateHeaders)
	if !ok {
		t.Fatalf("approval result = %T", approvedResult)
	}
	experienceRef, ok := approved.Response.ResultArtifact.Get()
	if !ok {
		t.Fatal("generated Experience approval has no result Artifact")
	}

	generatedSkillResult, err := sdk.GenerateSkill(ctx, &v1.GenerateSkillRequest{
		ScopeID: scope, Origin: v1.SkillGenerationOriginExperience,
		SourceRefs: []v1.SourceReference{}, ArtifactRefs: []v1.ArtifactReference{experienceRef},
	})
	if err != nil {
		t.Fatal(err)
	}
	generatedSkill, ok := generatedSkillResult.(*v1.GeneratedCandidateResponseHeaders)
	if !ok || generatedSkill.Response.Status != v1.GeneratedCandidateStatusPending {
		t.Fatalf("generated Skill = %T %#v", generatedSkillResult, generatedSkillResult)
	}
	skillCandidate, ok := generatedSkill.Response.Candidate.Get()
	if !ok || skillCandidate.Family != v1.CandidateFamilySkill ||
		len(skillCandidate.ArtifactRefs) != 1 || skillCandidate.ArtifactRefs[0] != experienceRef {
		t.Fatalf("generated Skill Candidate = %#v", generatedSkill.Response.Candidate)
	}
}

func TestOpenApplicationReportsRuntimeAndDatabaseReadiness(t *testing.T) {
	t.Parallel()
	application, err := OpenApplication(t.Context(), applicationTestConfig(t), Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close(context.Background()) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}

	response := perform(handler, http.MethodGet, "/health/ready", "")
	if response.Code != http.StatusOK {
		t.Fatalf("readiness = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Status string            `json:"status"`
		Checks map[string]string `json:"checks"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ready" || len(body.Checks) != 2 ||
		body.Checks["runtime"] != "ready" || body.Checks["database"] != "ready" {
		t.Fatalf("readiness body = %#v", body)
	}
}

func TestOpenApplicationWiresReadinessAndAccessLogging(t *testing.T) {
	t.Parallel()
	config := applicationTestConfig(t)
	var output bytes.Buffer
	application, err := OpenApplication(t.Context(), config, Dependencies{Logger: newJSONTestLogger(t, &output)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close(context.Background()) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	if response := perform(handler, http.MethodGet, "/v1/capabilities", ""); response.Code != http.StatusOK {
		t.Fatalf("capabilities = %d: %s", response.Code, response.Body.String())
	}
	if response := perform(handler, http.MethodGet, "/health/ready", ""); response.Code != http.StatusOK {
		t.Fatalf("readiness = %d: %s", response.Code, response.Body.String())
	}

	records := decodeLogRecords(t, output.String())
	findLogRecord(t, records, serverlogging.TransportCompletedEvent, "get_capabilities")
	readyTransitions := 0
	for _, record := range records {
		if record["event"] == "server.ready" {
			readyTransitions++
		}
	}
	if readyTransitions != 1 {
		t.Fatalf("server.ready transitions = %d in %#v", readyTransitions, records)
	}
}

func TestOpenApplicationReportsAndCachesConfiguredEmbeddingReadiness(t *testing.T) {
	t.Parallel()
	config := applicationTestConfig(t)
	var output bytes.Buffer
	var calls atomic.Int64
	embedding := failingReadinessEmbedding{
		calls: &calls,
		err:   inference.NewConfigurationError("embedding-model", "secret provider response"),
	}
	application, err := OpenApplication(t.Context(), config, Dependencies{
		EmbeddingModel: embedding,
		Logger:         newJSONTestLogger(t, &output),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close(context.Background()) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		response := perform(handler, http.MethodGet, "/health/ready", "")
		if response.Code != http.StatusOK {
			t.Fatalf("readiness = %d: %s", response.Code, response.Body.String())
		}
		var body struct {
			Status string            `json:"status"`
			Checks map[string]string `json:"checks"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Status != "degraded" || body.Checks["runtime"] != "ready" ||
			body.Checks["database"] != "ready" || body.Checks["inference.embedding"] != "misconfigured" ||
			len(body.Checks) != 3 {
			t.Fatalf("readiness body = %#v", body)
		}
		if strings.Contains(response.Body.String(), "secret provider response") {
			t.Fatalf("readiness leaked provider detail: %s", response.Body.String())
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("embedding probes = %d, want one startup refresh", calls.Load())
	}
	if strings.Contains(output.String(), "secret provider response") {
		t.Fatalf("readiness log leaked provider detail: %s", output.String())
	}
}

func TestOpenApplicationReportsMissingOpenAIEmbeddingAPIPrefixAsDegraded(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret-api-key")
	t.Setenv("OPENAI_BASE_URL", "https://provider.example")
	config := applicationTestConfig(t)
	config.Inference.EmbeddingModel = "openai:text-embedding-3-small"
	config.Inference.EmbeddingProfileID = "readiness-test"
	config.Inference.EmbeddingDimension = 3

	var nowSeconds atomic.Int64
	var requestsMu sync.Mutex
	var paths, authorizations []string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestsMu.Lock()
		paths = append(paths, request.URL.Path)
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		requestsMu.Unlock()
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"message":"secret provider response","type":"invalid_request_error"}}`,
			)),
			Request: request,
		}, nil
	})}
	var output bytes.Buffer
	application, err := OpenApplication(t.Context(), config, Dependencies{
		Clock:      func() time.Time { return time.Unix(nowSeconds.Load(), 0).UTC() },
		HTTPClient: httpClient,
		Logger:     newJSONTestLogger(t, &output),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close(context.Background()) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}

	for index, at := range []int64{0, 299, 300} {
		nowSeconds.Store(at)
		response := perform(handler, http.MethodGet, "/health/ready", "")
		if response.Code != http.StatusOK {
			t.Fatalf("readiness %d = %d: %s", index, response.Code, response.Body.String())
		}
		var body struct {
			Status string            `json:"status"`
			Checks map[string]string `json:"checks"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Status != "degraded" || len(body.Checks) != 3 ||
			body.Checks["runtime"] != "ready" || body.Checks["database"] != "ready" ||
			body.Checks["inference.embedding"] != "misconfigured" {
			t.Fatalf("readiness %d = %#v", index, body)
		}
		if strings.Contains(response.Body.String(), "secret-api-key") ||
			strings.Contains(response.Body.String(), "secret provider response") {
			t.Fatalf("readiness %d leaked provider detail: %s", index, response.Body.String())
		}
	}

	requestsMu.Lock()
	gotPaths := append([]string(nil), paths...)
	gotAuthorizations := append([]string(nil), authorizations...)
	requestsMu.Unlock()
	if len(gotPaths) != 2 || gotPaths[0] != "/embeddings" || gotPaths[1] != "/embeddings" {
		t.Fatalf("embedding request paths = %v", gotPaths)
	}
	for _, authorization := range gotAuthorizations {
		if authorization != "Bearer secret-api-key" {
			t.Fatalf("embedding authorization = %q", authorization)
		}
	}
	if strings.Contains(output.String(), "secret-api-key") || strings.Contains(output.String(), "secret provider response") {
		t.Fatalf("readiness log leaked provider detail: %s", output.String())
	}
}

func TestOpenApplicationPersistsBusinessInferenceAndRecallAcrossRestartWithoutMeteringReadiness(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", "https://provider.test/v1")
	config := applicationTestConfig(t)
	config.Inference.GenerationModel = "openai-chat:test-model"
	clock := func() time.Time {
		return time.Date(2026, time.August, 17, 13, 14, 15, 123456000, time.UTC)
	}
	sourceContent := strings.Repeat(
		"The statistics contract keeps exact Source lineage auditable. ", 12,
	)
	providerCalls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		providerCalls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		content := "ok"
		inputTokens, outputTokens := int64(2), int64(1)
		if _, structured := body["response_format"]; structured {
			content = `{"candidates":[{"intent":"add","kind":"decision","text":"The statistics contract is stable.","evidence_ids":["source:0"],"entry_id":null,"reason":"Captured by the Go statistics flow."}]}`
			inputTokens, outputTokens = 17, 5
		}
		response := map[string]any{
			"id": "response", "object": "chat.completion", "created": 0, "model": "test-model",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens": inputTokens, "completion_tokens": outputTokens,
				"total_tokens": inputTokens + outputTokens,
			},
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewReader(encoded)),
			Request:    request,
		}, nil
	})}
	application, err := OpenApplication(t.Context(), config, Dependencies{HTTPClient: client, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close(context.Background()) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}

	if response := postApplicationJSON(t, handler, "/v1/sources/content", map[string]any{
		"scope_id": "scope-statistics", "source_id": "source-1", "content": sourceContent,
	}); response.Code != http.StatusAccepted {
		t.Fatalf("capture = %d: %s", response.Code, response.Body.String())
	}
	if response := postApplicationJSON(t, handler, "/v1/memory/flush", map[string]any{
		"scope_id": "scope-statistics",
	}); response.Code != http.StatusOK {
		t.Fatalf("flush = %d: %s", response.Code, response.Body.String())
	}
	preparedResponse := postApplicationJSON(t, handler, "/v1/context/prepare", map[string]any{
		"scope_id": "scope-statistics", "query": "statistics contract",
	})
	if preparedResponse.Code != http.StatusOK {
		t.Fatalf("prepare = %d: %s", preparedResponse.Code, preparedResponse.Body.String())
	}
	var prepared struct {
		Status  string  `json:"status"`
		Content *string `json:"content"`
	}
	if err := json.Unmarshal(preparedResponse.Body.Bytes(), &prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.Status != "ready" || prepared.Content == nil {
		t.Fatalf("prepared = %#v", prepared)
	}

	statisticsResponse := perform(
		handler, http.MethodGet, "/v1/stats?scope_id=scope-statistics&period=today", "",
	)
	if statisticsResponse.Code != http.StatusOK {
		t.Fatalf("statistics = %d: %s", statisticsResponse.Code, statisticsResponse.Body.String())
	}
	var statistics struct {
		Usage struct {
			Totals struct {
				Generation struct {
					Requests     int64  `json:"requests"`
					InputTokens  *int64 `json:"input_tokens"`
					OutputTokens *int64 `json:"output_tokens"`
				} `json:"generation"`
			} `json:"totals"`
			ByPurpose []struct {
				Purpose string `json:"purpose"`
			} `json:"by_purpose"`
			Daily []json.RawMessage `json:"daily"`
		} `json:"usage"`
		Recall struct {
			Estimator *struct {
				EstimatorID string `json:"estimator_id"`
				Version     string `json:"version"`
			} `json:"estimator"`
			Totals struct {
				Preparations           int64 `json:"preparations"`
				ReadyPreparations      int64 `json:"ready_preparations"`
				ComparablePreparations int64 `json:"comparable_preparations"`
				BaselineTokens         int64 `json:"baseline_tokens"`
				RecalledTokens         int64 `json:"recalled_tokens"`
			} `json:"totals"`
			Daily []json.RawMessage `json:"daily"`
		} `json:"recall"`
	}
	if err := json.Unmarshal(statisticsResponse.Body.Bytes(), &statistics); err != nil {
		t.Fatal(err)
	}
	generation := statistics.Usage.Totals.Generation
	if generation.Requests != 1 || generation.InputTokens == nil || *generation.InputTokens != 17 ||
		generation.OutputTokens == nil || *generation.OutputTokens != 5 {
		t.Fatalf("generation usage = %#v", generation)
	}
	if len(statistics.Usage.ByPurpose) != 1 || statistics.Usage.ByPurpose[0].Purpose != "memory_extraction" {
		t.Fatalf("usage purposes = %#v", statistics.Usage.ByPurpose)
	}
	recall := statistics.Recall
	if recall.Estimator == nil || recall.Estimator.EstimatorID != "character:weighted" || recall.Estimator.Version != "1" ||
		recall.Totals.Preparations != 1 || recall.Totals.ReadyPreparations != 1 ||
		recall.Totals.ComparablePreparations != 1 {
		t.Fatalf("recall = %#v", recall)
	}
	estimator := inference.CharacterTokenEstimator()
	wantBaseline, _ := estimator.Estimate(sourceContent)
	wantRecalled, _ := estimator.Estimate(*prepared.Content)
	if recall.Totals.BaselineTokens != int64(wantBaseline) || recall.Totals.RecalledTokens != int64(wantRecalled) {
		t.Fatalf("recall totals = %#v, want baseline=%d recalled=%d", recall.Totals, wantBaseline, wantRecalled)
	}
	if providerCalls != 2 {
		t.Fatalf("provider calls = %d, want readiness + business", providerCalls)
	}

	if err := application.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	application, err = OpenApplication(t.Context(), config, Dependencies{HTTPClient: client, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	handler, err = application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}
	restoredResponse := perform(
		handler, http.MethodGet, "/v1/stats?scope_id=scope-statistics&period=7d", "",
	)
	if restoredResponse.Code != http.StatusOK {
		t.Fatalf("restored statistics = %d: %s", restoredResponse.Code, restoredResponse.Body.String())
	}
	restored := statistics
	if err := json.Unmarshal(restoredResponse.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	restoredGeneration := restored.Usage.Totals.Generation
	if restoredGeneration.Requests != generation.Requests || restoredGeneration.InputTokens == nil ||
		*restoredGeneration.InputTokens != *generation.InputTokens || restoredGeneration.OutputTokens == nil ||
		*restoredGeneration.OutputTokens != *generation.OutputTokens || len(restored.Usage.Daily) != 7 {
		t.Fatalf("restored usage = %#v", restored.Usage)
	}
	if restored.Recall.Totals != statistics.Recall.Totals || len(restored.Recall.Daily) != 7 {
		t.Fatalf("restored recall = %#v", restored.Recall)
	}

	preparedAgainResponse := postApplicationJSON(t, handler, "/v1/context/prepare", map[string]any{
		"scope_id": "scope-statistics", "query": "statistics contract",
	})
	if preparedAgainResponse.Code != http.StatusOK {
		t.Fatalf("prepare after restart = %d: %s", preparedAgainResponse.Code, preparedAgainResponse.Body.String())
	}
	preparedAgain := prepared
	if err := json.Unmarshal(preparedAgainResponse.Body.Bytes(), &preparedAgain); err != nil {
		t.Fatal(err)
	}
	if preparedAgain.Status != "ready" || preparedAgain.Content == nil || *preparedAgain.Content != *prepared.Content {
		t.Fatalf("prepared after restart = %#v", preparedAgain)
	}
	updatedResponse := perform(
		handler, http.MethodGet, "/v1/stats?scope_id=scope-statistics&period=7d", "",
	)
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("updated statistics = %d: %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	updated := restored
	if err := json.Unmarshal(updatedResponse.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Recall.Totals.Preparations != 2 || updated.Recall.Totals.ReadyPreparations != 2 ||
		updated.Recall.Totals.ComparablePreparations != 2 ||
		updated.Recall.Totals.BaselineTokens != recall.Totals.BaselineTokens*2 ||
		updated.Recall.Totals.RecalledTokens != recall.Totals.RecalledTokens*2 {
		t.Fatalf("updated recall = %#v", updated.Recall.Totals)
	}
	if providerCalls != 3 {
		t.Fatalf("provider calls after restart = %d, want two readiness probes + one business call", providerCalls)
	}
}

func TestOpenApplicationStatsUsesInclusiveUTCPeriodsForEmptyScope(t *testing.T) {
	t.Parallel()
	config := applicationTestConfig(t)
	now := time.Date(2026, time.August, 17, 23, 59, 58, 123456000, time.FixedZone("test-offset", 8*60*60))
	application, err := OpenApplication(t.Context(), config, Dependencies{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close(context.Background()) })
	handler, err := application.HTTPHandler()
	if err != nil {
		t.Fatal(err)
	}

	type usageValue struct {
		Requests     int `json:"requests"`
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	}
	type periodBody struct {
		Preset    string `json:"preset"`
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Timezone  string `json:"timezone"`
	}
	type dayBody struct {
		Date       string     `json:"date"`
		Generation usageValue `json:"generation"`
		Embedding  usageValue `json:"embedding"`
	}
	type recallDayBody struct {
		Date                   string `json:"date"`
		Preparations           int    `json:"preparations"`
		ReadyPreparations      int    `json:"ready_preparations"`
		ComparablePreparations int    `json:"comparable_preparations"`
		BaselineTokens         int    `json:"baseline_tokens"`
		RecalledTokens         int    `json:"recalled_tokens"`
		TokenReduction         int    `json:"token_reduction"`
	}
	type statisticsBody struct {
		ScopeID string    `json:"scope_id"`
		AsOf    time.Time `json:"as_of"`
		Usage   struct {
			Period periodBody `json:"period"`
			Totals struct {
				Generation usageValue `json:"generation"`
				Embedding  usageValue `json:"embedding"`
			} `json:"totals"`
			Daily []dayBody `json:"daily"`
		} `json:"usage"`
		Recall struct {
			Period periodBody      `json:"period"`
			Daily  []recallDayBody `json:"daily"`
		} `json:"recall"`
	}

	utcNow := now.UTC()
	for _, test := range []struct {
		name, query, preset string
		days                int
	}{
		{name: "default", query: "", preset: "30d", days: 30},
		{name: "today", query: "&period=today", preset: "today", days: 1},
		{name: "seven days", query: "&period=7d", preset: "7d", days: 7},
		{name: "thirty days", query: "&period=30d", preset: "30d", days: 30},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := perform(
				handler, http.MethodGet, "/v1/stats?scope_id=project%3Atest"+test.query, "",
			)
			if response.Code != http.StatusOK {
				t.Fatalf("statistics = %d: %s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" ||
				response.Header().Get("X-PowerContext-Request-ID") == "" ||
				response.Header().Get("X-Request-ID") != "" {
				t.Fatalf("statistics headers = %#v", response.Header())
			}
			var body statisticsBody
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.ScopeID != "project:test" || !body.AsOf.Equal(utcNow) {
				t.Fatalf("statistics identity = scope:%q as_of:%s", body.ScopeID, body.AsOf)
			}
			endDate := time.Date(utcNow.Year(), utcNow.Month(), utcNow.Day(), 0, 0, 0, 0, time.UTC)
			startDate := endDate.AddDate(0, 0, -(test.days - 1))
			wantPeriod := periodBody{
				Preset: test.preset, StartDate: startDate.Format(time.DateOnly),
				EndDate: endDate.Format(time.DateOnly), Timezone: "UTC",
			}
			if body.Usage.Period != wantPeriod || body.Recall.Period != wantPeriod ||
				len(body.Usage.Daily) != test.days || len(body.Recall.Daily) != test.days {
				t.Fatalf("statistics period = usage:%#v/%d recall:%#v/%d", body.Usage.Period, len(body.Usage.Daily), body.Recall.Period, len(body.Recall.Daily))
			}
			if body.Usage.Totals.Generation != (usageValue{}) || body.Usage.Totals.Embedding != (usageValue{}) {
				t.Fatalf("statistics totals = %#v", body.Usage.Totals)
			}
			for index, day := range body.Usage.Daily {
				wantDate := startDate.AddDate(0, 0, index).Format(time.DateOnly)
				if day.Date != wantDate || day.Generation != (usageValue{}) || day.Embedding != (usageValue{}) {
					t.Fatalf("usage day %d = %#v", index, day)
				}
				recallDay := body.Recall.Daily[index]
				if recallDay.Date != wantDate || recallDay != (recallDayBody{Date: wantDate}) {
					t.Fatalf("recall day %d = %#v", index, recallDay)
				}
			}
		})
	}

	invalid := perform(
		handler, http.MethodGet, "/v1/stats?scope_id=project%3Atest&period=all", "",
	)
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid period = %d: %s", invalid.Code, invalid.Body.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(invalid.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "invalid_request" {
		t.Fatalf("invalid period envelope = %#v", envelope)
	}
}

func TestDisabledHandoffReportDoesNotCreateOptionalTables(t *testing.T) {
	t.Parallel()
	config := applicationTestConfig(t)
	config.HandoffReport.Enabled = false
	path, err := SQLiteDSN(config.Database.SQLite.URL)
	if err != nil {
		t.Fatal(err)
	}
	application, err := OpenApplication(t.Context(), config, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name LIKE 'pc_handoff_report_%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("disabled Handoff Report created %d optional tables", count)
	}
}

func TestProcessConfigEnforcesTrustAndInferenceBoundaries(t *testing.T) {
	t.Parallel()
	config := applicationTestConfig(t)
	config.Dashboard.Enabled = true
	config.Dashboard.Scopes = nil
	if err := config.Validate(); err != nil {
		t.Fatalf("default public Dashboard was rejected: %v", err)
	}
	config.Auth = AuthConfig{Enabled: true, Token: "secret"}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.Inference.EmbeddingModel = "openai:text-embedding-3-small"
	if err := config.Validate(); err == nil {
		t.Fatal("partial embedding profile was accepted")
	}
}

func TestOpenApplicationOwnsBothPersistentSchedulerJobs(t *testing.T) {
	t.Parallel()
	config := applicationTestConfig(t)
	hour, twoHours := time.Hour, 2*time.Hour
	config.Runtime.SourceWindowInterval = &hour
	config.Runtime.ExperienceIncubationInterval = &twoHours
	config.SchedulerPath = filepath.Join(t.TempDir(), "scheduler.db")
	config.Inference.GenerationModel = "openai:test-model"
	application, err := OpenApplication(t.Context(), config, Dependencies{
		MemoryCandidates:     noOpMemoryCandidates{},
		ExperienceCandidates: noOpExperienceCandidates{},
		ExperienceGenerator:  noOpExperienceGenerator{},
		SkillGenerator:       noOpSkillGenerator{},
		HandoffGenerator:     noOpHandoffGenerator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	database, err := sql.Open("sqlite3", config.SchedulerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	rows, err := database.Query(`SELECT id FROM powercontext_scheduler_jobs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if len(ids) != 2 || ids[0] != scheduler.ExperienceIncubationJobID || ids[1] != scheduler.SourceWindowJobID {
		t.Fatalf("scheduler job IDs = %v", ids)
	}
}

func TestOpenApplicationSchedulerUsesPowerContextHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(PowerContextHomeEnv, home)
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	hour := time.Hour
	config.Runtime.ExperienceIncubationInterval = &hour
	config.Inference.GenerationModel = "openai:test-model"
	config.MCP.Enabled = false
	config.Metrics.Enabled = false
	application, err := OpenApplication(t.Context(), config, Dependencies{
		MemoryCandidates:     noOpMemoryCandidates{},
		ExperienceCandidates: noOpExperienceCandidates{},
		ExperienceGenerator:  noOpExperienceGenerator{},
		SkillGenerator:       noOpSkillGenerator{},
		HandoffGenerator:     noOpHandoffGenerator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := application.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "scheduler.db")); err != nil {
		t.Fatalf("scheduler sidecar was not created under POWERCONTEXT_HOME: %v", err)
	}
}

func applicationTestConfig(t *testing.T) ProcessConfig {
	t.Helper()
	config, err := DefaultConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.Database.SQLite.URL = sqliteURL(filepath.Join(t.TempDir(), "powercontext.db"))
	config.MCP.Enabled = false
	config.Metrics.Enabled = false
	return config
}

func postApplicationJSON(t *testing.T, handler http.Handler, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

type noOpMemoryCandidates struct{}

func (noOpMemoryCandidates) Extract(context.Context, memory.CandidateRequest) ([]memory.EntryInput, error) {
	return nil, nil
}

type noOpExperienceCandidates struct{}

func (noOpExperienceCandidates) Incubate(context.Context, []source.Value) ([]experience.CandidateInput, error) {
	return nil, nil
}

type noOpExperienceGenerator struct{}

func (noOpExperienceGenerator) Generate(context.Context, artifact.GenerationInput) (*experience.Content, error) {
	return nil, nil
}

type noOpSkillGenerator struct{}

func (noOpSkillGenerator) Generate(context.Context, artifact.GenerationInput) (*skill.Content, error) {
	return nil, nil
}

type fixedReviewExperienceGenerator struct{}

func (fixedReviewExperienceGenerator) Generate(
	context.Context,
	artifact.GenerationInput,
) (*experience.Content, error) {
	value, err := experience.NewContent(
		"The public contract changed.",
		"Regenerate checked-in clients and run contract tests.",
		"The generated client and server contract remained aligned.",
		"Regenerate the client before validating a changed contract.",
	)
	return &value, err
}

type fixedReviewSkillGenerator struct{}

func (fixedReviewSkillGenerator) Generate(
	context.Context,
	artifact.GenerationInput,
) (*skill.Content, error) {
	value, err := skill.NewContent(
		"regenerate-http-contract",
		"Use after changing the PowerContext OpenAPI contract.",
		"Regenerate checked-in transport code, inspect the diff, then run contract tests.",
		[]string{"generation checks pass", "contract tests pass"},
	)
	return &value, err
}

type noOpHandoffGenerator struct{}

func (noOpHandoffGenerator) Generate(context.Context, handoff.GenerationRequest) (handoff.Draft, error) {
	return handoff.Draft{}, nil
}

type failingReadinessEmbedding struct {
	calls *atomic.Int64
	err   error
}

func (m failingReadinessEmbedding) Profile() inference.EmbeddingProfile {
	return readinessEmbeddingProfile{}
}

func (m failingReadinessEmbedding) Embed(context.Context, []string) (inference.EmbeddingResult, error) {
	m.calls.Add(1)
	return inference.EmbeddingResult{}, m.err
}

type readinessEmbeddingProfile struct{}

func (readinessEmbeddingProfile) ID() string                { return "readiness-test" }
func (readinessEmbeddingProfile) ModelName() string         { return "test:embedding" }
func (readinessEmbeddingProfile) DimensionCount() int       { return 3 }
func (readinessEmbeddingProfile) NormalizationMode() string { return "unit" }
