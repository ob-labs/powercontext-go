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

package work

import (
	"slices"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/handoff"
	"github.com/ob-labs/powercontext-go/source"
)

type ReceiptStatus string

const (
	ReceiptAccepted           ReceiptStatus = "accepted"
	ReceiptNeedsClarification ReceiptStatus = "needs_clarification"
	ReceiptDeclined           ReceiptStatus = "declined"
)

type LiveStateCheckStatus string

const (
	LiveStateConfirmed  LiveStateCheckStatus = "confirmed"
	LiveStateMismatch   LiveStateCheckStatus = "mismatch"
	LiveStateNotChecked LiveStateCheckStatus = "not_checked"
)

type ReadinessCheckStatus string

const (
	ReadinessConfirmed    ReadinessCheckStatus = "confirmed"
	ReadinessInsufficient ReadinessCheckStatus = "insufficient"
	ReadinessNotChecked   ReadinessCheckStatus = "not_checked"
)

type ReceiverChecks struct {
	liveState     LiveStateCheckStatus
	capability    ReadinessCheckStatus
	authorization ReadinessCheckStatus
}

func NewReceiverChecks(live LiveStateCheckStatus, capability, authorization ReadinessCheckStatus) (ReceiverChecks, error) {
	value := ReceiverChecks{liveState: live, capability: capability, authorization: authorization}
	if err := value.Validate(); err != nil {
		return ReceiverChecks{}, err
	}
	return value, nil
}

func (c ReceiverChecks) LiveState() LiveStateCheckStatus     { return c.liveState }
func (c ReceiverChecks) Capability() ReadinessCheckStatus    { return c.capability }
func (c ReceiverChecks) Authorization() ReadinessCheckStatus { return c.authorization }
func (c ReceiverChecks) AllConfirmed() bool {
	return c.liveState == LiveStateConfirmed && c.capability == ReadinessConfirmed && c.authorization == ReadinessConfirmed
}

func (c ReceiverChecks) Validate() error {
	if c.liveState != LiveStateConfirmed && c.liveState != LiveStateMismatch && c.liveState != LiveStateNotChecked {
		return &InvalidError{Field: "receiver_checks.live_state", Detail: "has an unsupported value"}
	}
	for name, value := range map[string]ReadinessCheckStatus{
		"receiver_checks.capability": c.capability, "receiver_checks.authorization": c.authorization,
	} {
		if value != ReadinessConfirmed && value != ReadinessInsufficient && value != ReadinessNotChecked {
			return &InvalidError{Field: name, Detail: "has an unsupported value"}
		}
	}
	return nil
}

type Acknowledge struct {
	sourceID       string
	receiver       string
	status         ReceiptStatus
	selection      handoff.Selection
	receiverChecks *ReceiverChecks
	prepared       *handoff.Prepared
	revision       *artifact.Ref
	message        *string
}

func NewAcknowledge(
	sourceID, receiver string,
	status ReceiptStatus,
	selection handoff.Selection,
	receiverChecks *ReceiverChecks,
	prepared *handoff.Prepared,
	revision *artifact.Ref,
	message *string,
) (Acknowledge, error) {
	value := Acknowledge{
		sourceID: sourceID, receiver: receiver, status: status, selection: selection,
		receiverChecks: cloneReceiverChecks(receiverChecks), prepared: clonePrepared(prepared),
		revision: cloneArtifactRef(revision), message: cloneString(message),
	}
	if err := value.Validate(); err != nil {
		return Acknowledge{}, err
	}
	return value, nil
}

func (a Acknowledge) SourceID() string                { return a.sourceID }
func (a Acknowledge) Receiver() string                { return a.receiver }
func (a Acknowledge) Status() ReceiptStatus           { return a.status }
func (a Acknowledge) Selection() handoff.Selection    { return a.selection }
func (a Acknowledge) ReceiverChecks() *ReceiverChecks { return cloneReceiverChecks(a.receiverChecks) }
func (a Acknowledge) Prepared() *handoff.Prepared     { return clonePrepared(a.prepared) }
func (a Acknowledge) Revision() *artifact.Ref         { return cloneArtifactRef(a.revision) }
func (a Acknowledge) Message() *string                { return cloneString(a.message) }
func (a Acknowledge) Validate() error {
	if err := validateText("acknowledgement.source_id", a.sourceID, source.MaxIDLength); err != nil {
		return err
	}
	if err := validateText("acknowledgement.receiver", a.receiver, source.MaxIDLength); err != nil {
		return err
	}
	if a.status != ReceiptAccepted && a.status != ReceiptNeedsClarification && a.status != ReceiptDeclined {
		return &InvalidError{Field: "acknowledgement.status", Detail: "has an unsupported value"}
	}
	if a.selection == handoff.PreparedSelection {
		if a.prepared == nil || a.revision != nil {
			return &InvalidError{Field: "acknowledgement.selection", Detail: "does not match its exact input"}
		}
		if err := a.prepared.Validate(); err != nil {
			return err
		}
	} else if a.selection == handoff.ExactSelection {
		if a.prepared != nil || a.revision == nil {
			return &InvalidError{Field: "acknowledgement.selection", Detail: "does not match its exact input"}
		}
		if err := a.revision.Validate(); err != nil {
			return err
		}
	} else {
		return &InvalidError{Field: "acknowledgement.selection", Detail: "must be prepared or exact"}
	}
	if a.receiverChecks != nil {
		if err := a.receiverChecks.Validate(); err != nil {
			return err
		}
	}
	if a.status == ReceiptAccepted && (a.receiverChecks == nil || !a.receiverChecks.AllConfirmed()) {
		return &InvalidError{Field: "acknowledgement.receiver_checks", Detail: "accepted Handoff acknowledgement requires all receiver checks"}
	}
	if a.status != ReceiptAccepted && a.message == nil {
		return &InvalidError{Field: "acknowledgement.message", Detail: "non-accepted Handoff acknowledgement requires a message"}
	}
	if a.message != nil {
		return validateText("acknowledgement.message", *a.message, MaxTextLength)
	}
	return nil
}

