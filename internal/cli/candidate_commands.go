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
	"io"
	"os"
	"unicode/utf8"

	"github.com/spf13/cobra"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	pcclient "github.com/ob-labs/powercontext-go/client"
)

func newCandidateCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{Use: "candidate", Short: "Review generated Artifact Candidates."}
	command.AddCommand(
		newCandidateListCommand(state), newCandidateShowCommand(state),
		newCandidateApproveCommand(state), newCandidateRejectCommand(state),
		newCandidateReviseCommand(state),
	)
	return command
}

func newCandidateListCommand(state *commandState) *cobra.Command {
	var scopeID, status, family, cursor string
	var limit int
	command := &cobra.Command{
		Use: "list", Short: "List current Candidate heads; pending is the default Inbox view.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			statusValue := v1.CandidateStatus(status)
			if statusValue != v1.CandidateStatusPending && statusValue != v1.CandidateStatusApproved && statusValue != v1.CandidateStatusRejected {
				return usageError(errors.New("--status must be pending, approved, or rejected"))
			}
			if limit < 1 || limit > 100 {
				return usageError(errors.New("--limit must be between 1 and 100"))
			}
			request := &v1.ListArtifactCandidatesRequest{
				ScopeID: scopeID, Status: v1.NewOptCandidateStatus(statusValue),
				Limit: v1.NewOptInt(limit),
			}
			if family != "" {
				familyValue := v1.CandidateFamily(family)
				if familyValue != v1.CandidateFamilyExperience && familyValue != v1.CandidateFamilySkill {
					return usageError(errors.New("--family must be experience or skill"))
				}
				request.Family = v1.NewOptNilCandidateFamily(familyValue)
			}
			if cursor != "" {
				request.Cursor = v1.NewOptNilString(cursor)
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.ListArtifactCandidates(ctx, request)
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope containing the Review Inbox.")
	command.Flags().StringVar(&status, "status", string(v1.CandidateStatusPending), "Candidate lifecycle state.")
	command.Flags().StringVar(&family, "family", "", "Optional Artifact family: experience or skill.")
	command.Flags().StringVar(&cursor, "cursor", "", "Opaque cursor from the previous page.")
	command.Flags().IntVar(&limit, "limit", 50, "Maximum Candidate heads to return.")
	_ = command.MarkFlagRequired("scope-id")
	return command
}

func newCandidateShowCommand(state *commandState) *cobra.Command {
	var scopeID string
	command := &cobra.Command{
		Use: "show CANDIDATE_ID", Short: "Show the current exact Candidate version and evidence.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			request := &v1.GetArtifactCandidateRequest{ScopeID: scopeID, CandidateID: args[0]}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.GetArtifactCandidate(ctx, request)
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope containing the Candidate.")
	_ = command.MarkFlagRequired("scope-id")
	return command
}

func newCandidateApproveCommand(state *commandState) *cobra.Command {
	var scopeID string
	var expectedVersion int
	command := &cobra.Command{
		Use: "approve CANDIDATE_ID", Short: "Approve one exact pending Candidate version.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expectedVersion < 1 {
				return usageError(errors.New("--expected-version must be at least 1"))
			}
			request := &v1.ApproveArtifactCandidateRequest{
				ScopeID: scopeID, CandidateID: args[0], ExpectedVersion: expectedVersion,
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.ApproveArtifactCandidate(ctx, request)
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope containing the Candidate.")
	command.Flags().IntVar(&expectedVersion, "expected-version", 0, "Exact reviewed Candidate version.")
	_ = command.MarkFlagRequired("scope-id")
	_ = command.MarkFlagRequired("expected-version")
	return command
}

func newCandidateRejectCommand(state *commandState) *cobra.Command {
	var scopeID, reason string
	var expectedVersion int
	command := &cobra.Command{
		Use: "reject CANDIDATE_ID", Short: "Reject one exact pending Candidate version.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if expectedVersion < 1 {
				return usageError(errors.New("--expected-version must be at least 1"))
			}
			request := &v1.RejectArtifactCandidateRequest{
				ScopeID: scopeID, CandidateID: args[0], ExpectedVersion: expectedVersion, Reason: reason,
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.RejectArtifactCandidate(ctx, request)
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope containing the Candidate.")
	command.Flags().IntVar(&expectedVersion, "expected-version", 0, "Exact reviewed Candidate version.")
	command.Flags().StringVar(&reason, "reason", "", "Why the proposal was rejected.")
	_ = command.MarkFlagRequired("scope-id")
	_ = command.MarkFlagRequired("expected-version")
	_ = command.MarkFlagRequired("reason")
	return command
}

type candidateRevisionFlags struct {
	scopeID, reason, target string
	expectedVersion         int
	sourceRefs              []string
	artifactRefs            []string
}

func newCandidateReviseCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{Use: "revise", Short: "Append a complete replacement proposal."}
	command.AddCommand(newReviseExperienceCommand(state), newReviseSkillCommand(state))
	return command
}

func newReviseExperienceCommand(state *commandState) *cobra.Command {
	var flags candidateRevisionFlags
	var situation, action, outcome, lesson string
	command := &cobra.Command{
		Use: "experience CANDIDATE_ID", Short: "Append a complete Experience replacement proposal.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if flags.expectedVersion < 1 {
				return usageError(errors.New("--expected-version must be at least 1"))
			}
			sources, artifacts, target, err := evidenceReferences(flags.sourceRefs, flags.artifactRefs, flags.target)
			if err != nil {
				return usageError(err)
			}
			if err := requireTargetFamily(target, "experience"); err != nil {
				return usageError(err)
			}
			request := &v1.ReviseArtifactCandidateRequest{
				ScopeID: flags.scopeID, CandidateID: args[0], ExpectedVersion: flags.expectedVersion,
				Proposal: v1.NewExperienceProposalReviseArtifactCandidateRequestProposal(v1.ExperienceProposal{
					Situation: situation, Action: action, Outcome: outcome, Lesson: lesson,
				}),
				SourceRefs: sources, ArtifactRefs: artifacts, Target: target, Reason: optionalString(flags.reason),
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.ReviseArtifactCandidate(ctx, request)
			})
		},
	}
	bindCandidateRevisionFlags(command, &flags)
	command.Flags().StringVar(&situation, "situation", "", "Situation addressed by the replacement proposal.")
	command.Flags().StringVar(&action, "action", "", "Action taken in the replacement proposal.")
	command.Flags().StringVar(&outcome, "outcome", "", "Observed outcome in the replacement proposal.")
	command.Flags().StringVar(&lesson, "lesson", "", "Reusable lesson in the replacement proposal.")
	for _, name := range []string{"situation", "action", "outcome", "lesson"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}

func newReviseSkillCommand(state *commandState) *cobra.Command {
	var flags candidateRevisionFlags
	var name, description, instructions, instructionsFile string
	var validation []string
	command := &cobra.Command{
		Use: "skill CANDIDATE_ID", Short: "Append a complete managed Skill replacement proposal.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if flags.expectedVersion < 1 {
				return usageError(errors.New("--expected-version must be at least 1"))
			}
			body, err := resolveInstructions(instructions, instructionsFile)
			if err != nil {
				return usageError(err)
			}
			sources, artifacts, target, err := evidenceReferences(flags.sourceRefs, flags.artifactRefs, flags.target)
			if err != nil {
				return usageError(err)
			}
			if err := requireTargetFamily(target, "skill"); err != nil {
				return usageError(err)
			}
			checks := make([]v1.SkillValidationItem, len(validation))
			for index, value := range validation {
				checks[index] = v1.SkillValidationItem(value)
			}
			request := &v1.ReviseArtifactCandidateRequest{
				ScopeID: flags.scopeID, CandidateID: args[0], ExpectedVersion: flags.expectedVersion,
				Proposal: v1.NewSkillProposalReviseArtifactCandidateRequestProposal(v1.SkillProposal{
					Name: name, Description: description, Instructions: body, Validation: checks,
				}),
				SourceRefs: sources, ArtifactRefs: artifacts, Target: target, Reason: optionalString(flags.reason),
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.ReviseArtifactCandidate(ctx, request)
			})
		},
	}
	bindCandidateRevisionFlags(command, &flags)
	command.Flags().StringVar(&name, "name", "", "Managed Skill name.")
	command.Flags().StringVar(&description, "description", "", "Managed Skill discovery description.")
	command.Flags().StringVar(&instructions, "instructions", "", "Managed Skill instructions.")
	command.Flags().StringVar(&instructionsFile, "instructions-file", "", "UTF-8 file containing managed Skill instructions.")
	command.Flags().StringArrayVar(&validation, "validation", nil, "Validation check; repeat for additional checks.")
	_ = command.MarkFlagRequired("name")
	_ = command.MarkFlagRequired("description")
	return command
}

