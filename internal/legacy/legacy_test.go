package legacy

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
)

var testNow = time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

// legacyDDL returns the production legacy schema without the comment/row-count
// trailer and the internal sqlite_sequence table (created by SQLite itself).
func legacyDDL(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "research", "legacy-rustdeskapi-schema.sql"))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") || strings.HasPrefix(line, "===") ||
			strings.HasPrefix(line, "CREATE TABLE sqlite_sequence") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// newLegacyDB creates a legacy database with the production schema, a
// versions row and whatever fill inserts.
func newLegacyDB(t *testing.T, version int, fill func(x func(q string, args ...any))) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rustdeskapi.db")
	d, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(legacyDDL(t)); err != nil {
		t.Fatal(err)
	}
	x := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	x("INSERT INTO versions (version, created_at, updated_at) VALUES (?, '2024-05-01 10:00:00+08:00', '2024-05-01 10:00:00+08:00')", version)
	if fill != nil {
		fill(x)
	}
	return path
}

func newTarget(t *testing.T) *db.DB {
	t.Helper()
	ctx := context.Background()
	d, err := db.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), db.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return d
}

func newAEAD(t *testing.T) *crypto.AEAD {
	t.Helper()
	a, err := crypto.NewAEAD(make([]byte, crypto.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func md5Legacy(pw string) string {
	s := md5.Sum([]byte(pw + "rustdesk-api"))
	return hex.EncodeToString(s[:])
}

type fixture struct {
	path       string
	bcryptHash string
	md5Hash    string
	jwtGood    string
	jwtWeb     string
	opaque     string
}

const jwtKey = "legacy-jwt-key"

// fullFixture covers every mapping rule and edge case of §7.4.
func fullFixture(t *testing.T) fixture {
	t.Helper()
	N := testNow.Unix()
	j, err := auth.NewClientJWT([]byte(jwtKey), nil)
	if err != nil {
		t.Fatal(err)
	}
	mint := func(uid uint32, exp int64) string {
		s, err := j.Mint(uid, time.Unix(exp, 0))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	bh, err := bcrypt.GenerateFromPassword([]byte("adminpw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{bcryptHash: string(bh), md5Hash: md5Legacy("secret"),
		jwtGood: mint(1, N+30*86400), jwtWeb: mint(1, N+10*86400), opaque: md5Legacy("admin" + "2026-10-14")}

	f.path = newLegacyDB(t, ExpectedVersion, func(x func(string, ...any)) {
		// groups: case-insensitive duplicate name gets renamed.
		x(`INSERT INTO groups (id, name, type, created_at, updated_at) VALUES
			(1, 'Default group', 1, '2024-05-01 10:00:00.123456789+08:00', '2024-05-01 10:00:00.123456789+08:00'),
			(2, 'Share group', 2, '2024-05-01 10:00:00+08:00', '2024-05-01 10:00:00+08:00'),
			(3, 'default GROUP', 1, NULL, NULL)`)
		x(`INSERT INTO device_groups (id, name, created_at, updated_at) VALUES (1, 'Office', '2024-05-01 10:00:00+00:00', NULL)`)

		// users: id 6 and 7 are deleted afterwards so the legacy sequence is 7.
		x(`INSERT INTO users (id, username, email, password, nickname, avatar, group_id, is_admin, status, remark, created_at, updated_at) VALUES
			(1, 'admin', 'Admin@Example.com', ?, 'Administrator', 'https://a/x.png', 1, 1, 1, 'boss', '2024-05-01 10:00:00+08:00', '2024-06-01 10:00:00+08:00'),
			(2, 'alice', '', ?, '', '', 2, 0, 1, '', '2024-05-02 10:00:00+08:00', '2024-05-02 10:00:00+08:00'),
			(3, 'bob', '', '', '', '', 0, 0, 2, '', '2024-05-03 10:00:00+08:00', '2024-05-03 10:00:00+08:00'),
			(4, 'carol', 'carol@gmail.com', '', 'Carol', '', 99, 0, 5, '', '2024-05-04 10:00:00+08:00', '2024-05-04 10:00:00+08:00'),
			(5, 'dave', '', 'weird-format', '', '', 2, 'true', 1, '', NULL, '2025-01-02T03:04:05Z'),
			(7, 'ghost', '', '', '', '', 1, 0, 1, '', NULL, NULL)`, f.bcryptHash, f.md5Hash)
		x(`DELETE FROM users WHERE id = 7`)

		// oauths
		x(`INSERT INTO oauths (id, op, oauth_type, client_id, client_secret, auto_register, scopes, issuer, pkce_enable, pkce_method, created_at, updated_at) VALUES
			(1, 'github', 'github', 'gh-id', 'gh-secret', 1, '', '', NULL, '', '2024-05-01 10:00:00+08:00', '2024-05-01 10:00:00+08:00'),
			(2, 'google', 'google', 'g-id', 'g-secret', NULL, 'openid, email,profile,,email', '', 1, 'plain', '2024-05-01 10:00:00+08:00', '2024-05-01 10:00:00+08:00'),
			(3, 'github', 'github', 'dup', 'dup', 0, '', '', 0, '', NULL, NULL),
			(4, 'weird', 'saml', 'x', 'x', 0, '', '', 0, '', NULL, NULL)`)

		// user_thirds
		x(`INSERT INTO user_thirds (id, user_id, open_id, name, username, email, verified_email, picture, union_id, third_type, oauth_type, op, created_at, updated_at) VALUES
			(1, 1, '12345', 'Admin', 'admin-gh', 'ADMIN@example.com', 1, 'https://gh/p.png', '', '', 'github', 'github', '2024-05-01 10:00:00+08:00', NULL),
			(2, 4, 'carol@gmail.com', 'Carol', NULL, 'carol@gmail.com', NULL, NULL, '', 'google', '', '', NULL, NULL),
			(3, 99, 'x', '', '', '', 0, '', '', '', 'github', 'github', NULL, NULL),
			(4, 2, '12345', '', '', '', 0, '', '', '', 'github', 'github', NULL, NULL),
			(5, 2, '777', '', '', '', 0, '', '', '', 'oidc', 'nope', NULL, NULL)`)

		// peers: rows 1+2 duplicate 111111111 (row 2 wins on last_online_time).
		x(`INSERT INTO peers (row_id, id, cpu, hostname, memory, os, username, uuid, version, user_id, last_online_time, last_online_ip, group_id, alias, created_at, updated_at) VALUES
			(1, '111111111', '', 'pc-old', '16GB', 'Windows', 'winuser', 'uuid-A', '1.4.1', 1, 1000, '9.9.9.9', 0, 'old alias', '2024-06-01 00:00:00+00:00', '2024-06-02 00:00:00+00:00'),
			(2, '111111111', 'i7', 'pc-new', '', '', '', '', '1.4.2', 0, 2000, '1.2.3.4', 1, '', '2024-06-03 00:00:00+00:00', '2024-06-04 00:00:00+00:00'),
			(3, '222222222', 'arm', 'pi', '1GB', 'Linux', 'pi', 'uuid-B', '1.3.0', 99, 0, '', 5, '', NULL, NULL),
			(4, '', '', '', '', '', '', '', '', 0, 0, '', 0, '', NULL, NULL)`)

		// collections: id 3 deleted so the legacy sequence is 3.
		x(`INSERT INTO address_book_collections (id, user_id, name, created_at, updated_at) VALUES
			(1, 1, 'Team book', '2024-07-01 00:00:00+00:00', '2024-07-01 00:00:00+00:00'),
			(2, 99, 'Ghost', NULL, NULL),
			(3, 1, 'deleted', NULL, NULL)`)
		x(`DELETE FROM address_book_collections WHERE id = 3`)

		x(`INSERT INTO address_book_collection_rules (id, user_id, collection_id, rule, type, to_id, created_at, updated_at) VALUES
			(1, 1, 1, 1, 1, 2, NULL, NULL),
			(2, 1, 1, 3, 1, 2, NULL, NULL),
			(3, 1, 1, 2, 2, 2, NULL, NULL),
			(4, 1, 1, 0, 1, 3, NULL, NULL),
			(5, 99, 2, 1, 1, 2, NULL, NULL),
			(6, 1, 1, 1, 1, 99, NULL, NULL),
			(7, 1, 1, 2, 3, 2, NULL, NULL)`)

		x(`INSERT INTO tags (id, name, user_id, color, collection_id, created_at, updated_at) VALUES
			(1, 'work', 1, 4288585374, 0, '2024-01-01 00:00:00+00:00', '2024-01-01 00:00:00+00:00'),
			(2, 'work', 1, 4278190335, 0, '2024-02-01 00:00:00+00:00', '2024-02-01 00:00:00+00:00'),
			(3, 'team', 1, -16777216, 1, NULL, NULL),
			(4, 'ghost', 99, 1, 0, NULL, NULL),
			(5, '', 1, 1, 0, NULL, NULL)`)

		x(`INSERT INTO address_books (row_id, id, username, password, hostname, alias, platform, tags, hash, user_id, force_always_relay, rdp_port, rdp_username, online, login_name, same_server, collection_id, created_at, updated_at) VALUES
			(1, '111111111', 'u', 'old', 'h', 'Old', 'Windows', '["work"]', 'oldhash', 1, 0, '', '', 1, '', 0, 0, '2024-01-01 00:00:00+00:00', '2024-01-01 00:00:00+00:00'),
			(2, '111111111', 'winuser', 'p@ss wörd ', 'pc-new', 'New', 'Windows', '["work","home","work"]', 'aGFzaA==', 1, 1, '3389', 'rdp', 1, 'admin', 0, 0, '2024-01-01 00:00:00+00:00', '2024-02-01 00:00:00+00:00'),
			(3, '333333333', '', '', '', '', 'Mac OS', 'not json', '', 1, 0, '', '', 0, 'bob', 'true', 1, NULL, NULL),
			(4, '444444444', '', '', '', '', 'Linux', '', '', 2, 0, '', '', 0, '', 0, 0, NULL, NULL),
			(5, '555', '', '', '', '', '', '[]', '', 99, 0, '', '', 0, '', 0, 0, NULL, NULL),
			(6, '666', '', '', '', '', '', '[]', '', 1, 0, '', '', 0, '', 0, 2, NULL, NULL),
			(7, '', '', '', '', '', '', '[]', '', 1, 0, '', '', 0, '', 0, 0, NULL, NULL),
			(8, '777777777', '', '', '', '', '', '[1, "x", "x", null]', '', 2, 0, '', '', 0, '', 0, 0, NULL, NULL)`)

		x(`INSERT INTO user_tokens (id, user_id, device_uuid, device_id, token, expired_at, created_at, updated_at) VALUES
			(1, 1, 'uuid-A', '111111111', ?, ?, '2026-10-14 12:00:00+00:00', NULL),
			(2, 1, 'uuid-A', '111111111', ?, ?, NULL, NULL),
			(3, 1, 'uuid-B', '222222222', ?, ?, '2026-10-14 12:00:00+00:00', NULL),
			(4, 1, '', '', ?, ?, '2026-10-14 12:00:00+00:00', NULL),
			(5, 1, 'uuid-A', '111111111', 'expired-token', ?, NULL, NULL),
			(6, 99, 'uuid-A', '111111111', 'orphan-token', ?, NULL, NULL),
			(7, 3, 'uuid-A', '111111111', 'disabled-user-token', ?, NULL, NULL),
			(8, 1, 'uuid-A', '111111111', ?, ?, '2026-10-14 13:00:00+00:00', NULL),
			(9, 2, 'uuid-C', '333333333', ?, ?, NULL, NULL)`,
			f.jwtGood, N+30*86400,
			mint(1, N-3600), N+86400,
			f.opaque, N+3*86400,
			f.jwtWeb, N+2*86400,
			N-10,
			N+86400,
			N+86400,
			f.jwtGood, N+31*86400,
			mint(1, N+86400*20), N+86400*20)

		x(`INSERT INTO login_logs (id, user_id, client, device_id, uuid, ip, type, platform, user_token_id, is_deleted, created_at, updated_at) VALUES
			(1, 1, 'webadmin', '', '', '10.0.0.1', 'account', '', 1, 0, '2026-10-01 10:00:00+08:00', NULL),
			(2, 1, 'client', '111111111', 'uuid-A', '1.2.3.4', 'oauth', 'Windows', 1, 1, '2026-10-02 00:00:00+00:00', NULL),
			(3, 99, NULL, NULL, NULL, NULL, 'weird', NULL, 99, 0, '2025-01-01 00:00:00+00:00', NULL),
			(4, 2, 'client', '', '', '', 'account', '', 2, 0, NULL, NULL)`)

		x(`INSERT INTO audit_conns (id, action, conn_id, peer_id, from_peer, from_name, ip, session_id, type, uuid, close_time, created_at, updated_at) VALUES
			(1, 'new', 5, '111111111', '222222222', 'Bob PC', '5.6.7.8', '18446744073709551615', 1, 'uuid-A', ?, '2026-10-14 12:00:00.5+03:00', '2026-10-14 12:10:00+03:00'),
			(2, '', 6, '111111111', '', '', '', '9007199254740993', 0, '', 0, 'garbage', NULL)`, N-100)

		x(`INSERT INTO audit_files (id, from_peer, info, is_file, path, peer_id, type, uuid, ip, num, from_name, created_at, updated_at) VALUES
			(1, '222222222', '{"ip":"5.6.7.8","name":"Bob","num":2,"files":[["a.txt",10]]}', 1, 'C:\x', '111111111', 1, 'uuid-A', '5.6.7.8', 2, 'Bob', '2026-10-14 12:00:00+00:00', NULL),
			(2, '', 'not json', 0, '/tmp', '111111111', 0, '', '', 0, '', NULL, NULL),
			(3, '', '', 'false', '', '111111111', 0, '', '', 0, '', NULL, NULL)`)

		x(`INSERT INTO share_records (id, user_id, peer_id, share_token, password_type, password, expire, created_at, updated_at) VALUES
			(1, 1, '111111111', 'tok-uuid-1', 'fixed', 'sharepw', 0, '2024-01-01 00:00:00+00:00', NULL),
			(2, 1, '111111111', 'tok2', 'once', '', 60, '2026-10-15 11:59:50+00:00', NULL),
			(3, 1, '111111111', 'tok3', 'once', '', 60, '2024-01-01 00:00:00+00:00', NULL),
			(4, 99, '111111111', 'tok4', 'fixed', '', 0, NULL, NULL),
			(5, 1, '111111111', 'tok-uuid-1', 'fixed', '', 0, NULL, NULL),
			(6, 1, '111111111', '', 'fixed', '', 0, NULL, NULL)`)

		x(`INSERT INTO server_cmds (id, cmd, alias, option, explain, target, created_at, updated_at) VALUES
			(1, 'relay-servers', 'rs', '<host>', 'set relays', '21115', NULL, NULL),
			(2, 'x', '', '', '', '21117', NULL, NULL),
			(3, 'y', '', '', '', '9999', NULL, NULL),
			(4, 'relay-servers', '', '', '', '21115', NULL, NULL)`)
	})
	return f
}

func runImport(t *testing.T, target *db.DB, path string, mod func(*Options)) (*Report, error) {
	t.Helper()
	opts := Options{SourcePath: path, AEAD: newAEAD(t), Now: func() time.Time { return testNow }}
	if mod != nil {
		mod(&opts)
	}
	return Run(context.Background(), target, opts)
}

func hasWarning(r *Report, code, legacyID string) bool {
	for _, w := range r.Warnings {
		if w.Code == code && (legacyID == "" || w.LegacyID == legacyID) {
			return true
		}
	}
	return false
}

func ms(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t.UnixMilli()
}

func TestImportFull(t *testing.T) {
	f := fullFixture(t)
	target := newTarget(t)
	aead := newAEAD(t)
	rep, err := runImport(t, target, f.path, func(o *Options) { o.AEAD = aead })
	if err != nil {
		t.Fatalf("import: %v\n%s", err, rep.JSON())
	}
	if rep.Status != "succeeded" || rep.SourceVersion != 265 || rep.MigrationRunID == 0 || len(rep.Errors) != 0 {
		t.Fatalf("report: %s", rep.JSON())
	}
	ctx := context.Background()
	N := testNow.Unix()

	q := func(query string, args ...any) *sql.Row { return target.QueryRowContext(ctx, query, args...) }
	count := func(table string) int {
		var n int
		if err := q("SELECT count(*) FROM " + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// ---- report counts (read = imported + deduped + orphaned + skipped) ----
	want := map[string][5]int{ // read, imported, deduped, orphaned, skipped
		"groups":                        {3, 3, 0, 0, 0},
		"device_groups":                 {1, 1, 0, 0, 0},
		"users":                         {5, 5, 0, 0, 0},
		"oauths":                        {4, 2, 1, 0, 1},
		"user_thirds":                   {5, 2, 1, 2, 0},
		"peers":                         {4, 2, 1, 0, 1},
		"address_book_collections":      {2, 1, 0, 1, 0},
		"address_book_collection_rules": {7, 2, 1, 2, 2},
		"tags":                          {5, 2, 1, 1, 1},
		"address_books":                 {8, 4, 1, 2, 1},
		"user_tokens":                   {9, 3, 1, 1, 4},
		"login_logs":                    {4, 4, 0, 0, 0},
		"audit_conns":                   {2, 2, 0, 0, 0},
		"audit_files":                   {3, 3, 0, 0, 0},
		"share_records":                 {6, 2, 1, 1, 2},
		"server_cmds":                   {4, 2, 1, 0, 1},
	}
	for table, w := range want {
		c := rep.Tables[table]
		got := [5]int{c.Read, c.Imported, c.Deduped, c.Orphaned, c.Skipped}
		if got != w {
			t.Errorf("%s counts: got %v want %v", table, got, w)
		}
	}
	for table, c := range rep.Tables {
		if c.Read != c.Imported+c.Deduped+c.Orphaned+c.Skipped {
			t.Errorf("%s: counts do not add up: %+v", table, c)
		}
	}
	if rep.Tables["share_records"].Extra["expired"] != 1 {
		t.Errorf("share_records extras: %v", rep.Tables["share_records"].Extra)
	}

	// ---- user_groups ----
	type grp struct {
		name, vis string
		created   int64
	}
	for id, w := range map[int64]grp{
		1: {"Default group", "self", ms("2024-05-01T02:00:00.123Z")},
		2: {"Share group", "shared", ms("2024-05-01T02:00:00Z")},
		3: {"default GROUP (3)", "self", testNow.UnixMilli()},
	} {
		var g grp
		if err := q("SELECT name, visibility, created_at FROM user_groups WHERE id = ?", id).Scan(&g.name, &g.vis, &g.created); err != nil {
			t.Fatal(err)
		}
		if g != w {
			t.Errorf("group %d: got %+v want %+v", id, g, w)
		}
	}
	if !hasWarning(rep, "renamed", "3") {
		t.Error("missing rename warning")
	}

	// ---- users ----
	type usr struct {
		username              string
		email, hash, algo     sql.NullString
		display, avatar, note string
		role, status          string
		group                 sql.NullInt64
		created, updated      int64
		lastLogin             sql.NullInt64
	}
	getUser := func(id int64) usr {
		var u usr
		if err := q(`SELECT username, email, password_hash, password_algo, display_name, avatar_url, note, role, status,
			user_group_id, created_at, updated_at, last_login_at FROM users WHERE id = ?`, id).Scan(&u.username, &u.email, &u.hash,
			&u.algo, &u.display, &u.avatar, &u.note, &u.role, &u.status, &u.group, &u.created, &u.updated, &u.lastLogin); err != nil {
			t.Fatalf("user %d: %v", id, err)
		}
		return u
	}
	u1 := getUser(1)
	if u1.username != "admin" || u1.email.String != "Admin@Example.com" || u1.hash.String != f.bcryptHash || u1.algo.String != "bcrypt" ||
		u1.display != "Administrator" || u1.avatar != "https://a/x.png" || u1.note != "boss" || u1.role != "admin" ||
		u1.status != "active" || u1.group.Int64 != 1 || u1.created != ms("2024-05-01T02:00:00Z") || u1.updated != ms("2024-06-01T02:00:00Z") ||
		u1.lastLogin.Int64 != ms("2026-10-02T00:00:00Z") {
		t.Errorf("user 1: %+v", u1)
	}
	u2 := getUser(2)
	if u2.email.Valid || u2.hash.String != f.md5Hash || u2.algo.String != "legacy_md5_rustdesk_api" || u2.role != "user" ||
		u2.group.Int64 != 2 || !u2.lastLogin.Valid || u2.lastLogin.Int64 != testNow.UnixMilli() {
		t.Errorf("user 2: %+v", u2)
	}
	u3 := getUser(3)
	if u3.hash.Valid || u3.algo.Valid || u3.status != "disabled" || u3.group.Int64 != 1 || u3.lastLogin.Valid {
		t.Errorf("user 3: %+v", u3)
	}
	u4 := getUser(4)
	if u4.status != "disabled" || u4.group.Int64 != 1 || u4.hash.Valid {
		t.Errorf("user 4: %+v", u4)
	}
	u5 := getUser(5)
	if u5.hash.Valid || u5.role != "admin" || u5.created != ms("2025-01-02T03:04:05Z") || u5.created != u5.updated {
		t.Errorf("user 5: %+v", u5)
	}
	for code, id := range map[string]string{"unknown_user_status": "4", "orphan_user_group": "4", "unknown_password_format": "5",
		"user_no_login_method": "3"} {
		if !hasWarning(rep, code, id) {
			t.Errorf("missing warning %s for user %s", code, id)
		}
	}
	if hasWarning(rep, "user_no_login_method", "4") { // carol has a Google identity
		t.Error("carol has an identity")
	}

	// ---- identity_providers ----
	var slug, kind, scopes, method string
	var issuer sql.NullString
	var secretEnc []byte
	var pkce, autoReg, linkEmail int
	if err := q(`SELECT slug, kind, client_secret_enc, issuer, scopes, pkce_enabled, pkce_method, auto_register, link_by_verified_email
		FROM identity_providers WHERE id = 2`).Scan(&slug, &kind, &secretEnc, &issuer, &scopes, &pkce, &method, &autoReg, &linkEmail); err != nil {
		t.Fatal(err)
	}
	if slug != "google" || kind != "google" || issuer.String != "https://accounts.google.com" || scopes != `["openid","email","profile"]` ||
		pkce != 1 || method != "plain" || autoReg != 0 || linkEmail != 0 {
		t.Errorf("provider 2: %s %s %v %s %d %s %d", slug, kind, issuer, scopes, pkce, method, autoReg)
	}
	if s, err := aead.OpenString(secretEnc, crypto.IdentityProviderClientSecretAD(2)); err != nil || s != "g-secret" {
		t.Errorf("provider 2 secret: %q %v", s, err)
	}
	if err := q(`SELECT scopes, pkce_enabled, pkce_method, auto_register, issuer FROM identity_providers WHERE id = 1`).Scan(&scopes, &pkce, &method, &autoReg, &issuer); err != nil {
		t.Fatal(err)
	}
	if scopes != "[]" || pkce != 0 || method != "S256" || autoReg != 1 || issuer.Valid {
		t.Errorf("provider 1: %s %d %s %d %v", scopes, pkce, method, autoReg, issuer)
	}
	if count("identity_providers") != 2 || !hasWarning(rep, "duplicate_provider", "3") || !hasWarning(rep, "null_auto_register", "2") ||
		!hasWarning(rep, "email_linking_changed", "1") {
		t.Error("provider dedupe/warnings")
	}

	// ---- user_identities ----
	var uid, pid int64
	var email sql.NullString
	var verified int
	var username string
	if err := q(`SELECT user_id, provider_id, email, email_verified, username FROM user_identities WHERE subject = '12345'`).Scan(&uid, &pid, &email, &verified, &username); err != nil {
		t.Fatal(err)
	}
	if uid != 1 || pid != 1 || email.String != "admin@example.com" || verified != 1 || username != "admin-gh" {
		t.Errorf("identity 1: %d %d %v %d %s", uid, pid, email, verified, username)
	}
	if err := q(`SELECT user_id, provider_id, email_verified FROM user_identities WHERE subject = 'carol@gmail.com'`).Scan(&uid, &pid, &verified); err != nil {
		t.Fatal(err)
	}
	if uid != 4 || pid != 2 || verified != 0 {
		t.Errorf("identity 2: %d %d %d", uid, pid, verified)
	}
	if !hasWarning(rep, "google_email_subject", "2") {
		t.Error("missing pre-v245 google warning")
	}

	// ---- devices ----
	type dev struct {
		uuid, ip                                       sql.NullString
		hostname, os, cpu, mem, osUser, version, alias string
		owner, group, lastSeen                         sql.NullInt64
		created, updated                               int64
	}
	getDev := func(rid string) dev {
		var d dev
		if err := q(`SELECT uuid, last_seen_ip, hostname, os, cpu, memory, os_username, client_version, alias, owner_user_id,
			device_group_id, last_seen_at, created_at, updated_at FROM devices WHERE rustdesk_id = ?`, rid).Scan(&d.uuid, &d.ip,
			&d.hostname, &d.os, &d.cpu, &d.mem, &d.osUser, &d.version, &d.alias, &d.owner, &d.group, &d.lastSeen, &d.created, &d.updated); err != nil {
			t.Fatalf("device %s: %v", rid, err)
		}
		return d
	}
	d1 := getDev("111111111")
	if d1.uuid.String != "uuid-A" || d1.ip.String != "1.2.3.4" || d1.hostname != "pc-new" || d1.os != "Windows" || d1.cpu != "i7" ||
		d1.mem != "16GB" || d1.osUser != "winuser" || d1.version != "1.4.2" || d1.alias != "old alias" || d1.owner.Int64 != 1 ||
		d1.group.Int64 != 1 || d1.lastSeen.Int64 != 2000*1000 || d1.created != ms("2024-06-01T00:00:00Z") || d1.updated != ms("2024-06-04T00:00:00Z") {
		t.Errorf("device 1: %+v", d1)
	}
	d2 := getDev("222222222")
	if d2.owner.Valid || d2.group.Valid || d2.lastSeen.Valid || d2.ip.Valid || d2.uuid.String != "uuid-B" {
		t.Errorf("device 2: %+v", d2)
	}
	if !hasWarning(rep, "duplicate_peer", "2") || !hasWarning(rep, "orphan_peer_owner", "3") || !hasWarning(rep, "orphan_peer_group", "3") {
		t.Error("peer warnings")
	}

	// ---- address_books ----
	var guid, legacyGUID, name string
	var owner, personal, legacyColl int64
	if err := q(`SELECT guid, legacy_guid, owner_user_id, name, is_personal, legacy_collection_id FROM address_books WHERE id = 1`).Scan(
		&guid, &legacyGUID, &owner, &name, &personal, &legacyColl); err != nil {
		t.Fatal(err)
	}
	if guid != legacyUUID("address_book_collections", 1) || legacyGUID != "1-1-1" || owner != 1 || name != "Team book" || personal != 0 || legacyColl != 1 {
		t.Errorf("collection 1: %s %s %d %s %d %d", guid, legacyGUID, owner, name, personal, legacyColl)
	}
	personalIDs := map[int64]int64{}
	rows, err := target.QueryContext(ctx, `SELECT owner_user_id, id, legacy_guid, legacy_collection_id FROM address_books WHERE is_personal = 1 ORDER BY owner_user_id`)
	if err != nil {
		t.Fatal(err)
	}
	wantGUID := map[int64]string{1: "1-1-0", 2: "2-2-0", 3: "0-3-0", 4: "99-4-0", 5: "2-5-0"}
	for rows.Next() {
		var o, id, lc int64
		var lg string
		if err := rows.Scan(&o, &id, &lg, &lc); err != nil {
			t.Fatal(err)
		}
		personalIDs[o] = id
		if lg != wantGUID[o] || lc != 0 || id <= 3 { // ids after the legacy collection sequence (3)
			t.Errorf("personal book of %d: id %d guid %s", o, id, lg)
		}
	}
	rows.Close()
	if len(personalIDs) != 5 || count("address_books") != 6 {
		t.Errorf("personal books: %v", personalIDs)
	}

	// ---- address_book_shares ----
	var perm string
	if err := q(`SELECT permission FROM address_book_shares WHERE address_book_id = 1 AND grantee_user_id = 2`).Scan(&perm); err != nil || perm != "full" {
		t.Errorf("user share: %s %v", perm, err)
	}
	if err := q(`SELECT permission FROM address_book_shares WHERE address_book_id = 1 AND grantee_group_id = 2`).Scan(&perm); err != nil || perm != "read_write" {
		t.Errorf("group share: %s %v", perm, err)
	}

	// ---- tags ----
	tagColor := func(book int64, name string) int64 {
		var c int64
		if err := q(`SELECT color_argb FROM address_book_tags WHERE address_book_id = ? AND name = ?`, book, name).Scan(&c); err != nil {
			t.Fatalf("tag %d/%s: %v", book, name, err)
		}
		return c
	}
	if c := tagColor(personalIDs[1], "work"); c != 4278190335 {
		t.Errorf("work colour %d", c)
	}
	if c := tagColor(1, "team"); c != 0xFF000000 {
		t.Errorf("team colour %d", c)
	}
	if c := tagColor(personalIDs[1], "home"); c != DefaultTagColor {
		t.Errorf("home colour %d", c)
	}
	if c := tagColor(personalIDs[2], "x"); c != DefaultTagColor {
		t.Errorf("x colour %d", c)
	}
	if count("address_book_tags") != 4 || rep.Tables["address_books"].Extra["tags_created_from_entries"] != 2 {
		t.Errorf("tags: %d %v", count("address_book_tags"), rep.Tables["address_books"].Extra)
	}

	// ---- address_book_entries ----
	var entryID int64
	var alias, osUser, rdpPort, loginName string
	var relay, same int
	var pwEnc, hashEnc []byte
	var created, updated int64
	if err := q(`SELECT id, alias, os_username, rdp_port, login_name, force_always_relay, same_server, password_enc, hash_enc, created_at, updated_at
		FROM address_book_entries WHERE address_book_id = ? AND rustdesk_id = '111111111'`, personalIDs[1]).Scan(
		&entryID, &alias, &osUser, &rdpPort, &loginName, &relay, &same, &pwEnc, &hashEnc, &created, &updated); err != nil {
		t.Fatal(err)
	}
	if alias != "New" || osUser != "winuser" || rdpPort != "3389" || loginName != "admin" || relay != 1 || same != 0 ||
		created != ms("2024-01-01T00:00:00Z") || updated != ms("2024-02-01T00:00:00Z") {
		t.Errorf("entry: %s %s %s %s %d %d", alias, osUser, rdpPort, loginName, relay, same)
	}
	if pw, err := aead.OpenString(pwEnc, crypto.ABEntryPasswordAD(entryID)); err != nil || pw != "p@ss wörd " {
		t.Errorf("entry password %q %v", pw, err)
	}
	if h, err := aead.OpenString(hashEnc, crypto.ABEntryHashAD(entryID)); err != nil || h != "aGFzaA==" {
		t.Errorf("entry hash %q %v", h, err)
	}
	var tagNames []string
	rows, err = target.QueryContext(ctx, `SELECT t.name FROM address_book_entry_tags et JOIN address_book_tags t ON t.id = et.tag_id
		WHERE et.entry_id = ? ORDER BY t.name`, entryID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tagNames = append(tagNames, n)
	}
	rows.Close()
	if strings.Join(tagNames, ",") != "home,work" {
		t.Errorf("entry tags %v", tagNames)
	}
	if err := q(`SELECT same_server, login_name, password_enc, hash_enc FROM address_book_entries WHERE address_book_id = 1 AND rustdesk_id = '333333333'`).Scan(
		&same, &loginName, &pwEnc, &hashEnc); err != nil {
		t.Fatal(err)
	}
	if same != 1 || loginName != "bob" || pwEnc != nil || hashEnc != nil {
		t.Errorf("entry 3: %d %s", same, loginName)
	}
	if count("address_book_entries") != 4 || count("address_book_entry_tags") != 3 || !hasWarning(rep, "invalid_tags_json", "3") {
		t.Errorf("entries %d links %d", count("address_book_entries"), count("address_book_entry_tags"))
	}

	// ---- auth_tokens ----
	type tok struct {
		kind             string
		jwtExp, abs      sql.NullInt64
		expires, created int64
		devID, devUUID   sql.NullString
		legacyID         int64
	}
	getTok := func(token string) tok {
		var k tok
		if err := q(`SELECT kind, jwt_expires_at, absolute_expires_at, expires_at, created_at, device_rustdesk_id, device_uuid, legacy_id
			FROM auth_tokens WHERE token_sha256 = ?`, crypto.HashToken(token)).Scan(&k.kind, &k.jwtExp, &k.abs, &k.expires, &k.created,
			&k.devID, &k.devUUID, &k.legacyID); err != nil {
			t.Fatalf("token: %v", err)
		}
		return k
	}
	k1 := getTok(f.jwtGood)
	if k1.kind != "client" || k1.jwtExp.Int64 != (N+30*86400)*1000 || k1.expires != (N+31*86400)*1000 || k1.abs.Valid ||
		k1.devID.String != "111111111" || k1.devUUID.String != "uuid-A" || k1.legacyID != 8 || k1.created != ms("2026-10-14T13:00:00Z") {
		t.Errorf("jwt token: %+v", k1)
	}
	k3 := getTok(f.opaque)
	if k3.kind != "client" || k3.jwtExp.Valid || k3.expires != (N+3*86400)*1000 || k3.legacyID != 3 {
		t.Errorf("opaque token: %+v", k3)
	}
	k4 := getTok(f.jwtWeb)
	if k4.kind != "web" || k4.jwtExp.Valid || k4.expires != (N+2*86400)*1000 || k4.abs.Int64 != (N+2*86400)*1000 || k4.devID.Valid || k4.devUUID.Valid {
		t.Errorf("web token: %+v", k4)
	}
	if count("auth_tokens") != 3 {
		t.Errorf("tokens: %d", count("auth_tokens"))
	}
	var plainLeak int
	_ = q(`SELECT count(*) FROM auth_tokens WHERE CAST(token_sha256 AS TEXT) LIKE '%eyJ%'`).Scan(&plainLeak)
	if plainLeak != 0 {
		t.Error("plain token stored")
	}
	tx := rep.Tables["user_tokens"].Extra
	if tx["expired"] != 1 || tx["expired_jwt"] != 1 || tx["disabled_user"] != 1 || tx["jwt_user_mismatch"] != 1 || tx["opaque_client_tokens"] != 1 {
		t.Errorf("token extras: %v", tx)
	}
	if !hasWarning(rep, "token_expires_soon", "3") || !hasWarning(rep, "token_expires_soon", "4") || hasWarning(rep, "token_expires_soon", "8") ||
		!hasWarning(rep, "opaque_client_tokens", "") || !hasWarning(rep, "jwt_user_mismatch", "9") {
		t.Error("token warnings")
	}

	// ---- login_events ----
	type le struct {
		user, token                        sql.NullInt64
		method, result                     string
		client, dev, devUUID, platform, ip sql.NullString
		hidden                             int
		created                            int64
	}
	var events []le
	rows, err = target.QueryContext(ctx, `SELECT user_id, token_id, method, result, client_kind, device_rustdesk_id, device_uuid, platform, ip,
		hidden_by_user, created_at FROM login_events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var e le
		if err := rows.Scan(&e.user, &e.token, &e.method, &e.result, &e.client, &e.dev, &e.devUUID, &e.platform, &e.ip, &e.hidden, &e.created); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	rows.Close()
	if len(events) != 4 {
		t.Fatalf("login events: %d", len(events))
	}
	e0, e1, e2, e3 := events[0], events[1], events[2], events[3]
	if e0.user.Int64 != 1 || e0.token.Valid || e0.method != "password" || e0.result != "success" || e0.client.String != "webadmin" ||
		e0.dev.Valid || e0.ip.String != "10.0.0.1" || e0.hidden != 0 || e0.created != ms("2026-10-01T02:00:00Z") {
		t.Errorf("login 1: %+v", e0)
	}
	if e1.method != "oidc" || e1.hidden != 1 || e1.dev.String != "111111111" || e1.devUUID.String != "uuid-A" || e1.platform.String != "Windows" || e1.token.Valid {
		t.Errorf("login 2: %+v", e1)
	}
	if e2.user.Valid || e2.method != "password" || e2.client.Valid || e2.ip.Valid {
		t.Errorf("login 3: %+v", e2)
	}
	if e3.created != testNow.UnixMilli() || e3.user.Int64 != 2 {
		t.Errorf("login 4: %+v", e3)
	}

	// ---- connection_audits ----
	var sessionID, cguid, ctrlName string
	var connType, connID int64
	var started int64
	var ended sql.NullInt64
	var devUUID sql.NullString
	if err := q(`SELECT guid, session_id, conn_type, conn_id, controller_name, started_at, ended_at, device_uuid FROM connection_audits
		WHERE conn_id = 5`).Scan(&cguid, &sessionID, &connType, &connID, &ctrlName, &started, &ended, &devUUID); err != nil {
		t.Fatal(err)
	}
	if cguid != legacyUUID("audit_conns", 1) || sessionID != "18446744073709551615" || connType != 1 || ctrlName != "Bob PC" ||
		started != ms("2026-10-14T09:00:00.5Z") || ended.Int64 != (N-100)*1000 || devUUID.String != "uuid-A" {
		t.Errorf("audit 1: %s %s %d %s %d %v", cguid, sessionID, connType, ctrlName, started, ended)
	}
	if err := q(`SELECT session_id, started_at, ended_at, device_uuid FROM connection_audits WHERE conn_id = 6`).Scan(&sessionID, &started, &ended, &devUUID); err != nil {
		t.Fatal(err)
	}
	if sessionID != "9007199254740993" || started != testNow.UnixMilli() || ended.Valid || devUUID.Valid {
		t.Errorf("audit 2: %s %d %v", sessionID, started, ended)
	}
	if !hasWarning(rep, "bad_timestamp", "") || rep.Tables["audit_conns"].Extra["unparseable_timestamps"] != 1 {
		t.Error("bad timestamp not reported")
	}

	// ---- file_transfer_audits ----
	infos := map[string]string{}
	rows, err = target.QueryContext(ctx, `SELECT path, info FROM file_transfer_audits`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var p, i string
		_ = rows.Scan(&p, &i)
		infos[p] = i
	}
	rows.Close()
	if infos[`C:\x`] != `{"ip":"5.6.7.8","name":"Bob","num":2,"files":[["a.txt",10]]}` || infos["/tmp"] != `{"raw":"not json"}` || infos[""] != "{}" {
		t.Errorf("audit infos: %v", infos)
	}
	var dir, isFile, num int
	if err := q(`SELECT direction, is_file, file_count, controller_name FROM file_transfer_audits WHERE path = 'C:\x'`).Scan(&dir, &isFile, &num, &ctrlName); err != nil {
		t.Fatal(err)
	}
	if dir != 1 || isFile != 1 || num != 2 || ctrlName != "Bob" {
		t.Errorf("file audit: %d %d %d %s", dir, isFile, num, ctrlName)
	}

	// ---- share_links ----
	var linkID int64
	var mode string
	var exp sql.NullInt64
	var lpw []byte
	if err := q(`SELECT id, password_mode, expires_at, password_enc FROM share_links WHERE token_sha256 = ?`, crypto.HashToken("tok-uuid-1")).Scan(&linkID, &mode, &exp, &lpw); err != nil {
		t.Fatal(err)
	}
	if mode != "fixed" || exp.Valid {
		t.Errorf("share 1: %s %v", mode, exp)
	}
	if pw, err := aead.OpenString(lpw, crypto.ShareLinkPasswordAD(linkID)); err != nil || pw != "sharepw" {
		t.Errorf("share password %q %v", pw, err)
	}
	if err := q(`SELECT expires_at, password_enc FROM share_links WHERE token_sha256 = ?`, crypto.HashToken("tok2")).Scan(&exp, &lpw); err != nil {
		t.Fatal(err)
	}
	if exp.Int64 != ms("2026-10-15T12:00:50Z") || lpw != nil {
		t.Errorf("share 2: %v", exp)
	}
	if count("share_links") != 2 {
		t.Errorf("share links %d", count("share_links"))
	}

	// ---- server_commands ----
	var target1, alias1, opt, desc string
	if err := q(`SELECT target, alias, option_hint, description FROM server_commands WHERE cmd = 'relay-servers'`).Scan(&target1, &alias1, &opt, &desc); err != nil {
		t.Fatal(err)
	}
	if target1 != "hbbs" || alias1 != "rs" || opt != "<host>" || desc != "set relays" {
		t.Errorf("server cmd: %s %s %s %s", target1, alias1, opt, desc)
	}
	if err := q(`SELECT target FROM server_commands WHERE cmd = 'x'`).Scan(&target1); err != nil || target1 != "hbbr" {
		t.Errorf("server cmd x: %s %v", target1, err)
	}

	// ---- sequences: legacy ids are never reused ----
	seq := func(table string) int64 {
		var s int64
		if err := q(`SELECT seq FROM sqlite_sequence WHERE name = ?`, table).Scan(&s); err != nil {
			t.Fatalf("seq %s: %v", table, err)
		}
		return s
	}
	if seq("users") < 7 || seq("user_groups") < 3 || seq("identity_providers") < 4 || seq("address_books") < 8 || seq("device_groups") < 1 {
		t.Errorf("sequences: users %d groups %d idp %d books %d", seq("users"), seq("user_groups"), seq("identity_providers"), seq("address_books"))
	}
	if _, err := target.ExecContext(ctx, `INSERT INTO users (username, created_at, updated_at) VALUES ('newbie', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	var newID int64
	_ = q(`SELECT id FROM users WHERE username = 'newbie'`).Scan(&newID)
	if newID != 8 {
		t.Errorf("new user id %d, want 8 (legacy id 7 was deleted, never reuse)", newID)
	}

	// ---- migration_runs ----
	var status, reportJSON string
	var dry int
	var srcVer int
	if err := q(`SELECT status, dry_run, source_version, report FROM migration_runs WHERE id = ?`, rep.MigrationRunID).Scan(&status, &dry, &srcVer, &reportJSON); err != nil {
		t.Fatal(err)
	}
	var stored Report
	if err := json.Unmarshal([]byte(reportJSON), &stored); err != nil {
		t.Fatal(err)
	}
	if status != "succeeded" || dry != 0 || srcVer != 265 || stored.Tables["users"].Imported != 5 {
		t.Errorf("migration run: %s %d %d", status, dry, srcVer)
	}
	// The report never contains secrets.
	full := string(rep.JSON())
	for _, secret := range []string{"gh-secret", "g-secret", "p@ss", "aGFzaA==", "sharepw", f.jwtGood, f.opaque, f.md5Hash, f.bcryptHash} {
		if strings.Contains(full, secret) {
			t.Errorf("report leaks %q", secret)
		}
	}
}

func TestRefusesNonEmptyTargetAndForce(t *testing.T) {
	f := fullFixture(t)
	target := newTarget(t)
	if _, err := runImport(t, target, f.path, nil); err != nil {
		t.Fatal(err)
	}
	rep, err := runImport(t, target, f.path, nil)
	if !errors.Is(err, ErrTargetNotEmpty) || rep == nil || rep.Status != "failed" {
		t.Fatalf("second run: %v", err)
	}
	// --force skips the check; preserved ids now conflict, so the run fails
	// as a whole and changes nothing.
	var before int
	_ = target.QueryRow(`SELECT count(*) FROM login_events`).Scan(&before)
	if _, err := runImport(t, target, f.path, func(o *Options) { o.Force = true }); err == nil {
		t.Fatal("forced re-import over the same ids should fail")
	}
	var after int
	_ = target.QueryRow(`SELECT count(*) FROM login_events`).Scan(&after)
	if before != after {
		t.Fatalf("failed run changed data: %d -> %d", before, after)
	}

	// A target with only an unrelated user is refused without --force ...
	t2 := newTarget(t)
	if _, err := t2.Exec(`INSERT INTO users (id, username, created_at, updated_at) VALUES (100, 'root', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := runImport(t, t2, f.path, nil); !errors.Is(err, ErrTargetNotEmpty) {
		t.Fatalf("got %v", err)
	}
	// ... and accepted with it when ids do not collide.
	rep, err = runImport(t, t2, f.path, func(o *Options) { o.Force = true })
	if err != nil || rep.Status != "succeeded" || !rep.Force {
		t.Fatalf("force: %v", err)
	}
}

func TestDryRun(t *testing.T) {
	f := fullFixture(t)
	target := newTarget(t)
	rep, err := runImport(t, target, f.path, func(o *Options) { o.DryRun = true })
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "dry_run" || !rep.DryRun || rep.Tables["users"].Imported != 5 || rep.Tables["audit_conns"].Imported != 2 {
		t.Fatalf("report: %s", rep.JSON())
	}
	for _, table := range []string{"users", "user_groups", "devices", "auth_tokens", "address_books", "connection_audits", "migration_runs"} {
		var n int
		if err := target.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows after dry run (%v)", table, n, err)
		}
	}
	// The real import still works afterwards and matches the dry run.
	rep2, err := runImport(t, target, f.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, c := range rep.Tables {
		c2 := rep2.Tables[k]
		if c.Imported != c2.Imported || c.Read != c2.Read {
			t.Errorf("%s: dry %+v real %+v", k, c, c2)
		}
	}
}

func TestBlockingUsernameCollision(t *testing.T) {
	path := newLegacyDB(t, ExpectedVersion, func(x func(string, ...any)) {
		x(`INSERT INTO users (id, username, password, status) VALUES (1, 'alice', '', 1), (2, 'Alice', '', 1)`)
		x(`INSERT INTO peers (id, user_id) VALUES ('123', 2)`)
	})
	target := newTarget(t)
	for _, dry := range []bool{true, false} {
		rep, err := runImport(t, target, path, func(o *Options) { o.DryRun = dry })
		if !errors.Is(err, ErrBlocking) || rep.Status != "failed" || len(rep.Errors) != 1 ||
			rep.Errors[0].Code != "username_case_collision" || rep.Errors[0].LegacyID != "2" {
			t.Fatalf("dry=%v: %v %s", dry, err, rep.JSON())
		}
	}
	var users, runs, failed int
	_ = target.QueryRow(`SELECT count(*) FROM users`).Scan(&users)
	_ = target.QueryRow(`SELECT count(*), sum(status = 'failed') FROM migration_runs`).Scan(&runs, &failed)
	if users != 0 || runs != 1 || failed != 1 { // only the non-dry failure is recorded
		t.Fatalf("users %d runs %d failed %d", users, runs, failed)
	}
}

func TestVersionCheck(t *testing.T) {
	path := newLegacyDB(t, 250, func(x func(string, ...any)) {
		x(`INSERT INTO users (id, username, password, status) VALUES (1, 'admin', '', 1)`)
	})
	target := newTarget(t)
	rep, err := runImport(t, target, path, nil)
	if !errors.Is(err, ErrVersion) || rep.SourceVersion != 250 {
		t.Fatalf("got %v", err)
	}
	v := 250
	rep, err = runImport(t, target, path, func(o *Options) { o.AllowVersion = &v })
	if err != nil || rep.Tables["users"].Imported != 1 {
		t.Fatalf("allow-version: %v", err)
	}
}

func TestSourceIsReadOnlyAndUnchanged(t *testing.T) {
	f := fullFixture(t)
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runImport(t, newTarget(t), f.path, nil); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(f.path)
	if string(before) != string(after) {
		t.Fatal("source database was modified")
	}
	if _, err := os.Stat(f.path + "-journal"); err == nil {
		t.Fatal("journal created next to the source")
	}
	src, err := OpenSource(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if _, err := src.Exec(`DELETE FROM users`); err == nil {
		t.Fatal("source should be read-only")
	}
}

func TestRefusesUncheckpointedWAL(t *testing.T) {
	f := fullFixture(t)
	if err := os.WriteFile(f.path+"-wal", []byte("not empty"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runImport(t, newTarget(t), f.path, nil); err == nil || !strings.Contains(err.Error(), "wal") {
		t.Fatalf("got %v", err)
	}
}

func TestEmptyLegacyDatabase(t *testing.T) {
	path := newLegacyDB(t, ExpectedVersion, nil)
	rep, err := runImport(t, newTarget(t), path, nil)
	if err != nil || rep.Status != "succeeded" {
		t.Fatalf("%v", err)
	}
	for k, c := range rep.Tables {
		if c.Read != 0 {
			t.Errorf("%s read %d", k, c.Read)
		}
	}
}
