package relays

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/relays/agentproto"
	"github.com/bropines/rdpanel/internal/store"
)

// Hub timeouts.
const (
	handshakeTimeout = 10 * time.Second
	pingInterval     = 30 * time.Second
	commandTimeout   = 10 * time.Second
	writeTimeout     = 10 * time.Second
)

// Register mounts the agent endpoints on r (scoped to /agent/v1). They are
// unauthenticated at the HTTP layer (no cookies, no CSRF): enrollment needs a
// one-time token, sessions an ed25519 signature. Both are rate limited per
// client IP and back off after failures.
func (s *Service) Register(r chi.Router) {
	r.Use(s.gate)
	r.Post("/enroll", s.handleEnroll)
	r.Get("/connect", s.handleConnect)
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeAgentError(w, http.StatusNotFound, "not_found", "not found")
	})
}

func writeAgentError(w http.ResponseWriter, status int, code, msg string) {
	httpx.WriteJSON(w, status, agentproto.Error{Code: code, Message: msg})
}

func (s *Service) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := httpx.ClientIPString(r)
		if !s.rate.allow(ip) {
			w.Header().Set("Retry-After", "5")
			writeAgentError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
			return
		}
		if l := s.cfg.Limiter; l != nil {
			if ok, retry := l.Allow("agent-ip:" + ip); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
				writeAgentError(w, http.StatusTooManyRequests, "rate_limited", "too many failed attempts")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Service) failure(ip string) {
	if l := s.cfg.Limiter; l != nil {
		l.Failure("agent-ip:" + ip)
	}
}

