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
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/mattn/go-sqlite3"

	embeddedseekdb "github.com/ob-labs/powercontext-go/internal/sqlstore/seekdb"
	"github.com/ob-labs/powercontext-go/internal/sqlstore/sqlitevec"
)

// DBTX is the smallest query surface shared by sql.DB and sql.Tx.
type DBTX interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Database owns a connection pool and drains admitted transactions before it
// closes. Repositories receive DBTX and never own this lifecycle.
type Database struct {
	db   *sql.DB
	owns bool

	stateMu sync.Mutex
	closing bool
	closed  bool
	active  int
	drained chan struct{}
	closeMu sync.Mutex
}

// Attach uses a caller-owned pool without taking close ownership.
func Attach(db *sql.DB) (*Database, error) {
	if db == nil {
		return nil, errors.New("sqlstore: attached database must not be nil")
	}
	return &Database{db: db}, nil
}

// SQLDB exposes the pool for driver-specific read-only capability probes. Use
// Transaction for application operations.
func (d *Database) SQLDB() *sql.DB { return d.db }

// Transaction runs one caller-owned use case transaction. A nil callback is
// rejected before admission.
func (d *Database) Transaction(ctx context.Context, fn func(DBTX) error) error {
	if fn == nil {
		return errors.New("sqlstore: transaction callback must not be nil")
	}
	if err := d.admit(); err != nil {
		return err
	}
	defer d.release()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return tx.Commit()
}

// Ping verifies that the pool admits and executes a transaction.
func (d *Database) Ping(ctx context.Context) error {
	return d.Transaction(ctx, func(tx DBTX) error {
		var one int
		return tx.QueryRowContext(ctx, "SELECT 1").Scan(&one)
	})
}

// Close rejects new work, waits for admitted transactions, then closes the
// owned pool. A canceled close restores admission so callers can retry.
func (d *Database) Close(ctx context.Context) error {
	d.closeMu.Lock()
	defer d.closeMu.Unlock()

	d.stateMu.Lock()
	if d.closed {
		d.stateMu.Unlock()
		return nil
	}
	if !d.closing {
		d.closing = true
		d.drained = make(chan struct{})
		if d.active == 0 {
			close(d.drained)
		}
	}
	drained := d.drained
	d.stateMu.Unlock()

	select {
	case <-drained:
	case <-ctx.Done():
		d.stateMu.Lock()
		d.closing = false
		d.drained = nil
		d.stateMu.Unlock()
		return context.Cause(ctx)
	}

	if d.owns {
		if err := d.db.Close(); err != nil {
			d.stateMu.Lock()
			d.closing = false
			d.drained = nil
			d.stateMu.Unlock()
			return err
		}
	}
	d.stateMu.Lock()
	d.closed = true
	d.closing = false
	d.drained = nil
	d.stateMu.Unlock()
	return nil
}

func (d *Database) admit() error {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.closed || d.closing {
		return &DatabaseClosedError{}
	}
	d.active++
	return nil
}

func (d *Database) release() {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	d.active--
	if d.active == 0 && d.closing && d.drained != nil {
		close(d.drained)
	}
}

