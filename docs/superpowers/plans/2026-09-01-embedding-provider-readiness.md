# Embedding Provider Dimension and Readiness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Carry the configured embedding dimension through the public transport contract and every built-in provider, then expose only a stable, redacted non-transient provider-rejection reason through readiness.

**Architecture:** `BatchedEmbeddingModel` creates one immutable `EmbeddingRequest` per batch from its fixed profile. Built-in transports map that request to provider-specific wire fields, while shared provider error classification and Runtime readiness expose only an allowlisted HTTP status reason. SQLite projection drift is explicitly excluded and is delivered by the second sequential plan after this PR merges.

**Tech Stack:** Go 1.27, OpenAI Go SDK v3, Google Gen AI Go SDK, Cohere Go SDK v2, AWS Bedrock Runtime SDK, ONNX Runtime local embeddings, `encoding/json/v2`, repository `apidiff`, parity inventory generator, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-09-01-embedding-dimension-readiness-design.md`

## Global Constraints

- Start from the latest `origin/main`; a changed Base invalidates all prior diff, test, review, and CI evidence.
- Load the complete Modern Go guideline list before editing any Go file.
- Use TDD for every behavior: observe the focused test fail for the intended missing behavior before production changes.
- `EmbeddingProfile` is the only authoritative dimension source.
- Do not add a second dimension to provider constructors, environment variables, CLI flags, or OpenAPI.
- Do not add a legacy `EmbeddingTransport` compatibility interface or runtime interface detection.
- Provider bodies, URLs, credentials, prompts, Source, Memory, vectors, scope IDs, database locations, and SDK object strings must not enter stable errors or readiness.
- HTTP `408`, `425`, and `429` remain transient; they must not become public misconfiguration reasons.
- OceanBase, seekDB, SQLite schema inspection, SQLite probe lifecycle, and automatic projection rebuild are outside this plan.
- The open retained-adapter surface is outside this plan; no files under `integrations/` change.
- Update the pre-release API baseline deliberately and exercise an external downstream implementation of the new interface.
- Update parity counts only through `tools/parity-inventory-generate`; never edit the generated summary by hand.
- Every commit uses the local Lore format and the required `Co-authored-by: OmX <omx@oh-my-codex.dev>` trailer.
- On Windows, do not disable CGO or weaken tests to hide missing `sqlite3.h`; exact-Head Linux CI supplies full Server and CGO evidence.

---

### Task 1: Introduce the immutable EmbeddingRequest transport contract

**Files:**
- Modify: `inference/embedding.go`
- Modify: `inference/embedding_test.go`
- Modify: `inference/tracing.go`
- Modify: `inference/tracing_test.go`
- Modify: `internal/modelprovider/openai.go`
- Modify: `internal/modelprovider/google.go`
- Modify: `internal/modelprovider/cohere.go`
- Modify: `internal/modelprovider/voyage.go`
- Modify: `internal/modelprovider/bedrock.go`
- Modify: `internal/modelprovider/sentence_transformers_ort.go`
- Create: `internal/modelprovider/embedding_test_helpers_test.go`
- Modify: provider tests that directly call `EmbeddingTransport.Embed`

**Interfaces:**
- Produces: `NewEmbeddingRequest([]string, EmbeddingInputType, int) (EmbeddingRequest, error)`
- Produces: `EmbeddingRequest.Inputs() []string`
- Produces: `EmbeddingRequest.InputType() EmbeddingInputType`
- Produces: `EmbeddingRequest.DimensionCount() int`
- Changes: `EmbeddingTransport.Embed(context.Context, EmbeddingRequest) (ProviderEmbeddingResult, error)`
- Preserves: `BatchedEmbeddingModel.Embed(context.Context, []string) (EmbeddingResult, error)`

- [ ] **Step 1: Add failing value-object and batch propagation tests**

Add tests in `inference/embedding_test.go` that define the required public contract:

```go
func TestEmbeddingRequestClonesInputsAndReportsDimension(t *testing.T) {
    inputs := []string{"alpha", "beta"}
    request, err := NewEmbeddingRequest(inputs, EmbeddingQuery, 384)
    if err != nil {
        t.Fatal(err)
    }
    inputs[0] = "changed"
    got := request.Inputs()
    got[1] = "mutated"
    if !slices.Equal(request.Inputs(), []string{"alpha", "beta"}) ||
        request.InputType() != EmbeddingQuery || request.DimensionCount() != 384 {
        t.Fatalf("request = %#v", request)
    }
}

