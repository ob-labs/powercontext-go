// Copyright (c) 2026 OceanBase.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package personalsvc

import (
	"bytes"
	"encoding/base64"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/internal/transportpolicy"
)

const (
	// OwnershipMarker is the only ownership marker a PowerContext native
	// manager may treat as its own registration.
	OwnershipMarker = "powercontext.personal-server"

	// DefinitionVersion is the currently supported registration metadata
	// format. A newer value must be deliberately introduced with a parser and
	// migration decision rather than silently accepted.
	DefinitionVersion uint = 3

	MaxOwnershipLength      = 128
	MaxPackageVersionLength = 128
	MaxBinaryLength         = 4096
	MaxEndpointLength       = 2048
	MaxDataDirLength        = 4096
	MaxEnvFileLength        = 4096

	maxDecodedRegistrationLength = 12 * 1024
	maxEncodedRegistrationLength = 16 * 1024
)

// InvalidMetadataError describes a rejected personal-service declaration. It
// never retains or renders caller-provided metadata, which can include local
// paths or credentials embedded in an endpoint.
type InvalidMetadataError struct {
	Field  string
	Reason string
}

func (e *InvalidMetadataError) Error() string {
	return fmt.Sprintf("invalid personal service metadata %s: %s", e.Field, e.Reason)
}

// DefinitionInput is the mutable construction input for a Definition.
// NewDefinition validates and copies every field into an immutable value.
type DefinitionInput struct {
	Ownership         string
	DefinitionVersion uint
	PackageVersion    string
	Binary            string
	Endpoint          string
	DataDir           string
	EnvFile           string
}

// Definition is an immutable, side-effect-free declaration that a native
// personal-service manager can later consume.
type Definition struct {
	ownership         string
	definitionVersion uint
	packageVersion    string
	binary            string
	endpoint          string
	dataDir           string
	envFile           string
}

// NewDefinition validates an owned personal-service declaration.
func NewDefinition(input DefinitionInput) (Definition, error) {
	definition := Definition{
		ownership:         input.Ownership,
		definitionVersion: input.DefinitionVersion,
		packageVersion:    input.PackageVersion,
		binary:            input.Binary,
		endpoint:          input.Endpoint,
		dataDir:           input.DataDir,
		envFile:           input.EnvFile,
	}
	if err := definition.Validate(); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

func (d Definition) Ownership() string      { return d.ownership }
func (d Definition) Version() uint          { return d.definitionVersion }
func (d Definition) PackageVersion() string { return d.packageVersion }
func (d Definition) Binary() string         { return d.binary }
func (d Definition) Endpoint() string       { return d.endpoint }
func (d Definition) DataDir() string        { return d.dataDir }
func (d Definition) EnvFile() string        { return d.envFile }

// Validate rejects zero-value, unsupported, and unsafe definitions.
func (d Definition) Validate() error {
	if err := validateText("ownership", d.ownership, MaxOwnershipLength); err != nil {
		return err
	}
	if d.ownership != OwnershipMarker {
		return invalidMetadata("ownership", "must identify the PowerContext personal Server")
	}
	if d.definitionVersion != DefinitionVersion {
		return invalidMetadata("definition_version", "is not supported")
	}
	if err := validateText("package_version", d.packageVersion, MaxPackageVersionLength); err != nil {
		return err
	}
	if err := validateAbsolutePath("binary", d.binary, MaxBinaryLength); err != nil {
		return err
	}
	if err := validateEndpoint(d.endpoint); err != nil {
		return err
	}
	if err := validateAbsolutePath("data_dir", d.dataDir, MaxDataDirLength); err != nil {
		return err
	}
	return validateNormalizedAbsolutePath("env_file", d.envFile, MaxEnvFileLength)
}

// CanonicalJSON returns the RFC 8785 canonical registration definition.
func (d Definition) CanonicalJSON() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(definitionWire{
		Ownership:         d.ownership,
		DefinitionVersion: d.definitionVersion,
		PackageVersion:    d.packageVersion,
		Binary:            d.binary,
		Endpoint:          d.endpoint,
		DataDir:           d.dataDir,
		EnvFile:           d.envFile,
	})
	if err != nil {
		return nil, invalidMetadata("payload", "cannot be encoded")
	}
	canonical := jsontext.Value(payload)
	if err := canonical.Canonicalize(); err != nil {
		return nil, invalidMetadata("payload", "cannot be canonicalized")
	}
	return bytes.Clone(canonical), nil
}