// SQLiteConfig configures the Python-compatible SQLite profile.
type SQLiteConfig struct {
	DSN             string
	BusyTimeout     time.Duration
	JournalMode     string
	ForeignKeys     bool
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// DefaultSQLiteConfig returns the current Python-compatible profile defaults.
func DefaultSQLiteConfig(dsn string) SQLiteConfig {
	return SQLiteConfig{
		DSN:          dsn,
		BusyTimeout:  5 * time.Second,
		JournalMode:  "WAL",
		ForeignKeys:  true,
		MaxOpenConns: 8,
		MaxIdleConns: 8,
	}
}

// OpenSQLite opens, configures, warms, and initializes the relational schema.
func OpenSQLite(ctx context.Context, config SQLiteConfig) (*Database, error) {
	if err := validateSQLiteConfig(config); err != nil {
		return nil, err
	}
	if err := sqlitevec.RegisterAuto(); err != nil {
		return nil, err
	}
	if err := createSQLiteParent(config.DSN); err != nil {
		return nil, err
	}
	connector := &sqliteConnector{
		driver:        &sqlite3.SQLiteDriver{},
		dsn:           config.DSN,
		busyTimeoutMS: config.BusyTimeout.Milliseconds(),
		foreignKeys:   config.ForeignKeys,
	}
	pool := sql.OpenDB(connector)
	maxOpen := config.MaxOpenConns
	maxIdle := config.MaxIdleConns
	if sqliteMemoryDSN(config.DSN) {
		maxOpen, maxIdle = 1, 1
	}
	pool.SetMaxOpenConns(maxOpen)
	pool.SetMaxIdleConns(maxIdle)
	pool.SetConnMaxLifetime(config.ConnMaxLifetime)

	owned := &Database{db: pool, owns: true}
	cleanup := func(err error) (*Database, error) {
		_ = pool.Close()
		return nil, err
	}
	if _, err := pool.ExecContext(ctx, "PRAGMA journal_mode = "+config.JournalMode); err != nil {
		return cleanup(err)
	}
	if err := owned.Transaction(ctx, func(tx DBTX) error {
		return EnsureBuiltinSchema(ctx, tx)
	}); err != nil {
		return cleanup(err)
	}
	return owned, nil
}

// OceanBaseConfig configures the Python-compatible OceanBase MySQL profile.
// URL must retain the frozen mysql+aoceanbase scheme even though the Go driver
// speaks the MySQL wire protocol directly.
type OceanBaseConfig struct {
	URL             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

// SeekDBConfig configures one optional embedded seekDB process. libseekdb and
// its sibling seekdb executable are loaded only when this profile is selected.
type SeekDBConfig struct {
	Path            string
	Database        string
	LibraryPath     string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

func OpenSeekDB(
	ctx context.Context,
	config SeekDBConfig,
) (*Database, *embeddedseekdb.Instance, error) {
	if config.Database != "test" {
		return nil, nil, errors.New("sqlstore: embedded seekDB database must be test")
	}
	if config.MaxOpenConns < 1 || config.MaxIdleConns < 0 ||
		config.MaxIdleConns > config.MaxOpenConns || config.ConnMaxLifetime < 0 {
		return nil, nil, errors.New("sqlstore: invalid embedded seekDB connection pool limits")
	}
	instance, err := embeddedseekdb.Open(ctx, embeddedseekdb.Config{
		Path: config.Path, LibraryPath: config.LibraryPath,
	})
	if err != nil {
		return nil, nil, err
	}
	driverConfig, err := seekDBDriverConfig(instance.ConnectionOptions(), config.Database)
	if err != nil {
		_ = instance.Close(context.Background())
		return nil, nil, err
	}
	connector, err := mysql.NewConnector(driverConfig)
	if err != nil {
		_ = instance.Close(context.Background())
		return nil, nil, errors.New("sqlstore: embedded seekDB connector could not be configured")
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(config.MaxOpenConns)
	pool.SetMaxIdleConns(config.MaxIdleConns)
	pool.SetConnMaxLifetime(config.ConnMaxLifetime)
	owned := &Database{db: pool, owns: true}
	cleanup := func(cause error) (*Database, *embeddedseekdb.Instance, error) {
		_ = pool.Close()
		_ = instance.Close(context.Background())
		return nil, nil, cause
	}
	if err := owned.Transaction(ctx, func(tx DBTX) error {
		return EnsureBuiltinSchemaForDialect(ctx, tx, MySQLDialect)
	}); err != nil {
		return cleanup(err)
	}
	return owned, instance, nil
}

func seekDBDriverConfig(options embeddedseekdb.ConnectionOptions, database string) (*mysql.Config, error) {
	if options.User == "" || database != "test" {
		return nil, errors.New("sqlstore: embedded seekDB connection options are invalid")
	}
	config := mysql.NewConfig()
	if err := config.Apply(mysql.Charset("utf8mb4", "")); err != nil {
		return nil, errors.New("sqlstore: embedded seekDB charset could not be configured")
	}
	config.User = options.User
	config.DBName = database
	config.ParseTime = true
	config.Params = map[string]string{"autocommit": "0"}
	switch options.Transport {
	case "tcp":
		if options.Port < 1 || options.Port > 65_535 {
			return nil, errors.New("sqlstore: embedded seekDB TCP port is invalid")
		}
		config.Net = "tcp"
		config.Addr = net.JoinHostPort("localhost", strconv.Itoa(int(options.Port)))
	case "unix_socket":
		if strings.TrimSpace(options.Endpoint) == "" {
			return nil, errors.New("sqlstore: embedded seekDB Unix socket is invalid")
		}
		config.Net = "unix"
		config.Addr = options.Endpoint
	default:
		return nil, fmt.Errorf("sqlstore: embedded seekDB transport %q is unsupported", options.Transport)
	}
	return config, nil
}

// UnsupportedOceanBaseTenantError reports an OceanBase tenant whose
// authoritative compatibility marker is absent or not MYSQL.
type UnsupportedOceanBaseTenantError struct{ CompatibilityMode *string }

func (e *UnsupportedOceanBaseTenantError) Error() string {
	if e == nil || e.CompatibilityMode == nil {
		return "sqlstore: OceanBase profile requires a MySQL-compatible tenant; ob_compatibility_mode is missing"
	}
	return fmt.Sprintf("sqlstore: OceanBase profile requires a MySQL-compatible tenant; found %q", *e.CompatibilityMode)
}

// OpenOceanBase validates the frozen URL shape, probes the tenant mode, and
// initializes only the Python-compatible relational schema.
func OpenOceanBase(ctx context.Context, config OceanBaseConfig) (*Database, error) {
	driverConfig, err := oceanBaseDriverConfig(config.URL)
	if err != nil {
		return nil, err
	}
	if config.MaxOpenConns < 1 || config.MaxIdleConns < 0 || config.MaxIdleConns > config.MaxOpenConns || config.ConnMaxLifetime < 0 {
		return nil, errors.New("sqlstore: invalid OceanBase connection pool limits")
	}
	connector, err := mysql.NewConnector(driverConfig)
	if err != nil {
		return nil, errors.New("sqlstore: OceanBase connector could not be configured")
	}
	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(config.MaxOpenConns)
	pool.SetMaxIdleConns(config.MaxIdleConns)
	pool.SetConnMaxLifetime(config.ConnMaxLifetime)
	owned := &Database{db: pool, owns: true}
	cleanup := func(cause error) (*Database, error) {
		_ = pool.Close()
		return nil, cause
	}
	if err := owned.Transaction(ctx, func(tx DBTX) error {
		var name, mode string
		queryErr := tx.QueryRowContext(ctx, "SHOW VARIABLES LIKE 'ob_compatibility_mode'").Scan(&name, &mode)
		if validationErr := validateOceanBaseTenantMode(name, mode, queryErr); validationErr != nil {
			return validationErr
		}
		return EnsureBuiltinSchemaForDialect(ctx, tx, MySQLDialect)
	}); err != nil {
		return cleanup(err)
	}
	return owned, nil
}

func validateOceanBaseTenantMode(name, mode string, queryErr error) error {
	if errors.Is(queryErr, sql.ErrNoRows) {
		return &UnsupportedOceanBaseTenantError{}
	}
	if queryErr != nil {
		return queryErr
	}
	mode = strings.ToUpper(mode)
	if name != "ob_compatibility_mode" || mode != "MYSQL" {
		return &UnsupportedOceanBaseTenantError{CompatibilityMode: &mode}
	}
	return nil
}

// ValidateOceanBaseURL validates the frozen Python profile URL without
// returning credentials or opening a network connection.
func ValidateOceanBaseURL(rawURL string) error {
	_, err := oceanBaseDriverConfig(rawURL)
	return err
}

func oceanBaseDriverConfig(rawURL string) (*mysql.Config, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "mysql+aoceanbase" || parsed.User == nil || parsed.User.Username() == "" ||
		parsed.Hostname() == "" || parsed.Port() == "" || parsed.Fragment != "" {
		return nil, errors.New("sqlstore: OceanBase profile URL is invalid")
	}
	portNumber, err := strconv.Atoi(parsed.Port())
	if err != nil || portNumber < 1 || portNumber > 65_535 {
		return nil, errors.New("sqlstore: OceanBase profile URL must include a valid explicit port")
	}
	database := strings.TrimPrefix(parsed.EscapedPath(), "/")
	decodedDatabase, err := url.PathUnescape(database)
	if err != nil || decodedDatabase == "" || strings.Contains(decodedDatabase, "/") {
		return nil, errors.New("sqlstore: OceanBase profile URL must include a database")
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || query.Get("charset") != "utf8mb4" {
		return nil, errors.New("sqlstore: OceanBase profile URL must set charset=utf8mb4")
	}
	port := strconv.Itoa(portNumber)
	password, _ := parsed.User.Password()
	params := make(map[string]string, len(query))
	for name, values := range query {
		if len(values) != 1 {
			return nil, errors.New("sqlstore: OceanBase profile URL query is invalid")
		}
		if name == "charset" {
			continue
		}
		params[name] = values[0]
	}
	driverConfig := mysql.NewConfig()
	if err := driverConfig.Apply(mysql.Charset("utf8mb4", "")); err != nil {
		return nil, errors.New("sqlstore: OceanBase charset could not be configured")
	}
	driverConfig.User = parsed.User.Username()
	driverConfig.Passwd = password
	driverConfig.Net = "tcp"
	driverConfig.Addr = net.JoinHostPort(parsed.Hostname(), port)
	driverConfig.DBName = decodedDatabase
	driverConfig.Params = params
	driverConfig.ParseTime = true
	return driverConfig, nil
}

type sqliteConnector struct {
	driver        *sqlite3.SQLiteDriver
	dsn           string
	busyTimeoutMS int64
	foreignKeys   bool
}

func (c *sqliteConnector) Driver() driver.Driver { return c.driver }

func (c *sqliteConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	opened, err := c.driver.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	conn, ok := opened.(*sqlite3.SQLiteConn)
	if !ok {
		_ = opened.Close()
		return nil, fmt.Errorf("sqlstore: sqlite driver returned %T", opened)
	}
	closeWith := func(err error) (driver.Conn, error) {
		_ = conn.Close()
		return nil, err
	}
	if _, err := conn.Exec(fmt.Sprintf("PRAGMA busy_timeout = %d", c.busyTimeoutMS), nil); err != nil {
		return closeWith(err)
	}
	foreignKeys := "OFF"
	if c.foreignKeys {
		foreignKeys = "ON"
	}
	if _, err := conn.Exec("PRAGMA foreign_keys = "+foreignKeys, nil); err != nil {
		return closeWith(err)
	}
	if err := context.Cause(ctx); err != nil {
		return closeWith(err)
	}
	return conn, nil
}

func validateSQLiteConfig(config SQLiteConfig) error {
	if strings.TrimSpace(config.DSN) == "" {
		return errors.New("sqlstore: SQLite DSN must not be empty")
	}
	const maxBusyTimeout = time.Duration(1<<31-1) * time.Millisecond
	if config.BusyTimeout < 0 || config.BusyTimeout > maxBusyTimeout {
		return errors.New("sqlstore: SQLite busy timeout is outside the supported range")
	}
	switch config.JournalMode {
	case "WAL", "DELETE", "MEMORY":
	default:
		return fmt.Errorf("sqlstore: unsupported SQLite journal mode %q", config.JournalMode)
	}
	if config.MaxOpenConns < 1 || config.MaxIdleConns < 0 || config.MaxIdleConns > config.MaxOpenConns {
		return errors.New("sqlstore: invalid SQLite connection pool limits")
	}
	return nil
}

func sqliteMemoryDSN(dsn string) bool {
	return dsn == ":memory:" || strings.Contains(dsn, "mode=memory")
}

func createSQLiteParent(dsn string) error {
	if sqliteMemoryDSN(dsn) || strings.HasPrefix(dsn, "file:") || strings.Contains(dsn, "?") {
		return nil
	}
	directory := filepath.Dir(dsn)
	if directory == "." || directory == "" {
		return nil
	}
	return ensureDirectory(directory)
}
