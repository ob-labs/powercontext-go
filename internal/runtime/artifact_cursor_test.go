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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestArtifactCursorBindingAndExpiry(t *testing.T) {
	now := time.Unix(1000, 0)
	codec := artifactCursor{key: [32]byte{1}, clock: func() time.Time { return now }}
	token, err := codec.encode("scope-secret", "experience", "Z-secret")
	if err != nil {
		t.Fatal(err)
	}
	if got, decodeErr := codec.decode(token, "scope-secret", "experience"); decodeErr != nil || got != "Z-secret" {
		t.Fatalf("continuation = %q, %v", got, decodeErr)
	}
	again, err := codec.encode("scope-secret", "experience", "Z-secret")
	if err != nil || token != again {
		t.Fatalf("fixed inputs did not produce a deterministic cursor: %v", err)
	}
	for _, replay := range [][2]string{{"other-scope", "experience"}, {"scope-secret", "skill"}} {
		_, replayErr := codec.decode(token, replay[0], replay[1])
		assertInvalidArtifactCursor(t, replayErr)
	}
	now = time.Unix(4599, 999999999)
	if _, decodeErr := codec.decode(token, "scope-secret", "experience"); decodeErr != nil {
		t.Fatalf("cursor expired before deadline: %v", decodeErr)
	}
	now = time.Unix(4600, 0)
	_, expiredErr := codec.decode(token, "scope-secret", "experience")
	if _, ok := errors.AsType[*ExpiredArtifactCursorError](expiredErr); !ok {
		t.Fatalf("deadline error = %T %v", expiredErr, expiredErr)
	}
	_, replayErr := codec.decode(token, "other-scope", "experience")
	assertInvalidArtifactCursor(t, replayErr)
	other := artifactCursor{key: [32]byte{2}, clock: codec.clock}
	_, wrongKeyErr := other.decode(token, "scope-secret", "experience")
	assertInvalidArtifactCursor(t, wrongKeyErr)
}

func TestArtifactCursorRejectsMalformedAndTamperedPayloads(t *testing.T) {
	codec := artifactCursor{key: [32]byte{1}, clock: func() time.Time { return time.Unix(1000, 0) }}
	valid := `{"version":1,"endpoint":"list_artifacts","scope_id":"scope-secret","family":"experience","order":"artifact_id:asc","after":"Z-secret","expires_at":4600}`
	if after, err := codec.decode(signArtifactCursorTestPayload(valid), "scope-secret", "experience"); err != nil || after != "Z-secret" {
		t.Fatalf("independently signed fixture rejected: %q %v", after, err)
	}
	for _, payload := range []string{
		`null`, `[]`, `{}`, valid + `{}`,
		strings.Replace(valid, `"version":1`, `"version":true`, 1),
		strings.Replace(valid, `"version":1`, `"version":2`, 1),
		strings.Replace(valid, `"endpoint":"list_artifacts"`, `"endpoint":"other"`, 1),
		strings.Replace(valid, `"order":"artifact_id:asc"`, `"order":"artifact_id:desc"`, 1),
		strings.Replace(valid, `"after":"Z-secret"`, `"after":null`, 1),
		strings.Replace(valid, `"after":"Z-secret"`, `"after":""`, 1),
		strings.Replace(valid, `"after":"Z-secret"`, `"after":" leading-space"`, 1),
		strings.Replace(valid, `"expires_at":4600`, `"expires_at":"4600"`, 1),
		strings.Replace(valid, `"expires_at":4600`, `"expires_at":0`, 1),
		strings.Replace(valid, `"expires_at":4600`, `"expires_at":4600.5`, 1),
		strings.Replace(valid, `"expires_at":4600`, `"expires_at":null`, 1),
		strings.Replace(valid, `"version":1,`, ``, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(valid, `"version":1`, `"Version":1`, 1),
		strings.Replace(valid, `"version":1`, `"version":1,"extra":"secret"`, 1),
	} {
		t.Run(payload, func(t *testing.T) {
			_, err := codec.decode(signArtifactCursorTestPayload(payload), "scope-secret", "experience")
			assertInvalidArtifactCursor(t, err)
		})
	}
	validToken := signArtifactCursorTestPayload(valid)
	for _, token := range []string{"", "a", ".", "a.b.c", "!.!", validToken + "x", strings.Repeat("x", 4097), strings.Replace(validToken, ".", "=.", 1)} {
		_, err := codec.decode(token, "scope-secret", "experience")
		assertInvalidArtifactCursor(t, err)
	}
}

func signArtifactCursorTestPayload(payload string) string {
	key := [32]byte{1}
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func assertInvalidArtifactCursor(t *testing.T, err error) {
	t.Helper()
	if _, ok := errors.AsType[*InvalidArtifactCursorError](err); !ok {
		t.Fatalf("invalid cursor = %T %v", err, err)
	}
	for _, protected := range []string{"scope-secret", "Z-secret", "extra"} {
		if strings.Contains(fmt.Sprintf("%v %+v %#v", err, err, err), protected) {
			t.Fatalf("cursor error disclosed protected value %q", protected)
		}
	}
}
