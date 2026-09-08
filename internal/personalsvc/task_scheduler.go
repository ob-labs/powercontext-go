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
	"context"
	"encoding/xml"
	"io"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	// TaskSchedulerTaskName is the only Task Scheduler identity owned by the
	// PowerContext personal Server manager.
	TaskSchedulerTaskName = `\PowerContext Personal Server`

	// TaskSchedulerInteractiveUser is a deliberate placeholder. The native
	// registration boundary must resolve it to the current interactive user;
	// this portable contract never selects SYSTEM or a service account.
	TaskSchedulerInteractiveUser = "{{POWERCONTEXT_CURRENT_INTERACTIVE_USER}}"

	taskSchedulerNamespace   = "http://schemas.microsoft.com/windows/2004/02/mit/task"
	taskSchedulerVersion     = "1.3"
	taskSchedulerPrincipalID = "PowerContextInteractiveUser"
	taskSchedulerProgram     = "schtasks.exe"
	taskSchedulerNotFound    = uint32(0x80070002)
	maxTaskArgumentLength    = 8192
)

// TaskSchedulerErrorKind classifies a redacted Task Scheduler contract
// failure. It never includes XML, command arguments, a user identity, or a
// native command diagnostic.
type TaskSchedulerErrorKind string

const (
	// TaskSchedulerInvalid means a task document or scheduler input is not a
	// supported PowerContext Task Scheduler shape.
	TaskSchedulerInvalid TaskSchedulerErrorKind = "invalid"
	// TaskSchedulerForeign means an existing task does not carry the expected
	// PowerContext ownership marker and canonical registration metadata.
	TaskSchedulerForeign TaskSchedulerErrorKind = "foreign"
	// TaskSchedulerExecution means schtasks.exe did not produce the required
	// machine-readable exit result.
	TaskSchedulerExecution TaskSchedulerErrorKind = "execution"
)

// TaskSchedulerError is an intentionally redacted Task Scheduler failure.
type TaskSchedulerError struct{ kind TaskSchedulerErrorKind }

func (e *TaskSchedulerError) Error() string {
	switch e.kind {
	case TaskSchedulerForeign:
		return "Task Scheduler task is not owned by PowerContext"
	case TaskSchedulerExecution:
		return "Task Scheduler command failed"
	default:
		return "Task Scheduler task is invalid"
	}
}

// Kind returns the fixed classification for the Task Scheduler failure.
func (e *TaskSchedulerError) Kind() TaskSchedulerErrorKind { return e.kind }

func taskSchedulerError(kind TaskSchedulerErrorKind) *TaskSchedulerError {
	return &TaskSchedulerError{kind: kind}
}

// TaskSchedulerSpec is an immutable Task Scheduler action and lifecycle
// declaration. Its executable is fixed by its Registration; arguments and a
// working directory remain distinct values until XML is rendered.
type TaskSchedulerSpec struct {
	registration     Registration
	arguments        []string
	workingDirectory string
	startOnLogin     bool
}

