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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"time"

	webpkijcs "github.com/gowebpki/jcs"
	"golang.org/x/text/unicode/norm"
)

func CanonicalJSONBytes(value any) ([]byte, error) {
	normalized, err := normalizeCanonical(value)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	result, err := webpkijcs.Transform(encoded)
	if err != nil {
		return nil, fmt.Errorf("canonicalize report JSON: %w", err)
	}
	return result, nil
}

func SelectionEnvelope(report Report) (map[string]any, error) {
	events := map[string]ActivityEvent{}
	for _, item := range report.workstreams {
		for _, event := range item.activities {
			events[event.EventID()] = event
		}
	}
	for _, event := range report.unassignedActivity {
		events[event.EventID()] = event
	}
	activity := make([]any, 0, len(report.activitySelection))
	for _, id := range report.activitySelection {
		event, ok := events[id]
		if !ok {
			return nil, &CanonicalizationError{Code: "unknown-event", Detail: id}
		}
		activity = append(activity, map[string]any{"event_id": event.EventID(), "source": event.Source(), "source_event_id": event.SourceEventID(), "occurred_at": canonicalTimePtr(event.OccurredAt()), "observed_at": UTCText(event.ObservedAt()), "time_basis": event.TimeBasis()})
	}
	end := make([]any, len(report.endSelection))
	for i, item := range report.endSelection {
		end[i] = selectionObject(item)
	}
	var baseline any
	if report.baselinePresent {
		values := make([]any, len(report.baselineSelection))
		for i, item := range report.baselineSelection {
			values[i] = selectionObject(item)
		}
		baseline = values
	}
	return map[string]any{"schema": "powercontext.handoff-report-selection.v1", "project_id": report.project.ProjectID(), "project_revision": report.project.Version(), "normalized_filters": report.normalizedFilters, "normalized_period": report.normalizedPeriod, "selection_consistency": "optimistic_stable", "activity_cursor": report.activityCursor, "baseline_selection": baseline, "end_selection": end, "activity_selection": activity}, nil
}

func SelectionDigest(report Report) (string, error) {
	value, err := SelectionEnvelope(report)
	if err != nil {
		return "", err
	}
	return digest(value)
}

func ReportDigest(report Report) (string, error) {
	value, err := report.object(false, true)
	if err != nil {
		return "", err
	}
	return digest(value)
}

func FinalizeDigests(report Report) (Report, error) {
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	selection, err := SelectionDigest(report)
	if err != nil {
		return Report{}, err
	}
	report.selectionDigest = selection
	output, err := ReportDigest(report)
	if err != nil {
		return Report{}, err
	}
	report.reportDigest = output
	if err := report.Validate(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func digest(value any) (string, error) {
	encoded, err := CanonicalJSONBytes(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalizeCanonical(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	reflected := reflect.ValueOf(value)
	// Interfaces containing a typed nil pointer are not themselves nil. Check
	// them before invoking json.Marshaler: a value-receiver MarshalJSON method
	// promoted to a nil pointer would otherwise panic instead of encoding null.
	if (reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer ||
		reflected.Kind() == reflect.Map || reflected.Kind() == reflect.Slice) && reflected.IsNil() {
		return nil, nil
	}
	if timestamp, ok := value.(time.Time); ok {
		return UTCText(timestamp), nil
	}
	if marshaler, ok := value.(json.Marshaler); ok {
		encoded, err := marshaler.MarshalJSON()
		if err != nil {
			return nil, err
		}
		decoded, err := DecodeJSONValue(encoded)
		if err != nil {
			return nil, err
		}
		return normalizeCanonical(decoded)
	}
	if number, ok := value.(json.Number); ok {
		if stringsContainsFloat(number.String()) {
			return nil, &CanonicalizationError{Code: "float"}
		}
		parsed, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil {
			return nil, err
		}
		return parsed, nil
	}
	for reflected.Kind() == reflect.Interface || reflected.Kind() == reflect.Pointer {
		if reflected.IsNil() {
			return nil, nil
		}
		reflected = reflected.Elem()
	}
	// A typed nil map or slice stored in an interface is not equal to nil.  It
	// still represents JSON null, matching Python's None and encoding/json.
	if (reflected.Kind() == reflect.Map || reflected.Kind() == reflect.Slice) && reflected.IsNil() {
		return nil, nil
	}
	if timestamp, ok := reflected.Interface().(time.Time); ok {
		return UTCText(timestamp), nil
	}
	switch reflected.Kind() {
	case reflect.Bool:
		return reflected.Bool(), nil
	case reflect.String:
		return norm.NFC.String(reflected.String()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflected.Uint(), nil
	case reflect.Float32, reflect.Float64:
		_ = math.IsNaN(reflected.Float())
		return nil, &CanonicalizationError{Code: "float"}
	case reflect.Map:
		if reflected.Type().Key().Kind() != reflect.String {
			return nil, &CanonicalizationError{Code: "key-type"}
		}
		result := map[string]any{}
		iter := reflected.MapRange()
		for iter.Next() {
			key := norm.NFC.String(iter.Key().String())
			if _, exists := result[key]; exists {
				return nil, &CanonicalizationError{Code: "key-collision", Detail: key}
			}
			item, err := normalizeCanonical(iter.Value().Interface())
			if err != nil {
				return nil, err
			}
			result[key] = item
		}
		return result, nil
	case reflect.Array, reflect.Slice:
		if reflected.Type().Elem().Kind() == reflect.Uint8 {
			return nil, &CanonicalizationError{Code: "unsupported-type", Detail: reflected.Type().String()}
		}
		result := make([]any, reflected.Len())
		for i := range reflected.Len() {
			item, err := normalizeCanonical(reflected.Index(i).Interface())
			if err != nil {
				return nil, err
			}
			result[i] = item
		}
		return result, nil
	default:
		return nil, &CanonicalizationError{Code: "unsupported-type", Detail: reflected.Type().String()}
	}
}

func stringsContainsFloat(value string) bool {
	for _, marker := range []string{".", "e", "E"} {
		if slices.Contains([]byte(value), marker[0]) {
			return true
		}
	}
	return false
}

func canonicalTimePtr(value *time.Time) any {
	if value == nil {
		return nil
	}
	return UTCText(*value)
}

func cloneJSONMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	decoded, err := DecodeJSONValue(encoded)
	if err != nil {
		return nil
	}
	result, _ := decoded.(map[string]any)
	return result
}
