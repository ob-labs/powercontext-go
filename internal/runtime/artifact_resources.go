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

package runtime

import (
	"context"
	"errors"

	"github.com/ob-labs/powercontext-go/artifact"
)

// ArtifactResourceReader reads a complete revision and lineage atomically.
// A zero revision selects the current head; a positive revision is exact.
type ArtifactResourceReader interface {
	ReadArtifact(context.Context, string, string, string, int64) (artifact.Snapshot, error)
}

type ArtifactResourceApplication struct {
	runtime *Runtime
	reader  ArtifactResourceReader
}

type ArtifactRecord struct {
	ScopeID string
	Value   artifact.Snapshot
}

func NewArtifactResourceApplication(runtime *Runtime, reader ArtifactResourceReader) (*ArtifactResourceApplication, error) {
	if runtime == nil || reader == nil {
		return nil, errors.New("runtime: Artifact resource dependencies must not be nil")
	}
	return &ArtifactResourceApplication{runtime: runtime, reader: reader}, nil
}

func (a *ArtifactResourceApplication) GetArtifact(ctx context.Context, scopeID, family, artifactID string, revision int64) (ArtifactRecord, error) {
	var result ArtifactRecord
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		switch family {
		case "memory", "experience", "skill", "handoff":
		default:
			return &artifact.InvalidReferenceError{Field: "family", Detail: "must be a supported Artifact family"}
		}
		if revision < 0 {
			return &artifact.InvalidReferenceError{Field: "revision", Detail: "must not be negative"}
		}
		if _, err := artifact.NewRef(family, artifactID, max(1, revision)); err != nil {
			return err
		}
		value, err := a.reader.ReadArtifact(ctx, scope, family, artifactID, revision)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		result = ArtifactRecord{ScopeID: scope, Value: value}
		return nil
	})
	return result, err
}
