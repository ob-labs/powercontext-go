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

package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type AgentSkillProvider struct {
	hostID        string
	targets       []AgentSkillTarget
	providerNames []string
}

func NewAgentSkillProvider(hostID string, targets []AgentSkillTarget) (*AgentSkillProvider, error) {
	return newAgentSkillProvider(hostID, targets, []string{string(CodexAgent), string(ClaudeCodeAgent)})
}

func newAgentSkillProvider(
	hostID string,
	targets []AgentSkillTarget,
	providerNames []string,
) (*AgentSkillProvider, error) {
	if err := externalText("host_id", hostID, MaxExternalHostIDLength); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if _, exists := seen[target.targetID]; exists {
			return nil, fmt.Errorf("Agent Skill target IDs must be unique")
		}
		seen[target.targetID] = struct{}{}
	}
	return &AgentSkillProvider{
		hostID:        hostID,
		targets:       slices.Clone(targets),
		providerNames: slices.Clone(providerNames),
	}, nil
}

func (*AgentSkillProvider) Name() string              { return "agent-targets" }
func (*AgentSkillProvider) AgentKind() string         { return "multi" }
func (p *AgentSkillProvider) HostID() string          { return p.hostID }
func (p *AgentSkillProvider) ProviderNames() []string { return slices.Clone(p.providerNames) }

func (p *AgentSkillProvider) Scan(ctx context.Context) (ProviderScan, error) {
	var registrations []Registration
	skipped := 0
	for _, target := range p.targets {
		if err := ctx.Err(); err != nil {
			return ProviderScan{}, err
		}
		entries, err := os.ReadDir(target.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return ProviderScan{}, err
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return ProviderScan{}, err
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			registration, err := p.registration(target, filepath.Join(target.path, entry.Name()))
			if err != nil {
				skipped++
				continue
			}
			registrations = append(registrations, registration)
		}
	}
	return NewProviderScan(registrations, skipped)
}

func (p *AgentSkillProvider) Resolve(ctx context.Context, registration Registration) (Resolution, error) {
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	if !slices.Contains(p.providerNames, registration.provider) ||
		registration.agentKind != registration.provider || registration.hostID != p.hostID {
		return unavailable(registration), nil
	}
	target, ok := p.targetFor(registration)
	if !ok {
		return unavailable(registration), nil
	}
	resolved, err := filepath.EvalSymlinks(registration.locator)
	if err != nil {
		return unavailable(registration), nil
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil || filepath.Dir(resolved) != target.path {
		return unavailable(registration), nil
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return unavailable(registration), nil
	}
	current, err := p.registration(target, resolved)
	if err != nil || current.externalSkillID != registration.externalSkillID || current.fingerprint != registration.fingerprint {
		return unavailable(registration), nil
	}
	return Resolution{Registration: registration, Status: Available, Entrypoint: filepath.Join(resolved, "SKILL.md")}, nil
}

func (p *AgentSkillProvider) targetFor(registration Registration) (AgentSkillTarget, bool) {
	prefix := registration.agentKind + ":" + string(registration.installationScope) + ":"
	if !strings.HasPrefix(registration.externalSkillID, prefix) {
		return AgentSkillTarget{}, false
	}
	remainder := strings.TrimPrefix(registration.externalSkillID, prefix)
	targetID := strings.SplitN(remainder, "/", 2)[0]
	for _, target := range p.targets {
		if target.targetID == targetID && string(target.agentKind) == registration.agentKind &&
			target.installationScope == registration.installationScope {
			return target, true
		}
	}
	return AgentSkillTarget{}, false
}

func (p *AgentSkillProvider) registration(target AgentSkillTarget, packagePath string) (Registration, error) {
	if filepath.Dir(packagePath) != target.path {
		return Registration{}, fmt.Errorf("Agent Skill package must be an immediate child of its configured target")
	}
	name, description, err := skillMetadata(
		filepath.Join(packagePath, "SKILL.md"), filepath.Base(packagePath), target.agentKind,
	)
	if err != nil {
		return Registration{}, err
	}
	fingerprint, err := packageFingerprint(packagePath)
	if err != nil {
		return Registration{}, err
	}
	externalID := fmt.Sprintf(
		"%s:%s:%s/%s", target.agentKind, target.installationScope, target.targetID, filepath.Base(packagePath),
	)
	return NewRegistration(
		externalID, string(target.agentKind), string(target.agentKind), p.hostID, target.installationScope,
		packagePath, fingerprint, name, description,
	)
}

type CodexProvider struct{ provider *AgentSkillProvider }

func NewCodexProvider(hostID string, roots []CodexRoot) (*CodexProvider, error) {
	targets := make([]AgentSkillTarget, len(roots))
	for index, root := range roots {
		targets[index] = root.AgentTarget()
	}
	provider, err := newAgentSkillProvider(hostID, targets, []string{string(CodexAgent)})
	if err != nil {
		return nil, err
	}
	return &CodexProvider{provider: provider}, nil
}

func (*CodexProvider) Name() string              { return "codex" }
func (*CodexProvider) AgentKind() string         { return "codex" }
func (p *CodexProvider) HostID() string          { return p.provider.HostID() }
func (p *CodexProvider) ProviderNames() []string { return []string{string(CodexAgent)} }
func (p *CodexProvider) Scan(ctx context.Context) (ProviderScan, error) {
	return p.provider.Scan(ctx)
}

func (p *CodexProvider) Resolve(ctx context.Context, registration Registration) (Resolution, error) {
	return p.provider.Resolve(ctx, registration)
}
