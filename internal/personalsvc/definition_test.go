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

package personalsvc_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

func TestRegistrationEncodesAnImmutableCanonicalDefinition(t *testing.T) {
	definition := testDefinition(t)
	registration, err := personalsvc.NewRegistration(definition)
	if err != nil {
		t.Fatalf("NewRegistration() error = %v", err)
	}

	encoded, err := registration.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Fatalf("Encode() = %q, want raw URL-safe base64", encoded)
	}

	payload, decodeErr := base64.RawURLEncoding.DecodeString(encoded)
	if decodeErr != nil {
		t.Fatalf("DecodeString() error = %v", decodeErr)
	}
	const want = `{"binary":"/opt/powercontext/bin/powercontext","data_dir":"/var/lib/powercontext","definition_version":2,"endpoint":"http://127.0.0.1:7614","ownership":"powercontext.personal-server","package_version":"0.2.0"}`
	if string(payload) != want {
		t.Fatalf("canonical payload = %q, want %q", payload, want)
	}

	decoded, err := personalsvc.DecodeRegistration(encoded)
	if err != nil {
		t.Fatalf("DecodeRegistration() error = %v", err)
	}
	if decoded.Definition() != definition {
		t.Fatalf("decoded Definition() = %#v, want %#v", decoded.Definition(), definition)
	}
	if decodedEncoded, encodeErr := decoded.Encode(); encodeErr != nil || decodedEncoded != encoded {
		t.Fatalf("round-trip Encode() = %q, %v; want %q, nil", decodedEncoded, encodeErr, encoded)
	}
}

func TestDefinitionRejectsUnstableAndSensitiveConfiguration(t *testing.T) {
	valid := testDefinitionInput()
	cases := []struct {
		name   string
		mutate func(*personalsvc.DefinitionInput)
		secret string
	}{
		{
			name: "foreign ownership",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.Ownership = "foreign-service"
			},
		},
		{
			name: "future definition version",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.DefinitionVersion = personalsvc.DefinitionVersion + 1
			},
		},
		{
			name: "endpoint user info",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.Endpoint = "http://operator:super-secret@127.0.0.1:7614"
			},
			secret: "super-secret",
		},
		{
			name: "endpoint query",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.Endpoint = "http://127.0.0.1:7614/?token=super-secret"
			},
			secret: "super-secret",
		},
		{
			name: "trimmed binary",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.Binary = " /opt/powercontext/bin/powercontext"
			},
		},
		{
			name: "relative binary",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.Binary = "bin/powercontext"
			},
		},
		{
			name: "oversized data directory",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.DataDir = strings.Repeat("x", personalsvc.MaxDataDirLength+1)
			},
		},
		{
			name: "relative data directory",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.DataDir = "powercontext-data"
			},
		},
		{
			name: "remote HTTP endpoint",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.Endpoint = "http://personal.example.invalid:7614"
			},
		},
		{
			name: "remote HTTPS endpoint",
			mutate: func(value *personalsvc.DefinitionInput) {
				value.Endpoint = "https://personal.example.invalid:7614"
			},
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			_, err := personalsvc.NewDefinition(input)
			if err == nil {
				t.Fatal("NewDefinition() accepted invalid metadata")
			}
			if _, ok := errors.AsType[*personalsvc.InvalidMetadataError](err); !ok {
				t.Fatalf("NewDefinition() error type = %T, want *InvalidMetadataError", err)
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("NewDefinition() error exposed sensitive value: %q", err)
			}
		})
	}
}

func TestDefinitionAcceptsTransportPolicyLoopbackEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"http://localhost:7614",
		"https://127.0.0.2:7614",
		"http://[::1]:7614",
	} {
		t.Run(endpoint, func(t *testing.T) {
			input := testDefinitionInput()
			input.Endpoint = endpoint
			if _, err := personalsvc.NewDefinition(input); err != nil {
				t.Fatalf("NewDefinition() error = %v", err)
			}
		})
	}
}

