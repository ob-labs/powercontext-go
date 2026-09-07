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

	canonicalsource "github.com/ob-labs/powercontext-go/api/canonical/sources"
)

// The outer httpapi.Wrap authenticates before the generated route is reached.
type canonicalSourceSecurity struct{}

func (canonicalSourceSecurity) HandleBearerAuth(ctx context.Context, _ canonicalsource.OperationName, _ canonicalsource.BearerAuth) (context.Context, error) {
	return ctx, nil
}

type canonicalSourceSidecar struct {
	next    http.Handler
	sources *canonicalsource.Server
}

func (h canonicalSourceSidecar) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, found := h.sources.FindPath(r.Method, r.URL); found {
		h.sources.ServeHTTP(w, r)
		return
	}
	h.next.ServeHTTP(w, r)
}
