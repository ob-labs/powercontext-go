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

package server

import (
	"context"
	"errors"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	pcruntime "github.com/ob-labs/powercontext-go/internal/runtime"
	"github.com/ob-labs/powercontext-go/source"
)

// managedSkillOperations composes the two deliberately narrow Runtime use cases
// behind the canonical managed-Skill sidecar without exposing generic writes.
type managedSkillOperations struct {
	packages *pcruntime.SkillPackageResourceApplication
	usage    *pcruntime.SkillUsageApplication
}

func (o managedSkillOperations) ReadSkillPackage(
	ctx context.Context,
	scopeID string,
	ref artifact.Ref,
) (skill.PackageSnapshot, error) {
	if o.packages == nil {
		return skill.PackageSnapshot{}, errors.New("server: Skill package operations are unavailable")
	}
	return o.packages.ReadSkillPackage(ctx, scopeID, ref)
}

func (o managedSkillOperations) Record(
	ctx context.Context,
	scopeID string,
	capture source.SkillUsageCapture,
) (pcruntime.SourceReceipt, error) {
	if o.usage == nil {
		return pcruntime.SourceReceipt{}, errors.New("server: Skill usage operations are unavailable")
	}
	return o.usage.Record(ctx, scopeID, capture)
}
