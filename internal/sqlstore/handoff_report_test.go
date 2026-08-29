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
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ob-labs/powercontext-go/internal/handoffreport"
	"github.com/ob-labs/powercontext-go/internal/sqlstore"
)

func TestHandoffReportSchemaIsOptInAndCatalogRevisionsAreAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	var tables int
	if err := database.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master
        WHERE type = 'table' AND name LIKE 'pc_handoff_report_%'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("disabled core created %d Handoff Report tables", tables)
	}
	store, err := sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	if schemaErr := store.EnsureSchema(ctx); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	project := reportProject(t, "prj-1", "one", 1, handoffreport.CatalogIncluded)
	if _, createErr := store.CreateProject(ctx, project, reportTime(1)); createErr != nil {
		t.Fatal(createErr)
	}
	workstream := reportWorkstream(t, "scope-a", "prj-1", 1, handoffreport.CatalogIncluded)
	if _, registerErr := store.RegisterWorkstream(ctx, workstream, reportTime(1)); registerErr != nil {
		t.Fatal(registerErr)
	}
	updated := reportProject(t, "prj-1", "one", 2, handoffreport.CatalogArchived)
	if _, updateErr := store.UpdateProject(ctx, updated, 1, reportTime(2)); updateErr != nil {
		t.Fatal(updateErr)
	}
	page, err := store.ListProjects(ctx, nil, 50, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("archived project leaked into default page: %#v", page.Items)
	}
	page, err = store.ListProjects(ctx, nil, 50, true)
	if err != nil || len(page.Items) != 1 || page.Items[0].Version() != 2 {
		t.Fatalf("all projects = %#v, %v", page, err)
	}
	var revisions int
	if queryErr := database.SQLDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM pc_handoff_report_project_revisions WHERE project_id = ?", "prj-1").Scan(&revisions); queryErr != nil {
		t.Fatal(queryErr)
	}
	if revisions != 2 {
		t.Fatalf("revision count = %d", revisions)
	}
	stale := reportProject(t, "prj-1", "one", 2, handoffreport.CatalogIncluded)
	_, err = store.UpdateProject(ctx, stale, 1, reportTime(3))
	var conflict *handoffreport.ProjectConflictError
	if !errors.As(err, &conflict) || conflict.CurrentVersion == nil || *conflict.CurrentVersion != 2 {
		t.Fatalf("expected current version 2 conflict, got %v", err)
	}
	var version int
	if queryErr := database.SQLDB().QueryRowContext(ctx, "SELECT version FROM pc_handoff_report_projects WHERE project_id = ?", "prj-1").Scan(&version); queryErr != nil {
		t.Fatal(queryErr)
	}
	if version != 2 {
		t.Fatalf("failed CAS changed project version to %d", version)
	}

	updatedWorkstream := reportWorkstream(t, "scope-a", "prj-1", 2, handoffreport.CatalogIncluded)
	if _, updateErr := store.UpdateWorkstream(ctx, updatedWorkstream, 1, reportTime(2)); updateErr != nil {
		t.Fatal(updateErr)
	}
	staleWorkstream := reportWorkstream(t, "scope-a", "prj-1", 2, handoffreport.CatalogArchived)
	_, err = store.UpdateWorkstream(ctx, staleWorkstream, 1, reportTime(3))
	var workstreamConflict *handoffreport.WorkstreamConflictError
	if !errors.As(err, &workstreamConflict) || workstreamConflict.CurrentVersion == nil || *workstreamConflict.CurrentVersion != 2 {
		t.Fatalf("expected current Workstream version 2 conflict, got %v", err)
	}
	if err := database.SQLDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM pc_handoff_report_workstream_revisions WHERE scope_id = ?", "scope-a").Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 2 {
		t.Fatalf("Workstream revision count = %d", revisions)
	}
}

