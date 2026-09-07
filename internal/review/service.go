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

package review

import (
	"context"
	"errors"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/source"
)

type IDFactory func(kind string) (string, error)

// Backend owns the transaction-coupled Candidate/Artifact implementation.
// Its methods are deliberately use-case shaped so no SQL concern leaks into
// the domain service.
type Backend interface {
	Propose(
		context.Context,
		string,
		string,
		any,
		[]source.Ref,
		[]artifact.Ref,
		*artifact.Ref,
		*string,
	) (Snapshot, error)
	Get(context.Context, string) (Snapshot, error)
	List(context.Context, Status, *string, *string, int) (Page, error)
	Revise(
		context.Context,
		string,
		int64,
		any,
		[]source.Ref,
		[]artifact.Ref,
		*artifact.Ref,
		*string,
	) (Snapshot, error)
	Reject(context.Context, string, int64, string) (Snapshot, error)
	Approve(context.Context, string, int64, IDFactory) (Snapshot, error)
	GetArtifact(context.Context, artifact.Ref) (artifact.Snapshot, error)
}

type Service struct {
	backend   Backend
	idFactory IDFactory
}

func NewService(backend Backend, idFactory IDFactory) (*Service, error) {
	if backend == nil || idFactory == nil {
		return nil, errors.New("review: backend and ID factory must not be nil")
	}
	return &Service{backend: backend, idFactory: idFactory}, nil
}

