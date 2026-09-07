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
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"strings"
	"time"

	"github.com/ob-labs/powercontext-go/artifact"
)

type InvalidArtifactCursorError struct{}

func (*InvalidArtifactCursorError) Error() string { return "invalid Artifact list cursor" }

type ExpiredArtifactCursorError struct{}

func (*ExpiredArtifactCursorError) Error() string { return "Artifact list cursor has expired" }

type artifactCursor struct {
	key   [32]byte
	clock func() time.Time
}

type artifactCursorPayload struct {
	Version   int    `json:"version"`
	Endpoint  string `json:"endpoint"`
	ScopeID   string `json:"scope_id"`
	Family    string `json:"family"`
	Order     string `json:"order"`
	After     string `json:"after"`
	ExpiresAt int64  `json:"expires_at"`
}

func (c artifactCursor) encode(scopeID, family, after string) (string, error) {
	payload, err := json.Marshal(artifactCursorPayload{
		Version: 1, Endpoint: "list_artifacts", ScopeID: scopeID, Family: family,
		Order: "artifact_id:asc", After: after, ExpiresAt: c.clock().Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", &InvalidArtifactCursorError{}
	}
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (c artifactCursor) decode(token, scopeID, family string) (string, error) {
	if len(token) < 1 || len(token) > 4096 {
		return "", &InvalidArtifactCursorError{}
	}
	body, signature, found := strings.Cut(token, ".")
	if !found {
		return "", &InvalidArtifactCursorError{}
	}
	payload, payloadErr := base64.RawURLEncoding.Strict().DecodeString(body)
	signed, signatureErr := base64.RawURLEncoding.Strict().DecodeString(signature)
	if payloadErr != nil || signatureErr != nil || len(signed) != sha256.Size ||
		base64.RawURLEncoding.EncodeToString(payload) != body || base64.RawURLEncoding.EncodeToString(signed) != signature {
		return "", &InvalidArtifactCursorError{}
	}
	mac := hmac.New(sha256.New, c.key[:])
	_, _ = mac.Write(payload)
	if !hmac.Equal(mac.Sum(nil), signed) {
		return "", &InvalidArtifactCursorError{}
	}
	var value artifactCursorPayload
	if err := json.Unmarshal(payload, &value, json.RejectUnknownMembers(true)); err != nil {
		return "", &InvalidArtifactCursorError{}
	}
	if value.Version != 1 || value.Endpoint != "list_artifacts" || value.ScopeID != scopeID ||
		value.Family != family || value.Order != "artifact_id:asc" || value.ExpiresAt <= 0 {
		return "", &InvalidArtifactCursorError{}
	}
	if _, refErr := artifact.NewRef(family, value.After, 1); refErr != nil {
		return "", &InvalidArtifactCursorError{}
	}
	if c.clock().Unix() >= value.ExpiresAt {
		return "", &ExpiredArtifactCursorError{}
	}
	return value.After, nil
}
