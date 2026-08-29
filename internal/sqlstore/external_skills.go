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
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/artifact/skill"
)

// ExternalSkillRepository reads and replaces rebuildable host projections.
// The caller controls the transaction so a failed replacement restores the
// preceding snapshot.
type ExternalSkillRepository struct{}

func (ExternalSkillRepository) Replace(
	ctx context.Context,
	db DBTX,
	scopeID string,
	providers []string,
	hostID string,
	registrations []skill.Registration,
) ([]skill.Registration, error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return nil, &InvalidRepositoryArgumentError{Field: "providers", Detail: "must not be empty"}
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		if err := requireRepositoryText("provider", provider, 128); err != nil {
			return nil, err
		}
		if _, duplicate := providerSet[provider]; duplicate {
			return nil, &InvalidRepositoryArgumentError{Field: "providers", Detail: "must be unique"}
		}
		providerSet[provider] = struct{}{}
	}
	if err := requireRepositoryText("host_id", hostID, skill.MaxExternalHostIDLength); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		_, knownProvider := providerSet[registration.Provider()]
		if !knownProvider || registration.HostID() != hostID {
			return nil, &InvalidRepositoryArgumentError{
				Field: "registrations", Detail: "must belong to a replaced provider and host",
			}
		}
		if _, exists := seen[registration.ExternalSkillID()]; exists {
			return nil, &InvalidRepositoryArgumentError{
				Field: "registrations", Detail: "must have unique external Skill identities",
			}
		}
		seen[registration.ExternalSkillID()] = struct{}{}
	}
	for _, provider := range providers {
		if _, err := db.ExecContext(ctx, `DELETE FROM pc_external_skill_registrations
        WHERE scope_id = ? AND provider = ? AND host_id = ?`, scopeID, provider, hostID); err != nil {
			return nil, err
		}
	}
	for _, registration := range registrations {
		locatorHash := fmt.Sprintf("%x", sha256.Sum256([]byte(registration.Locator())))
		_, err := db.ExecContext(ctx, `INSERT INTO pc_external_skill_registrations
            (scope_id, external_skill_id, provider, agent_kind, host_id, installation_scope,
             locator, locator_hash, fingerprint, name, description)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			scopeID, registration.ExternalSkillID(), registration.Provider(), registration.AgentKind(),
			registration.HostID(), string(registration.InstallationScope()), registration.Locator(),
			locatorHash, registration.Fingerprint(), registration.Name(), registration.Description())
		if err != nil {
			return nil, err
		}
	}
	return slices.Clone(registrations), nil
}

func (ExternalSkillRepository) Get(
	ctx context.Context,
	db DBTX,
	scopeID, externalSkillID string,
) (skill.Registration, error) {
	if err := requireScope(scopeID); err != nil {
		return skill.Registration{}, err
	}
	row, err := scanExternalSkill(db.QueryRowContext(ctx, `SELECT
        external_skill_id, provider, agent_kind, host_id, installation_scope,
        locator, fingerprint, name, description
        FROM pc_external_skill_registrations
        WHERE scope_id = ? AND external_skill_id = ?`, scopeID, externalSkillID))
	if errors.Is(err, sql.ErrNoRows) {
		return skill.Registration{}, &RepositoryNotFoundError{
			Kind: "external-skill", Identity: scopeID + "/" + externalSkillID,
		}
	}
	return row, err
}

func (ExternalSkillRepository) List(
	ctx context.Context,
	db DBTX,
	scopeID string,
) (result []skill.Registration, returnErr error) {
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT
        external_skill_id, provider, agent_kind, host_id, installation_scope,
        locator, fingerprint, name, description
        FROM pc_external_skill_registrations WHERE scope_id = ?
        ORDER BY name, external_skill_id`, scopeID)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	result = make([]skill.Registration, 0)
	for rows.Next() {
		registration, err := scanExternalSkill(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, registration)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func scanExternalSkill(row scanner) (skill.Registration, error) {
	var externalSkillID, provider, agentKind, hostID, installationScope string
	var locator, fingerprint, name, description string
	if err := row.Scan(
		&externalSkillID, &provider, &agentKind, &hostID, &installationScope,
		&locator, &fingerprint, &name, &description,
	); err != nil {
		return skill.Registration{}, err
	}
	registration, err := skill.NewRegistration(
		externalSkillID, provider, agentKind, hostID, skill.InstallationScope(installationScope),
		locator, fingerprint, name, description,
	)
	if err != nil {
		return skill.Registration{}, &InvalidStoredPayloadError{
			Kind: "external-skill", Name: externalSkillID, Issue: err.Error(),
		}
	}
	return registration, nil
}

// ExternalSkillStore owns transaction boundaries for the domain Registry.
type ExternalSkillStore struct {
	database   *Database
	scopeID    string
	repository ExternalSkillRepository
}

func NewExternalSkillStore(database *Database, scopeID string) (*ExternalSkillStore, error) {
	if database == nil {
		return nil, errors.New("sqlstore: external Skill database must not be nil")
	}
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	return &ExternalSkillStore{database: database, scopeID: scopeID}, nil
}

func (s *ExternalSkillStore) Replace(
	ctx context.Context,
	providers []string,
	hostID string,
	registrations []skill.Registration,
) ([]skill.Registration, error) {
	var result []skill.Registration
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.repository.Replace(ctx, tx, s.scopeID, providers, hostID, registrations)
		return err
	})
	return result, err
}

func (s *ExternalSkillStore) Get(ctx context.Context, externalSkillID string) (skill.Registration, error) {
	var result skill.Registration
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.repository.Get(ctx, tx, s.scopeID, externalSkillID)
		return err
	})
	if err != nil {
		var missing *RepositoryNotFoundError
		if errors.As(err, &missing) {
			return skill.Registration{}, &skill.ExternalNotFoundError{ExternalSkillID: externalSkillID}
		}
	}
	return result, err
}

func (s *ExternalSkillStore) List(ctx context.Context) ([]skill.Registration, error) {
	var result []skill.Registration
	err := s.database.Transaction(ctx, func(tx DBTX) error {
		var err error
		result, err = s.repository.List(ctx, tx, s.scopeID)
		return err
	})
	return result, err
}

func requireRepositoryText(field, value string, maximum int) error {
	// Round-trip through the domain constructor is intentionally avoided: this
	// validates repository control values without inventing a fake registration.
	if strings.TrimSpace(value) == "" || value != strings.TrimSpace(value) {
		return &InvalidRepositoryArgumentError{Field: field, Detail: "must be a non-empty trimmed string"}
	}
	if utf8.RuneCountInString(value) > maximum {
		return &InvalidRepositoryArgumentError{Field: field, Detail: fmt.Sprintf("must not exceed %d characters", maximum)}
	}
	return nil
}
