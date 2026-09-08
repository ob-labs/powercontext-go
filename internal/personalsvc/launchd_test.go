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
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/personalsvc"
)

func TestLaunchdPlanRendersAndStrictlyInspectsOwnedLaunchAgent(t *testing.T) {
	registration := launchdRegistration(t)
	plan, err := personalsvc.NewLaunchdPlan(501, registration)
	if err != nil {
		t.Fatalf("NewLaunchdPlan() error = %v", err)
	}

	first, err := plan.Render()
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	second, err := plan.Render()
	if err != nil {
		t.Fatalf("second Render() error = %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("Render() did not produce a deterministic plist")
	}
	if !strings.Contains(string(first), "<key>Label</key>\n\t<string>com.oceanbase.powercontext</string>") ||
		!strings.Contains(string(first), "<key>RunAtLoad</key>\n\t<true/>") ||
		!strings.Contains(string(first), "<key>ProcessType</key>\n\t<string>Background</string>") ||
		!strings.Contains(string(first), "<key>ThrottleInterval</key>\n\t<integer>5</integer>") {
		t.Fatalf("Render() did not contain the required LaunchAgent policy: %s", first)
	}
	if !strings.Contains(string(first), "/var/lib/powercontext/logs/server.stdout.log") ||
		!strings.Contains(string(first), "/var/lib/powercontext/logs/server.stderr.log") ||
		!strings.Contains(string(first), "/var/lib/powercontext/logs/launchd-retry.enabled") {
		t.Fatalf("Render() did not keep managed files under the registered data directory: %s", first)
	}
	if !strings.Contains(string(first), "<key>PathState</key>\n\t\t<dict>\n\t\t\t<key>/var/lib/powercontext/logs/launchd-retry.enabled</key>\n\t\t\t<true/>") {
		t.Fatalf("Render() did not make the retry token an enabled PathState: %s", first)
	}
	if strings.Contains(string(first), "sh -c") || !strings.Contains(string(first), "<key>ProgramArguments</key>") {
		t.Fatalf("Render() did not use a shell-free argument array: %s", first)
	}

	inspected, err := plan.Inspect(first)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if inspected != registration {
		t.Fatalf("Inspect() registration = %#v, want %#v", inspected, registration)
	}
}

func TestLaunchdPlanInspectionRejectsEveryOwnedShapeMutation(t *testing.T) {
	registration := launchdRegistration(t)
	plan, err := personalsvc.NewLaunchdPlan(501, registration)
	if err != nil {
		t.Fatalf("NewLaunchdPlan() error = %v", err)
	}
	plist, err := plan.Render()
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	metadata, err := registration.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	mutations := []struct {
		name string
		from string
		to   string
	}{
		{name: "owned artifact path", from: "/var/lib/powercontext/logs/server.stdout.log", to: "/tmp/foreign.log"},
		{name: "program", from: "/opt/powercontext/bin/powercontext", to: "/opt/foreign/bin/powercontext"},
		{name: "arguments", from: "<string>server</string>", to: "<string>foreign</string>"},
		{name: "ownership marker", from: "POWERCONTEXT_SERVICE_OWNED", to: "FOREIGN_SERVICE_OWNED"},
		{name: "canonical metadata", from: metadata, to: "invalid-metadata"},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			mutated := bytes.Replace(plist, []byte(test.from), []byte(test.to), 1)
			if bytes.Equal(mutated, plist) {
				t.Fatalf("test mutation did not change the rendered plist")
			}
			_, inspectErr := plan.Inspect(mutated)
			if inspectErr == nil {
				t.Fatal("Inspect() accepted a mutated owned LaunchAgent")
			}
			if strings.Contains(inspectErr.Error(), "/tmp/foreign.log") || strings.Contains(inspectErr.Error(), "invalid-metadata") {
				t.Fatalf("Inspect() error leaked artifact content: %v", inspectErr)
			}
		})
	}
}

func TestLaunchdPlanFormsExactPerUserNativeArguments(t *testing.T) {
	plan, err := personalsvc.NewLaunchdPlan(501, launchdRegistration(t))
	if err != nil {
		t.Fatalf("NewLaunchdPlan() error = %v", err)
	}

	bootstrap, err := plan.BootstrapArgv("/Users/alex/Library/LaunchAgents/com.oceanbase.powercontext.plist")
	if err != nil {
		t.Fatalf("BootstrapArgv() error = %v", err)
	}
	if want := []string{
		"launchctl", "bootstrap", "gui/501", "/Users/alex/Library/LaunchAgents/com.oceanbase.powercontext.plist",
	}; !reflect.DeepEqual(bootstrap, want) {
		t.Fatalf("BootstrapArgv() = %#v, want %#v", bootstrap, want)
	}
	bootout, err := plan.BootoutArgv()
	if err != nil {
		t.Fatalf("BootoutArgv() error = %v", err)
	}
	if want := []string{"launchctl", "bootout", "gui/501/com.oceanbase.powercontext"}; !reflect.DeepEqual(bootout, want) {
		t.Fatalf("BootoutArgv() = %#v, want %#v", bootout, want)
	}
	printed, err := plan.PrintArgv()
	if err != nil {
		t.Fatalf("PrintArgv() error = %v", err)
	}
	if want := []string{"launchctl", "print", "gui/501/com.oceanbase.powercontext"}; !reflect.DeepEqual(printed, want) {
		t.Fatalf("PrintArgv() = %#v, want %#v", printed, want)
	}

	for _, artifactPath := range []string{
		"relative/super-secret.plist",
		"/Users/alex/Library/LaunchAgents/super-secret\n/com.oceanbase.powercontext.plist",
	} {
		_, pathErr := plan.BootstrapArgv(artifactPath)
		if pathErr == nil {
			t.Fatalf("BootstrapArgv() accepted invalid artifact path %q", artifactPath)
		}
		if strings.Contains(pathErr.Error(), "super-secret") || strings.Contains(pathErr.Error(), "relative") {
			t.Fatalf("BootstrapArgv() error leaked a path: %v", pathErr)
		}
	}
}

