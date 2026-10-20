package db_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/db/dbtest"
)

// These tests need a PostgreSQL server (RDPANEL_TEST_PG_DSN); the SQLite
// schema itself is covered by db_test.go.

func pgOnly(t *testing.T) {
	t.Helper()
	if !dbtest.Postgres() {
		t.Skip(dbtest.EnvPostgresDSN + " not set")
	}
}

func strs(t *testing.T, q db.Querier, query string, args ...any) []string {
	t.Helper()
	rows, err := q.QueryContext(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func sqliteRef(t *testing.T) *db.DB {
	t.Helper()
	d := dbtest.OpenSQLite(t, filepath.Join(t.TempDir(), "ref.db"))
	if _, err := d.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return d
}

const pgTables = `SELECT table_name FROM information_schema.tables
	WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' AND table_name <> 'goose_db_version'`

const sqliteTables = `SELECT name FROM sqlite_master WHERE type = 'table'
	AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version'`

// TestPostgresSchemaMatchesSQLite: same tables, columns (name, type class,
// nullability) and named indexes as the SQLite schema.
func TestPostgresSchemaMatchesSQLite(t *testing.T) {
	pgOnly(t)
	pg, lite := dbtest.Open(t), sqliteRef(t)
	tables := strs(t, lite, sqliteTables)
	if got := strs(t, pg, pgTables); !reflect.DeepEqual(got, tables) {
		t.Fatalf("tables:\n pg     %v\n sqlite %v", got, tables)
	}
	pgType := map[string]string{"INTEGER": "bigint", "TEXT": "text", "BLOB": "bytea", "REAL": "double precision"}
	for _, table := range tables {
		var want, got []string
		rows, err := lite.Query(`SELECT name, type, "notnull", pk FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, typ string
			var notNull, pk int
			if err := rows.Scan(&name, &typ, &notNull, &pk); err != nil {
				t.Fatal(err)
			}
			want = append(want, strings.Join([]string{name, pgType[typ], boolStr(notNull == 1 || pk > 0)}, " "))
		}
		rows.Close()
		rows, err = pg.QueryContext(context.Background(), `SELECT column_name,
			CASE WHEN udt_name = 'citext' THEN 'text' ELSE data_type END, is_nullable = 'NO'
			FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = ?`, table)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var name, typ string
			var notNull bool
			if err := rows.Scan(&name, &typ, &notNull); err != nil {
				t.Fatal(err)
			}
			got = append(got, strings.Join([]string{name, typ, boolStr(notNull)}, " "))
		}
		rows.Close()
		sort.Strings(want)
		sort.Strings(got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s columns:\n pg     %v\n sqlite %v", table, got, want)
		}
	}
	wantIdx := strs(t, lite, `SELECT name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`)
	gotIdx := strs(t, pg, `SELECT indexname FROM pg_indexes WHERE schemaname = current_schema()
		AND indexname NOT LIKE '%\_pkey' AND indexname NOT LIKE '%\_key'`)
	if !reflect.DeepEqual(gotIdx, wantIdx) {
		t.Errorf("indexes:\n pg     %v\n sqlite %v", gotIdx, wantIdx)
	}
}

func boolStr(b bool) string {
	if b {
		return "notnull"
	}
	return "null"
}

func TestPostgresMigrationsDownUp(t *testing.T) {
	pgOnly(t)
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if res, err := d.Migrate(ctx); err != nil || len(res) != 0 {
		t.Fatalf("second migrate: %v %v", res, err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, d.DB, db.MigrationsPG())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := strs(t, d, pgTables); len(got) != 0 {
		t.Fatalf("tables left after down: %v", got)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresRelayDataMigration mirrors TestRelayDataMigration.
func TestPostgresRelayDataMigration(t *testing.T) {
	pgOnly(t)
	ctx := context.Background()
	d := dbtest.OpenEmpty(t)
	p, err := goose.NewProvider(goose.DialectPostgres, d.DB, db.MigrationsPG())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExecContext(ctx, `INSERT INTO server_settings (target, key, value, updated_at)
		VALUES ('hbbs', 'relay_servers', '["r1.example.com:21117","r2.example.com","r1.example.com:21117",5,"a b"]', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var hosts string
	if err := d.QueryRowContext(ctx, `SELECT string_agg(public_host || '/' || kind || '/' || health, ' ' ORDER BY id) FROM relays`).Scan(&hosts); err != nil {
		t.Fatal(err)
	}
	if hosts != "r1.example.com:21117/static/active r2.example.com/static/active" {
		t.Fatalf("relays after up: %q", hosts)
	}
	var n int
	_ = d.QueryRowContext(ctx, `SELECT count(*) FROM server_settings WHERE key = 'relay_servers'`).Scan(&n)
	if n != 0 {
		t.Fatal("relay_servers row kept")
	}
	if _, err := p.DownTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := d.QueryRowContext(ctx, `SELECT value FROM server_settings WHERE target = 'hbbs' AND key = 'relay_servers'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != `["r1.example.com:21117","r2.example.com"]` {
		t.Fatalf("relay_servers after down: %s", v)
	}
}

// TestPostgresConstraintsAndDialect checks the constraints of the SQLite
// schema hold on PostgreSQL and that the query adaptation (placeholders,
// COLLATE NOCASE, LIKE, booleans) gives SQLite's results.
func TestPostgresConstraintsAndDialect(t *testing.T) {
	pgOnly(t)
	ctx := context.Background()
	d := dbtest.Open(t)
	q := d.Wrap(d.DB)
	if db.DialectOf(q) != db.DialectPostgres || db.InTx(q) {
		t.Fatal("dialect/tx detection")
	}
	var uid int64
	if err := q.QueryRowContext(ctx, `INSERT INTO users (username, created_at, updated_at, totp_required)
		VALUES (?, 1, 1, ?) RETURNING id`, "Alice", true).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	var totp bool
	if err := q.QueryRowContext(ctx, `SELECT totp_required FROM users WHERE username = ?`, "aLiCe").Scan(&totp); err != nil || !totp {
		t.Fatalf("case-insensitive username lookup / bool round trip: %v %v", totp, err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO users (username, created_at, updated_at) VALUES ('ALICE', 1, 1)`); !db.IsUniqueViolation(err) {
		t.Fatalf("case-insensitive unique username: %v", err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO auth_tokens (user_id, kind, token_sha256, expires_at, created_at)
		VALUES (9999, 'client', ?, 1, 1)`, make([]byte, 32)); !db.IsForeignKeyViolation(err) {
		t.Fatalf("fk: %v", err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO auth_tokens (user_id, kind, token_sha256, expires_at, created_at)
		VALUES (?, 'bogus', ?, 1, 1)`, uid, make([]byte, 32)); !db.IsCheckViolation(err) {
		t.Fatalf("check: %v", err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO users (id, username, created_at, updated_at) VALUES (4294967296, 'big', 1, 1)`); !db.IsCheckViolation(err) {
		t.Fatalf("user id above u32 must fail: %v", err)
	}

	// Explicit ids (legacy import) followed by generated ones.
	if _, err := q.ExecContext(ctx, `INSERT INTO users (id, username, created_at, updated_at) VALUES (40, 'legacy', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.SyncIDSequence(ctx, q, "users"); err != nil {
		t.Fatal(err)
	}
	var next int64
	if err := q.QueryRowContext(ctx, `INSERT INTO users (username, created_at, updated_at) VALUES ('new', 1, 1) RETURNING id`).Scan(&next); err != nil || next != 41 {
		t.Fatalf("id after explicit ids: %d %v", next, err)
	}

	// Group names are unique ignoring case; COLLATE NOCASE works in queries.
	if _, err := q.ExecContext(ctx, `INSERT INTO user_groups (name, created_at, updated_at) VALUES ('Ops', 1, 1), ('dev', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO user_groups (name, created_at, updated_at) VALUES ('OPS', 1, 1)`); !db.IsUniqueViolation(err) {
		t.Fatalf("case-insensitive unique group name: %v", err)
	}
	if got := strs(t, q, `SELECT name FROM user_groups WHERE name = ? COLLATE NOCASE`, "oPs"); !reflect.DeepEqual(got, []string{"Ops"}) {
		t.Fatalf("COLLATE NOCASE lookup: %v", got)
	}
	var names []string
	rows, err := q.QueryContext(ctx, `SELECT name FROM user_groups ORDER BY name COLLATE NOCASE`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		names = append(names, s)
	}
	rows.Close()
	if strings.Join(names, ",") != "dev,Ops" {
		t.Fatalf("ORDER BY COLLATE NOCASE: %v", names)
	}
	// LIKE is case-insensitive, as on SQLite, on TEXT and CITEXT columns.
	if got := strs(t, q, `SELECT name FROM user_groups WHERE name LIKE ? ESCAPE '\'`, "%P%"); !reflect.DeepEqual(got, []string{"Ops"}) {
		t.Fatalf("LIKE on text: %v", got)
	}
	if got := strs(t, q, `SELECT username FROM users WHERE username LIKE ?1 OR display_name LIKE ?1`, "ALI%"); !reflect.DeepEqual(got, []string{"Alice"}) {
		t.Fatalf("LIKE on citext with numbered placeholder: %v", got)
	}

	// The entry/tag same-book trigger.
	var b1, b2, e1, t1, t2 int64
	scan := func(dst *int64, query string, args ...any) {
		t.Helper()
		if err := q.QueryRowContext(ctx, query, args...).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	scan(&b1, `INSERT INTO address_books (guid, owner_user_id, is_personal, created_at, updated_at) VALUES ('g1', ?, ?, 1, 1) RETURNING id`, uid, true)
	scan(&b2, `INSERT INTO address_books (guid, owner_user_id, created_at, updated_at) VALUES ('g2', ?, 1, 1) RETURNING id`, uid)
	scan(&e1, `INSERT INTO address_book_entries (address_book_id, rustdesk_id, created_at, updated_at) VALUES (?, '123', 1, 1) RETURNING id`, b1)
	scan(&t1, `INSERT INTO address_book_tags (address_book_id, name, color_argb, created_at, updated_at) VALUES (?, 'x', 4288585374, 1, 1) RETURNING id`, b1)
	scan(&t2, `INSERT INTO address_book_tags (address_book_id, name, created_at, updated_at) VALUES (?, 'x', 1, 1) RETURNING id`, b2)
	if _, err := q.ExecContext(ctx, `INSERT INTO address_book_entry_tags (entry_id, tag_id) VALUES (?, ?)`, e1, t1); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO address_book_entry_tags (entry_id, tag_id) VALUES (?, ?)`, e1, t2); err == nil {
		t.Fatal("cross-book tag link must be rejected")
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO address_books (guid, owner_user_id, is_personal, created_at, updated_at) VALUES ('g3', ?, 1, 1, 1)`, uid); !db.IsUniqueViolation(err) {
		t.Fatalf("second personal book: %v", err)
	}

	// Cascade: deleting the user removes their books.
	if _, err := q.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = q.QueryRowContext(ctx, `SELECT count(*) FROM address_books`).Scan(&n)
	if n != 0 {
		t.Fatalf("books not cascaded: %d", n)
	}

	// Transactions are detected through the wrapper.
	if err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if !db.InTx(d.Wrap(tx)) {
			t.Error("InTx false inside WithTx")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestPostgresTransactions: WithTx transactions are serialized (as SQLite's
// BEGIN IMMEDIATE does), Savepoint keeps a transaction usable after a
// recovered constraint error, and InJSONArray binds a list as one parameter.
func TestPostgresTransactions(t *testing.T) {
	pgOnly(t)
	ctx := context.Background()
	d := dbtest.Open(t)

	var firstDone atomic.Bool
	started := make(chan struct{})
	errc := make(chan error, 1)
	go func() {
		errc <- d.WithTx(ctx, func(tx *sql.Tx) error {
			close(started)
			time.Sleep(300 * time.Millisecond)
			firstDone.Store(true)
			return nil
		})
	}()
	<-started
	if err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if !firstDone.Load() {
			t.Error("second transaction ran while the first was open")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}

	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		q := d.Wrap(tx)
		insert := func(name string) error {
			_, err := q.ExecContext(ctx, `INSERT INTO user_groups (name, created_at, updated_at) VALUES (?, 1, 1)`, name)
			return err
		}
		if err := insert("ops"); err != nil {
			return err
		}
		if err := db.Savepoint(ctx, q, func() error { return insert("OPS") }); !db.IsUniqueViolation(err) {
			t.Errorf("savepoint error = %v, want unique violation", err)
		}
		return insert("dev")
	})
	if err != nil {
		t.Fatalf("transaction after a recovered error: %v", err)
	}
	if got := strs(t, d, `SELECT name FROM user_groups WHERE `+db.DialectPostgres.InJSONArray("id", "bigint"), `[1, 2, 3]`); !reflect.DeepEqual(got, []string{"dev", "ops"}) {
		t.Fatalf("groups: %v", got)
	}
}
