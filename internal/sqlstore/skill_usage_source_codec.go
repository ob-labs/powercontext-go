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
	json "encoding/json/v2"
	"fmt"

	"github.com/ob-labs/powercontext-go/source"
)

// SkillUsageSourceCodec persists the bounded managed-Skill usage Source.
func SkillUsageSourceCodec() SourceCodec {
	codec, err := NewSourceCodec(source.SkillUsageType, encodeSkillUsageSource, decodeSkillUsageSource)
	if err != nil {
		panic(err)
	}
	return codec
}

type skillUsageSourceJSON struct {
	Name                   string                    `json:"name"`
	Materialization        source.Materialization    `json:"materialization"`
	Description            *string                   `json:"description"`
	SkillRef               artifactRefJSON           `json:"skill_ref"`
	PackageDigest          string                    `json:"package_digest"`
	TargetID               string                    `json:"target_id"`
	Selected               bool                      `json:"selected"`
	Invoked                source.ObservedInvocation `json:"invoked"`
	Validation             source.ObservedValidation `json:"validation"`
	Outcome                source.ObservedOutcome    `json:"outcome"`
	TaskSource             *sourceRefJSON            `json:"task_source,omitempty"`
	EnvironmentFingerprint *string                   `json:"environment_fingerprint,omitempty"`
}

func encodeSkillUsageSource(value source.SkillUsageSource) ([]byte, error) {
	capture := value.Capture()
	description, present := value.SourceDescription()
	if !present {
		return nil, fmt.Errorf("Skill usage Source description is missing")
	}
	encoded := skillUsageSourceJSON{
		Name: value.SourceName(), Materialization: value.SourceMaterialization(), Description: &description,
		SkillRef:      artifactRefJSON{Family: capture.SkillRef().Family(), ArtifactID: capture.SkillRef().ID(), Revision: capture.SkillRef().Revision()},
		PackageDigest: capture.PackageDigest(), TargetID: capture.TargetID(), Selected: capture.Selected(),
		Invoked: capture.Invoked(), Validation: capture.Validation(), Outcome: capture.Outcome(),
	}
	if task, found := capture.TaskSource(); found {
		encoded.TaskSource = &sourceRefJSON{SourceType: task.Type(), SourceID: task.ID()}
	}
	if fingerprint, found := capture.EnvironmentFingerprint(); found {
		encoded.EnvironmentFingerprint = &fingerprint
	}
	return json.Marshal(encoded, json.Deterministic(true))
}

func decodeSkillUsageSource(payload []byte) (source.SkillUsageSource, error) {
	var encoded skillUsageSourceJSON
	if err := json.Unmarshal(payload, &encoded, json.RejectUnknownMembers(true)); err != nil {
		return source.SkillUsageSource{}, err
	}
	skillRef, err := source.NewSkillUsageArtifactReference(encoded.SkillRef.Family, encoded.SkillRef.ArtifactID, encoded.SkillRef.Revision)
	if err != nil {
		return source.SkillUsageSource{}, err
	}
	var taskSource *source.Ref
	if encoded.TaskSource != nil {
		ref, refErr := source.NewRef(encoded.TaskSource.SourceType, encoded.TaskSource.SourceID)
		if refErr != nil {
			return source.SkillUsageSource{}, refErr
		}
		taskSource = &ref
	}
	capture, err := source.NewSkillUsageCapture(
		encoded.Name, skillRef, encoded.PackageDigest, encoded.TargetID, encoded.Selected,
		encoded.Invoked, encoded.Validation, encoded.Outcome, taskSource, encoded.EnvironmentFingerprint,
	)
	if err != nil {
		return source.SkillUsageSource{}, err
	}
	resolved, err := (source.SkillUsageSourceAdapter{}).Resolve(context.Background(), capture)
	if err != nil {
		return source.SkillUsageSource{}, err
	}
	description, _ := resolved.SourceDescription()
	if encoded.Materialization != source.Captured || encoded.Description == nil || *encoded.Description != description {
		return source.SkillUsageSource{}, fmt.Errorf("Skill usage Source authority fields are inconsistent")
	}
	return resolved, nil
}
