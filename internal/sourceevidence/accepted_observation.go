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

package sourceevidence

import "github.com/ob-labs/powercontext-go/source"

// AcceptedObservation is the durable counterpart of a worker observation. Its
// fields remain private so external callers cannot construct model-capable
// evidence from source.AdmitObservation alone.
//
// Production construction is intentionally limited to the SQLite source
// sidecar paths after they have re-admitted the observation against the
// persisted Definition and established the acceptance marker in the same
// transaction.
type AcceptedObservation struct {
	observation source.SourceObservation
}

// NewAcceptedObservation constructs the internal durable representation after
// the caller has completed persisted Definition re-admission and marker work.
// It validates the worker payload again so malformed durable data cannot gain
// model capability through a sidecar marker alone.
func NewAcceptedObservation(observation source.SourceObservation) (AcceptedObservation, error) {
	if err := observation.Validate(); err != nil {
		return AcceptedObservation{}, err
	}
	return AcceptedObservation{observation: observation}, nil
}

func (o AcceptedObservation) Ref() source.Ref { return o.observation.Ref() }

func (o AcceptedObservation) Observation() source.SourceObservation { return o.observation }

func (o AcceptedObservation) SourceName() string { return o.observation.SourceName() }

func (o AcceptedObservation) SourceMaterialization() source.Materialization {
	return o.observation.SourceMaterialization()
}

func (o AcceptedObservation) SourceDescription() (string, bool) {
	return o.observation.SourceDescription()
}

func (o AcceptedObservation) TextEvidence() (source.TextEvidence, error) {
	projection, err := o.observation.Projection(source.TextEvidenceProjectionKey())
	if err != nil {
		return source.TextEvidence{}, &source.InvalidTextEvidenceError{
			Field: "projection", Detail: "must be supplied by an accepted observation",
		}
	}
	return source.ParseTextEvidence(projection, o.observation.Ref())
}

func (o AcceptedObservation) Validate() error {
	return o.observation.Validate()
}

var _ source.Value = AcceptedObservation{}
