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

package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/ob-labs/powercontext-go/artifact"
	internaljcs "github.com/ob-labs/powercontext-go/internal/jcs"
	"github.com/ob-labs/powercontext-go/source"
)

var lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

var (
	entryHashDomain     = []byte("powercontext:entry-content:v1\x00")
	embeddingHashDomain = []byte("powercontext:embedding-content:v1\x00")
)

func CanonicalJSON(value any) ([]byte, error) { return internaljcs.Marshal(value) }

func NormalizeText(value string) (string, error) {
	normalized, err := normalizeNonEmpty(value, "memory entry text")
	if err != nil {
		return "", err
	}
	if len([]byte(normalized)) > 8192 {
		return "", &CanonicalError{Code: "text-too-long"}
	}
	return normalized, nil
}

func NormalizeKind(value string) (string, error) {
	return normalizeNonEmpty(value, "memory entry kind")
}

func NormalizeReason(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	normalized := trimPythonWhitespace(norm.NFC.String(*value))
	if normalized == "" {
		return nil, nil
	}
	if utf8.RuneCountInString(normalized) > 512 {
		return nil, &CanonicalError{Code: "reason-too-long"}
	}
	return &normalized, nil
}

func ValidateIdentifier(value string) (string, error) {
	if value == "" {
		return "", &CanonicalError{Code: "identifier-empty"}
	}
	if !isASCII(value) {
		return "", &CanonicalError{Code: "identifier-ascii"}
	}
	if len(value) > 128 {
		return "", &CanonicalError{Code: "identifier-too-long"}
	}
	return value, nil
}

func ValidateContentHash(value string) (string, error) {
	if !lowerHex64.MatchString(value) {
		return "", &CanonicalError{Code: "hash"}
	}
	return value, nil
}

func EntryContentBytes(kind, text string, sourceRefs []source.Ref, artifactRefs []artifact.Ref) ([]byte, error) {
	normalizedKind, err := NormalizeKind(kind)
	if err != nil {
		return nil, err
	}
	normalizedText, err := NormalizeText(text)
	if err != nil {
		return nil, err
	}
	sources, err := normalizeSourceRefs(sourceRefs)
	if err != nil {
		return nil, err
	}
	artifacts, err := normalizeArtifactRefs(artifactRefs)
	if err != nil {
		return nil, err
	}
	return CanonicalJSON(map[string]any{
		"kind": normalizedKind, "text": normalizedText,
		"source_refs": sources, "artifact_refs": artifacts,
	})
}

func EntryContentHash(kind, text string, sourceRefs []source.Ref, artifactRefs []artifact.Ref) (string, error) {
	contents, err := EntryContentBytes(kind, text, sourceRefs, artifactRefs)
	if err != nil {
		return "", err
	}
	return domainHash(entryHashDomain, contents), nil
}

func ContentBytes(content Content) ([]byte, error) {
	entries := make([]any, 0, len(content.manifest.entries))
	for _, entry := range content.manifest.entries {
		if _, err := ValidateIdentifier(entry.entryID); err != nil {
			return nil, err
		}
		if _, err := ValidateIdentifier(entry.entryVersionID); err != nil {
			return nil, err
		}
		if _, err := ValidateContentHash(entry.entryContentHash); err != nil {
			return nil, err
		}
		entries = append(entries, map[string]any{
			"entry_id": entry.entryID, "entry_version_id": entry.entryVersionID,
			"entry_content_hash": entry.entryContentHash, "state": string(entry.state),
		})
	}
	changes := make([]any, 0, len(content.changes))
	for _, change := range content.changes {
		if _, err := ValidateIdentifier(change.entryID); err != nil {
			return nil, err
		}
		if change.fromEntryVersionID != nil {
			if _, err := ValidateIdentifier(*change.fromEntryVersionID); err != nil {
				return nil, err
			}
		}
		if change.toEntryVersionID != nil {
			if _, err := ValidateIdentifier(*change.toEntryVersionID); err != nil {
				return nil, err
			}
		}
		reason, err := NormalizeReason(change.reason)
		if err != nil {
			return nil, err
		}
		changes = append(changes, map[string]any{
			"op": string(change.op), "entry_id": change.entryID,
			"from_entry_version_id": stringValue(change.fromEntryVersionID),
			"to_entry_version_id":   stringValue(change.toEntryVersionID), "reason": stringValue(reason),
		})
	}
	return CanonicalJSON(map[string]any{
		"schema":   content.Schema(),
		"manifest": map[string]any{"format": content.manifest.Format(), "entries": entries},
		"changes":  changes,
	})
}