func bindCandidateRevisionFlags(command *cobra.Command, flags *candidateRevisionFlags) {
	command.Flags().StringVar(&flags.scopeID, "scope-id", "", "Application scope containing the Candidate.")
	command.Flags().IntVar(&flags.expectedVersion, "expected-version", 0, "Exact reviewed Candidate version.")
	command.Flags().StringArrayVar(&flags.sourceRefs, "source-ref", nil, "Exact Source as TYPE/ID; repeat for more evidence.")
	command.Flags().StringArrayVar(&flags.artifactRefs, "artifact-ref", nil, "Exact Artifact as FAMILY/ID@REVISION; repeat for more evidence.")
	command.Flags().StringVar(&flags.target, "target", "", "Exact replacement target as FAMILY/ID@REVISION.")
	command.Flags().StringVar(&flags.reason, "reason", "", "Why this replacement proposal is requested.")
	_ = command.MarkFlagRequired("scope-id")
	_ = command.MarkFlagRequired("expected-version")
}

func resolveInstructions(inline, path string) (string, error) {
	if (inline == "") == (path == "") {
		return "", errors.New("exactly one of --instructions and --instructions-file is required")
	}
	if path == "" {
		if !utf8.ValidString(inline) {
			return "", errors.New("--instructions must be valid UTF-8")
		}
		return inline, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot read --instructions-file")
	}
	defer func() { _ = file.Close() }()
	const maximum = 1 << 20
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || len(content) > maximum || !utf8.Valid(content) {
		return "", fmt.Errorf("--instructions-file must contain at most %d valid UTF-8 bytes", maximum)
	}
	return string(content), nil
}