func TestDecodeRegistrationRejectsNonCanonicalUnknownDuplicateAndSensitiveMetadata(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		secret  string
	}{
		{
			name:    "noncanonical member order",
			payload: `{"data_dir":"/var/lib/powercontext","ownership":"powercontext.personal-server","definition_version":2,"package_version":"0.2.0","binary":"/opt/powercontext/bin/powercontext","endpoint":"http://127.0.0.1:7614"}`,
		},
		{
			name:    "unknown member",
			payload: `{"ownership":"powercontext.personal-server","definition_version":2,"package_version":"0.2.0","binary":"/opt/powercontext/bin/powercontext","endpoint":"http://127.0.0.1:7614","data_dir":"/var/lib/powercontext","mode":"debug"}`,
		},
		{
			name:    "duplicate member",
			payload: `{"ownership":"powercontext.personal-server","definition_version":2,"package_version":"0.2.0","binary":"/opt/powercontext/bin/powercontext","endpoint":"http://127.0.0.1:7614","data_dir":"/var/lib/powercontext","binary":"other"}`,
		},
		{
			name:    "sensitive member",
			payload: `{"ownership":"powercontext.personal-server","definition_version":2,"package_version":"0.2.0","binary":"/opt/powercontext/bin/powercontext","endpoint":"http://127.0.0.1:7614","data_dir":"/var/lib/powercontext","token":"super-secret"}`,
			secret:  "super-secret",
		},
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			encoded := base64.RawURLEncoding.EncodeToString([]byte(test.payload))
			_, err := personalsvc.DecodeRegistration(encoded)
			if err == nil {
				t.Fatal("DecodeRegistration() accepted invalid payload")
			}
			if _, ok := errors.AsType[*personalsvc.InvalidMetadataError](err); !ok {
				t.Fatalf("DecodeRegistration() error type = %T, want *InvalidMetadataError", err)
			}
			if test.secret != "" && strings.Contains(err.Error(), test.secret) {
				t.Fatalf("DecodeRegistration() error exposed sensitive value: %q", err)
			}
		})
	}
}

func TestDecodeRegistrationRejectsInvalidBase64WithoutEchoingPayload(t *testing.T) {
	_, err := personalsvc.DecodeRegistration("not base64=super-secret")
	if err == nil {
		t.Fatal("DecodeRegistration() accepted invalid base64")
	}
	if _, ok := errors.AsType[*personalsvc.InvalidMetadataError](err); !ok {
		t.Fatalf("DecodeRegistration() error type = %T, want *InvalidMetadataError", err)
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("DecodeRegistration() error exposed sensitive value: %q", err)
	}
}

func TestDecodeRegistrationRejectsPaddedAndOversizedEncodings(t *testing.T) {
	registration, err := personalsvc.NewRegistration(testDefinition(t))
	if err != nil {
		t.Fatalf("NewRegistration() error = %v", err)
	}
	encoded, err := registration.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	for _, value := range []string{
		encoded + "=",
		strings.Repeat("A", 16*1024+1),
	} {
		if _, decodeErr := personalsvc.DecodeRegistration(value); decodeErr == nil {
			t.Fatalf("DecodeRegistration(%q) accepted an invalid encoding", value[:min(len(value), 32)])
		}
	}
}

func testDefinition(t *testing.T) personalsvc.Definition {
	t.Helper()
	definition, err := personalsvc.NewDefinition(testDefinitionInput())
	if err != nil {
		t.Fatalf("NewDefinition() error = %v", err)
	}
	return definition
}

func testDefinitionInput() personalsvc.DefinitionInput {
	return personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    "0.2.0",
		Binary:            "/opt/powercontext/bin/powercontext",
		Endpoint:          "http://127.0.0.1:7614",
		DataDir:           "/var/lib/powercontext",
	}
}
