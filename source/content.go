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

package source

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

const ContentType = "content"

// ContentCapture is text supplied by an integration with a caller-stable
// identity. Metadata is copied recursively at construction and access.
type ContentCapture struct {
	sourceID string
	content  string
	metadata map[string]any
}

func NewContentCapture(sourceID, content string, metadata map[string]any) (ContentCapture, error) {
	if strings.TrimSpace(sourceID) == "" || strings.TrimSpace(content) == "" {
		return ContentCapture{}, fmt.Errorf("content text must not be blank")
	}
	cloned, err := cloneJSONObject(metadata)
	if err != nil {
		return ContentCapture{}, err
	}
	return ContentCapture{sourceID: sourceID, content: content, metadata: cloned}, nil
}

func (c ContentCapture) ID() string      { return c.sourceID }
func (c ContentCapture) Content() string { return c.content }
func (c ContentCapture) Metadata() map[string]any {
	value, _ := cloneJSONObject(c.metadata)
	return value
}

// ContentSource is captured neutral text usable as Artifact evidence.
type ContentSource struct {
	name            string
	materialization Materialization
	description     *string
	content         string
	metadata        map[string]any
}

func (s ContentSource) SourceName() string { return s.name }
func (s ContentSource) SourceMaterialization() Materialization {
	return s.materialization
}

func (s ContentSource) SourceDescription() (string, bool) {
	if s.description == nil {
		return "", false
	}
	return *s.description, true
}
func (s ContentSource) Content() string { return s.content }
func (s ContentSource) Metadata() map[string]any {
	value, _ := cloneJSONObject(s.metadata)
	return value
}

// RestoreContentSource reconstructs a persisted Content Source. Unlike
// ContentCapture, persisted content is allowed to be empty because that is a
// valid ContentSource payload in the Python v0.0.1 storage schema.
func RestoreContentSource(
	name string,
	materialization Materialization,
	description *string,
	content string,
	metadata map[string]any,
) (ContentSource, error) {
	value := ContentSource{
		name:            name,
		materialization: materialization,
		description:     cloneOptionalText(description),
		content:         content,
	}
	cloned, err := cloneJSONObject(metadata)
	if err != nil {
		return ContentSource{}, err
	}
	value.metadata = cloned
	if err := validateValue(value); err != nil {
		return ContentSource{}, err
	}
	return value, nil
}

type ContentAdapter struct{}

func (ContentAdapter) Name() string { return ContentType }
func (ContentAdapter) Resolve(_ context.Context, value ContentCapture) (ContentSource, error) {
	metadata, err := cloneJSONObject(value.metadata)
	if err != nil {
		return ContentSource{}, err
	}
	return ContentSource{
		name:            value.sourceID,
		materialization: Captured,
		content:         value.content,
		metadata:        metadata,
	}, nil
}

func (ContentAdapter) Read(_ context.Context, value ContentSource) (ContentCapture, error) {
	return NewContentCapture(value.name, value.content, value.metadata)
}

func RegisterContent(registry *Registry) error {
	return Register(registry, ContentAdapter{})
}

func cloneOptionalText(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneJSONObject(value map[string]any) (map[string]any, error) {
	if value == nil {
		return map[string]any{}, nil
	}
	cloned := make(map[string]any, len(value))
	for key, item := range value {
		copy, err := cloneJSONValue(item)
		if err != nil {
			return nil, fmt.Errorf("metadata %q: %w", key, err)
		}
		cloned[key] = copy
	}
	return cloned, nil
}

func cloneJSONValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch typed := value.(type) {
	case bool, string, json.Number, float32, float64,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64:
		return typed, nil
	case map[string]any:
		return cloneJSONObject(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			cloned, err := cloneJSONValue(item)
			if err != nil {
				return nil, err
			}
			result[index] = cloned
		}
		return result, nil
	default:
		return nil, fmt.Errorf("value of type %s is not JSON-compatible", reflect.TypeOf(value))
	}
}
