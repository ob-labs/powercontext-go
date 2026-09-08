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
	"strings"
	"testing"
)

func TestSkillPackageUploadCaptureUsesPackageTreeIdentity(t *testing.T) {
	treeDigest := strings.Repeat("a", 64)
	capture, err := NewSkillPackageUploadCapture(
		treeDigest,
		strings.Repeat("b", 64),
		2,
		128,
		96,
		"release-check",
		"Validate the release package.",
	)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := (SkillPackageUploadSourceAdapter{}).Resolve(t.Context(), capture)
	if err != nil {
		t.Fatal(err)
	}
	if resource.SourceName() != "skill_pkg_"+treeDigest {
		t.Fatalf("SourceName() = %q", resource.SourceName())
	}
	if resource.SourceMaterialization() != Captured {
		t.Fatalf("SourceMaterialization() = %q", resource.SourceMaterialization())
	}
	description, found := resource.SourceDescription()
	if !found || description != skillPackageUploadDescription {
		t.Fatalf("SourceDescription() = %q, %t", description, found)
	}
	stored := resource.Capture()
	if stored.SkillName() != "release-check" || stored.SkillDescription() != "Validate the release package." {
		t.Fatalf("captured metadata = %#v", stored)
	}
	if stored.Package().TreeDigest() != treeDigest || stored.Package().ArchiveDigest() != strings.Repeat("b", 64) {
		t.Fatalf("package reference = %#v", stored.Package())
	}
}

func TestSkillPackageUploadCaptureRejectsNonCanonicalFields(t *testing.T) {
	_, err := NewSkillPackageUploadCapture(
		strings.Repeat("A", 64), strings.Repeat("b", 64), 1, 1, 1, "skill", "description",
	)
	if err == nil {
		t.Fatal("non-canonical package digest was accepted")
	}
}
