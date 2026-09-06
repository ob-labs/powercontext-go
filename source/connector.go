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
	"fmt"
	"strings"
	"unicode/utf8"
)

// ConnectorBinding identifies one durable Connector checkpoint within a Scope.
type ConnectorBinding struct {
	scopeID          string
	bindingID        string
	connectorName    string
	connectorVersion string
}

// InvalidConnectorBindingError reports an invalid Connector identity without
// disclosing the rejected value.
type InvalidConnectorBindingError struct {
	Field  string
	Detail string
}

func (e *InvalidConnectorBindingError) Error() string {
	return fmt.Sprintf("invalid Connector binding %s: %s", e.Field, e.Detail)
}

func NewConnectorBinding(scopeID, bindingID, connectorName, connectorVersion string) (ConnectorBinding, error) {
	binding := ConnectorBinding{
		scopeID:          scopeID,
		bindingID:        bindingID,
		connectorName:    connectorName,
		connectorVersion: connectorVersion,
	}
	if err := binding.Validate(); err != nil {
		return ConnectorBinding{}, err
	}
	return binding, nil
}

func (b ConnectorBinding) ScopeID() string          { return b.scopeID }
func (b ConnectorBinding) ID() string               { return b.bindingID }
func (b ConnectorBinding) ConnectorName() string    { return b.connectorName }
func (b ConnectorBinding) ConnectorVersion() string { return b.connectorVersion }

// Validate rejects zero-value and otherwise invalid Connector identities at
// every boundary that accepts an already constructed binding.
func (b ConnectorBinding) Validate() error {
	for _, field := range []struct {
		name    string
		value   string
		maximum int
	}{
		{name: "scope_id", value: b.scopeID, maximum: MaxIDLength},
		{name: "binding_id", value: b.bindingID, maximum: MaxIDLength},
		{name: "connector_name", value: b.connectorName, maximum: MaxTypeLength},
		{name: "connector_version", value: b.connectorVersion, maximum: MaxTypeLength},
	} {
		if err := connectorIdentity(field.name, field.value, field.maximum); err != nil {
			return err
		}
	}
	return nil
}

func connectorIdentity(field, value string, maximum int) error {
	if !utf8.ValidString(value) {
		return &InvalidConnectorBindingError{Field: field, Detail: "must be valid UTF-8"}
	}
	trimmed := strings.TrimFunc(value, isPythonWhitespace)
	if trimmed == "" {
		return &InvalidConnectorBindingError{Field: field, Detail: "must be a non-empty string"}
	}
	if trimmed != value {
		return &InvalidConnectorBindingError{Field: field, Detail: "must not contain leading or trailing whitespace"}
	}
	if utf8.RuneCountInString(value) > maximum {
		return &InvalidConnectorBindingError{
			Field: field, Detail: fmt.Sprintf("must not exceed %d characters", maximum),
		}
	}
	return nil
}
