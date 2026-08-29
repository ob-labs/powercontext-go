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

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/ogen-go/ogen/ogenerrors"
	"github.com/ogen-go/ogen/validate"

	v1 "github.com/ob-labs/powercontext-go/api/v1"
)

var (
	decodedFieldPattern    = regexp.MustCompile(`decode field "([^"]+)"`)
	unexpectedFieldPattern = regexp.MustCompile(`unexpected field "([^"]+)"`)
	integerMinimumPattern  = regexp.MustCompile(`\bvalue (-?[0-9]+) less than (-?[0-9]+)\b`)
	integerMaximumPattern  = regexp.MustCompile(`\bvalue (-?[0-9]+) greater than (-?[0-9]+)\b`)
)

// ErrorMapper maps application/domain errors at the transport boundary. The
// bool is false when the mapper does not recognize an error.
type ErrorMapper func(error) (statusCode int, detail Error, ok bool)

// ErrorHandler returns the error adapter used by the generated server. Decode
// failures are deliberately normalized to FastAPI's 422 contract and raw error
// strings are never returned to clients.
func ErrorHandler(mapper ErrorMapper) v1.ErrorHandler {
	return func(_ context.Context, w http.ResponseWriter, _ *http.Request, err error) {
		if statusCode, detail, ok := mapTransportError(err); ok {
			writeError(w, statusCode, detail)
			return
		}
		if mapper != nil {
			if statusCode, detail, ok := mapper(err); ok {
				writeError(w, statusCode, detail)
				return
			}
		}
		writeError(w, http.StatusInternalServerError, Error{
			Code:    "internal_error",
			Message: "The Server failed.",
		})
	}
}

func mapTransportError(err error) (int, Error, bool) {
	var security *ogenerrors.SecurityError
	if errors.As(err, &security) {
		return http.StatusUnauthorized, Error{
			Code:    "unauthorized",
			Message: "A valid bearer token is required.",
		}, true
	}

	var semantic *requestContractError
	if errors.As(err, &semantic) {
		// The frozen Python boundary currently turns this model-level validation
		// failure into its generic 500 envelope because Pydantic includes a
		// non-JSON-serializable ValueError in the validation context. Preserve the
		// observable contract until the Python Oracle intentionally changes.
		return http.StatusInternalServerError, Error{
			Code:    "internal_error",
			Message: "The Server failed.",
		}, true
	}

	var request *ogenerrors.DecodeRequestError
	var params *ogenerrors.DecodeParamsError
	if errors.As(err, &request) || errors.As(err, &params) {
		return http.StatusUnprocessableEntity, Error{
			Code:    "invalid_request",
			Message: "The request violates the API contract.",
			Details: validationErrorDetails(err),
		}, true
	}
	return 0, Error{}, false
}

// validationErrorDetails translates ogen's decoder and validator tree into
// the stable FastAPI/Pydantic error shape exposed by the Python server. Raw
// inputs and decoder error strings are deliberately excluded: request bodies
// may contain credentials or private context, and the Python boundary removes
// the Pydantic "input" member for the same reason.
func validationErrorDetails(err error) map[string]any {
	location := []any{"body"}
	validationErr := err
	operationID := ""
	var request *ogenerrors.DecodeRequestError
	if errors.As(err, &request) {
		operationID = request.OperationID()
	}
	var params *ogenerrors.DecodeParamsError
	if errors.As(err, &params) {
		operationID = params.OperationID()
	}

	var parameter *ogenerrors.DecodeParamError
	if errors.As(err, &parameter) {
		location = []any{string(parameter.In), parameter.Name}
		validationErr = parameter.Err
	} else {
		var body *ogenerrors.DecodeBodyError
		if errors.As(err, &body) {
			validationErr = body.Err
		} else {
			if errors.As(err, &request) {
				validationErr = request.Err
			}
		}
	}

	issues := collectValidationIssues(validationErr, location, operationID)
	if len(issues) == 0 {
		// Every recognized transport failure must retain the non-null validation
		// envelope. Keep the fallback intentionally generic so implementation
		// details and user input cannot leak through an unexpected error type.
		issues = append(issues, validationIssue(location, "value_error", "Value error"))
	}
	return map[string]any{"errors": issues}
}

