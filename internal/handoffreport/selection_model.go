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

package handoffreport

import (
	"encoding/json"

	"github.com/ob-labs/powercontext-go/artifact"
)

type SelectionEntry struct {
	scopeID            string
	workstreamRevision int
	status             SelectionStatus
	handoffRef         *artifact.Ref
}

func NewSelectionEntry(scopeID string, revision int, status SelectionStatus, ref *artifact.Ref) (SelectionEntry, error) {
	value := SelectionEntry{scopeID, revision, status, cloneArtifactRef(ref)}
	if err := value.Validate(); err != nil {
		return SelectionEntry{}, err
	}
	return value, nil
}

func (v SelectionEntry) Validate() error {
	if err := requireText("scope_id", v.scopeID, MaxScopeIDLength); err != nil {
		return err
	}
	if v.workstreamRevision < 1 {
		return fieldError("workstream_revision", "must be positive")
	}
	if v.status == SelectionSelected {
		if v.handoffRef == nil || v.handoffRef.Validate() != nil || v.handoffRef.Family() != "handoff" {
			return fieldError("handoff_ref", "must be an exact Handoff reference")
		}
	} else if v.status == SelectionNoHandoff {
		if v.handoffRef != nil {
			return fieldError("handoff_ref", "must be null for no_handoff")
		}
	} else {
		return fieldError("status", "has an unsupported value")
	}
	return nil
}
func (v SelectionEntry) ScopeID() string           { return v.scopeID }
func (v SelectionEntry) WorkstreamRevision() int   { return v.workstreamRevision }
func (v SelectionEntry) Status() SelectionStatus   { return v.status }
func (v SelectionEntry) HandoffRef() *artifact.Ref { return cloneArtifactRef(v.handoffRef) }
func (v SelectionEntry) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"scope_id": v.scopeID, "workstream_revision": v.workstreamRevision, "status": v.status, "handoff_ref": artifactRefMap(v.handoffRef)})
}
