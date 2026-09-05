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
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const releaseIntegrationsPath = "build/release-integrations.json"

var releaseIntegrationID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type releaseIntegration struct {
	ID            string   `json:"id"`
	Class         string   `json:"class"`
	RequiredPaths []string `json:"required_paths"`
	LockPaths     []string `json:"lock_paths"`
	ConsumerMode  string   `json:"consumer_mode"`
}

type releaseIntegrationInventory struct {
	SchemaVersion int                  `json:"schema_version"`
	Integrations  []releaseIntegration `json:"integrations"`
}

func readReleaseIntegrations(repository string) ([]releaseIntegration, error) {
	contents, err := readBoundedFile(filepath.Join(repository, releaseIntegrationsPath), maxMetadataBytes)
	if err != nil {
		return nil, fmt.Errorf("read release integration inventory: %w", err)
	}
	var inventory releaseIntegrationInventory
	if err := json.Unmarshal(contents, &inventory, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("decode release integration inventory: %w", err)
	}
	if inventory.SchemaVersion != 1 {
		return nil, fmt.Errorf("release integration inventory has unsupported schema version %d", inventory.SchemaVersion)
	}
	if err := validateReleaseIntegrationRecords(inventory.Integrations); err != nil {
		return nil, err
	}
	return inventory.Integrations, nil
}

func validateReleaseIntegrations(repository string, integrations []releaseIntegration) error {
	if err := validateReleaseIntegrationRecords(integrations); err != nil {
		return err
	}
	repositoryInfo, err := os.Stat(repository)
	if err != nil {
		return fmt.Errorf("read release repository: %w", err)
	}
	if !repositoryInfo.IsDir() {
		return errors.New("release repository is not a directory")
	}
	integrationDirectory := filepath.Join(repository, "integrations")
	directories, err := os.ReadDir(integrationDirectory)
	if err != nil {
		return fmt.Errorf("read integration roots: %w", err)
	}

	recorded := make(map[string]struct{}, len(integrations))
	for _, integration := range integrations {
		recorded[integration.ID] = struct{}{}
		root := filepath.Join(integrationDirectory, integration.ID)
		info, statErr := os.Stat(root)
		if statErr != nil {
			return fmt.Errorf("release integration %q root is missing: %w", integration.ID, statErr)
		}
		if !info.IsDir() {
			return fmt.Errorf("release integration %q root is not a directory", integration.ID)
		}
		for _, releasePath := range append(slices.Clone(integration.RequiredPaths), integration.LockPaths...) {
			info, statErr := os.Stat(filepath.Join(repository, filepath.FromSlash(releasePath)))
			if statErr != nil {
				return fmt.Errorf("release integration %q is missing declared file %q: %w", integration.ID, releasePath, statErr)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("release integration %q declared path %q is not a regular file", integration.ID, releasePath)
			}
		}
	}

	for _, entry := range directories {
		if !entry.IsDir() {
			continue
		}
		if _, ok := recorded[entry.Name()]; !ok {
			return fmt.Errorf("integration root %q is absent from the release inventory", entry.Name())
		}
	}
	return nil
}

func validateReleaseIntegrationRecords(integrations []releaseIntegration) error {
	if len(integrations) == 0 {
		return errors.New("release integration inventory is empty")
	}
	ids := make(map[string]struct{}, len(integrations))
	for index := range integrations {
		integration := &integrations[index]
		if !releaseIntegrationID.MatchString(integration.ID) {
			return fmt.Errorf("release integration at index %d has an invalid ID", index)
		}
		if _, exists := ids[integration.ID]; exists {
			return fmt.Errorf("release integration inventory contains duplicate ID %q", integration.ID)
		}
		ids[integration.ID] = struct{}{}
		if err := validateReleaseIntegrationClass(*integration); err != nil {
			return fmt.Errorf("release integration %q: %w", integration.ID, err)
		}
		if len(integration.RequiredPaths) == 0 {
			return fmt.Errorf("release integration %q has no required paths", integration.ID)
		}
		paths := make(map[string]struct{}, len(integration.RequiredPaths)+len(integration.LockPaths))
		for _, releasePaths := range [][]string{integration.RequiredPaths, integration.LockPaths} {
			for pathIndex, releasePath := range releasePaths {
				normalized, err := normalizeReleaseIntegrationPath(integration.ID, releasePath)
				if err != nil {
					return fmt.Errorf("release integration %q path %d: %w", integration.ID, pathIndex, err)
				}
				if _, exists := paths[normalized]; exists {
					return fmt.Errorf("release integration %q declares duplicate path %q", integration.ID, normalized)
				}
				paths[normalized] = struct{}{}
				releasePaths[pathIndex] = normalized
			}
		}
	}
	return nil
}

func validateReleaseIntegrationClass(integration releaseIntegration) error {
	switch integration.Class {
	case "command-host":
		if integration.ConsumerMode != "command" {
			return errors.New("command-host class requires command consumer mode")
		}
	case "python-package":
		if integration.ConsumerMode != "python" {
			return errors.New("python-package class requires python consumer mode")
		}
	default:
		return fmt.Errorf("unsupported class %q", integration.Class)
	}
	return nil
}

func normalizeReleaseIntegrationPath(id, value string) (string, error) {
	if value == "" {
		return "", errors.New("must not be empty")
	}
	if strings.ContainsRune(value, '\\') || path.IsAbs(value) || filepath.IsAbs(filepath.FromSlash(value)) {
		return "", errors.New("must be a relative slash path")
	}
	normalized := path.Clean(value)
	if normalized == "." || normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", errors.New("must not escape the repository")
	}
	prefix := "integrations/" + id + "/"
	if !strings.HasPrefix(normalized, prefix) {
		return "", fmt.Errorf("must remain under %q", prefix)
	}
	return normalized, nil
}
