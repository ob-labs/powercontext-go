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

	remoteskills "github.com/ob-labs/powercontext-go/api/canonical/remoteskills"
)

// canonicalRemoteSkillSecurity is permissive because the outer HTTP transport
// owns the static bearer boundary and its single generated enrollment exception.
type canonicalRemoteSkillSecurity struct{}

func (canonicalRemoteSkillSecurity) HandleBearerAuth(
	ctx context.Context,
	_ remoteskills.OperationName,
	_ remoteskills.BearerAuth,
) (context.Context, error) {
	return ctx, nil
}

type canonicalRemoteSkillSidecar struct {
	next   http.Handler
	skills *remoteskills.Server
}

func (h canonicalRemoteSkillSidecar) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, found := h.skills.FindPath(r.Method, r.URL); found {
		h.skills.ServeHTTP(w, r)
		return
	}
	h.next.ServeHTTP(w, r)
}

func isCanonicalRemoteSkillEnrollment(server *remoteskills.Server, request *http.Request) bool {
	if server == nil || request.Method != http.MethodPost {
		return false
	}
	route, found := server.FindPath(request.Method, request.URL)
	return found && route.OperationID() == "enroll_remote_skill_target"
}
