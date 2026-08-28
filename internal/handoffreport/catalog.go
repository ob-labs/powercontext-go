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

package handoffreport

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ExternalReference struct {
	kind       ExternalReferenceKind
	provider   string
	externalID string
	url        *string
}

func NewExternalReference(kind ExternalReferenceKind, provider, externalID string, target *string) (ExternalReference, error) {
	value := ExternalReference{kind: kind, provider: provider, externalID: externalID, url: cloneString(target)}
	if err := value.Validate(); err != nil {
		return ExternalReference{}, err
	}
	return value, nil
}

func (v ExternalReference) Validate() error {
	if !validExternalKind(v.kind) {
		return fieldError("kind", "has an unsupported value")
	}
	if err := requireText("provider", v.provider, MaxReportProviderLength); err != nil {
		return err
	}
	if err := requireText("external_id", v.externalID, MaxReportExternalIDLength); err != nil {
		return err
	}
	if err := requireOptionalText("url", v.url, MaxReportURLLength); err != nil {
		return err
	}
	return nil
}
func (v ExternalReference) Kind() ExternalReferenceKind { return v.kind }
func (v ExternalReference) Provider() string            { return v.provider }
func (v ExternalReference) ExternalID() string          { return v.externalID }
func (v ExternalReference) URL() *string                { return cloneString(v.url) }
func (v ExternalReference) key() string {
	return string(v.kind) + "\x00" + v.provider + "\x00" + v.externalID + "\x00" + optionalKey(v.url)
}

func (v ExternalReference) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"kind": v.kind, "provider": v.provider, "external_id": v.externalID, "url": v.url})
}

func (v *ExternalReference) UnmarshalJSON(data []byte) error {
	var dto struct {
		Kind       ExternalReferenceKind `json:"kind"`
		Provider   string                `json:"provider"`
		ExternalID string                `json:"external_id"`
		URL        *string               `json:"url"`
	}
	if err := decodeStrict(data, &dto); err != nil {
		return err
	}
	value, err := NewExternalReference(dto.Kind, dto.Provider, dto.ExternalID, dto.URL)
	if err == nil {
		*v = value
	}
	return err
}

type RepositoryRef struct {
	provider                                RepositoryProvider
	repositoryID, normalizedRemote, subpath *string
}

func NewRepositoryRef(provider RepositoryProvider, repositoryID, normalizedRemote, subpath *string) (RepositoryRef, error) {
	if !validRepositoryProvider(provider) {
		return RepositoryRef{}, fieldError("provider", "has an unsupported value")
	}
	if err := requireOptionalText("repository_id", repositoryID, MaxReportRepositoryIDLength); err != nil {
		return RepositoryRef{}, err
	}
	if err := requireOptionalText("normalized_remote", normalizedRemote, MaxReportNormalizedRemoteLength); err != nil {
		return RepositoryRef{}, err
	}
	if err := requireOptionalText("subpath", subpath, MaxReportSubpathLength); err != nil {
		return RepositoryRef{}, err
	}
	if repositoryID == nil && normalizedRemote == nil && subpath == nil {
		return RepositoryRef{}, fieldError("repository_ref", "must contain a repository id, remote, or subpath")
	}
	if normalizedRemote != nil && strings.ContainsAny(*normalizedRemote, "@?#") {
		return RepositoryRef{}, fieldError("normalized_remote", "must not contain credentials or query fragments")
	}
	remote, err := normalizeRepositoryRemote(normalizedRemote)
	if err != nil {
		return RepositoryRef{}, err
	}
	cleanSubpath, err := normalizeRepositorySubpath(subpath)
	if err != nil {
		return RepositoryRef{}, err
	}
	return RepositoryRef{provider: provider, repositoryID: cloneString(repositoryID), normalizedRemote: remote, subpath: cleanSubpath}, nil
}
func (v RepositoryRef) Provider() RepositoryProvider { return v.provider }
func (v RepositoryRef) RepositoryID() *string        { return cloneString(v.repositoryID) }
func (v RepositoryRef) NormalizedRemote() *string    { return cloneString(v.normalizedRemote) }
func (v RepositoryRef) Subpath() *string             { return cloneString(v.subpath) }
func (v RepositoryRef) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"provider": v.provider, "repository_id": v.repositoryID, "normalized_remote": v.normalizedRemote, "subpath": v.subpath})
}

