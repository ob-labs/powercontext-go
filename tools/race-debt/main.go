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

// Command race-debt validates the bounded ledger for temporary race-test exclusions.
package main

import (
	"encoding/json/v2"
	"flag"
	"fmt"
	"go/token"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultLedgerPath = ".github/race-debt.json"

type ledger struct {
	Version    int         `json:"version"`
	Exclusions []exclusion `json:"exclusions"`
}

type exclusion struct {
	Package          string `json:"package"`
	Test             string `json:"test"`
	Issue            string `json:"issue"`
	Owner            string `json:"owner"`
	Reason           string `json:"reason"`
	Added            string `json:"added"`
	RemovalCondition string `json:"removal_condition"`
	Expires          string `json:"expires"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "race debt:", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("race-debt", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	path := flags.String("file", defaultLedgerPath, "path to the race-debt ledger")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}

	contents, err := os.ReadFile(*path)
	if err != nil {
		return fmt.Errorf("read ledger: %w", err)
	}
	var value ledger
	decodeErr := json.Unmarshal(contents, &value, json.RejectUnknownMembers(true))
	if decodeErr != nil {
		return fmt.Errorf("parse ledger: %w", decodeErr)
	}
	validationErr := validate(value, time.Now().UTC())
	if validationErr != nil {
		return validationErr
	}
	_, err = fmt.Fprintf(output, "race debt: %d temporary exclusions verified\n", len(value.Exclusions))
	return err
}

func validate(value ledger, now time.Time) error {
	if value.Version != 1 {
		return fmt.Errorf("version = %d, want 1", value.Version)
	}
	seen := make(map[string]struct{}, len(value.Exclusions))
	for index, entry := range value.Exclusions {
		prefix := fmt.Sprintf("exclusions[%d]", index)
		if !strings.HasPrefix(entry.Package, "./") || strings.TrimSpace(entry.Package) == "./" {
			return fmt.Errorf("%s.package must be a repository-relative package path", prefix)
		}
		if !strings.HasPrefix(entry.Test, "Test") || !token.IsIdentifier(entry.Test) || entry.Test == "Test" {
			return fmt.Errorf("%s.test must name one Test function", prefix)
		}
		if err := validateIssue(entry.Issue); err != nil {
			return fmt.Errorf("%s.issue: %w", prefix, err)
		}
		if !strings.HasPrefix(entry.Owner, "@") || len(strings.TrimSpace(entry.Owner)) == 1 {
			return fmt.Errorf("%s.owner must name an accountable GitHub user or team", prefix)
		}
		if strings.TrimSpace(entry.Reason) == "" {
			return fmt.Errorf("%s.reason must explain the temporary exclusion", prefix)
		}
		added, addedErr := time.Parse(time.DateOnly, entry.Added)
		if addedErr != nil {
			return fmt.Errorf("%s.added must use YYYY-MM-DD: %w", prefix, addedErr)
		}
		if strings.TrimSpace(entry.RemovalCondition) == "" {
			return fmt.Errorf("%s.removal_condition must state how the exclusion will be removed", prefix)
		}
		expires, expiresErr := time.Parse(time.DateOnly, entry.Expires)
		if expiresErr != nil {
			return fmt.Errorf("%s.expires must use YYYY-MM-DD: %w", prefix, expiresErr)
		}
		if expires.Before(added) {
			return fmt.Errorf("%s.expires must not precede added", prefix)
		}
		if now.After(expires.AddDate(0, 0, 1)) {
			return fmt.Errorf("%s.expires has passed; remove or renew the exclusion through its Issue", prefix)
		}
		key := entry.Package + "#" + entry.Test
		if _, ok := seen[key]; ok {
			return fmt.Errorf("%s duplicates %q", prefix, key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateIssue(value string) error {
	const prefix = "https://github.com/ob-labs/powercontext-go/issues/"
	identifier, ok := strings.CutPrefix(value, prefix)
	if !ok || identifier == "" {
		return fmt.Errorf("must be an ob-labs/powercontext-go Issue URL")
	}
	if _, err := strconv.ParseUint(identifier, 10, 64); err != nil {
		return fmt.Errorf("must end with a numeric Issue number")
	}
	return nil
}