func TestBatchedEmbeddingModelSendsProfileDimensionOnEveryBatch(t *testing.T) {
    transport := &recordedEmbeddingTransport{}
    model, err := NewBatchedEmbeddingModel(
        transport,
        testEmbeddingProfile{dimension: 3},
        2,
        nil,
    )
    if err != nil {
        t.Fatal(err)
    }
    if _, err := model.Embed(t.Context(), []string{"a", "b", "c"}); err != nil {
        t.Fatal(err)
    }
    if len(transport.requests) != 2 ||
        transport.requests[0].DimensionCount() != 3 ||
        transport.requests[1].DimensionCount() != 3 {
        t.Fatalf("requests = %#v", transport.requests)
    }
}
```

Add invalid-construction cases for zero dimension and an unknown input type. Expect stable `*ConfigurationError` values and no input text in `Error()`.

- [ ] **Step 2: Run the core tests and verify RED**

Run:

```text
go test -count=1 ./inference -run 'TestEmbeddingRequest|TestBatchedEmbeddingModelSendsProfileDimension'
```

Expected: build failure because `EmbeddingRequest`, `NewEmbeddingRequest`, and the request-based transport method do not exist.

- [ ] **Step 3: Implement EmbeddingRequest and change the interface**

Add the value type to `inference/embedding.go` following the existing `ProviderEmbeddingResult` copy pattern:

```go
type EmbeddingRequest struct {
    inputs    []string
    inputType EmbeddingInputType
    dimension int
}

func NewEmbeddingRequest(inputs []string, inputType EmbeddingInputType, dimension int) (EmbeddingRequest, error) {
    if inputType != EmbeddingDocument && inputType != EmbeddingQuery {
        return EmbeddingRequest{}, NewConfigurationError("request-rejected", "invalid embedding input type")
    }
    if dimension < 1 {
        return EmbeddingRequest{}, NewConfigurationError("dimension-positive", "")
    }
    return EmbeddingRequest{
        inputs: slices.Clone(inputs), inputType: inputType, dimension: dimension,
    }, nil
}

func (r EmbeddingRequest) Inputs() []string              { return slices.Clone(r.inputs) }
func (r EmbeddingRequest) InputType() EmbeddingInputType { return r.inputType }
func (r EmbeddingRequest) DimensionCount() int           { return r.dimension }
```

Change the interface to:

```go
type EmbeddingTransport interface {
    Embed(context.Context, EmbeddingRequest) (ProviderEmbeddingResult, error)
}
```

Construct each batch request in `BatchedEmbeddingModel.Embed` with
`EmbeddingDocument` and `m.profile.dimension`.

- [ ] **Step 4: Update wrappers and all transport signatures without adding wire fields yet**

Update `tracedEmbeddingTransport.Embed` to accept and forward the request unchanged. Update all six built-in transports to unpack:

```go
inputs := request.Inputs()
inputType := request.InputType()
```

Do not use `request.DimensionCount()` in provider wire values in this task.

Create a shared provider-test constructor:

```go
func embeddingRequestForProviderTest(
    t *testing.T,
    inputs []string,
    inputType inference.EmbeddingInputType,
    dimension int,
) inference.EmbeddingRequest {
    t.Helper()
    request, err := inference.NewEmbeddingRequest(inputs, inputType, dimension)
    if err != nil {
        t.Fatal(err)
    }
    return request
}
```

Use it to update every direct provider transport call and real-provider smoke call so the repository compiles under the new public interface.

- [ ] **Step 5: Add and run a tracing-forwarding test**

In `inference/tracing_test.go`, make the wrapped fake capture the full request and assert that dimension `384` survives tracing unchanged. Run:

```text
go test -count=1 ./inference
go test -count=1 ./internal/modelprovider -run '^$'
```

Expected: PASS. The second command is a compile-only gate for every built-in transport and test fake.

- [ ] **Step 6: Run focused formatter and lint checks**

Run:

```text
gofmt -w inference/embedding.go inference/embedding_test.go inference/tracing.go inference/tracing_test.go internal/modelprovider/openai.go internal/modelprovider/google.go internal/modelprovider/cohere.go internal/modelprovider/voyage.go internal/modelprovider/bedrock.go internal/modelprovider/sentence_transformers_ort.go internal/modelprovider/embedding_test_helpers_test.go
go vet ./inference ./internal/modelprovider
```

Expected: PASS.

- [ ] **Step 7: Commit the transport contract**

```text
git add inference internal/modelprovider
git commit \
  -m "Introduce embedding request transport contract" \
  -m "Embedding batches now cross the public transport boundary as one immutable request carrying inputs, input type, and the profile dimension. Built-in transports and wrappers compile against the new contract without changing provider wire behavior yet." \
  -m "Constraint: Keep EmbeddingProfile as the only dimension source and do not add a legacy compatibility interface" \
  -m "Tested: go test -count=1 ./inference and compile-only internal/modelprovider tests" \
  -m "Not-tested: provider dimension wire fields are delivered by the following tasks" \
  -m "Co-authored-by: OmX <omx@oh-my-codex.dev>"
