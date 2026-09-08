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

package server

import (
	"context"
	"net/http"

	canonicalstats "github.com/ob-labs/powercontext-go/api/canonical/stats"
)

// Authentication is enforced by the outer httpapi.Wrap before Stats routing.
type canonicalStatsSecurity struct{}

func (canonicalStatsSecurity) HandleBearerAuth(ctx context.Context, _ canonicalstats.OperationName, _ canonicalstats.BearerAuth) (context.Context, error) {
	return ctx, nil
}

// canonicalStatsSidecar reserves only canonical POST /v1/stats. The frozen
// legacy GET /v1/stats keeps its generated v1 handler and wire contract.
type canonicalStatsSidecar struct {
	next  http.Handler
	stats *canonicalstats.Server
}

func (h canonicalStatsSidecar) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, found := h.stats.FindPath(r.Method, r.URL); found {
		h.stats.ServeHTTP(w, r)
		return
	}
	h.next.ServeHTTP(w, r)
}
