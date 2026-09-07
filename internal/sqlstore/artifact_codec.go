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

package sqlstore

import (
	"context"
	"fmt"
	"reflect"

	"github.com/ob-labs/powercontext-go/artifact"
)

// ArtifactCodec is an exact family route with a finite set of persisted
// content schemas. It is immutable after construction.
type ArtifactCodec struct {
	family        string
	contentTypes  map[reflect.Type]struct{}
	encode        func(any) ([]byte, error)
	decodeContent func([]byte) (any, error)
	decodeScoped  func(context.Context, DBTX, string, []byte) (any, error)
	decode        func(context.Context, DBTX, string, artifact.Ref, artifact.Lineage, []byte) (artifact.Snapshot, error)
}

// NewArtifactCodec constructs an exact concrete content codec.
func NewArtifactCodec[T any](
	family string,
	encode func(T) ([]byte, error),
	decode func([]byte) (T, error),
) (ArtifactCodec, error) {
	if _, err := artifact.NewRef(family, "codec-validation", 1); err != nil {
		return ArtifactCodec{}, err
	}
	contentType := reflect.TypeFor[T]()
	if contentType.Kind() == reflect.Interface {
		return ArtifactCodec{}, fmt.Errorf("sqlstore: Artifact codec content type must be concrete")
	}
	if encode == nil || decode == nil {
		return ArtifactCodec{}, fmt.Errorf("sqlstore: Artifact codec functions must not be nil")
	}
	return ArtifactCodec{
		family:       family,
		contentTypes: map[reflect.Type]struct{}{contentType: {}},
		encode: func(value any) ([]byte, error) {
			if reflect.TypeOf(value) != contentType {
				return nil, fmt.Errorf("sqlstore: Artifact codec %q expected %s, got %T", family, contentType, value)
			}
			return encode(value.(T))
		},
		decodeContent: func(payload []byte) (any, error) {
			return decode(payload)
		},
		decodeScoped: func(_ context.Context, _ DBTX, _ string, payload []byte) (any, error) {
			return decode(payload)
		},
		decode: func(_ context.Context, _ DBTX, _ string, ref artifact.Ref, lineage artifact.Lineage, payload []byte) (artifact.Snapshot, error) {
			content, err := decode(payload)
			if err != nil {
				return nil, err
			}
			return artifact.Restore(ref, content, lineage)
		},
	}, nil
}

func (c ArtifactCodec) supportsContent(value any) bool {
	_, ok := c.contentTypes[reflect.TypeOf(value)]
	return ok
}