```

### Task 2: Send the configured dimension to OpenAI, VoyageAI, Google, and Cohere

**Files:**
- Modify: `internal/modelprovider/openai.go`
- Modify: `internal/modelprovider/openai_test.go`
- Modify: `internal/modelprovider/voyage.go`
- Modify: `internal/modelprovider/provider_b_test.go`
- Modify: `internal/modelprovider/google.go`
- Modify: `internal/modelprovider/cohere.go`

**Interfaces:**
- Consumes: `EmbeddingRequest.DimensionCount()` from Task 1
- Produces: exact OpenAI, VoyageAI, Google, and Cohere dimension wire fields

- [ ] **Step 1: Add failing OpenAI and VoyageAI wire tests**

Extend `TestOpenAIEmbeddingTransportUsesOrderedBatchAndUsage` to inspect the actual SDK request and require:

```go
if !params.Dimensions.Valid() || params.Dimensions.Value != 384 {
    t.Fatalf("dimensions = %#v", params.Dimensions)
}
```

Extend the VoyageAI HTTP fixture test to decode the request body and require:

```json
{
  "output_dimension": 384
}
```

Call each transport with `embeddingRequestForProviderTest(..., 384)`.

- [ ] **Step 2: Run OpenAI and VoyageAI tests and verify RED**

Run:

```text
go test -count=1 ./internal/modelprovider -run 'TestOpenAIEmbeddingTransportUsesOrderedBatchAndUsage|TestVoyageAIEmbeddingRestoresProviderIndexes'
```

Expected: FAIL because the two request values omit dimension.

- [ ] **Step 3: Implement OpenAI and VoyageAI fields**

Import `github.com/openai/openai-go/v3/packages/param` and set:

```go
Dimensions: param.NewOpt(int64(request.DimensionCount())),
```

Add to `voyageEmbeddingRequest`:

```go
OutputDimension int `json:"output_dimension"`
```

Populate it from `request.DimensionCount()`.

- [ ] **Step 4: Run OpenAI and VoyageAI tests and verify GREEN**

Run the same focused command. Expected: PASS.

- [ ] **Step 5: Add failing Google and Cohere wire tests**

In `TestGoogleOfficialSDKMappingAndEmbeddingTaskDefaults`, require:

```go
if config.OutputDimensionality == nil || *config.OutputDimensionality != 384 {
    t.Fatalf("output dimensionality = %#v", config.OutputDimensionality)
}
```

Add a second Google case that constructs dimension `math.MaxInt32 + 1`, verifies the SDK method is not called, and expects a stable `*inference.ConfigurationError` without the input text.

In the Cohere SDK fake, require:

```go
if request.OutputDimension == nil || *request.OutputDimension != 384 {
    t.Fatalf("output dimension = %#v", request.OutputDimension)
}
```

- [ ] **Step 6: Run Google and Cohere tests and verify RED**

Run:

```text
go test -count=1 ./internal/modelprovider -run 'TestGoogleOfficialSDKMappingAndEmbeddingTaskDefaults|TestCohereOfficialSDKWireAndNoRetry'
```

Expected: FAIL for missing fields; the Google overflow case must show that the SDK call currently occurs or no stable overflow error exists.

- [ ] **Step 7: Implement Google checked conversion and Cohere mapping**

In Google:

```go
if request.DimensionCount() > math.MaxInt32 {
    return inference.ProviderEmbeddingResult{}, inference.NewConfigurationError(
        "request-rejected", "Google embedding dimension exceeds int32",
    )
}
dimension := int32(request.DimensionCount())
config.OutputDimensionality = &dimension
```

In Cohere:

```go
dimension := request.DimensionCount()
providerRequest.OutputDimension = &dimension
```

- [ ] **Step 8: Run all four provider tests and verify GREEN**

Run:

```text
go test -count=1 ./internal/modelprovider -run 'TestOpenAIEmbedding|TestVoyageAIEmbedding|TestGoogleOfficialSDKMappingAndEmbeddingTaskDefaults|TestCohereOfficialSDKWireAndNoRetry'
```

Expected: PASS.

- [ ] **Step 9: Commit remote provider dimension mapping**

```text
git add internal/modelprovider
git commit \
  -m "Send dimensions to remote embedding providers" \
  -m "OpenAI, VoyageAI, Google, and Cohere requests now carry the dimension from EmbeddingRequest, including a checked Google int32 conversion." \
  -m "Constraint: Do not clamp or default configured dimensions and do not move dimension into provider constructors" \
  -m "Tested: focused real SDK and serialized JSON provider wire tests" \
  -m "Not-tested: Bedrock and local Sentence Transformers mappings are delivered separately" \
  -m "Co-authored-by: OmX <omx@oh-my-codex.dev>"
