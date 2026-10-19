package adminapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/events"
	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/hbbstest"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/relays"
	"github.com/bropines/rdpanel/internal/store"
)

type relayEnv struct {
	*b2env
	svc    *relays.Service
	rstore *relays.Store
}

// newRelayEnv wires the admin API with a relays.Service as the reconciler's
// relay source (as internal/app does).
func newRelayEnv(t *testing.T) *relayEnv {
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
	clk := &testClock{t: time.Now().UTC()}
	key, _ := crypto.RandomBytes(32)
	aead, _ := crypto.NewAEAD(key)
	st := store.New(d, store.WithSecrets(aead)).WithClock(clk.now)
	jwtSvc, _ := auth.NewClientJWT([]byte("k"), clk.now)
	cfg := config.Defaults()
	cfg.PublicURL = "http://panel.test"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e := &relayEnv{b2env: &b2env{ctx: ctx, pres: &b2bFakePresence{online: map[string]bool{}}}}
	e.rstore = &relays.Store{DB: d}
	e.svc, err = relays.New(relays.Config{Store: e.rstore, Panel: st, Log: log,
		PublicKey: func() (string, error) { return "OeVuKk5nlHiXp+APNn0Y3pC1Iwpwn44JGqrQCsWqmBw=", nil },
		PublicURL: cfg.PublicURL, Prober: &relays.Prober{Timeout: time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	e.fake = hbbstest.NewFakeAdmin(t)
	e.ctl, _ = control.New(control.Config{HBBSAddr: e.fake.Addr(), HBBRAddr: e.fake.Addr(), Timeout: 2 * time.Second})
	e.rec, err = reconcile.New(reconcile.Config{Control: e.ctl, Store: &reconcile.Store{DB: d}, Bus: events.NewBus(nil), Relays: e.svc})
	if err != nil {
		t.Fatal(err)
	}
	e.svc.SetPusher(e.rec, e.rec)
	e.svc.SetLocal(e.ctl, func() bool { return true })
	deps := Deps{Log: log, Config: cfg, Store: st, AEAD: aead, JWT: jwtSvc, Hasher: cheapHasher,
		Limiter: auth.NewBackoff(auth.BackoffConfig{Free: 50, Base: time.Second, Max: time.Minute, Forget: time.Hour}, clk.now),
		Schema:  d, PresenceSrc: e.pres, Control: e.ctl, Reconciler: e.rec, Relays: e.svc}
	r := chi.NewRouter()
	r.Route("/api/v1/admin", func(r chi.Router) { Register(r, deps) })
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	e.tenv = &tenv{t: t, st: st, clk: clk, srv: srv, deps: deps, aead: aead}
	return e
}

func TestRelaysAPI(t *testing.T) {
	e := newRelayEnv(t)
	admin, _ := e.as("root", store.RoleAdmin)
	user, _ := e.as("bob", store.RoleUser)
	code, m, _ := user.do(http.MethodGet, "/relays", nil)
	wantErr(t, "user list", code, 403, m, "forbidden")

	code, m, _ = admin.do(http.MethodPut, "/relays/options", map[string]any{"require_approval": true, "allow_private_hosts": true})
	want(t, "options", code, 200, m)

	fr := hbbstest.NewFakeRelay(t, "OeVuKk5nlHiXp+APNn0Y3pC1Iwpwn44JGqrQCsWqmBw=")
	code, m, _ = admin.do(http.MethodPost, "/relays", map[string]any{"kind": "static", "name": "partner", "public_host": fr.Addr(), "weight": 3})
	want(t, "create static", code, 201, m)
	rel := m["relay"].(map[string]any)
	sid := int64(b2bNum(rel["id"]))
	if rel["health"] != "pending" || m["enrollment"] != nil || rel["agent"] != nil {
		t.Fatalf("static: %v", m)
	}
	code, m, _ = admin.do(http.MethodPost, "/relays", map[string]any{"kind": "static", "name": "partner", "public_host": "x.example.com"})
	wantErr(t, "duplicate", code, 409, m, "conflict")
	code, m, _ = admin.do(http.MethodPost, "/relays", map[string]any{"kind": "static", "name": "bad", "public_host": "a,b"})
	wantErr(t, "bad host", code, 400, m, "validation_failed")

	// Probe now -> active, in rotation with weight 3; hbbs gets the list.
	code, m, _ = admin.do(http.MethodPost, "/relays/"+itoa(sid)+"/probe", nil)
	want(t, "probe", code, 200, m)
	if m["health"] != "active" || b2bNum(m["rotation_weight"]) != 3 {
		t.Fatalf("after probe: %v", m)
	}
	e.svc.Pass(e.ctx, false) // pushes the rotation through the reconciler
	if relaysNow, _, _, _, _, _, _, _ := e.fake.State(); strings.Join(relaysNow, ",") != strings.Repeat(fr.Addr()+",", 2)+fr.Addr() {
		t.Fatalf("hbbs rs: %v", relaysNow)
	}
	code, m, _ = admin.do(http.MethodGet, "/relays/rotation", nil)
	want(t, "rotation", code, 200, m)
	if len(m["servers"].([]any)) != 3 || len(m["observed"].([]any)) != 3 {
		t.Fatalf("rotation: %v", m)
	}
	// relay_servers is computed now: the old settings key is refused.
	code, m, _ = admin.do(http.MethodPatch, "/server/settings", map[string]any{"relay_servers": []string{"x.example.com"}})
	wantErr(t, "relay_servers patch", code, 400, m, "validation_failed")
	code, m, _ = admin.do(http.MethodGet, "/server/settings", nil)
	want(t, "server settings", code, 200, m)
	if d := m["desired"].(map[string]any); len(d["relay_servers"].([]any)) != 3 {
		t.Fatalf("desired relay_servers: %v", d)
	}

	// Drain.
	code, m, _ = admin.do(http.MethodPatch, "/relays/"+itoa(sid), map[string]any{"admin_state": "draining", "region": "DE"})
	want(t, "drain", code, 200, m)
	if m["admin_state"] != "draining" || m["region"] != "DE" || b2bNum(m["rotation_weight"]) != 0 {
		t.Fatalf("drained: %v", m)
	}
	code, m, _ = admin.do(http.MethodPatch, "/relays/"+itoa(sid), map[string]any{"mgmt_host": "100.64.0.1:21117"})
	want(t, "patch mgmt_host", code, 200, m)
	if m["mgmt_host"] != "100.64.0.1:21117" {
		t.Fatalf("mgmt_host after patch: %v", m)
	}
	code, m, _ = admin.do(http.MethodPatch, "/relays/"+itoa(sid), map[string]any{"mgmt_host": nil})
	want(t, "clear mgmt_host", code, 200, m)
	if m["mgmt_host"] != nil {
		t.Fatalf("mgmt_host after clear: %v", m)
	}
	code, m, _ = admin.do(http.MethodPatch, "/relays/"+itoa(sid), map[string]any{"mgmt_host": "bad,host"})
	wantErr(t, "bad mgmt_host", code, 400, m, "validation_failed")

	code, m, _ = admin.do(http.MethodPatch, "/relays/"+itoa(sid), map[string]any{"weight": 9})
	wantErr(t, "weight", code, 400, m, "validation_failed")
	code, m, _ = admin.do(http.MethodPatch, "/relays/"+itoa(sid), map[string]any{"bogus": 1})
	wantErr(t, "unknown field", code, 400, m, "validation_failed")

	// Agent relay: enrollment material once.
	code, m, _ = admin.do(http.MethodPost, "/relays", map[string]any{"kind": "agent", "name": "fra-1", "public_host": "fra-1.example.com", "mgmt_host": "10.0.0.1:21117"})
	want(t, "create agent", code, 201, m)
	if m["relay"].(map[string]any)["mgmt_host"] != "10.0.0.1:21117" {
		t.Fatalf("agent mgmt_host: %v", m)
	}
	enr := m["enrollment"].(map[string]any)
	aid := int64(b2bNum(m["relay"].(map[string]any)["id"]))
	tok := enr["token"].(string)
	if tok == "" || !strings.Contains(enr["compose"].(string), tok) {
		t.Fatalf("enrollment: %v", enr)
	}
	code, m, _ = admin.do(http.MethodPost, "/relays/"+itoa(aid)+"/approve", nil)
	wantErr(t, "approve before enroll", code, 400, m, "validation_failed")
	_, pk, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := e.rstore.Enroll(e.ctx, relays.EnrollInput{TokenHash: relays.TokenHash(tok), Pubkey: pk.Public().(ed25519.PublicKey)}); err != nil {
		t.Fatal(err)
	}
	code, m, _ = admin.do(http.MethodPost, "/relays/"+itoa(aid)+"/approve", nil)
	want(t, "approve", code, 200, m)
	if a := m["agent"].(map[string]any); a["approved"] != true || a["enrolled"] != true || a["connected"] != false {
		t.Fatalf("approved agent: %v", a)
	}
	// Per-relay overrides.
	code, m, _ = admin.do(http.MethodPut, "/relays/"+itoa(aid)+"/settings", map[string]any{"total_bandwidth_mbps": 50})
	want(t, "overrides", code, 200, m)
	if b2bNum(m["effective"].(map[string]any)["total_bandwidth_mbps"]) != 50 {
		t.Fatalf("effective: %v", m)
	}
	code, m, _ = admin.do(http.MethodPut, "/relays/"+itoa(aid)+"/settings", map[string]any{"total_bandwidth_mbps": -1})
	wantErr(t, "bad override", code, 400, m, "validation_failed")
	code, m, _ = admin.do(http.MethodPut, "/relays/"+itoa(sid)+"/settings", map[string]any{"total_bandwidth_mbps": 5})
	wantErr(t, "static override", code, 400, m, "validation_failed")
	code, m, _ = admin.do(http.MethodGet, "/relays/"+itoa(aid)+"/usage", nil)
	wantErr(t, "usage of a disconnected agent", code, 503, m, "server_unavailable")
	code, m, _ = admin.do(http.MethodPost, "/relays/"+itoa(aid)+"/enroll-token", nil)
	want(t, "new token", code, 200, m)
	if m["token"] == tok {
		t.Fatal("token reused")
	}
	code, m, _ = admin.do(http.MethodPost, "/relays/"+itoa(aid)+"/revoke", nil)
	want(t, "revoke", code, 200, m)

	code, m, _ = admin.do(http.MethodGet, "/relays", nil)
	want(t, "list", code, 200, m)
	if len(m["items"].([]any)) != 2 {
		t.Fatalf("list: %v", m)
	}
	code, _, _ = admin.do(http.MethodDelete, "/relays/"+itoa(aid), nil)
	if code != http.StatusNoContent {
		t.Fatalf("delete: %d", code)
	}
	code, m, _ = admin.do(http.MethodGet, "/relays/"+itoa(aid), nil)
	wantErr(t, "deleted", code, 404, m, "not_found")

	// Audit trail of admin actions.
	var actions string
	_ = e.rstore.DB.QueryRow(`SELECT group_concat(action, ',') FROM (SELECT action FROM admin_audit ORDER BY id)`).Scan(&actions)
	for _, a := range []string{"relay.options.update", "relay.create", "relay.update", "relay.approve", "relay.settings.update",
		"relay.enroll_token", "relay.revoke", "relay.delete"} {
		if !strings.Contains(actions, a) {
			t.Errorf("audit lacks %s: %s", a, actions)
		}
	}
}

func TestRelayTrafficEndpoint(t *testing.T) {
	e := newRelayEnv(t)
	admin, _ := e.as("root", store.RoleAdmin)

	// Create a relay
	v, _, err := e.svc.Create(e.ctx, relays.CreateInput{Name: "relay-traffic-test", Kind: relays.KindStatic, PublicHost: "1.2.3.4:21117"})
	if err != nil {
		t.Fatal(err)
	}

	// Insert some traffic samples for the current and previous hour
	now := e.st.Now()
	if err := e.rstore.RecordTraffic(e.ctx, v.ID, now, 1024, 2, 4); err != nil {
		t.Fatal(err)
	}
	if err := e.rstore.RecordTraffic(e.ctx, v.ID, now.Add(-time.Hour), 2048, 1, 3); err != nil {
		t.Fatal(err)
	}

	// GET /relays/{id}/traffic?hours=6
	code, m, _ := admin.do(http.MethodGet, "/relays/"+itoa(v.ID)+"/traffic?hours=6", nil)
	want(t, "relay traffic", code, 200, m)

	if int(m["hours"].(float64)) != 6 {
		t.Errorf("hours = %v, want 6", m["hours"])
	}
	if int64(m["total_bytes"].(float64)) != 3072 {
		t.Errorf("total_bytes = %v, want 3072", m["total_bytes"])
	}
	if int(m["total_sessions"].(float64)) != 3 {
		t.Errorf("total_sessions = %v, want 3", m["total_sessions"])
	}
	if int(m["max_active"].(float64)) != 4 {
		t.Errorf("max_active = %v, want 4", m["max_active"])
	}
	items := m["items"].([]any)
	if len(items) != 6 {
		t.Fatalf("items count = %d, want 6", len(items))
	}

	// 404 for nonexistent relay
	code, _, _ = admin.do(http.MethodGet, "/relays/999999/traffic", nil)
	if code != http.StatusNotFound {
		t.Errorf("want 404, got %d", code)
	}
}

func TestRelayPrefixRulesAPI(t *testing.T) {
	e := newRelayEnv(t)
	admin, _ := e.as("root", store.RoleAdmin)
	user, _ := e.as("bob", store.RoleUser)

	// User forbidden
	code, m, _ := user.do(http.MethodGet, "/relays/prefix-rules", nil)
	wantErr(t, "user get prefix rules", code, 403, m, "forbidden")

	// Initially empty
	code, _, _ = admin.do(http.MethodGet, "/relays/prefix-rules", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}

	// Create valid prefix rule
	code, m, _ = admin.do(http.MethodPost, "/relays/prefix-rules", map[string]any{
		"cidr":       "192.168.1.0/24",
		"relay_host": "relay-local.example.com",
		"comment":    "Local office",
	})
	want(t, "create prefix rule", code, 201, m)
	id := int64(m["id"].(float64))
	if id <= 0 || m["cidr"] != "192.168.1.0/24" || m["relay_host"] != "relay-local.example.com" {
		t.Fatalf("unexpected rule: %v", m)
	}

	// List rules
	code, _, _ = admin.do(http.MethodGet, "/relays/prefix-rules", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on list, got %d", code)
	}

	// Invalid rule
	code, m, _ = admin.do(http.MethodPost, "/relays/prefix-rules", map[string]any{
		"cidr":       "invalid-cidr",
		"relay_host": "relay.example.com",
	})
	wantErr(t, "invalid cidr", code, 400, m, "validation_failed")

	// Delete rule
	code, _, _ = admin.do(http.MethodDelete, "/relays/prefix-rules/"+itoa(id), nil)
	if code != http.StatusNoContent {
		t.Fatalf("expected 204 on delete, got %d", code)
	}

	// Delete nonexistent rule
	code, m, _ = admin.do(http.MethodDelete, "/relays/prefix-rules/99999", nil)
	wantErr(t, "delete nonexistent", code, 404, m, "not_found")

	// List countries
	code, m, _ = user.do(http.MethodGet, "/relays/prefix-rules/countries", nil)
	wantErr(t, "user get countries", code, 403, m, "forbidden")

	code, m, _ = admin.do(http.MethodGet, "/relays/prefix-rules/countries", nil)
	if code != http.StatusOK {
		t.Fatalf("expected 200 on list countries, got %d", code)
	}
	raw, _ := m["_raw"].(string)
	if !strings.Contains(raw, `"code":"RU"`) || !strings.Contains(raw, `"code":"DE"`) {
		t.Fatalf("expected RU and DE in countries, got %s", raw)
	}

	// Import country rules
	code, m, _ = admin.do(http.MethodPost, "/relays/prefix-rules/import-country", map[string]any{
		"country_code": "RU",
		"relay_host":   "relay-ru.example.com",
	})
	want(t, "import country rules", code, 200, m)
	if m["country_code"] != "RU" || m["added_count"].(float64) <= 0 {
		t.Fatalf("expected added_count > 0, got %v", m)
	}

	// Importing again should not duplicate
	code, m, _ = admin.do(http.MethodPost, "/relays/prefix-rules/import-country", map[string]any{
		"country_code": "RU",
		"relay_host":   "relay-ru.example.com",
	})
	want(t, "import country rules again", code, 200, m)
	if m["added_count"].(float64) != 0 {
		t.Fatalf("expected added_count 0 on repeat import, got %v", m)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
