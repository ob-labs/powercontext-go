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

	"github.com/spf13/cobra"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
	pcclient "github.com/ob-labs/powercontext-go/client"
)

func newExternalSkillCommand(state *commandState) *cobra.Command {
	command := &cobra.Command{Use: "external-skill", Short: "Inspect and import explicitly configured local Skills."}
	command.AddCommand(
		newExternalSkillScanCommand(state), newExternalSkillListCommand(state),
		newExternalSkillResolveCommand(state), newExternalSkillImportCommand(state),
	)
	return command
}

func newExternalSkillScanCommand(state *commandState) *cobra.Command {
	var scopeID string
	command := &cobra.Command{
		Use: "scan", Short: "Refresh explicitly configured local external Skill roots.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			request := &v1.ScanExternalSkillsRequest{ScopeID: scopeID}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.ScanExternalSkills(ctx, request)
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope receiving the local Registry projection.")
	_ = command.MarkFlagRequired("scope-id")
	return command
}

func newExternalSkillListCommand(state *commandState) *cobra.Command {
	var scopeID string
	var includeUnavailable bool
	command := &cobra.Command{
		Use: "list", Short: "List external Skills after live host and fingerprint checks.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			request := &v1.ListExternalSkillsRequest{
				ScopeID: scopeID, IncludeUnavailable: v1.NewOptBool(includeUnavailable),
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.ListExternalSkills(ctx, request)
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope containing the local Registry projection.")
	command.Flags().BoolVar(&includeUnavailable, "include-unavailable", false, "Include stale or missing local bindings for audit.")
	_ = command.MarkFlagRequired("scope-id")
	return command
}

func newExternalSkillResolveCommand(state *commandState) *cobra.Command {
	var scopeID, fingerprint string
	command := &cobra.Command{
		Use: "resolve EXTERNAL_SKILL_ID", Short: "Resolve one exact local package version without fallback or installation.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			request := &v1.ResolveExternalSkillRequest{
				ScopeID: scopeID, ExternalSkillID: args[0], Fingerprint: fingerprint,
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.ResolveExternalSkill(ctx, request)
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope containing the local Registry projection.")
	command.Flags().StringVar(&fingerprint, "fingerprint", "", "Exact observed package SHA-256 fingerprint.")
	_ = command.MarkFlagRequired("scope-id")
	_ = command.MarkFlagRequired("fingerprint")
	return command
}

func newExternalSkillImportCommand(state *commandState) *cobra.Command {
	var scopeID, fingerprint, mode, reason string
	command := &cobra.Command{
		Use: "import EXTERNAL_SKILL_ID", Short: "Capture an exact local snapshot and propose a new managed Skill.", Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			modeValue := v1.ExternalSkillImportMode(mode)
			if modeValue != v1.ExternalSkillImportModeImport && modeValue != v1.ExternalSkillImportModeFork {
				return usageError(errors.New("--mode must be import or fork"))
			}
			request := &v1.ImportExternalSkillRequest{
				ScopeID: scopeID, ExternalSkillID: args[0], Fingerprint: fingerprint,
				Mode: modeValue, Reason: optionalString(reason),
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.ImportExternalSkill(ctx, request)
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope receiving the managed Candidate.")
	command.Flags().StringVar(&fingerprint, "fingerprint", "", "Exact observed package SHA-256 fingerprint.")
	command.Flags().StringVar(&mode, "mode", string(v1.ExternalSkillImportModeImport), "Import mode: import or fork.")
	command.Flags().StringVar(&reason, "reason", "", "Why this managed proposal is requested.")
	_ = command.MarkFlagRequired("scope-id")
	_ = command.MarkFlagRequired("fingerprint")
	return command
}
