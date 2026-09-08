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

// Command api-generate generates the Go HTTP contract from the frozen OpenAPI
// document without modifying that authoritative file.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	var specification string
	var target string
	var packageName string
	var clientInvoker string
	var compatibility string
	var scopeSidecarManifest string
	var sourceSidecarManifest string
	var artifactSidecarManifest string
	var managedSkillSidecarManifest string
	var statsSidecarManifest string
	var legacySpecification string
	flag.StringVar(&specification, "spec", "powercontext.yaml", "canonical OpenAPI document")
	flag.StringVar(&target, "target", "../api/v1", "generated package directory")
	flag.StringVar(&packageName, "package", "v1", "generated Go package name")
	flag.StringVar(&clientInvoker, "client-invoker", "", "optional normalized Client Invoker output")
	flag.StringVar(&compatibility, "compatibility", "", "optional legacy/canonical compatibility surface")
	flag.StringVar(&scopeSidecarManifest, "scope-sidecar-manifest", "", "optional Scope sidecar projection manifest")
	flag.StringVar(&sourceSidecarManifest, "source-sidecar-manifest", "", "optional Source sidecar projection manifest")
	flag.StringVar(&artifactSidecarManifest, "artifact-sidecar-manifest", "", "optional Artifact sidecar projection manifest")
	flag.StringVar(&managedSkillSidecarManifest, "managed-skill-sidecar-manifest", "", "optional managed Skill package read projection manifest")
	flag.StringVar(&statsSidecarManifest, "stats-sidecar-manifest", "", "optional Stats sidecar projection manifest")
	flag.StringVar(&legacySpecification, "legacy-spec", "", "legacy OpenAPI document required for Scope sidecar generation")
	flag.Parse()
	var err error
	selectedSidecars := 0
	for _, manifest := range []string{scopeSidecarManifest, sourceSidecarManifest, artifactSidecarManifest, managedSkillSidecarManifest, statsSidecarManifest} {
		if manifest != "" {
			selectedSidecars++
		}
	}
	if selectedSidecars > 1 {
		err = errors.New("select exactly one sidecar manifest")
	} else if managedSkillSidecarManifest != "" {
		err = runManagedSkillSidecar(specification, managedSkillSidecarManifest, target, packageName, clientInvoker, compatibility, legacySpecification)
	} else if statsSidecarManifest != "" {
		err = runStatsSidecar(specification, statsSidecarManifest, target, packageName, clientInvoker, compatibility, legacySpecification)
	} else if artifactSidecarManifest != "" {
		err = runArtifactSidecar(specification, artifactSidecarManifest, target, packageName, clientInvoker, compatibility, legacySpecification)
	} else if sourceSidecarManifest != "" {
		err = runSourceSidecar(specification, sourceSidecarManifest, target, packageName, clientInvoker, compatibility, legacySpecification)
	} else if scopeSidecarManifest == "" {
		err = run(specification, target, packageName, clientInvoker, compatibility)
	} else {
		err = runScopeSidecar(
			specification,
			scopeSidecarManifest,
			target,
			packageName,
			clientInvoker,
			compatibility,
			legacySpecification,
		)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "api-generate:", err)
		os.Exit(1)
	}
}

func run(specification, target, packageName, clientInvoker, compatibility string) error {
	if specification == "" || target == "" || packageName == "" {
		return errors.New("spec, target, and package must not be empty")
	}
	source, err := os.ReadFile(specification)
	if err != nil {
		return err
	}
	if compatibility != "" {
		contents, readErr := os.ReadFile(compatibility)
		if readErr != nil {
			return fmt.Errorf("read compatibility surface: %w", readErr)
		}
		if validateErr := validateCompatibilitySurface(source, contents); validateErr != nil {
			return fmt.Errorf("validate compatibility surface: %w", validateErr)
		}
	}
	generatedInput, err := normalizeNullableReferences(source)
	if err != nil {
		return err
	}
	absoluteTarget, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := runOgen(generatedInput, absoluteTarget, packageName, false); err != nil {
		return err
	}
	if err := generateContractValidation(
		source,
		filepath.Join(absoluteTarget, "powercontext_contract_validation_gen.go"),
	); err != nil {
		return fmt.Errorf("generate PowerContext contract validation: %w", err)
	}
	if clientInvoker != "" {
		invokerTarget, err := filepath.Abs(clientInvoker)
		if err != nil {
			return err
		}
		if err := generateClientInvoker(
			source,
			filepath.Join(absoluteTarget, "oas_client_gen.go"),
			invokerTarget,
		); err != nil {
			return fmt.Errorf("generate normalized Client Invoker: %w", err)
		}
	}
	return nil
}

