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
	"encoding/json/v2"
	"fmt"
	"strings"
	"testing"

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
