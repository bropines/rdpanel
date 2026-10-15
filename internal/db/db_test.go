package db

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"
)

func openTest(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "panel.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return d
}

var wantTables = []string{
	"address_book_entries", "address_book_entry_tags", "address_book_shares", "address_book_tags",
	"address_books", "admin_audit", "alarm_audits", "audit_nonces", "auth_tokens", "connection_audits", "device_groups", "devices",
	"file_transfer_audits", "identity_providers", "login_events", "migration_runs", "oauth_states",
	"recovery_codes", "server_commands", "server_settings", "settings", "share_links", "totp_secrets",
	"user_groups", "user_identities", "users",
}

func tables(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.Query(`SELECT name FROM sqlite_master WHERE type='table'
		AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

func TestMigrateCreatesSchema(t *testing.T) {
	d := openTest(t)
	got := tables(t, d)
	sort.Strings(wantTables)
	if strings.Join(got, ",") != strings.Join(wantTables, ",") {
		t.Fatalf("tables:\n got %v\nwant %v", got, wantTables)
	}
	v, err := d.SchemaVersion(context.Background())
	if err != nil || v < 3 {
		t.Fatalf("version %d err %v", v, err)
	}
	// Idempotent.
	res, err := d.Migrate(context.Background())
	if err != nil || len(res) != 0 {
		t.Fatalf("second migrate: %v %v", res, err)
	}
}

func TestMigrationsDownUp(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	p, err := goose.NewProvider(goose.DialectSQLite3, d.DB, Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := tables(t, d); len(got) != 0 {
		t.Fatalf("tables left after down: %v", got)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDBFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	d := openTest(t)
	st, err := os.Stat(d.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("db file mode %o, want 600", perm)
	}
}

func TestPragmas(t *testing.T) {
	d := openTest(t)
	var fk, busy int
	var mode string
	_ = d.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	_ = d.QueryRow("PRAGMA busy_timeout").Scan(&busy)
	_ = d.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if fk != 1 || busy != 5000 || mode != "wal" {
		t.Fatalf("fk=%d busy=%d mode=%s", fk, busy, mode)
	}
}

func insertUser(t *testing.T, q Querier, username string) int64 {
	t.Helper()
	res, err := q.ExecContext(context.Background(),
		`INSERT INTO users (username, created_at, updated_at) VALUES (?, 1, 1)`, username)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestConstraintsAndErrorClassification(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	uid := insertUser(t, d, "alice")

	_, err := d.ExecContext(ctx, `INSERT INTO users (username, created_at, updated_at) VALUES ('ALICE', 1, 1)`)
	if !IsUniqueViolation(err) {
		t.Fatalf("case-insensitive unique username: %v", err)
	}
	_, err = d.ExecContext(ctx, `INSERT INTO auth_tokens (user_id, kind, token_sha256, expires_at, created_at)
		VALUES (9999, 'client', zeroblob(32), 1, 1)`)
	if !IsForeignKeyViolation(err) {
		t.Fatalf("fk: %v", err)
	}
	_, err = d.ExecContext(ctx, `INSERT INTO auth_tokens (user_id, kind, token_sha256, expires_at, created_at)
		VALUES (?, 'bogus', zeroblob(32), 1, 1)`, uid)
	if !IsCheckViolation(err) {
		t.Fatalf("check: %v", err)
	}
	_, err = d.ExecContext(ctx, `UPDATE users SET password_hash = 'x' WHERE id = ?`, uid)
	if !IsCheckViolation(err) {
		t.Fatalf("hash without algo must fail: %v", err)
	}
	_, err = d.ExecContext(ctx, `INSERT INTO users (id, username, created_at, updated_at) VALUES (4294967296, 'big', 1, 1)`)
	if !IsCheckViolation(err) {
		t.Fatalf("user id above u32 must fail: %v", err)
	}

	// Cascade: deleting the user removes their tokens.
	if _, err := d.ExecContext(ctx, `INSERT INTO auth_tokens (user_id, kind, token_sha256, expires_at, created_at)
		VALUES (?, 'client', randomblob(32), 1, 1)`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.QueryRow(`SELECT count(*) FROM auth_tokens`).Scan(&n)
	if n != 0 {
		t.Fatalf("tokens not cascaded: %d", n)
	}
}

func TestEntryTagSameBookTrigger(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	uid := insertUser(t, d, "bob")
	mustExec := func(q string, args ...any) int64 {
		t.Helper()
		res, err := d.ExecContext(ctx, q, args...)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	b1 := mustExec(`INSERT INTO address_books (guid, owner_user_id, is_personal, created_at, updated_at) VALUES ('g1', ?, 1, 1, 1)`, uid)
	b2 := mustExec(`INSERT INTO address_books (guid, owner_user_id, created_at, updated_at) VALUES ('g2', ?, 1, 1)`, uid)
	e1 := mustExec(`INSERT INTO address_book_entries (address_book_id, rustdesk_id, created_at, updated_at) VALUES (?, '123', 1, 1)`, b1)
	t1 := mustExec(`INSERT INTO address_book_tags (address_book_id, name, color_argb, created_at, updated_at) VALUES (?, 'x', 4288585374, 1, 1)`, b1)
	t2 := mustExec(`INSERT INTO address_book_tags (address_book_id, name, created_at, updated_at) VALUES (?, 'x', 1, 1)`, b2)

	mustExec(`INSERT INTO address_book_entry_tags (entry_id, tag_id) VALUES (?, ?)`, e1, t1)
	if _, err := d.ExecContext(ctx, `INSERT INTO address_book_entry_tags (entry_id, tag_id) VALUES (?, ?)`, e1, t2); err == nil {
		t.Fatal("cross-book tag link must be rejected")
	}
	// Only one personal book per user.
	if _, err := d.ExecContext(ctx, `INSERT INTO address_books (guid, owner_user_id, is_personal, created_at, updated_at) VALUES ('g3', ?, 1, 1, 1)`, uid); !IsUniqueViolation(err) {
		t.Fatalf("second personal book: %v", err)
	}
	// Share must name exactly one grantee.
	if _, err := d.ExecContext(ctx, `INSERT INTO address_book_shares (address_book_id, permission, created_at, updated_at) VALUES (?, 'read', 1, 1)`, b2); !IsCheckViolation(err) {
		t.Fatalf("grantee check: %v", err)
	}
}

func TestWithTx(t *testing.T) {
	d := openTest(t)
	ctx := context.Background()
	boom := errors.New("boom")
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		insertUser(t, tx, "carol")
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	var n int
	_ = d.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Fatal("rollback did not happen")
	}
	if err := d.WithTx(ctx, func(tx *sql.Tx) error { insertUser(t, tx, "dave"); return nil }); err != nil {
		t.Fatal(err)
	}
	_ = d.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	if n != 1 {
		t.Fatal("commit did not happen")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("panic must propagate")
			}
		}()
		_ = d.WithTx(ctx, func(tx *sql.Tx) error { insertUser(t, tx, "eve"); panic("x") })
	}()
	_ = d.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	if n != 1 {
		t.Fatal("panic must roll back")
	}
}
