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
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
)

type licenseInventoryOptions struct {
	Binary     string
	Edition    string
	Output     string
	Repository string
}

type licenseInventoryResult struct {
	GoModules          int    `json:"go_modules"`
	NativeDependencies int    `json:"native_dependencies"`
	Output             string `json:"output"`
}

func runLicenseInventory(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("licenses", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := new(licenseInventoryOptions)
	flags.StringVar(&options.Binary, "binary", "", "built powercontext binary")
	flags.StringVar(&options.Edition, "edition", "standard", "standard or full")
	flags.StringVar(&options.Output, "output", "", "new dependency-license manifest path")
	flags.StringVar(&options.Repository, "repository", ".", "repository root")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || options.Binary == "" || options.Output == "" ||
		(options.Edition != "standard" && options.Edition != "full") {
		return errors.New("licenses requires binary, output, and standard or full edition")
	}
	repository, err := filepath.Abs(options.Repository)
	if err != nil {
		return err
	}
	assets, err := readAssets(repository)
	if err != nil {
		return err
	}
	manifest, _, err := collectLicenses(options.Binary, repository, options.Edition, assets)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(&manifest, jsontext.WithIndent("  "))
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if mkdirErr := os.MkdirAll(filepath.Dir(options.Output), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	if writeErr := writeNewFile(options.Output, encoded, 0o644); writeErr != nil {
		return writeErr
	}
	result, err := json.Marshal(&licenseInventoryResult{
		GoModules: len(manifest.Modules), NativeDependencies: len(manifest.Native), Output: filepath.Base(options.Output),
	})
	if err != nil {
		return err
	}
	result = append(result, '\n')
	written, err := output.Write(result)
	if err != nil {
		return err
	}
	if written != len(result) {
		return io.ErrShortWrite
	}
	return nil
}