func ContentHash(content Content) (string, error) {
	encoded, err := ContentBytes(content)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func EmbeddingContentHash(profile EmbeddingProfile, entryContentHash string) (string, error) {
	if profile.Dimension < 1 {
		return "", &CanonicalError{Code: "dimension-positive"}
	}
	if _, err := ValidateContentHash(entryContentHash); err != nil {
		return "", err
	}
	profileID, err := normalizeNonEmpty(profile.ProfileID, "embedding profile ID")
	if err != nil {
		return "", err
	}
	model, err := normalizeNonEmpty(profile.Model, "embedding model")
	if err != nil {
		return "", err
	}
	distance, err := normalizeNonEmpty(profile.Distance, "embedding distance")
	if err != nil {
		return "", err
	}
	normalization, err := normalizeNonEmpty(profile.Normalization, "embedding normalization")
	if err != nil {
		return "", err
	}
	encoded, err := CanonicalJSON(map[string]any{
		"profile": map[string]any{
			"profile_id": profileID,
			"model":      model,
			"dimension":  profile.Dimension, "distance": distance,
			"normalization": normalization,
		},
		"entry_content_hash": entryContentHash,
	})
	if err != nil {
		return "", err
	}
	return domainHash(embeddingHashDomain, encoded), nil
}

func ValidateEmbedding(values []float64, dimension int) ([]float64, error) {
	if dimension < 1 {
		return nil, &CanonicalError{Code: "dimension-positive"}
	}
	if len(values) != dimension {
		return nil, &CanonicalError{Code: "vector-dimension", Detail: dimension}
	}
	result := slices.Clone(values)
	for _, value := range result {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil, &CanonicalError{Code: "vector-finite"}
		}
	}
	return result, nil
}

func NormalizeEmbedding(values []float64, dimension int) ([]float64, error) {
	vector, err := ValidateEmbedding(values, dimension)
	if err != nil {
		return nil, err
	}
	scale := 0.0
	for _, value := range vector {
		scale = max(scale, math.Abs(value))
	}
	if scale == 0 {
		return nil, &CanonicalError{Code: "vector-zero"}
	}
	normSquared := 0.0
	for index := range vector {
		vector[index] /= scale
		normSquared += vector[index] * vector[index]
	}
	normValue := math.Sqrt(normSquared)
	for index := range vector {
		vector[index] /= normValue
	}
	return vector, nil
}

func CanonicalEmbedding(values []float64, dimension int, normalization string) ([]float64, error) {
	if normalization == "unit" {
		return NormalizeEmbedding(values, dimension)
	}
	return ValidateEmbedding(values, dimension)
}

func normalizeSourceRefs(refs []source.Ref) ([]any, error) {
	values := make([]any, 0, len(refs))
	for _, ref := range refs {
		if _, err := source.NewRef(ref.Type(), ref.ID()); err != nil {
			return nil, err
		}
		values = append(values, map[string]any{"source_type": ref.Type(), "source_id": ref.ID()})
	}
	return normalizeRefValues(values)
}

func normalizeArtifactRefs(refs []artifact.Ref) ([]any, error) {
	values := make([]any, 0, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		values = append(values, map[string]any{
			"family": ref.Family(), "artifact_id": ref.ID(), "revision": ref.Revision(),
		})
	}
	return normalizeRefValues(values)
}

func normalizeRefValues(values []any) ([]any, error) {
	type item struct {
		encoded string
		value   any
	}
	byEncoded := make(map[string]any, len(values))
	for _, value := range values {
		encoded, err := CanonicalJSON(value)
		if err != nil {
			return nil, err
		}
		byEncoded[string(encoded)] = value
	}
	items := make([]item, 0, len(byEncoded))
	for encoded, value := range byEncoded {
		items = append(items, item{encoded: encoded, value: value})
	}
	slices.SortFunc(items, func(left, right item) int { return strings.Compare(left.encoded, right.encoded) })
	result := make([]any, len(items))
	for index, value := range items {
		result[index] = value.value
	}
	return result, nil
}

func normalizeNonEmpty(value, label string) (string, error) {
	normalized := trimPythonWhitespace(norm.NFC.String(value))
	if normalized == "" {
		return "", &CanonicalError{Code: "string-empty", Detail: label}
	}
	return normalized, nil
}

// trimPythonWhitespace matches the frozen Python oracle's str.strip().
// Unicode White_Space is shared by both runtimes, but CPython additionally
// treats the four ASCII information separators U+001C..U+001F as whitespace.
func trimPythonWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || character >= '\u001c' && character <= '\u001f'
	})
}

func domainHash(domain, contents []byte) string {
	hash := sha256.New()
	_, _ = hash.Write(domain)
	_, _ = hash.Write(contents)
	return hex.EncodeToString(hash.Sum(nil))
}

func isASCII(value string) bool {
	for _, character := range []byte(value) {
		if character > 0x7f {
			return false
		}
	}
	return true
}

func stringValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func (p EmbeddingProfile) ID() string                { return p.ProfileID }
func (p EmbeddingProfile) ModelName() string         { return p.Model }
func (p EmbeddingProfile) DimensionCount() int       { return p.Dimension }
func (p EmbeddingProfile) NormalizationMode() string { return p.Normalization }
