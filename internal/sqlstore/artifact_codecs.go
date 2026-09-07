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
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"reflect"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/artifact/skill"
)

// ExperienceArtifactCodec returns the Python-compatible Experience route.
func ExperienceArtifactCodec() ArtifactCodec {
	codec, err := NewArtifactCodec(experience.Family, encodeExperience, decodeExperience)
	if err != nil {
		panic(err)
	}
	return codec
}

// SkillArtifactCodec returns the legacy Python-compatible managed Skill route
// together with the Go-only package-backed v2 variant.
func SkillArtifactCodec() ArtifactCodec {
	return ArtifactCodec{
		family: skill.Family,
		contentTypes: map[reflect.Type]struct{}{
			reflect.TypeFor[skill.Content]():        {},
			reflect.TypeFor[skill.PackageContent](): {},
		},
		encode: encodeSkillVariant,
		decodeContent: func(payload []byte) (any, error) {
			return decodeSkill(payload)
		},
		decodeScoped: func(ctx context.Context, db DBTX, scopeID string, payload []byte) (any, error) {
			return decodeSkillVariant(ctx, db, scopeID, payload)
		},
		decode: func(
			ctx context.Context,
			db DBTX,
			scopeID string,
			ref artifact.Ref,
			lineage artifact.Lineage,
			payload []byte,
		) (artifact.Snapshot, error) {
			content, err := decodeSkillVariant(ctx, db, scopeID, payload)
			if err != nil {
				return nil, err
			}
			switch value := content.(type) {
			case skill.Content:
				return artifact.Restore(ref, value, lineage)
			case skill.PackageContent:
				return artifact.Restore(ref, value, lineage)
			default:
				return nil, fmt.Errorf("unsupported managed Skill content type %T", content)
			}
		},
	}
}

// MemoryArtifactCodec returns the authoritative Memory manifest route.
func MemoryArtifactCodec() ArtifactCodec {
	codec, err := NewArtifactCodec(memory.Family, encodeMemory, decodeMemory)
	if err != nil {
		panic(err)
	}
	return codec
}

type experienceJSON struct {
	Situation string `json:"situation"`
	Action    string `json:"action"`
	Outcome   string `json:"outcome"`
	Lesson    string `json:"lesson"`
}

func encodeExperience(value experience.Content) ([]byte, error) {
	return marshalJSON(experienceJSON{
		Situation: value.Situation(),
		Action:    value.Action(),
		Outcome:   value.Outcome(),
		Lesson:    value.Lesson(),
	})
}

func decodeExperience(payload []byte) (experience.Content, error) {
	var value experienceJSON
	if err := unmarshalJSON(payload, &value); err != nil {
		return experience.Content{}, err
	}
	return experience.NewContent(value.Situation, value.Action, value.Outcome, value.Lesson)
}

type skillJSON struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Instructions string   `json:"instructions"`
	Validation   []string `json:"validation"`
}

func encodeSkill(value skill.Content) ([]byte, error) {
	return marshalJSON(skillJSON{
		Name:         value.Name(),
		Description:  value.Description(),
		Instructions: value.Instructions(),
		Validation:   value.Validation(),
	})
}

func decodeSkill(payload []byte) (skill.Content, error) {
	var value skillJSON
	if err := unmarshalJSON(payload, &value); err != nil {
		return skill.Content{}, err
	}
	return skill.NewContent(value.Name, value.Description, value.Instructions, value.Validation)
}

type skillPackageContentJSON struct {
	PackageRef skillPackageRefJSON `json:"package_ref"`
}

type skillPackageRefJSON struct {
	TreeDigest       string `json:"tree_digest"`
	ArchiveDigest    string `json:"archive_digest"`
	FileCount        int    `json:"file_count"`
	UncompressedSize int    `json:"uncompressed_size"`
	ArchiveSize      int    `json:"archive_size"`
}

