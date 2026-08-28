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
	"time"
)

// ActivityAgent identifies the producer of an observed activity without
// granting the observation any additional trust.
type ActivityAgent struct{ provider, label *string }

func NewActivityAgent(provider, label *string) (ActivityAgent, error) {
	value := ActivityAgent{cloneString(provider), cloneString(label)}
	if err := value.Validate(); err != nil {
		return ActivityAgent{}, err
	}
	return value, nil
}
func (v ActivityAgent) Provider() *string { return cloneString(v.provider) }
func (v ActivityAgent) Label() *string    { return cloneString(v.label) }
func (v ActivityAgent) Validate() error {
	if err := requireOptionalText("provider", v.provider, MaxReportProviderLength); err != nil {
		return err
	}
	if err := requireOptionalText("label", v.label, MaxReportAgentLabelLength); err != nil {
		return err
	}
	if v.provider == nil && v.label == nil {
		return fieldError("agent", "must contain a provider or label")
	}
	return nil
}

func (v ActivityAgent) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"provider": v.provider, "label": v.label})
}

func (v *ActivityAgent) UnmarshalJSON(data []byte) error {
	var dto struct {
		Provider *string `json:"provider"`
		Label    *string `json:"label"`
	}
	if err := decodeStrict(data, &dto); err != nil {
		return err
	}
	value, err := NewActivityAgent(dto.Provider, dto.Label)
	if err == nil {
		*v = value
	}
	return err
}

type ActivityVCSContext struct{ branch, headRevision *string }

func NewActivityVCSContext(branch, head *string) (ActivityVCSContext, error) {
	value := ActivityVCSContext{cloneString(branch), cloneString(head)}
	if err := value.Validate(); err != nil {
		return ActivityVCSContext{}, err
	}
	return value, nil
}
func (v ActivityVCSContext) Branch() *string       { return cloneString(v.branch) }
func (v ActivityVCSContext) HeadRevision() *string { return cloneString(v.headRevision) }
func (v ActivityVCSContext) Validate() error {
	if err := requireOptionalText("branch", v.branch, MaxReportTitleLength); err != nil {
		return err
	}
	if err := requireOptionalText("head_revision", v.headRevision, MaxReportIDLength); err != nil {
		return err
	}
	if v.branch == nil && v.headRevision == nil {
		return fieldError("vcs_context", "must contain a branch or head revision")
	}
	return nil
}

func (v ActivityVCSContext) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"branch": v.branch, "head_revision": v.headRevision})
}

func (v *ActivityVCSContext) UnmarshalJSON(data []byte) error {
	var dto struct {
		Branch       *string `json:"branch"`
		HeadRevision *string `json:"head_revision"`
	}
	if err := decodeStrict(data, &dto); err != nil {
		return err
	}
	value, err := NewActivityVCSContext(dto.Branch, dto.HeadRevision)
	if err == nil {
		*v = value
	}
	return err
}

type ActivityEvent struct {
	eventID, projectID string
	scopeID            *string
	source             ActivitySource
	sourceEventID      string
	sourceRef          *ExternalReference
	occurredAt         *time.Time
	observedAt         time.Time
	timeBasis          TimeBasis
	title, summary     *string
	agent              *ActivityAgent
	sessionID          *string
	vcs                *ActivityVCSContext
	evidenceRefs       []ExternalReference
}

type ActivityEventInput struct {
	EventID, ProjectID string
	ScopeID            *string
	Source             ActivitySource
	SourceEventID      string
	SourceRef          *ExternalReference
	OccurredAt         *time.Time
	ObservedAt         time.Time
	TimeBasis          TimeBasis
	Title, Summary     *string
	Agent              *ActivityAgent
	SessionID          *string
	VCSContext         *ActivityVCSContext
	EvidenceRefs       []ExternalReference
}

