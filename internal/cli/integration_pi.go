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

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

const (
	piPackageName = "powercontext-pi"
	piRelative    = "integrations/pi/plugins/powercontext"
)

type piPackageListing struct {
	source string
	path   string
	scope  string
}

func newSetupPiCommand(state *commandState) *cobra.Command {
	var source, ref string
	command := &cobra.Command{
		Use: "pi", Short: "Install the PowerContext Pi package and prepare local storage.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			executable, err := piExecutable(state.system, runtime.GOOS)
			if err != nil {
				return errors.New("Pi CLI is not installed or is not on PATH")
			}
			dataDirectory, err := prepareDataDirectory()
			if err != nil {
				return err
			}
			packagePath, err := resolvePiPackage(command.Context(), state.system, source, ref, dataDirectory)
			if err != nil {
				return err
			}
			if packageErr := requirePiPackage(packagePath); packageErr != nil {
				return packageErr
			}
			if _, installErr := state.system.Run(command.Context(), executable, "install", packagePath); installErr != nil {
				return installErr
			}
			if cleanupErr := removeSupersededPiPackages(command.Context(), state.system, executable, packagePath); cleanupErr != nil {
				return cleanupErr
			}
			checks := runPiDiagnostics(command.Context(), state.system)
			if diagnosticsStatus(checks) != "ok" {
				if writeErr := writeDiagnostics(state, checks); writeErr != nil {
					return writeErr
				}
				return alreadyReported(setupVerificationError(checks))
			}
			result := map[string]string{"package": piPackageName, "package_path": packagePath, "data_dir": dataDirectory}
			if state.json {
				return writeJSON(state.stdout, result)
			}
			_, err = fmt.Fprintf(state.stdout,
				"PowerContext Pi setup complete.\nPackage: %s (%s)\nData directory: %s\nNext: run `powercontext server run`, then start a new Pi session.\n",
				piPackageName, packagePath, dataDirectory,
			)
			return err
		},
	}
	command.Flags().StringVar(&source, "source", defaultMarketplaceSource, "PowerContext Git source or local checkout path.")
	command.Flags().StringVar(&ref, "ref", defaultMarketplaceRef, "Git ref used for a remote source.")
	return command
}

func newDoctorPiCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "pi", Short: "Check the optional Pi CLI and PowerContext package.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			checks := runPiDiagnostics(command.Context(), state.system)
			if err := writeDiagnostics(state, checks); err != nil {
				return err
			}
			if diagnosticsStatus(checks) != "ok" {
				return alreadyReported(errors.New("Pi diagnostics did not pass"))
			}
			return nil
		},
	}
}

func piExecutable(commands systemCommandExecutor, goos string) (string, error) {
	if goos == "windows" {
		if executable, err := commands.LookPath("pi.cmd"); err == nil {
			return executable, nil
		}
	}
	return commands.LookPath("pi")
}

func resolvePiPackage(
	ctx context.Context,
	commands systemCommandExecutor,
	source, ref, dataDirectory string,
) (string, error) {
	root, local, err := normalizeMarketplaceSource(source)
	if err != nil {
		return "", err
	}
	if !local {
		if err := validateRemoteRef(ref); err != nil {
			return "", errors.New("invalid Pi ref")
		}
		cloneURL, err := githubRepositoryCloneURL(source)
		if err != nil {
			return "", errors.New("invalid Pi source; use a local path or an HTTPS/SSH GitHub repository")
		}
		root, err = refreshIntegrationCheckout(ctx, commands, cloneURL, ref, filepath.Join(dataDirectory, "checkouts", "pi", "current"), func(path string) error {
			packagePath, ok := findPiPackage(path)
			if !ok {
				return errors.New("PowerContext Pi package was not found under the selected source")
			}
			return requirePiPackage(packagePath)
		})
		if err != nil {
			return "", err
		}
	}
	packagePath, ok := findPiPackage(root)
	if !ok {
		return "", errors.New("PowerContext Pi package was not found under the selected source")
	}
	return packagePath, nil
}

func findPiPackage(root string) (string, bool) {
	for _, candidate := range []string{root, filepath.Join(root, filepath.FromSlash(piRelative))} {
		payload, err := os.ReadFile(filepath.Join(candidate, "package.json"))
		if err != nil || len(payload) > maximumCommandOutput {
			continue
		}
		var manifest struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(payload, &manifest) == nil && manifest.Name == piPackageName {
			resolved, resolveErr := resolvePath(candidate)
			return resolved, resolveErr == nil
		}
	}
	return "", false
}

