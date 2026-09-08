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

// SkillPackageUploadSourceCodec persists archive-free standard Skill upload
// evidence. The immutable package archive stays in Skill package storage.
func SkillPackageUploadSourceCodec() SourceCodec {
	codec, err := NewSourceCodec(
		source.SkillPackageUploadType,
		encodeSkillPackageUploadSource,
		decodeSkillPackageUploadSource,
	)
	if err != nil {
		panic(err)
	}
	return codec
}

type skillPackageUploadReferenceJSON struct {
	TreeDigest       string `json:"tree_digest"`
	ArchiveDigest    string `json:"archive_digest"`
	FileCount        int    `json:"file_count"`
	UncompressedSize int    `json:"uncompressed_size"`
	ArchiveSize      int    `json:"archive_size"`
}

type skillPackageUploadSourceJSON struct {
	Name             string                          `json:"name"`
	Materialization  source.Materialization          `json:"materialization"`
	Description      *string                         `json:"description"`
	Package          skillPackageUploadReferenceJSON `json:"package"`
	SkillName        string                          `json:"skill_name"`
	SkillDescription string                          `json:"skill_description"`
}

func encodeSkillPackageUploadSource(value source.SkillPackageUploadSource) ([]byte, error) {
	description, present := value.SourceDescription()
	if !present {
		return nil, fmt.Errorf("Skill package upload Source description is missing")
	}
	capture := value.Capture()
	packageRef := capture.Package()
	return json.Marshal(skillPackageUploadSourceJSON{
		Name: value.SourceName(), Materialization: value.SourceMaterialization(), Description: &description,
		Package: skillPackageUploadReferenceJSON{
			TreeDigest: packageRef.TreeDigest(), ArchiveDigest: packageRef.ArchiveDigest(),
			FileCount: packageRef.FileCount(), UncompressedSize: packageRef.UncompressedSize(), ArchiveSize: packageRef.ArchiveSize(),
		},
		SkillName: capture.SkillName(), SkillDescription: capture.SkillDescription(),
	}, json.Deterministic(true))
}

func decodeSkillPackageUploadSource(payload []byte) (source.SkillPackageUploadSource, error) {
	var encoded skillPackageUploadSourceJSON
	if err := json.Unmarshal(payload, &encoded, json.RejectUnknownMembers(true)); err != nil {
		return source.SkillPackageUploadSource{}, err
	}
	capture, err := source.NewSkillPackageUploadCapture(
		encoded.Package.TreeDigest, encoded.Package.ArchiveDigest,
		encoded.Package.FileCount, encoded.Package.UncompressedSize, encoded.Package.ArchiveSize,
		encoded.SkillName, encoded.SkillDescription,
	)
	if err != nil {
		return source.SkillPackageUploadSource{}, err
	}
	resolved, err := (source.SkillPackageUploadSourceAdapter{}).Resolve(context.Background(), capture)
	if err != nil {
		return source.SkillPackageUploadSource{}, err
	}
	description, _ := resolved.SourceDescription()
	if encoded.Name != resolved.SourceName() || encoded.Materialization != source.Captured ||
		encoded.Description == nil || *encoded.Description != description {
		return source.SkillPackageUploadSource{}, fmt.Errorf("Skill package upload Source authority fields are inconsistent")
	}
	return resolved, nil
}