```

### Task 3: Align Bedrock model families and local Sentence Transformers

**Files:**
- Modify: `internal/modelprovider/bedrock.go`
- Modify: `internal/modelprovider/provider_b_test.go`
- Modify: `internal/modelprovider/sentence_transformers_ort.go`
- Modify: `internal/modelprovider/sentence_transformers_ort_test.go`

**Interfaces:**
- Consumes: `EmbeddingRequest`
- Produces: model-family-aware Bedrock dimension mapping
- Produces: local output truncation before common validation

- [ ] **Step 1: Add failing Bedrock model-family table tests**

Add table-driven payload assertions for these exact models:

```text
amazon.titan-embed-text-v1          -> no dimensions
amazon.titan-embed-text-v2:0        -> dimensions: 384
cohere.embed-english-v3             -> no output_dimension
cohere.embed-v4:0                   -> output_dimension: 384
amazon.nova-2-multimodal-embeddings-v1:0
                                      -> singleEmbeddingParams.embeddingDimension: 384
```

Retain the existing input-type and truncation assertions. For Nova, assert the field is inside `singleEmbeddingParams`, not at the top level.

- [ ] **Step 2: Run Bedrock tests and verify RED**

Run:

```text
go test -count=1 ./internal/modelprovider -run 'TestBedrock.*Embedding.*Dimension|TestBedrockConverseAndEmbeddingFormats'
```

Expected: FAIL for the supported model families because dimension fields are absent.

- [ ] **Step 3: Implement explicit Bedrock family/version branches**

Thread dimension through `embedIndividually`, `invokeTitan`, `invokeCohere`, and `invokeNova`.

Use explicit normalized-model checks:

```go
case strings.Contains(model, "amazon.titan-embed-text-v2"):
    payload["dimensions"] = dimension
case strings.Contains(model, "cohere.embed-v4"):
    payload["output_dimension"] = dimension
