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
	"html"
	"strings"
	"time"
	"unicode"
)

var markdownLabels = map[Locale]map[string]string{
	LocaleChinese: {
		"title": "PowerContext 项目交接报告", "overview": "项目概览", "blockers": "阻塞事项", "workstreams": "Workstream 状态", "details": "Workstream 详情", "objective": "目标", "progress": "当前进度", "next": "下一步", "omissions": "缺失信息", "activities": "观察到的 Activity", "unassigned_activities": "未分配 Activity", "event": "事件", "schema": "Schema", "event_id": "事件 ID", "project_id": "Project ID", "source": "来源", "source_event_id": "来源事件 ID", "scope": "Scope", "time_basis": "时间依据", "occurred_at": "发生时间", "observed_at": "观察时间", "event_title": "标题", "event_summary": "摘要", "source_ref": "来源引用", "agent": "Agent", "session": "Session", "vcs": "VCS 上下文", "evidence": "证据引用", "evidence_checks": "Evidence 检查", "revision_history": "Handoff Revision 历史", "revision_history_summary": "共 %d 个 Revision，显示最近 %d 个。", "revision_state_count": "状态条目", "revision_omission_count": "缺失条目", "continuity": "连续性时间线", "transfer_state": "交接状态", "outcome_state": "结果状态", "journal_order_notice": "按 Source journal 的稳定位置排序；位置表示先后顺序，不代表时间戳。", "invalid_work_records": "无法读取的 Work 记录", "metadata": "报告元数据", "selection_digest": "Selection Digest", "report_digest": "Report Digest", "report_kind": "报告类型", "period": "报告周期", "period_comparison": "与前一周期对比", "current_activity_count": "本周期 Activity 数", "previous_activity_count": "前一周期 Activity 数", "activity_delta": "Activity 变化", "handoff_boundary_coverage": "Handoff 时间边界覆盖", "format": "格式", "trust": "信任标记", "none": "无", "activity_notice": "Activity Adapter 未配置；此处不能解释为没有活动。",
	},
	LocaleEnglish: {
		"title": "PowerContext Project Handoff Report", "overview": "Project Overview", "blockers": "Blockers", "workstreams": "Workstream Status", "details": "Workstream Details", "objective": "Objective", "progress": "Current Progress", "next": "Next Action", "omissions": "Omissions", "activities": "Observed Activity", "unassigned_activities": "Unassigned Activity", "event": "Event", "schema": "Schema", "event_id": "Event ID", "project_id": "Project ID", "source": "Source", "source_event_id": "Source Event ID", "scope": "Scope", "time_basis": "Time Basis", "occurred_at": "Occurred At", "observed_at": "Observed At", "event_title": "Title", "event_summary": "Summary", "source_ref": "Source Reference", "agent": "Agent", "session": "Session", "vcs": "VCS Context", "evidence": "Evidence References", "evidence_checks": "Evidence Checks", "revision_history": "Handoff Revision History", "revision_history_summary": "%d Revisions total. Showing the latest %d.", "revision_state_count": "State Items", "revision_omission_count": "Omissions", "continuity": "Continuity Timeline", "transfer_state": "Transfer State", "outcome_state": "Outcome State", "journal_order_notice": "Ordered by stable Source journal position; positions show sequence, not timestamps.", "invalid_work_records": "Unreadable Work Records", "metadata": "Report Metadata", "selection_digest": "Selection Digest", "report_digest": "Report Digest", "report_kind": "Report Kind", "period": "Report Period", "period_comparison": "Previous Period Comparison", "current_activity_count": "Current Activity Count", "previous_activity_count": "Previous Activity Count", "activity_delta": "Activity Delta", "handoff_boundary_coverage": "Handoff Boundary Coverage", "format": "Format", "trust": "Trust", "none": "None", "activity_notice": "Activity adapters are not configured; this does not mean that no activity occurred.",
	},
}