func NewActivityEvent(in ActivityEventInput) (ActivityEvent, error) {
	if err := requireText("event_id", in.EventID, MaxReportIDLength); err != nil {
		return ActivityEvent{}, activityField(err)
	}
	if err := requireText("project_id", in.ProjectID, MaxReportIDLength); err != nil {
		return ActivityEvent{}, activityField(err)
	}
	if err := requireOptionalText("scope_id", in.ScopeID, MaxScopeIDLength); err != nil {
		return ActivityEvent{}, activityField(err)
	}
	if !validActivitySource(in.Source) {
		return ActivityEvent{}, invalidActivity("source", "has an unsupported value")
	}
	if err := requireText("source_event_id", in.SourceEventID, MaxReportIDLength); err != nil {
		return ActivityEvent{}, activityField(err)
	}
	if in.SourceRef != nil {
		if err := in.SourceRef.Validate(); err != nil {
			return ActivityEvent{}, activityField(err)
		}
	}
	_, observedOffset := in.ObservedAt.Zone()
	if observedOffset != 0 {
		return ActivityEvent{}, invalidActivity("observed_at", "must be UTC")
	}
	if !validTimeBasis(in.TimeBasis) {
		return ActivityEvent{}, invalidActivity("time_basis", "has an unsupported value")
	}
	if in.TimeBasis == TimeSourceReported && in.OccurredAt == nil {
		return ActivityEvent{}, invalidActivity("occurred_at", "is required for source-reported activity")
	}
	if in.TimeBasis != TimeSourceReported && in.OccurredAt != nil {
		return ActivityEvent{}, invalidActivity("occurred_at", "is only valid for source-reported activity")
	}
	optionalText := map[string]struct {
		value   *string
		maximum int
	}{
		"title":      {in.Title, MaxReportTitleLength},
		"summary":    {in.Summary, MaxReportSourceSummaryLength},
		"session_id": {in.SessionID, MaxReportIDLength},
	}
	for name, item := range optionalText {
		if err := requireOptionalText(name, item.value, item.maximum); err != nil {
			return ActivityEvent{}, activityField(err)
		}
	}
	if in.Agent != nil {
		if err := in.Agent.Validate(); err != nil {
			return ActivityEvent{}, activityField(err)
		}
	}
	if in.VCSContext != nil {
		if err := in.VCSContext.Validate(); err != nil {
			return ActivityEvent{}, activityField(err)
		}
	}
	if len(in.EvidenceRefs) > MaxReportEvidenceRefs {
		return ActivityEvent{}, invalidActivity("evidence_refs", fmt.Sprintf("must not exceed %d items", MaxReportEvidenceRefs))
	}
	seen := map[string]struct{}{}
	for _, ref := range in.EvidenceRefs {
		if err := ref.Validate(); err != nil {
			return ActivityEvent{}, activityField(err)
		}
		if _, ok := seen[ref.key()]; ok {
			return ActivityEvent{}, invalidActivity("evidence_refs", "must be unique")
		}
		seen[ref.key()] = struct{}{}
	}
	evidenceRefs := make([]ExternalReference, len(in.EvidenceRefs))
	copy(evidenceRefs, in.EvidenceRefs)
	occurredAt := cloneTime(in.OccurredAt)
	if occurredAt != nil {
		truncated := occurredAt.Truncate(time.Microsecond)
		occurredAt = &truncated
	}
	return ActivityEvent{eventID: in.EventID, projectID: in.ProjectID, scopeID: cloneString(in.ScopeID), source: in.Source, sourceEventID: in.SourceEventID, sourceRef: cloneExternal(in.SourceRef), occurredAt: occurredAt, observedAt: in.ObservedAt.Truncate(time.Microsecond), timeBasis: in.TimeBasis, title: cloneString(in.Title), summary: cloneString(in.Summary), agent: cloneAgent(in.Agent), sessionID: cloneString(in.SessionID), vcs: cloneVCS(in.VCSContext), evidenceRefs: evidenceRefs}, nil
}

func (v ActivityEvent) EventID() string                   { return v.eventID }
func (v ActivityEvent) Schema() string                    { return ActivitySchemaVersion }
func (v ActivityEvent) Trust() string                     { return ActivityTrust }
func (v ActivityEvent) ProjectID() string                 { return v.projectID }
func (v ActivityEvent) ScopeID() *string                  { return cloneString(v.scopeID) }
func (v ActivityEvent) Source() ActivitySource            { return v.source }
func (v ActivityEvent) SourceEventID() string             { return v.sourceEventID }
func (v ActivityEvent) SourceRef() *ExternalReference     { return cloneExternal(v.sourceRef) }
func (v ActivityEvent) OccurredAt() *time.Time            { return cloneTime(v.occurredAt) }
func (v ActivityEvent) ObservedAt() time.Time             { return v.observedAt }
func (v ActivityEvent) TimeBasis() TimeBasis              { return v.timeBasis }
func (v ActivityEvent) Title() *string                    { return cloneString(v.title) }
func (v ActivityEvent) Summary() *string                  { return cloneString(v.summary) }
func (v ActivityEvent) Agent() *ActivityAgent             { return cloneAgent(v.agent) }
func (v ActivityEvent) SessionID() *string                { return cloneString(v.sessionID) }
func (v ActivityEvent) VCSContext() *ActivityVCSContext   { return cloneVCS(v.vcs) }
func (v ActivityEvent) EvidenceRefs() []ExternalReference { return slices.Clone(v.evidenceRefs) }
func (v ActivityEvent) Validate() error {
	_, err := NewActivityEvent(ActivityEventInput{
		EventID: v.eventID, ProjectID: v.projectID, ScopeID: v.scopeID,
		Source: v.source, SourceEventID: v.sourceEventID, SourceRef: v.sourceRef,
		OccurredAt: v.occurredAt, ObservedAt: v.observedAt, TimeBasis: v.timeBasis,
		Title: v.title, Summary: v.summary, Agent: v.agent, SessionID: v.sessionID,
		VCSContext: v.vcs, EvidenceRefs: v.evidenceRefs,
	})
	return err
}