```

For Nova:

```go
singleEmbeddingParams["embeddingDimension"] = dimension
```

Do not send an override for Titan v1 or Cohere v3. Unknown model families remain rejected by `bedrockEmbeddingKind`.

- [ ] **Step 4: Run Bedrock tests and verify GREEN**

Run the same focused Bedrock command. Expected: PASS.

- [ ] **Step 5: Add failing Sentence Transformers truncation tests**

Add one test where the native run returns two four-dimensional vectors and the request dimension is `2`. Require the result vectors to be the first two components in input order.

Add one test where native output has two dimensions and the request asks for `3`. Require a stable `*inference.ConfigurationError`, no input content in `Error()`, and no padded result.

- [ ] **Step 6: Run local embedding tests and verify RED**

Run:

```text
go test -count=1 -tags 'sqlite_fts5,local_embeddings,ORT' ./internal/modelprovider -run 'TestLocalEmbeddingTransport.*Dimension'
```

Expected: the truncation test returns four values and the oversized request is not classified as a configuration error.

- [ ] **Step 7: Implement truncation before ProviderEmbeddingResult construction**

After the native call returns and before float64 conversion:

```go
dimension := request.DimensionCount()
for index, vector := range result.vectors {
    if len(vector) < dimension {
        return inference.ProviderEmbeddingResult{}, inference.NewConfigurationError(
            "request-rejected", "configured dimension exceeds local model output",
        )
    }
    vector = vector[:dimension]
    vectors[index] = make([]float64, dimension)
    for valueIndex, value := range vector {
        vectors[index][valueIndex] = float64(value)
    }
}
```

Common `BatchedEmbeddingModel` canonicalization remains responsible for unit normalization after truncation.

- [ ] **Step 8: Run Bedrock and local suites and verify GREEN**

Run:

```text
go test -count=1 ./internal/modelprovider
go test -count=1 -tags 'sqlite_fts5,local_embeddings,ORT' ./internal/modelprovider -run 'TestLocalEmbeddingTransport'
```

Expected: PASS on a supported native-asset host. If the local Windows host lacks the required native asset, record that exact gap and require the existing Linux Full exact-Head job; do not weaken the test.

- [ ] **Step 9: Commit Bedrock and local behavior**

```text
git add internal/modelprovider
git commit \
  -m "Align Bedrock and local embedding dimensions" \
  -m "Supported Bedrock model families now receive their exact dimension field, fixed-dimension families omit overrides, and local Sentence Transformers truncate before common validation." \
  -m "Constraint: Keep Titan v1 and Cohere v3 fixed and reject local requests larger than native output" \
  -m "Tested: Bedrock payload tables and focused local embedding dimension tests" \
  -m "Not-tested: full native local-embedding suite requires a supported Linux or macOS asset host" \
  -m "Co-authored-by: OmX <omx@oh-my-codex.dev>"
