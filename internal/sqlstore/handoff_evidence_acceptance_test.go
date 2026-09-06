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

package sqlstore_test

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/artifact/memory"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestHandoffEvidenceResolverMapsLegacyRawObservationToUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	sources, artifacts := repositories(t)
	ref, err := source.NewRef("worker.legacy", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := source.NewSourceObservation(ref, "1", "fingerprint", nil,
		jsontext.Value(`{"name":"item-1","definition_version":"1","materialization":"captured"}`), nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := observationEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, insertErr := database.SQLDB().ExecContext(ctx, `INSERT INTO pc_sources
        (scope_id, source_type, source_id, payload, journal_position) VALUES (?, ?, ?, ?, ?)`,
		"scope-handoff-raw", ref.Type(), ref.ID(), payload, 1,
	); insertErr != nil {
		t.Fatal(insertErr)
	}
	memoryRepository, err := sqlstore.NewMemoryRepository(database, "scope-handoff-raw", artifacts, nil)
	if err != nil {
		t.Fatal(err)
	}
	memoryService, err := memory.NewService(memoryRepository, memory.ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := sqlstore.NewHandoffEvidenceResolver(database, "scope-handoff-raw", sources, artifacts, memoryService)
	if err != nil {
		t.Fatal(err)
	}
	citation, err := handoff.NewSourceCitation(ref)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Resolve(ctx, citation)
	if _, ok := errors.AsType[*handoff.EvidenceUnavailableError](err); !ok {
		t.Fatalf("legacy raw Handoff resolution = %T %v", err, err)
	}
}
