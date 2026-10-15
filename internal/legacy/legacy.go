// Package legacy imports a lejianwen/rustdesk-api v2.7 SQLite database
// (`rustdeskapi.db`) into the rustdesk-panel schema.
//
// The source file is opened read-only and immutable; the whole import runs in
// one transaction on the target database and follows the mapping of
// docs/research/legacy-model-and-migration.md §7.4 (operator guide:
// docs/migration.md). Every run produces a Report with per-table counts
// (read = imported + deduped + orphaned + skipped) and warnings.
package legacy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/db"
)

// ExpectedVersion is the legacy `versions.version` this importer was written
// for (rustdesk-api v2.7, DatabaseVersion = 265).
const ExpectedVersion = 265

// SourceName is recorded in migration_runs.source.
const SourceName = "lejianwen/rustdesk-api"

// DefaultTagColor is the ARGB colour given to tags that exist only inside
// address_books.tags (no legacy `tags` row): 0xFF9E9E9E, Material grey 500.
const DefaultTagColor int64 = 0xFF9E9E9E

// tokenExpiryWarnWindow: unexpired tokens closer than this to expiry are reported.
const tokenExpiryWarnWindow = 7 * 24 * time.Hour

// Errors returned by Run.
var (
	// ErrTargetNotEmpty: the target already has users or a completed import (use Force).
	ErrTargetNotEmpty = errors.New("legacy: target database is not empty")
	// ErrVersion: the legacy schema version is not the expected one (use AllowVersion).
	ErrVersion = errors.New("legacy: unexpected legacy schema version")
	// ErrBlocking: the report contains errors that make the import unsafe;
	// nothing was committed. See Report.Errors.
	ErrBlocking = errors.New("legacy: import blocked by errors (see report)")
)

// Options configure Run.
type Options struct {
	// SourcePath is the legacy rustdeskapi.db file.
	SourcePath string
	// DryRun performs the full import and rolls it back.
	DryRun bool
	// Force imports even if the target already has users or a completed import.
	Force bool
	// AllowVersion, when non-nil, accepts this legacy schema version instead of 265.
	AllowVersion *int
	// LegacyLocation interprets legacy timestamps that carry no UTC offset
	// (default UTC). Values written by rustdesk-api always carry one.
	LegacyLocation *time.Location
	// AEAD encrypts secrets (address-book passwords/hashes, OAuth client
	// secrets, share-link passwords). Required.
	AEAD *crypto.AEAD
	// Now overrides the clock (tests). Default time.Now.
	Now func() time.Time
	// Log receives progress messages (optional).
	Log *slog.Logger
}

// Counts are per-legacy-table statistics. Read = Imported + Deduped + Orphaned + Skipped.
type Counts struct {
	Target   string `json:"target"`
	Read     int    `json:"read"`
	Imported int    `json:"imported"`
	// Deduped rows were merged into another row with the same natural key.
	Deduped int `json:"deduped"`
	// Orphaned rows referenced a missing (or not imported) parent and were dropped.
	Orphaned int `json:"orphaned"`
	// Skipped rows were dropped deliberately (expired, invalid, disabled owner, ...).
	Skipped int `json:"skipped"`
	// Extra holds table-specific numbers (e.g. derived rows created).
	Extra map[string]int `json:"extra,omitempty"`
}

// Issue is a warning or error attached to a legacy row (or the whole import).
type Issue struct {
	Code     string `json:"code"`
	Table    string `json:"table,omitempty"`
	LegacyID string `json:"legacy_id,omitempty"`
	Message  string `json:"message"`
}

// Report summarises one run. It never contains secrets.
type Report struct {
	Source         string             `json:"source"`
	SourcePath     string             `json:"source_path"`
	SourceVersion  int                `json:"source_version"`
	DryRun         bool               `json:"dry_run"`
	Force          bool               `json:"force"`
	Status         string             `json:"status"` // succeeded | failed | dry_run
	StartedAt      time.Time          `json:"started_at"`
	FinishedAt     time.Time          `json:"finished_at"`
	MigrationRunID int64              `json:"migration_run_id,omitempty"`
	Tables         map[string]*Counts `json:"tables"`
	Warnings       []Issue            `json:"warnings"`
	Errors         []Issue            `json:"errors"`
}