```

### Task 4: Expose only allowlisted provider-rejection readiness reasons

**Files:**
- Modify: `internal/modelprovider/http_transport.go`
- Create: `internal/modelprovider/http_transport_test.go`
- Modify: `internal/modelprovider/openai.go`
- Modify: `internal/modelprovider/openai_test.go`
- Modify: `internal/runtime/readiness.go`
- Modify: `internal/runtime/readiness_test.go`

**Interfaces:**
- Produces: `ConfigurationError{Code: "provider-rejected", Detail: "HTTP NNN"}` for non-transient 4xx
- Produces: strict `CheckStatus` form `misconfigured: provider-rejected (HTTP NNN)`
- Preserves: plain `misconfigured` for every non-allowlisted configuration error

- [ ] **Step 1: Add failing provider HTTP classification tests**

Create a table around `mapProviderHTTPStatus`:

```go
tests := []struct {
    status int
    wantConfiguration bool
    wantDetail string
    wantUnavailable bool
    wantDeadline bool
}{
    {status: 400, wantConfiguration: true, wantDetail: "HTTP 400"},
    {status: 401, wantConfiguration: true, wantDetail: "HTTP 401"},
    {status: 422, wantConfiguration: true, wantDetail: "HTTP 422"},
    {status: 408, wantDeadline: true},
    {status: 425, wantUnavailable: true},
    {status: 429, wantUnavailable: true},
    {status: 500, wantUnavailable: true},
    {status: 302, wantUnavailable: true},
}
```

Use a sentinel cause containing a credential and URL. Assert `errors.Is` retains the cause when wrapped, while `Error()` and `Detail()` contain neither secret.

- [ ] **Step 2: Run classification tests and verify RED**

Run:

```text
go test -count=1 ./internal/modelprovider -run '^TestMapProviderHTTPStatus'
```

Expected: FAIL because current detail is `provider returned HTTP status N` and 3xx falls into configuration classification.

- [ ] **Step 3: Implement exact status classification**

Refactor `mapProviderHTTPStatus` to:

```go
switch {
case status == http.StatusRequestTimeout || status == http.StatusGatewayTimeout:
    return context.DeadlineExceeded
case status == http.StatusConflict || status == http.StatusTooEarly ||
    status == http.StatusTooManyRequests || status >= http.StatusInternalServerError:
    if cause != nil {
        return inference.WrapUnavailableError(operation, cause)
    }
    return inference.NewUnavailableError(operation)
case status >= http.StatusBadRequest && status < http.StatusInternalServerError:
    detail := fmt.Sprintf("HTTP %d", status)
    if cause != nil {
        return inference.WrapConfigurationError("provider-rejected", detail, cause)
    }
    return inference.NewConfigurationError("provider-rejected", detail)
default:
    if cause != nil {
        return inference.WrapUnavailableError(operation, cause)
    }
    return inference.NewUnavailableError(operation)
}
```

Use existing `WrapConfigurationError` and `WrapUnavailableError`; do not include provider body or URL.

Update `mapOpenAIError` so HTTP status mapping delegates to `mapProviderHTTPStatus` instead of returning an empty-detail provider rejection.

- [ ] **Step 4: Run provider mapping tests and verify GREEN**

Run:

```text
go test -count=1 ./internal/modelprovider -run 'TestMapProviderHTTPStatus|TestOpenAISDKRetriesAreDisabledAndErrorsAreSanitized'
```

Expected: PASS.

- [ ] **Step 5: Add failing Runtime readiness allowlist tests**

In `internal/runtime/readiness_test.go`, add:

```go
func TestDependencyProbePublishesOnlyAllowlistedProviderRejectionReason(t *testing.T) {
    safe := inference.WrapConfigurationError(
        "provider-rejected", "HTTP 400", errors.New("API_KEY=secret https://credential@example.invalid private Memory"),
    )
    status, err := DependencyProbe(func(context.Context) error { return safe }, time.Second)(t.Context())
    if err != nil || status != "misconfigured: provider-rejected (HTTP 400)" {
        t.Fatalf("status = %q, err = %v", status, err)
    }
}
```

Add mutants for arbitrary detail, `HTTP 429`, lowercase/malformed status text, trailing text, and a plain configuration error. All must return plain `misconfigured` except the exact permitted form.

- [ ] **Step 6: Run Runtime readiness tests and verify RED**

Run:

```text
go test -count=1 ./internal/runtime -run 'TestDependencyProbe.*Provider|TestDependencyProbeClassification'
```

Expected: FAIL because current `DependencyProbe` always returns plain `misconfigured` and `CheckStatus.valid()` rejects the new form.

- [ ] **Step 7: Implement strict readiness parsing**

Add an unexported formatter/parser in `internal/runtime/readiness.go`. It must parse the exact `HTTP NNN` detail, reject transient statuses, and construct only:

```text
misconfigured: provider-rejected (HTTP NNN)
```

Extend `CheckStatus.valid()` through that parser; do not use a prefix-only check. In `DependencyProbe`, return the formatted reason only for matching `ConfigurationError` code/detail; otherwise keep `CheckMisconfigured`.

- [ ] **Step 8: Run Runtime and provider suites and verify GREEN**

Run:

```text
go test -count=1 ./internal/modelprovider ./internal/runtime
```

Expected: PASS.

- [ ] **Step 9: Commit safe provider readiness classification**

```text
git add internal/modelprovider internal/runtime
git commit \
  -m "Expose redacted provider readiness reasons" \
  -m "Non-transient provider 4xx failures now carry a status-code-only configuration detail, and Runtime publishes only the exact allowlisted readiness reason." \
  -m "Constraint: Keep transient statuses unavailable and never publish provider bodies, URLs, credentials, or arbitrary configuration text" \
  -m "Tested: provider HTTP classification and Runtime readiness mutant tables" \
  -m "Not-tested: public Server readiness is delivered by the next task" \
  -m "Co-authored-by: OmX <omx@oh-my-codex.dev>"
