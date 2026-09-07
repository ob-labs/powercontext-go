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

package personalsvc_test

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

func TestTaskSchedulerSpecRoundTripsOwnedUTF16Task(t *testing.T) {
	spec := taskSchedulerSpec(t, true)
	document, err := spec.XML()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(document, []byte{0xff, 0xfe}) {
		t.Fatalf("task XML must begin with a UTF-16LE BOM, got %x", document[:min(len(document), 4)])
	}
	decoded := decodeUTF16LE(t, document)
	for _, required := range []string{
		`<?xml version="1.0" encoding="UTF-16"?>`,
		`<Task xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task" version="1.3">`,
		`<URI>\PowerContext Personal Server</URI>`,
		`<UserId>{{POWERCONTEXT_CURRENT_INTERACTIVE_USER}}</UserId>`,
		`<LogonType>InteractiveToken</LogonType>`,
		`<RunLevel>LeastPrivilege</RunLevel>`,
		`<MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>`,
		`<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>`,
		`<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>`,
		`<StartWhenAvailable>true</StartWhenAvailable>`,
		`<Interval>PT1M</Interval>`,
		`<Count>3</Count>`,
		`<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>`,
		`<LogonTrigger>`,
		`<Command>C:\Program Files\PowerContext\powercontext.exe</Command>`,
		`<Arguments>serve --endpoint http://127.0.0.1:8123 --data-dir C:\Users\person\AppData\Local\PowerContext --tag &#34;alpha beta&#34;</Arguments>`,
		`<WorkingDirectory>C:\Users\person\AppData\Local\PowerContext</WorkingDirectory>`,
	} {
		if !strings.Contains(decoded, required) {
			t.Fatalf("task XML does not contain %q:\n%s", required, decoded)
		}
	}
	if strings.Contains(strings.ToLower(decoded), "system") || strings.Contains(strings.ToLower(decoded), "serviceaccount") {
		t.Fatalf("task XML must not use a service principal:\n%s", decoded)
	}

	parsed, err := personalsvc.ParseTaskSchedulerXML(document)
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.Matches(spec) {
		t.Fatalf("parsed TaskSchedulerSpec does not match the original")
	}
}

func TestTaskSchedulerSpecRejectsShellAction(t *testing.T) {
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    "1.2.3",
		Binary:            `C:\Windows\System32\cmd.exe`,
		Endpoint:          "http://127.0.0.1:8123",
		DataDir:           `C:\Users\person\AppData\Local\PowerContext`,
	})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := personalsvc.NewRegistration(definition)
	if err != nil {
		t.Fatal(err)
	}

	_, specificationErr := personalsvc.NewTaskSchedulerSpec(registration, []string{"/c", "echo should not run"}, definition.DataDir(), true)
	assertTaskSchedulerError(t, specificationErr, personalsvc.TaskSchedulerInvalid)
	assertTaskSchedulerErrorRedacted(t, specificationErr)
}

func TestTaskSchedulerSpecControlsLoginTrigger(t *testing.T) {
	for _, startOnLogin := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[startOnLogin], func(t *testing.T) {
			spec := taskSchedulerSpec(t, startOnLogin)
			document, err := spec.XML()
			if err != nil {
				t.Fatal(err)
			}
			gotTrigger := strings.Contains(decodeUTF16LE(t, document), "<LogonTrigger>")
			if gotTrigger != startOnLogin {
				t.Fatalf("LogonTrigger present = %t, want %t", gotTrigger, startOnLogin)
			}
			parsed, err := personalsvc.ParseTaskSchedulerXML(document)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.StartOnLogin() != startOnLogin {
				t.Fatalf("StartOnLogin() = %t, want %t", parsed.StartOnLogin(), startOnLogin)
			}
		})
	}
}

