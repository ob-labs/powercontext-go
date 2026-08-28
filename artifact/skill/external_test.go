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

package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/skill"
)

func TestCodexProviderDiscoversAndExactlyResolvesLocalPackage(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	packagePath := writeSkill(t, root, "friendly-python")
	provider := codexProvider(t, root)

	scan, err := provider.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if scan.Skipped() != 0 || len(scan.Registrations()) != 1 {
		t.Fatalf("scan = %d registrations/%d skipped", len(scan.Registrations()), scan.Skipped())
	}
	registration := scan.Registrations()[0]
	if registration.ExternalSkillID() != "codex:project:repository/friendly-python" || registration.Locator() != packagePath || registration.Name() != "friendly-python" || len(registration.Fingerprint()) != 64 {
		t.Fatalf("unexpected registration: %#v", registration)
	}
	resolution, err := provider.Resolve(context.Background(), registration)
	if err != nil || resolution.Status != skill.Available || resolution.Entrypoint != filepath.Join(packagePath, "SKILL.md") {
		t.Fatalf("resolution = %#v, %v", resolution, err)
	}
}

func TestExactResolutionRejectsContentDriftUntilRescan(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	packagePath := writeSkill(t, root, "friendly-python")
	provider := codexProvider(t, root)
	scan, _ := provider.Scan(context.Background())
	registration := scan.Registrations()[0]
	if err := os.Mkdir(filepath.Join(packagePath, "references"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "references", "review.md"), []byte("New package content.\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	stale, err := provider.Resolve(context.Background(), registration)
	if err != nil || stale.Status != skill.Unavailable || stale.Entrypoint != "" {
		t.Fatalf("stale resolution = %#v, %v", stale, err)
	}
	refreshed, err := provider.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current := refreshed.Registrations()[0]
	if current.ExternalSkillID() != registration.ExternalSkillID() || current.Fingerprint() == registration.Fingerprint() {
		t.Fatalf("rescan did not preserve identity and change fingerprint")
	}
	resolved, err := provider.Resolve(context.Background(), current)
	if err != nil || resolved.Status != skill.Available {
		t.Fatalf("refreshed resolution = %#v, %v", resolved, err)
	}
}

func TestResolutionIsBoundToConfiguredHostAndRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	writeSkill(t, root, "friendly-python")
	provider := codexProvider(t, root)
	scan, _ := provider.Scan(context.Background())
	registration := scan.Registrations()[0]
	wrongHost := copyRegistration(t, registration, "workstation-2", registration.Locator())
	wrongLocator := copyRegistration(t, registration, registration.HostID(), filepath.Dir(root))
	for name, candidate := range map[string]skill.Registration{"host": wrongHost, "root": wrongLocator} {
		t.Run(name, func(t *testing.T) {
			resolution, err := provider.Resolve(context.Background(), candidate)
			if err != nil || resolution.Status != skill.Unavailable {
				t.Fatalf("resolution = %#v, %v", resolution, err)
			}
		})
	}
}

func TestScanSkipsInvalidOrSymlinkedPackages(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	packagePath := writeSkill(t, root, "friendly-python")
	invalid := filepath.Join(root, "missing-frontmatter")
	if err := os.Mkdir(invalid, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(invalid, "SKILL.md"), []byte("# Missing frontmatter\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(invalid, filepath.Join(packagePath, "linked")); err != nil {
		t.Fatal(err)
	}
	scan, err := codexProvider(t, root).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Registrations()) != 0 || scan.Skipped() != 2 {
		t.Fatalf("scan = %d registrations/%d skipped", len(scan.Registrations()), scan.Skipped())
	}
}

func TestScanIgnoresSymlinkedPackageDirectories(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := writeSkill(t, t.TempDir(), "outside-root")
	if err := os.Symlink(outside, filepath.Join(root, "linked-package")); err != nil {
		t.Fatal(err)
	}

	scan, err := codexProvider(t, root).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Registrations()) != 0 || scan.Skipped() != 0 {
		t.Fatalf("scan followed a symlinked package directory: %d registrations/%d skipped", len(scan.Registrations()), scan.Skipped())
	}
}