```

### Task 5: Prove Server composition, external API compatibility, and release parity evidence

**Files:**
- Modify: `server/application_readiness_test.go`
- Modify: `server/application_test.go`
- Modify: `test/downstream/consumer_test.go`
- Modify: `test/api-compat/pre-release.apidiff`
- Modify: `test/conformance/parity-inventory-rules.json`
- Regenerate: `test/conformance/parity-inventory.json`

**Interfaces:**
- Consumes: request-based `EmbeddingTransport` and strict readiness status
- Produces: public Server readiness evidence and external consumer compile proof
- Produces: case-specific mappings for six release-target cases

- [ ] **Step 1: Add failing Server dimension composition and readiness tests**

Add a fake embedding transport that records `EmbeddingRequest.DimensionCount()` during configured readiness. Configure dimension `3`, call the readiness operation, and require the fake to receive `3`.

Add a public readiness response test whose embedding readiness operation returns:

```go
inference.WrapConfigurationError(
    "provider-rejected",
    "HTTP 400",
    errors.New("API_KEY=secret https://credential@example.invalid private Memory"),
)
```

Require the JSON check value to equal:

```text
misconfigured: provider-rejected (HTTP 400)
```

and require the serialized response to contain none of the sentinel text.

Add an explicit no-embedding-configuration test that proves composition returns no embedding model and no embedding readiness operation.

- [ ] **Step 2: Run Server tests and verify RED**

Run:

```text
go test -count=1 ./server -run 'Test.*Embedding.*Dimension|Test.*ProviderRejected.*Readiness|Test.*WithoutEmbeddingConfiguration'
```

Expected: FAIL for missing request dimension assertion or missing public reason.

- [ ] **Step 3: Make the minimum Server test-fixture updates**

Update Server embedding fakes to the new transport method. Do not add Server-specific dimension state; the dimension must arrive through the model request constructed from the existing profile.

No production Server configuration or endpoint code should need a new field.

- [ ] **Step 4: Run Server tests and verify GREEN**

Run the same focused Server command. Expected: PASS.

- [ ] **Step 5: Add an external downstream transport implementation**

Import `github.com/ob-labs/powercontext-go/inference` in `test/downstream/consumer_test.go` and add:

```go
type downstreamEmbeddingTransport struct{}

func (downstreamEmbeddingTransport) Embed(
    _ context.Context,
    request inference.EmbeddingRequest,
) (inference.ProviderEmbeddingResult, error) {
    vectors := make([][]float64, len(request.Inputs()))
    for index := range vectors {
        vectors[index] = make([]float64, request.DimensionCount())
        vectors[index][0] = 1
    }
    return inference.NewProviderEmbeddingResult(
        request.Inputs(), request.InputType(), vectors, inference.Usage{},
    )
}
```

Add a focused test that constructs a profile, builds `BatchedEmbeddingModel`, embeds one input, and proves the external module can consume the new request value.

- [ ] **Step 6: Run downstream consumer and verify GREEN**

Build the standard Server binary and run:

```text
make downstream-compat
```

Expected: PASS on a supported host. If Windows cannot build the CGO Server, run the downstream module's focused compile/unit test and require the Linux exact-Head downstream job for the process suite.

- [ ] **Step 7: Regenerate the deliberate pre-release API baseline**

Run:

```text
make api-baseline
git diff -- test/api-compat/pre-release.apidiff
make api-compat
```

Inspect the baseline diff and confirm it contains the `EmbeddingRequest` addition and the intentional `EmbeddingTransport.Embed` signature change only. Any unrelated exported API change stops the task.

- [ ] **Step 8: Add six exact parity mappings**

Add case rules for:

```text
tests/builtin/runtime/test_composition_embedding.py
  test_embedding_models_send_the_configured_dimension_to_the_provider
  test_embedding_models_without_configuration_return_no_models

tests/builtin/runtime/test_readiness.py
  test_dependency_readiness_probe_surfaces_a_stable_redacted_configuration_reason
  test_dependency_readiness_probe_redacts_plain_configuration_errors

tests/builtin/inference/test_pydantic_ai.py
  test_embedding_adapter_maps_a_rejected_request_to_a_stable_reason_without_leaking_body

tests/test_server.py
  test_server_factory_reports_a_rejected_embedding_request_with_a_redacted_reason