func (v ActivityEvent) EffectivePeriodTime() *time.Time {
	switch v.timeBasis {
	case TimeSourceReported:
		return cloneTime(v.occurredAt)
	case TimeHostObserved, TimeFirstSeen:
		t := v.observedAt
		return &t
	default:
		return nil
	}
}

func (v ActivityEvent) MarshalJSON() ([]byte, error) {
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(activityEventObject(v, false))
}

func activityEventObject(v ActivityEvent, canonical bool) map[string]any {
	observedAt := JSONTimestampText(v.observedAt)
	var occurredAt any
	if v.occurredAt != nil {
		occurredAt = JSONTimestampText(*v.occurredAt)
	}
	if canonical {
		observedAt = UTCText(v.observedAt)
		if v.occurredAt != nil {
			occurredAt = UTCText(*v.occurredAt)
		}
	}
	return map[string]any{
		"schema": ActivitySchemaVersion, "event_id": v.eventID, "project_id": v.projectID,
		"scope_id": v.scopeID, "source": v.source, "source_event_id": v.sourceEventID,
		"source_ref": v.sourceRef, "occurred_at": occurredAt, "observed_at": observedAt,
		"time_basis": v.timeBasis, "title": v.title, "summary": v.summary, "agent": v.agent,
		"session_id": v.sessionID, "vcs_context": v.vcs, "evidence_refs": v.evidenceRefs,
		"trust": ActivityTrust,
	}
}

func (v *ActivityEvent) UnmarshalJSON(data []byte) error {
	var dto struct {
		Schema        string              `json:"schema"`
		EventID       string              `json:"event_id"`
		ProjectID     string              `json:"project_id"`
		ScopeID       *string             `json:"scope_id"`
		Source        ActivitySource      `json:"source"`
		SourceEventID string              `json:"source_event_id"`
		SourceRef     *ExternalReference  `json:"source_ref"`
		OccurredAt    *string             `json:"occurred_at"`
		ObservedAt    string              `json:"observed_at"`
		TimeBasis     TimeBasis           `json:"time_basis"`
		Title         *string             `json:"title"`
		Summary       *string             `json:"summary"`
		Agent         *ActivityAgent      `json:"agent"`
		SessionID     *string             `json:"session_id"`
		VCSContext    *ActivityVCSContext `json:"vcs_context"`
		EvidenceRefs  []ExternalReference `json:"evidence_refs"`
		Trust         string              `json:"trust"`
	}
	if err := decodeStrict(data, &dto); err != nil {
		return err
	}
	if dto.Schema != ActivitySchemaVersion || dto.Trust != ActivityTrust {
		return invalidActivity("schema", "has an unsupported value")
	}
	observed, err := time.Parse(time.RFC3339Nano, dto.ObservedAt)
	if err != nil {
		return invalidActivity("observed_at", "must be an RFC 3339 timestamp")
	}
	var occurred *time.Time
	if dto.OccurredAt != nil {
		t, e := time.Parse(time.RFC3339Nano, *dto.OccurredAt)
		if e != nil {
			return invalidActivity("occurred_at", "must be an RFC 3339 timestamp")
		}
		occurred = &t
	}
	value, err := NewActivityEvent(ActivityEventInput{dto.EventID, dto.ProjectID, dto.ScopeID, dto.Source, dto.SourceEventID, dto.SourceRef, occurred, observed, dto.TimeBasis, dto.Title, dto.Summary, dto.Agent, dto.SessionID, dto.VCSContext, dto.EvidenceRefs})
	if err == nil {
		*v = value
	}
	return err
}
