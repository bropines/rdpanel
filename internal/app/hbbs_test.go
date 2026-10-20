package app

import (
	"bufio"
	"context"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/events"
)

func TestHBBSWiring(t *testing.T) {
	a := newTestApp(t, nil)
	if a.Events == nil || a.HBBS.Presence == nil || a.HBBS.Control == nil || a.HBBS.Reconciler == nil {
		t.Fatalf("hbbs integration not wired: %+v", a.HBBS)
	}
	off := newTestApp(t, map[string]string{"RDPANEL_HBBS_ENABLED": "false"})
	if off.HBBS.Presence != nil || off.HBBS.Reconciler != nil || off.Events == nil {
		t.Fatal("disabled integration must not create workers")
	}
	// Non-loopback rendezvous address: presence only, no admin channel.
	remote := newTestApp(t, map[string]string{"RDPANEL_HBBS_ID_SERVER": "10.0.0.5:21116"})
	if remote.HBBS.Presence == nil || remote.HBBS.Control != nil || remote.HBBS.Reconciler != nil {
		t.Fatalf("non-loopback wiring %+v", remote.HBBS)
	}
}

func authzRequest(t *testing.T, h http.Handler, method, path, remote, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = remote
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The hbbs authorization endpoint lives on the main router, generates its
// secret file on first start and answers loopback callers with the secret
// only.
func TestHBBSAuthzEndpoint(t *testing.T) {
	a := newTestApp(t, nil)
	path := a.Config.HBBS.AuthzSecretFile
	if filepath.Base(path) != "hbbs-authz.key" || filepath.Dir(path) != a.Config.DataDir {
		t.Fatalf("secret file %q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimSpace(string(raw))
	if b, err := hex.DecodeString(secret); err != nil || len(b) != 32 {
		t.Fatalf("secret file content %q", raw)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Fatalf("secret file mode %o", st.Mode().Perm())
		}
	}
	h := a.Handler()
	body := `{"target_id":"123456789","token":"","conn_type":0,"controller_ip":"203.0.113.1","kind":"punch_hole"}`
	rec := authzRequest(t, h, http.MethodPost, "/internal/hbbs/authorize", "127.0.0.1:50000", secret, body)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"allow":true,"permissions":"0","reason":""}` {
		t.Fatalf("authorize: %d %s", rec.Code, rec.Body.String())
	}
	for name, c := range map[string]struct{ method, path, remote, secret string }{
		"no secret":     {http.MethodPost, "/internal/hbbs/authorize", "127.0.0.1:50000", ""},
		"wrong secret":  {http.MethodPost, "/internal/hbbs/authorize", "127.0.0.1:50000", strings.Repeat("0", 64)},
		"remote peer":   {http.MethodPost, "/internal/hbbs/authorize", "198.51.100.4:50000", secret},
		"GET":           {http.MethodGet, "/internal/hbbs/authorize", "127.0.0.1:50000", secret},
		"other path":    {http.MethodPost, "/internal/hbbs/other", "127.0.0.1:50000", secret},
		"namespace":     {http.MethodGet, "/internal/", "127.0.0.1:50000", ""},
		"trailing path": {http.MethodPost, "/internal/hbbs/authorize/x", "127.0.0.1:50000", secret},
	} {
		rec := authzRequest(t, h, c.method, c.path, c.remote, c.secret, body)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"not_found"`) {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	metrics := do(t, h, http.MethodGet, "/metrics", "127.0.0.1:1234", nil).Body.String()
	for _, series := range []string{`rdpanel_hbbs_authz_total{result="allow"} 1`, `rdpanel_hbbs_authz_total{result="deny"} 0`,
		`rdpanel_hbbs_authz_total{result="error"} 2`} {
		if !strings.Contains(metrics, series) {
			t.Errorf("metrics: %s missing", series)
		}
	}
	// The generated file is reused on the next start.
	b, err := New(context.Background(), a.Config, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	_ = b.Close()
	if again, _ := os.ReadFile(path); string(again) != string(raw) {
		t.Fatal("secret file rewritten")
	}
}

func TestHBBSAuthzDisabled(t *testing.T) {
	a := newTestApp(t, map[string]string{"RDPANEL_HBBS_AUTHZ": "false"})
	if _, err := os.Stat(a.Config.HBBS.AuthzSecretFile); !os.IsNotExist(err) {
		t.Fatalf("secret file written while disabled: %v", err)
	}
	rec := authzRequest(t, a.Handler(), http.MethodPost, "/internal/hbbs/authorize", "127.0.0.1:50000", "", `{"target_id":"1"}`)
	if rec.Code == http.StatusOK || strings.Contains(rec.Body.String(), "permissions") {
		t.Fatalf("endpoint mounted while disabled: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(do(t, a.Handler(), http.MethodGet, "/metrics", "127.0.0.1:1234", nil).Body.String(), "rdpanel_hbbs_authz_total") {
		t.Fatal("authz metric registered while disabled")
	}
}

// An operator-provided secret file is used as is; a too short one stops
// the start.
func TestHBBSAuthzOwnSecretFile(t *testing.T) {
	own := filepath.Join(t.TempDir(), "authz.key")
	if err := os.WriteFile(own, []byte("operator-secret-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t, map[string]string{"RDPANEL_HBBS_AUTHZ_SECRET_FILE": own})
	rec := authzRequest(t, a.Handler(), http.MethodPost, "/internal/hbbs/authorize", "[::1]:50000", "operator-secret-0123456789",
		`{"target_id":"123456789"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("own secret: %d %s", rec.Code, rec.Body.String())
	}

	if err := os.WriteFile(own, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env, _ := testEnv(t, map[string]string{"RDPANEL_HBBS_AUTHZ_SECRET_FILE": own})
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := New(context.Background(), cfg, slog.New(slog.DiscardHandler)); err == nil {
		_ = b.Close()
		t.Fatal("short secret accepted")
	}
}

func TestEventsEndpoint(t *testing.T) {
	a := newTestApp(t, map[string]string{"RDPANEL_HBBS_ENABLED": "false"})
	// Default: no authenticator -> deny.
	rec := do(t, a.Handler(), http.MethodGet, "/api/v1/events", "127.0.0.1:1", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("default deny: %d", rec.Code)
	}

	a.EventsAuth = events.AuthenticatorFunc(func(r *http.Request) (events.Principal, error) {
		if r.Header.Get("X-Test") == "ok" {
			return events.Principal{UserID: 1, Role: events.RoleAdmin}, nil
		}
		return events.Principal{}, events.ErrUnauthenticated
	})
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	req.Header.Set("X-Test", "ok")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Scan() // retry:
	for a.Events.Subscribers() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	a.Events.Publish(events.PresenceChanged, events.Presence{ID: "123456789", Online: true, Source: "hbbs"}, events.Admins)
	var got []string
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			got = append(got, l)
		}
		if len(got) == 3 {
			break
		}
	}
	if len(got) != 3 || got[1] != "event: presence.changed" || !strings.Contains(got[2], `"id":"123456789"`) {
		t.Fatalf("stream %q", got)
	}
}
