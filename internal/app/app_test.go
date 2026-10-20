package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db/dbtest"
	"github.com/bropines/rdpanel/internal/store"
)

func testEnv(t *testing.T, extra map[string]string) (config.LookupFunc, string) {
	t.Helper()
	dir := t.TempDir()
	m := map[string]string{
		"RDPANEL_DATA_DIR":  dir,
		"RDPANEL_LOG_LEVEL": "error",
	}
	for k, v := range extra {
		m[k] = v
	}
	// With RDPANEL_TEST_PG_DSN the app runs on a fresh PostgreSQL database,
	// unless the test picks the database itself.
	_, dbType := m["RDPANEL_DB_TYPE"]
	_, dbURL := m["RDPANEL_DATABASE_URL"]
	if dbtest.Postgres() && !dbType && !dbURL {
		m["RDPANEL_DATABASE_URL"] = dbtest.PostgresURL(t)
	}
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }, dir
}

func newTestApp(t *testing.T, extra map[string]string) *App {
	t.Helper()
	env, _ := testEnv(t, extra)
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func do(t *testing.T, h http.Handler, method, path, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if remote != "" {
		req.RemoteAddr = remote
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutes(t *testing.T) {
	a := newTestApp(t, map[string]string{"RDPANEL_PUBLIC_URL": "https://desk.example.test"})
	h := a.Handler()

	rec := do(t, h, http.MethodGet, "/healthz", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("healthz: %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Security-Policy") == "" || rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("security headers missing")
	}

	// Relay agent installer: public, templated with the public URL, not the SPA.
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		rec = do(t, h, m, "/agent/install.sh", "", nil)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/x-shellscript") {
			t.Fatalf("%s install.sh: %d %q", m, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if body := do(t, h, http.MethodGet, "/agent/install.sh", "", nil).Body.String(); !strings.Contains(body, "DEFAULT_PANEL_URL='https://desk.example.test'") ||
		!strings.Contains(body, "DEFAULT_IMAGE='ghcr.io/bropines/rdpanel:latest'") {
		t.Fatalf("install.sh not templated:\n%.500s", body)
	}

	rec = do(t, h, http.MethodGet, "/metrics", "127.0.0.1:1234", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "rdpanel_http_requests_total") {
		t.Fatalf("metrics loopback: %d", rec.Code)
	}
	for _, series := range []string{"rdpanel_build_info{", "rdpanel_auth_login_attempts_total{",
		"rdpanel_auth_client_tokens_issued_total{", "rdpanel_audit_events_total{", "rdpanel_hbbr_relay_sessions "} {
		if !strings.Contains(rec.Body.String(), series) {
			t.Fatalf("metrics: %s missing", series)
		}
	}
	if strings.Contains(rec.Body.String(), "rdpanel_presence_device_online") {
		t.Fatal("per-device presence metrics must be opt-in")
	}
	rec = do(t, h, http.MethodGet, "/metrics", "127.0.0.1:1234", map[string]string{"X-Forwarded-For": "198.51.100.1"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("metrics via proxy must be forbidden: %d", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/metrics", "203.0.113.5:1234", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("metrics external: %d", rec.Code)
	}

	for _, p := range []string{"/api/nope", "/api/v1/admin/nope", "/api/v1/events/nope"} {
		rec = do(t, h, http.MethodGet, p, "", nil)
		var body map[string]map[string]string
		if rec.Code != 404 || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body["error"]["code"] != "not_found" {
			t.Fatalf("%s: %d %s", p, rec.Code, rec.Body.String())
		}
	}

	rec = do(t, h, http.MethodGet, "/devices", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("spa: %d", rec.Code)
	}
}

func TestKeysGenerated(t *testing.T) {
	a := newTestApp(t, nil)
	dir := a.Config.DataDir
	files := []string{"secret.key", "jwt.key"}
	if a.Config.DBType == "sqlite" {
		files = append(files, "panel.db")
	}
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not created: %v", f, err)
		}
	}
	tok, err := a.JWT.Mint(1, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.JWT.Verify(tok); err != nil {
		t.Fatal(err)
	}
}

func TestJWTKeyFromEnvIsVerbatim(t *testing.T) {
	a := newTestApp(t, map[string]string{"RUSTDESK_API_JWT_KEY": " spaced key "})
	if _, err := os.Stat(filepath.Join(a.Config.DataDir, "jwt.key")); !os.IsNotExist(err) {
		t.Fatal("jwt.key must not be generated when the env key is set")
	}
	// A token signed with the exact env bytes verifies.
	other := newTestApp(t, map[string]string{"RUSTDESK_API_JWT_KEY": " spaced key "})
	tok, _ := other.JWT.Mint(5, time.Now().Add(time.Hour))
	if _, err := a.JWT.Verify(tok); err != nil {
		t.Fatal(err)
	}
	trimmed := newTestApp(t, map[string]string{"RUSTDESK_API_JWT_KEY": "spaced key"})
	if _, err := trimmed.JWT.Verify(tok); err == nil {
		t.Fatal("trimmed key must not verify")
	}
}

func TestJanitorPass(t *testing.T) {
	a := newTestApp(t, nil)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour).UnixMilli()
	fresh := time.Now().Add(time.Hour).UnixMilli()
	u := &store.User{Username: "j"}
	if err := a.Store.Users.Create(ctx, u); err != nil {
		t.Fatal(err)
	}
	rnd := func() []byte { b, _ := crypto.RandomBytes(32); return b }
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO auth_tokens (user_id, kind, token_sha256, expires_at, created_at) VALUES (?, 'client', ?, ?, ?)`, []any{u.ID, rnd(), old, old}},
		{`INSERT INTO auth_tokens (user_id, kind, token_sha256, expires_at, created_at) VALUES (?, 'client', ?, ?, ?)`, []any{u.ID, rnd(), fresh, old}},
		{`INSERT INTO oauth_states (state_sha256, action, flow, created_at, expires_at) VALUES (?, 'webauth', 'client', ?, ?)`, []any{rnd(), old, old}},
		{`INSERT INTO audit_nonces (nonce, created_at) VALUES ('n-old', ?)`, []any{old}},
		{`INSERT INTO audit_nonces (nonce, created_at) VALUES ('n-new', ?)`, []any{time.Now().UnixMilli()}},
	}
	for _, s := range stmts {
		if _, err := a.DB.ExecContext(ctx, s.q, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	a.janitorPass(ctx)
	count := func(q string) int {
		var n int
		_ = a.DB.QueryRowContext(ctx, q).Scan(&n)
		return n
	}
	if n := count(`SELECT count(*) FROM auth_tokens`); n != 1 {
		t.Errorf("tokens left = %d", n)
	}
	if n := count(`SELECT count(*) FROM oauth_states`); n != 0 {
		t.Errorf("states left = %d", n)
	}
	if n := count(`SELECT count(*) FROM audit_nonces`); n != 1 {
		t.Errorf("nonces left = %d", n)
	}
}

func TestServeGracefulShutdown(t *testing.T) {
	a := newTestApp(t, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.ServeListener(ctx, ln) }()

	url := "http://" + ln.Addr().String() + "/healthz"
	var resp *http.Response
	for i := 0; i < 50; i++ {
		if resp, err = http.Get(url); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func runCLI(t *testing.T, env config.LookupFunc, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Main(context.Background(), args, env, Stdio{In: strings.NewReader(stdin), Out: &out, Err: &errOut})
	return code, out.String(), errOut.String()
}

func TestCLIUserLifecycle(t *testing.T) {
	env, dir := testEnv(t, nil)
	const pw = "correct horse battery"

	code, out, errOut := runCLI(t, env, pw+"\n", "user", "create", "admin", "--admin", "--email", "a@example.test")
	if code != 0 || !strings.Contains(out, `created user "admin"`) || !strings.Contains(out, "role admin") {
		t.Fatalf("create: %d %q %q", code, out, errOut)
	}
	if strings.Contains(out+errOut, pw) {
		t.Fatal("password echoed")
	}
	if code, _, errOut = runCLI(t, env, pw+"\n", "user", "create", "ADMIN"); code != 1 || !strings.Contains(errOut, "already exists") {
		t.Fatalf("duplicate: %d %q", code, errOut)
	}
	if code, _, errOut = runCLI(t, env, "short\n", "user", "create", "bob"); code != 1 || !strings.Contains(errOut, "at least") {
		t.Fatalf("short pw: %d %q", code, errOut)
	}

	// Verify the stored hash and audit trail through the store.
	cfg, _ := config.Load(env)
	a, err := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	u, err := a.Store.Users.GetByUsername(ctx, "admin")
	if err != nil || !u.IsAdmin() || u.PasswordAlgo != "argon2id" || !a.Hasher.Verify(u.PasswordHash, pw) {
		t.Fatalf("user: %+v %v", u, err)
	}
	_ = a.Store.AuthTokens.Create(ctx, &store.AuthToken{UserID: u.ID, Kind: store.TokenWeb,
		TokenSHA256: bytes.Repeat([]byte{1}, 32), ExpiresAt: time.Now().Add(time.Hour)})
	_ = a.Close()

	const pw2 = "another long password"
	code, out, errOut = runCLI(t, env, pw2+"\r\n", "user", "reset-password", "--password-stdin", "admin")
	if code != 0 || !strings.Contains(out, "revoked 1 token") {
		t.Fatalf("reset: %d %q %q", code, out, errOut)
	}
	a2, _ := New(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer a2.Close()
	u, _ = a2.Store.Users.GetByUsername(ctx, "admin")
	if !a2.Hasher.Verify(u.PasswordHash, pw2) || a2.Hasher.Verify(u.PasswordHash, pw) {
		t.Fatal("password not reset")
	}
	entries, _ := a2.Store.AdminAudit.List(ctx, store.AuditFilter{ActionPrefix: "user."})
	if len(entries) != 2 || entries[0].Action != "user.reset_password" || entries[1].ActorName != "cli" {
		t.Fatalf("audit: %+v", entries)
	}
	for _, e := range entries {
		js, _ := json.Marshal(e.Details)
		if strings.Contains(string(js), pw) || strings.Contains(string(js), pw2) {
			t.Fatal("password in audit details")
		}
	}

	if code, _, errOut = runCLI(t, env, pw+"\n", "user", "reset-password", "ghost"); code != 1 || !strings.Contains(errOut, "not found") {
		t.Fatalf("unknown user: %d %q", code, errOut)
	}
	_ = dir
}

func TestCLIConfigPrintAndUsage(t *testing.T) {
	secret := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	env, _ := testEnv(t, map[string]string{"RUSTDESK_API_JWT_KEY": "topsecret", "RDPANEL_SECRET_KEY": secret})
	code, out, _ := runCLI(t, env, "", "config", "print")
	if code != 0 || !strings.Contains(out, "listen: :21114") || strings.Contains(out, "topsecret") || strings.Contains(out, secret) {
		t.Fatalf("config print: %d\n%s", code, out)
	}
	if code, out, _ = runCLI(t, env, "", "version"); code != 0 || !strings.HasPrefix(out, "rdpanel ") {
		t.Fatalf("version: %d %q", code, out)
	}
	if code, _, _ = runCLI(t, env, "", "bogus"); code != 2 {
		t.Fatalf("unknown command exit %d", code)
	}
	if code, _, _ = runCLI(t, env, ""); code != 2 {
		t.Fatalf("no command exit %d", code)
	}
	if code, out, _ = runCLI(t, env, "", "--help"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Fatalf("help: %d", code)
	}
	if code, _, _ = runCLI(t, env, "", "user", "create"); code != 2 {
		t.Fatalf("missing username exit %d", code)
	}
	bad := func(k string) (string, bool) {
		if k == "RDPANEL_LISTEN" {
			return "nope", true
		}
		return env(k)
	}
	if code, _, _ = runCLI(t, bad, "", "config", "print"); code != 1 {
		t.Fatalf("invalid config exit %d", code)
	}
}

func TestUpdateCheckWiring(t *testing.T) {
	if a := newTestApp(t, nil); a.Updates == nil || a.StartedAt.IsZero() {
		t.Fatalf("default: updates=%v started=%v", a.Updates, a.StartedAt)
	}
	if a := newTestApp(t, map[string]string{"RDPANEL_UPDATE_CHECK": "false"}); a.Updates != nil {
		t.Fatal("RDPANEL_UPDATE_CHECK=false must disable the update check")
	}
}
