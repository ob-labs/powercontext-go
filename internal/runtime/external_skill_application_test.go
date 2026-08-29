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

package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/review"
)

func TestRuntimeExposesConfiguredExternalSkillRegistryWithoutModel(t *testing.T) {
	registration := externalApplicationRegistration(t)
	store := &externalApplicationStore{}
	provider := externalApplicationProvider{registration: registration}
	registry, err := skill.NewRegistryService(store, provider)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewExternalSkillApplication(
		New(), func(string) (*skill.RegistryService, error) { return registry, nil }, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scan, err := application.Scan(ctx, "project:example")
	if err != nil || len(scan.Registrations()) != 1 {
		t.Fatalf("scan = %#v, %v", scan, err)
	}
	listed, err := application.List(ctx, "project:example", false)
	if err != nil || len(listed) != 1 || listed[0].Status != skill.Available {
		t.Fatalf("list = %#v, %v", listed, err)
	}
	resolved, err := application.Resolve(
		ctx, "project:example", registration.ExternalSkillID(), registration.Fingerprint(),
	)
	if err != nil || resolved.Status != skill.Available || resolved.Entrypoint == "" {
		t.Fatalf("resolve = %#v, %v", resolved, err)
	}
}

func TestRuntimeReportsUnconfiguredExternalSkillRegistry(t *testing.T) {
	application, err := NewExternalSkillApplication(New(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.Scan(context.Background(), "project:example")
	var unavailable *skill.ExternalRegistryUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("scan error = %v", err)
	}
}

func TestExternalSkillImportRequiresGenerationBeforeSnapshot(t *testing.T) {
	registryCalls := 0
	application, err := NewExternalSkillApplication(
		New(),
		func(string) (*skill.RegistryService, error) {
			registryCalls++
			return nil, errors.New("registry must not be touched")
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.Import(
		context.Background(), "project:example", "external", strings.Repeat("a", 64),
		skill.ImportModeImport, nil,
	)
	var unavailable *review.GenerationCapabilityUnavailableError
	if !errors.As(err, &unavailable) || registryCalls != 0 {
		t.Fatalf("import error = %v, registry calls = %d", err, registryCalls)
	}
}

type externalApplicationProvider struct{ registration skill.Registration }

func (externalApplicationProvider) Name() string            { return "codex" }
func (externalApplicationProvider) AgentKind() string       { return "codex" }
func (externalApplicationProvider) HostID() string          { return "workstation-1" }
func (externalApplicationProvider) ProviderNames() []string { return []string{"codex"} }
func (p externalApplicationProvider) Scan(context.Context) (skill.ProviderScan, error) {
	return skill.NewProviderScan([]skill.Registration{p.registration}, 0)
}

func (p externalApplicationProvider) Resolve(context.Context, skill.Registration) (skill.Resolution, error) {
	return skill.Resolution{
		Registration: p.registration, Status: skill.Available, Entrypoint: "/bounded/SKILL.md",
	}, nil
}

type externalApplicationStore struct{ registrations []skill.Registration }

func (s *externalApplicationStore) Replace(
	_ context.Context, _ []string, _ string, registrations []skill.Registration,
) ([]skill.Registration, error) {
	s.registrations = slices.Clone(registrations)
	return slices.Clone(registrations), nil
}

func (s *externalApplicationStore) Get(_ context.Context, id string) (skill.Registration, error) {
	for _, registration := range s.registrations {
		if registration.ExternalSkillID() == id {
			return registration, nil
		}
	}
	return skill.Registration{}, &skill.ExternalNotFoundError{ExternalSkillID: id}
}

func (s *externalApplicationStore) List(context.Context) ([]skill.Registration, error) {
	return slices.Clone(s.registrations), nil
}

func externalApplicationRegistration(t *testing.T) skill.Registration {
	t.Helper()
	registration, err := skill.NewRegistration(
		"codex:project:repository/friendly-go", "codex", "codex", "workstation-1",
		skill.ProjectScope, "/bounded/friendly-go", strings.Repeat("a", 64),
		"friendly-go", "Use when writing Go.",
	)
	if err != nil {
		t.Fatal(err)
	}
	return registration
}