func encodeSkillVariant(value any) ([]byte, error) {
	switch content := value.(type) {
	case skill.Content:
		return encodeSkill(content)
	case skill.PackageContent:
		ref := content.Reference()
		if err := ref.Validate(); err != nil {
			return nil, err
		}
		return jsonv2.Marshal(skillPackageContentJSON{PackageRef: skillPackageRefJSON{
			TreeDigest:       ref.TreeDigest(),
			ArchiveDigest:    ref.ArchiveDigest(),
			FileCount:        ref.FileCount(),
			UncompressedSize: ref.UncompressedSize(),
			ArchiveSize:      ref.ArchiveSize(),
		}})
	default:
		return nil, fmt.Errorf("unsupported managed Skill content type %T", value)
	}
}

func decodeSkillVariant(ctx context.Context, db DBTX, scopeID string, payload []byte) (any, error) {
	var fields map[string]jsontext.Value
	if err := jsonv2.Unmarshal(payload, &fields); err != nil {
		return nil, err
	}
	if _, packageBacked := fields["package_ref"]; !packageBacked {
		return decodeSkill(payload)
	}
	var encoded skillPackageContentJSON
	if err := jsonv2.Unmarshal(payload, &encoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	ref, err := skill.NewPackageRef(
		encoded.PackageRef.TreeDigest,
		encoded.PackageRef.ArchiveDigest,
		encoded.PackageRef.FileCount,
		encoded.PackageRef.UncompressedSize,
		encoded.PackageRef.ArchiveSize,
	)
	if err != nil {
		return nil, err
	}
	snapshot, err := (SkillPackageRepository{}).Get(ctx, db, scopeID, ref)
	if err != nil {
		return nil, err
	}
	return skill.NewPackageContent(snapshot)
}

type memoryContentJSON struct {
	Manifest memoryManifestJSON `json:"manifest"`
	Changes  []memoryChangeJSON `json:"changes"`
	Schema   string             `json:"schema"`
}

type memoryManifestJSON struct {
	Entries []memoryManifestEntryJSON `json:"entries"`
	Format  string                    `json:"format"`
}

type memoryManifestEntryJSON struct {
	EntryID          string `json:"entry_id"`
	EntryVersionID   string `json:"entry_version_id"`
	EntryContentHash string `json:"entry_content_hash"`
	State            string `json:"state"`
}

type memoryChangeJSON struct {
	Op                 string  `json:"op"`
	EntryID            string  `json:"entry_id"`
	FromEntryVersionID *string `json:"from_entry_version_id"`
	ToEntryVersionID   *string `json:"to_entry_version_id"`
	Reason             *string `json:"reason"`
}

func encodeMemory(value memory.Content) ([]byte, error) {
	entries := value.Manifest().Entries()
	encodedEntries := make([]memoryManifestEntryJSON, len(entries))
	for index, entry := range entries {
		encodedEntries[index] = memoryManifestEntryJSON{
			EntryID:          entry.EntryID(),
			EntryVersionID:   entry.EntryVersionID(),
			EntryContentHash: entry.EntryContentHash(),
			State:            string(entry.State()),
		}
	}
	changes := value.Changes()
	encodedChanges := make([]memoryChangeJSON, len(changes))
	for index, change := range changes {
		encodedChanges[index] = memoryChangeJSON{
			Op:                 string(change.Op()),
			EntryID:            change.EntryID(),
			FromEntryVersionID: change.FromEntryVersionID(),
			ToEntryVersionID:   change.ToEntryVersionID(),
			Reason:             change.Reason(),
		}
	}
	return marshalJSON(memoryContentJSON{
		Manifest: memoryManifestJSON{Entries: encodedEntries, Format: value.Manifest().Format()},
		Changes:  encodedChanges,
		Schema:   value.Schema(),
	})
}

func decodeMemory(payload []byte) (memory.Content, error) {
	var fields map[string]json.RawMessage
	if err := unmarshalJSON(payload, &fields); err != nil {
		return memory.Content{}, err
	}
	manifestRaw, ok := fields["manifest"]
	if !ok || string(manifestRaw) == "null" {
		return memory.Content{}, fmt.Errorf("memory manifest is required")
	}
	manifest, err := decodeMemoryManifest(manifestRaw)
	if err != nil {
		return memory.Content{}, err
	}
	changes := []memory.Change{}
	if raw, exists := fields["changes"]; exists {
		if string(raw) == "null" {
			return memory.Content{}, fmt.Errorf("memory changes must be an array")
		}
		var encoded []map[string]json.RawMessage
		if err := unmarshalJSON(raw, &encoded); err != nil {
			return memory.Content{}, err
		}
		changes = make([]memory.Change, len(encoded))
		for index, item := range encoded {
			decoded, err := decodeMemoryChange(item)
			if err != nil {
				return memory.Content{}, err
			}
			changes[index] = decoded
		}
	}
	schema := "powercontext.memory.v1"
	if raw, exists := fields["schema"]; exists {
		if string(raw) == "null" {
			return memory.Content{}, fmt.Errorf("Memory schema cannot be null")
		}
		if err := unmarshalJSON(raw, &schema); err != nil {
			return memory.Content{}, err
		}
	}
	if schema != "powercontext.memory.v1" {
		return memory.Content{}, fmt.Errorf("unsupported Memory schema %q", schema)
	}
	return memory.NewContent(manifest, changes), nil
}

func decodeMemoryManifest(payload []byte) (memory.Manifest, error) {
	var fields map[string]json.RawMessage
	if err := unmarshalJSON(payload, &fields); err != nil {
		return memory.Manifest{}, err
	}
	format := "flat-v1"
	if raw, exists := fields["format"]; exists {
		if string(raw) == "null" {
			return memory.Manifest{}, fmt.Errorf("Memory manifest format cannot be null")
		}
		if err := unmarshalJSON(raw, &format); err != nil {
			return memory.Manifest{}, err
		}
	}
	if format != "flat-v1" {
		return memory.Manifest{}, fmt.Errorf("unsupported Memory manifest format %q", format)
	}
	entries := []memory.ManifestEntry{}
	if raw, exists := fields["entries"]; exists {
		if string(raw) == "null" {
			return memory.Manifest{}, fmt.Errorf("memory manifest entries must be an array")
		}
		var encoded []memoryManifestEntryJSON
		if err := unmarshalJSON(raw, &encoded); err != nil {
			return memory.Manifest{}, err
		}
		entries = make([]memory.ManifestEntry, len(encoded))
		for index, item := range encoded {
			entry, err := memory.NewManifestEntry(
				item.EntryID,
				item.EntryVersionID,
				item.EntryContentHash,
				memory.EntryState(item.State),
			)
			if err != nil {
				return memory.Manifest{}, err
			}
			entries[index] = entry
		}
	}
	return memory.NewManifest(entries), nil
}

func decodeMemoryChange(fields map[string]json.RawMessage) (memory.Change, error) {
	requiredString := func(name string) (string, error) {
		raw, ok := fields[name]
		if !ok {
			return "", fmt.Errorf("memory change field %q is required", name)
		}
		var value string
		if err := unmarshalJSON(raw, &value); err != nil {
			return "", err
		}
		return value, nil
	}
	op, err := requiredString("op")
	if err != nil {
		return memory.Change{}, err
	}
	entryID, err := requiredString("entry_id")
	if err != nil {
		return memory.Change{}, err
	}
	from, err := requiredNullableString(fields, "from_entry_version_id", true)
	if err != nil {
		return memory.Change{}, err
	}
	to, err := requiredNullableString(fields, "to_entry_version_id", true)
	if err != nil {
		return memory.Change{}, err
	}
	reason, err := requiredNullableString(fields, "reason", false)
	if err != nil {
		return memory.Change{}, err
	}
	return memory.NewChange(memory.ChangeOp(op), entryID, from, to, reason)
}

func requiredNullableString(fields map[string]json.RawMessage, name string, required bool) (*string, error) {
	raw, ok := fields[name]
	if !ok {
		if required {
			return nil, fmt.Errorf("field %q is required", name)
		}
		return nil, nil
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var value string
	if err := unmarshalJSON(raw, &value); err != nil {
		return nil, err
	}
	return &value, nil
}
