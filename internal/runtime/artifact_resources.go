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
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
)

// ArtifactResourceReader reads a complete revision and lineage atomically.
// A zero revision selects the current head; a positive revision is exact.
type ArtifactResourceReader interface {
	ReadArtifact(context.Context, string, string, string, int64) (artifact.Snapshot, error)
	ReadArtifactPage(context.Context, string, string, string, int) ([]artifact.Snapshot, bool, error)
}

type ArtifactResourceApplication struct {
	runtime *Runtime
	reader  ArtifactResourceReader
	cursor  artifactCursor
}

type ArtifactRecord struct {
	ScopeID string
	Value   artifact.Snapshot
}

type ArtifactPage struct {
	Items      []ArtifactRecord
	NextCursor *string
}

func NewArtifactResourceApplication(runtime *Runtime, reader ArtifactResourceReader, key [32]byte, clock func() time.Time) (*ArtifactResourceApplication, error) {
	if runtime == nil || reader == nil {
		return nil, errors.New("runtime: Artifact resource dependencies must not be nil")
	}
	if clock == nil {
		clock = time.Now
	}
	return &ArtifactResourceApplication{runtime: runtime, reader: reader, cursor: artifactCursor{key: key, clock: clock}}, nil
}

func (a *ArtifactResourceApplication) ListArtifacts(ctx context.Context, scopeID, family string, limit int, cursor *string) (ArtifactPage, error) {
	var result ArtifactPage
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		switch family {
		case "memory", "experience", "skill", "handoff":
		default:
			return &artifact.InvalidReferenceError{Field: "family", Detail: "must be a supported Artifact family"}
		}
		if limit < 1 || limit > 100 {
			return &artifact.InvalidReferenceError{Field: "limit", Detail: "must be between 1 and 100"}
		}
		after := ""
		if cursor != nil {
			var decodeErr error
			after, decodeErr = a.cursor.decode(*cursor, scope, family)
			if decodeErr != nil {
				return decodeErr
			}
		}
		values, hasMore, readErr := a.reader.ReadArtifactPage(ctx, scope, family, after, limit)
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		if readErr != nil {
			return readErr
		}
		result.Items = make([]ArtifactRecord, len(values))
		for index, value := range values {
			result.Items[index] = ArtifactRecord{ScopeID: scope, Value: value}
		}
		if hasMore && len(values) > 0 {
			next, encodeErr := a.cursor.encode(scope, family, values[len(values)-1].Ref().ID())
			if encodeErr != nil {
				return encodeErr
			}
			result.NextCursor = &next
		}
		return ctx.Err()
	})
	if err != nil {
		return ArtifactPage{}, err
	}
	return result, nil
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