func TestCodexProviderRequiresUniqueStableRootIDs(t *testing.T) {
	path := t.TempDir()
	first, err := skill.NewCodexRoot("repository", skill.ProjectScope, path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := skill.NewCodexRoot("repository", skill.UserScope, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := skill.NewCodexProvider("workstation-1", []skill.CodexRoot{first, second}); err == nil {
		t.Fatal("duplicate root IDs were accepted")
	}
}

func TestAgentProviderDiscoversCodexAndClaudeCodeTargets(t *testing.T) {
	codexRoot := filepath.Join(t.TempDir(), ".agents", "skills")
	claudeRoot := filepath.Join(t.TempDir(), ".claude", "skills")
	writeSkill(t, codexRoot, "codex-review")
	claudePackage := filepath.Join(claudeRoot, "claude-review")
	if err := os.MkdirAll(claudePackage, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(claudePackage, "SKILL.md"),
		[]byte("---\ndescription: Review a change with Claude Code.\n---\n\nReview the change.\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	codexTarget, err := skill.NewAgentSkillTarget(
		"codex-project", skill.CodexAgent, skill.ProjectScope, codexRoot, false,
	)
	if err != nil {
		t.Fatal(err)
	}
	claudeTarget, err := skill.NewAgentSkillTarget(
		"claude-project", skill.ClaudeCodeAgent, skill.ProjectScope, claudeRoot, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := skill.NewAgentSkillProvider(
		"workstation-1", []skill.AgentSkillTarget{codexTarget, claudeTarget},
	)
	if err != nil {
		t.Fatal(err)
	}

	scan, err := provider.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registrations := scan.Registrations()
	if scan.Skipped() != 0 || len(registrations) != 2 {
		t.Fatalf("scan = %d registrations/%d skipped", len(registrations), scan.Skipped())
	}
	wantIDs := []string{
		"codex:project:codex-project/codex-review",
		"claude_code:project:claude-project/claude-review",
	}
	gotIDs := []string{registrations[0].ExternalSkillID(), registrations[1].ExternalSkillID()}
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("registration IDs = %v, want %v", gotIDs, wantIDs)
	}
	claude := registrations[1]
	if claude.Name() != "claude-review" || claude.Provider() != "claude_code" ||
		!claudeTarget.AllowManagedPublish() {
		t.Fatalf("Claude Code registration/target = %#v / %#v", claude, claudeTarget)
	}
	resolved, err := provider.Resolve(context.Background(), claude)
	wantPackage, pathErr := filepath.EvalSymlinks(claudePackage)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if err != nil || resolved.Status != skill.Available ||
		resolved.Entrypoint != filepath.Join(wantPackage, "SKILL.md") {
		t.Fatalf("Claude Code resolution = %#v, %v", resolved, err)
	}
}

func TestSkillContentRequiresCompletePortableInstructions(t *testing.T) {
	content, err := skill.NewContent(
		"powercontext-openapi-change",
		"Use when changing PowerContext's public HTTP contract.",
		"Regenerate checked-in clients, inspect the diff, and run contract tests.",
		[]string{"make api-generate-check passes", "make contract-test passes"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(content.Validation(), []string{"make api-generate-check passes", "make contract-test passes"}) {
		t.Fatalf("validation = %v", content.Validation())
	}
	invalid := []struct {
		name, description, instructions string
		validation                      []string
	}{
		{" ", "description", "instructions", []string{"check"}},
		{"name", " trailing ", "instructions", []string{"check"}},
		{"name", "description", "\n\t", []string{"check"}},
		{"name", "description", "instructions", []string{""}},
		{"\u001c", "description", "instructions", []string{"check"}},
		{"name", "description", "\u001f", []string{"check"}},
	}
	for index, value := range invalid {
		if _, err := skill.NewContent(value.name, value.description, value.instructions, value.validation); err == nil {
			t.Fatalf("invalid case %d was accepted", index)
		}
	}
}

func TestRegistryRefreshesProjectionAndChecksLiveAvailability(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".agents", "skills")
	packagePath := writeSkill(t, root, "friendly-python")
	provider := codexProvider(t, root)
	store := &registrationStore{values: make(map[string]skill.Registration)}
	service, err := skill.NewRegistryService(store, provider)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	scan, err := service.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	registration := scan.Registrations()[0]
	listed, err := service.List(ctx, false)
	if err != nil || len(listed) != 1 {
		t.Fatalf("initial list = %d, %v", len(listed), err)
	}
	manifest := filepath.Join(packagePath, "SKILL.md")
	file, err := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("Changed.\n"); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close changed Skill manifest: %v", closeErr)
		}
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	listed, err = service.List(ctx, false)
	if err != nil || len(listed) != 0 {
		t.Fatalf("available-only list after drift = %d, %v", len(listed), err)
	}
	audit, err := service.List(ctx, true)
	if err != nil || len(audit) != 1 || audit[0].Status != skill.Unavailable {
		t.Fatalf("audit list = %#v, %v", audit, err)
	}
	exact, err := service.Resolve(ctx, registration.ExternalSkillID(), registration.Fingerprint())
	if err != nil || exact.Status != skill.Unavailable {
		t.Fatalf("stale exact resolution = %#v, %v", exact, err)
	}
	refreshed, err := service.Scan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	current := refreshed.Registrations()[0]
	if current.Fingerprint() == registration.Fingerprint() {
		t.Fatal("registry rescan retained a stale fingerprint")
	}
	exact, err = service.Resolve(ctx, current.ExternalSkillID(), current.Fingerprint())
	if err != nil || exact.Status != skill.Available {
		t.Fatalf("current exact resolution = %#v, %v", exact, err)
	}
}

type registrationStore struct{ values map[string]skill.Registration }

func (s *registrationStore) Replace(_ context.Context, _ []string, _ string, values []skill.Registration) ([]skill.Registration, error) {
	s.values = make(map[string]skill.Registration, len(values))
	for _, value := range values {
		s.values[value.ExternalSkillID()] = value
	}
	return slices.Clone(values), nil
}

func (s *registrationStore) Get(_ context.Context, id string) (skill.Registration, error) {
	value, ok := s.values[id]
	if !ok {
		return skill.Registration{}, &skill.ExternalNotFoundError{ExternalSkillID: id}
	}
	return value, nil
}

func (s *registrationStore) List(context.Context) ([]skill.Registration, error) {
	result := make([]skill.Registration, 0, len(s.values))
	for _, value := range s.values {
		result = append(result, value)
	}
	slices.SortFunc(result, func(left, right skill.Registration) int {
		if left.ExternalSkillID() < right.ExternalSkillID() {
			return -1
		}
		if left.ExternalSkillID() > right.ExternalSkillID() {
			return 1
		}
		return 0
	})
	return result, nil
}

func writeSkill(t *testing.T, root, name string) string {
	t.Helper()
	packagePath := filepath.Join(root, name)
	if err := os.MkdirAll(packagePath, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := "---\nname: " + name + "\ndescription: Use when writing or refactoring Python code.\n---\n\n# Instructions\n\nPrefer explicit, readable boundaries.\n"
	if err := os.WriteFile(filepath.Join(packagePath, "SKILL.md"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func codexProvider(t *testing.T, root string) *skill.CodexProvider {
	t.Helper()
	configured, err := skill.NewCodexRoot("repository", skill.ProjectScope, root)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := skill.NewCodexProvider("workstation-1", []skill.CodexRoot{configured})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func copyRegistration(t *testing.T, value skill.Registration, hostID, locator string) skill.Registration {
	t.Helper()
	result, err := skill.NewRegistration(
		value.ExternalSkillID(), value.Provider(), value.AgentKind(), hostID, value.InstallationScope(), locator,
		value.Fingerprint(), value.Name(), value.Description(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
