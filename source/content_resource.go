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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"strings"
)

// InvalidContentResourceError refuses content outside the RFC 8785 value domain.
// Its representations never retain the rejected value.
type InvalidContentResourceError struct{}

func (*InvalidContentResourceError) Error() string      { return "invalid Source JSON content" }
func (e *InvalidContentResourceError) GoString() string { return e.Error() }

// NewContentResource creates a captured Source with explicit JSON content.
// Identity allocation belongs to the application, not the caller transport.
func NewContentResource(name string, raw []byte) (ContentSource, error) {
	value, err := RestoreContentSource(name, Captured, nil, "", nil)
	if err != nil {
		return ContentSource{}, err
	}
	return value.WithJSONContent(raw)
}

// WithJSONContent returns an immutable copy carrying the JSON value and its
// standard text evidence. Unlike legacy capture, null and empty strings are valid.
func (s ContentSource) WithJSONContent(raw []byte) (ContentSource, error) {
	canonical, err := canonicalResourceJSON(raw)
	if err != nil {
		return ContentSource{}, err
	}
	text := string(canonical)
	if canonical.Kind() == '"' {
		if err := json.Unmarshal(canonical, &text); err != nil {
			return ContentSource{}, &InvalidContentResourceError{}
		}
	}
	s.content = text
	s.wireContent = bytes.Clone(raw)
	return s, nil
}

// HasJSONContent distinguishes explicit JSON null from a legacy text record.
func (s ContentSource) HasJSONContent() bool { return s.wireContent != nil }

// WireContentJSON preserves integer versus floating-point tokens for durable
// round trips. Canonical JSON alone cannot preserve that domain distinction.
func (s ContentSource) WireContentJSON() []byte { return bytes.Clone(s.wireContent) }

// ContentJSON preserves the supplied JSON value and numeric token category,
// while representing legacy text as a JSON string without parsing it.
func (s ContentSource) ContentJSON() (jsontext.Value, error) {
	if s.HasJSONContent() {
		return bytes.Clone(s.wireContent), nil
	}
	encoded, err := json.Marshal(s.content)
	if err != nil {
		return nil, &InvalidContentResourceError{}
	}
	return canonicalResourceJSON(encoded)
}

func (s ContentSource) ContentDigest() (string, error) {
	encoded, err := s.ContentJSON()
	if err != nil {
		return "", err
	}
	canonical, err := canonicalResourceJSON(encoded)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func canonicalResourceJSON(raw []byte) (jsontext.Value, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.ReadToken()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, &InvalidContentResourceError{}
		}
		if token.Kind() != '0' {
			continue
		}
		// Python's RFC 8785 implementation rejects integer tokens outside its
		// safe integer domain before any binary64 conversion can lose precision.
		if !strings.ContainsAny(token.String(), ".eE") {
			integer, integerErr := token.Int()
			if integerErr != nil || integer < -9007199254740991 || integer > 9007199254740991 {
				return nil, &InvalidContentResourceError{}
			}
		} else if _, floatErr := token.Float(); floatErr != nil {
			return nil, &InvalidContentResourceError{}
		}
	}
	canonical := jsontext.Value(bytes.Clone(raw))
	if err := canonical.Canonicalize(); err != nil {
		return nil, &InvalidContentResourceError{}
	}
	return canonical, nil
}
