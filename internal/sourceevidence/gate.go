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

// Package sourceevidence owns the closed product boundary for Source values
// that may enter Artifact evidence or model input.
package sourceevidence

import (
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/source"
)

// Require admits only exact immutable product Source values. Unsupported
// adapters, wrappers, raw observations, and pointer forms fail closed.
func Require(value source.Value) error {
	switch typed := value.(type) {
	case source.ContentSource, skill.SnapshotSource:
		return nil
	case AcceptedObservation:
		if err := typed.Validate(); err != nil {
			return err
		}
		_, err := typed.TextEvidence()
		return err
	default:
		return &source.UnacceptedObservationError{}
	}
}

// Allows reports whether value passes the complete product evidence gate.
func Allows(value source.Value) bool { return Require(value) == nil }
