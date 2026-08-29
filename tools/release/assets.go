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
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	assetsPath       = "build/native-assets.json"
	oraclePath       = "test/conformance/testdata/python-v0.0.2/manifest.json"
	modulePath       = "github.com/ob-labs/powercontext-go"
	maxMetadataBytes = 16 << 20
)

var commitHash = regexp.MustCompile(`^[0-9a-f]{40}$`)

type nativeAssets struct {
	SchemaVersion int `json:"schema_version"`
	SQLiteVec     struct {
		Version   string `json:"version"`
		SourceURL string `json:"source_url"`
		SHA256    string `json:"sha256"`
	} `json:"sqlite_vec"`
	Tokenizers struct {
		Version string                 `json:"version"`
		Assets  map[string]nativeAsset `json:"assets"`
	} `json:"tokenizers"`
	ONNXRuntime struct {
		Version string                 `json:"version"`
		Commit  string                 `json:"commit"`
		Assets  map[string]nativeAsset `json:"assets"`
	} `json:"onnxruntime"`
	Syft struct {
		Version string `json:"version"`
	} `json:"syft"`
}

type nativeAsset struct {
	Name            string `json:"name,omitempty"`
	SHA256          string `json:"sha256,omitempty"`
	BuildFromSource bool   `json:"build_from_source,omitempty"`
}

type oracleManifest struct {
	OracleCommit  string `json:"oracle_commit"`
	OpenAPISHA256 string `json:"openapi_sha256"`
}

func runAsset(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("asset", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var repository, component, target, field string
	flags.StringVar(&repository, "repository", ".", "repository root")
	flags.StringVar(&component, "component", "", "sqlite-vec, tokenizers, onnxruntime, or syft")
	flags.StringVar(&target, "target", "", "GOOS-GOARCH target")
	flags.StringVar(&field, "field", "", "version, url, sha256, commit, name, or build-from-source")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	assets, err := readAssets(repository)
	if err != nil {
		return err
	}
	value, err := assetValue(assets, component, target, field)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, value)
	return err
}

func assetValue(assets nativeAssets, component, target, field string) (string, error) {
	switch component {
	case "sqlite-vec":
		switch field {
		case "version":
			return assets.SQLiteVec.Version, nil
		case "url":
			return assets.SQLiteVec.SourceURL, nil
		case "sha256":
			return assets.SQLiteVec.SHA256, nil
		}
	case "syft":
		if field == "version" {
			return assets.Syft.Version, nil
		}
	case "tokenizers":
		if field == "version" {
			return assets.Tokenizers.Version, nil
		}
		asset, ok := assets.Tokenizers.Assets[target]
		if !ok {
			return "", fmt.Errorf("unsupported tokenizers target %q", target)
		}
		return releaseAssetValue(
			asset, field,
			fmt.Sprintf("https://github.com/daulet/tokenizers/releases/download/v%s/%s", assets.Tokenizers.Version, asset.Name),
		)
	case "onnxruntime":
		if field == "version" {
			return assets.ONNXRuntime.Version, nil
		}
		if field == "commit" {
			return assets.ONNXRuntime.Commit, nil
		}
		asset, ok := assets.ONNXRuntime.Assets[target]
		if !ok {
			return "", fmt.Errorf("unsupported ONNX Runtime target %q", target)
		}
		return releaseAssetValue(
			asset, field,
			fmt.Sprintf("https://github.com/microsoft/onnxruntime/releases/download/v%s/%s", assets.ONNXRuntime.Version, asset.Name),
		)
	}
	return "", fmt.Errorf("unsupported asset field %q for %q", field, component)
}

func releaseAssetValue(asset nativeAsset, field, url string) (string, error) {
	switch field {
	case "name":
		return asset.Name, nil
	case "sha256":
		return asset.SHA256, nil
	case "url":
		if asset.Name == "" {
			return "", errors.New("asset is built from source and has no binary URL")
		}
		return url, nil
	case "build-from-source":
		return fmt.Sprintf("%t", asset.BuildFromSource), nil
	default:
		return "", fmt.Errorf("unsupported asset field %q", field)
	}
}

func readAssets(repository string) (nativeAssets, error) {
	var assets nativeAssets
	contents, err := readBoundedFile(filepath.Join(repository, assetsPath), maxMetadataBytes)
	if err != nil {
		return assets, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&assets); err != nil {
		return assets, err
	}
	if err := validateAssets(assets); err != nil {
		return assets, err
	}
	return assets, nil
}

func validateAssets(assets nativeAssets) error {
	if assets.SchemaVersion != 2 || assets.SQLiteVec.Version == "" || assets.Tokenizers.Version == "" ||
		assets.ONNXRuntime.Version == "" || assets.Syft.Version == "" || !commitHash.MatchString(assets.ONNXRuntime.Commit) {
		return errors.New("native asset manifest is incomplete")
	}
	for _, target := range []string{"darwin-amd64", "darwin-arm64", "linux-amd64", "linux-arm64"} {
		tokenizers, tokenizersOK := assets.Tokenizers.Assets[target]
		onnx, onnxOK := assets.ONNXRuntime.Assets[target]
		if !tokenizersOK || tokenizers.Name == "" || !validSHA256(tokenizers.SHA256) || !onnxOK {
			return fmt.Errorf("native asset manifest is incomplete for %s", target)
		}
		if onnx.BuildFromSource {
			if onnx.Name != "" || onnx.SHA256 != "" {
				return fmt.Errorf("source-built ONNX Runtime target %s also declares a binary asset", target)
			}
		} else if onnx.Name == "" || !validSHA256(onnx.SHA256) {
			return fmt.Errorf("ONNX Runtime asset is incomplete for %s", target)
		}
	}
	if !validSHA256(assets.SQLiteVec.SHA256) {
		return errors.New("sqlite-vec source digest is invalid")
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func readOracle(repository string) (oracleManifest, error) {
	var oracle oracleManifest
	contents, err := readBoundedFile(filepath.Join(repository, oraclePath), maxMetadataBytes)
	if err != nil {
		return oracle, err
	}
	if err := json.Unmarshal(contents, &oracle); err != nil {
		return oracle, err
	}
	if !commitHash.MatchString(oracle.OracleCommit) || !validSHA256(oracle.OpenAPISHA256) {
		return oracle, errors.New("Python Oracle manifest is incomplete")
	}
	return oracle, nil
}

func readBoundedFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReader(io.LimitReader(file, maximum+1))
	contents, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("metadata file %q exceeds %d bytes", filepath.Base(path), maximum)
	}
	return contents, nil
}
