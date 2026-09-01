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

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testReleaseCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testOtherCommit   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testWheelName     = "powercontext-0.1.0-py3-none-any.whl"
	testSdistName     = "powercontext-0.1.0.tar.gz"
	testWheelDigest   = "40c2e87b960db2bbd181cb6d8704bd60415c23cb3f2b619c56b87197b195aa13"
	testSdistDigest   = "0c29bf6333bf7f09b7a85ea94ec22b5c6a35129933cb7c18c0539c305dc8d6bf"
	testAPIBase       = "https://release.test"
)

type releaseFixtureOptions struct {
	annotatedTag          bool
	tagCommit             string
	omitGitHubWheel       bool
	duplicateGitHubWheel  bool
	githubWheelDigest     string
	wheelContents         string
	duplicatePyPIWheel    bool
	pypiWheelDigest       string
	publisherWorkflow     string
	provenanceWheelDigest string
	provenanceStatus      int
}

func TestVerifyReleaseAcceptsOfficialIdentityAssetsAndProvenance(t *testing.T) {
	err := verifyRelease(t.Context(), releaseVerifierConfig{
		ContractPath:  writeTestContract(t),
		GitHubAPIBase: testAPIBase,
		PyPIBase:      testAPIBase,
		HTTPClient:    newReleaseClient(defaultReleaseFixtureOptions()),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerifyReleaseAcceptsAnnotatedTagResolvingToContractCommit(t *testing.T) {
	options := defaultReleaseFixtureOptions()
	options.annotatedTag = true

	err := verifyRelease(t.Context(), releaseVerifierConfig{
		ContractPath:  writeTestContract(t),
		GitHubAPIBase: testAPIBase,
		PyPIBase:      testAPIBase,
		HTTPClient:    newReleaseClient(options),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestVerifyReleaseRejectsIdentityAssetAndProvenanceDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*releaseFixtureOptions)
		want   string
	}{
		{
			name: "tag commit",
			mutate: func(options *releaseFixtureOptions) {
				options.tagCommit = testOtherCommit
			},
			want: "release tag resolves to",
		},
		{
			name: "missing GitHub asset",
			mutate: func(options *releaseFixtureOptions) {
				options.omitGitHubWheel = true
			},
			want: "GitHub release asset " + testWheelName + " is missing",
		},
		{
			name: "duplicate GitHub asset",
			mutate: func(options *releaseFixtureOptions) {
				options.duplicateGitHubWheel = true
			},
			want: "GitHub release asset " + testWheelName + " appears 2 times",
		},
		{
			name: "GitHub asset metadata digest",
			mutate: func(options *releaseFixtureOptions) {
				options.githubWheelDigest = strings.Repeat("c", 64)
			},
			want: "GitHub release asset " + testWheelName + " digest",
		},
		{
			name: "downloaded asset bytes",
			mutate: func(options *releaseFixtureOptions) {
				options.wheelContents = "tampered-release-bytes"
			},
			want: "downloaded GitHub release asset " + testWheelName + " digest",
		},
		{
			name: "duplicate PyPI file",
			mutate: func(options *releaseFixtureOptions) {
				options.duplicatePyPIWheel = true
			},
			want: "PyPI file " + testWheelName + " appears 2 times",
		},
		{
			name: "PyPI file digest",
			mutate: func(options *releaseFixtureOptions) {
				options.pypiWheelDigest = strings.Repeat("d", 64)
			},
			want: "PyPI file " + testWheelName + " digest",
		},
		{
			name: "provenance publisher",
			mutate: func(options *releaseFixtureOptions) {
				options.publisherWorkflow = "other.yml"
			},
			want: "publisher =",
		},
		{
			name: "provenance subject",
			mutate: func(options *releaseFixtureOptions) {
				options.provenanceWheelDigest = strings.Repeat("e", 64)
			},
			want: "subject does not bind",
		},
		{
			name: "provenance unavailable",
			mutate: func(options *releaseFixtureOptions) {
				options.provenanceStatus = http.StatusNotFound
			},
			want: "PyPI provenance for " + testWheelName + " returned HTTP 404",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := defaultReleaseFixtureOptions()
			test.mutate(&options)

			err := verifyRelease(t.Context(), releaseVerifierConfig{
				ContractPath:  writeTestContract(t),
				GitHubAPIBase: testAPIBase,
				PyPIBase:      testAPIBase,
				HTTPClient:    newReleaseClient(options),
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("verifyRelease error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestVerifyReleaseDoesNotExposeHTTPBodiesOrTokens(t *testing.T) {
	const secret = "remote-body-and-token-secret"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return testResponse(request, http.StatusUnauthorized, "text/plain", []byte(secret)), nil
	})}

	err := verifyRelease(t.Context(), releaseVerifierConfig{
		ContractPath:  writeTestContract(t),
		GitHubAPIBase: testAPIBase,
		PyPIBase:      testAPIBase,
		GitHubToken:   secret,
		HTTPClient:    client,
	})
	if err == nil || !strings.Contains(err.Error(), "GitHub tag reference returned HTTP 401") {
		t.Fatalf("verifyRelease error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("verifyRelease error exposed remote body or token: %v", err)
	}
}

func TestVerifyReleaseRejectsPlaintextAPIBaseBeforeSendingToken(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("transport must not run")
	})}
	err := verifyRelease(t.Context(), releaseVerifierConfig{
		ContractPath:  writeTestContract(t),
		GitHubAPIBase: "http://release.test",
		PyPIBase:      testAPIBase,
		GitHubToken:   "credential-must-stay-local",
		HTTPClient:    client,
	})
	if err == nil || !strings.Contains(err.Error(), "GitHub API base: must use HTTPS") {
		t.Fatalf("verifyRelease error = %v", err)
	}
}

func TestReadReleaseContractRejectsAmbiguousDocuments(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{
			name:     "duplicate member",
			contents: `{"schema_version":3,"schema_version":3}`,
		},
		{
			name:     "unknown member",
			contents: `{"schema_version":3,"future_semantics":true}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "contract.json")
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readReleaseContract(path); err == nil {
				t.Fatal("accepted ambiguous release contract")
			}
		})
	}
}

func defaultReleaseFixtureOptions() releaseFixtureOptions {
	return releaseFixtureOptions{
		tagCommit:             testReleaseCommit,
		githubWheelDigest:     testWheelDigest,
		wheelContents:         "wheel-release-bytes",
		pypiWheelDigest:       testWheelDigest,
		publisherWorkflow:     "release.yml",
		provenanceWheelDigest: testWheelDigest,
		provenanceStatus:      http.StatusOK,
	}
}

func newReleaseClient(options releaseFixtureOptions) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var value any
		switch request.URL.Path {
		case "/repos/oceanbase/powercontext/git/ref/tags/powercontext-v0.1.0":
			objectType, objectSHA := "commit", options.tagCommit
			if options.annotatedTag {
				objectType, objectSHA = "tag", "tag-object"
			}
			value = map[string]any{"object": map[string]any{"type": objectType, "sha": objectSHA}}
		case "/repos/oceanbase/powercontext/git/tags/tag-object":
			value = map[string]any{"object": map[string]any{"type": "commit", "sha": options.tagCommit}}
		case "/repos/oceanbase/powercontext/releases/tags/powercontext-v0.1.0":
			assets := []any{}
			wheel := map[string]any{
				"name": testWheelName, "digest": "sha256:" + options.githubWheelDigest,
				"browser_download_url": testAPIBase + "/downloads/wheel",
			}
			if !options.omitGitHubWheel {
				assets = append(assets, wheel)
			}
			if options.duplicateGitHubWheel {
				assets = append(assets, wheel)
			}
			assets = append(assets, map[string]any{
				"name": testSdistName, "digest": "sha256:" + testSdistDigest,
				"browser_download_url": testAPIBase + "/downloads/sdist",
			})
			value = map[string]any{"tag_name": "powercontext-v0.1.0", "assets": assets}
		case "/downloads/wheel":
			return testResponse(request, http.StatusOK, "application/octet-stream", []byte(options.wheelContents)), nil
		case "/downloads/sdist":
			return testResponse(request, http.StatusOK, "application/octet-stream", []byte("sdist-release-bytes")), nil
		case "/pypi/powercontext/0.1.0/json":
			files := []any{
				map[string]any{
					"filename": testWheelName, "packagetype": "bdist_wheel",
					"digests": map[string]any{"sha256": options.pypiWheelDigest},
				},
				map[string]any{
					"filename": testSdistName, "packagetype": "sdist",
					"digests": map[string]any{"sha256": testSdistDigest},
				},
			}
			if options.duplicatePyPIWheel {
				files = append(files, map[string]any{
					"filename": testWheelName, "packagetype": "bdist_wheel",
					"digests": map[string]any{"sha256": options.pypiWheelDigest},
				})
			}
			value = map[string]any{
				"info": map[string]any{"name": "powercontext", "version": "0.1.0"},
				"urls": files,
			}
		default:
			prefix := "/integrity/powercontext/0.1.0/"
			if !strings.HasPrefix(request.URL.Path, prefix) || !strings.HasSuffix(request.URL.Path, "/provenance") {
				return testResponse(request, http.StatusNotFound, "text/plain", nil), nil
			}
			if options.provenanceStatus != http.StatusOK {
				return testResponse(request, options.provenanceStatus, "application/json", nil), nil
			}
			filename := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, prefix), "/provenance")
			digest := testSdistDigest
			if filename == testWheelName {
				digest = options.provenanceWheelDigest
			}
			statement, err := json.Marshal(map[string]any{
				"_type": "https://in-toto.io/Statement/v1",
				"subject": []any{map[string]any{
					"name": filename, "digest": map[string]any{"sha256": digest},
				}},
				"predicateType": "https://docs.pypi.org/attestations/publish/v1",
				"predicate":     nil,
			})
			if err != nil {
				panic(err)
			}
			value = map[string]any{
				"version": 1,
				"attestation_bundles": []any{map[string]any{
					"publisher": map[string]any{
						"kind": "GitHub", "repository": "oceanbase/powercontext", "workflow": options.publisherWorkflow,
					},
					"attestations": []any{map[string]any{
						"version":  1,
						"envelope": map[string]any{"statement": base64.StdEncoding.EncodeToString(statement), "signature": "test-signature"},
					}},
				}},
			}
		}
		contents, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return testResponse(request, http.StatusOK, "application/json", contents), nil
	})}
}

func writeTestContract(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "parity-contract.json")
	contents := fmt.Sprintf(`{
  "schema_version": 3,
  "upstream": {
    "repository": "oceanbase/powercontext",
    "url": "https://github.com/oceanbase/powercontext",
    "default_branch": "master"
  },
  "frozen_release_oracle": {
    "commit": "ffffffffffffffffffffffffffffffffffffffff",
    "baseline": "python-v0.0.2",
    "evidence_directory": "test/conformance/testdata/python-v0.0.2"
  },
  "release_target": {
    "tag": "powercontext-v0.1.0",
    "version": "0.1.0",
    "commit": %q,
    "test_case_count": 812,
    "test_file_count": 132,
    "wheel": {"filename": %q, "sha256": %q},
    "sdist": {"filename": %q, "sha256": %q},
    "provenance": {
      "pypi_project": "powercontext",
      "publisher": {
        "kind": "GitHub",
        "repository": "oceanbase/powercontext",
        "workflow": "release.yml"
      }
    }
  },
  "exact_target_sha": %q,
  "target_test_case_count": 812,
  "active_parity_target": %q
}
`, testReleaseCommit, testWheelName, testWheelDigest, testSdistName, testSdistDigest, testReleaseCommit, testReleaseCommit)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func testResponse(request *http.Request, status int, contentType string, contents []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(bytes.NewReader(contents)),
		Request:    request,
	}
}
