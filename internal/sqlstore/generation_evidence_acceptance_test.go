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
	"encoding/json/jsontext"
	"errors"
	"strings"
	"testing"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/skill"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
	"github.com/ob-labs/powercontext-go/source"
)

func TestGenerationEvidenceReaderProjectsAcceptedObservationTextEvidence(t *testing.T) {
	ctx := t.Context()
	database := openTestDatabase(t)
	sources, artifacts := repositories(t)
	manifest, raw := generationObservation(t, "remote.generation", "item-1", "accepted text")
	accepted, err := source.AdmitObservation(manifest, raw)
	if err != nil {
		t.Fatal(err)
	}
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		if _, registerErr := (sqlstore.DefinitionManifestRepository{}).Register(ctx, tx, manifest); registerErr != nil {
			return registerErr
		}
		_, addErr := sources.AddAccepted(ctx, tx, "scope-generation", accepted)
		return addErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	reader, err := sqlstore.NewGenerationEvidenceReader(database, "scope-generation", sources, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := reader.Read(ctx, []source.Ref{raw.Ref()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Kind != artifact.SourceEvidence || evidence[0].Truncated {
		t.Fatalf("evidence = %#v", evidence)
	}
	want := `{"source_type":"remote.generation","source_id":"item-1","content":"accepted text","metadata":{"visible":true}}`
	if evidence[0].Content != want {
		t.Fatalf("accepted evidence content = %s, want %s", evidence[0].Content, want)
	}
	if strings.Contains(evidence[0].Content, "private-observation-payload") {
		t.Fatalf("accepted evidence exposed raw observation payload: %s", evidence[0].Content)
	}
}

func TestGenerationEvidenceReaderRejectsRawObservationWithoutDisclosure(t *testing.T) {
	ctx := t.Context()
	database := openTestDatabase(t)
	sources, artifacts := repositories(t)
	_, raw := generationObservation(t, "remote.raw-secret", "raw-id-secret", "not accepted")
	payload, err := observationEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, insertErr := database.SQLDB().ExecContext(ctx, `INSERT INTO pc_sources
        (scope_id, source_type, source_id, payload, journal_position) VALUES (?, ?, ?, ?, ?)`,
		"scope-raw-secret", raw.Ref().Type(), raw.Ref().ID(), payload, 1,
	); insertErr != nil {
		t.Fatal(insertErr)
	}
	reader, err := sqlstore.NewGenerationEvidenceReader(database, "scope-raw-secret", sources, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.Read(ctx, []source.Ref{raw.Ref()}, nil)
	if _, ok := errors.AsType[*source.UnacceptedObservationError](err); !ok {
		t.Fatalf("Read(raw observation) error = %T %v", err, err)
	}
	for _, secret := range []string{"scope-raw-secret", "remote.raw-secret", "raw-id-secret", "private-observation-payload"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("raw rejection exposed %q: %v", secret, err)
		}
	}
}

func TestGenerationEvidenceReaderPreservesSnapshotNativePayload(t *testing.T) {
	ctx := t.Context()
	database := openTestDatabase(t)
	sources, err := sqlstore.NewSourceRepository(
		sqlstore.SQLiteDialect,
		sqlstore.ContentSourceCodec(),
		sqlstore.ExternalSkillSnapshotSourceCodec(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, artifacts := repositories(t)
	registration, err := skill.NewRegistration(
		"codex:project:repository/generation-skill", "codex", "codex", "workstation-1",
		skill.ProjectScope, "/workspace/.agents/skills/generation-skill", strings.Repeat("a", 64),
		"generation-skill", "Generation evidence snapshot.",
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := skill.NewSnapshot(registration, "---\nname: generation-skill\n---\n\nKeep native bytes.\n")
	if err != nil {
		t.Fatal(err)
	}
	value, err := (skill.SnapshotSourceAdapter{}).Resolve(ctx, skill.SnapshotCapture{
		Snapshot: snapshot, Mode: skill.ImportModeImport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transactionErr := database.Transaction(ctx, func(tx sqlstore.DBTX) error {
		_, addErr := sources.Add(ctx, tx, "scope-snapshot", value)
		return addErr
	}); transactionErr != nil {
		t.Fatal(transactionErr)
	}
	ref, err := sources.Ref(value)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := sqlstore.NewGenerationEvidenceReader(database, "scope-snapshot", sources, artifacts)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := reader.Read(ctx, []source.Ref{ref}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Kind != artifact.SourceEvidence || evidence[0].Truncated {
		t.Fatalf("evidence = %#v", evidence)
	}
	for _, fragment := range []string{
		`"materialization":"captured"`,
		`"manifest":"---\nname: generation-skill\n---\n\nKeep native bytes.\n"`,
		`"mode":"import"`,
	} {
		if !strings.Contains(evidence[0].Content, fragment) {
			t.Fatalf("snapshot evidence missing %s: %s", fragment, evidence[0].Content)
		}
	}
}

func generationObservation(
	t *testing.T,
	sourceType, sourceID, content string,
) (source.DefinitionManifest, source.SourceObservation) {
	t.Helper()
	identity, err := source.NewDefinitionIdentity(sourceType, "1")
	if err != nil {
		t.Fatal(err)
	}
	projectionManifest, err := source.NewProjectionManifest(
		source.TextEvidenceProjectionKey(), source.TextEvidenceSchema(),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := source.NewDefinitionManifest(
		identity, jsontext.Value(`{"type":"object"}`), []source.ProjectionManifest{projectionManifest},
	)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := source.NewRef(sourceType, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := source.NewSourceProjectionValue(
		source.TextEvidenceProjectionKey(),
		jsontext.Value(`{"source_type":"`+sourceType+`","source_id":"`+sourceID+`","content":"`+content+`","metadata":{"visible":true}}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	observation, err := source.NewSourceObservation(
		ref, manifest.Version(), manifest.Fingerprint(), nil,
		jsontext.Value(`{"name":"`+sourceID+`","definition_version":"1","materialization":"captured","private":"private-observation-payload"}`),
		[]source.SourceProjectionValue{projection},
	)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, observation
}
