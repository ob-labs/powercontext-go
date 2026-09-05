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

import "fmt"

type AgentSkillTarget struct {
	targetID            string
	agentKind           AgentKind
	installationScope   InstallationScope
	path                string
	allowManagedPublish bool
}

func NewAgentSkillTarget(
	targetID string,
	agentKind AgentKind,
	scope InstallationScope,
	path string,
	allowManagedPublish bool,
) (AgentSkillTarget, error) {
	if len(targetID) < 1 || len(targetID) > 64 || !rootIDPattern.MatchString(targetID) {
		return AgentSkillTarget{}, fmt.Errorf("Agent Skill target ID is invalid")
	}
	if !validAgentKind(string(agentKind)) {
		return AgentSkillTarget{}, &UnsupportedAgentKindError{}
	}
	if scope != UserScope && scope != ProjectScope && scope != PluginScope {
		return AgentSkillTarget{}, fmt.Errorf("invalid external Skill installation scope %q", scope)
	}
	resolved, err := resolveLoose(path)
	if err != nil {
		return AgentSkillTarget{}, err
	}
	return AgentSkillTarget{
		targetID: targetID, agentKind: agentKind, installationScope: scope,
		path: resolved, allowManagedPublish: allowManagedPublish,
	}, nil
}

func (t AgentSkillTarget) ID() string                           { return t.targetID }
func (t AgentSkillTarget) AgentKind() AgentKind                 { return t.agentKind }
func (t AgentSkillTarget) InstallationScope() InstallationScope { return t.installationScope }
func (t AgentSkillTarget) Path() string                         { return t.path }
func (t AgentSkillTarget) AllowManagedPublish() bool            { return t.allowManagedPublish }

type CodexRoot struct{ target AgentSkillTarget }

func NewCodexRoot(rootID string, scope InstallationScope, path string) (CodexRoot, error) {
	return NewCodexRootWithPublish(rootID, scope, path, false)
}

func NewCodexRootWithPublish(
	rootID string,
	scope InstallationScope,
	path string,
	allowManagedPublish bool,
) (CodexRoot, error) {
	target, err := NewAgentSkillTarget(rootID, CodexAgent, scope, path, allowManagedPublish)
	if err != nil {
		return CodexRoot{}, err
	}
	return CodexRoot{target: target}, nil
}

func (r CodexRoot) ID() string                           { return r.target.ID() }
func (r CodexRoot) InstallationScope() InstallationScope { return r.target.InstallationScope() }
func (r CodexRoot) Path() string                         { return r.target.Path() }
func (r CodexRoot) AllowManagedPublish() bool            { return r.target.AllowManagedPublish() }
func (r CodexRoot) AgentTarget() AgentSkillTarget        { return r.target }
