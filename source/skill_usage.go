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

package source

import (
	"context"
	"encoding/hex"
	"strings"
)

const (
	// SkillUsageType identifies bounded immutable managed-Skill usage evidence.
	SkillUsageType = "skill-usage"

	maxSkillUsageTargetIDLength = 128
	skillUsageDescription       = "Bounded Skill usage observed by the owning Agent integration."
)

// ObservedInvocation records whether the owning integration observed a Skill invocation.
type ObservedInvocation string

const (
	ObservedInvocationTrue    ObservedInvocation = "true"
	ObservedInvocationFalse   ObservedInvocation = "false"
	ObservedInvocationUnknown ObservedInvocation = "unknown"
)

// ObservedValidation records the bounded validation result reported by the integration.
type ObservedValidation string

const (
	ObservedValidationPassed  ObservedValidation = "passed"
	ObservedValidationFailed  ObservedValidation = "failed"
	ObservedValidationUnknown ObservedValidation = "unknown"
)

// ObservedOutcome records the bounded task outcome reported by the integration.
type ObservedOutcome string

const (
	ObservedOutcomeSuccess ObservedOutcome = "success"
	ObservedOutcomeFailure ObservedOutcome = "failure"
	ObservedOutcomeUnknown ObservedOutcome = "unknown"
)

// InvalidSkillUsageError reports an invalid bounded usage value without
// including the protected observation, package, or task contents.
type InvalidSkillUsageError struct{ Field string }

func (e *InvalidSkillUsageError) Error() string {
	if e == nil || e.Field == "" {
		return "invalid Skill usage"
	}
	return "invalid Skill usage " + e.Field
}

// SkillUsageTaskSourceNotFoundError reports an absent optional task Source
// without disclosing its identity.
type SkillUsageTaskSourceNotFoundError struct{}

func (*SkillUsageTaskSourceNotFoundError) Error() string {
	return "Skill usage task Source was not found"
}

// SkillUsageArtifactReference identifies the exact managed Skill revision
// observed by a usage Source without making source depend on artifact.
type SkillUsageArtifactReference struct {
	family     string
	artifactID string
	revision   int64
}

// NewSkillUsageArtifactReference validates one exact Skill Artifact revision.
func NewSkillUsageArtifactReference(family, artifactID string, revision int64) (SkillUsageArtifactReference, error) {
	if err := validateReferencePart("skill_family", family, maxSkillUsageTargetIDLength); err != nil {
		return SkillUsageArtifactReference{}, &InvalidSkillUsageError{Field: "skill_ref"}
	}
	if err := validateReferencePart("skill_artifact_id", artifactID, maxSkillUsageTargetIDLength); err != nil || revision < 1 {
		return SkillUsageArtifactReference{}, &InvalidSkillUsageError{Field: "skill_ref"}
	}
	return SkillUsageArtifactReference{family: family, artifactID: artifactID, revision: revision}, nil
}

func (r SkillUsageArtifactReference) Family() string  { return r.family }
func (r SkillUsageArtifactReference) ID() string      { return r.artifactID }
func (r SkillUsageArtifactReference) Revision() int64 { return r.revision }

func (r SkillUsageArtifactReference) Validate() error {
	_, err := NewSkillUsageArtifactReference(r.family, r.artifactID, r.revision)
	return err
}

// SkillUsageCapture is the caller-stable bounded input for one immutable
// managed-Skill usage Source. It deliberately cannot carry a prompt, command,
// environment value, package bytes, or Agent-specific payload.
type SkillUsageCapture struct {
	observationID          string
	skillRef               SkillUsageArtifactReference
	packageDigest          string
	targetID               string
	selected               bool
	invoked                ObservedInvocation
	validation             ObservedValidation
	outcome                ObservedOutcome
	taskSource             *Ref
	environmentFingerprint *string
}

// NewSkillUsageCapture validates and freezes one bounded Skill usage observation.
func NewSkillUsageCapture(
	observationID string,
	skillRef SkillUsageArtifactReference,
	packageDigest, targetID string,
	selected bool,
	invoked ObservedInvocation,
	validation ObservedValidation,
	outcome ObservedOutcome,
	taskSource *Ref,
	environmentFingerprint *string,
) (SkillUsageCapture, error) {
	value := SkillUsageCapture{
		observationID: observationID, skillRef: skillRef, packageDigest: packageDigest, targetID: targetID,
		selected: selected, invoked: invoked, validation: validation, outcome: outcome,
	}
	if taskSource != nil {
		copied := *taskSource
		value.taskSource = &copied
	}
	if environmentFingerprint != nil {
		copied := *environmentFingerprint
		value.environmentFingerprint = &copied
	}
	if err := value.Validate(); err != nil {
		return SkillUsageCapture{}, err
	}
	return value, nil
}