type EvidenceStatus string

const (
	EvidenceAvailable   EvidenceStatus = "available"
	EvidenceUnavailable EvidenceStatus = "unavailable"
)

type HandoffReceipt struct {
	receiver            string
	status              ReceiptStatus
	selection           handoff.Selection
	selectedRevision    *artifact.Ref
	preparedDigest      *string
	receiverChecks      *ReceiverChecks
	evidenceStatus      EvidenceStatus
	unavailableEvidence []handoff.Citation
	message             *string
}

func NewHandoffReceipt(
	receiver string,
	status ReceiptStatus,
	selection handoff.Selection,
	selectedRevision *artifact.Ref,
	preparedDigest *string,
	receiverChecks *ReceiverChecks,
	evidenceStatus EvidenceStatus,
	unavailableEvidence []handoff.Citation,
	message *string,
) (HandoffReceipt, error) {
	value := HandoffReceipt{
		receiver: receiver, status: status, selection: selection,
		selectedRevision: cloneArtifactRef(selectedRevision), preparedDigest: cloneString(preparedDigest),
		receiverChecks: cloneReceiverChecks(receiverChecks), evidenceStatus: evidenceStatus,
		unavailableEvidence: slices.Clone(unavailableEvidence), message: cloneString(message),
	}
	if err := value.Validate(); err != nil {
		return HandoffReceipt{}, err
	}
	return value, nil
}

func (r HandoffReceipt) Schema() string                  { return HandoffReceiptSchema }
func (r HandoffReceipt) Trust() string                   { return UntrustedObservation }
func (r HandoffReceipt) Receiver() string                { return r.receiver }
func (r HandoffReceipt) Status() ReceiptStatus           { return r.status }
func (r HandoffReceipt) Selection() handoff.Selection    { return r.selection }
func (r HandoffReceipt) SelectedRevision() *artifact.Ref { return cloneArtifactRef(r.selectedRevision) }
func (r HandoffReceipt) PreparedDigest() *string         { return cloneString(r.preparedDigest) }
func (r HandoffReceipt) ReceiverChecks() *ReceiverChecks {
	return cloneReceiverChecks(r.receiverChecks)
}
func (r HandoffReceipt) EvidenceStatus() EvidenceStatus { return r.evidenceStatus }
func (r HandoffReceipt) UnavailableEvidence() []handoff.Citation {
	return slices.Clone(r.unavailableEvidence)
}
func (r HandoffReceipt) Message() *string { return cloneString(r.message) }
func (r HandoffReceipt) Validate() error {
	if err := validateText("receipt.receiver", r.receiver, source.MaxIDLength); err != nil {
		return err
	}
	if r.status != ReceiptAccepted && r.status != ReceiptNeedsClarification && r.status != ReceiptDeclined {
		return &InvalidError{Field: "receipt.status", Detail: "has an unsupported value"}
	}
	if r.selection == handoff.PreparedSelection {
		if r.selectedRevision != nil || r.preparedDigest == nil {
			return &InvalidError{Field: "receipt.selection", Detail: "must preserve its exact resolved target"}
		}
	} else if r.selection == handoff.ExactSelection {
		if r.selectedRevision == nil || r.preparedDigest != nil {
			return &InvalidError{Field: "receipt.selection", Detail: "must preserve its exact resolved target"}
		}
		if err := r.selectedRevision.Validate(); err != nil {
			return err
		}
	} else {
		return &InvalidError{Field: "receipt.selection", Detail: "must be prepared or exact"}
	}
	if r.preparedDigest != nil {
		if err := validateText("receipt.prepared_digest", *r.preparedDigest, 128); err != nil {
			return err
		}
	}
	if r.receiverChecks != nil {
		if err := r.receiverChecks.Validate(); err != nil {
			return err
		}
	}
	if len(r.unavailableEvidence) > MaxReceiptEvidence {
		return &InvalidError{Field: "receipt.unavailable_evidence", Detail: "contains too many items"}
	}
	if err := validateCitations("receipt.unavailable_evidence", r.unavailableEvidence); err != nil {
		return err
	}
	if r.evidenceStatus == EvidenceAvailable && len(r.unavailableEvidence) != 0 {
		return &InvalidError{Field: "receipt.unavailable_evidence", Detail: "available receipt cannot contain unavailable evidence"}
	}
	if r.evidenceStatus == EvidenceUnavailable && len(r.unavailableEvidence) == 0 {
		return &InvalidError{Field: "receipt.unavailable_evidence", Detail: "unavailable receipt must identify unavailable evidence"}
	}
	if r.evidenceStatus != EvidenceAvailable && r.evidenceStatus != EvidenceUnavailable {
		return &InvalidError{Field: "receipt.evidence_status", Detail: "has an unsupported value"}
	}
	if r.status == ReceiptAccepted && r.evidenceStatus == EvidenceUnavailable {
		return &InvalidError{Field: "receipt.status", Detail: "a Handoff with unavailable evidence cannot be accepted"}
	}
	if r.status == ReceiptAccepted && r.receiverChecks != nil && !r.receiverChecks.AllConfirmed() {
		return &InvalidError{Field: "receipt.receiver_checks", Detail: "accepted Handoff receipt requires all recorded receiver checks"}
	}
	if r.message != nil {
		return validateText("receipt.message", *r.message, MaxTextLength)
	}
	return nil
}
