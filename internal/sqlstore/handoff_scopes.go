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

package sqlstore

import (
	"context"
	"errors"

	"github.com/ob-labs/powercontext-go/artifact/handoff"
)

// HandoffScopeIDs returns only scopes that own a committed Handoff head. It is
// the authority behind scope-centric Handoff Report discovery.
func HandoffScopeIDs(ctx context.Context, database *Database) ([]string, error) {
	var result []string
	err := database.Transaction(ctx, func(tx DBTX) (returnErr error) {
		rows, err := tx.QueryContext(ctx,
			"SELECT DISTINCT scope_id FROM pc_artifact_heads WHERE family = ? ORDER BY scope_id", handoff.Family,
		)
		if err != nil {
			return err
		}
		defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
		for rows.Next() {
			var scope string
			if err := rows.Scan(&scope); err != nil {
				return err
			}
			result = append(result, scope)
		}
		return rows.Err()
	})
	return result, err
}
