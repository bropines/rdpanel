package relays

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/relays/agentproto"
)

type hubFixture struct {
	*harness
	srv *httptest.Server
	id  int64
	key ed25519.PrivateKey
}

func newHub(t *testing.T) *hubFixture {
	t.Helper()
	h := newHarness(t, nil)
	r := chi.NewRouter()
	r.Route("/agent/v1", h.svc.Register)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	v, enr, err := h.svc.Create(ctx, CreateInput{Kind: KindAgent, Name: "hel-2", PublicHost: "hel-2.example.com", Weight: 1, InRotation: true})
	if err != nil {
		t.Fatal(err)
	}
	f := &hubFixture{harness: h, srv: srv, id: v.ID, key: newAgentKey(t)}
	// Enroll over HTTP.
	body, _ := json.Marshal(agentproto.EnrollRequest{Token: enr.Token, AgentPubkey: f.key.Public().(ed25519.PublicKey), Hostname: "hel-2", Version: "test"})
	resp, err := http.Post(srv.URL+agentproto.EnrollPath, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var er agentproto.EnrollResponse
	_ = json.NewDecoder(resp.Body).Decode(&er)
	resp.Body.Close()
	if resp.StatusCode != 200 || er.AgentID != v.ID || er.Approved {
		t.Fatalf("enroll: %d %+v", resp.StatusCode, er)
	}
	// Second use of the token: 401.
	resp, _ = http.Post(srv.URL+agentproto.EnrollPath, "application/json", bytes.NewReader(body))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token reuse: %d", resp.StatusCode)
	}
	return f
}

func (f *hubFixture) dial(t *testing.T) (*websocket.Conn, agentproto.Challenge) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(f.srv.URL, "http")+agentproto.ConnectPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	var env agentproto.Envelope
	if err := wsjson.Read(ctx, ws, &env); err != nil {
		t.Fatal(err)
	}
	var ch agentproto.Challenge
	if env.Type != agentproto.TypeChallenge || env.Decode(&ch) != nil || len(ch.Nonce) != agentproto.NonceSize {
		t.Fatalf("challenge %+v", env)
	}
	return ws, ch
}

// answer sends auth and returns the next message type or the close code.
func answer(t *testing.T, ws *websocket.Conn, a agentproto.Auth) (string, websocket.StatusCode) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	env, _ := agentproto.NewEnvelope(agentproto.TypeAuth, "", a)
	if err := wsjson.Write(ctx, ws, env); err != nil {
		t.Fatal(err)
	}
	var in agentproto.Envelope
	if err := wsjson.Read(ctx, ws, &in); err != nil {
		return "", websocket.CloseStatus(err)
	}
	return in.Type, -1
}

func TestHubSignatureReplayAndSkew(t *testing.T) {
	f := newHub(t)
	ws, ch := f.dial(t)
	auth := agentproto.Sign(f.key, ch.Nonce, f.id, f.clk.now())
	if typ, _ := answer(t, ws, auth); typ != agentproto.TypeWelcome {
		t.Fatalf("valid auth: %q", typ)
	}
	ws.Close(websocket.StatusNormalClosure, "")

	// Replaying the captured auth on a new connection (new nonce) fails.
	ws2, _ := f.dial(t)
	if _, code := answer(t, ws2, auth); code != agentproto.CloseAuthFailed {
		t.Fatalf("replay: close %d", code)
	}
	// Clock skew beyond 60 s.
	ws3, ch3 := f.dial(t)
	if _, code := answer(t, ws3, agentproto.Sign(f.key, ch3.Nonce, f.id, f.clk.now().Add(-5*time.Minute))); code != agentproto.CloseAuthFailed {
		t.Fatalf("skew: close %d", code)
	}
	// Signature over another agent id.
	ws4, ch4 := f.dial(t)
	bad := agentproto.Sign(f.key, ch4.Nonce, f.id+1, f.clk.now())
	bad.AgentID = f.id
	if _, code := answer(t, ws4, bad); code != agentproto.CloseAuthFailed {
		t.Fatalf("wrong id in signature: close %d", code)
	}
	// Another key.
	ws5, ch5 := f.dial(t)
	if _, code := answer(t, ws5, agentproto.Sign(newAgentKey(t), ch5.Nonce, f.id, f.clk.now())); code != agentproto.CloseAuthFailed {
		t.Fatalf("foreign key: close %d", code)
	}
	if f.svc.m == nil {
		t.Fatal("no metrics")
	}
}