func TestHandoffReportCatalogCreatesProjectsAndResolvesScopeMembership(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	store, err := sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	if schemaErr := store.EnsureSchema(ctx); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	project := reportProject(t, "prj-1", "powercontext", 1, handoffreport.CatalogIncluded)
	if _, createErr := store.CreateProject(ctx, project, reportTime(1)); createErr != nil {
		t.Fatal(createErr)
	}
	workstream := reportWorkstream(t, "scope-report", project.ProjectID(), 1, handoffreport.CatalogIncluded)
	if _, registerErr := store.RegisterWorkstream(ctx, workstream, reportTime(1)); registerErr != nil {
		t.Fatal(registerErr)
	}
	gotProject, err := store.GetProject(ctx, project.ProjectID())
	if err != nil || !reflect.DeepEqual(gotProject, project) {
		t.Fatalf("Project = %#v, %v", gotProject, err)
	}
	page, err := store.ListWorkstreams(ctx, project.ProjectID(), nil, 50, false)
	if err != nil || len(page.Items) != 1 || !reflect.DeepEqual(page.Items[0], workstream) {
		t.Fatalf("Workstreams = %#v, %v", page, err)
	}
}

func TestHandoffReportCatalogEnforcesProjectWorkstreamAndScopeUniqueness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	store, err := sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	if schemaErr := store.EnsureSchema(ctx); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	first := reportProject(t, "prj-1", "same-key", 1, handoffreport.CatalogIncluded)
	if _, createErr := store.CreateProject(ctx, first, reportTime(1)); createErr != nil {
		t.Fatal(createErr)
	}
	_, err = store.CreateProject(ctx, reportProject(t, "prj-2", "same-key", 1, handoffreport.CatalogIncluded), reportTime(1))
	var projectConflict *handoffreport.ProjectConflictError
	if !errors.As(err, &projectConflict) {
		t.Fatalf("duplicate Project key error = %v", err)
	}

	key := "same-workstream-key"
	firstWorkstream, err := handoffreport.NewWorkstreamDescriptor("scope-1", first.ProjectID(), &key, "Workstream", handoffreport.WorkstreamFeature, handoffreport.CatalogIncluded, nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, registerErr := store.RegisterWorkstream(ctx, firstWorkstream, reportTime(1)); registerErr != nil {
		t.Fatal(registerErr)
	}
	duplicateKey, err := handoffreport.NewWorkstreamDescriptor("scope-2", first.ProjectID(), &key, "Duplicate", handoffreport.WorkstreamFeature, handoffreport.CatalogIncluded, nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.RegisterWorkstream(ctx, duplicateKey, reportTime(1))
	var workstreamConflict *handoffreport.WorkstreamConflictError
	if !errors.As(err, &workstreamConflict) {
		t.Fatalf("duplicate Workstream key error = %v", err)
	}

	second := reportProject(t, "prj-2", "second-key", 1, handoffreport.CatalogIncluded)
	if _, createErr := store.CreateProject(ctx, second, reportTime(1)); createErr != nil {
		t.Fatal(createErr)
	}
	movedScope := reportWorkstream(t, "scope-1", second.ProjectID(), 1, handoffreport.CatalogIncluded)
	_, err = store.RegisterWorkstream(ctx, movedScope, reportTime(1))
	var grouped *handoffreport.ScopeAlreadyGroupedError
	if !errors.As(err, &grouped) || grouped.ProjectID != first.ProjectID() {
		t.Fatalf("reused scope error = %v", err)
	}
}

func TestHandoffReportCatalogPaginatesAndExcludesArchivedProjectsByDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	store, err := sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	if schemaErr := store.EnsureSchema(ctx); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	for index, id := range []string{"prj-a", "prj-b", "prj-c"} {
		if _, createErr := store.CreateProject(ctx, reportProject(t, id, "project-"+id, 1, handoffreport.CatalogIncluded), reportTime(index+1)); createErr != nil {
			t.Fatal(createErr)
		}
	}
	if _, updateErr := store.UpdateProject(ctx, reportProject(t, "prj-b", "project-prj-b", 2, handoffreport.CatalogArchived), 1, reportTime(4)); updateErr != nil {
		t.Fatal(updateErr)
	}
	first, err := store.ListProjects(ctx, nil, 1, false)
	if err != nil || len(first.Items) != 1 || first.Items[0].ProjectID() != "prj-a" || first.NextCursor == nil || *first.NextCursor != "prj-a" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := store.ListProjects(ctx, first.NextCursor, 1, false)
	if err != nil || len(second.Items) != 1 || second.Items[0].ProjectID() != "prj-c" || second.NextCursor != nil {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	all, err := store.ListProjects(ctx, nil, 50, true)
	if err != nil || len(all.Items) != 3 {
		t.Fatalf("all projects = %#v, %v", all, err)
	}
}

func TestHandoffReportActivityIsGloballyIdempotentAndPurgeKeepsCursor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	store, _ := sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if schemaErr := store.EnsureSchema(ctx); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	project := reportProject(t, "prj-1", "one", 1, handoffreport.CatalogIncluded)
	if _, err := store.CreateProject(ctx, project, reportTime(1)); err != nil {
		t.Fatal(err)
	}
	first := reportActivity(t, "evt-1", "prj-1", "git:stable", reportTime(2), reportTime(2).Add(-time.Hour), nil)
	stored, err := store.RecordActivity(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	retry := reportActivity(t, "evt-2", "prj-1", "git:stable", reportTime(2).Add(time.Minute), reportTime(2).Add(-time.Hour), nil)
	repeated, err := store.RecordActivity(ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Cursor != stored.Cursor || repeated.Event.EventID() != "evt-1" {
		t.Fatalf("idempotent retry = %#v", repeated)
	}
	changedTitle := "different"
	changed := reportActivity(t, "evt-3", "prj-1", "git:stable", reportTime(2), reportTime(2).Add(-time.Hour), &changedTitle)
	_, err = store.RecordActivity(ctx, changed)
	var eventConflict *handoffreport.ActivityEventConflictError
	if !errors.As(err, &eventConflict) {
		t.Fatalf("expected Activity conflict, got %v", err)
	}
	page, err := store.ListActivities(ctx, "prj-1", nil, nil, nil, 0, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.HighWatermark != 1 || len(page.Items) != 1 {
		t.Fatalf("page = %#v", page)
	}
	deleted, err := store.PurgeActivities(ctx, "prj-1", reportTime(3))
	if err != nil || deleted != 1 {
		t.Fatalf("purge = %d, %v", deleted, err)
	}
	page, err = store.ListActivities(ctx, "prj-1", nil, nil, nil, 0, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.HighWatermark != 1 || len(page.Items) != 0 {
		t.Fatalf("purged page = %#v", page)
	}
}

func TestHandoffReportWorkspaceRequiresDetachBeforeProjectMove(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	store, _ := sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if schemaErr := store.EnsureSchema(ctx); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	for _, value := range []handoffreport.ProjectDescriptor{reportProject(t, "prj-1", "one", 1, handoffreport.CatalogIncluded), reportProject(t, "prj-2", "two", 1, handoffreport.CatalogIncluded)} {
		if _, err := store.CreateProject(ctx, value, reportTime(1)); err != nil {
			t.Fatal(err)
		}
	}
	remote := "HTTPS://GitHub.com/ob-labs/powercontext-go.git/"
	subpath := "./services/api"
	repository, err := handoffreport.NewRepositoryRef(handoffreport.RepositoryGitHub, nil, &remote, &subpath)
	if err != nil {
		t.Fatal(err)
	}
	if got := *repository.NormalizedRemote(); got != "https://github.com/ob-labs/powercontext-go.git" {
		t.Fatalf("remote = %q", got)
	}
	if got := *repository.Subpath(); got != "services/api" {
		t.Fatalf("subpath = %q", got)
	}
	binding, err := store.AttachWorkspaceBinding(ctx, "workspace-1", "prj-1", repository, nil, reportTime(2))
	if err != nil || binding.Version() != 1 {
		t.Fatalf("attach = %#v, %v", binding, err)
	}
	expected := 1
	_, err = store.AttachWorkspaceBinding(ctx, "workspace-1", "prj-2", repository, &expected, reportTime(3))
	var conflict *handoffreport.WorkspaceBindingConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected project move conflict, got %v", err)
	}
	detached, err := store.DetachWorkspaceBinding(ctx, "workspace-1", 1)
	if err != nil || detached.State() != handoffreport.WorkspaceDetached {
		t.Fatalf("detach = %#v, %v", detached, err)
	}
	_, err = store.GetWorkspaceBinding(ctx, "workspace-1")
	var missing *handoffreport.WorkspaceBindingNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("detached binding should be hidden, got %v", err)
	}
	expected = 2
	rebound, err := store.AttachWorkspaceBinding(ctx, "workspace-1", "prj-2", repository, &expected, reportTime(4))
	if err != nil || rebound.Version() != 3 || rebound.ProjectID() != "prj-2" {
		t.Fatalf("rebind = %#v, %v", rebound, err)
	}
}

func TestHandoffReportWorkspaceRejectsUnknownProjectAndMissingExactVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database := openTestDatabase(t)
	store, err := sqlstore.NewHandoffReportStore(database, sqlstore.SQLiteDialect)
	if err != nil {
		t.Fatal(err)
	}
	if schemaErr := store.EnsureSchema(ctx); schemaErr != nil {
		t.Fatal(schemaErr)
	}
	subpath := "."
	repository, err := handoffreport.NewRepositoryRef(handoffreport.RepositoryLocal, nil, nil, &subpath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.AttachWorkspaceBinding(ctx, "ws-unknown", "missing", repository, nil, reportTime(1))
	var missingProject *handoffreport.ProjectNotFoundError
	if !errors.As(err, &missingProject) {
		t.Fatalf("unknown Project error = %v", err)
	}
	_, err = store.DetachWorkspaceBinding(ctx, "ws-unknown", 1)
	var conflict *handoffreport.WorkspaceBindingConflictError
	if !errors.As(err, &conflict) || conflict.CurrentVersion != nil {
		t.Fatalf("missing exact version error = %v", err)
	}
	_, err = store.GetWorkspaceBinding(ctx, "ws-unknown")
	var missingBinding *handoffreport.WorkspaceBindingNotFoundError
	if !errors.As(err, &missingBinding) {
		t.Fatalf("missing binding error = %v", err)
	}
}

func reportProject(t *testing.T, id, key string, version int, state handoffreport.CatalogState) handoffreport.ProjectDescriptor {
	t.Helper()
	value, err := handoffreport.NewProjectDescriptor(id, key, "Project", nil, handoffreport.LocaleChinese, "UTC", state, version)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func reportWorkstream(t *testing.T, scope, project string, version int, state handoffreport.CatalogState) handoffreport.WorkstreamDescriptor {
	t.Helper()
	value, err := handoffreport.NewWorkstreamDescriptor(scope, project, nil, "Workstream", handoffreport.WorkstreamFeature, state, nil, nil, version)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func reportActivity(t *testing.T, id, project, sourceID string, observed, occurred time.Time, title *string) handoffreport.ActivityEvent {
	t.Helper()
	value, err := handoffreport.NewActivityEvent(handoffreport.ActivityEventInput{EventID: id, ProjectID: project, Source: handoffreport.ActivityGitCommit, SourceEventID: sourceID, OccurredAt: &occurred, ObservedAt: observed.UTC(), TimeBasis: handoffreport.TimeSourceReported, Title: title})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func reportTime(day int) time.Time {
	return time.Date(2026, time.August, day, 10, 0, 0, 123456000, time.UTC)
}
