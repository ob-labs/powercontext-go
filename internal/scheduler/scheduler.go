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
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type ConfigurationError struct{ Field string }

func (e *ConfigurationError) Error() string {
	if e.Field == "scheduler_path" {
		return "scheduler_path must reference a file"
	}
	return e.Field + " must be positive"
}

type StateError struct {
	Code, RuntimeKey string
}

func (e *StateError) Error() string {
	if e.Code == "duplicate" {
		return "a scheduled runtime is already open for " + e.RuntimeKey
	}
	return "no live scheduled runtime is registered for " + e.RuntimeKey
}

type RunError struct {
	Kind JobKind
	Err  error
}

func (e RunError) Error() string {
	return fmt.Sprintf("scheduled %s processor failed: %v", e.Kind, e.Err)
}
func (e RunError) Unwrap() error { return e.Err }

type Config struct {
	Path                         string
	SourceWindowInterval         *time.Duration
	ExperienceIncubationInterval *time.Duration
	SourceWindow                 func(context.Context) error
	ExperienceIncubation         func(context.Context) error
	OnError                      func(RunError)
	Clock                        func() time.Time
}

type Scheduler struct {
	path     string
	db       *sql.DB
	clock    func() time.Time
	onError  func(RunError)
	handlers map[JobKind]func(context.Context) error

	jobsMu sync.RWMutex
	jobs   map[JobKind]Job

	ctx    context.Context
	cancel context.CancelFunc
	wake   chan struct{}
	wg     sync.WaitGroup

	closeMu sync.Mutex
	closed  bool
}

var liveOwners = struct {
	sync.Mutex
	paths map[string]struct{}
}{paths: make(map[string]struct{})}

func Open(ctx context.Context, config Config) (*Scheduler, error) {
	path, err := canonicalPath(config.Path)
	if err != nil {
		return nil, &ConfigurationError{Field: "scheduler_path"}
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if err := claimOwner(path); err != nil {
		return nil, err
	}
	claimed := true
	defer func() {
		if claimed {
			releaseOwner(path)
		}
	}()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := (&url.URL{Scheme: "file", Path: path}).String() + "?_busy_timeout=30000&_foreign_keys=on"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	cleanup := func(err error) (*Scheduler, error) { _ = db.Close(); return nil, err }
	if err := db.PingContext(ctx); err != nil {
		return cleanup(err)
	}
	if err := ensureSchema(ctx, db); err != nil {
		return cleanup(err)
	}
	clock := config.Clock
	if clock == nil {
		clock = time.Now
	}
	jobs, err := reconcile(ctx, db, path, clock().UTC().Truncate(time.Microsecond), config)
	if err != nil {
		return cleanup(err)
	}

	schedulerContext, cancel := context.WithCancel(context.Background())
	scheduler := &Scheduler{
		path: path, db: db, clock: clock, onError: config.OnError,
		handlers: map[JobKind]func(context.Context) error{
			SourceWindow: config.SourceWindow, ExperienceIncubation: config.ExperienceIncubation,
		},
		jobs: jobs, ctx: schedulerContext, cancel: cancel, wake: make(chan struct{}, 1),
	}
	claimed = false
	// WaitGroup.Go is used only with a recovery boundary around the entire loop.
	scheduler.wg.Go(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				scheduler.report(RunError{Err: fmt.Errorf("scheduler loop panic: %v", recovered)})
			}
		}()
		scheduler.loop()
	})
	return scheduler, nil
}

func validateConfig(config Config) error {
	if config.SourceWindowInterval == nil && config.ExperienceIncubationInterval == nil {
		return &ConfigurationError{Field: "schedule_seconds"}
	}
	for _, item := range []struct {
		field    string
		interval *time.Duration
		handler  func(context.Context) error
	}{
		{"schedule_seconds", config.SourceWindowInterval, config.SourceWindow},
		{"experience_schedule_seconds", config.ExperienceIncubationInterval, config.ExperienceIncubation},
	} {
		if item.interval == nil {
			continue
		}
		if *item.interval <= 0 || *item.interval%time.Microsecond != 0 {
			return &ConfigurationError{Field: item.field}
		}
		if item.handler == nil {
			return fmt.Errorf("scheduler processor for %s is not configured", item.field)
		}
	}
	return nil
}

func ensureSchema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS powercontext_scheduler_jobs (
        id VARCHAR(191) NOT NULL,
        next_run_time FLOAT,
        job_state BLOB NOT NULL,
        PRIMARY KEY (id)
    )`); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS ix_powercontext_scheduler_jobs_next_run_time
        ON powercontext_scheduler_jobs (next_run_time)`)
	return err
}

