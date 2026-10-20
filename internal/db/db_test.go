package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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
	"punch_requests", "recovery_codes", "server_commands", "server_settings", "settings", "share_links", "totp_secrets",
	"user_groups", "user_identities", "users",
	"panel_assets", "presence_samples", // 00005_b2a_panel
	"webauthn_challenges", "webauthn_credentials", "webauthn_user_handles", // 00008_webauthn_passkeys
	"email_codes",                              // 00009_email_codes
	"relay_agents", "relay_settings", "relays", // 00010_relay_agents
	"api_tokens",                           // 00011_api_tokens
	"relay_traffic_samples",                // 00014_relay_traffic_samples
	"relay_prefix_rules",                   // 00015_relay_prefix_rules
	"federation_peers", "federated_relays", // 00016_federation
	"client_strategies",                // 00017_strategies
	"connection_acls", "control_roles", // 00018_acl
	"ldap_settings",      // 00019_ldap
	"server_share_links", // 00022_server_share_links
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

// TestRelayDataMigration: the admin-typed relay list becomes static relays
// (00010) and comes back on Down.
func TestRelayDataMigration(t *testing.T) {
	ctx := context.Background()
	d, err := Open(ctx, filepath.Join(t.TempDir(), "panel.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	p, err := goose.NewProvider(goose.DialectSQLite3, d.DB, Migrations())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO server_settings (target, key, value, updated_at)
		VALUES ('hbbs', 'relay_servers', '["r1.example.com:21117","r2.example.com","r1.example.com:21117"]', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var hosts string
	if err := d.QueryRow(`SELECT group_concat(public_host || '/' || kind || '/' || health, ' ') FROM (SELECT * FROM relays ORDER BY id)`).Scan(&hosts); err != nil {
		t.Fatal(err)
	}
	if hosts != "r1.example.com:21117/static/active r2.example.com/static/active" {
		t.Fatalf("relays after up: %q", hosts)
	}
	var n int
	_ = d.QueryRow(`SELECT count(*) FROM server_settings WHERE key = 'relay_servers'`).Scan(&n)
	if n != 0 {
		t.Fatal("relay_servers row kept")
	}
	if _, err := p.DownTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := d.QueryRow(`SELECT value FROM server_settings WHERE target = 'hbbs' AND key = 'relay_servers'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != `["r1.example.com:21117","r2.example.com"]` {
		t.Fatalf("relay_servers after down: %s", v)
	}
}

// TestStrategySeedDefaultMigration: 00020 drops the default flag of the
// untouched seeded "Standard Office" (so an upgrade pushes nothing to devices
// without a strategy) but keeps a default the admin chose.
func TestStrategySeedDefaultMigration(t *testing.T) {
	ctx := context.Background()
	defaults := func(d *DB) string {
		t.Helper()
		var names sql.NullString
		if err := d.QueryRow(`SELECT group_concat(name, ',') FROM client_strategies WHERE is_default = 1`).Scan(&names); err != nil {
			t.Fatal(err)
		}
		return names.String
	}
	for _, tc := range []struct {
		name   string
		before string // SQL run at version 19
		want   string
	}{
		{"untouched seed", ``, ""},
		{"edited seed", `UPDATE client_strategies SET note = 'ours', updated_at = 1760000000000 WHERE name = 'Standard Office'`, "Standard Office"},
		{"admin default", `UPDATE client_strategies SET is_default = (name = 'Strict Security'), updated_at = 1760000000000 WHERE name = 'Strict Security' OR is_default = 1`, "Strict Security"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := Open(ctx, filepath.Join(t.TempDir(), "panel.db"), Options{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { d.Close() })
			p, err := goose.NewProvider(goose.DialectSQLite3, d.DB, Migrations())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.UpTo(ctx, 19); err != nil {
				t.Fatal(err)
			}
			if got := defaults(d); got != "Standard Office" {
				t.Fatalf("seeded default: %q", got)
			}
			if tc.before != "" {
				if _, err := d.Exec(tc.before); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := p.UpTo(ctx, 20); err != nil {
				t.Fatal(err)
			}
			if got := defaults(d); got != tc.want {
				t.Fatalf("default after 00020: %q, want %q", got, tc.want)
			}
			if _, err := p.DownTo(ctx, 19); err != nil {
				t.Fatal(err)
			}
			if got := defaults(d); got == "" {
				t.Fatal("down left no default")
			}
		})
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

func TestMigrationsPG(t *testing.T) {
	fsys := MigrationsPG()
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 20 {
		t.Fatalf("expected at least 20 migrations in migrations_pg, got %d", len(entries))
	}

	sqliteEntries, err := fs.ReadDir(Migrations(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(sqliteEntries) {
		t.Fatalf("migration count mismatch: pg=%d sqlite=%d", len(entries), len(sqliteEntries))
	}

	// Versions increase strictly (branches may reserve numbers, so gaps are
	// allowed) and both dialects have the same files.
	prev := 0
	for i, entry := range entries {
		var v int
		if _, err := fmt.Sscanf(entry.Name(), "%05d_", &v); err != nil || len(entry.Name()) < 6 || entry.Name()[5] != '_' || v <= prev {
			t.Errorf("entry %d: %q is not a version above %05d", i, entry.Name(), prev)
		}
		prev = v
		if sqliteEntries[i].Name() != entry.Name() {
			t.Errorf("entry %d: pg %q, sqlite %q", i, entry.Name(), sqliteEntries[i].Name())
		}
		if !strings.HasSuffix(entry.Name(), ".sql") {
			t.Errorf("entry %q does not end with .sql", entry.Name())
		}
		content, err := fs.ReadFile(fsys, entry.Name())
		if err != nil {
			t.Errorf("failed to read %s: %v", entry.Name(), err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("migration %s is empty", entry.Name())
		}
		if !strings.Contains(string(content), "-- +goose Up") {
			t.Errorf("migration %s missing '-- +goose Up'", entry.Name())
		}
	}
}

func TestPostgresHelpers(t *testing.T) {
	uniqErr := &pgconn.PgError{Code: "23505"}
	if !IsUniqueViolation(uniqErr) {
		t.Error("expected 23505 to be unique violation")
	}
	fkErr := &pgconn.PgError{Code: "23503"}
	if !IsForeignKeyViolation(fkErr) {
		t.Error("expected 23503 to be foreign key violation")
	}
	chkErr := &pgconn.PgError{Code: "23514"}
	if !IsCheckViolation(chkErr) {
		t.Error("expected 23514 to be check violation")
	}
	otherErr := &pgconn.PgError{Code: "42P01"}
	if IsUniqueViolation(otherErr) || IsForeignKeyViolation(otherErr) || IsCheckViolation(otherErr) {
		t.Error("other error should not be violation")
	}
}

// A transaction wrapped for Postgres must still be seen as a transaction, or
// store.Tx/atomic open a second, independent transaction inside it.
func TestIsTxSeesWrappedTransactions(t *testing.T) {
	d := openTest(t)
	tx, err := d.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	pg := &DB{dialect: DialectPostgres}
	for name, q := range map[string]Querier{
		"sqlite tx":         d.Wrap(tx),
		"postgres tx":       pg.Wrap(tx),
		"postgres tx twice": pg.Wrap(pg.Wrap(tx)),
	} {
		if !IsTx(q) {
			t.Errorf("%s: not a transaction", name)
		}
	}
	for name, q := range map[string]Querier{"sqlite db": d.Wrap(d), "postgres db": pg.Wrap(d.DB), "db": d} {
		if IsTx(q) {
			t.Errorf("%s: seen as a transaction", name)
		}
	}
}

func TestDialectAndWrap(t *testing.T) {
	dSqlite := &DB{dialect: DialectSQLite}
	if dSqlite.Dialect() != DialectSQLite {
		t.Fatalf("got %v", dSqlite.Dialect())
	}
	wrapped := dSqlite.Wrap(dSqlite)
	if _, ok := wrapped.(dialectQuerier); ok {
		t.Error("sqlite wrap should return underlying querier directly")
	}

	dPg := &DB{dialect: DialectPostgres}
	if dPg.Dialect() != DialectPostgres {
		t.Fatalf("got %v", dPg.Dialect())
	}
}
