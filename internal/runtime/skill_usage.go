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

	"github.com/ob-labs/powercontext-go/source"
)

// SkillUsageBackend owns durable bounded Skill usage evidence.
type SkillUsageBackend interface {
	Record(context.Context, string, source.SkillUsageCapture) (source.Ref, int64, error)
}

// SkillUsageApplication admits one bounded exact managed-Skill usage observation.
type SkillUsageApplication struct {
	runtime *Runtime
	backend SkillUsageBackend
}

func NewSkillUsageApplication(runtime *Runtime, backend SkillUsageBackend) (*SkillUsageApplication, error) {
	if runtime == nil || backend == nil {
		return nil, errors.New("runtime: Skill usage dependencies must not be nil")
	}
	return &SkillUsageApplication{runtime: runtime, backend: backend}, nil
}

func (a *SkillUsageApplication) Record(
	ctx context.Context,
	scopeID string,
	capture source.SkillUsageCapture,
) (SourceReceipt, error) {
	if err := capture.Validate(); err != nil {
		return SourceReceipt{}, err
	}
	var receipt SourceReceipt
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		ref, sequence, operationErr := a.backend.Record(ctx, scope, capture)
		if operationErr == nil {
			receipt = SourceReceipt{Ref: ref, Sequence: sequence}
		}
		return operationErr
	})
	return receipt, err
}
