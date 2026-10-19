// Package db opens the SQLite database, applies embedded goose migrations and
// provides transaction helpers shared by the repositories in internal/store.
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Dialect identifies the underlying SQL database engine.
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// Querier is the subset of *sql.DB / *sql.Tx used by repositories, so the same
// repository code runs inside or outside a transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var (
	_ Querier = (*sql.DB)(nil)
	_ Querier = (*sql.Tx)(nil)
)

// DB wraps *sql.DB for the panel database.
type DB struct {
	*sql.DB
	path    string
	dialect Dialect
}

// Dialect returns the database engine dialect (sqlite or postgres).
func (d *DB) Dialect() Dialect {
	if d == nil || d.dialect == "" {
		return DialectSQLite
	}
	return d.dialect
}

// Options tune Open. The zero value is suitable for production.
type Options struct {
	// MaxOpenConns caps the pool (default 8). WAL allows concurrent readers;
	// writers are serialized by SQLite (BEGIN IMMEDIATE + busy_timeout).
	MaxOpenConns int
	// BusyTimeout is how long a writer waits for the lock (default 5s).
	BusyTimeout time.Duration
}

// DSN builds the modernc.org/sqlite connection string for path with the
// pragmas every connection must carry.
func DSN(path string, busy time.Duration) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busy.Milliseconds()))
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "temp_store(MEMORY)")
	// Take the write lock at BEGIN so read->write upgrades cannot deadlock.
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// Open opens (creating if needed) the SQLite database at path and verifies the
// connection. It does not run migrations; call Migrate.
func Open(ctx context.Context, path string, opts Options) (*DB, error) {
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = 8
	}
	if opts.BusyTimeout <= 0 {
		opts.BusyTimeout = 5 * time.Second
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("db: create dir: %w", err)
		}
	}
	// Create the file ourselves so it is private (0600); SQLite gives the
	// -wal/-shm files the same mode as the main database file.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("db: create file: %w", err)
	}
	_ = f.Close()
	sqldb, err := sql.Open("sqlite", DSN(path, opts.BusyTimeout))
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	sqldb.SetMaxOpenConns(opts.MaxOpenConns)
	sqldb.SetMaxIdleConns(opts.MaxOpenConns)
	sqldb.SetConnMaxIdleTime(5 * time.Minute)

	d := &DB{DB: sqldb, path: path, dialect: DialectSQLite}
	if err := d.verify(ctx); err != nil {
		_ = sqldb.Close()
		return nil, err
	}
	return d, nil
}

// PostgresOptions configures connection pooling for PostgreSQL.
type PostgresOptions struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// OpenPostgres opens a connection pool to a PostgreSQL database at dsn and verifies it.
func OpenPostgres(ctx context.Context, dsn string, opts PostgresOptions) (*DB, error) {
	if opts.MaxOpenConns <= 0 {
		opts.MaxOpenConns = 25
	}
	if opts.MaxIdleConns <= 0 {
		opts.MaxIdleConns = 10
	}
	if opts.ConnMaxIdleTime <= 0 {
		opts.ConnMaxIdleTime = 5 * time.Minute
	}
	if opts.ConnMaxLifetime <= 0 {
		opts.ConnMaxLifetime = 1 * time.Hour
	}

	sqldb, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open postgres: %w", err)
	}
	sqldb.SetMaxOpenConns(opts.MaxOpenConns)
	sqldb.SetMaxIdleConns(opts.MaxIdleConns)
	sqldb.SetConnMaxIdleTime(opts.ConnMaxIdleTime)
	sqldb.SetConnMaxLifetime(opts.ConnMaxLifetime)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := sqldb.PingContext(pingCtx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("db: ping postgres: %w", err)
	}

	return &DB{DB: sqldb, path: dsn, dialect: DialectPostgres}, nil
}

type dialectQuerier struct {
	q       Querier
	dialect Dialect
}

func (dq dialectQuerier) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if dq.dialect == DialectPostgres {
		query = Rebind(query)
	}
	return dq.q.ExecContext(ctx, query, args...)
}

func (dq dialectQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if dq.dialect == DialectPostgres {
		query = Rebind(query)
	}
	return dq.q.QueryContext(ctx, query, args...)
}

func (dq dialectQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if dq.dialect == DialectPostgres {
		query = Rebind(query)
	}
	return dq.q.QueryRowContext(ctx, query, args...)
}

// ExecContext executes a query using the underlying db, rebinding parameters for PostgreSQL.
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if d != nil && d.dialect == DialectPostgres {
		query = Rebind(query)
	}
	return d.DB.ExecContext(ctx, query, args...)
}

// QueryContext executes a query using the underlying db, rebinding parameters for PostgreSQL.
func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if d != nil && d.dialect == DialectPostgres {
		query = Rebind(query)
	}
	return d.DB.QueryContext(ctx, query, args...)
}

// QueryRowContext executes a query using the underlying db, rebinding parameters for PostgreSQL.
func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if d != nil && d.dialect == DialectPostgres {
		query = Rebind(query)
	}
	return d.DB.QueryRowContext(ctx, query, args...)
}

// PrepareContext creates a prepared statement using the underlying db, rebinding parameters for PostgreSQL.
func (d *DB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if d != nil && d.dialect == DialectPostgres {
		query = Rebind(query)
	}
	return d.DB.PrepareContext(ctx, query)
}

// Wrap returns a Querier that automatically adapts queries (e.g. parameter rebinding)
// according to the database dialect.
func (d *DB) Wrap(q Querier) Querier {
	if d == nil || d.Dialect() == DialectSQLite {
		return q
	}
	if dq, ok := q.(dialectQuerier); ok {
		return dq
	}
	if pdb, ok := q.(*DB); ok {
		return pdb
	}
	return dialectQuerier{q: q, dialect: d.Dialect()}
}

func (d *DB) verify(ctx context.Context) error {
	var mode string
	if err := d.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return fmt.Errorf("db: ping: %w", err)
	}
	if mode != "wal" {
		return fmt.Errorf("db: journal_mode is %q, want wal", mode)
	}
	var fk int
	if err := d.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		return fmt.Errorf("db: pragma foreign_keys: %w", err)
	}
	if fk != 1 {
		return errors.New("db: foreign_keys pragma not enabled")
	}
	return nil
}

// Path returns the database file path.
func (d *DB) Path() string { return d.path }

// WithTx runs fn inside a transaction (BEGIN IMMEDIATE). It commits when fn
// returns nil and rolls back otherwise (also on panic, which is re-raised).
func (d *DB) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) (err error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("db: commit: %w", err)
	}
	return nil
}

// IsUniqueViolation reports whether err is a UNIQUE or PRIMARY KEY constraint failure.
func IsUniqueViolation(err error) bool {
	if code, ok := sqliteCode(err); ok {
		return code == sqlite3.SQLITE_CONSTRAINT_UNIQUE || code == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}

// IsForeignKeyViolation reports whether err is a FOREIGN KEY constraint failure.
func IsForeignKeyViolation(err error) bool {
	if code, ok := sqliteCode(err); ok {
		return code == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	return false
}

// IsCheckViolation reports whether err is a CHECK constraint failure.
func IsCheckViolation(err error) bool {
	if code, ok := sqliteCode(err); ok {
		return code == sqlite3.SQLITE_CONSTRAINT_CHECK
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23514"
	}
	return false
}

func sqliteCode(err error) (int, bool) {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code(), true
	}
	return 0, false
}
