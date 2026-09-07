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
	"encoding/xml"
	"io"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// LaunchdLabel is the fixed per-user LaunchAgent label owned by PowerContext.
	LaunchdLabel = "com.oceanbase.powercontext"

	launchdFailureLimit  = 3
	launchdFailureWindow = time.Minute
)

// LaunchdPlanError is a redacted failure while constructing or inspecting a
// macOS LaunchAgent contract. It intentionally never retains native paths,
// endpoint values, metadata, or command arguments.
type LaunchdPlanError struct{}

func (*LaunchdPlanError) Error() string {
	return "invalid macOS personal service LaunchAgent contract"
}

// LaunchdPlan is an immutable, process-free per-user LaunchAgent declaration.
// It can render and inspect its owned plist and form launchctl arguments, but
// it never accesses the filesystem or starts a native process.
type LaunchdPlan struct {
	uid          uint64
	registration Registration
}

// NewLaunchdPlan validates an owned registration for one GUI login domain.
func NewLaunchdPlan(uid uint64, registration Registration) (LaunchdPlan, error) {
	if uid == 0 || registration.Definition().Validate() != nil || !validLaunchdDefinition(registration.Definition()) {
		return LaunchdPlan{}, newLaunchdPlanError()
	}
	return LaunchdPlan{uid: uid, registration: registration}, nil
}

// Domain returns the exact per-user launchctl domain target.
func (p LaunchdPlan) Domain() string {
	return "gui/" + strconv.FormatUint(p.uid, 10)
}

// ServiceTarget returns the exact per-user launchctl service target.
func (p LaunchdPlan) ServiceTarget() string {
	return p.Domain() + "/" + LaunchdLabel
}

// Render returns a deterministic XML plist for the exact owned registration.
func (p LaunchdPlan) Render() ([]byte, error) {
	if !p.valid() {
		return nil, newLaunchdPlanError()
	}
	metadata, err := p.registration.Encode()
	if err != nil {
		return nil, newLaunchdPlanError()
	}
	host, port, ok := launchdEndpoint(p.registration.Definition().Endpoint())
	if !ok {
		return nil, newLaunchdPlanError()
	}

	definition := p.registration.Definition()
	logs := path.Join(definition.DataDir(), "logs")
	programArguments := []string{
		definition.Binary(), "server", "run", "--host", host, "--port", port,
	}
	environment := [][2]string{
		{"POWERCONTEXT_HOME", definition.DataDir()},
		{"POWERCONTEXT_SERVICE_METADATA", metadata},
		{"POWERCONTEXT_SERVICE_OWNED", "true"},
	}
	return renderLaunchdPlist(programArguments, environment, logs), nil
}

// Inspect verifies an on-disk plist as the exact owned shape for this plan.
// It rejects any path, program, argument, marker, metadata, or policy change.
func (p LaunchdPlan) Inspect(document []byte) (Registration, error) {
	if !p.valid() {
		return Registration{}, newLaunchdPlanError()
	}
	metadata, ok := launchdMetadata(document)
	if !ok {
		return Registration{}, newLaunchdPlanError()
	}
	registration, err := DecodeRegistration(metadata)
	if err != nil || registration != p.registration {
		return Registration{}, newLaunchdPlanError()
	}
	expected, err := p.Render()
	if err != nil || !bytes.Equal(document, expected) {
		return Registration{}, newLaunchdPlanError()
	}
	return registration, nil
}

// BootstrapArgv returns the exact argv that bootstraps this plist into the
// plan's GUI domain. Callers execute it through their own native boundary.
func (p LaunchdPlan) BootstrapArgv(plistPath string) ([]string, error) {
	if !p.valid() || !validLaunchdArtifactPath(plistPath) {
		return nil, newLaunchdPlanError()
	}
	return []string{"launchctl", "bootstrap", p.Domain(), plistPath}, nil
}

// BootoutArgv returns the exact argv that removes this owned service target.
func (p LaunchdPlan) BootoutArgv() ([]string, error) {
	if !p.valid() {
		return nil, newLaunchdPlanError()
	}
	return []string{"launchctl", "bootout", p.ServiceTarget()}, nil
}