// NewTaskSchedulerSpec validates an immutable personal-server Task Scheduler
// declaration. It never resolves a user identity or invokes a native tool.
func NewTaskSchedulerSpec(
	registration Registration,
	arguments []string,
	workingDirectory string,
	startOnLogin bool,
) (TaskSchedulerSpec, error) {
	definition := registration.Definition()
	if err := definition.Validate(); err != nil || taskSchedulerShell(definition.Binary()) {
		return TaskSchedulerSpec{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	if err := validateAbsolutePath("working_directory", workingDirectory, MaxDataDirLength); err != nil {
		return TaskSchedulerSpec{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	for _, argument := range arguments {
		if !validTaskSchedulerArgument(argument) {
			return TaskSchedulerSpec{}, taskSchedulerError(TaskSchedulerInvalid)
		}
	}
	return TaskSchedulerSpec{
		registration:     registration,
		arguments:        slices.Clone(arguments),
		workingDirectory: strings.Clone(workingDirectory),
		startOnLogin:     startOnLogin,
	}, nil
}

// Registration returns the immutable canonical registration bound to the
// Task Scheduler action.
func (s TaskSchedulerSpec) Registration() Registration { return s.registration }

// Arguments returns a copy of the executable arguments, never a shell
// command line.
func (s TaskSchedulerSpec) Arguments() []string { return slices.Clone(s.arguments) }

// WorkingDirectory returns the action's working directory, separately from
// the executable and its arguments.
func (s TaskSchedulerSpec) WorkingDirectory() string { return s.workingDirectory }

// StartOnLogin reports whether the Task Scheduler document includes exactly
// one logon trigger.
func (s TaskSchedulerSpec) StartOnLogin() bool { return s.startOnLogin }

// Matches reports whether another complete Task Scheduler declaration has
// the same registration, argv, working directory, and trigger policy.
func (s TaskSchedulerSpec) Matches(other TaskSchedulerSpec) bool {
	return s.registration == other.registration &&
		slices.Equal(s.arguments, other.arguments) &&
		s.workingDirectory == other.workingDirectory &&
		s.startOnLogin == other.startOnLogin
}

// XML renders a Task Scheduler 1.3 XML document encoded as UTF-16LE with a
// BOM. Rendering does not write a task or touch the host Task Scheduler.
func (s TaskSchedulerSpec) XML() ([]byte, error) {
	canonical, err := s.registration.Encode()
	if err != nil {
		return nil, taskSchedulerError(TaskSchedulerInvalid)
	}
	document := taskSchedulerDocument{
		Version: taskSchedulerVersion,
		RegistrationInfo: taskSchedulerRegistrationInfo{
			URI:         TaskSchedulerTaskName,
			Description: OwnershipMarker + ";registration=" + canonical,
		},
		Principals: taskSchedulerPrincipals{Principal: taskSchedulerPrincipal{
			ID:        taskSchedulerPrincipalID,
			UserID:    TaskSchedulerInteractiveUser,
			LogonType: "InteractiveToken",
			RunLevel:  "LeastPrivilege",
		}},
		Settings: taskSchedulerSettings{
			MultipleInstancesPolicy:    "IgnoreNew",
			DisallowStartIfOnBatteries: "false",
			StopIfGoingOnBatteries:     "false",
			StartWhenAvailable:         "true",
			RestartOnFailure: taskSchedulerRestartOnFailure{
				Interval: "PT1M",
				Count:    "3",
			},
			ExecutionTimeLimit: "PT0S",
		},
		Actions: taskSchedulerActions{
			Context: taskSchedulerPrincipalID,
			Exec: taskSchedulerExec{
				Command:          s.registration.Definition().Binary(),
				Arguments:        encodeWindowsArguments(s.arguments),
				WorkingDirectory: s.workingDirectory,
			},
		},
	}
	if s.startOnLogin {
		document.Triggers.LogonTrigger = new(taskSchedulerLogonTrigger)
		document.Triggers.LogonTrigger.Enabled = "true"
	}

	payload, err := xml.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, taskSchedulerError(TaskSchedulerInvalid)
	}
	return encodeTaskSchedulerUTF16(`<?xml version="1.0" encoding="UTF-16"?>` + "\n" + string(payload) + "\n"), nil
}

// ParseTaskSchedulerXML strictly parses a complete owned Task Scheduler 1.3
// task. Extra actions, triggers, principals, metadata, or settings are
// rejected instead of being tolerated as compatible configuration.
func ParseTaskSchedulerXML(document []byte) (TaskSchedulerSpec, error) {
	decoded, err := decodeTaskSchedulerUTF16(document)
	if err != nil {
		return TaskSchedulerSpec{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	root, err := decodeTaskSchedulerXML(decoded)
	if err != nil {
		return TaskSchedulerSpec{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	return parseTaskSchedulerRoot(root)
}

func validTaskSchedulerArgument(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxTaskArgumentLength {
		return false
	}
	for _, character := range value {
		if character == 0 || character == '\r' || character == '\n' {
			return false
		}
	}
	return true
}

func taskSchedulerShell(path string) bool {
	name := path
	if _, suffix, found := strings.CutLast(name, `\`); found {
		name = suffix
	}
	if _, suffix, found := strings.CutLast(name, "/"); found {
		name = suffix
	}
	switch strings.ToLower(name) {
	case "cmd", "cmd.exe", "powershell", "powershell.exe", "pwsh", "pwsh.exe", "wscript", "wscript.exe":
		return true
	default:
		return false
	}
}

func encodeWindowsArguments(arguments []string) string {
	if len(arguments) == 0 {
		return ""
	}
	encoded := make([]string, len(arguments))
	for index, argument := range arguments {
		encoded[index] = quoteWindowsArgument(argument)
	}
	return strings.Join(encoded, " ")
}

func quoteWindowsArgument(argument string) string {
	if argument != "" && !strings.ContainsAny(argument, " \t\"") {
		return argument
	}
	var builder strings.Builder
	builder.Grow(len(argument) + 2)
	builder.WriteByte('"')
	backslashes := 0
	for _, character := range argument {
		if character == '\\' {
			backslashes++
			continue
		}
		if character == '"' {
			for range backslashes*2 + 1 {
				builder.WriteByte('\\')
			}
			builder.WriteRune(character)
			backslashes = 0
			continue
		}
		for range backslashes {
			builder.WriteByte('\\')
		}
		backslashes = 0
		builder.WriteRune(character)
	}
	for range backslashes * 2 {
		builder.WriteByte('\\')
	}
	builder.WriteByte('"')
	return builder.String()
}

func parseWindowsArguments(commandLine string) ([]string, bool) {
	if commandLine == "" {
		return nil, true
	}
	arguments := make([]string, 0, 4)
	for position := 0; position < len(commandLine); {
		for position < len(commandLine) && (commandLine[position] == ' ' || commandLine[position] == '\t') {
			position++
		}
		if position == len(commandLine) {
			return nil, false
		}
		var argument strings.Builder
		quoted := false
		for position < len(commandLine) {
			if !quoted && (commandLine[position] == ' ' || commandLine[position] == '\t') {
				break
			}
			backslashes := 0
			for position < len(commandLine) && commandLine[position] == '\\' {
				backslashes++
				position++
			}
			if position < len(commandLine) && commandLine[position] == '"' {
				for range backslashes / 2 {
					argument.WriteByte('\\')
				}
				if backslashes%2 == 0 {
					quoted = !quoted
				} else {
					argument.WriteByte('"')
				}
				position++
				continue
			}
			for range backslashes {
				argument.WriteByte('\\')
			}
			if position == len(commandLine) {
				break
			}
			argument.WriteByte(commandLine[position])
			position++
		}
		if quoted {
			return nil, false
		}
		arguments = append(arguments, argument.String())
		for position < len(commandLine) && (commandLine[position] == ' ' || commandLine[position] == '\t') {
			position++
		}
	}
	return arguments, true
}

func encodeTaskSchedulerUTF16(document string) []byte {
	codeUnits := utf16.Encode([]rune(document))
	encoded := make([]byte, 2, 2+len(codeUnits)*2)
	encoded[0] = 0xff
	encoded[1] = 0xfe
	for _, codeUnit := range codeUnits {
		encoded = append(encoded, byte(codeUnit), byte(codeUnit>>8))
	}
	return encoded
}

func decodeTaskSchedulerUTF16(document []byte) (string, error) {
	if len(document) < 2 || len(document)%2 != 0 || document[0] != 0xff || document[1] != 0xfe {
		return "", taskSchedulerError(TaskSchedulerInvalid)
	}
	codeUnits := make([]uint16, 0, (len(document)-2)/2)
	for index := 2; index < len(document); index += 2 {
		codeUnits = append(codeUnits, uint16(document[index])|uint16(document[index+1])<<8)
	}
	for index := 0; index < len(codeUnits); index++ {
		codeUnit := codeUnits[index]
		if codeUnit >= 0xd800 && codeUnit <= 0xdbff {
			if index+1 == len(codeUnits) || codeUnits[index+1] < 0xdc00 || codeUnits[index+1] > 0xdfff {
				return "", taskSchedulerError(TaskSchedulerInvalid)
			}
			index++
			continue
		}
		if codeUnit >= 0xdc00 && codeUnit <= 0xdfff {
			return "", taskSchedulerError(TaskSchedulerInvalid)
		}
	}
	return string(utf16.Decode(codeUnits)), nil
}

type taskSchedulerDocument struct {
	XMLName          xml.Name                      `xml:"http://schemas.microsoft.com/windows/2004/02/mit/task Task"`
	Version          string                        `xml:"version,attr"`
	RegistrationInfo taskSchedulerRegistrationInfo `xml:"RegistrationInfo"`
	Triggers         taskSchedulerTriggers         `xml:"Triggers"`
	Principals       taskSchedulerPrincipals       `xml:"Principals"`
	Settings         taskSchedulerSettings         `xml:"Settings"`
	Actions          taskSchedulerActions          `xml:"Actions"`
}

type taskSchedulerRegistrationInfo struct {
	URI         string `xml:"URI"`
	Description string `xml:"Description"`
}

type taskSchedulerTriggers struct {
	LogonTrigger *taskSchedulerLogonTrigger `xml:"LogonTrigger,omitempty"`
}

type taskSchedulerLogonTrigger struct {
	Enabled string `xml:"Enabled"`
}

type taskSchedulerPrincipals struct {
	Principal taskSchedulerPrincipal `xml:"Principal"`
}

type taskSchedulerPrincipal struct {
	ID        string `xml:"id,attr"`
	UserID    string `xml:"UserId"`
	LogonType string `xml:"LogonType"`
	RunLevel  string `xml:"RunLevel"`
}

type taskSchedulerSettings struct {
	MultipleInstancesPolicy    string                        `xml:"MultipleInstancesPolicy"`
	DisallowStartIfOnBatteries string                        `xml:"DisallowStartIfOnBatteries"`
	StopIfGoingOnBatteries     string                        `xml:"StopIfGoingOnBatteries"`
	StartWhenAvailable         string                        `xml:"StartWhenAvailable"`
	RestartOnFailure           taskSchedulerRestartOnFailure `xml:"RestartOnFailure"`
	ExecutionTimeLimit         string                        `xml:"ExecutionTimeLimit"`
}

type taskSchedulerRestartOnFailure struct {
	Interval string `xml:"Interval"`
	Count    string `xml:"Count"`
}

type taskSchedulerActions struct {
	Context string            `xml:"Context,attr"`
	Exec    taskSchedulerExec `xml:"Exec"`
}

type taskSchedulerExec struct {
	Command          string `xml:"Command"`
	Arguments        string `xml:"Arguments"`
	WorkingDirectory string `xml:"WorkingDirectory"`
}

type taskSchedulerXMLNode struct {
	name     xml.Name
	attrs    []xml.Attr
	text     string
	children []taskSchedulerXMLNode
}

func decodeTaskSchedulerXML(document string) (taskSchedulerXMLNode, error) {
	decoder := xml.NewDecoder(strings.NewReader(document))
	decoder.Strict = true
	decoder.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(label, "UTF-16") {
			return input, nil
		}
		return nil, taskSchedulerError(TaskSchedulerInvalid)
	}

	declaration := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return taskSchedulerXMLNode{}, taskSchedulerError(TaskSchedulerInvalid)
		}
		if err != nil {
			return taskSchedulerXMLNode{}, err
		}
		switch value := token.(type) {
		case xml.ProcInst:
			if declaration || value.Target != "xml" || string(value.Inst) != `version="1.0" encoding="UTF-16"` {
				return taskSchedulerXMLNode{}, taskSchedulerError(TaskSchedulerInvalid)
			}
			declaration = true
			continue
		case xml.CharData:
			if strings.TrimSpace(string(value)) == "" {
				continue
			}
			return taskSchedulerXMLNode{}, taskSchedulerError(TaskSchedulerInvalid)
		case xml.StartElement:
			if !declaration {
				return taskSchedulerXMLNode{}, taskSchedulerError(TaskSchedulerInvalid)
			}
			root, err := decodeTaskSchedulerXMLNode(decoder, value)
			if err != nil {
				return taskSchedulerXMLNode{}, err
			}
			for {
				tail, err := decoder.Token()
				if err == io.EOF {
					return root, nil
				}
				if err != nil {
					return taskSchedulerXMLNode{}, err
				}
				if whitespace, found := tail.(xml.CharData); found && strings.TrimSpace(string(whitespace)) == "" {
					continue
				}
				return taskSchedulerXMLNode{}, taskSchedulerError(TaskSchedulerInvalid)
			}
		default:
			return taskSchedulerXMLNode{}, taskSchedulerError(TaskSchedulerInvalid)
		}
	}
}

func decodeTaskSchedulerXMLNode(decoder *xml.Decoder, start xml.StartElement) (taskSchedulerXMLNode, error) {
	node := taskSchedulerXMLNode{name: start.Name, attrs: slices.Clone(start.Attr)}
	for {
		token, err := decoder.Token()
		if err != nil {
			return taskSchedulerXMLNode{}, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			child, err := decodeTaskSchedulerXMLNode(decoder, value)
			if err != nil {
				return taskSchedulerXMLNode{}, err
			}
			node.children = append(node.children, child)
		case xml.CharData:
			node.text += string(value)
		case xml.EndElement:
			if value.Name != start.Name {
				return taskSchedulerXMLNode{}, taskSchedulerError(TaskSchedulerInvalid)
			}
			return node, nil
		default:
			return taskSchedulerXMLNode{}, taskSchedulerError(TaskSchedulerInvalid)
		}
	}
}

func parseTaskSchedulerRoot(root taskSchedulerXMLNode) (TaskSchedulerSpec, error) {
	children, valid := taskSchedulerChildMap(
		root,
		"Task",
		map[string]string{"xmlns": taskSchedulerNamespace, "version": taskSchedulerVersion},
		[]string{"RegistrationInfo", "Triggers", "Principals", "Settings", "Actions"},
		[]string{"RegistrationInfo", "Triggers", "Principals", "Settings", "Actions"},
	)
	if !valid {
		return TaskSchedulerSpec{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	registration, err := parseTaskSchedulerRegistration(children["RegistrationInfo"])
	if err != nil {
		return TaskSchedulerSpec{}, err
	}
	startOnLogin, valid := parseTaskSchedulerTriggers(children["Triggers"])
	if !valid || !parseTaskSchedulerPrincipals(children["Principals"]) || !parseTaskSchedulerSettings(children["Settings"]) {
		return TaskSchedulerSpec{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	arguments, workingDirectory, valid := parseTaskSchedulerActions(children["Actions"], registration)
	if !valid {
		return TaskSchedulerSpec{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	specification, err := NewTaskSchedulerSpec(registration, arguments, workingDirectory, startOnLogin)
	if err != nil {
		return TaskSchedulerSpec{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	return specification, nil
}

func parseTaskSchedulerRegistration(node taskSchedulerXMLNode) (Registration, error) {
	children, valid := taskSchedulerChildMap(
		node,
		"RegistrationInfo",
		nil,
		[]string{"URI", "Description"},
		[]string{"URI", "Description"},
	)
	if !valid || !taskSchedulerLeaf(children["URI"], "URI", TaskSchedulerTaskName) {
		return Registration{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	description, valid := taskSchedulerLeafText(children["Description"], "Description")
	if !valid {
		return Registration{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	encoded, found := strings.CutPrefix(description, OwnershipMarker+";registration=")
	if !found || encoded == "" {
		return Registration{}, taskSchedulerError(TaskSchedulerForeign)
	}
	registration, err := DecodeRegistration(encoded)
	if err != nil {
		return Registration{}, taskSchedulerError(TaskSchedulerForeign)
	}
	return registration, nil
}

func parseTaskSchedulerTriggers(node taskSchedulerXMLNode) (bool, bool) {
	if node.name.Space != taskSchedulerNamespace || node.name.Local != "Triggers" ||
		!taskSchedulerAttributes(node.attrs, nil) || strings.TrimSpace(node.text) != "" {
		return false, false
	}
	if len(node.children) == 0 {
		return false, true
	}
	if len(node.children) != 1 {
		return false, false
	}
	trigger, valid := taskSchedulerContainer(node.children[0], "LogonTrigger", nil, "Enabled")
	enabled := valid && taskSchedulerLeaf(trigger[0], "Enabled", "true")
	return enabled, enabled
}

func parseTaskSchedulerPrincipals(node taskSchedulerXMLNode) bool {
	children, valid := taskSchedulerContainer(node, "Principals", nil, "Principal")
	if !valid {
		return false
	}
	principal, valid := taskSchedulerChildMap(
		children[0],
		"Principal",
		map[string]string{"id": taskSchedulerPrincipalID},
		[]string{"UserId", "LogonType", "RunLevel"},
		[]string{"UserId", "LogonType"},
	)
	if !valid ||
		!taskSchedulerLeaf(principal["UserId"], "UserId", TaskSchedulerInteractiveUser) ||
		!taskSchedulerLeaf(principal["LogonType"], "LogonType", "InteractiveToken") {
		return false
	}
	runLevel, found := principal["RunLevel"]
	return !found || taskSchedulerLeaf(runLevel, "RunLevel", "LeastPrivilege")
}

func parseTaskSchedulerSettings(node taskSchedulerXMLNode) bool {
	children, valid := taskSchedulerChildMap(
		node,
		"Settings",
		nil,
		[]string{
			"MultipleInstancesPolicy", "DisallowStartIfOnBatteries", "StopIfGoingOnBatteries",
			"StartWhenAvailable", "RestartOnFailure", "ExecutionTimeLimit", "IdleSettings",
			"UseUnifiedSchedulingEngine", "Enabled",
		},
		[]string{
			"MultipleInstancesPolicy", "DisallowStartIfOnBatteries", "StopIfGoingOnBatteries",
			"StartWhenAvailable", "RestartOnFailure", "ExecutionTimeLimit",
		},
	)
	if !valid ||
		!taskSchedulerLeaf(children["MultipleInstancesPolicy"], "MultipleInstancesPolicy", "IgnoreNew") ||
		!taskSchedulerLeaf(children["DisallowStartIfOnBatteries"], "DisallowStartIfOnBatteries", "false") ||
		!taskSchedulerLeaf(children["StopIfGoingOnBatteries"], "StopIfGoingOnBatteries", "false") ||
		!taskSchedulerLeaf(children["StartWhenAvailable"], "StartWhenAvailable", "true") ||
		!taskSchedulerLeaf(children["ExecutionTimeLimit"], "ExecutionTimeLimit", "PT0S") {
		return false
	}
	restart, valid := taskSchedulerChildMap(
		children["RestartOnFailure"],
		"RestartOnFailure",
		nil,
		[]string{"Interval", "Count"},
		[]string{"Interval", "Count"},
	)
	if !valid || !taskSchedulerLeaf(restart["Interval"], "Interval", "PT1M") ||
		!taskSchedulerLeaf(restart["Count"], "Count", "3") {
		return false
	}
	if idle, found := children["IdleSettings"]; found {
		settings, valid := taskSchedulerChildMap(
			idle,
			"IdleSettings",
			nil,
			[]string{"StopOnIdleEnd", "RestartOnIdle"},
			[]string{"StopOnIdleEnd", "RestartOnIdle"},
		)
		if !valid || !taskSchedulerLeaf(settings["StopOnIdleEnd"], "StopOnIdleEnd", "true") ||
			!taskSchedulerLeaf(settings["RestartOnIdle"], "RestartOnIdle", "false") {
			return false
		}
	}
	if unified, found := children["UseUnifiedSchedulingEngine"]; found &&
		!taskSchedulerLeaf(unified, "UseUnifiedSchedulingEngine", "true") {
		return false
	}
	if enabled, found := children["Enabled"]; found &&
		!taskSchedulerLeaf(enabled, "Enabled", "true") && !taskSchedulerLeaf(enabled, "Enabled", "false") {
		return false
	}
	return true
}

func parseTaskSchedulerActions(node taskSchedulerXMLNode, registration Registration) ([]string, string, bool) {
	children, valid := taskSchedulerContainer(node, "Actions", map[string]string{"Context": taskSchedulerPrincipalID}, "Exec")
	if !valid {
		return nil, "", false
	}
	exec, valid := taskSchedulerContainer(children[0], "Exec", nil, "Command", "Arguments", "WorkingDirectory")
	if !valid || !taskSchedulerLeaf(exec[0], "Command", registration.Definition().Binary()) {
		return nil, "", false
	}
	encodedArguments, valid := taskSchedulerLeafText(exec[1], "Arguments")
	if !valid {
		return nil, "", false
	}
	arguments, valid := parseWindowsArguments(encodedArguments)
	if !valid || encodeWindowsArguments(arguments) != encodedArguments {
		return nil, "", false
	}
	workingDirectory, valid := taskSchedulerLeafText(exec[2], "WorkingDirectory")
	if !valid {
		return nil, "", false
	}
	return arguments, workingDirectory, true
}

func taskSchedulerContainer(
	node taskSchedulerXMLNode,
	name string,
	attributes map[string]string,
	children ...string,
) ([]taskSchedulerXMLNode, bool) {
	if node.name.Space != taskSchedulerNamespace || node.name.Local != name ||
		!taskSchedulerAttributes(node.attrs, attributes) || strings.TrimSpace(node.text) != "" || len(node.children) != len(children) {
		return nil, false
	}
	for index, child := range node.children {
		if child.name.Space != taskSchedulerNamespace || child.name.Local != children[index] {
			return nil, false
		}
	}
	return node.children, true
}

func taskSchedulerChildMap(
	node taskSchedulerXMLNode,
	name string,
	attributes map[string]string,
	allowed []string,
	required []string,
) (map[string]taskSchedulerXMLNode, bool) {
	if node.name.Space != taskSchedulerNamespace || node.name.Local != name ||
		!taskSchedulerAttributes(node.attrs, attributes) || strings.TrimSpace(node.text) != "" {
		return nil, false
	}
	allowedNames := make(map[string]struct{}, len(allowed))
	for _, child := range allowed {
		allowedNames[child] = struct{}{}
	}
	children := make(map[string]taskSchedulerXMLNode, len(node.children))
	for _, child := range node.children {
		if child.name.Space != taskSchedulerNamespace {
			return nil, false
		}
		if _, found := allowedNames[child.name.Local]; !found {
			return nil, false
		}
		if _, duplicate := children[child.name.Local]; duplicate {
			return nil, false
		}
		children[child.name.Local] = child
	}
	for _, child := range required {
		if _, found := children[child]; !found {
			return nil, false
		}
	}
	return children, true
}

func taskSchedulerAttributes(attributes []xml.Attr, expected map[string]string) bool {
	if len(attributes) != len(expected) {
		return false
	}
	for _, attribute := range attributes {
		value, found := expected[attribute.Name.Local]
		if !found || attribute.Name.Space != "" || attribute.Value != value {
			return false
		}
	}
	return true
}

func taskSchedulerLeaf(node taskSchedulerXMLNode, name, expected string) bool {
	value, valid := taskSchedulerLeafText(node, name)
	return valid && value == expected
}

func taskSchedulerLeafText(node taskSchedulerXMLNode, name string) (string, bool) {
	if node.name.Space != taskSchedulerNamespace || node.name.Local != name || len(node.attrs) != 0 || len(node.children) != 0 {
		return "", false
	}
	return node.text, true
}

// TaskSchedulerResult is the process result used by the portable task
// contract. Output is retained only for command forms whose structured XML or
// CSV payload is consumed by the Windows host adapter; native diagnostics must
// not be returned in this field.
type TaskSchedulerResult struct {
	ExitCode uint32
	Output   []byte
}

// TaskSchedulerExecutor is the native process boundary used by TaskScheduler.
// Implementations receive only the fixed schtasks.exe command forms built
// below; the contract never invokes a shell.
type TaskSchedulerExecutor interface {
	Execute(context.Context, string, []string) (TaskSchedulerResult, error)
}

// TaskSchedulerState is the machine-readable result of querying the one owned
// Task Scheduler identity.
type TaskSchedulerState string

const (
	// TaskSchedulerPresent means schtasks.exe reported the fixed task exists.
	TaskSchedulerPresent TaskSchedulerState = "present"
	// TaskSchedulerAbsent means schtasks.exe reported ERROR_FILE_NOT_FOUND.
	TaskSchedulerAbsent TaskSchedulerState = "absent"
	// TaskSchedulerUnknown means no supported exit code described the task.
	TaskSchedulerUnknown TaskSchedulerState = "unknown"
)

// TaskScheduler constructs fixed, shell-free schtasks.exe invocations. It
// must be constructed with NewTaskScheduler. It does not register a task by
// itself; an injected process boundary owns that native side effect.
type TaskScheduler struct{ executor TaskSchedulerExecutor }

// NewTaskScheduler validates the native process boundary.
func NewTaskScheduler(executor TaskSchedulerExecutor) (TaskScheduler, error) {
	if executor == nil {
		return TaskScheduler{}, taskSchedulerError(TaskSchedulerInvalid)
	}
	return TaskScheduler{executor: executor}, nil
}

// Create asks the executor to create or replace only the fixed owned task from
// an already-written XML path. XML file creation is deliberately outside this
// portable command contract.
func (s TaskScheduler) Create(ctx context.Context, xmlPath string) error {
	if err := validateAbsolutePath("xml_path", xmlPath, MaxDataDirLength); err != nil {
		return taskSchedulerError(TaskSchedulerInvalid)
	}
	_, err := s.execute(ctx, []string{"/Create", "/TN", TaskSchedulerTaskName, "/XML", xmlPath, "/F", "/HRESULT"})
	return err
}

// Query maps only schtasks.exe exit codes. It deliberately ignores output so
// locale changes cannot alter ownership or lifecycle decisions.
func (s TaskScheduler) Query(ctx context.Context) (TaskSchedulerState, error) {
	state, _, err := s.QueryXML(ctx)
	return state, err
}

// QueryXML maps the fixed query exit code and returns an independent copy of
// the task XML for host-owned identity normalization and ownership parsing.
func (s TaskScheduler) QueryXML(ctx context.Context) (TaskSchedulerState, []byte, error) {
	result, err := s.executeRaw(ctx, []string{"/Query", "/TN", TaskSchedulerTaskName, "/XML", "/HRESULT"})
	if err != nil {
		return TaskSchedulerUnknown, nil, taskSchedulerError(TaskSchedulerExecution)
	}
	switch result.ExitCode {
	case 0:
		return TaskSchedulerPresent, slices.Clone(result.Output), nil
	case taskSchedulerNotFound:
		return TaskSchedulerAbsent, nil, nil
	default:
		return TaskSchedulerUnknown, nil, taskSchedulerError(TaskSchedulerExecution)
	}
}

// Delete asks the executor to remove only the fixed owned Task Scheduler
// identity. Ownership verification belongs to ParseTaskSchedulerXML before a
// future adapter invokes this command.
func (s TaskScheduler) Delete(ctx context.Context) error {
	_, err := s.execute(ctx, []string{"/Delete", "/TN", TaskSchedulerTaskName, "/F", "/HRESULT"})
	return err
}

// Run asks the executor to run only the fixed owned Task Scheduler identity.
func (s TaskScheduler) Run(ctx context.Context) error {
	_, err := s.execute(ctx, []string{"/Run", "/TN", TaskSchedulerTaskName, "/HRESULT"})
	return err
}

// End asks the executor to stop only the fixed owned Task Scheduler identity.
func (s TaskScheduler) End(ctx context.Context) error {
	_, err := s.execute(ctx, []string{"/End", "/TN", TaskSchedulerTaskName, "/HRESULT"})
	return err
}

// Enable asks the executor to enable only the fixed owned Task Scheduler identity.
func (s TaskScheduler) Enable(ctx context.Context) error {
	_, err := s.execute(ctx, []string{"/Change", "/TN", TaskSchedulerTaskName, "/ENABLE", "/HRESULT"})
	return err
}

// Disable asks the executor to disable only the fixed owned Task Scheduler identity.
func (s TaskScheduler) Disable(ctx context.Context) error {
	_, err := s.execute(ctx, []string{"/Change", "/TN", TaskSchedulerTaskName, "/DISABLE", "/HRESULT"})
	return err
}

// QueryStatus returns the fixed verbose CSV query for locale-independent
// numeric Task Scheduler result classification in the Windows host adapter.
func (s TaskScheduler) QueryStatus(ctx context.Context) ([]byte, error) {
	result, err := s.execute(ctx, []string{
		"/Query", "/TN", TaskSchedulerTaskName, "/FO", "CSV", "/NH", "/V", "/HRESULT",
	})
	if err != nil {
		return nil, err
	}
	return slices.Clone(result.Output), nil
}

func (s TaskScheduler) execute(ctx context.Context, arguments []string) (TaskSchedulerResult, error) {
	result, err := s.executeRaw(ctx, arguments)
	if err != nil || result.ExitCode != 0 {
		return TaskSchedulerResult{}, taskSchedulerError(TaskSchedulerExecution)
	}
	return result, nil
}

func (s TaskScheduler) executeRaw(ctx context.Context, arguments []string) (TaskSchedulerResult, error) {
	if s.executor == nil {
		return TaskSchedulerResult{}, taskSchedulerError(TaskSchedulerExecution)
	}
	result, err := s.executor.Execute(ctx, taskSchedulerProgram, slices.Clone(arguments))
	if err != nil {
		return TaskSchedulerResult{}, taskSchedulerError(TaskSchedulerExecution)
	}
	return result, nil
}
