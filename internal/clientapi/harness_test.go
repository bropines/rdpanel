package clientapi

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/config"
	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/db"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

// cheapHasher keeps argon2id fast under the race detector.
var cheapHasher = &auth.Hasher{Params: auth.Argon2Params{Memory: 64, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type fakeTOTP struct{ codes map[int64]string }

func (f *fakeTOTP) Enrolled(_ context.Context, id int64) (bool, error) {
	_, ok := f.codes[id]
	return ok, nil
}
func (f *fakeTOTP) Verify(_ context.Context, id int64, code string) (bool, error) {
	c, ok := f.codes[id]
	return ok && c == code, nil
}

type env struct {
	t     *testing.T
	h     http.Handler
	st    *store.Store
	clk   *clock
	kicks *MemKickQueue
	totp  *fakeTOTP
	deps  Deps
}

type envOpt func(*Deps)

func newEnv(t *testing.T, opts ...envOpt) *env {
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
	clk := &clock{t: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)}
	aead, _ := crypto.NewAEAD(bytes.Repeat([]byte{9}, crypto.KeySize))
	var (
		mu sync.Mutex
		n  int
	)
	gen := func() string { mu.Lock(); defer mu.Unlock(); n++; return fmt.Sprintf("guid-%d", n) }
	st := store.New(d, store.WithSecrets(aead), store.WithIDGenerator(gen)).WithClock(clk.now)
	jwt, _ := auth.NewClientJWT([]byte("test-jwt-key"), clk.now)
	cfg := config.Defaults()
	cfg.Auth.JWTLifetime = 168 * time.Hour
	kicks := NewMemKickQueue(0, clk.now)
	totp := &fakeTOTP{codes: map[int64]string{}}
	deps := Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Config: cfg, Store: st, AEAD: aead, JWT: jwt,
		Hasher: cheapHasher, Limiter: auth.NewBackoff(auth.DefaultBackoffConfig, clk.now), TOTP: totp,
		Kicks: kicks, DeviceRate: RateConfig{Rate: -1}, Now: clk.now,
	}
	for _, o := range opts {
		o(&deps)
	}
	r := chi.NewRouter()
	r.Use(httpx.RealIP(nil, "X-Forwarded-For"))
	r.Route("/api", func(api chi.Router) {
		api.NotFound(httpx.NotFound)
		api.MethodNotAllowed(httpx.MethodNotAllowed)
		Register(api, deps)
	})
	return &env{t: t, h: r, st: st, clk: clk, kicks: kicks, totp: totp, deps: deps}
}

type resp struct {
	code int
	hdr  http.Header
	body string
}

// do sends a request; body "" sends no body (Content-Length: 0).
func (e *env) do(method, path, body, token string) resp {
	e.t.Helper()
	req := newReq(method, path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return e.serve(req)
}

func newReq(method, path, body string) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Content-Type", "application/json")
	return req
}

func (e *env) serve(req *http.Request) resp {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return resp{code: rec.Code, hdr: rec.Header(), body: rec.Body.String()}
}

func (e *env) mustDo(method, path, body, token string, code int) resp {
	e.t.Helper()
	r := e.do(method, path, body, token)
	if r.code != code {
		e.t.Fatalf("%s %s: status %d (want %d) body %s", method, path, r.code, code, r.body)
	}
	return r
}

// empty asserts the action-endpoint success contract: 200 and an empty body.
func (e *env) empty(method, path, body, token string) {
	e.t.Helper()
	r := e.do(method, path, body, token)
	if r.code != http.StatusOK || r.body != "" {
		e.t.Fatalf("%s %s: want 200 + empty body, got %d %q", method, path, r.code, r.body)
	}
}

// golden compares body with testdata/<name>.json (or rewrites it with -update).
func golden(t *testing.T, name, body string) {
	t.Helper()
	path := filepath.Join("testdata", name+".json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (run go test -update)", name, err)
	}
	if string(want) != body {
		t.Fatalf("golden %s mismatch:\n got: %s\nwant: %s", name, body, want)
	}
}

func (e *env) golden(name string, r resp, code int) {
	e.t.Helper()
	if r.code != code {
		e.t.Fatalf("%s: status %d (want %d) body %s", name, r.code, code, r.body)
	}
	golden(e.t, name, r.body)
}

func (e *env) user(name, password string, mut func(u *store.User)) *store.User {
	e.t.Helper()
	u := &store.User{Username: name, DisplayName: strings.ToUpper(name[:1]) + name[1:], Email: name + "@example.test"}
	if password != "" {
		hash, algo, err := cheapHasher.Hash(password)
		if err != nil {
			e.t.Fatal(err)
		}
		u.PasswordHash, u.PasswordAlgo = hash, string(algo)
	}
	if mut != nil {
		mut(u)
	}
	if err := e.st.Users.Create(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	return u
}

// login logs in through POST /api/login and returns the access token.
func (e *env) login(name, password, deviceID, uuid string) string {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"username": name, "password": password, "id": deviceID, "uuid": uuid,
		"autoLogin": true, "type": "account", "deviceInfo": map[string]string{"os": "linux", "type": "client", "name": "HOST"}})
	r := e.mustDo("POST", "/api/login", string(body), "", 200)
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(r.body), &out); err != nil || out.AccessToken == "" {
		e.t.Fatalf("login: %s", r.body)
	}
	return out.AccessToken
}

// device registers a device through POST /api/sysinfo.
func (e *env) device(id, uuid string) *store.Device {
	e.t.Helper()
	body, _ := json.Marshal(map[string]any{"id": id, "uuid": uuid, "cpu": "Intel, 3.6GHz, 8/4 cores",
		"memory": "15.87GB", "os": "windows / Windows 11 Pro - 10.0.22631", "hostname": "HOST-" + id,
		"username": "alice", "version": "1.4.9"})
	e.mustDo("POST", "/api/sysinfo", string(body), "", 200)
	d, err := e.st.Devices.GetByRustdeskID(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return d
}

func scrub(body, secret, placeholder string) string {
	return strings.ReplaceAll(body, secret, placeholder)
}