// MarshalJSON emits the canonical registration definition.
func (d Definition) MarshalJSON() ([]byte, error) {
	return d.CanonicalJSON()
}

// Registration is an immutable definition ready for native-manager transport.
type Registration struct {
	definition Definition
}

// NewRegistration validates an immutable definition before it can be encoded.
func NewRegistration(definition Definition) (Registration, error) {
	if err := definition.Validate(); err != nil {
		return Registration{}, err
	}
	return Registration{definition: definition}, nil
}

// Definition returns the immutable declaration belonging to this registration.
func (r Registration) Definition() Definition { return r.definition }

// Encode serializes the canonical definition with unpadded URL-safe base64.
func (r Registration) Encode() (string, error) {
	payload, err := r.definition.CanonicalJSON()
	if err != nil {
		return "", err
	}
	if len(payload) > maxDecodedRegistrationLength {
		return "", invalidMetadata("payload", "exceeds the supported size")
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeRegistration strictly decodes canonical, URL-safe registration
// metadata. It rejects padding, duplicate members, unknown members, and any
// semantically equivalent but noncanonical representation.
func DecodeRegistration(encoded string) (Registration, error) {
	if encoded == "" || len(encoded) > maxEncodedRegistrationLength {
		return Registration{}, invalidMetadata("payload", "is not a supported encoded registration")
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) == 0 || len(payload) > maxDecodedRegistrationLength {
		return Registration{}, invalidMetadata("payload", "is not a supported encoded registration")
	}
	if base64.RawURLEncoding.EncodeToString(payload) != encoded {
		return Registration{}, invalidMetadata("payload", "is not a canonical URL-safe encoding")
	}
	definition, err := decodeDefinition(payload)
	if err != nil {
		return Registration{}, err
	}
	canonical, err := definition.CanonicalJSON()
	if err != nil {
		return Registration{}, err
	}
	if !bytes.Equal(payload, canonical) {
		return Registration{}, invalidMetadata("payload", "is not canonical JSON")
	}
	return NewRegistration(definition)
}

type definitionWire struct {
	Ownership         string `json:"ownership"`
	DefinitionVersion uint   `json:"definition_version"`
	PackageVersion    string `json:"package_version"`
	Binary            string `json:"binary"`
	Endpoint          string `json:"endpoint"`
	DataDir           string `json:"data_dir"`
	EnvFile           string `json:"env_file"`
}

func decodeDefinition(payload []byte) (Definition, error) {
	if jsontext.Value(payload).Kind() != '{' {
		return Definition{}, invalidMetadata("payload", "must be a JSON object")
	}
	var object map[string]jsontext.Value
	if err := json.Unmarshal(payload, &object); err != nil {
		return Definition{}, invalidMetadata("payload", "must be valid JSON without duplicate members or trailing data")
	}
	if len(object) != 7 {
		for name := range object {
			if !definitionField(name) && sensitiveMetadataName(name) {
				return Definition{}, invalidMetadata("metadata", "must not contain sensitive members")
			}
		}
		return Definition{}, invalidMetadata("payload", "must contain exactly the supported members")
	}
	for name := range object {
		if !definitionField(name) {
			if sensitiveMetadataName(name) {
				return Definition{}, invalidMetadata("metadata", "must not contain sensitive members")
			}
			return Definition{}, invalidMetadata("payload", "must not contain unknown members")
		}
	}

	ownership, err := requiredString(object["ownership"], "ownership")
	if err != nil {
		return Definition{}, err
	}
	version, err := requiredUint(object["definition_version"], "definition_version")
	if err != nil {
		return Definition{}, err
	}
	packageVersion, err := requiredString(object["package_version"], "package_version")
	if err != nil {
		return Definition{}, err
	}
	binary, err := requiredString(object["binary"], "binary")
	if err != nil {
		return Definition{}, err
	}
	endpoint, err := requiredString(object["endpoint"], "endpoint")
	if err != nil {
		return Definition{}, err
	}
	dataDir, err := requiredString(object["data_dir"], "data_dir")
	if err != nil {
		return Definition{}, err
	}
	envFile, err := requiredString(object["env_file"], "env_file")
	if err != nil {
		return Definition{}, err
	}
	return NewDefinition(DefinitionInput{
		Ownership:         ownership,
		DefinitionVersion: version,
		PackageVersion:    packageVersion,
		Binary:            binary,
		Endpoint:          endpoint,
		DataDir:           dataDir,
		EnvFile:           envFile,
	})
}

func requiredString(value jsontext.Value, field string) (string, error) {
	if value.Kind() != '"' {
		return "", invalidMetadata(field, "must be a string")
	}
	var parsed string
	if err := json.Unmarshal(value, &parsed); err != nil {
		return "", invalidMetadata(field, "must be a valid string")
	}
	return parsed, nil
}

func requiredUint(value jsontext.Value, field string) (uint, error) {
	if value.Kind() != '0' {
		return 0, invalidMetadata(field, "must be an unsigned integer")
	}
	var parsed uint
	if err := json.Unmarshal(value, &parsed); err != nil {
		return 0, invalidMetadata(field, "must be an unsigned integer")
	}
	return parsed, nil
}

func validateText(field, value string, maximum int) error {
	if !utf8.ValidString(value) {
		return invalidMetadata(field, "must be valid UTF-8")
	}
	if value == "" {
		return invalidMetadata(field, "must be non-empty")
	}
	if strings.TrimFunc(value, definitionWhitespace) != value {
		return invalidMetadata(field, "must not contain leading or trailing whitespace")
	}
	if utf8.RuneCountInString(value) > maximum {
		return invalidMetadata(field, "exceeds the supported length")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return invalidMetadata(field, "must not contain control characters")
		}
	}
	return nil
}

func validateEndpoint(endpoint string) error {
	if err := validateText("endpoint", endpoint, MaxEndpointLength); err != nil {
		return err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Hostname() == "" {
		return invalidMetadata("endpoint", "must be an absolute HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return invalidMetadata("endpoint", "must use HTTP or HTTPS")
	}
	if !transportpolicy.IsLoopbackHost(parsed.Hostname()) {
		return invalidMetadata("endpoint", "must use a loopback host")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return invalidMetadata("endpoint", "must not contain credentials or request metadata")
	}
	return nil
}

func validateAbsolutePath(field, value string, maximum int) error {
	if err := validateText(field, value, maximum); err != nil {
		return err
	}
	if !absolutePath(value) {
		return invalidMetadata(field, "must be absolute")
	}
	return nil
}

func validateNormalizedAbsolutePath(field, value string, maximum int) error {
	if err := validateAbsolutePath(field, value, maximum); err != nil {
		return err
	}
	if !normalizedAbsolutePath(value) {
		return invalidMetadata(field, "must be normalized")
	}
	return nil
}

// absolutePath accepts a portable POSIX absolute path and the stable Windows
// drive and UNC forms. It deliberately does not inspect the host filesystem.
func absolutePath(value string) bool {
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && asciiLetter(value[0]) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
}

func normalizedAbsolutePath(value string) bool {
	separator := "/"
	remainder := ""
	switch {
	case strings.HasPrefix(value, "/"):
		if strings.HasPrefix(value, "//") {
			return false
		}
		remainder = value[1:]
	case strings.HasPrefix(value, `\\`):
		separator = `\`
		remainder = value[2:]
	case len(value) >= 3 && asciiLetter(value[0]) && value[1] == ':' && (value[2] == '/' || value[2] == '\\'):
		separator = value[2:3]
		remainder = value[3:]
	default:
		return false
	}
	if remainder == "" || strings.Contains(remainder, separator+separator) {
		return false
	}
	if separator == "/" && strings.ContainsRune(remainder, '\\') || separator == `\` && strings.ContainsRune(remainder, '/') {
		return false
	}
	for segment := range strings.SplitSeq(remainder, separator) {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func asciiLetter(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func definitionField(name string) bool {
	switch name {
	case "ownership", "definition_version", "package_version", "binary", "endpoint", "data_dir", "env_file":
		return true
	default:
		return false
	}
}

func sensitiveMetadataName(name string) bool {
	name = strings.ToLower(name)
	for _, marker := range [...]string{
		"authorization", "cookie", "credential", "header", "password", "secret", "token", "environment", "env",
	} {
		if strings.Contains(name, marker) {
			return true
		}
	}
	return false
}

func definitionWhitespace(character rune) bool {
	return unicode.IsSpace(character) || character >= '\x1c' && character <= '\x1f'
}

func invalidMetadata(field, reason string) *InvalidMetadataError {
	return &InvalidMetadataError{Field: field, Reason: reason}
}
