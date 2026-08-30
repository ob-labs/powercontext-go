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
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

const seekDBPackageDoc = "Package seekdb loads and owns the embedded seekDB runtime used by the SQL store."

type listedPackage struct {
	Doc      string   `json:"Doc"`
	GoFiles  []string `json:"GoFiles"`
	CgoFiles []string `json:"CgoFiles"`
	CFiles   []string `json:"CFiles"`
	Error    *struct {
		Err string `json:"Err"`
	} `json:"Error"`
}

func TestSeekDBBuildConstraintsSelectImplementationAndNativeSourcesTogether(t *testing.T) {
	tests := []struct {
		name       string
		goos       string
		cgoEnabled string
		wantNative bool
	}{
		{name: "linux native", goos: "linux", cgoEnabled: "1", wantNative: true},
		{name: "darwin native", goos: "darwin", cgoEnabled: "1", wantNative: true},
		{name: "linux stub without cgo", goos: "linux", cgoEnabled: "0"},
		{name: "darwin stub without cgo", goos: "darwin", cgoEnabled: "0"},
		{name: "windows stub with cgo", goos: "windows", cgoEnabled: "1"},
		{name: "windows stub without cgo", goos: "windows", cgoEnabled: "0"},
	}

	repository := filepath.Clean(filepath.Join("..", ".."))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(
				t.Context(),
				"go",
				"-C",
				repository,
				"list",
				"-e",
				"-json",
				"./internal/sqlstore/seekdb",
			)
			command.Env = append(
				os.Environ(),
				"GOOS="+test.goos,
				"GOARCH=amd64",
				"CGO_ENABLED="+test.cgoEnabled,
			)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("go list: %v\n%s", err, output)
			}
			var pkg listedPackage
			if decodeErr := json.Unmarshal(output, &pkg); decodeErr != nil {
				t.Fatalf("decode go list output: %v", decodeErr)
			}
			if pkg.Error != nil {
				t.Fatalf("go list package error: %s", pkg.Error.Err)
			}
			if pkg.Doc != seekDBPackageDoc {
				t.Fatalf("package Doc = %q, want %q", pkg.Doc, seekDBPackageDoc)
			}

			if test.wantNative {
				if !slices.Contains(pkg.CgoFiles, "seekdb.go") || !slices.Contains(pkg.CFiles, "loader.c") {
					t.Fatalf("native files: CgoFiles=%v CFiles=%v", pkg.CgoFiles, pkg.CFiles)
				}
				if slices.Contains(pkg.GoFiles, "seekdb_stub.go") {
					t.Fatalf("native build selected seekdb_stub.go: GoFiles=%v", pkg.GoFiles)
				}
				return
			}

			if !slices.Contains(pkg.GoFiles, "seekdb_stub.go") {
				t.Fatalf("stub build did not select seekdb_stub.go: GoFiles=%v", pkg.GoFiles)
			}
			if slices.Contains(pkg.CgoFiles, "seekdb.go") || slices.Contains(pkg.CFiles, "loader.c") {
				t.Fatalf("stub build retained native files: CgoFiles=%v CFiles=%v", pkg.CgoFiles, pkg.CFiles)
			}
		})
	}
}
