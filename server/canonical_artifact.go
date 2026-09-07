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

	canonicalartifact "github.com/ob-labs/powercontext-go/api/canonical/artifacts"
)

// Authentication is enforced by the outer httpapi.Wrap before routing.
type canonicalArtifactSecurity struct{}

func (canonicalArtifactSecurity) HandleBearerAuth(ctx context.Context, _ canonicalartifact.OperationName, _ canonicalartifact.BearerAuth) (context.Context, error) {
	return ctx, nil
}

type canonicalArtifactSidecar struct {
	next      http.Handler
	artifacts *canonicalartifact.Server
}

func (h canonicalArtifactSidecar) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, found := h.artifacts.FindPath(r.Method, r.URL); found {
		h.artifacts.ServeHTTP(w, r)
		return
	}
	h.next.ServeHTTP(w, r)
}