func RenderMarkdown(report Report) (string, error) {
	projection := report
	projection.format = FormatMarkdown
	projection.rendererVersion = "markdown-v1"
	var err error
	projection, err = FinalizeDigests(projection)
	if err != nil {
		return "", err
	}
	labels, ok := markdownLabels[projection.locale]
	if !ok {
		return "", fmt.Errorf("unsupported Handoff Report locale %q", projection.locale)
	}
	lines := frontMatterLines(projection)
	lines = append(lines, "---", "", "# "+labels["title"], "", "## "+labels["overview"], "", "- Project: "+markdownText(projection.project.Title()), fmt.Sprintf("- Workstreams: %d", projection.coverage.SelectedWorkstreams), fmt.Sprintf("- Missing Handoff: %d", projection.coverage.MissingHandoffWorkstreams), fmt.Sprintf("- Continuable: %d", projection.summary.ContinuableCount), fmt.Sprintf("- Blocked: %d", projection.summary.BlockedCount), fmt.Sprintf("- Complete: %d", projection.summary.CompleteCount), fmt.Sprintf("- No Handoff: %d", projection.summary.NoHandoffCount))
	if projection.coverage.ActivityCoverage == ActivityNotConfigured {
		lines = append(lines, "- "+markdownText(labels["activity_notice"]), "")
	} else {
		lines = append(lines, "")
	}
	if projection.normalizedPeriod != nil {
		lines = append(lines, "## "+labels["period"], "", "- Start: "+codeSpan(fmt.Sprint(projection.normalizedPeriod["start"])), "- End: "+codeSpan(fmt.Sprint(projection.normalizedPeriod["end"])), "- Timezone: "+codeSpan(fmt.Sprint(projection.normalizedPeriod["timezone"])), "")
	}
	if projection.periodComparison != nil {
		value := projection.periodComparison
		lines = append(lines, "## "+labels["period_comparison"], "", fmt.Sprintf("- %s: %d", labels["current_activity_count"], value.CurrentActivityCount), fmt.Sprintf("- %s: %d", labels["previous_activity_count"], value.PreviousActivityCount), fmt.Sprintf("- %s: %+d", labels["activity_delta"], value.ActivityDelta), "- "+labels["handoff_boundary_coverage"]+": "+codeSpan("unavailable"), "")
	}
	lines = append(lines, "## "+labels["blockers"], "")
	blockers := 0
	for _, item := range projection.workstreams {
		if item.workStatus == WorkBlocked {
			lines = append(lines, "- "+markdownText(item.workstream.Title())+" ("+codeSpan(item.workstream.ScopeID())+"): "+codeSpan(string(item.reportingStatus)))
			blockers++
		}
	}
	if blockers == 0 {
		lines = append(lines, labels["none"], "")
	}
	lines = append(lines, "## "+labels["workstreams"], "")
	for _, item := range projection.workstreams {
		lines = append(lines, "- "+markdownText(item.workstream.Title())+" ("+codeSpan(item.workstream.ScopeID())+"): "+codeSpan(string(item.workStatus))+" / "+codeSpan(string(item.reportingStatus)))
	}
	lines = append(lines, "", "## "+labels["details"], "")
	for _, item := range projection.workstreams {
		lines = append(lines, renderWorkstream(item, labels)...)
	}
	lines = append(lines, "## "+labels["unassigned_activities"], "")
	if len(projection.unassignedActivity) == 0 {
		lines = append(lines, labels["none"], "")
	} else {
		for _, event := range projection.unassignedActivity {
			lines = append(lines, renderActivity(event, labels)...)
		}
	}
	lines = append(lines, "## "+labels["metadata"], "", "- "+labels["selection_digest"]+": "+codeSpan(orNone(projection.selectionDigest, labels["none"])), "- "+labels["report_digest"]+": "+codeSpan(orNone(projection.reportDigest, labels["none"])), "- "+labels["report_kind"]+": "+codeSpan(string(projection.reportKind)), "- "+labels["format"]+": "+codeSpan(string(projection.format)), "- "+labels["trust"]+": "+codeSpan("untrusted_history"))
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n", nil
}

