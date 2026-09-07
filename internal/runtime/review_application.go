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
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/source"
)

type ReviewServiceFactory func(string) (*review.Service, error)

type ReviewApplication struct {
	runtime  *Runtime
	services ReviewServiceFactory
}

func NewReviewApplication(runtime *Runtime, services ReviewServiceFactory) (*ReviewApplication, error) {
	if runtime == nil || services == nil {
		return nil, errors.New("runtime: Review application dependencies must not be nil")
	}
	return &ReviewApplication{runtime: runtime, services: services}, nil
}

func (a *ReviewApplication) ProposeExperience(
	ctx context.Context,
	scopeID string,
	proposal experience.Content,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (review.Snapshot, error) {
	var result review.Snapshot
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.ProposeExperience(ctx, proposal, sources, artifacts, target, reason)
		return err
	})
	return result, err
}

func (a *ReviewApplication) ProposeSkill(
	ctx context.Context,
	scopeID string,
	proposal skill.Content,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (review.Snapshot, error) {
	var result review.Snapshot
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.ProposeSkill(ctx, proposal, sources, artifacts, target, reason)
		return err
	})
	return result, err
}

// ProposePackageSkill serializes a v2 package-backed managed Skill proposal
// through the resolved scope's review service.
func (a *ReviewApplication) ProposePackageSkill(
	ctx context.Context,
	scopeID string,
	proposal skill.PackageContent,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (review.Snapshot, error) {
	var result review.Snapshot
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.ProposePackageSkill(ctx, proposal, sources, artifacts, target, reason)
		return err
	})
	return result, err
}

func (a *ReviewApplication) GetCandidate(ctx context.Context, scopeID, candidateID string) (review.Snapshot, error) {
	var result review.Snapshot
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.Get(ctx, candidateID)
		return err
	})
	return result, err
}

func (a *ReviewApplication) ListCandidates(
	ctx context.Context,
	scopeID string,
	status review.Status,
	family, cursor *string,
	limit int,
) (review.Page, error) {
	var result review.Page
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.List(ctx, status, family, cursor, limit)
		return err
	})
	return result.Clone(), err
}

func (a *ReviewApplication) Approve(
	ctx context.Context,
	scopeID, candidateID string,
	expectedVersion int64,
) (review.Snapshot, error) {
	return a.mutate(ctx, scopeID, func(ctx context.Context, service *review.Service) (review.Snapshot, error) {
		return service.Approve(ctx, candidateID, expectedVersion)
	})
}

func (a *ReviewApplication) Reject(
	ctx context.Context,
	scopeID, candidateID string,
	expectedVersion int64,
	reason string,
) (review.Snapshot, error) {
	return a.mutate(ctx, scopeID, func(ctx context.Context, service *review.Service) (review.Snapshot, error) {
		return service.Reject(ctx, candidateID, expectedVersion, reason)
	})
}

func (a *ReviewApplication) Revise(
	ctx context.Context,
	scopeID, candidateID string,
	expectedVersion int64,
	proposal any,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (review.Snapshot, error) {
	return a.mutate(ctx, scopeID, func(ctx context.Context, service *review.Service) (review.Snapshot, error) {
		return service.Revise(
			ctx, candidateID, expectedVersion, proposal, sources, artifacts, target, reason,
		)
	})
}

func (a *ReviewApplication) GetExperience(
	ctx context.Context,
	scopeID string,
	ref artifact.Ref,
) (experience.Experience, error) {
	var result experience.Experience
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.GetExperience(ctx, ref)
		return err
	})
	return result, err
}

func (a *ReviewApplication) GetSkill(
	ctx context.Context,
	scopeID string,
	ref artifact.Ref,
) (skill.Skill, error) {
	var result skill.Skill
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.GetSkill(ctx, ref)
		return err
	})
	return result, err
}

// GetPackageSkill reads one v2 package-backed managed Skill revision.
func (a *ReviewApplication) GetPackageSkill(
	ctx context.Context,
	scopeID string,
	ref artifact.Ref,
) (skill.PackageSkill, error) {
	var result skill.PackageSkill
	err := a.runtime.ScopedRead(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = service.GetPackageSkill(ctx, ref)
		return err
	})
	return result, err
}

func (a *ReviewApplication) mutate(
	ctx context.Context,
	scopeID string,
	operation func(context.Context, *review.Service) (review.Snapshot, error),
) (review.Snapshot, error) {
	var result review.Snapshot
	err := a.runtime.ScopedWrite(ctx, scopeID, func(ctx context.Context, scope string) error {
		service, err := a.service(scope)
		if err != nil {
			return err
		}
		result, err = operation(ctx, service)
		return err
	})
	return result, err
}

func (a *ReviewApplication) service(scope string) (*review.Service, error) {
	service, err := a.services(scope)
	if err != nil {
		return nil, err
	}
	if service == nil {
		return nil, &StateError{Code: "review"}
	}
	return service, nil
}