func collectValidationIssues(err error, location []any, operationID string) []any {
	if err == nil {
		return nil
	}

	var fields *validate.Error
	if errors.As(err, &fields) {
		issues := make([]any, 0, len(fields.Fields))
		for _, field := range fields.Fields {
			childLocation := appendLocation(location, field.Name)
			issues = append(issues, collectValidationIssues(field.Error, childLocation, operationID)...)
		}
		return issues
	}

	if errors.Is(err, validate.ErrFieldRequired) || errors.Is(err, validate.ErrBodyRequired) {
		return []any{validationIssue(location, "missing", "Field required")}
	}
	var contentType *validate.InvalidContentTypeError
	if errors.As(err, &contentType) && contentType.ContentType == "" {
		return []any{validationIssue(location, "missing", "Field required")}
	}

	var minimumLength *validate.MinLengthError
	if errors.As(err, &minimumLength) {
		if strings.Contains(err.Error(), "string:") {
			unit := "characters"
			if minimumLength.MinLength == 1 {
				unit = "character"
			}
			return []any{validationIssueWithContext(
				location,
				"string_too_short",
				fmt.Sprintf("String should have at least %d %s", minimumLength.MinLength, unit),
				map[string]any{"min_length": minimumLength.MinLength},
			)}
		}
		if strings.Contains(err.Error(), "array:") {
			return []any{validationIssueWithContext(
				location,
				"too_short",
				fmt.Sprintf(
					"List should have at least %d items after validation, not %d",
					minimumLength.MinLength,
					minimumLength.Len,
				),
				map[string]any{
					"field_type": "List", "min_length": minimumLength.MinLength,
					"actual_length": minimumLength.Len,
				},
			)}
		}
	}
	var maximumLength *validate.MaxLengthError
	if errors.As(err, &maximumLength) {
		if strings.Contains(err.Error(), "string:") {
			unit := "characters"
			if maximumLength.MaxLength == 1 {
				unit = "character"
			}
			return []any{validationIssueWithContext(
				location,
				"string_too_long",
				fmt.Sprintf("String should have at most %d %s", maximumLength.MaxLength, unit),
				map[string]any{"max_length": maximumLength.MaxLength},
			)}
		}
		if strings.Contains(err.Error(), "array:") {
			return []any{validationIssueWithContext(
				location,
				"too_long",
				fmt.Sprintf(
					"List should have at most %d items after validation, not %d",
					maximumLength.MaxLength,
					maximumLength.Len,
				),
				map[string]any{
					"field_type": "List", "max_length": maximumLength.MaxLength,
					"actual_length": maximumLength.Len,
				},
			)}
		}
	}
	var pattern *validate.NoRegexMatchError
	if errors.As(err, &pattern) {
		value := pattern.Pattern.String()
		return []any{validationIssueWithContext(
			location,
			"string_pattern_mismatch",
			fmt.Sprintf("String should match pattern '%s'", value),
			map[string]any{"pattern": value},
		)}
	}

	message := err.Error()
	if strings.Contains(message, "mime: no media type") {
		return []any{validationIssue(location, "missing", "Field required")}
	}
	if match := integerMinimumPattern.FindStringSubmatch(message); len(match) == 3 {
		minimum, parseErr := strconv.ParseInt(match[2], 10, 64)
		if parseErr == nil {
			return []any{validationIssueWithContext(
				location,
				"greater_than_equal",
				fmt.Sprintf("Input should be greater than or equal to %d", minimum),
				map[string]any{"ge": minimum},
			)}
		}
	}
	if match := integerMaximumPattern.FindStringSubmatch(message); len(match) == 3 {
		maximum, parseErr := strconv.ParseInt(match[2], 10, 64)
		if parseErr == nil {
			return []any{validationIssueWithContext(
				location,
				"less_than_equal",
				fmt.Sprintf("Input should be less than or equal to %d", maximum),
				map[string]any{"le": maximum},
			)}
		}
	}

	decodedLocation := decodedLocation(message, location)
	if match := unexpectedFieldPattern.FindStringSubmatch(message); len(match) == 2 {
		return []any{validationIssue(
			appendLocation(decodedLocation, match[1]),
			"extra_forbidden",
			"Extra inputs are not permitted",
		)}
	}
	if len(decodedLocation) > len(location) {
		switch {
		case strings.Contains(message, "unexpected floating point character"):
			return []any{validationIssue(decodedLocation, "int_type", "Input should be a valid integer")}
		case strings.Contains(message, `"[" expected`):
			return []any{validationIssue(decodedLocation, "list_type", "Input should be a valid list")}
		case strings.Contains(message, `"{" expected`):
			if modelField(operationID, decodedLocation) {
				return []any{validationIssue(
					decodedLocation,
					"model_attributes_type",
					"Input should be a valid dictionary or object to extract fields from",
				)}
			}
			return []any{validationIssue(decodedLocation, "dict_type", "Input should be a valid dictionary")}
		case booleanField(decodedLocation):
			return []any{validationIssue(decodedLocation, "bool_type", "Input should be a valid boolean")}
		default:
			return []any{validationIssue(decodedLocation, "string_type", "Input should be a valid string")}
		}
	}
	if strings.Contains(message, "query parameter") && strings.Contains(message, "not set") {
		return []any{validationIssue(location, "missing", "Field required")}
	}
	if len(location) == 2 && location[0] == "query" && location[1] == "period" && strings.Contains(message, "invalid value:") {
		const expected = "'today', '7d' or '30d'"
		return []any{validationIssueWithContext(
			location,
			"enum",
			"Input should be "+expected,
			map[string]any{"expected": expected},
		)}
	}
	if operationID == "search_memory" && locationKey(location) == "body.mode" && strings.Contains(message, "invalid value:") {
		const expected = "'auto', 'fts', 'vector' or 'hybrid'"
		return []any{validationIssueWithContext(
			location,
			"enum",
			"Input should be "+expected,
			map[string]any{"expected": expected},
		)}
	}

	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		return collectValidationIssues(unwrapped, location, operationID)
	}
	return nil
}