```

Each mapping references the exact new Go test declaration. Do not use a generic file-level placeholder.

- [ ] **Step 9: Regenerate and verify parity inventory**

Run:

```text
go run ./tools/parity-inventory-generate \
  -upstream <exact-powercontext-v0.1.0-checkout>

go run ./tools/parity-inventory-generate \
  -upstream <exact-powercontext-v0.1.0-checkout> \
  -check
```

Inspect the generated diff. Exactly the six named cases must move from pending to mapped; counts must change only by the generator.

- [ ] **Step 10: Commit Server, API, downstream, and parity evidence**

```text
git add server test/downstream test/api-compat test/conformance
git commit \
  -m "Map embedding provider readiness parity" \
  -m "Public Server readiness, the external consumer, API baseline, and case-specific inventory now prove configured dimension propagation and redacted provider rejection behavior." \
  -m "Constraint: Map only the six exact release cases and leave SQLite vector cases pending for the second PR" \
  -m "Tested: focused Server readiness, downstream consumer, api-compat, and exact-upstream parity generation" \
  -m "Not-tested: SQLite dimension drift remains outside this PR" \
  -m "Co-authored-by: OmX <omx@oh-my-codex.dev>"
```

### Task 6: Run exact final validation and submit PR 1

**Files:**
- Review: every file changed by Tasks 1-5
- Update only if evidence requires it: PR body and Issue #3 comment

**Interfaces:**
- Consumes: all prior task outputs
- Produces: one reviewable PR with exact-Head CI evidence

- [ ] **Step 1: Refresh Base and rebuild if main moved**

Run:

```text
git fetch --no-prune origin main
git rev-list --left-right --count origin/main...HEAD
git merge-base --is-ancestor origin/main HEAD
```

If main is not an ancestor, rebase the branch onto current `origin/main`, then rerun every validation step below. Do not reuse pre-rebase evidence.

- [ ] **Step 2: Run focused and cross-layer tests**

Run:

```text
go test -count=1 ./inference ./internal/modelprovider ./internal/runtime
go test -count=1 ./server -run 'Embedding|Readiness'
go test -race -count=1 ./inference ./internal/runtime
```

Expected: PASS on a supported host. Record exact Windows native gaps without weakening commands.

- [ ] **Step 3: Run repository gates tied to the changed surface**

Run:

```text
make api-compat
make contract-test
make lint
make generated-consumers
go mod tidy -diff
go mod verify
git diff --check
```

Run the exact-upstream parity generator `-check` again after every generated or mapping change.

- [ ] **Step 4: Review tests with test-guard rules**

Confirm:

- provider tests assert real SDK fields or serialized JSON;
- no test asserts only an internal helper call;
- network fakes return complete response structures;
- all provider variants are table-driven where setup is shared;
- no test contains provider secrets outside bounded sentinel values;
- every new test names the specific mutation it catches.

- [ ] **Step 5: Reconcile final diff and secrets**

Run:

```text
git diff origin/main...HEAD --stat
git diff origin/main...HEAD --name-only
git diff origin/main...HEAD --check
git status --short --branch
```

Scan the diff for credentials, raw provider bodies, URLs with userinfo, debug statements, unfinished markers, and conflict markers. Confirm no files under `internal/sqlstore` or `integrations` changed.

- [ ] **Step 6: Push and create PR**

Push the branch and create:

```text
feat(inference): pass embedding dimensions to providers
```

The PR body must state:

- Part of #3; it does not close WP1;
- the deliberate public interface change;
- exact Base and Head SHAs;
- local RED/GREEN evidence;
- provider field matrix;
- security/redaction evidence;
- API baseline and downstream evidence;
- exact parity cases mapped;
- Windows/native checks not represented as local passes.

- [ ] **Step 7: Verify GitHub delivery state**

Reconcile:

```text
GitHub changedFiles
REST unique filenames
local current-Base...Head filenames
```

Require exact equality. Verify GitHub Head matches the pushed local Head and Base matches current main. Wait for the exact-Head check set; any Head or Base drift invalidates earlier evidence.

- [ ] **Step 8: Stop only at the requested PR boundary**

Report the PR URL, exact Head/Base, local verification, registered CI state, and remaining PR 2 SQLite work. Do not check the combined WP1 embedding item until both PRs merge and both exact-main post-merge gates succeed.
