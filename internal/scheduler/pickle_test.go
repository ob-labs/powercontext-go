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

package scheduler

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const frozenSchedulerSHA256 = "45aaa07dd69a3180a9bfa8b4f17aa8ff89c9cd3f6212893c6459ada09b2d1753"

func TestDecodeFrozenPythonAPSchedulerSidecar(t *testing.T) {
	path := filepath.Join("..", "..", "test", "conformance", "testdata", "python-v0.0.2", "scheduler.db")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(contents)); got != frozenSchedulerSHA256 {
		t.Fatalf("scheduler fixture SHA-256 = %s, want %s", got, frozenSchedulerSHA256)
	}
	rows := storedRows(t, path)
	if len(rows) != 2 {
		t.Fatalf("stored jobs = %d, want 2", len(rows))
	}
	want := map[JobKind]struct {
		interval time.Duration
		start    time.Time
	}{
		SourceWindow: {
			interval: time.Hour,
			start:    time.Date(2026, 8, 17, 1, 2, 3, 456_789_000, time.UTC),
		},
		ExperienceIncubation: {
			interval: 2 * time.Hour,
			start:    time.Date(2026, 8, 17, 2, 3, 4, 567_890_000, time.UTC),
		},
	}
	for _, row := range rows {
		kind, ok := kindForID(row.id)
		if !ok {
			t.Fatalf("unknown Job ID %q", row.id)
		}
		job, err := decodeJobState(row.blob, row.id, "/powercontext-fixtures/scheduler.db", row.next)
		if err != nil {
			t.Fatal(err)
		}
		expected := want[kind]
		if job.Interval() != expected.interval || job.StartDate() != expected.start || job.NextRunTime() != expected.start {
			t.Fatalf("decoded %s = %#v", kind, job)
		}
	}
}

const pythonSourceWindowPickle = "gAWVQAIAAAAAAAB9lCiMB3ZlcnNpb26USwGMAmlklIwkcG93ZXJjb250ZXh0Lm1lbW9yeS5zb3VyY2Utd2luZG93LnYxlIwEZnVuY5SMPnBvd2VyY29udGV4dC5idWlsdGluLnJ1bnRpbWUuc2NoZWR1bGVyOmRpc3BhdGNoX3NvdXJjZV93aW5kb3dzlIwHdHJpZ2dlcpSMHWFwc2NoZWR1bGVyLnRyaWdnZXJzLmludGVydmFslIwPSW50ZXJ2YWxUcmlnZ2VylJOUKYGUfZQoaAFLAowIdGltZXpvbmWUjAhkYXRldGltZZSMCHRpbWV6b25llJOUaA2MCXRpbWVkZWx0YZSTlEsASwBLAIeUUpSFlFKUjApzdGFydF9kYXRllGgNjAhkYXRldGltZZSTlEMKB+oIEBMGHQBJM5RoFYaUUpSMCGVuZF9kYXRllE6MCGludGVydmFslGgRSwBNEA5LAIeUUpSMBmppdHRlcpROdWKMCGV4ZWN1dG9ylIwHZGVmYXVsdJSMBGFyZ3OUjC0vcHJpdmF0ZS90bXAvcG93ZXJjb250ZXh0LXNjaGVkdWxlci1vcmFjbGUuZGKUhZSMBmt3YXJnc5R9lIwEbmFtZZSMF2Rpc3BhdGNoX3NvdXJjZV93aW5kb3dzlIwSbWlzZmlyZV9ncmFjZV90aW1llE6MCGNvYWxlc2NllIiMDW1heF9pbnN0YW5jZXOUSwGMDW5leHRfcnVuX3RpbWWUaBhDCgfqCBATBh0ASTOUaBWGlFKUdS4="

func TestDecodeFrozenPythonAPSchedulerJob(t *testing.T) {
	t.Parallel()
	blob, err := base64.StdEncoding.DecodeString(pythonSourceWindowPickle)
	if err != nil {
		t.Fatal(err)
	}
	path := "/private/tmp/powercontext-scheduler-oracle.db"
	job, err := decodeJobState(blob, SourceWindowJobID, path, 1786907189.018739)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, time.August, 16, 19, 6, 29, 18_739_000, time.UTC)
	if job.Kind() != SourceWindow || job.Interval() != time.Hour || job.StartDate() != want || job.NextRunTime() != want {
		t.Fatalf("decoded Job differs: %#v", job)
	}
}

func TestGoProtocolFiveWriterRoundTripsThroughRestrictedReader(t *testing.T) {
	t.Parallel()
	path := "/tmp/scheduler.db"
	start := time.Date(2026, 8, 17, 1, 2, 3, 456_789_000, time.UTC)
	job, err := NewJob(ExperienceIncubation, path, 1500*time.Millisecond, start, start.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	blob, err := encodeJobState(job)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) < 3 || !bytes.Equal(blob[:2], []byte{0x80, 5}) {
		t.Fatalf("writer did not use protocol 5: %x", blob[:min(3, len(blob))])
	}
	decoded, err := decodeJobState(blob, job.ID(), path, unixTimestamp(job.NextRunTime()))
	if err != nil {
		t.Fatal(err)
	}
	if decoded != job {
		t.Fatalf("round trip differs\n got: %#v\nwant: %#v", decoded, job)
	}
}

func TestRestrictedReaderRejectsArbitraryReduceAndPreservesTypedError(t *testing.T) {
	t.Parallel()
	// PROTO 5; "posix", "system"; STACK_GLOBAL; empty tuple; REDUCE; STOP.
	malicious := []byte{0x80, 5, 0x8c, 5, 'p', 'o', 's', 'i', 'x', 0x8c, 6, 's', 'y', 's', 't', 'e', 'm', 0x93, ')', 'R', '.'}
	_, err := parsePickle(malicious)
	var invalid *InvalidJobStateError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected typed invalid state, got %v", err)
	}
}

func TestReaderRejectsColumnMismatchAndOversizedBlob(t *testing.T) {
	t.Parallel()
	blob, _ := base64.StdEncoding.DecodeString(pythonSourceWindowPickle)
	_, err := decodeJobState(blob, SourceWindowJobID, "/private/tmp/powercontext-scheduler-oracle.db", 1)
	var invalid *InvalidJobStateError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected timestamp mismatch, got %v", err)
	}
	if _, err := parsePickle(make([]byte, maxPickleBytes+1)); !errors.As(err, &invalid) {
		t.Fatalf("expected size rejection, got %v", err)
	}
}

func TestRestrictedReaderRejectsStaleMarksBeforeSlicingStack(t *testing.T) {
	t.Parallel()
	for _, operation := range []struct {
		name string
		run  func(*pickleParser) error
	}{
		{name: "tuple", run: (*pickleParser).markTuple},
		{name: "set items", run: (*pickleParser).setItems},
	} {
		t.Run(operation.name, func(t *testing.T) {
			parser := &pickleParser{stack: make([]any, 9), marks: []int{10}}
			err := operation.run(parser)
			var invalid *InvalidJobStateError
			if !errors.As(err, &invalid) {
				t.Fatalf("stale mark error = %T %v", err, err)
			}
		})
	}
}
