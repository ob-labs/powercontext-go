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

package modelprovider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/inference"
)

func TestMapProviderHTTPStatus(t *testing.T) {
	t.Parallel()
	cause := errors.New("API_KEY=secret https://credential@example.invalid")
	for _, test := range []struct {
		status            int
		wantConfiguration bool
		wantDetail        string
		wantUnavailable   bool
		wantDeadline      bool
	}{
		{status: http.StatusBadRequest, wantConfiguration: true, wantDetail: "HTTP 400"},
		{status: http.StatusUnauthorized, wantConfiguration: true, wantDetail: "HTTP 401"},
		{status: http.StatusUnprocessableEntity, wantConfiguration: true, wantDetail: "HTTP 422"},
		{status: http.StatusRequestTimeout, wantDeadline: true},
		{status: http.StatusConflict, wantUnavailable: true},
		{status: http.StatusTooEarly, wantUnavailable: true},
		{status: http.StatusTooManyRequests, wantUnavailable: true},
		{status: http.StatusInternalServerError, wantUnavailable: true},
		{status: http.StatusGatewayTimeout, wantDeadline: true},
		{status: http.StatusFound, wantUnavailable: true},
		{status: 299, wantUnavailable: true},
	} {
		name := http.StatusText(test.status)
		if name == "" {
			name = "unrecognized status"
		}
		t.Run(name, func(t *testing.T) {
			err := mapProviderHTTPStatus(test.status, "embed", cause)
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "credential") {
				t.Fatalf("error leaked cause: %q", err)
			}
			if test.wantDeadline {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("error = %v, want deadline", err)
				}
				return
			}
			if !errors.Is(err, cause) {
				t.Fatalf("errors.Is(%v, cause) = false", err)
			}
			if test.wantUnavailable {
				if _, ok := errors.AsType[*inference.UnavailableError](err); !ok {
					t.Fatalf("error = %T, want unavailable", err)
				}
				return
			}
			configuration, ok := errors.AsType[*inference.ConfigurationError](err)
			if !test.wantConfiguration || !ok || configuration.Code() != "provider-rejected" || configuration.Detail() != test.wantDetail {
				t.Fatalf("configuration = %#v, want provider-rejected %q", configuration, test.wantDetail)
			}
			if strings.Contains(configuration.Detail(), "secret") || strings.Contains(configuration.Detail(), "credential") {
				t.Fatalf("detail leaked cause: %q", configuration.Detail())
			}
		})
	}
}