func reconcile(ctx context.Context, db *sql.DB, path string, now time.Time, config Config) (map[JobKind]Job, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	rollback := func(err error) (map[JobKind]Job, error) {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return nil, errors.Join(err, rollbackErr)
		}
		return nil, err
	}
	existing, err := loadJobs(ctx, tx, path)
	if err != nil {
		return rollback(err)
	}
	wanted := []struct {
		kind     JobKind
		interval *time.Duration
	}{
		{SourceWindow, config.SourceWindowInterval},
		{ExperienceIncubation, config.ExperienceIncubationInterval},
	}
	result := make(map[JobKind]Job, 2)
	for _, item := range wanted {
		current, found := existing[item.kind]
		if item.interval == nil {
			if found {
				if _, err := tx.ExecContext(ctx, `DELETE FROM powercontext_scheduler_jobs WHERE id = ?`, current.ID()); err != nil {
					return rollback(err)
				}
			}
			continue
		}
		if found && current.Interval() == *item.interval {
			result[item.kind] = current
			continue
		}
		start := now.Add(*item.interval).UTC().Truncate(time.Microsecond)
		job, err := NewJob(item.kind, path, *item.interval, start, start)
		if err != nil {
			return rollback(err)
		}
		blob, err := encodeJobState(job)
		if err != nil {
			return rollback(err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO powercontext_scheduler_jobs (id, next_run_time, job_state)
            VALUES (?, ?, ?) ON CONFLICT(id) DO UPDATE SET
              next_run_time = excluded.next_run_time, job_state = excluded.job_state`,
			job.ID(), unixTimestamp(job.NextRunTime()), blob)
		if err != nil {
			return rollback(err)
		}
		result[item.kind] = job
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadJobs(ctx context.Context, db queryer, path string) (map[JobKind]Job, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, next_run_time, job_state FROM powercontext_scheduler_jobs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[JobKind]Job, 2)
	for rows.Next() {
		var id string
		var nextValue, blobValue any
		if err := rows.Scan(&id, &nextValue, &blobValue); err != nil {
			return nil, err
		}
		kind, known := kindForID(id)
		if !known {
			return nil, invalidState("unknown stable Job ID")
		}
		if _, duplicate := result[kind]; duplicate {
			return nil, invalidState("duplicate stable Job ID")
		}
		next, ok := storedFloat(nextValue)
		if !ok {
			return nil, invalidState("next_run_time column is not a finite float")
		}
		blob, ok := storedBlob(blobValue)
		if !ok {
			return nil, invalidState("job_state column is not bytes")
		}
		job, err := decodeJobState(blob, id, path, next)
		if err != nil {
			return nil, err
		}
		result[kind] = job
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Scheduler) loop() {
	for {
		jobs := s.snapshot()
		if len(jobs) == 0 {
			select {
			case <-s.ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}
		now := s.clock().UTC()
		wait := jobs[0].NextRunTime().Sub(now)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-s.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-s.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-timer.C:
			s.runDue(s.clock().UTC())
		}
	}
}

func (s *Scheduler) snapshot() []Job {
	s.jobsMu.RLock()
	jobs := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job)
	}
	s.jobsMu.RUnlock()
	slices.SortFunc(jobs, func(left, right Job) int {
		if order := left.NextRunTime().Compare(right.NextRunTime()); order != 0 {
			return order
		}
		if left.ID() < right.ID() {
			return -1
		}
		if left.ID() > right.ID() {
			return 1
		}
		return 0
	})
	return jobs
}

func (s *Scheduler) runDue(now time.Time) {
	for _, job := range s.snapshot() {
		if job.NextRunTime().After(now) {
			break
		}
		next, err := job.withNext(now)
		if err != nil {
			s.report(RunError{Kind: job.Kind(), Err: err})
			s.cancel()
			return
		}
		blob, err := encodeJobState(next)
		if err == nil {
			var result sql.Result
			result, err = s.db.ExecContext(s.ctx, `UPDATE powercontext_scheduler_jobs
                SET next_run_time = ?, job_state = ? WHERE id = ? AND next_run_time = ?`,
				unixTimestamp(next.NextRunTime()), blob, job.ID(), unixTimestamp(job.NextRunTime()))
			if err == nil {
				var affected int64
				affected, err = result.RowsAffected()
				if err == nil && affected != 1 {
					err = fmt.Errorf("scheduler Job %q next-run CAS affected %d rows", job.ID(), affected)
				}
			}
		}
		if err != nil {
			s.report(RunError{Kind: job.Kind(), Err: err})
			s.cancel()
			return
		}
		s.jobsMu.Lock()
		s.jobs[job.Kind()] = next
		s.jobsMu.Unlock()
		s.safeHandle(job.Kind())
	}
}

func (s *Scheduler) safeHandle(kind JobKind) {
	handler := s.handlers[kind]
	if handler == nil {
		s.report(RunError{Kind: kind, Err: errors.New("processor is not configured")})
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			s.report(RunError{Kind: kind, Err: fmt.Errorf("processor panic: %v", recovered)})
		}
	}()
	if err := handler(s.ctx); err != nil {
		s.report(RunError{Kind: kind, Err: err})
	}
}

func (s *Scheduler) report(value RunError) {
	if s.onError != nil {
		s.onError(value)
	}
}

// Pause prevents future dispatch without waiting for a currently executing
// processor. Close performs the corresponding drain and resource release.
func (s *Scheduler) Pause() { s.cancel() }

func (s *Scheduler) Close(ctx context.Context) error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	s.cancel()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	if err := s.db.Close(); err != nil {
		return err
	}
	releaseOwner(s.path)
	s.closed = true
	return nil
}

func claimOwner(path string) error {
	liveOwners.Lock()
	defer liveOwners.Unlock()
	if _, exists := liveOwners.paths[path]; exists {
		return &StateError{Code: "duplicate", RuntimeKey: path}
	}
	liveOwners.paths[path] = struct{}{}
	return nil
}

func releaseOwner(path string) {
	liveOwners.Lock()
	delete(liveOwners.paths, path)
	liveOwners.Unlock()
}

func storedFloat(value any) (float64, bool) {
	var result float64
	switch typed := value.(type) {
	case float64:
		result = typed
	case float32:
		result = float64(typed)
	case int64:
		result = float64(typed)
	default:
		return 0, false
	}
	return result, !math.IsNaN(result) && !math.IsInf(result, 0)
}

func storedBlob(value any) ([]byte, bool) {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...), true
	case string:
		return []byte(typed), true
	default:
		return nil, false
	}
}