func TestLaunchdPlanInvalidPlanRejectsExecutableArguments(t *testing.T) {
	for _, test := range []struct {
		name string
		argv func(personalsvc.LaunchdPlan) ([]string, error)
	}{
		{name: "bootout", argv: personalsvc.LaunchdPlan.BootoutArgv},
		{name: "print", argv: personalsvc.LaunchdPlan.PrintArgv},
	} {
		t.Run(test.name, func(t *testing.T) {
			argv, err := test.argv(personalsvc.LaunchdPlan{})
			if err == nil {
				t.Fatal("invalid LaunchdPlan returned executable arguments")
			}
			if argv != nil {
				t.Fatalf("invalid LaunchdPlan argv = %#v, want nil", argv)
			}
			if _, ok := errors.AsType[*personalsvc.LaunchdPlanError](err); !ok {
				t.Fatalf("invalid LaunchdPlan error type = %T, want *LaunchdPlanError", err)
			}
		})
	}
}

func TestLaunchdRetryBudgetStopsAtThreeFailuresWithoutAutomaticallyResetting(t *testing.T) {
	now := time.Date(2026, time.September, 7, 8, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	state := personalsvc.NewLaunchdRetryStateForFreshInstall()

	for failure := 1; failure <= 3; failure++ {
		var err error
		state, err = state.ObserveLaunchdExit(clock, personalsvc.LaunchdExitFailure)
		if err != nil {
			t.Fatalf("failure %d ObserveLaunchdExit() error = %v", failure, err)
		}
		if got := state.FailureCount(); got != failure {
			t.Fatalf("failure %d FailureCount() = %d, want %d", failure, got, failure)
		}
		if got := state.TokenPresent(); got != (failure < 3) {
			t.Fatalf("failure %d TokenPresent() = %t, want %t", failure, got, failure < 3)
		}
		now = now.Add(10 * time.Second)
	}

	afterExhaustion, err := state.ObserveLaunchdExit(clock, personalsvc.LaunchdExitFailure)
	if err != nil {
		t.Fatalf("exhausted ObserveLaunchdExit() error = %v", err)
	}
	if afterExhaustion.TokenPresent() || afterExhaustion.FailureCount() != 3 {
		t.Fatalf("exhausted state = token=%t failures=%d, want token=false failures=3", afterExhaustion.TokenPresent(), afterExhaustion.FailureCount())
	}

	now = now.Add(2 * time.Minute)
	expiredWindow, err := afterExhaustion.ObserveLaunchdExit(clock, personalsvc.LaunchdExitFailure)
	if err != nil {
		t.Fatalf("expired-window ObserveLaunchdExit() error = %v", err)
	}
	if expiredWindow.TokenPresent() {
		t.Fatal("an exhausted retry token was re-enabled without a fresh installation")
	}
	if fresh := personalsvc.NewLaunchdRetryStateForFreshInstall(); !fresh.TokenPresent() || fresh.FailureCount() != 0 {
		t.Fatalf("fresh installation retry state = token=%t failures=%d, want token=true failures=0", fresh.TokenPresent(), fresh.FailureCount())
	}
}

func TestLaunchdRetryBudgetTreatsAlreadyLiveCleanExitAsNonFailure(t *testing.T) {
	now := time.Date(2026, time.September, 7, 8, 0, 0, 0, time.UTC)
	state, err := personalsvc.NewLaunchdRetryStateForFreshInstall().ObserveLaunchdExit(
		func() time.Time { return now }, personalsvc.LaunchdExitAlreadyLive,
	)
	if err != nil {
		t.Fatalf("ObserveLaunchdExit() error = %v", err)
	}
	if state.FailureCount() != 0 {
		t.Fatalf("FailureCount() = %d, want 0 after an already-live clean exit", state.FailureCount())
	}
	if state.TokenPresent() {
		t.Fatal("already-live clean exit left the retry token enabled")
	}
}

func launchdRegistration(t *testing.T) personalsvc.Registration {
	t.Helper()
	definition, err := personalsvc.NewDefinition(personalsvc.DefinitionInput{
		Ownership:         personalsvc.OwnershipMarker,
		DefinitionVersion: personalsvc.DefinitionVersion,
		PackageVersion:    "0.2.0",
		Binary:            "/opt/powercontext/bin/powercontext",
		Endpoint:          "http://127.0.0.1:7614",
		DataDir:           "/var/lib/powercontext",
		EnvFile:           "/etc/powercontext/server.env",
	})
	if err != nil {
		t.Fatalf("NewDefinition() error = %v", err)
	}
	registration, err := personalsvc.NewRegistration(definition)
	if err != nil {
		t.Fatalf("NewRegistration() error = %v", err)
	}
	return registration
}
