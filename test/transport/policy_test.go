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

package transport_test

import (
	json "encoding/json/v2"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

type loopbackVectors struct {
	Loopback    []string `json:"loopback"`
	NonLoopback []string `json:"non_loopback"`
}

func TestSharedLoopbackHostVectors(t *testing.T) {
	vectors := loadLoopbackVectors(t)
	for _, host := range vectors.Loopback {
		t.Run("loopback/"+host, func(t *testing.T) {
			if !transportpolicy.IsLoopbackHost(host) {
				t.Fatalf("IsLoopbackHost(%q) = false, want true", host)
			}
		})
	}
	for _, host := range vectors.NonLoopback {
		t.Run("non-loopback/"+host, func(t *testing.T) {
			if transportpolicy.IsLoopbackHost(host) {
				t.Fatalf("IsLoopbackHost(%q) = true, want false", host)
			}
		})
	}
}

func TestSharedPlaintextURLVectors(t *testing.T) {
	vectors := loadLoopbackVectors(t)
	for _, host := range vectors.Loopback {
		t.Run("loopback/"+host, func(t *testing.T) {
			endpoint := &url.URL{Scheme: "http", Host: host}
			if transportpolicy.IsPlaintextNonLoopback(endpoint) {
				t.Fatalf("IsPlaintextNonLoopback(%q) = true, want false", endpoint)
			}
		})
	}
	for _, host := range vectors.NonLoopback {
		t.Run("non-loopback/"+host, func(t *testing.T) {
			endpoint := &url.URL{Scheme: "http", Host: host}
			if !transportpolicy.IsPlaintextNonLoopback(endpoint) {
				t.Fatalf("IsPlaintextNonLoopback(%q) = false, want true", endpoint)
			}
			endpoint.Scheme = "https"
			if transportpolicy.IsPlaintextNonLoopback(endpoint) {
				t.Fatalf("IsPlaintextNonLoopback(%q) = true, want false", endpoint)
			}
		})
	}
}

func loadLoopbackVectors(t *testing.T) loopbackVectors {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "loopback_hosts.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors loopbackVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}