func renderWorkstream(item WorkstreamReport, labels map[string]string) []string {
	lines := []string{"### " + markdownText(item.workstream.Title()) + " (" + codeSpan(item.workstream.ScopeID()) + ")", ""}
	if item.content == nil {
		lines = append(lines, "#### "+labels["objective"], "", labels["none"], "")
	} else {
		content := *item.content
		lines = append(lines, "#### "+labels["objective"], "", markdownText(content.Objective()), "", "#### "+labels["progress"], "")
		for _, statement := range content.State() {
			lines = append(lines, "- "+markdownText(statement.Text()))
		}
		lines = append(lines, "", "#### "+labels["next"], "")
		if next := content.NextAction(); next == nil {
			lines = append(lines, labels["none"])
		} else {
			lines = append(lines, markdownText(next.Text()))
		}
		lines = append(lines, "", "#### "+labels["omissions"], "")
		omissions := content.Omissions()
		if len(omissions) == 0 {
			lines = append(lines, labels["none"])
		} else {
			for _, omission := range omissions {
				lines = append(lines, "- "+markdownText(omission.Text()))
			}
		}
		lines = append(lines, "", "#### "+labels["evidence_checks"], "")
		if !item.evidenceChecked {
			state := "not_checked"
			if item.evidenceUnavailable {
				state += " (adapter unavailable)"
			}
			lines = append(lines, codeSpan(state), "")
		} else if len(item.evidenceChecks) > 0 {
			for _, check := range item.evidenceChecks {
				lines = append(lines, "- "+codeSpan(string(check.Claim()))+": "+codeSpan(string(check.Status())))
			}
			lines = append(lines, "")
		}
	}
	lines = append(lines, renderRevisionHistory(item, labels)...)
	lines = append(lines, renderContinuity(item, labels)...)
	lines = append(lines, "#### "+labels["activities"], "")
	if len(item.activities) == 0 {
		lines = append(lines, labels["none"], "")
	} else {
		for _, event := range item.activities {
			lines = append(lines, renderActivity(event, labels)...)
		}
	}
	return lines
}

func renderRevisionHistory(item WorkstreamReport, labels map[string]string) []string {
	lines := []string{"#### " + labels["revision_history"], ""}
	if len(item.handoffHistory) == 0 {
		return append(lines, labels["none"], "")
	}
	lines = append(lines, markdownText(fmt.Sprintf(
		labels["revision_history_summary"], item.handoffRevisionCount, len(item.handoffHistory),
	)), "")
	for index := len(item.handoffHistory) - 1; index >= 0; index-- {
		revision := item.handoffHistory[index]
		lines = append(lines,
			"- "+codeSpan(fmt.Sprintf("@%d", revision.reference.Revision()))+" "+
				codeSpan(string(revision.disposition))+": "+markdownText(revision.objectiveExcerpt),
			fmt.Sprintf("  - %s: %d; %s: %d", labels["revision_state_count"], revision.stateCount, labels["revision_omission_count"], revision.omissionCount),
		)
		if revision.nextActionExcerpt != nil {
			lines = append(lines, "  - "+labels["next"]+": "+markdownText(*revision.nextActionExcerpt))
		}
	}
	return append(lines, "")
}

func renderContinuity(item WorkstreamReport, labels map[string]string) []string {
	continuity := item.continuity
	coverage := continuity.Coverage()
	lines := []string{
		"#### " + labels["continuity"], "",
		"- " + labels["transfer_state"] + ": " + codeSpan(string(coverage.TransferState())),
		"- " + labels["outcome_state"] + ": " + codeSpan(string(coverage.OutcomeState())),
		fmt.Sprintf("- %s: %d", labels["invalid_work_records"], continuity.InvalidRecordCount()),
		"- " + markdownText(labels["journal_order_notice"]),
	}
	events := continuity.Events()
	if len(events) == 0 {
		lines = append(lines, labels["none"])
	} else {
		for _, event := range events {
			detail := labels["none"]
			if summary := event.Summary(); summary != nil {
				detail = *summary
			} else if actor := event.Actor(); actor != nil {
				detail = *actor
			}
			lines = append(lines,
				"- "+codeSpan(fmt.Sprintf("#%d", event.Position()))+" "+codeSpan(string(event.Kind()))+
					" / "+codeSpan(string(event.Status()))+": "+markdownText(detail),
			)
		}
	}
	return append(lines, "")
}

