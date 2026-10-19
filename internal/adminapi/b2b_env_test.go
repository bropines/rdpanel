package adminapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/federation"
	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/hbbstest"
	"github.com/bropines/rdpanel/internal/hbbs/presence"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/logs"
	"github.com/bropines/rdpanel/internal/store"
)

// b2bFakePresence is a PresenceSource with settable state.
type b2bFakePresence struct {
	mu     sync.Mutex
	online map[string]bool
	health presence.Health
}

func (p *b2bFakePresence) IsOnline(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.online[id]
}

func (p *b2bFakePresence) Health() presence.Health {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.health
}

func (p *b2bFakePresence) set(id string, on bool) {
	p.mu.Lock()
	p.online[id] = on
	p.mu.Unlock()
}

// b2env is a test server with a secrets-enabled store, fake presence and
// (optionally) the fake hbbs/hbbr admin channel.
type b2env struct {
	*tenv
	ctx  context.Context
	pres *b2bFakePresence
	fake *hbbstest.FakeAdmin
	ctl  *control.Client
	rec  *reconcile.Reconciler
}

type b2opts struct {
	hbbs         bool
	consoleLimit int
	updates      UpdateChecker
	logs         *logs.Buffer
	logCapture   string
	federation   *federation.Service
}

const testPassword = "correct horse battery"

func newB2Env(t *testing.T, o b2opts) *b2env {
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
	clk := &testClock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	key, _ := crypto.RandomBytes(32)
	aead, _ := crypto.NewAEAD(key)
	st := store.New(d, store.WithSecrets(aead)).WithClock(clk.now)
	jwtSvc, _ := auth.NewClientJWT([]byte("k"), clk.now)
	cfg := config.Defaults()
	cfg.PublicURL = "http://panel.test"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	deps := Deps{Log: log, Config: cfg, Store: st, AEAD: aead, JWT: jwtSvc, Hasher: cheapHasher,
		Limiter:      auth.NewBackoff(auth.BackoffConfig{Free: 50, Base: time.Second, Max: time.Minute, Forget: time.Hour}, clk.now),
		ConsoleLimit: o.consoleLimit, Schema: d, Updates: o.updates, StartedAt: clk.t.Add(-90 * time.Minute),
		Logs: o.logs, LogCapture: o.logCapture, LogSources: []string{"hbbs", "hbbr", "panel"}, Federation: o.federation}
	e := &b2env{ctx: ctx, pres: &b2bFakePresence{online: map[string]bool{}}}
	deps.PresenceSrc = e.pres
	if o.hbbs {
		e.fake = hbbstest.NewFakeAdmin(t)
		e.ctl, err = control.New(control.Config{HBBSAddr: e.fake.Addr(), HBBRAddr: e.fake.Addr(), Timeout: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		e.ctl.SetAudit(NewCommandAudit(st, log))
		e.rec, err = reconcile.New(reconcile.Config{Control: e.ctl, Store: &reconcile.Store{DB: d}, Bus: events.NewBus(nil)})
		if err != nil {
			t.Fatal(err)
		}
		deps.Control, deps.Reconciler = e.ctl, e.rec
	}
	r := chi.NewRouter()
	r.Route("/api/v1/admin", func(r chi.Router) { Register(r, deps) })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	e.tenv = &tenv{t: t, st: st, clk: clk, srv: srv, deps: deps, aead: aead}
	return e
}

// as creates a user with role and returns a logged-in client.
func (e *b2env) as(name string, role store.Role) (*client, *store.User) {
	e.t.Helper()
	u := e.user(name, testPassword, role)
	c := e.client()
	if code, m := c.login(name, testPassword); code != http.StatusOK {
		e.t.Fatalf("login %s: %d %v", name, code, m)
	}
	return c, u
}

// raw performs a request and returns the status and raw body.
func (c *client) b2bRaw(method, path string, body any) (int, string) {
	c.e.t.Helper()
	code, m, _ := c.do(method, path, body)
	b, _ := json.Marshal(m)
	return code, string(b)
}

func want(t *testing.T, what string, code, wantCode int, m map[string]any) {
	t.Helper()
	if code != wantCode {
		t.Fatalf("%s: status %d, want %d: %v", what, code, wantCode, m)
	}
}

func wantErr(t *testing.T, what string, code, wantCode int, m map[string]any, wantErrCode string) {
	t.Helper()
	if code != wantCode || errCode(m) != wantErrCode {
		t.Fatalf("%s: got %d %q, want %d %q: %v", what, code, errCode(m), wantCode, wantErrCode, m)
	}
}

func b2bItems(m map[string]any) []map[string]any {
	raw, _ := m["items"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, it := range raw {
		if mm, ok := it.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}

func b2bNum(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func idOf(m map[string]any) string { return strconv.FormatInt(b2bNum(m["id"]), 10) }

func hasStr(list any, s string) bool {
	l, _ := list.([]any)
	for _, v := range l {
		if v == s {
			return true
		}
	}
	return false
}

func noSecret(t *testing.T, what, body string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(body, s) {
			t.Fatalf("%s: secret %q leaked: %s", what, s, body)
		}
	}
	for _, k := range []string{`"password"`, `"hash"`, `"default_password"`} {
		if strings.Contains(body, k) {
			t.Fatalf("%s: secret field %s returned: %s", what, k, body)
		}
	}
}
