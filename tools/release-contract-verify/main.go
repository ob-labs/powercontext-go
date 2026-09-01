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

// Command release-contract-verify compares the checked-in PowerContext
// release contract with the current official GitHub release and PyPI
// provenance records.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultContractPath = "test/conformance/parity-contract.json"
	defaultGitHubAPI    = "https://api.github.com"
	defaultPyPIBase     = "https://pypi.org"
	maxJSONBytes        = 8 << 20
	maxAssetBytes       = 64 << 20
	maxAnnotatedTags    = 8
	statementType       = "https://in-toto.io/Statement/v1"
	publishPredicate    = "https://docs.pypi.org/attestations/publish/v1"
)

type releaseVerifierConfig struct {
	ContractPath  string
	GitHubAPIBase string
	PyPIBase      string
	GitHubToken   string
	HTTPClient    *http.Client
}

type releaseAsset struct {
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
}

type releaseContract struct {
	SchemaVersion int `json:"schema_version"`
	Upstream      struct {
		Repository    string `json:"repository"`
		URL           string `json:"url"`
		DefaultBranch string `json:"default_branch"`
	} `json:"upstream"`
	FrozenReleaseOracle struct {
		Commit            string `json:"commit"`
		Baseline          string `json:"baseline"`
		EvidenceDirectory string `json:"evidence_directory"`
	} `json:"frozen_release_oracle"`
	ReleaseTarget struct {
		Tag           string       `json:"tag"`
		Version       string       `json:"version"`
		Commit        string       `json:"commit"`
		TestCaseCount int          `json:"test_case_count"`
		TestFileCount int          `json:"test_file_count"`
		Wheel         releaseAsset `json:"wheel"`
		Sdist         releaseAsset `json:"sdist"`
		Provenance    struct {
			PyPIProject string `json:"pypi_project"`
			Publisher   struct {
				Kind       string `json:"kind"`
				Repository string `json:"repository"`
				Workflow   string `json:"workflow"`
			} `json:"publisher"`
		} `json:"provenance"`
	} `json:"release_target"`
	ExactTargetSHA      string `json:"exact_target_sha"`
	TargetTestCaseCount int    `json:"target_test_case_count"`
	ActiveParityTarget  string `json:"active_parity_target"`
}

type gitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

type gitReference struct {
	Object gitObject `json:"object"`
}

