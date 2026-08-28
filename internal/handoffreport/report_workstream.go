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
	"fmt"
	"slices"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/internal/work"
)

type WorkstreamReport struct {
	workstream              WorkstreamDescriptor
	continuity              work.Continuity
	handoffRef              *artifact.Ref
	content                 *handoff.Content
	handoffRevisionCount    int
	handoffHistoryTruncated bool
	handoffHistory          []HandoffRevisionSummary
	evidenceChecks          []handoff.EvidenceCheck
	evidenceChecked         bool
	evidenceUnavailable     bool
	activities              []ActivityEvent
	workStatus              WorkStatus
	reportingStatus         ReportingStatus
	activityStatus          ActivityStatus
	handoffActivityRelation *HandoffActivityRelation
	observedActivityCount   int
}

func (v WorkstreamReport) Workstream() WorkstreamDescriptor { return v.workstream }
func (v WorkstreamReport) Continuity() work.Continuity      { return v.continuity }
func (v WorkstreamReport) HandoffRef() *artifact.Ref        { return cloneArtifactRef(v.handoffRef) }

func (v WorkstreamReport) Content() *handoff.Content {
	if v.content == nil {
		return nil
	}
	copy := *v.content
	return &copy
}
func (v WorkstreamReport) HandoffRevisionCount() int     { return v.handoffRevisionCount }
func (v WorkstreamReport) HandoffHistoryTruncated() bool { return v.handoffHistoryTruncated }
func (v WorkstreamReport) HandoffHistory() []HandoffRevisionSummary {
	return slices.Clone(v.handoffHistory)
}

func (v WorkstreamReport) EvidenceChecks() ([]handoff.EvidenceCheck, bool) {
	if !v.evidenceChecked {
		return nil, false
	}
	return cloneEvidenceChecks(v.evidenceChecks), true
}
func (v WorkstreamReport) EvidenceUnavailable() bool        { return v.evidenceUnavailable }
func (v WorkstreamReport) Activities() []ActivityEvent      { return slices.Clone(v.activities) }
func (v WorkstreamReport) WorkStatus() WorkStatus           { return v.workStatus }
func (v WorkstreamReport) ReportingStatus() ReportingStatus { return v.reportingStatus }
func (v WorkstreamReport) ActivityStatus() ActivityStatus   { return v.activityStatus }
func (v WorkstreamReport) HandoffActivityRelation() *HandoffActivityRelation {
	if v.handoffActivityRelation == nil {
		return nil
	}
	copy := *v.handoffActivityRelation
	return &copy
}
func (v WorkstreamReport) ObservedActivityCount() int { return v.observedActivityCount }

func (v WorkstreamReport) Validate() error {
	if err := v.workstream.Validate(); err != nil {
		return err
	}
	if err := v.continuity.Validate(); err != nil {
		return err
	}
	if v.continuity.ScopeID() != v.workstream.ScopeID() {
		return fieldError("continuity.scope_id", "must match its Workstream")
	}
	if v.handoffRevisionCount < 0 || len(v.handoffHistory) > MaxReportHandoffHistory {
		return fieldError("handoff_history", "contains invalid Revision counts")
	}
	for _, summary := range v.handoffHistory {
		if err := summary.Validate(); err != nil {
			return err
		}
	}
	if len(v.activities) > MaxReportActivities {
		return fieldError("activities", fmt.Sprintf("must not exceed %d items", MaxReportActivities))
	}
	for _, event := range v.activities {
		if err := event.Validate(); err != nil {
			return err
		}
	}
	if v.observedActivityCount < 0 || v.observedActivityCount != len(v.activities) {
		return fieldError("observed_activity_count", "must match the Workstream activity count")
	}
	if !validWorkStatus(v.workStatus) || !validReportingStatus(v.reportingStatus) ||
		!validActivityStatus(v.activityStatus) || !validActivityRelation(v.handoffActivityRelation) {
		return fieldError("workstream_report", "contains an unsupported status")
	}
	if v.handoffRef == nil {
		if v.content != nil {
			return fieldError("content", "a Workstream without Handoff cannot contain Handoff content")
		}
		if v.evidenceChecked || len(v.evidenceChecks) != 0 {
			return fieldError("evidence_checks", "a Workstream without Handoff cannot contain evidence checks")
		}
		if v.evidenceUnavailable {
			return fieldError("evidence_unavailable", "a Workstream without Handoff cannot have unavailable evidence checks")
		}
		if v.workStatus != WorkNoHandoff || v.reportingStatus != ReportingNoHandoff {
			return fieldError("work_status", "a Workstream without Handoff must report no_handoff state")
		}
		if v.handoffRevisionCount != 0 || len(v.handoffHistory) != 0 || v.handoffHistoryTruncated {
			return fieldError("handoff_history", "a Workstream without Handoff cannot contain Handoff Revision history")
		}
		return nil
	}
	if err := v.handoffRef.Validate(); err != nil || v.handoffRef.Family() != handoff.Family {
		return fieldError("handoff_ref", "must be an exact Handoff reference")
	}
	if v.content == nil {
		return fieldError("content", "an exact Handoff selection must contain Handoff content")
	}
	if err := v.content.Validate(); err != nil {
		return err
	}
	if !v.evidenceChecked && len(v.evidenceChecks) != 0 {
		return fieldError("evidence_checks", "not_checked evidence cannot contain checks")
	}
	if v.evidenceUnavailable && v.evidenceChecked {
		return fieldError("evidence_checks", "an unavailable evidence check must remain not_checked")
	}
	if v.evidenceChecked {
		for _, check := range v.evidenceChecks {
			if err := check.Validate(); err != nil {
				return err
			}
		}
	}
	if v.workStatus != WorkStatus(v.content.Disposition()) {
		return fieldError("work_status", "must match the exact Handoff disposition")
	}
	if v.reportingStatus == ReportingNoHandoff {
		return fieldError("reporting_status", "an exact Handoff selection cannot report missing Handoff state")
	}
	if len(v.handoffHistory) == 0 || v.handoffHistory[len(v.handoffHistory)-1].reference != *v.handoffRef {
		return fieldError("handoff_history", "Handoff reference history must end at the exact selected Handoff")
	}
	if v.handoffRevisionCount < len(v.handoffHistory) || v.handoffHistoryTruncated != (v.handoffRevisionCount > len(v.handoffHistory)) {
		return fieldError("handoff_history", "Revision count and truncation must match the projected history")
	}
	var previous int64
	for _, summary := range v.handoffHistory {
		ref := summary.reference
		if ref.Family() != v.handoffRef.Family() || ref.ID() != v.handoffRef.ID() {
			return fieldError("handoff_history", "must belong to the selected Artifact lifecycle")
		}
		if ref.Revision() <= previous {
			return fieldError("handoff_history", "must be unique and ascending")
		}
		previous = ref.Revision()
	}
	return nil
}