func requirePiPackage(path string) error {
	for _, relative := range []string{"extensions/powercontext.ts", "skills/project-context/SKILL.md"} {
		info, err := os.Stat(filepath.Join(path, filepath.FromSlash(relative)))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("PowerContext Pi package at %s is missing its extension or project-context skill", displayPath(path))
		}
	}
	return nil
}

func removeSupersededPiPackages(
	ctx context.Context,
	commands systemCommandExecutor,
	executable, keep string,
) error {
	output, err := commands.Run(ctx, executable, "list")
	if err != nil {
		return err
	}
	keep, err = resolvePath(keep)
	if err != nil {
		return err
	}
	for _, listing := range parsePiPackages(string(output)) {
		if listing.scope != "user" || !isPowerContextPiListing(listing) || piListingMatches(listing, keep) {
			continue
		}
		remove := ""
		removeSource := hasRemotePackageScheme(listing.source) ||
			(listing.path == "" && filepath.IsAbs(listing.source))
		if removeSource {
			remove = listing.source
		} else if listing.path != "" {
			remove = listing.path
		}
		if remove != "" {
			if _, err := commands.Run(ctx, executable, "remove", remove); err != nil {
				return err
			}
		}
	}
	return nil
}

func parsePiPackages(output string) []piPackageListing {
	result := make([]piPackageListing, 0)
	current := piPackageListing{scope: "user"}
	appendCurrent := func() {
		if current.source != "" {
			result = append(result, current)
		}
	}
	for _, raw := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(raw)
		switch trimmed {
		case "User packages:":
			appendCurrent()
			current = piPackageListing{scope: "user"}
			continue
		case "Project packages:":
			appendCurrent()
			current = piPackageListing{scope: "project"}
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " \t"))
		if indent == 2 && trimmed != "" {
			appendCurrent()
			current = piPackageListing{source: strings.TrimSuffix(trimmed, " (filtered)"), scope: current.scope}
		} else if indent >= 4 && current.source != "" && trimmed != "" {
			current.path = trimmed
		}
	}
	appendCurrent()
	return result
}

func isPowerContextPiListing(listing piPackageListing) bool {
	if listing.path != "" {
		if _, ok := findPiPackage(listing.path); ok {
			return true
		}
	}
	source := filepath.ToSlash(listing.source)
	return source == "npm:"+piPackageName || strings.Contains(source, piRelative)
}

func piListingMatches(listing piPackageListing, keep string) bool {
	for _, candidate := range []string{listing.path, listing.source} {
		if candidate == "" || hasRemotePackageScheme(candidate) {
			continue
		}
		resolved, err := resolvePath(candidate)
		if err == nil && pathsEqual(resolved, keep) {
			return true
		}
	}
	return false
}

func runPiDiagnostics(ctx context.Context, commands systemCommandExecutor) map[string]diagnostic {
	executable, err := piExecutable(commands, runtime.GOOS)
	if err != nil {
		return map[string]diagnostic{
			"pi":      {Status: "failed", Detail: "Pi CLI is not installed or is not on PATH"},
			"package": {Status: "skipped", Detail: "not checked because Pi CLI is unavailable"},
		}
	}
	output, err := commands.Run(ctx, executable, "list")
	if err != nil {
		return map[string]diagnostic{
			"pi":      {Status: "failed", Detail: err.Error()},
			"package": {Status: "skipped", Detail: "package list is unavailable"},
		}
	}
	installed := false
	for _, listing := range parsePiPackages(string(output)) {
		if listing.path != "" {
			if _, ok := findPiPackage(listing.path); ok {
				installed = true
				break
			}
		}
	}
	packageCheck := diagnostic{Status: "failed", Detail: "PowerContext Pi package is not installed"}
	if installed {
		packageCheck = diagnostic{OK: true, Status: "ok", Detail: piPackageName + " is installed"}
	}
	return map[string]diagnostic{"pi": {OK: true, Status: "ok", Detail: executable}, "package": packageCheck}
}

func hasRemotePackageScheme(value string) bool {
	for _, prefix := range []string{"npm:", "git:", "github:", "http:", "https:", "ssh:"} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}