func (s *Service) handleEnroll(w http.ResponseWriter, r *http.Request) {
	ip := httpx.ClientIPString(r)
	var req agentproto.EnrollRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := dec.Decode(&req); err != nil {
		writeAgentError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.Token == "" || len(req.Token) > 128 || len(req.AgentPubkey) != ed25519.PublicKeySize {
		writeAgentError(w, http.StatusBadRequest, "bad_request", "token and a 32-byte ed25519 agent_pubkey are required")
		return
	}
	rel, err := s.st.Enroll(r.Context(), EnrollInput{TokenHash: TokenHash(req.Token), Pubkey: req.AgentPubkey, IP: ip,
		Hostname: req.Hostname, Version: req.Version, HBBR: req.HBBRVersion, Arch: req.Arch})
	switch {
	case errors.Is(err, ErrTokenInvalid):
		s.failure(ip)
		s.m.enrollFail.Inc()
		s.log.Warn("relays: enrollment refused", slog.String("ip", ip))
		writeAgentError(w, http.StatusUnauthorized, "token_invalid", err.Error())
		return
	case errors.Is(err, store.ErrConflict):
		writeAgentError(w, http.StatusConflict, "key_in_use", "this agent key is already enrolled for another relay")
		return
	case err != nil:
		s.log.Error("relays: enroll", slog.Any("err", err))
		writeAgentError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	s.audit(r.Context(), rel.ID, ip, "relay.agent.enroll", map[string]any{"relay": rel.Name,
		"hostname": rel.Agent.Hostname, "version": rel.Agent.AgentVersion, "approved": rel.Agent.Approved()})
	s.log.Info("relays: agent enrolled", slog.String("relay", rel.Name), slog.String("ip", ip),
		slog.Bool("approved", rel.Agent.Approved()))
	s.publish(rel, rel.Health)
	s.Notify()
	httpx.WriteJSON(w, http.StatusOK, agentproto.EnrollResponse{AgentID: rel.ID, RelayName: rel.Name, Approved: rel.Agent.Approved()})
}

// agentConn is one authenticated agent session.
type agentConn struct {
	s           *Service
	id          int64
	name        string
	ws          *websocket.Conn
	cancel      context.CancelFunc
	connectedAt time.Time
	remoteIP    string

	seq     atomic.Uint64
	hello   atomic.Bool
	sentVer atomic.Int64

	mu      sync.Mutex
	pending map[string]chan agentproto.Result
}

func (c *agentConn) helloDone() bool    { return c.hello.Load() }
func (c *agentConn) sentVersion() int64 { return c.sentVer.Load() }

func (c *agentConn) send(ctx context.Context, typ, id string, data any) error {
	env, err := agentproto.NewEnvelope(typ, id, data)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	return wsjson.Write(ctx, c.ws, env)
}

func (c *agentConn) sendConfig(ctx context.Context, cfg agentproto.Config) error {
	if err := c.send(ctx, agentproto.TypeConfig, "", cfg); err != nil {
		return err
	}
	c.sentVer.Store(cfg.Version)
	return nil
}

func (c *agentConn) command(ctx context.Context, name string) (agentproto.Result, error) {
	id := strconv.FormatUint(c.seq.Add(1), 10)
	ch := make(chan agentproto.Result, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.send(ctx, agentproto.TypeCommand, id, agentproto.Command{Name: name}); err != nil {
		return agentproto.Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	select {
	case res := <-ch:
		return res, nil
	case <-ctx.Done():
		return agentproto.Result{}, fmt.Errorf("agent did not answer %q: %w", name, ctx.Err())
	}
}

func (s *Service) disconnect(id int64, code websocket.StatusCode, reason string) {
	s.mu.Lock()
	c := s.conns[id]
	delete(s.conns, id)
	s.mu.Unlock()
	if c != nil {
		c.close(code, reason)
	}
}

// close sends a close frame without waiting for the peer's answer (Close
// blocks up to 5 s for it) and ends the session.
func (c *agentConn) close(code websocket.StatusCode, reason string) {
	go func() {
		_ = c.ws.Close(code, reason)
		c.cancel()
	}()
}

func (s *Service) closeAll() {
	s.mu.Lock()
	conns := s.conns
	s.conns = map[int64]*agentConn{}
	s.mu.Unlock()
	for _, c := range conns {
		c.close(websocket.StatusGoingAway, "panel shutting down")
	}
}

// Connected reports whether relay id has an open agent session.
func (s *Service) Connected(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.conns[id]
	return ok
}

func (s *Service) handleConnect(w http.ResponseWriter, r *http.Request) {
	ip := httpx.ClientIPString(r)
	ws, err := websocket.Accept(w, r, nil) // agents send no Origin; browsers cannot pass the auth step
	if err != nil {
		return
	}
	ws.SetReadLimit(agentproto.MaxMessage)
	// The request context ends when the client goes away; the server's base
	// context (cancelled on shutdown) is its parent.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	rel, err := s.authenticate(ctx, ws, ip)
	if err != nil {
		s.m.authFail.Inc()
		s.failure(ip)
		code := websocket.StatusCode(agentproto.CloseAuthFailed)
		if errors.Is(err, errRevoked) {
			code = agentproto.CloseRevoked
		}
		s.log.Warn("relays: agent session refused", slog.String("ip", ip), slog.Any("err", err))
		_ = ws.Close(code, truncate(err.Error(), 120))
		return
	}
	if l := s.cfg.Limiter; l != nil {
		l.Success("agent-ip:" + ip)
	}
	c := &agentConn{s: s, id: rel.ID, name: rel.Name, ws: ws, cancel: cancel, connectedAt: s.now().UTC(),
		remoteIP: ip, pending: map[string]chan agentproto.Result{}}
	s.mu.Lock()
	old := s.conns[rel.ID]
	s.conns[rel.ID] = c
	s.mu.Unlock()
	if old != nil {
		old.close(agentproto.CloseSuperseded, "superseded by a newer connection")
		s.audit(ctx, rel.ID, ip, "relay.agent.superseded", map[string]any{"relay": rel.Name, "old_ip": old.remoteIP})
	}
	s.audit(ctx, rel.ID, ip, "relay.agent.connect", map[string]any{"relay": rel.Name})
	s.log.Info("relays: agent connected", slog.String("relay", rel.Name), slog.String("ip", ip))
	defer func() {
		s.mu.Lock()
		if s.conns[rel.ID] == c {
			delete(s.conns, rel.ID)
		}
		s.mu.Unlock()
		s.log.Info("relays: agent disconnected", slog.String("relay", rel.Name))
		s.Notify()
	}()
	if err := c.send(ctx, agentproto.TypeWelcome, "", agentproto.Welcome{RelayName: rel.Name, Approved: rel.Agent.Approved()}); err != nil {
		return
	}
	go func() {
		t := time.NewTicker(pingInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, writeTimeout)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	s.publish(rel, rel.Health)
	for {
		var env agentproto.Envelope
		if err := wsjson.Read(ctx, ws, &env); err != nil {
			return
		}
		if err := s.handleMessage(ctx, c, env); err != nil {
			s.log.Warn("relays: agent message", slog.String("relay", rel.Name), slog.String("type", env.Type), slog.Any("err", err))
			_ = ws.Close(websocket.StatusPolicyViolation, truncate(err.Error(), 120))
			return
		}
	}
}

var errRevoked = errors.New("agent revoked or reset; enroll again with a new token")

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// authenticate runs the challenge/response handshake.
func (s *Service) authenticate(ctx context.Context, ws *websocket.Conn, ip string) (Relay, error) {
	ctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()
	nonce := make([]byte, agentproto.NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return Relay{}, err
	}
	env, err := agentproto.NewEnvelope(agentproto.TypeChallenge, "", agentproto.Challenge{Nonce: nonce, ServerTime: s.now().Unix()})
	if err != nil {
		return Relay{}, err
	}
	if err := wsjson.Write(ctx, ws, env); err != nil {
		return Relay{}, err
	}
	var in agentproto.Envelope
	if err := wsjson.Read(ctx, ws, &in); err != nil {
		return Relay{}, fmt.Errorf("read auth: %w", err)
	}
	var a agentproto.Auth
	if in.Type != agentproto.TypeAuth || in.Decode(&a) != nil {
		return Relay{}, errors.New("expected an auth message")
	}
	rel, err := s.st.Get(ctx, a.AgentID)
	if err != nil || rel.Kind != KindAgent || rel.Agent == nil {
		return Relay{}, errors.New("unknown agent")
	}
	if !rel.Agent.Enrolled() || rel.Agent.RevokedAt != nil {
		return Relay{}, errRevoked
	}
	if err := agentproto.Verify(ed25519.PublicKey(rel.Agent.Pubkey), nonce, a, s.now(), s.cfg.ClockSkew); err != nil {
		return Relay{}, err
	}
	return rel, nil
}

func (s *Service) handleMessage(ctx context.Context, c *agentConn, env agentproto.Envelope) error {
	switch env.Type {
	case agentproto.TypeHello:
		var h agentproto.Hello
		if err := env.Decode(&h); err != nil {
			return err
		}
		if err := s.st.SetHello(ctx, c.id, h); err != nil {
			return err
		}
		c.sentVer.Store(-1)
		c.hello.Store(true)
		rel, err := s.st.Get(ctx, c.id)
		if err != nil {
			return err
		}
		if h.ConfigVersion > 0 && h.ConfigVersion == rel.Agent.DesiredVersion {
			c.sentVer.Store(h.ConfigVersion) // the agent already enforces it
		}
		return s.pushConfig(ctx, rel)
	case agentproto.TypeAck:
		var a agentproto.Ack
		if err := env.Decode(&a); err != nil {
			return err
		}
		if err := s.st.SetAck(ctx, c.id, a.Version, a.Errors); err != nil {
			return err
		}
		if len(a.Errors) > 0 {
			s.audit(ctx, c.id, c.remoteIP, "relay.agent.config_error", map[string]any{"relay": c.name,
				"version": a.Version, "errors": a.Errors})
		}
		// A freshly applied config (e.g. hbbr just started after approval):
		// probe on the next pass instead of waiting for the probe interval.
		s.mu.Lock()
		delete(s.probedAt, c.id)
		s.mu.Unlock()
		s.Notify()
		return nil
	case agentproto.TypeStatus:
		var st agentproto.Status
		if err := env.Decode(&st); err != nil {
			return err
		}
		s.mu.Lock()
		s.liveStats[c.id] = st
		save := s.now().Sub(s.statusAt[c.id]) >= statusSaveEvery
		if save {
			s.statusAt[c.id] = s.now()
		}
		s.mu.Unlock()
		if save {
			return s.st.SetStatus(ctx, c.id, st)
		}
		return nil
	case agentproto.TypeResult:
		var res agentproto.Result
		if err := env.Decode(&res); err != nil {
			return err
		}
		c.mu.Lock()
		ch := c.pending[env.ID]
		c.mu.Unlock()
		if ch != nil {
			select {
			case ch <- res:
			default:
			}
		}
		return nil
	}
	return fmt.Errorf("unexpected message type %q", env.Type)
}

// ipRate is a small per-IP token bucket for /agent/v1.
type ipRate struct {
	rate, burst float64
	now         func() time.Time
	mu          sync.Mutex
	b           map[string]*tokenBucket
	pruned      time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func newIPRate(rate, burst float64, now func() time.Time) *ipRate {
	return &ipRate{rate: rate, burst: burst, now: now, b: map[string]*tokenBucket{}, pruned: now()}
}

func (l *ipRate) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.pruned) > time.Minute {
		for k, b := range l.b {
			if now.Sub(b.last) > 10*time.Minute {
				delete(l.b, k)
			}
		}
		l.pruned = now
	}
	b := l.b[key]
	if b == nil {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.b[key] = b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = min(l.burst, b.tokens+el*l.rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
