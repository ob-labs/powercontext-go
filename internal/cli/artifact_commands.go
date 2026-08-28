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

package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"github.com/spf13/cobra"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	"github.com/ob-labs/powercontext-go/artifact"
	artifactskill "github.com/ob-labs/powercontext-go/artifact/skill"
	pcclient "github.com/ob-labs/powercontext-go/client"
)

func newExperienceCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{Use: "experience", Short: "Generate managed Experience Candidates."}
	command.AddCommand(newGenerateExperienceCommand(state))
	return command
}

func newGenerateExperienceCommand(state *commandState) *cobra.Command {
	var scopeID, target, reason string
	var sourceRefs, artifactRefs []string
	command := &cobra.Command{
		Use: "generate", Short: "Generate at most one pending Experience Candidate.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			sources, artifacts, targetRef, err := evidenceReferences(sourceRefs, artifactRefs, target)
			if err != nil {
				return usageError(err)
			}
			if err := requireTargetFamily(targetRef, "experience"); err != nil {
				return usageError(err)
			}
			request := &v1.GenerateExperienceRequest{
				ScopeID: scopeID, SourceRefs: sources, ArtifactRefs: artifacts,
				Target: targetRef, Reason: optionalString(reason),
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.GenerateExperience(ctx, request)
			})
		},
	}
	bindEvidenceFlags(command, &scopeID, &sourceRefs, &artifactRefs, &target, &reason)
	return command
}

func newSkillCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{Use: "skill", Short: "Generate, inspect, and export managed Skills."}
	command.AddCommand(newGenerateSkillCommand(state), newShowSkillCommand(state), newExportSkillCommand(state))
	return command
}

func newGenerateSkillCommand(state *commandState) *cobra.Command {
	var scopeID, origin, target, reason string
	var sourceRefs, artifactRefs []string
	command := &cobra.Command{
		Use: "generate", Short: "Generate at most one pending managed Skill Candidate.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			sources, artifacts, targetRef, err := evidenceReferences(sourceRefs, artifactRefs, target)
			if err != nil {
				return usageError(err)
			}
			if err := validateSkillOrigin(v1.SkillGenerationOrigin(origin), sources, artifacts, targetRef); err != nil {
				return usageError(err)
			}
			request := &v1.GenerateSkillRequest{
				ScopeID: scopeID, Origin: v1.SkillGenerationOrigin(origin),
				SourceRefs: sources, ArtifactRefs: artifacts, Target: targetRef, Reason: optionalString(reason),
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.GenerateSkill(ctx, request)
			})
		},
	}
	bindEvidenceFlags(command, &scopeID, &sourceRefs, &artifactRefs, &target, &reason)
	command.Flags().StringVar(&origin, "origin", "", "Provenance shape: experience, source, or usage.")
	_ = command.MarkFlagRequired("origin")
	return command
}

func newShowSkillCommand(state *commandState) *cobra.Command {
	var scopeID string
	var revision int
	command := &cobra.Command{
		Use: "show ARTIFACT_ID", Short: "Read one exact approved managed Skill Revision.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if revision < 1 {
				return usageError(errors.New("--revision must be at least 1"))
			}
			request := &v1.GetSkillRequest{ScopeID: scopeID, Artifact: v1.ArtifactReference{
				Family: "skill", ArtifactID: args[0], Revision: revision,
			}}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.GetSkill(ctx, request)
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope containing the managed Skill.")
	command.Flags().IntVar(&revision, "revision", 0, "Exact managed Skill Revision.")
	_ = command.MarkFlagRequired("scope-id")
	_ = command.MarkFlagRequired("revision")
	return command
}