func runOgen(generatedInput []byte, absoluteTarget, packageName string, allowNoDateTimeEncoders bool) error {
	temporary, err := os.CreateTemp("", "powercontext-ogen-*.json")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if _, writeErr := temporary.Write(generatedInput); writeErr != nil {
		return errors.Join(writeErr, temporary.Close())
	}
	if closeErr := temporary.Close(); closeErr != nil {
		return closeErr
	}
	command := exec.Command(
		"go", "tool", "ogen", "--target", absoluteTarget,
		"--package", packageName, "--clean", temporaryName,
	)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run ogen: %w", err)
	}
	if err := rewriteDateTimeEncoders(absoluteTarget); err != nil &&
		(!allowNoDateTimeEncoders || !errors.Is(err, errNoDateTimeEncoders)) {
		return fmt.Errorf("rewrite date-time encoders: %w", err)
	}
	if err := writeDateTimeSupport(absoluteTarget, packageName); err != nil {
		return fmt.Errorf("write date-time support: %w", err)
	}
	return nil
}

// rewriteDateTimeEncoders keeps generated wire structs as time.Time while
// replacing ogen's second-precision RFC3339 encoder with PowerContext's UTC
// microsecond policy. The canonical OpenAPI remains byte-for-byte identical to
// the Python Oracle; this deterministic generation step changes only emitted
// Go transport code.
func rewriteDateTimeEncoders(target string) error {
	path := filepath.Join(target, "oas_json_gen.go")
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	const generated = "json.EncodeDateTime"
	const replacement = "encodeDateTime"
	count := bytes.Count(contents, []byte(generated))
	if count == 0 {
		return errNoDateTimeEncoders
	}
	rewritten := bytes.ReplaceAll(contents, []byte(generated), []byte(replacement))
	return os.WriteFile(path, rewritten, 0o644)
}

var errNoDateTimeEncoders = errors.New("generated JSON contains no date-time encoders")

func writeDateTimeSupport(target, packageName string) error {
	contents := strings.Replace(dateTimeSupport, "package v1\n", "package "+packageName+"\n", 1)
	return os.WriteFile(filepath.Join(target, "time.go"), []byte(contents), 0o644)
}

const dateTimeSupport = `// Copyright (c) 2026 OceanBase.
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

// Code generated by tools/api-generate; DO NOT EDIT.

package v1

import (
	"time"

	"github.com/go-faster/jx"
	ogenjson "github.com/ogen-go/ogen/json"
)

const utcMicrosecondLayout = "2006-01-02T15:04:05.000000Z07:00"

// encodeDateTime matches Python's externally observable datetime encoding:
// values are UTC, precision is truncated to microseconds, a non-zero fraction
// contains exactly six digits, and an all-zero fraction is omitted.
func encodeDateTime(encoder *jx.Encoder, value time.Time) {
	value = value.UTC().Truncate(time.Microsecond)
	layout := time.RFC3339
	if value.Nanosecond() != 0 {
		layout = utcMicrosecondLayout
	}
	ogenjson.EncodeTimeFormat(encoder, value, layout)
}
`

// OpenAPI 3.0 ignores siblings of $ref, while the frozen document uses the
// common `$ref` + `nullable: true` spelling. Express the same contract as the
// JSON Schema union understood by ogen so required nullable references become
// real nullable Go values instead of invalid zero-value sentinels.
func normalizeNullableReferences(source []byte) ([]byte, error) {
	lines := bytes.SplitAfter(source, []byte{'\n'})
	var output bytes.Buffer
	replacements := 0
	for index := 0; index < len(lines); index++ {
		indent, reference, ok := referenceLine(lines[index])
		if !ok || index+1 >= len(lines) || !nullableLine(lines[index+1], indent) {
			output.Write(lines[index])
			continue
		}
		newline := "\n"
		if bytes.HasSuffix(lines[index], []byte("\r\n")) {
			newline = "\r\n"
		}
		fmt.Fprintf(&output, "%soneOf:%s%s  - $ref: %s%s%s  - type: \"null\"%s",
			indent, newline, indent, reference, newline, indent, newline)
		index++
		replacements++
	}
	if replacements == 0 {
		return nil, errors.New("canonical OpenAPI contains no nullable $ref siblings to normalize")
	}
	return output.Bytes(), nil
}

func referenceLine(line []byte) (indent, reference string, ok bool) {
	text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
	trimmed := strings.TrimLeft(text, " ")
	if !strings.HasPrefix(trimmed, "$ref:") {
		return "", "", false
	}
	indent = text[:len(text)-len(trimmed)]
	reference = strings.TrimSpace(strings.TrimPrefix(trimmed, "$ref:"))
	return indent, reference, reference != ""
}

func nullableLine(line []byte, indent string) bool {
	text := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
	return text == indent+"nullable: true"
}