func (v *RepositoryRef) UnmarshalJSON(data []byte) error {
	var dto struct {
		Provider         RepositoryProvider `json:"provider"`
		RepositoryID     *string            `json:"repository_id"`
		NormalizedRemote *string            `json:"normalized_remote"`
		Subpath          *string            `json:"subpath"`
	}
	if err := decodeStrict(data, &dto); err != nil {
		return err
	}
	value, err := NewRepositoryRef(dto.Provider, dto.RepositoryID, dto.NormalizedRemote, dto.Subpath)
	if err == nil {
		*v = value
	}
	return err
}

type ProjectDescriptor struct {
	projectID, projectKey, title string
	description                  *string
	defaultLocale                Locale
	timezone                     string
	catalogState                 CatalogState
	version                      int
}

func NewProjectDescriptor(projectID, projectKey, title string, description *string, locale Locale, timezone string, state CatalogState, version int) (ProjectDescriptor, error) {
	value := ProjectDescriptor{
		projectID: projectID, projectKey: projectKey, title: title, description: cloneString(description),
		defaultLocale: locale, timezone: timezone, catalogState: state, version: version,
	}
	if err := value.Validate(); err != nil {
		return ProjectDescriptor{}, err
	}
	return value, nil
}

func (v ProjectDescriptor) Validate() error {
	for _, item := range []struct {
		name, value string
		max         int
	}{{"project_id", v.projectID, MaxReportIDLength}, {"project_key", v.projectKey, MaxProjectKeyLength}, {"title", v.title, MaxReportTitleLength}, {"timezone", v.timezone, MaxReportIDLength}} {
		if err := requireText(item.name, item.value, item.max); err != nil {
			return err
		}
	}
	if err := requireOptionalText("description", v.description, MaxReportDescriptionLength); err != nil {
		return err
	}
	if v.defaultLocale != LocaleChinese && v.defaultLocale != LocaleEnglish {
		return fieldError("default_locale", "has an unsupported value")
	}
	if v.timezone == "Local" {
		return fieldError("timezone", "must be a recognized IANA timezone")
	}
	if _, err := time.LoadLocation(v.timezone); err != nil {
		return fieldError("timezone", "must be a recognized IANA timezone")
	}
	if v.catalogState != CatalogIncluded && v.catalogState != CatalogArchived {
		return fieldError("catalog_state", "has an unsupported value")
	}
	if v.version < 1 {
		return fieldError("version", "must be a positive integer")
	}
	return nil
}
func (v ProjectDescriptor) ProjectID() string          { return v.projectID }
func (v ProjectDescriptor) ProjectKey() string         { return v.projectKey }
func (v ProjectDescriptor) Title() string              { return v.title }
func (v ProjectDescriptor) Description() *string       { return cloneString(v.description) }
func (v ProjectDescriptor) DefaultLocale() Locale      { return v.defaultLocale }
func (v ProjectDescriptor) Timezone() string           { return v.timezone }
func (v ProjectDescriptor) CatalogState() CatalogState { return v.catalogState }
func (v ProjectDescriptor) Version() int               { return v.version }
func (v ProjectDescriptor) Schema() string             { return ProjectSchemaVersion }
func (v ProjectDescriptor) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"schema": ProjectSchemaVersion, "project_id": v.projectID, "project_key": v.projectKey, "title": v.title, "description": v.description, "default_locale": v.defaultLocale, "timezone": v.timezone, "catalog_state": v.catalogState, "version": v.version})
}

func (v *ProjectDescriptor) UnmarshalJSON(data []byte) error {
	var dto struct {
		Schema        string       `json:"schema"`
		ProjectID     string       `json:"project_id"`
		ProjectKey    string       `json:"project_key"`
		Title         string       `json:"title"`
		Description   *string      `json:"description"`
		DefaultLocale Locale       `json:"default_locale"`
		Timezone      string       `json:"timezone"`
		CatalogState  CatalogState `json:"catalog_state"`
		Version       int          `json:"version"`
	}
	if err := decodeStrict(data, &dto); err != nil {
		return err
	}
	if dto.Schema != ProjectSchemaVersion {
		return fieldError("schema", "has an unsupported value")
	}
	value, err := NewProjectDescriptor(dto.ProjectID, dto.ProjectKey, dto.Title, dto.Description, dto.DefaultLocale, dto.Timezone, dto.CatalogState, dto.Version)
	if err == nil {
		*v = value
	}
	return err
}

type WorkstreamDescriptor struct {
	scopeID, projectID string
	key                *string
	title              string
	kind               WorkstreamKind
	catalogState       CatalogState
	externalRefs       []ExternalReference
	labels             []string
	version            int
}