func TestHubSupersedeAndRevoke(t *testing.T) {
	f := newHub(t)
	ctx := context.Background()
	ws1, ch1 := f.dial(t)
	if typ, _ := answer(t, ws1, agentproto.Sign(f.key, ch1.Nonce, f.id, f.clk.now())); typ != agentproto.TypeWelcome {
		t.Fatal("first session")
	}
	ws2, ch2 := f.dial(t)
	if typ, _ := answer(t, ws2, agentproto.Sign(f.key, ch2.Nonce, f.id, f.clk.now())); typ != agentproto.TypeWelcome {
		t.Fatal("second session")
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var env agentproto.Envelope
	if err := wsjson.Read(rctx, ws1, &env); websocket.CloseStatus(err) != agentproto.CloseSuperseded {
		t.Fatalf("first session: %v", err)
	}
	if !f.svc.Connected(f.id) {
		t.Fatal("second session not registered")
	}
	// hello -> config (not approved: run=false).
	hello, _ := agentproto.NewEnvelope(agentproto.TypeHello, "", agentproto.Hello{AgentVersion: "test"})
	if err := wsjson.Write(rctx, ws2, hello); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Read(rctx, ws2, &env); err != nil || env.Type != agentproto.TypeConfig {
		t.Fatalf("config: %+v %v", env, err)
	}
	var cfg agentproto.Config
	_ = env.Decode(&cfg)
	if cfg.Run || cfg.Version != 1 || cfg.HBBSPublicKey != testKey {
		t.Fatalf("unapproved config %+v", cfg)
	}
	ack, _ := agentproto.NewEnvelope(agentproto.TypeAck, "", agentproto.Ack{Version: 1, Errors: map[string]string{"total_bandwidth": "boom"}})
	_ = wsjson.Write(rctx, ws2, ack)
	deadline := time.Now().Add(3 * time.Second)
	for {
		r, _ := f.st.Get(ctx, f.id)
		if r.Agent.AckedVersion == 1 && r.Agent.AckErrors["total_bandwidth"] == "boom" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ack not stored: %+v", r.Agent)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Approve -> next pass pushes run=true as version 2.
	if _, err := f.svc.Approve(ctx, f.id, nil); err != nil {
		t.Fatal(err)
	}
	f.svc.Pass(ctx, false)
	if err := wsjson.Read(rctx, ws2, &env); err != nil || env.Type != agentproto.TypeConfig {
		t.Fatalf("approved config: %+v %v", env, err)
	}
	_ = env.Decode(&cfg)
	if !cfg.Run || cfg.Version != 2 {
		t.Fatalf("approved config %+v", cfg)
	}
	// Revoke closes the session with 4003 and new sessions get 4003 too.
	if _, err := f.svc.Revoke(ctx, f.id); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Read(rctx, ws2, &env); websocket.CloseStatus(err) != agentproto.CloseRevoked {
		t.Fatalf("revoked session: %v", err)
	}
	ws3, ch3 := f.dial(t)
	if _, code := answer(t, ws3, agentproto.Sign(f.key, ch3.Nonce, f.id, f.clk.now())); code != agentproto.CloseRevoked {
		t.Fatalf("revoked reconnect: close %d", code)
	}
	// Audit trail.
	var actions string
	rows, err := f.st.DB.Query(`SELECT action FROM admin_audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		_ = rows.Scan(&a)
		actions += a + ","
	}
	rows.Close()
	for _, want := range []string{"relay.agent.enroll", "relay.agent.connect", "relay.agent.superseded", "relay.agent.config_error"} {
		if !strings.Contains(actions, want) {
			t.Errorf("audit lacks %s: %s", want, actions)
		}
	}
	var actor string
	_ = f.st.DB.QueryRow(`SELECT actor_name FROM admin_audit WHERE action = 'relay.agent.enroll'`).Scan(&actor)
	if actor != "agent:"+strconv.FormatInt(f.id, 10) {
		t.Errorf("actor %q", actor)
	}
}

// TestHubLiveStatsExpire: an agent's last status stops counting as load (auto
// weights) and traffic (sampling) once it disconnects or goes silent.
func TestHubLiveStatsExpire(t *testing.T) {
	f := newHub(t)
	ctx := context.Background()
	trafficBytes := func() int64 {
		samples, err := f.svc.Traffic(ctx, f.id, f.clk.now().Add(-24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		for _, s := range samples {
			n += s.Bytes
		}
		return n
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !cond(); {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	report := func(ws *websocket.Conn) {
		t.Helper()
		st := agentproto.Status{Usage: agentproto.UsageSummary{RelaysActive: 7}, Host: agentproto.HostStatus{RxBps: 8e6, TxBps: 8e6}}
		env, _ := agentproto.NewEnvelope(agentproto.TypeStatus, "", st)
		if err := wsjson.Write(ctx, ws, env); err != nil {
			t.Fatal(err)
		}
		waitFor("status", func() bool { return f.svc.getActiveSessionsMap()[f.id] == 7 })
	}
	connect := func() *websocket.Conn {
		t.Helper()
		ws, ch := f.dial(t)
		if typ, _ := answer(t, ws, agentproto.Sign(f.key, ch.Nonce, f.id, f.clk.now())); typ != agentproto.TypeWelcome {
			t.Fatalf("auth: %q", typ)
		}
		return ws
	}

	ws := connect()
	report(ws)
	f.svc.Pass(ctx, false) // first sample only initialises the traffic state
	f.clk.add(10 * time.Second)
	f.svc.Pass(ctx, false)
	live := trafficBytes()
	if live == 0 {
		t.Fatal("no traffic recorded from a live agent")
	}

	// Disconnect: the last status is dropped at once.
	ws.Close(websocket.StatusNormalClosure, "")
	waitFor("disconnect", func() bool { return !f.svc.Connected(f.id) })
	if n, ok := f.svc.getActiveSessionsMap()[f.id]; ok && n != 0 {
		t.Fatalf("disconnected agent still counts %d sessions", n)
	}
	for range 3 {
		f.clk.add(10 * time.Second)
		f.svc.Pass(ctx, false)
	}
	if got := trafficBytes(); got != live {
		t.Fatalf("traffic grew from %d to %d after the agent disconnected", live, got)
	}

	// Connected but silent past liveStatsMaxAge: stale, ignored as well.
	ws = connect()
	report(ws)
	f.svc.Pass(ctx, false)
	f.clk.add(liveStatsMaxAge + time.Second)
	if n := f.svc.getActiveSessionsMap()[f.id]; n != 0 {
		t.Fatalf("stale status still counts %d sessions", n)
	}
	before := trafficBytes()
	f.svc.Pass(ctx, false)
	if got := trafficBytes(); got != before {
		t.Fatalf("traffic grew from %d to %d on a stale status", before, got)
	}
}
