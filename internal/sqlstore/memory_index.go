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
	"errors"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/memory"
)

// CompositeMemoryIndex combines orthogonal FTS/vector projections while
// preserving each backend's deterministic channel order.
type CompositeMemoryIndex struct {
	indexes      []MemoryIndex
	capabilities memory.Capabilities
}

func NewCompositeMemoryIndex(indexes ...MemoryIndex) (*CompositeMemoryIndex, error) {
	if len(indexes) == 0 {
		return nil, errors.New("sqlstore: composite Memory index requires at least one index")
	}
	result := &CompositeMemoryIndex{indexes: append([]MemoryIndex(nil), indexes...)}
	for _, index := range result.indexes {
		if index == nil {
			return nil, errors.New("sqlstore: composite Memory index must not contain nil")
		}
		capabilities := index.Capabilities()
		result.capabilities.FTS = result.capabilities.FTS || capabilities.FTS
		result.capabilities.Vector = result.capabilities.Vector || capabilities.Vector
		if capabilities.Vector {
			if capabilities.EmbeddingProfile == nil {
				return nil, errors.New("sqlstore: vector Memory index has no embedding profile")
			}
			if result.capabilities.EmbeddingProfile != nil && *result.capabilities.EmbeddingProfile != *capabilities.EmbeddingProfile {
				return nil, errors.New("sqlstore: vector Memory indexes use different embedding profiles")
			}
			profile := *capabilities.EmbeddingProfile
			result.capabilities.EmbeddingProfile = &profile
		}
	}
	result.capabilities.Hybrid = result.capabilities.FTS && result.capabilities.Vector
	return result, nil
}

func (i *CompositeMemoryIndex) Capabilities() memory.Capabilities {
	result := i.capabilities
	if result.EmbeddingProfile != nil {
		profile := *result.EmbeddingProfile
		result.EmbeddingProfile = &profile
	}
	return result
}

func (i *CompositeMemoryIndex) Initialize(ctx context.Context, db DBTX) error {
	for _, index := range i.indexes {
		if err := index.Initialize(ctx, db); err != nil {
			return err
		}
	}
	return nil
}

func (i *CompositeMemoryIndex) Replace(
	ctx context.Context,
	db DBTX,
	scopeID string,
	ref artifact.Ref,
	projections []memory.Projection,
) error {
	for _, index := range i.indexes {
		if err := index.Replace(ctx, db, scopeID, ref, projections); err != nil {
			return err
		}
	}
	return nil
}

func (i *CompositeMemoryIndex) Search(
	ctx context.Context,
	db DBTX,
	scopeID string,
	request memory.SearchRequest,
) (memory.SearchChannels, error) {
	result := memory.SearchChannels{}
	for _, index := range i.indexes {
		channels, err := index.Search(ctx, db, scopeID, request)
		if err != nil {
			return memory.SearchChannels{}, err
		}
		result.FTS = append(result.FTS, channels.FTS...)
		result.Vector = append(result.Vector, channels.Vector...)
	}
	return result, nil
}

func (i *CompositeMemoryIndex) VectorComplete(
	ctx context.Context,
	db DBTX,
	scopeID string,
	memories []artifact.Ref,
	profile memory.EmbeddingProfile,
) (bool, error) {
	found := false
	for _, index := range i.indexes {
		if !index.Capabilities().Vector {
			continue
		}
		found = true
		complete, err := index.VectorComplete(ctx, db, scopeID, memories, profile)
		if err != nil || !complete {
			return false, err
		}
	}
	return found, nil
}

func (i *CompositeMemoryIndex) Hydrate(
	ctx context.Context,
	db DBTX,
	scopeID string,
	projections []memory.Projection,
) ([]memory.Projection, error) {
	result := cloneMemoryProjections(projections)
	for _, index := range i.indexes {
		var err error
		result, err = index.Hydrate(ctx, db, scopeID, result)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// MemoryIndex is a rebuildable projection updated in the authoritative Memory
// transaction.
type MemoryIndex interface {
	Capabilities() memory.Capabilities
	Initialize(context.Context, DBTX) error
	Replace(context.Context, DBTX, string, artifact.Ref, []memory.Projection) error
	Search(context.Context, DBTX, string, memory.SearchRequest) (memory.SearchChannels, error)
	VectorComplete(context.Context, DBTX, string, []artifact.Ref, memory.EmbeddingProfile) (bool, error)
	Hydrate(context.Context, DBTX, string, []memory.Projection) ([]memory.Projection, error)
}

// NoMemoryIndex exposes an authoritative store without search capabilities.
type NoMemoryIndex struct{}

func (NoMemoryIndex) Capabilities() memory.Capabilities { return memory.Capabilities{} }
func (NoMemoryIndex) Initialize(context.Context, DBTX) error {
	return nil
}

func (NoMemoryIndex) Replace(context.Context, DBTX, string, artifact.Ref, []memory.Projection) error {
	return nil
}

func (NoMemoryIndex) Search(
	context.Context,
	DBTX,
	string,
	memory.SearchRequest,
) (memory.SearchChannels, error) {
	return memory.SearchChannels{}, &memory.CapabilityNotSupportedError{Capability: "fts"}
}

func (NoMemoryIndex) VectorComplete(
	context.Context,
	DBTX,
	string,
	[]artifact.Ref,
	memory.EmbeddingProfile,
) (bool, error) {
	return false, nil
}

func (NoMemoryIndex) Hydrate(
	_ context.Context,
	_ DBTX,
	_ string,
	projections []memory.Projection,
) ([]memory.Projection, error) {
	return cloneMemoryProjections(projections), nil
}

func cloneMemoryProjections(values []memory.Projection) []memory.Projection {
	result := make([]memory.Projection, len(values))
	for index, value := range values {
		cloned, err := memory.NewProjection(
			value.EntryVersion(),
			value.SearchableText(),
			value.Embedding(),
			value.EmbeddingContentHash(),
		)
		if err != nil {
			panic(err)
		}
		result[index] = cloned
	}
	return result
}