type annotatedTag struct {
	Object gitObject `json:"object"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		Digest             string `json:"digest"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type pypiRelease struct {
	Info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"info"`
	URLs []struct {
		Filename    string `json:"filename"`
		PackageType string `json:"packagetype"`
		Digests     struct {
			SHA256 string `json:"sha256"`
		} `json:"digests"`
	} `json:"urls"`
}

type pypiProvenance struct {
	Version            int `json:"version"`
	AttestationBundles []struct {
		Publisher struct {
			Kind       string `json:"kind"`
			Repository string `json:"repository"`
			Workflow   string `json:"workflow"`
		} `json:"publisher"`
		Attestations []struct {
			Version  int `json:"version"`
			Envelope struct {
				Statement string `json:"statement"`
			} `json:"envelope"`
		} `json:"attestations"`
	} `json:"attestation_bundles"`
}

type inTotoStatement struct {
	Type    string `json:"_type"`
	Subject []struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	} `json:"subject"`
	PredicateType string `json:"predicateType"`
}

func main() {
	contractPath := flag.String("contract", defaultContractPath, "release parity contract")
	githubAPIBase := flag.String("github-api-base", defaultGitHubAPI, "GitHub API base URL")
	pypiBase := flag.String("pypi-base", defaultPyPIBase, "PyPI API base URL")
	timeout := flag.Duration("timeout", 2*time.Minute, "overall verification timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	err := verifyRelease(ctx, releaseVerifierConfig{
		ContractPath:  *contractPath,
		GitHubAPIBase: *githubAPIBase,
		PyPIBase:      *pypiBase,
		GitHubToken:   os.Getenv("GITHUB_TOKEN"),
		HTTPClient:    http.DefaultClient,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "release-contract-verify:", err)
		os.Exit(1)
	}
	fmt.Println("verified release contract")
}

func verifyRelease(ctx context.Context, config releaseVerifierConfig) error {
	contract, err := readReleaseContract(config.ContractPath)
	if err != nil {
		return fmt.Errorf("read release contract: %w", err)
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	owner, repository, err := splitRepository(contract.Upstream.Repository)
	if err != nil {
		return err
	}
	githubBase, err := normalizedBaseURL(config.GitHubAPIBase, defaultGitHubAPI)
	if err != nil {
		return fmt.Errorf("GitHub API base: %w", err)
	}
	pypiBase, err := normalizedBaseURL(config.PyPIBase, defaultPyPIBase)
	if err != nil {
		return fmt.Errorf("PyPI API base: %w", err)
	}

	commit, err := resolveReleaseTag(ctx, client, githubBase, config.GitHubToken, owner, repository, contract.ReleaseTarget.Tag)
	if err != nil {
		return err
	}
	if commit != contract.ReleaseTarget.Commit {
		return fmt.Errorf("release tag resolves to %s, want contract commit %s", commit, contract.ReleaseTarget.Commit)
	}
	if err := verifyGitHubRelease(ctx, client, githubBase, config.GitHubToken, owner, repository, contract); err != nil {
		return err
	}
	if err := verifyPyPIRelease(ctx, client, pypiBase, contract); err != nil {
		return err
	}
	return nil
}

func readReleaseContract(path string) (releaseContract, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return releaseContract{}, err
	}
	var contract releaseContract
	if err := json.Unmarshal(contents, &contract, json.RejectUnknownMembers(true)); err != nil {
		return releaseContract{}, err
	}
	if contract.SchemaVersion != 3 {
		return releaseContract{}, fmt.Errorf("unsupported schema version %d", contract.SchemaVersion)
	}
	if _, _, err := splitRepository(contract.Upstream.Repository); err != nil {
		return releaseContract{}, err
	}
	if contract.ReleaseTarget.Tag == "" || contract.ReleaseTarget.Version == "" || contract.ReleaseTarget.Commit == "" {
		return releaseContract{}, errors.New("release target tag, version, and commit are required")
	}
	if contract.ExactTargetSHA != contract.ReleaseTarget.Commit || contract.ActiveParityTarget != contract.ReleaseTarget.Commit {
		return releaseContract{}, errors.New("release commit, exact target SHA, and active parity target must match")
	}
	if contract.ReleaseTarget.Wheel.Filename == contract.ReleaseTarget.Sdist.Filename {
		return releaseContract{}, errors.New("wheel and sdist filenames must be distinct")
	}
	for _, asset := range []releaseAsset{contract.ReleaseTarget.Wheel, contract.ReleaseTarget.Sdist} {
		if asset.Filename == "" || !validSHA256(asset.SHA256) {
			return releaseContract{}, fmt.Errorf("release asset %q has an invalid SHA-256", asset.Filename)
		}
	}
	provenance := contract.ReleaseTarget.Provenance
	if provenance.PyPIProject == "" || provenance.Publisher.Kind == "" || provenance.Publisher.Repository == "" ||
		provenance.Publisher.Workflow == "" {
		return releaseContract{}, errors.New("release provenance identity is incomplete")
	}
	if provenance.Publisher.Repository != contract.Upstream.Repository {
		return releaseContract{}, errors.New("release provenance repository does not match the upstream repository")
	}
	return contract, nil
}

func splitRepository(slug string) (string, string, error) {
	owner, repository, found := strings.Cut(slug, "/")
	if !found || owner == "" || repository == "" || strings.ContainsRune(repository, '/') || strings.IndexFunc(slug, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) >= 0 {
		return "", "", fmt.Errorf("upstream repository %q is not an owner/name slug", slug)
	}
	return owner, repository, nil
}

func normalizedBaseURL(value, fallback string) (string, error) {
	if value == "" {
		value = fallback
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("must be an absolute credential-free HTTP(S) URL")
	}
	if parsed.Scheme != "https" {
		return "", errors.New("must use HTTPS")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func resolveReleaseTag(
	ctx context.Context,
	client *http.Client,
	base, token, owner, repository, tag string,
) (string, error) {
	var reference gitReference
	endpoint := fmt.Sprintf("%s/repos/%s/%s/git/ref/tags/%s", base, url.PathEscape(owner), url.PathEscape(repository), url.PathEscape(tag))
	if err := fetchJSON(ctx, client, "GitHub tag reference", endpoint, token, &reference); err != nil {
		return "", err
	}
	object := reference.Object
	seen := make(map[string]struct{})
	for range maxAnnotatedTags {
		switch object.Type {
		case "commit":
			if object.SHA == "" {
				return "", errors.New("GitHub tag reference has an empty commit SHA")
			}
			return object.SHA, nil
		case "tag":
			if object.SHA == "" {
				return "", errors.New("GitHub annotated tag has an empty object SHA")
			}
			if _, duplicate := seen[object.SHA]; duplicate {
				return "", errors.New("GitHub annotated tag chain contains a cycle")
			}
			seen[object.SHA] = struct{}{}
			var annotated annotatedTag
			endpoint = fmt.Sprintf("%s/repos/%s/%s/git/tags/%s", base, url.PathEscape(owner), url.PathEscape(repository), url.PathEscape(object.SHA))
			if err := fetchJSON(ctx, client, "GitHub annotated tag", endpoint, token, &annotated); err != nil {
				return "", err
			}
			object = annotated.Object
		default:
			return "", fmt.Errorf("GitHub tag points to unsupported object type %q", object.Type)
		}
	}
	return "", fmt.Errorf("GitHub annotated tag chain exceeds %d objects", maxAnnotatedTags)
}

func verifyGitHubRelease(
	ctx context.Context,
	client *http.Client,
	base, token, owner, repository string,
	contract releaseContract,
) error {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", base, url.PathEscape(owner), url.PathEscape(repository), url.PathEscape(contract.ReleaseTarget.Tag))
	var release githubRelease
	if err := fetchJSON(ctx, client, "GitHub release", endpoint, token, &release); err != nil {
		return err
	}
	if release.TagName != contract.ReleaseTarget.Tag {
		return fmt.Errorf("GitHub release tag = %q, want %q", release.TagName, contract.ReleaseTarget.Tag)
	}
	for _, expected := range []releaseAsset{contract.ReleaseTarget.Wheel, contract.ReleaseTarget.Sdist} {
		matches := make([]int, 0, 1)
		for index, asset := range release.Assets {
			if asset.Name == expected.Filename {
				matches = append(matches, index)
			}
		}
		if len(matches) == 0 {
			return fmt.Errorf("GitHub release asset %s is missing", expected.Filename)
		}
		if len(matches) != 1 {
			return fmt.Errorf("GitHub release asset %s appears %d times", expected.Filename, len(matches))
		}
		asset := release.Assets[matches[0]]
		if asset.Digest != "sha256:"+expected.SHA256 {
			return fmt.Errorf("GitHub release asset %s digest = %q, want sha256:%s", expected.Filename, asset.Digest, expected.SHA256)
		}
		if err := verifyDownloadedAsset(ctx, client, expected, asset.BrowserDownloadURL); err != nil {
			return err
		}
	}
	return nil
}

func verifyDownloadedAsset(ctx context.Context, client *http.Client, expected releaseAsset, location string) error {
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("GitHub release asset %s has an invalid download location", expected.Filename)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return fmt.Errorf("download GitHub release asset %s: %w", expected.Filename, err)
	}
	request.Header.Set("User-Agent", "powercontext-go-release-contract-verify")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download GitHub release asset %s: %w", expected.Filename, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub release asset %s download returned HTTP %d", expected.Filename, response.StatusCode)
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(response.Body, maxAssetBytes+1))
	if err != nil {
		return fmt.Errorf("hash GitHub release asset %s: %w", expected.Filename, err)
	}
	if written > maxAssetBytes {
		return fmt.Errorf("GitHub release asset %s exceeds %d bytes", expected.Filename, maxAssetBytes)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected.SHA256 {
		return fmt.Errorf("downloaded GitHub release asset %s digest = %s, want %s", expected.Filename, actual, expected.SHA256)
	}
	return nil
}

func verifyPyPIRelease(ctx context.Context, client *http.Client, base string, contract releaseContract) error {
	provenance := contract.ReleaseTarget.Provenance
	endpoint := fmt.Sprintf("%s/pypi/%s/%s/json", base, url.PathEscape(provenance.PyPIProject), url.PathEscape(contract.ReleaseTarget.Version))
	var release pypiRelease
	if err := fetchJSON(ctx, client, "PyPI release metadata", endpoint, "", &release); err != nil {
		return err
	}
	if !strings.EqualFold(release.Info.Name, provenance.PyPIProject) || release.Info.Version != contract.ReleaseTarget.Version {
		return fmt.Errorf("PyPI release identity = %q@%q, want %q@%q", release.Info.Name, release.Info.Version, provenance.PyPIProject, contract.ReleaseTarget.Version)
	}
	expected := []struct {
		Asset       releaseAsset
		PackageType string
	}{
		{Asset: contract.ReleaseTarget.Wheel, PackageType: "bdist_wheel"},
		{Asset: contract.ReleaseTarget.Sdist, PackageType: "sdist"},
	}
	for _, item := range expected {
		matches := make([]int, 0, 1)
		for index, file := range release.URLs {
			if file.Filename == item.Asset.Filename {
				matches = append(matches, index)
			}
		}
		if len(matches) == 0 {
			return fmt.Errorf("PyPI file %s is missing", item.Asset.Filename)
		}
		if len(matches) != 1 {
			return fmt.Errorf("PyPI file %s appears %d times", item.Asset.Filename, len(matches))
		}
		file := release.URLs[matches[0]]
		if file.PackageType != item.PackageType {
			return fmt.Errorf("PyPI file %s type = %q, want %q", item.Asset.Filename, file.PackageType, item.PackageType)
		}
		if file.Digests.SHA256 != item.Asset.SHA256 {
			return fmt.Errorf("PyPI file %s digest = %s, want %s", item.Asset.Filename, file.Digests.SHA256, item.Asset.SHA256)
		}
		if err := verifyPyPIProvenance(ctx, client, base, contract, item.Asset); err != nil {
			return err
		}
	}
	return nil
}

func verifyPyPIProvenance(
	ctx context.Context,
	client *http.Client,
	base string,
	contract releaseContract,
	asset releaseAsset,
) error {
	provenanceIdentity := contract.ReleaseTarget.Provenance
	endpoint := fmt.Sprintf("%s/integrity/%s/%s/%s/provenance", base, url.PathEscape(provenanceIdentity.PyPIProject), url.PathEscape(contract.ReleaseTarget.Version), url.PathEscape(asset.Filename))
	var provenance pypiProvenance
	label := "PyPI provenance for " + asset.Filename
	if err := fetchJSON(ctx, client, label, endpoint, "", &provenance); err != nil {
		return err
	}
	if provenance.Version != 1 || len(provenance.AttestationBundles) != 1 {
		return fmt.Errorf("%s has version %d and %d bundles, want version 1 and one bundle", label, provenance.Version, len(provenance.AttestationBundles))
	}
	bundle := provenance.AttestationBundles[0]
	wantPublisher := provenanceIdentity.Publisher
	if bundle.Publisher.Kind != wantPublisher.Kind || bundle.Publisher.Repository != wantPublisher.Repository ||
		bundle.Publisher.Workflow != wantPublisher.Workflow {
		return fmt.Errorf("%s publisher = %s/%s/%s, want %s/%s/%s", label,
			bundle.Publisher.Kind, bundle.Publisher.Repository, bundle.Publisher.Workflow,
			wantPublisher.Kind, wantPublisher.Repository, wantPublisher.Workflow)
	}
	if len(bundle.Attestations) != 1 || bundle.Attestations[0].Version != 1 {
		return fmt.Errorf("%s has %d attestations, want one version 1 attestation", label, len(bundle.Attestations))
	}
	statementBytes, err := base64.StdEncoding.DecodeString(bundle.Attestations[0].Envelope.Statement)
	if err != nil {
		return fmt.Errorf("%s statement is not valid base64", label)
	}
	var statement inTotoStatement
	if err := json.Unmarshal(statementBytes, &statement); err != nil {
		return fmt.Errorf("%s statement is not valid JSON", label)
	}
	if statement.Type != statementType || statement.PredicateType != publishPredicate {
		return fmt.Errorf("%s statement type or predicate is not the PyPI publish contract", label)
	}
	if len(statement.Subject) != 1 || statement.Subject[0].Name != asset.Filename ||
		statement.Subject[0].Digest["sha256"] != asset.SHA256 {
		return fmt.Errorf("%s subject does not bind %s at %s", label, asset.Filename, asset.SHA256)
	}
	return nil
}

func fetchJSON(ctx context.Context, client *http.Client, label, location, token string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return fmt.Errorf("%s request: %w", label, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "powercontext-go-release-contract-verify")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", label, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("%s returned HTTP %d", label, response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil {
		return fmt.Errorf("read %s response: %w", label, err)
	}
	if len(contents) > maxJSONBytes {
		return fmt.Errorf("%s response exceeds %d bytes", label, maxJSONBytes)
	}
	if err := json.Unmarshal(contents, target); err != nil {
		return fmt.Errorf("decode %s response: %w", label, err)
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}
