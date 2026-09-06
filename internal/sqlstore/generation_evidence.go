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
	json "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"unicode/utf8"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/internal/review"
	"github.com/ob-labs/powercontext-go/internal/sourceevidence"
	"github.com/ob-labs/powercontext-go/source"
)

type GenerationEvidenceReader struct {
	database  *Database
	scopeID   string
	sources   *SourceRepository
	artifacts *ArtifactRepository
}

func NewGenerationEvidenceReader(
	database *Database,
	scopeID string,
	sources *SourceRepository,
	artifacts *ArtifactRepository,
) (*GenerationEvidenceReader, error) {
	if database == nil || sources == nil || artifacts == nil {
		return nil, errors.New("sqlstore: generation evidence dependencies must not be nil")
	}
	if err := requireScope(scopeID); err != nil {
		return nil, err
	}
	return &GenerationEvidenceReader{database: database, scopeID: scopeID, sources: sources, artifacts: artifacts}, nil
}

func (r *GenerationEvidenceReader) Read(
	ctx context.Context,
	sourceRefs []source.Ref,
	artifactRefs []artifact.Ref,
) ([]artifact.GenerationEvidence, error) {
	result := make([]artifact.GenerationEvidence, 0, len(sourceRefs)+len(artifactRefs))
	err := r.database.Transaction(ctx, func(tx DBTX) error {
		for _, ref := range sourceRefs {
			stored, err := r.sources.Get(ctx, tx, r.scopeID, ref)
			if err != nil {
				return generationEvidenceError(err)
			}
			if evidenceErr := sourceevidence.Require(stored.Value); evidenceErr != nil {
				return evidenceErr
			}
			payload, err := r.encodeSource(stored.Value)
			if err != nil {
				return err
			}
			value, err := boundedGenerationEvidence(
				"source:"+ref.Type()+"/"+ref.ID(), artifact.SourceEvidence, string(payload),
			)
			if err != nil {
				return err
			}
			result = append(result, value)
		}
		for _, ref := range artifactRefs {
			stored, err := r.artifacts.Get(ctx, tx, r.scopeID, ref)
			if err != nil {
				return generationEvidenceError(err)
			}
			payload, err := r.encodeArtifact(stored)
			if err != nil {
				return err
			}
			value, err := boundedGenerationEvidence(
				fmt.Sprintf("artifact:%s/%s@%d", ref.Family(), ref.ID(), ref.Revision()),
				artifact.ArtifactEvidence,
				string(payload),
			)
			if err != nil {
				return err
			}
			result = append(result, value)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, &review.InvalidCandidateError{Field: "evidence", Detail: "at least one exact reference is required"}
	}
	return result, nil
}

func (r *GenerationEvidenceReader) encodeSource(value source.Value) ([]byte, error) {
	if accepted, ok := value.(sourceevidence.AcceptedObservation); ok {
		evidence, err := accepted.TextEvidence()
		if err != nil {
			return nil, err
		}
		return json.Marshal(evidence)
	}
	codec, ok := r.sources.bySource[sourceType(value)]
	if !ok {
		return nil, &RepositoryNotFoundError{Kind: "source-adapter", Identity: sourceType(value)}
	}
	return codec.encode(value)
}

func (r *GenerationEvidenceReader) encodeArtifact(value artifact.Snapshot) ([]byte, error) {
	ref := value.Ref()
	codec, ok := r.artifacts.byFamily[ref.Family()]
	if !ok {
		return nil, &RepositoryNotFoundError{Kind: "artifact-family", Identity: ref.Family()}
	}
	content, err := codec.encode(value.ContentValue())
	if err != nil {
		return nil, err
	}
	lineage := value.Lineage()
	sources := lineage.Sources()
	sourceValues := make([]sourceRefJSON, len(sources))
	for index, ref := range sources {
		sourceValues[index] = sourceRefJSON{SourceType: ref.Type(), SourceID: ref.ID()}
	}
	artifacts := lineage.Artifacts()
	artifactValues := make([]artifactRefJSON, len(artifacts))
	for index, ref := range artifacts {
		artifactValues[index] = encodeArtifactRef(ref)
	}
	return marshalJSON(generationArtifactJSON{
		ArtifactID: ref.ID(), Revision: ref.Revision(), RawContent: content,
		Lineage: generationLineageJSON{Sources: sourceValues, Artifacts: artifactValues},
	})
}

type generationArtifactJSON struct {
	ArtifactID string                `json:"artifact_id"`
	Revision   int64                 `json:"revision"`
	RawContent jsonRaw               `json:"content"`
	Lineage    generationLineageJSON `json:"lineage"`
}

type generationLineageJSON struct {
	Sources   []sourceRefJSON   `json:"sources"`
	Artifacts []artifactRefJSON `json:"artifacts"`
}

// jsonRaw keeps the already schema-validated content bytes byte-for-byte.
type jsonRaw []byte

func (r jsonRaw) MarshalJSON() ([]byte, error) { return r, nil }

func boundedGenerationEvidence(id string, kind artifact.EvidenceKind, content string) (artifact.GenerationEvidence, error) {
	if !utf8.ValidString(content) {
		return artifact.GenerationEvidence{}, fmt.Errorf("generation evidence is not valid UTF-8")
	}
	runes := []rune(content)
	truncated := len(runes) > artifact.MaxGenerationEvidenceChars
	if truncated {
		content = string(runes[:artifact.MaxGenerationEvidenceChars])
	}
	return artifact.NewGenerationEvidence(id, kind, content, truncated)
}

func generationEvidenceError(err error) error {
	var missing *RepositoryNotFoundError
	if errors.As(err, &missing) {
		return &review.InvalidCandidateError{Field: "evidence", Detail: "reference is not available in this scope"}
	}
	return err
}

func sourceType(value source.Value) reflect.Type { return reflect.TypeOf(value) }
