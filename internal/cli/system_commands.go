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

func newCapabilitiesCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "capabilities", Short: "Show behavior enabled by the remote Server runtime.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.GetCapabilities(ctx)
			})
		},
	}
}

func newLiveCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "live", Short: "Check whether the remote API process is alive.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.GetLiveness(ctx)
			})
		},
	}
}

func newReadyCommand(state *commandState) *cobra.Command {
	return &cobra.Command{
		Use: "ready", Short: "Check whether remote Server bindings are ready.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.GetReadiness(ctx)
			})
		},
	}
}

func newStatsCommand(state *commandState) *cobra.Command {
	var scopeID string
	var period string
	command := &cobra.Command{
		Use: "stats", Short: "Show current inventory and bounded usage for one scope.", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			value := v1.StatsPeriod(period)
			if value != v1.StatsPeriodToday && value != v1.StatsPeriod7d && value != v1.StatsPeriod30d {
				return usageError(errors.New("--period must be today, 7d, or 30d"))
			}
			return state.execute(command, func(ctx context.Context, client *pcclient.Client) (any, error) {
				return client.GetStats(ctx, v1.GetStatsParams{
					ScopeID: scopeID, Period: v1.NewOptStatsPeriod(value),
				})
			})
		},
	}
	command.Flags().StringVar(&scopeID, "scope-id", "", "Application scope to inspect.")
	command.Flags().StringVar(&period, "period", string(v1.StatsPeriod30d), "Bounded UTC statistics period: today, 7d, or 30d.")
	_ = command.MarkFlagRequired("scope-id")
	return command
}
