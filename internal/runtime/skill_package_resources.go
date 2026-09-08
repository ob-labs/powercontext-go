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

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
)

// SkillPackageResourceReader reads a verified package at an exact Artifact revision.
type SkillPackageResourceReader interface {
	ReadSkillPackage(context.Context, string, artifact.Ref) (skill.PackageSnapshot, error)
}

type SkillPackageResourceApplication struct {
	runtime *Runtime
	reader  SkillPackageResourceReader
}

func NewSkillPackageResourceApplication(runtime *Runtime, reader SkillPackageResourceReader) (*SkillPackageResourceApplication, error) {
	if runtime == nil || reader == nil {
		return nil, errors.New("runtime: Skill package resource dependencies must not be nil")
	}
	return &SkillPackageResourceApplication{runtime: runtime, reader: reader}, nil
}

func (a *SkillPackageResourceApplication) ReadSkillPackage(ctx context.Context, scopeID string, ref artifact.Ref) (skill.PackageSnapshot, error) {
	var result skill.PackageSnapshot
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		if refErr := ref.Validate(); refErr != nil {
			return refErr
		}
		value, readErr := a.reader.ReadSkillPackage(ctx, scope, ref)
		if cancelErr := ctx.Err(); cancelErr != nil {
			return cancelErr
		}
		if readErr != nil {
			return readErr
		}
		result = value
		return nil
	})
	if err != nil {
		return skill.PackageSnapshot{}, err
	}
	return result, nil
}