func (s *Service) ProposeExperience(
	ctx context.Context,
	proposal experience.Content,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (Candidate[experience.Content], error) {
	value, err := s.propose(ctx, experience.Family, proposal, sources, artifacts, target, reason)
	if err != nil {
		return Candidate[experience.Content]{}, err
	}
	return experienceCandidate(value)
}

func (s *Service) ProposeSkill(
	ctx context.Context,
	proposal skill.Content,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (Candidate[skill.Content], error) {
	value, err := s.propose(ctx, skill.Family, proposal, sources, artifacts, target, reason)
	if err != nil {
		return Candidate[skill.Content]{}, err
	}
	return skillCandidate(value)
}

// ProposePackageSkill submits a v2 package-backed managed Skill for review.
func (s *Service) ProposePackageSkill(
	ctx context.Context,
	proposal skill.PackageContent,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (Candidate[skill.PackageContent], error) {
	value, err := s.propose(ctx, skill.Family, proposal, sources, artifacts, target, reason)
	if err != nil {
		return Candidate[skill.PackageContent]{}, err
	}
	return packageSkillCandidate(value)
}

func (s *Service) propose(
	ctx context.Context,
	family string,
	proposal any,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (Snapshot, error) {
	canonicalSources := uniqueSources(sources)
	canonicalArtifacts := uniqueArtifacts(artifacts)
	if err := validateReason(reason); err != nil {
		return nil, err
	}
	id, err := s.idFactory("candidate")
	if err != nil {
		return nil, err
	}
	return s.backend.Propose(
		ctx, id, family, proposal, canonicalSources, canonicalArtifacts, target, reason,
	)
}

func (s *Service) Get(ctx context.Context, candidateID string) (Snapshot, error) {
	return s.backend.Get(ctx, candidateID)
}

func (s *Service) List(
	ctx context.Context,
	status Status,
	family, cursor *string,
	limit int,
) (Page, error) {
	return s.backend.List(ctx, status, family, cursor, limit)
}

func (s *Service) Revise(
	ctx context.Context,
	candidateID string,
	expectedVersion int64,
	proposal any,
	sources []source.Ref,
	artifacts []artifact.Ref,
	target *artifact.Ref,
	reason *string,
) (Snapshot, error) {
	if err := validateReason(reason); err != nil {
		return nil, err
	}
	return s.backend.Revise(
		ctx,
		candidateID,
		expectedVersion,
		proposal,
		uniqueSources(sources),
		uniqueArtifacts(artifacts),
		target,
		reason,
	)
}

func (s *Service) Reject(
	ctx context.Context,
	candidateID string,
	expectedVersion int64,
	reason string,
) (Snapshot, error) {
	if err := validateReason(&reason); err != nil {
		return nil, err
	}
	return s.backend.Reject(ctx, candidateID, expectedVersion, reason)
}

func (s *Service) Approve(
	ctx context.Context,
	candidateID string,
	expectedVersion int64,
) (Snapshot, error) {
	return s.backend.Approve(ctx, candidateID, expectedVersion, s.idFactory)
}

func (s *Service) GetExperience(ctx context.Context, ref artifact.Ref) (experience.Experience, error) {
	if ref.Family() != experience.Family {
		return experience.Experience{}, &artifact.NotFoundError{Ref: ref}
	}
	value, err := s.backend.GetArtifact(ctx, ref)
	if err != nil {
		return experience.Experience{}, err
	}
	result, ok := value.(experience.Experience)
	if !ok {
		return experience.Experience{}, &artifact.NotFoundError{Ref: ref}
	}
	return result, nil
}

func (s *Service) GetSkill(ctx context.Context, ref artifact.Ref) (skill.Skill, error) {
	if ref.Family() != skill.Family {
		return skill.Skill{}, &artifact.NotFoundError{Ref: ref}
	}
	value, err := s.backend.GetArtifact(ctx, ref)
	if err != nil {
		return skill.Skill{}, err
	}
	result, ok := value.(skill.Skill)
	if !ok {
		return skill.Skill{}, &artifact.NotFoundError{Ref: ref}
	}
	return result, nil
}

// GetPackageSkill returns one package-backed managed Skill revision.
func (s *Service) GetPackageSkill(ctx context.Context, ref artifact.Ref) (skill.PackageSkill, error) {
	if ref.Family() != skill.Family {
		return skill.PackageSkill{}, &artifact.NotFoundError{Ref: ref}
	}
	value, err := s.backend.GetArtifact(ctx, ref)
	if err != nil {
		return skill.PackageSkill{}, err
	}
	result, ok := value.(skill.PackageSkill)
	if !ok {
		return skill.PackageSkill{}, &artifact.NotFoundError{Ref: ref}
	}
	return result, nil
}

func experienceCandidate(value Snapshot) (Candidate[experience.Content], error) {
	proposal, ok := value.ProposalValue().(experience.Content)
	if !ok || value.Family() != experience.Family {
		return Candidate[experience.Content]{}, &InvalidCandidateError{Field: "family", Detail: value.Family()}
	}
	return NewCandidate(
		value.ID(), value.Version(), value.Family(), value.Status(), proposal,
		value.Sources(), value.Artifacts(), value.Target(), value.Reason(),
		value.ResultArtifact(), value.DecisionReason(),
	)
}

func skillCandidate(value Snapshot) (Candidate[skill.Content], error) {
	proposal, ok := value.ProposalValue().(skill.Content)
	if !ok || value.Family() != skill.Family {
		return Candidate[skill.Content]{}, &InvalidCandidateError{Field: "family", Detail: value.Family()}
	}
	return NewCandidate(
		value.ID(), value.Version(), value.Family(), value.Status(), proposal,
		value.Sources(), value.Artifacts(), value.Target(), value.Reason(),
		value.ResultArtifact(), value.DecisionReason(),
	)
}

func packageSkillCandidate(value Snapshot) (Candidate[skill.PackageContent], error) {
	proposal, ok := value.ProposalValue().(skill.PackageContent)
	if !ok || value.Family() != skill.Family {
		return Candidate[skill.PackageContent]{}, &InvalidCandidateError{Field: "family", Detail: value.Family()}
	}
	return NewCandidate(
		value.ID(), value.Version(), value.Family(), value.Status(), proposal,
		value.Sources(), value.Artifacts(), value.Target(), value.Reason(),
		value.ResultArtifact(), value.DecisionReason(),
	)
}

func uniqueSources(values []source.Ref) []source.Ref {
	seen := make(map[source.Ref]struct{}, len(values))
	result := make([]source.Ref, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueArtifacts(values []artifact.Ref) []artifact.Ref {
	seen := make(map[artifact.Ref]struct{}, len(values))
	result := make([]artifact.Ref, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func validateReason(value *string) error {
	if value == nil {
		return nil
	}
	return candidateText("reason", *value, MaxReasonLength)
}
