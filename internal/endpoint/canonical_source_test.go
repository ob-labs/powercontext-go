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

package endpoint

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"

	canonicalsource "github.com/ob-labs/powercontext-go/api/canonical/sources"
	"github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/source"
)

func TestSourceResourceErrorsPreserveKindAndRedactProtectedValues(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&source.ResourceConflictError{}, 409, "idempotency_conflict"},
		{&source.ResourceNotFoundError{}, 404, "source_not_found"},
		{&source.InvalidContentResourceError{}, 422, "invalid_request"},
	} {
		mapped := MapError(fmt.Errorf("private-source private-scope private-content: %w", tc.err))
		if mapped.StatusCode != tc.status || mapped.Code != tc.code {
			t.Fatalf("error mapping = %#v", mapped)
		}
		if tc.status == 409 && mapped.Details["kind"] != "source" {
			t.Fatalf("conflict kind = %#v", mapped.Details)
		}
		encoded, err := json.Marshal(mapped)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "private-") {
			t.Fatalf("protected value in mapping: %s", encoded)
		}
	}
}

func TestSourceIngestionErrorsUseExactRedactedWireCodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "Definition conflict", err: &source.DefinitionConflictError{}, status: 409, code: "source_conflict"},
		{name: "Observation conflict", err: &source.ObservationConflictError{}, status: 409, code: "source_conflict"},
		{name: "Definition missing", err: &source.DefinitionNotFoundError{}, status: 404, code: "source_definition_not_found"},
		{name: "Invalid Definition", err: &source.InvalidDefinitionManifestError{}, status: 422, code: "invalid_source_ingestion"},
		{name: "Invalid observation", err: &source.InvalidSourceObservationError{}, status: 422, code: "invalid_source_ingestion"},
		{name: "Invalid checkpoint", err: &source.InvalidConnectorRunError{}, status: 422, code: "invalid_request"},
		{name: "Checkpoint conflict", err: &runtime.ConnectorCheckpointConflictError{}, status: 409, code: "connector_checkpoint_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mapped := MapError(errors.Join(errors.New("private-source-value"), tc.err))
			if mapped.StatusCode != tc.status || mapped.Code != tc.code {
				t.Fatalf("MapError() = %#v", mapped)
			}
			encoded, err := json.Marshal(mapped)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "private-source-value") {
				t.Fatalf("protected value in mapping: %s", encoded)
			}
		})
	}
}

func TestCanonicalSourceIngestionWireConversionsPreserveJSONValues(t *testing.T) {
	identity, err := source.NewDefinitionIdentity("remote.endpoint", "1")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(identity, jsontext.Value(`{"type":"object"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	manifestPayload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var requestManifest canonicalsource.SourceDefinitionManifest
	if manifestUnmarshalErr := json.Unmarshal(manifestPayload, &requestManifest); manifestUnmarshalErr != nil {
		t.Fatal(manifestUnmarshalErr)
	}
	parsedManifest, err := canonicalSourceDefinition(requestManifest)
	if err != nil || parsedManifest.Fingerprint() != manifest.Fingerprint() {
		t.Fatalf("canonicalSourceDefinition() = %#v, %v", parsedManifest, err)
	}
	responseManifest, err := canonicalSourceDefinitionResponse(manifest)
	if err != nil || responseManifest.Fingerprint != manifest.Fingerprint() {
		t.Fatalf("canonicalSourceDefinitionResponse() = %#v, %v", responseManifest, err)
	}

	ref, err := source.NewRef(manifest.Name(), "observation")
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(
		ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"observation","definition_version":"1","materialization":"captured","large":9007199254740993}`), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	observationPayload, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	var requestObservation canonicalsource.SourceObservation
	if observationUnmarshalErr := json.Unmarshal(observationPayload, &requestObservation); observationUnmarshalErr != nil {
		t.Fatal(observationUnmarshalErr)
	}
	parsedObservation, err := canonicalSourceObservation(requestObservation)
	if err != nil || !bytes.Contains(parsedObservation.Payload(), []byte("9007199254740993")) {
		t.Fatalf("canonicalSourceObservation() = %#v, %v", parsedObservation, err)
	}

	empty := canonicalConnectorCheckpointState(canonicalsource.ConnectorBinding{}, source.NoConnectorCheckpoint())
	if string(empty.Checkpoint) != "null" {
		t.Fatalf("absent checkpoint = %s", empty.Checkpoint)
	}
	checkpoint, err := source.NewConnectorCheckpoint(jsontext.Value(`{"offset":9007199254740993}`))
	if err != nil {
		t.Fatal(err)
	}
	state := canonicalConnectorCheckpointState(canonicalsource.ConnectorBinding{}, checkpoint)
	if !bytes.Contains(state.Checkpoint, []byte("9007199254740993")) {
		t.Fatalf("checkpoint = %s", state.Checkpoint)
	}
}