// PrintArgv returns the exact argv that inspects this owned service target.
func (p LaunchdPlan) PrintArgv() ([]string, error) {
	if !p.valid() {
		return nil, newLaunchdPlanError()
	}
	return []string{"launchctl", "print", p.ServiceTarget()}, nil
}

func (p LaunchdPlan) valid() bool {
	return p.uid != 0 && p.registration.Definition().Validate() == nil && validLaunchdDefinition(p.registration.Definition())
}

func validLaunchdDefinition(definition Definition) bool {
	if !validLaunchdPath(definition.Binary()) || !validLaunchdPath(definition.DataDir()) {
		return false
	}
	_, _, ok := launchdEndpoint(definition.Endpoint())
	return ok
}

func validLaunchdArtifactPath(value string) bool {
	return validLaunchdPath(value) && path.Base(value) == LaunchdLabel+".plist"
}

func validLaunchdPath(value string) bool {
	if !strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	for segment := range strings.SplitSeq(value, "/") {
		if segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func launchdEndpoint(value string) (string, string, bool) {
	endpoint, err := url.Parse(value)
	if err != nil || endpoint == nil || endpoint.Path != "" || endpoint.RawPath != "" {
		return "", "", false
	}
	port := endpoint.Port()
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 || endpoint.Hostname() == "" {
		return "", "", false
	}
	return endpoint.Hostname(), port, true
}

func renderLaunchdPlist(programArguments []string, environment [][2]string, logs string) []byte {
	var output strings.Builder
	output.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	output.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	output.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writeLaunchdDict(&output, "EnvironmentVariables", environment)
	writeLaunchdPathState(&output, path.Join(logs, "launchd-retry.enabled"))
	writeLaunchdString(&output, "Label", LaunchdLabel)
	writeLaunchdString(&output, "ProcessType", "Background")
	output.WriteString("\t<key>ProgramArguments</key>\n\t<array>\n")
	for _, argument := range programArguments {
		output.WriteString("\t\t<string>")
		writeLaunchdEscaped(&output, argument)
		output.WriteString("</string>\n")
	}
	output.WriteString("\t</array>\n")
	output.WriteString("\t<key>RunAtLoad</key>\n\t<true/>\n")
	writeLaunchdString(&output, "StandardErrorPath", path.Join(logs, "server.stderr.log"))
	writeLaunchdString(&output, "StandardOutPath", path.Join(logs, "server.stdout.log"))
	output.WriteString("\t<key>ThrottleInterval</key>\n\t<integer>5</integer>\n")
	output.WriteString("</dict>\n</plist>\n")
	return []byte(output.String())
}

func writeLaunchdDict(output *strings.Builder, key string, values [][2]string) {
	output.WriteString("\t<key>")
	writeLaunchdEscaped(output, key)
	output.WriteString("</key>\n\t<dict>\n")
	for _, value := range values {
		writeLaunchdString(output, value[0], value[1])
	}
	output.WriteString("\t</dict>\n")
}

func writeLaunchdPathState(output *strings.Builder, tokenPath string) {
	output.WriteString("\t<key>KeepAlive</key>\n\t<dict>\n\t\t<key>PathState</key>\n\t\t<dict>\n\t\t\t<key>")
	writeLaunchdEscaped(output, tokenPath)
	output.WriteString("</key>\n\t\t\t<true/>\n\t\t</dict>\n\t</dict>\n")
}

func writeLaunchdString(output *strings.Builder, key, value string) {
	output.WriteString("\t<key>")
	writeLaunchdEscaped(output, key)
	output.WriteString("</key>\n\t<string>")
	writeLaunchdEscaped(output, value)
	output.WriteString("</string>\n")
}

func writeLaunchdEscaped(output *strings.Builder, value string) {
	var escaped bytes.Buffer
	if xml.EscapeText(&escaped, []byte(value)) != nil {
		return
	}
	output.Write(escaped.Bytes())
}

type launchdXMLNode struct {
	XMLName  xml.Name
	Children []launchdXMLNode `xml:",any"`
	Text     string           `xml:",chardata"`
}

func launchdMetadata(document []byte) (string, bool) {
	decoder := xml.NewDecoder(bytes.NewReader(document))
	var root launchdXMLNode
	if err := decoder.Decode(&root); err != nil || root.XMLName.Local != "plist" {
		return "", false
	}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false
		}
		if text, ok := token.(xml.CharData); !ok || strings.TrimSpace(string(text)) != "" {
			return "", false
		}
	}
	if len(root.Children) != 1 || root.Children[0].XMLName.Local != "dict" {
		return "", false
	}
	dictionary, ok := launchdDictionaryValue(root.Children[0], "EnvironmentVariables")
	if !ok || dictionary.XMLName.Local != "dict" {
		return "", false
	}
	metadata, ok := launchdDictionaryValue(*dictionary, "POWERCONTEXT_SERVICE_METADATA")
	if !ok || metadata.XMLName.Local != "string" {
		return "", false
	}
	return metadata.Text, true
}

