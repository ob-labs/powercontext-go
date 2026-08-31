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
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
)

type caseIdentity struct {
	File string `json:"file"`
	Name string `json:"name"`
}

type removedCaseDisposition struct {
	Case         caseIdentity   `json:"case"`
	Disposition  string         `json:"disposition"`
	Reason       string         `json:"reason"`
	Replacements []caseIdentity `json:"replacements,omitempty"`
	Evidence     []string       `json:"evidence,omitempty"`
}

type targetDeltaLedger struct {
	SchemaVersion int                      `json:"schema_version"`
	FromCommit    string                   `json:"from_commit"`
	ToCommit      string                   `json:"to_commit"`
	Added         []caseIdentity           `json:"added"`
	Removed       []removedCaseDisposition `json:"removed"`
}

func checkTargetDelta(root, contractPath, ledgerPath, previousUpstream, releaseUpstream string) error {
	var contract parityContract
	if err := readJSON(contractPath, &contract); err != nil {
		return fmt.Errorf("read parity contract: %w", err)
	}
	var ledger targetDeltaLedger
	if err := readJSON(ledgerPath, &ledger); err != nil {
		return fmt.Errorf("read target delta ledger: %w", err)
	}
	previousHead, err := upstreamHead(previousUpstream)
	if err != nil {
		return fmt.Errorf("resolve previous checkout identity: %w", err)
	}
	if previousHead != ledger.FromCommit {
		return fmt.Errorf("previous checkout HEAD = %s, want ledger from_commit %s", previousHead, ledger.FromCommit)
	}
	releaseHead, err := upstreamHead(releaseUpstream)
	if err != nil {
		return fmt.Errorf("resolve release checkout identity: %w", err)
	}
	if releaseHead != contract.ReleaseTarget.Commit {
		return fmt.Errorf("release checkout HEAD = %s, want contract release commit %s", releaseHead, contract.ReleaseTarget.Commit)
	}
	previousCases, err := extractUpstreamCases(previousUpstream)
	if err != nil {
		return fmt.Errorf("discover previous target cases: %w", err)
	}
	releaseCases, err := extractUpstreamCases(releaseUpstream)
	if err != nil {
		return fmt.Errorf("discover release target cases: %w", err)
	}
	return validateTargetDelta(
		ledger,
		previousCases,
		releaseCases,
		ledger.FromCommit,
		contract.ReleaseTarget.Commit,
		root,
	)
}

func validateTargetDelta(
	ledger targetDeltaLedger,
	previousCases, releaseCases []pythonTest,
	previousCommit, releaseCommit, root string,
) error {
	if ledger.SchemaVersion != 1 {
		return fmt.Errorf("unsupported target delta schema %d", ledger.SchemaVersion)
	}
	if ledger.FromCommit != previousCommit || ledger.ToCommit != releaseCommit {
		return fmt.Errorf(
			"target delta identity = %s..%s, want %s..%s",
			ledger.FromCommit, ledger.ToCommit, previousCommit, releaseCommit,
		)
	}
	added, removed := targetCaseDelta(previousCases, releaseCases)
	if err := validateIdentityList("added", ledger.Added, added); err != nil {
		return err
	}
	removedIdentities := make([]caseIdentity, 0, len(ledger.Removed))
	for _, disposition := range ledger.Removed {
		removedIdentities = append(removedIdentities, disposition.Case)
	}
	if err := validateIdentityList("removed", removedIdentities, removed); err != nil {
		return err
	}

	releaseSet := make(map[string]struct{}, len(releaseCases))
	for _, test := range releaseCases {
		releaseSet[caseIdentityKey(caseIdentity{File: test.File, Name: test.Name})] = struct{}{}
	}
	for _, disposition := range ledger.Removed {
		key := caseIdentityKey(disposition.Case)
		if !validDisposition(disposition.Disposition) {
			return fmt.Errorf("removed case %s has invalid disposition %q", key, disposition.Disposition)
		}
		if strings.TrimSpace(disposition.Reason) == "" {
			return fmt.Errorf("removed case %s has no reviewed reason", key)
		}
		if len(disposition.Replacements) == 0 && len(disposition.Evidence) == 0 {
			return fmt.Errorf("removed case %s has no replacement evidence", key)
		}
		for _, replacement := range disposition.Replacements {
			replacementKey := caseIdentityKey(replacement)
			if _, ok := releaseSet[replacementKey]; !ok {
				return fmt.Errorf("removed case %s replacement %s does not exist in the release target", key, replacementKey)
			}
		}
		for _, reference := range disposition.Evidence {
			if _, err := resolveEvidence(root, reference); err != nil {
				return fmt.Errorf("removed case %s evidence %q: %w", key, reference, err)
			}
		}
	}
	return nil
}

func targetCaseDelta(previousCases, releaseCases []pythonTest) ([]caseIdentity, []caseIdentity) {
	previous := make(map[string]caseIdentity, len(previousCases))
	for _, test := range previousCases {
		identity := caseIdentity{File: test.File, Name: test.Name}
		previous[caseIdentityKey(identity)] = identity
	}
	release := make(map[string]caseIdentity, len(releaseCases))
	for _, test := range releaseCases {
		identity := caseIdentity{File: test.File, Name: test.Name}
		release[caseIdentityKey(identity)] = identity
	}
	added := make(map[string]caseIdentity)
	for key, identity := range release {
		if _, ok := previous[key]; !ok {
			added[key] = identity
		}
	}
	removed := make(map[string]caseIdentity)
	for key, identity := range previous {
		if _, ok := release[key]; !ok {
			removed[key] = identity
		}
	}
	return slices.SortedFunc(maps.Values(added), compareCaseIdentity),
		slices.SortedFunc(maps.Values(removed), compareCaseIdentity)
}

func validateIdentityList(name string, got, want []caseIdentity) error {
	if !slices.IsSortedFunc(got, compareCaseIdentity) {
		return fmt.Errorf("target delta %s cases are not sorted", name)
	}
	if len(got) != len(want) {
		return fmt.Errorf("target delta %s case set has %d cases, want %d", name, len(got), len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			return fmt.Errorf(
				"target delta %s case set differs at %d: got %s, want %s",
				name, index, caseIdentityKey(got[index]), caseIdentityKey(want[index]),
			)
		}
	}
	return nil
}

func compareCaseIdentity(left, right caseIdentity) int {
	if order := cmp.Compare(left.File, right.File); order != 0 {
		return order
	}
	return cmp.Compare(left.Name, right.Name)
}

func caseIdentityKey(identity caseIdentity) string {
	return identity.File + "#" + identity.Name
}

func validDisposition(value string) bool {
	return value == "removed" || value == "renamed" || value == "superseded"
}