func TestTaskSchedulerParserFailsClosedForForeignOrMutatedTask(t *testing.T) {
	spec := taskSchedulerSpec(t, true)
	document, err := spec.XML()
	if err != nil {
		t.Fatal(err)
	}
	canonical := decodeUTF16LE(t, document)

	for _, test := range []struct {
		name   string
		mutate func(string) string
		want   personalsvc.TaskSchedulerErrorKind
	}{
		{
			name: "foreign ownership marker",
			mutate: func(document string) string {
				return strings.Replace(document, "powercontext.personal-server", "foreign.personal-server", 1)
			},
			want: personalsvc.TaskSchedulerForeign,
		},
		{
			name: "extra action",
			mutate: func(document string) string {
				return strings.Replace(document, "</Actions>", "<Exec><Command>C:\\Windows\\System32\\cmd.exe</Command><Arguments>/c echo foreign</Arguments><WorkingDirectory>C:\\</WorkingDirectory></Exec></Actions>", 1)
			},
			want: personalsvc.TaskSchedulerInvalid,
		},
		{
			name: "extra trigger",
			mutate: func(document string) string {
				return strings.Replace(document, "</Triggers>", "<TimeTrigger><StartBoundary>2026-01-01T00:00:00</StartBoundary><Enabled>true</Enabled></TimeTrigger></Triggers>", 1)
			},
			want: personalsvc.TaskSchedulerInvalid,
		},
		{
			name: "principal mismatch",
			mutate: func(document string) string {
				return strings.Replace(document, "InteractiveToken", "Password", 1)
			},
			want: personalsvc.TaskSchedulerInvalid,
		},
		{
			name: "extra principal",
			mutate: func(document string) string {
				return strings.Replace(document, "</Principals>", "<Principal id=\"foreign\"><UserId>{{POWERCONTEXT_CURRENT_INTERACTIVE_USER}}</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>", 1)
			},
			want: personalsvc.TaskSchedulerInvalid,
		},
		{
			name: "multiple instances mismatch",
			mutate: func(document string) string {
				return strings.Replace(document, "IgnoreNew", "Parallel", 1)
			},
			want: personalsvc.TaskSchedulerInvalid,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, parseErr := personalsvc.ParseTaskSchedulerXML(encodeUTF16LE(test.mutate(canonical)))
			assertTaskSchedulerError(t, parseErr, test.want)
			assertTaskSchedulerErrorRedacted(t, parseErr)
		})
	}
}

func TestTaskSchedulerUsesFixedSchtasksCommands(t *testing.T) {
	executor := &recordingTaskSchedulerExecutor{}
	scheduler, err := personalsvc.NewTaskScheduler(executor)
	if err != nil {
		t.Fatal(err)
	}

	createErr := scheduler.Create(t.Context(), `C:\Users\person\AppData\Local\Temp\powercontext-task.xml`)
	if createErr != nil {
		t.Fatal(createErr)
	}
	state, err := scheduler.Query(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if state != personalsvc.TaskSchedulerPresent {
		t.Fatalf("Query() = %q, want %q", state, personalsvc.TaskSchedulerPresent)
	}
	if err := scheduler.Delete(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Run(t.Context()); err != nil {
		t.Fatal(err)
	}

	want := []taskSchedulerCall{
		{program: "schtasks.exe", arguments: []string{"/Create", "/TN", `\PowerContext Personal Server`, "/XML", `C:\Users\person\AppData\Local\Temp\powercontext-task.xml`, "/F", "/HRESULT"}},
		{program: "schtasks.exe", arguments: []string{"/Query", "/TN", `\PowerContext Personal Server`, "/XML", "/HRESULT"}},
		{program: "schtasks.exe", arguments: []string{"/Delete", "/TN", `\PowerContext Personal Server`, "/F", "/HRESULT"}},
		{program: "schtasks.exe", arguments: []string{"/Run", "/TN", `\PowerContext Personal Server`, "/HRESULT"}},
	}
	if !slices.EqualFunc(executor.calls, want, func(got, want taskSchedulerCall) bool {
		return got.program == want.program && slices.Equal(got.arguments, want.arguments)
	}) {
		t.Fatalf("schtasks calls = %#v, want %#v", executor.calls, want)
	}
}

func TestTaskSchedulerQueryUsesExitCodesNotLocalizedOutput(t *testing.T) {
	for _, test := range []struct {
		name      string
		exitCode  uint32
		wantState personalsvc.TaskSchedulerState
		wantError bool
	}{
		{name: "present", exitCode: 0, wantState: personalsvc.TaskSchedulerPresent},
		{name: "absent", exitCode: 0x80070002, wantState: personalsvc.TaskSchedulerAbsent},
		{name: "unexpected", exitCode: 5, wantState: personalsvc.TaskSchedulerUnknown, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingTaskSchedulerExecutor{results: []personalsvc.TaskSchedulerResult{{
				ExitCode: test.exitCode,
				Output:   []byte("Aufgabe nicht gefunden: 机密的端点值"),
			}}}
			scheduler, err := personalsvc.NewTaskScheduler(executor)
			if err != nil {
				t.Fatal(err)
			}

			state, queryErr := scheduler.Query(t.Context())
			if state != test.wantState {
				t.Fatalf("Query() state = %q, want %q", state, test.wantState)
			}
			if (queryErr != nil) != test.wantError {
				t.Fatalf("Query() error = %v, want error %t", queryErr, test.wantError)
			}
			if queryErr != nil {
				assertTaskSchedulerError(t, queryErr, personalsvc.TaskSchedulerExecution)
				assertTaskSchedulerErrorRedacted(t, queryErr)
			}
		})
	}
}

func TestTaskSchedulerExecutionErrorDoesNotRevealCommandInputs(t *testing.T) {
	executor := &recordingTaskSchedulerExecutor{err: errors.New("C:\\Users\\person\\AppData\\Local\\Temp\\powercontext-task.xml http://127.0.0.1:8123")}
	scheduler, err := personalsvc.NewTaskScheduler(executor)
	if err != nil {
		t.Fatal(err)
	}

	createErr := scheduler.Create(t.Context(), `C:\Users\person\AppData\Local\Temp\powercontext-task.xml`)
	assertTaskSchedulerError(t, createErr, personalsvc.TaskSchedulerExecution)
	assertTaskSchedulerErrorRedacted(t, createErr)
}

func taskSchedulerSpec(t *testing.T, startOnLogin bool) personalsvc.TaskSchedulerSpec {
	t.Helper()
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    "1.2.3",
		Binary:            `C:\Program Files\PowerContext\powercontext.exe`,
		Endpoint:          "http://127.0.0.1:8123",
		DataDir:           `C:\Users\person\AppData\Local\PowerContext`,
	})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := personalsvc.NewRegistration(definition)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := personalsvc.NewTaskSchedulerSpec(
		registration,
		[]string{"serve", "--endpoint", definition.Endpoint(), "--data-dir", definition.DataDir(), "--tag", `alpha beta`},
		definition.DataDir(),
		startOnLogin,
	)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func assertTaskSchedulerError(t *testing.T, err error, want personalsvc.TaskSchedulerErrorKind) {
	t.Helper()
	value, found := errors.AsType[*personalsvc.TaskSchedulerError](err)
	if !found {
		t.Fatalf("error type = %T %v, want *TaskSchedulerError", err, err)
	}
	if value.Kind() != want {
		t.Fatalf("TaskSchedulerError.Kind() = %q, want %q", value.Kind(), want)
	}
}