func newExportSkillCommand(state *commandState) *cobra.Command {
	var scopeID, target, destination string
	var revision int
	command := &cobra.Command{
		Use: "export ARTIFACT_ID", Short: "Export one exact approved Revision for an Agent integration target.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if target != string(artifactskill.CodexAgent) && target != string(artifactskill.ClaudeCodeAgent) {
				return usageError(errors.New("--target must be codex or claude_code"))
			}
			if revision < 1 {
				return usageError(errors.New("--revision must be at least 1"))
			}
			request := &v1.GetSkillRequest{ScopeID: scopeID, Artifact: v1.ArtifactReference{
				Family: "skill", ArtifactID: args[0], Revision: revision,
			}}
			body, err := state.call(command.Context(), func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.GetSkill(ctx, request)
			})
			if err != nil {
				return err
			}
			value, ok := body.(v1.SkillArtifact)
			if !ok {
				return fmt.Errorf("unexpected Skill response %T", body)
			}
			exported, err := projectAgentSkill(value, destination, artifactskill.AgentKind(target))
			if err != nil {
				return usageError(fmt.Errorf("cannot export managed Skill for %s: %w", target, err))
			}
			_, err = fmt.Fprintf(state.stdout, "Exported %s@%d for %s to %s\n", value.Artifact.ArtifactID, value.Artifact.Revision, target, exported)
			return err
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope containing the managed Skill.")
	command.Flags().StringVar(&target, "target", "", "Agent integration target: codex or claude_code.")
	command.Flags().StringVar(&destination, "destination", "", "New target Skill directory; existing paths are never replaced.")
	command.Flags().IntVar(&revision, "revision", 0, "Exact managed Skill Revision.")
	for _, name := range []string{"scope-id", "target", "destination", "revision"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func bindEvidenceFlags(
	command *cobra.Command,
	scopeID *string,
	sourceRefs, artifactRefs *[]string,
	target, reason *string,
) {
	command.Flags().StringVar(scopeID, "scope-id", "", "Application scope receiving the generated Candidate.")
	command.Flags().StringArrayVar(sourceRefs, "source-ref", nil, "Exact Source as TYPE/ID; repeat for more evidence.")
	command.Flags().StringArrayVar(artifactRefs, "artifact-ref", nil, "Exact Artifact as FAMILY/ID@REVISION; repeat for more evidence.")
	command.Flags().StringVar(target, "target", "", "Exact replacement/evolution target as FAMILY/ID@REVISION.")
	command.Flags().StringVar(reason, "reason", "", "Why this generation is requested.")
	_ = command.MarkFlagRequired("scope-id")
}

func validateSkillOrigin(
	origin v1.SkillGenerationOrigin,
	sources []v1.SourceReference,
	artifacts []v1.ArtifactReference,
	target v1.OptNilArtifactReference,
) error {
	targetValue, hasTarget := target.Get()
	switch origin {
	case v1.SkillGenerationOriginExperience:
		if hasTarget || len(artifacts) == 0 {
			return errors.New("experience origin requires Experience refs and no target")
		}
		for _, ref := range artifacts {
			if ref.Family != "experience" {
				return errors.New("experience origin requires Experience refs and no target")
			}
		}
	case v1.SkillGenerationOriginSource:
		if hasTarget || len(sources) == 0 || len(artifacts) != 0 {
			return errors.New("source origin requires only Source refs")
		}
	case v1.SkillGenerationOriginUsage:
		if !hasTarget || targetValue.Family != "skill" || len(sources) == 0 {
			return errors.New("usage origin requires a target Skill and Source refs")
		}
	default:
		return errors.New("--origin must be experience, source, or usage")
	}
	return nil
}

const codexProjectionSchema = artifactskill.ProjectionSchema

func projectCodexSkill(value v1.SkillArtifact, destination string) (string, error) {
	return projectAgentSkill(value, destination, artifactskill.CodexAgent)
}

func projectAgentSkill(
	value v1.SkillArtifact,
	destination string,
	agentKind artifactskill.AgentKind,
) (string, error) {
	if value.Artifact.Family != "skill" {
		return "", errors.New("artifact must identify a managed Skill")
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return "", errors.New("destination is invalid")
	}
	absolute, err = resolvePath(absolute)
	if err != nil {
		return "", errors.New("destination is invalid")
	}
	validation := make([]string, len(value.Content.Validation))
	for index, item := range value.Content.Validation {
		validation[index] = string(item)
	}
	content, err := artifactskill.NewContent(
		value.Content.Name,
		value.Content.Description,
		value.Content.Instructions,
		validation,
	)
	if err != nil {
		return "", fmt.Errorf("managed Skill content is invalid: %w", err)
	}
	ref, err := artifact.NewRef(value.Artifact.Family, value.Artifact.ArtifactID, int64(value.Artifact.Revision))
	if err != nil {
		return "", fmt.Errorf("managed Skill Artifact reference is invalid: %w", err)
	}
	target, err := artifactskill.NewAgentSkillTarget(
		"cli-export",
		agentKind,
		artifactskill.ProjectScope,
		filepath.Dir(absolute),
		false,
	)
	if err != nil {
		return "", fmt.Errorf("destination is invalid: %w", err)
	}
	if filepath.Clean(filepath.Join(target.Path(), content.Name())) != filepath.Clean(absolute) {
		return "", fmt.Errorf("%s Skill directory name must match the managed Skill name", agentKind)
	}
	exported, err := artifactskill.ProjectSkill(ref, content, target)
	if errors.Is(err, fs.ErrExist) {
		return "", errors.New("destination already exists")
	}
	return exported, err
}