// Validate reports whether c is a bounded usage capture.
func (c SkillUsageCapture) Validate() error {
	if err := validateReferencePart("observation_id", c.observationID, MaxIDLength); err != nil {
		return &InvalidSkillUsageError{Field: "observation_id"}
	}
	if err := c.skillRef.Validate(); err != nil {
		return &InvalidSkillUsageError{Field: "skill_ref"}
	}
	if !skillUsageDigest(c.packageDigest) {
		return &InvalidSkillUsageError{Field: "package_digest"}
	}
	if err := validateReferencePart("target_id", c.targetID, maxSkillUsageTargetIDLength); err != nil {
		return &InvalidSkillUsageError{Field: "target_id"}
	}
	if !c.invoked.valid() {
		return &InvalidSkillUsageError{Field: "invoked"}
	}
	if !c.validation.valid() {
		return &InvalidSkillUsageError{Field: "validation"}
	}
	if !c.outcome.valid() {
		return &InvalidSkillUsageError{Field: "outcome"}
	}
	if c.taskSource != nil {
		if _, err := NewRef(c.taskSource.Type(), c.taskSource.ID()); err != nil {
			return &InvalidSkillUsageError{Field: "task_source"}
		}
	}
	if c.environmentFingerprint != nil && !skillUsageDigest(*c.environmentFingerprint) {
		return &InvalidSkillUsageError{Field: "environment_fingerprint"}
	}
	return nil
}

func (c SkillUsageCapture) ObservationID() string { return c.observationID }
func (c SkillUsageCapture) SkillRef() SkillUsageArtifactReference {
	return c.skillRef
}
func (c SkillUsageCapture) PackageDigest() string          { return c.packageDigest }
func (c SkillUsageCapture) TargetID() string               { return c.targetID }
func (c SkillUsageCapture) Selected() bool                 { return c.selected }
func (c SkillUsageCapture) Invoked() ObservedInvocation    { return c.invoked }
func (c SkillUsageCapture) Validation() ObservedValidation { return c.validation }
func (c SkillUsageCapture) Outcome() ObservedOutcome       { return c.outcome }

// TaskSource returns the optional Source that describes the completed task.
func (c SkillUsageCapture) TaskSource() (Ref, bool) {
	if c.taskSource == nil {
		return Ref{}, false
	}
	return *c.taskSource, true
}

// EnvironmentFingerprint returns the optional stable environment digest.
func (c SkillUsageCapture) EnvironmentFingerprint() (string, bool) {
	if c.environmentFingerprint == nil {
		return "", false
	}
	return *c.environmentFingerprint, true
}

// SkillUsageSource is the durable immutable representation of one usage capture.
type SkillUsageSource struct{ capture SkillUsageCapture }

// NewSkillUsageSource resolves a validated capture into its durable Source form.
func NewSkillUsageSource(capture SkillUsageCapture) (SkillUsageSource, error) {
	if err := capture.Validate(); err != nil {
		return SkillUsageSource{}, err
	}
	return SkillUsageSource{capture: capture}, nil
}

func (s SkillUsageSource) SourceName() string                   { return s.capture.ObservationID() }
func (SkillUsageSource) SourceMaterialization() Materialization { return Captured }
func (SkillUsageSource) SourceDescription() (string, bool)      { return skillUsageDescription, true }
func (s SkillUsageSource) Capture() SkillUsageCapture           { return s.capture }

// SkillUsageSourceAdapter maps the exact bounded capture and durable Source types.
type SkillUsageSourceAdapter struct{}

func (SkillUsageSourceAdapter) Name() string { return SkillUsageType }

func (SkillUsageSourceAdapter) Resolve(_ context.Context, value SkillUsageCapture) (SkillUsageSource, error) {
	return NewSkillUsageSource(value)
}

func (SkillUsageSourceAdapter) Read(_ context.Context, value SkillUsageSource) (SkillUsageCapture, error) {
	if err := value.capture.Validate(); err != nil {
		return SkillUsageCapture{}, err
	}
	return value.capture, nil
}

func (value ObservedInvocation) valid() bool {
	return value == ObservedInvocationTrue || value == ObservedInvocationFalse || value == ObservedInvocationUnknown
}

func (value ObservedValidation) valid() bool {
	return value == ObservedValidationPassed || value == ObservedValidationFailed || value == ObservedValidationUnknown
}

func (value ObservedOutcome) valid() bool {
	return value == ObservedOutcomeSuccess || value == ObservedOutcomeFailure || value == ObservedOutcomeUnknown
}

func skillUsageDigest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil && hex.EncodeToString(decoded) == strings.TrimPrefix(value, prefix)
}