// tableOrder lists legacy tables in import order (also the report order in logs).
var tableOrder = []struct{ legacy, target string }{
	{"groups", "user_groups"},
	{"device_groups", "device_groups"},
	{"users", "users"},
	{"oauths", "identity_providers"},
	{"user_thirds", "user_identities"},
	{"peers", "devices"},
	{"address_book_collections", "address_books"},
	{"address_book_collection_rules", "address_book_shares"},
	{"tags", "address_book_tags"},
	{"address_books", "address_book_entries"},
	{"user_tokens", "auth_tokens"},
	{"login_logs", "login_events"},
	{"audit_conns", "connection_audits"},
	{"audit_files", "file_transfer_audits"},
	{"share_records", "share_links"},
	{"server_cmds", "server_commands"},
}

func newReport(opts *Options, now time.Time) *Report {
	r := &Report{Source: SourceName, SourcePath: opts.SourcePath, DryRun: opts.DryRun, Force: opts.Force,
		StartedAt: now, Tables: map[string]*Counts{}, Warnings: []Issue{}, Errors: []Issue{}}
	for _, t := range tableOrder {
		r.Tables[t.legacy] = &Counts{Target: t.target}
	}
	return r
}

// JSON renders the report (indented).
func (r *Report) JSON() []byte {
	b, _ := json.MarshalIndent(r, "", "  ")
	return append(b, '\n')
}

// Summary renders a short human-readable table.
func (r *Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "legacy import (%s, schema %d): %s\n", r.SourcePath, r.SourceVersion, r.Status)
	fmt.Fprintf(&b, "%-31s %-22s %6s %8s %7s %8s %7s\n", "legacy table", "new table", "read", "imported", "deduped", "orphaned", "skipped")
	for _, t := range tableOrder {
		c := r.Tables[t.legacy]
		fmt.Fprintf(&b, "%-31s %-22s %6d %8d %7d %8d %7d\n", t.legacy, c.Target, c.Read, c.Imported, c.Deduped, c.Orphaned, c.Skipped)
	}
	fmt.Fprintf(&b, "warnings: %d, errors: %d\n", len(r.Warnings), len(r.Errors))
	for _, e := range r.Errors {
		fmt.Fprintf(&b, "  ERROR %s %s %s: %s\n", e.Code, e.Table, e.LegacyID, e.Message)
	}
	return b.String()
}

func (r *Report) warn(code, table string, id any, format string, args ...any) {
	r.Warnings = append(r.Warnings, Issue{Code: code, Table: table, LegacyID: idString(id), Message: fmt.Sprintf(format, args...)})
}

func (r *Report) fail(code, table string, id any, format string, args ...any) {
	r.Errors = append(r.Errors, Issue{Code: code, Table: table, LegacyID: idString(id), Message: fmt.Sprintf(format, args...)})
}

func idString(id any) string {
	if id == nil {
		return ""
	}
	return fmt.Sprint(id)
}

// OpenSource opens a legacy database read-only and immutable. It refuses a
// database with a non-empty write-ahead log, because immutable mode would
// silently ignore the un-checkpointed transactions in it.
func OpenSource(path string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("legacy: source: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("legacy: source %s is not a regular file", abs)
	}
	if wal, err := os.Stat(abs + "-wal"); err == nil && wal.Size() > 0 {
		return nil, fmt.Errorf("legacy: %s-wal exists and is not empty; stop rustdesk-api and checkpoint it "+
			"(sqlite3 %s 'PRAGMA wal_checkpoint(TRUNCATE)') or copy the database with `sqlite3 .backup` first", abs, abs)
	}
	esc := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(filepath.ToSlash(abs))
	if !strings.HasPrefix(esc, "/") {
		esc = "/" + esc // Windows drive paths: file:/C:/...
	}
	src, err := sql.Open("sqlite", "file:"+esc+"?mode=ro&immutable=1")
	if err != nil {
		return nil, fmt.Errorf("legacy: open source: %w", err)
	}
	src.SetMaxOpenConns(1)
	var n int
	if err := src.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&n); err != nil {
		_ = src.Close()
		return nil, fmt.Errorf("legacy: %s is not a readable SQLite database: %w", abs, err)
	}
	return src, nil
}