func assertTaskSchedulerErrorRedacted(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected a redacted error")
	}
	for _, forbidden := range []string{"127.0.0.1", "8123", "Program Files", "Users\\person", "机密"} {
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked protected value %q: %v", forbidden, err)
		}
	}
}

type recordingTaskSchedulerExecutor struct {
	calls   []taskSchedulerCall
	results []personalsvc.TaskSchedulerResult
	err     error
}

type taskSchedulerCall struct {
	program   string
	arguments []string
}

func (e *recordingTaskSchedulerExecutor) Execute(
	_ context.Context, program string, arguments []string,
) (personalsvc.TaskSchedulerResult, error) {
	e.calls = append(e.calls, taskSchedulerCall{program: program, arguments: slices.Clone(arguments)})
	if e.err != nil {
		return personalsvc.TaskSchedulerResult{}, e.err
	}
	if len(e.results) == 0 {
		return personalsvc.TaskSchedulerResult{}, nil
	}
	result := e.results[0]
	e.results = e.results[1:]
	return result, nil
}

func decodeUTF16LE(t *testing.T, document []byte) string {
	t.Helper()
	if len(document) < 2 || len(document)%2 != 0 || !bytes.HasPrefix(document, []byte{0xff, 0xfe}) {
		t.Fatalf("invalid UTF-16LE document %x", document[:min(len(document), 8)])
	}
	codeUnits := make([]uint16, 0, (len(document)-2)/2)
	for index := 2; index < len(document); index += 2 {
		codeUnits = append(codeUnits, uint16(document[index])|uint16(document[index+1])<<8)
	}
	return string(utf16.Decode(codeUnits))
}

func encodeUTF16LE(document string) []byte {
	codeUnits := utf16.Encode([]rune(document))
	encoded := make([]byte, 2, 2+len(codeUnits)*2)
	encoded[0] = 0xff
	encoded[1] = 0xfe
	for _, unit := range codeUnits {
		encoded = append(encoded, byte(unit), byte(unit>>8))
	}
	return encoded
}
