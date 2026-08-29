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
	"errors"
	"path/filepath"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/mattn/go-sqlite3"

	"github.com/ob-labs/powercontext-go/artifact"
	"github.com/ob-labs/powercontext-go/artifact/experience"
)

func TestArtifactInitialCreateIsAtomicAcrossConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "artifact-race.db")
	first, err := OpenSQLite(ctx, DefaultSQLiteConfig(path))
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenSQLite(ctx, DefaultSQLiteConfig(path))
	if err != nil {
		_ = first.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = first.Close(context.Background())
		_ = second.Close(context.Background())
	})

	repository, err := NewArtifactRepository(SQLiteDialect, ExperienceArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	content, err := experience.NewContent("s", "a", "o", "l")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := experience.NewDraft(content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	type outcome struct {
		created artifact.Snapshot
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, 2)
	for _, database := range []*Database{first, second} {
		go func() {
			<-start
			var created artifact.Snapshot
			err := database.Transaction(ctx, func(tx DBTX) error {
				var createErr error
				created, createErr = repository.Create(ctx, tx, "scope", "experience-1", draft)
				return createErr
			})
			results <- outcome{created: created, err: err}
		}()
	}
	close(start)

	created, conflicts := 0, 0
	for range 2 {
		result := <-results
		if result.err == nil {
			created++
			if result.created == nil || result.created.Ref().Revision() != 1 {
				t.Fatalf("created Artifact = %#v", result.created)
			}
			continue
		}
		var conflict *artifact.RevisionConflictError
		if !errors.As(result.err, &conflict) || conflict.Requested.Revision() != 1 || conflict.Current.Revision() != 1 {
			t.Fatalf("losing create error = %v", result.err)
		}
		conflicts++
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("created = %d, conflicts = %d", created, conflicts)
	}
}

func TestArtifactCreateIntegrityNormalizesOnlyCommittedLifecycle(t *testing.T) {
	ctx := context.Background()
	database, err := OpenSQLite(ctx, DefaultSQLiteConfig(":memory:"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := database.Close(context.Background()); closeErr != nil {
			t.Errorf("close database: %v", closeErr)
		}
	})
	repository, err := NewArtifactRepository(SQLiteDialect, ExperienceArtifactCodec())
	if err != nil {
		t.Fatal(err)
	}
	content, err := experience.NewContent("s", "a", "o", "l")
	if err != nil {
		t.Fatal(err)
	}
	draft, err := experience.NewDraft(content, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var created artifact.Snapshot
	if err := database.Transaction(ctx, func(tx DBTX) error {
		var createErr error
		created, createErr = repository.Create(ctx, tx, "scope", "experience-1", draft)
		return createErr
	}); err != nil {
		t.Fatal(err)
	}
	constraint := sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintUnique}
	if err := database.Transaction(ctx, func(tx DBTX) error {
		normalized := repository.normalizeCreateIntegrity(ctx, tx, "scope", created.Ref(), constraint)
		var conflict *artifact.RevisionConflictError
		if !errors.As(normalized, &conflict) || conflict.Current != created.Ref() {
			t.Fatalf("normalized error = %#v", normalized)
		}
		missing, refErr := artifact.NewRef(experience.Family, "missing", 1)
		if refErr != nil {
			return refErr
		}
		if got := repository.normalizeCreateIntegrity(ctx, tx, "scope", missing, constraint); !errors.Is(got, constraint) {
			t.Fatalf("unrelated constraint = %#v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrityConstraintClassification(t *testing.T) {
	for _, value := range []error{
		sqlite3.Error{Code: sqlite3.ErrConstraint, ExtendedCode: sqlite3.ErrConstraintPrimaryKey},
		&mysql.MySQLError{Number: 1062},
		&mysql.MySQLError{Number: 1452},
	} {
		if !isIntegrityConstraint(value) {
			t.Errorf("constraint %T(%v) was not classified", value, value)
		}
	}
	for _, value := range []error{errors.New("plain"), sqlite3.Error{Code: sqlite3.ErrBusy}, &mysql.MySQLError{Number: 2013}} {
		if isIntegrityConstraint(value) {
			t.Errorf("non-constraint %T(%v) was classified", value, value)
		}
	}
}