// Run imports the legacy database at opts.SourcePath into target (already
// migrated). The returned report is non-nil whenever the source could be
// opened, also on error. Nothing is committed on error or in dry-run mode.
func Run(ctx context.Context, target *db.DB, opts Options) (*Report, error) {
	if opts.AEAD == nil {
		return nil, errors.New("legacy: AEAD is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.LegacyLocation == nil {
		opts.LegacyLocation = time.UTC
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	now := opts.Now().UTC()
	rep := newReport(&opts, now)

	src, err := OpenSource(opts.SourcePath)
	if err != nil {
		return nil, err
	}
	defer src.Close()

	version, err := sourceVersion(ctx, src)
	if err != nil {
		return rep, err
	}
	rep.SourceVersion = version
	want := ExpectedVersion
	if opts.AllowVersion != nil {
		want = *opts.AllowVersion
	}
	if version != want {
		rep.Status = "failed"
		rep.fail("version", "versions", nil, "legacy schema version is %d, expected %d (use --allow-version %d to override)", version, want, version)
		return rep, fmt.Errorf("%w: got %d, want %d", ErrVersion, version, want)
	}

	tx, err := target.BeginTx(ctx, nil)
	if err != nil {
		return rep, fmt.Errorf("legacy: begin: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := checkTargetEmpty(ctx, tx, opts.Force); err != nil {
		rep.Status = "failed"
		rep.fail("target_not_empty", "", nil, "%v", err)
		return rep, err
	}

	im := &importer{ctx: ctx, src: src, tx: tx, opts: &opts, now: now, nowMs: now.UnixMilli(), rep: rep}
	res, err := tx.ExecContext(ctx, `INSERT INTO migration_runs (source, source_path, source_version, dry_run, status, started_at)
		VALUES (?, ?, ?, ?, 'running', ?)`, SourceName, opts.SourcePath, version, boolInt(opts.DryRun), im.nowMs)
	if err != nil {
		return rep, fmt.Errorf("legacy: record run: %w", err)
	}
	runID, _ := res.LastInsertId()

	if err := im.run(); err != nil {
		rep.Status = "failed"
		rep.FinishedAt = opts.Now().UTC()
		_ = tx.Rollback()
		committed = true
		if !opts.DryRun {
			recordFailedRun(ctx, target, &opts, rep)
		}
		return rep, err
	}

	rep.FinishedAt = opts.Now().UTC()
	switch {
	case len(rep.Errors) > 0:
		rep.Status = "failed"
		_ = tx.Rollback()
		committed = true
		if !opts.DryRun {
			recordFailedRun(ctx, target, &opts, rep)
		}
		return rep, ErrBlocking
	case opts.DryRun:
		rep.Status = "dry_run"
		return rep, nil // deferred rollback
	}
	rep.Status = "succeeded"
	rep.MigrationRunID = runID
	if _, err := tx.ExecContext(ctx, `UPDATE migration_runs SET status = 'succeeded', report = ?, finished_at = ? WHERE id = ?`,
		string(rep.JSON()), rep.FinishedAt.UnixMilli(), runID); err != nil {
		return rep, fmt.Errorf("legacy: record run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		rep.Status = "failed"
		rep.MigrationRunID = 0
		return rep, fmt.Errorf("legacy: commit: %w", err)
	}
	committed = true
	return rep, nil
}

// recordFailedRun stores a failed attempt outside the rolled-back transaction
// (best effort; it is bookkeeping only).
func recordFailedRun(ctx context.Context, target *db.DB, opts *Options, rep *Report) {
	res, err := target.ExecContext(ctx, `INSERT INTO migration_runs
		(source, source_path, source_version, dry_run, status, report, started_at, finished_at)
		VALUES (?, ?, ?, 0, 'failed', ?, ?, ?)`, SourceName, opts.SourcePath, rep.SourceVersion,
		string(rep.JSON()), rep.StartedAt.UnixMilli(), rep.FinishedAt.UnixMilli())
	if err == nil {
		rep.MigrationRunID, _ = res.LastInsertId()
	}
}

func sourceVersion(ctx context.Context, src *sql.DB) (int, error) {
	ok, err := tableExists(ctx, src, "versions")
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, nil
	}
	var v sql.NullInt64
	err = src.QueryRowContext(ctx, `SELECT CAST(version AS INTEGER) FROM versions ORDER BY id DESC LIMIT 1`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("legacy: read versions: %w", err)
	}
	return int(v.Int64), nil
}

func tableExists(ctx context.Context, q db.Querier, name string) (bool, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("legacy: inspect source: %w", err)
	}
	return n > 0, nil
}

func checkTargetEmpty(ctx context.Context, tx *sql.Tx, force bool) error {
	if force {
		return nil
	}
	var users, runs int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users`).Scan(&users); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM migration_runs WHERE status = 'succeeded' AND dry_run = 0`).Scan(&runs); err != nil {
		return err
	}
	if users > 0 || runs > 0 {
		return fmt.Errorf("%w: %d user(s), %d completed import(s); use --force to import anyway", ErrTargetNotEmpty, users, runs)
	}
	return nil
}
