// Package db opens the panel database (SQLite by default, or PostgreSQL),
// applies embedded goose migrations and provides transaction helpers shared by
// the repositories in internal/store.
//
// Repository SQL is written for SQLite. On PostgreSQL every query issued
// through DB or a Querier from DB.Wrap is adapted on the fly: placeholders are
// rebound, LIKE becomes ILIKE and Go booleans are bound as 0/1 (see
// postgresSQL and postgresArgs). The PostgreSQL schema (migrations_pg) mirrors
// the SQLite one: BIGINT for INTEGER, BYTEA for BLOB, CITEXT for NOCASE
// columns and an ICU collation named "nocase" so COLLATE NOCASE works as is.
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
	var version int
	if err := sqldb.QueryRowContext(pingCtx, `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("db: ping postgres: %w", err)
	}
	if version < minPostgresVersion {
		_ = sqldb.Close()
		return nil, fmt.Errorf("db: PostgreSQL %d.%d is not supported (need 13 or newer)", version/10000, version%10000)
	}
	// The DSN carries credentials: never keep it as the printable path.
	return &DB{DB: sqldb, dialect: DialectPostgres}, nil
}

// minPostgresVersion is the oldest supported server (server_version_num).
const minPostgresVersion = 130000

// dialectQuerier adapts SQLite-flavoured repository queries to PostgreSQL
// (see postgresSQL and postgresArgs). SQLite needs no adaptation, so Wrap only
// uses it for PostgreSQL.
type dialectQuerier struct {
	q    Querier
	inTx bool
}

// Dialect implements the interface DialectOf looks for.
func (dialectQuerier) Dialect() Dialect { return DialectPostgres }

// InTx implements the interface InTx looks for.
func (dq dialectQuerier) InTx() bool { return dq.inTx }

func (dq dialectQuerier) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	query, err := postgresSQL(query)
	if err != nil {
		return nil, err
	}
	return dq.q.ExecContext(ctx, query, postgresArgs(args)...)
}

func (dq dialectQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	query, err := postgresSQL(query)
	if err != nil {
		return nil, err
	}
	return dq.q.QueryContext(ctx, query, postgresArgs(args)...)
}

// QueryRowContext panics on a query mixing placeholder styles (a programming
// error): *sql.Row cannot carry an error constructed outside database/sql.
func (dq dialectQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return dq.q.QueryRowContext(ctx, mustPostgresSQL(query), postgresArgs(args)...)
}

func mustPostgresSQL(query string) string {
	q, err := postgresSQL(query)
	if err != nil {
		panic(fmt.Errorf("%w: %s", err, query))
	}
	return q
}

// postgresArgs converts Go booleans to 0/1: the schema keeps SQLite's INTEGER
// booleans on PostgreSQL too (the SQLite driver does this conversion itself,
// pgx refuses to encode a bool into an integer column).
func postgresArgs(args []any) []any {
	var out []any
	for i, a := range args {
		var v any
		switch x := a.(type) {
		case bool:
			v = boolInt(x)
		case *bool:
			if x != nil {
				v = boolInt(*x)
			}
		case sql.NullBool:
			if x.Valid {
				v = boolInt(x.Bool)
			}
		default:
			continue
		}
		if out == nil {
			out = append([]any(nil), args...)
		}
		out[i] = v
	}
	if out == nil {
		return args
	}
	return out
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ExecContext executes a query, adapting it to PostgreSQL when needed.
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if d.Dialect() == DialectPostgres {
		return dialectQuerier{q: d.DB}.ExecContext(ctx, query, args...)
	}
	return d.DB.ExecContext(ctx, query, args...)
}

// QueryContext executes a query, adapting it to PostgreSQL when needed.
func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if d.Dialect() == DialectPostgres {
		return dialectQuerier{q: d.DB}.QueryContext(ctx, query, args...)
	}
	return d.DB.QueryContext(ctx, query, args...)
}

// QueryRowContext executes a query, adapting it to PostgreSQL when needed.
func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if d.Dialect() == DialectPostgres {
		return dialectQuerier{q: d.DB}.QueryRowContext(ctx, query, args...)
	}
	return d.DB.QueryRowContext(ctx, query, args...)
}

// PrepareContext prepares a statement, adapting the query to PostgreSQL when
// needed. Arguments of the returned statement are passed to the driver as-is
// (no bool conversion); prefer ExecContext/QueryContext.
func (d *DB) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if d.Dialect() == DialectPostgres {
		q, err := postgresSQL(query)
		if err != nil {
			return nil, err
		}
		query = q
	}
	return d.DB.PrepareContext(ctx, query)
}

// Wrap returns q (the DB or a transaction from WithTx/BeginTx) as a Querier
// that adapts queries to the database dialect. On SQLite it returns q as-is.
func (d *DB) Wrap(q Querier) Querier {
	if d.Dialect() == DialectSQLite {
		return q
	}
	switch v := q.(type) {
	case dialectQuerier, *DB:
		return v
	case *sql.DB:
		if v == d.DB {
			return d
		}
	}
	_, inTx := q.(*sql.Tx)
	return dialectQuerier{q: q, inTx: inTx}
}

// DialectOf reports the dialect a Querier obtained from DB or DB.Wrap runs
// on. Raw *sql.DB and *sql.Tx values report SQLite, which is what Wrap returns
// them as.
func DialectOf(q Querier) Dialect {
	if d, ok := q.(interface{ Dialect() Dialect }); ok {
		return d.Dialect()
	}
	return DialectSQLite
}

// InTx reports whether q is bound to a transaction (a *sql.Tx, possibly
// wrapped by DB.Wrap), so callers can join it instead of nesting.
func InTx(q Querier) bool {
	switch v := q.(type) {
	case *sql.Tx:
		return true
	case interface{ InTx() bool }:
		return v.InTx()
	}
	return false
}

// SyncIDSequence moves the id sequence of table past its largest id after rows
// were inserted with explicit ids (legacy ids preserved on import). Only
// PostgreSQL needs it: SQLite's AUTOINCREMENT tracks explicit ids itself.
func SyncIDSequence(ctx context.Context, q Querier, table string) error {
	if DialectOf(q) != DialectPostgres {
		return nil
	}
	_, err := q.ExecContext(ctx, `SELECT setval(pg_get_serial_sequence(?, 'id'), coalesce(max(id), 0) + 1, false) FROM `+table, table)
	if err != nil {
		return fmt.Errorf("db: sync %s id sequence: %w", table, err)
	}
	return nil
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

// Path returns the SQLite database file path ("" for PostgreSQL).
func (d *DB) Path() string { return d.path }

// WithTx runs fn inside a transaction (BEGIN IMMEDIATE on SQLite, a READ
// COMMITTED transaction on PostgreSQL). It commits when fn returns nil and
// rolls back otherwise (also on panic, which is re-raised). Pass tx through
// Wrap before running repository queries on it.
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