func launchdDictionaryValue(dictionary launchdXMLNode, wanted string) (*launchdXMLNode, bool) {
	if dictionary.XMLName.Local != "dict" || len(dictionary.Children)%2 != 0 {
		return nil, false
	}
	for index := 0; index < len(dictionary.Children); index += 2 {
		key := dictionary.Children[index]
		if key.XMLName.Local == "key" && key.Text == wanted {
			return &dictionary.Children[index+1], true
		}
	}
	return nil, false
}

// LaunchdExit classifies one launcher outcome without exposing a native exit
// code or any process output.
type LaunchdExit string

const (
	// LaunchdExitFailure records a failed launcher attempt.
	LaunchdExitFailure LaunchdExit = "failure"
	// LaunchdExitClean records a clean owned launcher exit.
	LaunchdExitClean LaunchdExit = "clean"
	// LaunchdExitAlreadyLive records a clean exit because the Server is already live.
	LaunchdExitAlreadyLive LaunchdExit = "already_live"
)

// LaunchdRetryState is a pure immutable retry-token state. A fresh install
// creates the only state that may contain an enabled retry token.
type LaunchdRetryState struct {
	tokenPresent bool
	failures     []time.Time
}

// NewLaunchdRetryStateForFreshInstall creates the retry state for a fresh
// successful install. Re-enabling an exhausted token requires this explicit
// install transition rather than elapsed time or a clean process exit.
func NewLaunchdRetryStateForFreshInstall() LaunchdRetryState {
	return LaunchdRetryState{tokenPresent: true}
}

// TokenPresent reports whether the owned PathState retry token remains enabled.
func (s LaunchdRetryState) TokenPresent() bool { return s.tokenPresent }

// FailureCount reports retained failure history for this installation token.
func (s LaunchdRetryState) FailureCount() int { return len(s.failures) }

// ObserveLaunchdExit returns the next retry state using a caller-supplied
// clock. The third failure in a minute removes the token; a clean exit removes
// it without consuming a failure. An exhausted token never re-enables itself.
func (s LaunchdRetryState) ObserveLaunchdExit(clock func() time.Time, outcome LaunchdExit) (LaunchdRetryState, error) {
	if !s.tokenPresent {
		return LaunchdRetryState{failures: slices.Clone(s.failures)}, nil
	}
	if clock == nil {
		return LaunchdRetryState{}, newLaunchdPlanError()
	}
	now := clock()
	if now.IsZero() {
		return LaunchdRetryState{}, newLaunchdPlanError()
	}
	state := LaunchdRetryState{tokenPresent: true, failures: activeLaunchdFailures(s.failures, now)}
	switch outcome {
	case LaunchdExitClean, LaunchdExitAlreadyLive:
		state.tokenPresent = false
		return state, nil
	case LaunchdExitFailure:
		state.failures = append(state.failures, now)
		if len(state.failures) >= launchdFailureLimit {
			state.tokenPresent = false
		}
		return state, nil
	default:
		return LaunchdRetryState{}, newLaunchdPlanError()
	}
}

func activeLaunchdFailures(failures []time.Time, now time.Time) []time.Time {
	active := make([]time.Time, 0, len(failures))
	for _, failure := range failures {
		if failure.IsZero() || failure.After(now) || now.Sub(failure) > launchdFailureWindow {
			continue
		}
		active = append(active, failure)
	}
	return active
}

func newLaunchdPlanError() *LaunchdPlanError { return &LaunchdPlanError{} }