func renderActivity(event ActivityEvent, labels map[string]string) []string {
	lines := []string{"- **" + labels["event"] + "** " + codeSpan(event.EventID()), "  - " + labels["schema"] + ": " + codeSpan("powercontext.handoff-report-activity.v1"), "  - " + labels["event_id"] + ": " + codeSpan(event.EventID()), "  - " + labels["project_id"] + ": " + codeSpan(event.ProjectID()), "  - " + labels["source"] + ": " + codeSpan(string(event.Source())), "  - " + labels["source_event_id"] + ": " + codeSpan(event.SourceEventID()), "  - " + labels["scope"] + ": " + optionalCode(event.ScopeID(), labels), "  - " + labels["time_basis"] + ": " + codeSpan(string(event.TimeBasis())), "  - " + labels["occurred_at"] + ": " + optionalTimestamp(event.OccurredAt(), labels), "  - " + labels["observed_at"] + ": " + codeSpan(pythonISOTime(event.ObservedAt())), "  - " + labels["event_title"] + ": " + optionalText(event.Title(), labels), "  - " + labels["event_summary"] + ": " + optionalText(event.Summary(), labels), "  - " + labels["source_ref"] + ": " + optionalReference(event.SourceRef(), labels), "  - " + labels["agent"] + ": " + renderAgent(event.Agent(), labels), "  - " + labels["session"] + ": " + optionalCode(event.SessionID(), labels), "  - " + labels["vcs"] + ": " + renderVCS(event.VCSContext(), labels), "  - " + labels["trust"] + ": " + codeSpan("untrusted_observation"), "  - " + labels["evidence"] + ":"}
	refs := event.EvidenceRefs()
	if len(refs) == 0 {
		lines = append(lines, "    - "+labels["none"])
	} else {
		for _, ref := range refs {
			lines = append(lines, "    - "+renderReference(ref))
		}
	}
	return append(lines, "")
}

func frontMatterLines(report Report) []string {
	lines := []string{"---", "schema: powercontext.handoff-report.v1", "locale: " + string(report.locale), "format: markdown", "project_id: " + yamlString(report.project.ProjectID()), "project_key: " + yamlString(report.project.ProjectKey()), fmt.Sprintf("project_version: %d", report.project.Version()), "report_kind: " + string(report.reportKind), "selection_digest: " + yamlString(report.selectionDigest), "report_digest: " + yamlString(report.reportDigest), "generated_at: " + yamlString(pythonISOTime(report.generatedAt)), "trust: untrusted_history", "selection_consistency: optimistic_stable", fmt.Sprintf("activity_cursor: %d", report.activityCursor)}
	if len(report.endSelection) == 0 {
		lines = append(lines, "end_selection: []")
	} else {
		lines = append(lines, "end_selection:")
		for _, entry := range report.endSelection {
			lines = append(lines, "  - scope_id: "+yamlString(entry.ScopeID()), fmt.Sprintf("    workstream_revision: %d", entry.WorkstreamRevision()), "    status: "+string(entry.Status()))
			if ref := entry.HandoffRef(); ref == nil {
				lines = append(lines, "    handoff_ref: null")
			} else {
				lines = append(lines, "    handoff_ref:", "      family: "+yamlString(ref.Family()), "      artifact_id: "+yamlString(ref.ID()), fmt.Sprintf("      revision: %d", ref.Revision()))
			}
		}
	}
	if len(report.activitySelection) == 0 {
		lines = append(lines, "activity_selection: []")
	} else {
		lines = append(lines, "activity_selection:")
		for _, id := range report.activitySelection {
			lines = append(lines, "  - "+yamlString(id))
		}
	}
	return lines
}