func booleanField(location []any) bool {
	if len(location) == 0 {
		return false
	}
	field, ok := location[len(location)-1].(string)
	return ok && (strings.HasPrefix(field, "include_") || strings.HasSuffix(field, "_enabled"))
}

func modelField(operationID string, location []any) bool {
	return operationID == "propose_experience" && locationKey(location) == "body.proposal"
}

func locationKey(location []any) string {
	parts := make([]string, 0, len(location))
	for _, component := range location {
		parts = append(parts, fmt.Sprint(component))
	}
	return strings.Join(parts, ".")
}

func decodedLocation(message string, location []any) []any {
	result := append([]any(nil), location...)
	for _, match := range decodedFieldPattern.FindAllStringSubmatch(message, -1) {
		if len(match) == 2 {
			result = append(result, match[1])
		}
	}
	return result
}

func appendLocation(location []any, field string) []any {
	result := make([]any, len(location), len(location)+1)
	copy(result, location)
	return append(result, field)
}

func validationIssue(location []any, issueType, message string) map[string]any {
	return map[string]any{
		"type": issueType,
		"loc":  append([]any(nil), location...),
		"msg":  message,
	}
}

func validationIssueWithContext(
	location []any,
	issueType, message string,
	issueContext map[string]any,
) map[string]any {
	result := validationIssue(location, issueType, message)
	result["ctx"] = issueContext
	return result
}