func NewWorkstreamDescriptor(scopeID, projectID string, key *string, title string, kind WorkstreamKind, state CatalogState, refs []ExternalReference, labels []string, version int) (WorkstreamDescriptor, error) {
	externalRefs := make([]ExternalReference, len(refs))
	copy(externalRefs, refs)
	clonedLabels := make([]string, len(labels))
	copy(clonedLabels, labels)
	value := WorkstreamDescriptor{
		scopeID: scopeID, projectID: projectID, key: cloneString(key), title: title,
		kind: kind, catalogState: state, externalRefs: externalRefs, labels: clonedLabels, version: version,
	}
	if err := value.Validate(); err != nil {
		return WorkstreamDescriptor{}, err
	}
	return value, nil
}

func (v WorkstreamDescriptor) Validate() error {
	if err := requireText("scope_id", v.scopeID, MaxScopeIDLength); err != nil {
		return err
	}
	if err := requireText("project_id", v.projectID, MaxReportIDLength); err != nil {
		return err
	}
	if err := requireOptionalText("key", v.key, MaxWorkstreamKeyLength); err != nil {
		return err
	}
	if err := requireText("title", v.title, MaxReportTitleLength); err != nil {
		return err
	}
	if !validWorkstreamKind(v.kind) {
		return fieldError("kind", "has an unsupported value")
	}
	if v.catalogState != CatalogIncluded && v.catalogState != CatalogArchived {
		return fieldError("catalog_state", "has an unsupported value")
	}
	if v.version < 1 {
		return fieldError("version", "must be a positive integer")
	}
	if len(v.externalRefs) > MaxReportExternalRefs {
		return fieldError("external_refs", fmt.Sprintf("must not exceed %d items", MaxReportExternalRefs))
	}
	if len(v.labels) > MaxReportLabels {
		return fieldError("labels", fmt.Sprintf("must not exceed %d items", MaxReportLabels))
	}
	seenRefs := map[string]struct{}{}
	for _, ref := range v.externalRefs {
		if err := ref.Validate(); err != nil {
			return err
		}
		if _, ok := seenRefs[ref.key()]; ok {
			return fieldError("external_refs", "must be unique")
		}
		seenRefs[ref.key()] = struct{}{}
	}
	seenLabels := map[string]struct{}{}
	for _, label := range v.labels {
		if err := requireText("label", label, MaxReportLabelLength); err != nil {
			return err
		}
		if _, ok := seenLabels[label]; ok {
			return fieldError("labels", "must be unique")
		}
		seenLabels[label] = struct{}{}
	}
	return nil
}
func (v WorkstreamDescriptor) ScopeID() string                   { return v.scopeID }
func (v WorkstreamDescriptor) ProjectID() string                 { return v.projectID }
func (v WorkstreamDescriptor) Key() *string                      { return cloneString(v.key) }
func (v WorkstreamDescriptor) Title() string                     { return v.title }
func (v WorkstreamDescriptor) Kind() WorkstreamKind              { return v.kind }
func (v WorkstreamDescriptor) CatalogState() CatalogState        { return v.catalogState }
func (v WorkstreamDescriptor) ExternalRefs() []ExternalReference { return slices.Clone(v.externalRefs) }
func (v WorkstreamDescriptor) Labels() []string                  { return slices.Clone(v.labels) }
func (v WorkstreamDescriptor) Version() int                      { return v.version }
func (v WorkstreamDescriptor) Schema() string                    { return WorkstreamSchemaVersion }
func (v WorkstreamDescriptor) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"schema": WorkstreamSchemaVersion, "scope_id": v.scopeID, "project_id": v.projectID, "key": v.key, "title": v.title, "kind": v.kind, "catalog_state": v.catalogState, "external_refs": v.externalRefs, "labels": v.labels, "version": v.version})
}

func (v *WorkstreamDescriptor) UnmarshalJSON(data []byte) error {
	var dto struct {
		Schema       string              `json:"schema"`
		ScopeID      string              `json:"scope_id"`
		ProjectID    string              `json:"project_id"`
		Key          *string             `json:"key"`
		Title        string              `json:"title"`
		Kind         WorkstreamKind      `json:"kind"`
		CatalogState CatalogState        `json:"catalog_state"`
		ExternalRefs []ExternalReference `json:"external_refs"`
		Labels       []string            `json:"labels"`
		Version      int                 `json:"version"`
	}
	if err := decodeStrict(data, &dto); err != nil {
		return err
	}
	if dto.Schema != WorkstreamSchemaVersion {
		return fieldError("schema", "has an unsupported value")
	}
	value, err := NewWorkstreamDescriptor(dto.ScopeID, dto.ProjectID, dto.Key, dto.Title, dto.Kind, dto.CatalogState, dto.ExternalRefs, dto.Labels, dto.Version)
	if err == nil {
		*v = value
	}
	return err
}