func renderAgent(value *ActivityAgent, labels map[string]string) string {
	if value == nil {
		return labels["none"]
	}
	values := []string{}
	if provider := value.Provider(); provider != nil {
		values = append(values, codeSpan(*provider))
	}
	if label := value.Label(); label != nil {
		values = append(values, codeSpan(*label))
	}
	return strings.Join(values, " / ")
}

func renderVCS(value *ActivityVCSContext, labels map[string]string) string {
	if value == nil {
		return labels["none"]
	}
	values := []string{}
	if branch := value.Branch(); branch != nil {
		values = append(values, codeSpan(*branch))
	}
	if head := value.HeadRevision(); head != nil {
		values = append(values, codeSpan(*head))
	}
	return strings.Join(values, " / ")
}

func optionalReference(value *ExternalReference, labels map[string]string) string {
	if value == nil {
		return labels["none"]
	}
	return renderReference(*value)
}

func renderReference(value ExternalReference) string {
	values := []string{codeSpan(string(value.Kind())), codeSpan(value.Provider()), codeSpan(value.ExternalID())}
	if target := value.URL(); target != nil {
		values = append(values, codeSpan(*target))
	}
	return strings.Join(values, " / ")
}

func optionalText(value *string, labels map[string]string) string {
	if value == nil {
		return labels["none"]
	}
	return markdownText(*value)
}

func optionalCode(value *string, labels map[string]string) string {
	if value == nil {
		return labels["none"]
	}
	return codeSpan(*value)
}

func optionalTimestamp(value *time.Time, labels map[string]string) string {
	if value == nil {
		return labels["none"]
	}
	return codeSpan(pythonISOTime(*value))
}

func collapseLines(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	runes := []rune(value)
	for index := 0; index < len(runes); index++ {
		r := runes[index]
		if r == '\r' {
			builder.WriteByte(' ')
			if index+1 < len(runes) && runes[index+1] == '\n' {
				index++
			}
			continue
		}
		if r == '\n' || r == '\v' || r == '\f' || r == '\u0085' || r == '\u2028' || r == '\u2029' || r == '\u001c' || r == '\u001d' || r == '\u001e' {
			builder.WriteByte(' ')
			continue
		}
		if unicode.Is(unicode.Cc, r) {
			builder.WriteByte(' ')
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func markdownText(value string) string {
	value = pythonHTMLEscape(collapseLines(value))
	var builder strings.Builder
	for _, r := range value {
		if strings.ContainsRune("\\`*_{}[]()#+-.!|>~", r) {
			builder.WriteByte('\\')
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func codeSpan(value string) string {
	value = pythonHTMLEscape(collapseLines(value))
	maxRun, current := 0, 0
	for _, r := range value {
		if r == '`' {
			current++
			if current > maxRun {
				maxRun = current
			}
		} else {
			current = 0
		}
	}
	delimiter := strings.Repeat("`", maxRun+1)
	if maxRun > 0 {
		return delimiter + " " + value + " " + delimiter
	}
	return delimiter + value + delimiter
}

func pythonHTMLEscape(value string) string {
	escaped := html.EscapeString(value)
	return strings.ReplaceAll(escaped, "&#39;", "&#x27;")
}

func yamlString(value string) string {
	encoded, err := marshalUnescaped(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

func pythonISOTime(value time.Time) string {
	_, offset := value.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	hours, minutes := offset/3600, (offset%3600)/60
	base := value.Format("2006-01-02T15:04:05")
	if micros := value.Nanosecond() / 1000; micros != 0 {
		base += fmt.Sprintf(".%06d", micros)
	}
	return fmt.Sprintf("%s%s%02d:%02d", base, sign, hours, minutes)
}

func orNone(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
