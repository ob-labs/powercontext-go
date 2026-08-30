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

// Command release builds auditable, deterministic PowerContext release bundles.
package main

import (
	"errors"
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fatal(errors.New("usage: release <asset|package|metadata|checksum|licenses|verify|verify-evidence> [flags]"))
	}
	var err error
	switch os.Args[1] {
	case "asset":
		err = runAsset(os.Args[2:], os.Stdout)
	case "package":
		err = runPackage(os.Args[2:], os.Stdout)
	case "metadata":
		err = runMetadata(os.Args[2:])
	case "checksum":
		err = runChecksum(os.Args[2:])
	case "licenses":
		err = runLicenseInventory(os.Args[2:], os.Stdout)
	case "verify":
		err = runVerify(os.Args[2:])
	case "verify-evidence":
		err = runVerifyEvidence(os.Args[2:])
	default:
		err = fmt.Errorf("unknown release command %q", os.Args[1])
	}
	if err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "release:", err)
	os.Exit(1)
}
