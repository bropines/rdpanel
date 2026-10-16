// Package agent implements `rdpanel agent`: it runs next to (as the parent
// of) hbbr on a relay host, enrolls with the central panel using a one-time
// token, keeps an outbound ed25519-authenticated WebSocket session to
// <panel>/agent/v1/connect, persists the desired hbbr config it receives,
// applies it through hbbr's loopback admin channel (the hbbr half of the
// panel's reconciler, unchanged) and reports status. See
// docs/relay-agents.md.
package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/bropines/rdpanel/internal/buildinfo"
	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/relays/agentproto"
)

// Config configures the agent (environment: see FromEnv).
type Config struct {
	// PanelURL is the panel's public (client API) base URL.
	PanelURL string
	// EnrollToken is the one-time token; ignored once enrolled.
	EnrollToken string
	// DataDir holds agent.key, agent-state.json and hbbr's list files.
	DataDir string
	// HBBRBin is the hbbr executable.
	HBBRBin string
	// RelayPort is hbbr's TCP port (its loopback admin channel too).
	RelayPort int
	// PanelCA is an optional PEM file with extra CAs to trust for the panel.
	PanelCA string
	// Hostname overrides the reported host name.
	Hostname string
	// StatusInterval is how often status is reported (default 10 s).
	StatusInterval time.Duration
	// ApplyInterval is the drift-check period (default 15 s).
	ApplyInterval time.Duration
	Log           *slog.Logger

	// Test hooks.
	Runner     Runner
	HTTPClient *http.Client
	// ControlAddr overrides the hbbr admin address (default 127.0.0.1:RelayPort).
	ControlAddr string
	// ProcDir is /proc (host sampling).
	ProcDir string
	// MaxBackoff caps reconnect delays (default 60 s).
	MaxBackoff time.Duration
	// HealthInterval is how often agent-health.json is written (default 10 s).
	HealthInterval time.Duration
}

// FromEnv reads the agent configuration from RDPANEL_AGENT_* variables.
func FromEnv(lookup func(string) (string, bool)) (Config, error) {
	get := func(k, def string) string {
		if v, ok := lookup(k); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return def
	}
	c := Config{
		PanelURL:    strings.TrimRight(get("RDPANEL_AGENT_PANEL_URL", ""), "/"),
		EnrollToken: get("RDPANEL_AGENT_ENROLL_TOKEN", ""),
		DataDir:     get("RDPANEL_AGENT_DATA_DIR", "/data"),
		HBBRBin:     get("RDPANEL_AGENT_HBBR_BIN", "hbbr"),
		PanelCA:     get("RDPANEL_AGENT_PANEL_CA", ""),
		Hostname:    get("RDPANEL_AGENT_HOSTNAME", ""),
	}
	var err error
	if c.RelayPort, err = strconv.Atoi(get("RDPANEL_AGENT_RELAY_PORT", "21117")); err != nil || c.RelayPort < 1 || c.RelayPort > 65535 {
		return c, errors.New("RDPANEL_AGENT_RELAY_PORT must be a port number")
	}
	if c.StatusInterval, err = time.ParseDuration(get("RDPANEL_AGENT_STATUS_INTERVAL", "10s")); err != nil || c.StatusInterval < time.Second {
		return c, errors.New("RDPANEL_AGENT_STATUS_INTERVAL must be a duration >= 1s")
	}
	if c.PanelURL != "" {
		u, err := url.Parse(c.PanelURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return c, errors.New("RDPANEL_AGENT_PANEL_URL must be an http(s) URL, e.g. https://desk-api.example.com")
		}
	}
	return c, nil
}

// Agent is one running relay agent.
type Agent struct {
	cfg  Config
	log  *slog.Logger
	key  ed25519.PrivateKey
	ctl  *control.Client
	sup  *Supervisor
	http *http.Client
	host hostSampler

	mu        sync.Mutex
	st        State
	session   *session
	applyErrs map[string]string
	applied   int64 // config version applied (with applyErrs)
	adminRTT  time.Duration
	usage     agentproto.UsageSummary
	cfgCh     chan struct{}
}

// session is the live WebSocket (nil when disconnected).
type session struct{ ws *websocket.Conn }

func (s *session) send(ctx context.Context, typ, id string, data any) error {
	env, err := agentproto.NewEnvelope(typ, id, data)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(ctx, s.ws, env)
}

// New prepares an agent: data dir, identity key and persisted state.
func New(cfg Config) (*Agent, error) {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.StatusInterval <= 0 {
		cfg.StatusInterval = 10 * time.Second
	}
	if cfg.ApplyInterval <= 0 {
		cfg.ApplyInterval = 15 * time.Second
	}
	if cfg.RelayPort == 0 {
		cfg.RelayPort = 21117
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 60 * time.Second
	}
	if cfg.HealthInterval <= 0 {
		cfg.HealthInterval = 10 * time.Second
	}
	if cfg.ProcDir == "" {
		cfg.ProcDir = "/proc"
	}
	if cfg.ControlAddr == "" {
		cfg.ControlAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.RelayPort))
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("agent: data dir: %w", err)
	}
	key, created, err := LoadOrCreateKey(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if created {
		cfg.Log.Info("agent: generated identity key", slog.String("file", KeyFile))
	}
	st, err := LoadState(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	ctl, err := control.New(control.Config{HBBRAddr: cfg.ControlAddr, Log: cfg.Log.With("component", "hbbr-admin")})
	if err != nil {
		return nil, err
	}
	hc := cfg.HTTPClient
	if hc == nil {
		if hc, err = httpClient(cfg.PanelCA); err != nil {
			return nil, err
		}
	}
	runner := cfg.Runner
	if runner == nil {
		runner = ExecRunner{Bin: cfg.HBBRBin, Dir: cfg.DataDir, Log: cfg.Log}
	}
	a := &Agent{cfg: cfg, log: cfg.Log, key: key, ctl: ctl, http: hc, st: st,
		host: hostSampler{proc: cfg.ProcDir}, cfgCh: make(chan struct{}, 1)}
	a.sup = NewSupervisor(runner, cfg.RelayPort, cfg.Log)
	if st.PanelURL == "" {
		a.st.PanelURL = cfg.PanelURL
	} else if cfg.PanelURL != "" && cfg.PanelURL != st.PanelURL {
		cfg.Log.Warn("agent: RDPANEL_AGENT_PANEL_URL differs from the enrolled panel; using the enrolled one (delete agent-state.json to re-enroll)",
			slog.String("enrolled", st.PanelURL), slog.String("env", cfg.PanelURL))
	}
	return a, nil
}

func httpClient(caFile string) (*http.Client, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("agent: panel CA: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("agent: panel CA %s: no PEM certificates", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, Timeout: 30 * time.Second}, nil
}

// State returns a copy of the persisted state.
func (a *Agent) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.st
}

// Supervisor exposes the hbbr supervisor (tests).
func (a *Agent) Supervisor() *Supervisor { return a.sup }

// Run enrolls if needed and then runs until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if a.State().PanelURL == "" {
		return errors.New("agent: RDPANEL_AGENT_PANEL_URL is required")
	}
	if a.State().AgentID == 0 {
		if err := a.enrollLoop(ctx); err != nil {
			return err
		}
	}
	a.mu.Lock()
	if c := a.st.Config; c != nil {
		a.sup.Set(c.HBBSPublicKey, c.Run)
		a.log.Info("agent: enforcing persisted config", slog.Int64("version", c.Version), slog.Bool("run", c.Run))
	}
	a.mu.Unlock()
	if strings.HasPrefix(a.State().PanelURL, "http://") {
		a.log.Warn("agent: the panel URL is plain http; use https outside of tests")
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); a.sup.Run(ctx) }()
	go func() { defer wg.Done(); a.applyLoop(ctx) }()
	go func() { defer wg.Done(); a.healthLoop(ctx) }()
	a.sessionLoop(ctx)
	wg.Wait()
	return nil
}

// ErrTokenRejected means the panel refused the enrollment token.
var ErrTokenRejected = errors.New("agent: the panel rejected the enrollment token (invalid, expired or already used); create a new one in the panel")

func (a *Agent) enrollLoop(ctx context.Context) error {
	backoff := time.Second
	for {
		err := a.enroll(ctx)
		if err == nil {
			return nil
		}
		if a.cfg.EnrollToken == "" {
			return errors.New("agent: not enrolled and RDPANEL_AGENT_ENROLL_TOKEN is empty")
		}
		if errors.Is(err, ErrTokenRejected) {
			a.log.Error("agent: enrollment refused; waiting for a new token (update RDPANEL_AGENT_ENROLL_TOKEN and restart)", slog.Any("err", err))
			backoff = a.cfg.MaxBackoff
		} else {
			a.log.Warn("agent: enrollment failed, retrying", slog.Any("err", err), slog.Duration("in", backoff))
		}
		if !sleepCtx(ctx, jitter(backoff)) {
			return ctx.Err()
		}
		backoff = min(backoff*2, a.cfg.MaxBackoff)
	}
}

func (a *Agent) hostname() string {
	if a.cfg.Hostname != "" {
		return a.cfg.Hostname
	}
	h, _ := os.Hostname()
	return h
}

func (a *Agent) hbbrVersion(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, a.cfg.HBBRBin, "--version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}

func (a *Agent) enroll(ctx context.Context) error {
	if a.cfg.EnrollToken == "" {
		return errors.New("no enrollment token")
	}
	body, _ := json.Marshal(agentproto.EnrollRequest{Token: a.cfg.EnrollToken, AgentPubkey: a.key.Public().(ed25519.PublicKey),
		Hostname: a.hostname(), Version: buildinfo.Version, HBBRVersion: a.hbbrVersion(ctx), Arch: runtime.GOARCH})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.State().PanelURL+agentproto.EnrollPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode == http.StatusUnauthorized {
		return ErrTokenRejected
	}
	if resp.StatusCode != http.StatusOK {
		var e agentproto.Error
		_ = json.Unmarshal(raw, &e)
		return fmt.Errorf("enroll: HTTP %d %s", resp.StatusCode, e.Message)
	}
	var er agentproto.EnrollResponse
	if err := json.Unmarshal(raw, &er); err != nil || er.AgentID <= 0 {
		return fmt.Errorf("enroll: bad response")
	}
	a.mu.Lock()
	a.st.AgentID, a.st.RelayName, a.st.Config = er.AgentID, er.RelayName, nil
	st := a.st
	a.mu.Unlock()
	if err := SaveState(a.cfg.DataDir, st); err != nil {
		return fmt.Errorf("persist state: %w", err)
	}
	a.log.Info("agent: enrolled", slog.String("relay", er.RelayName), slog.Int64("agent_id", er.AgentID),
		slog.Bool("approved", er.Approved))
	if !er.Approved {
		a.log.Info("agent: waiting for an admin to approve this relay in the panel")
	}
	return nil
}

func jitter(d time.Duration) time.Duration {
	return d/2 + time.Duration(rand.Int64N(int64(d)/2+1))
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// errRevoked is returned by a session the panel closed as revoked.
var errRevoked = errors.New("agent: the panel revoked this agent (or its enrollment was reset)")

func (a *Agent) sessionLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := a.runSession(ctx)
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errRevoked) {
			a.log.Error("agent: revoked by the panel; stopping hbbr and re-enrolling", slog.Any("err", err))
			a.sup.Set("", false)
			a.mu.Lock()
			a.st.AgentID, a.st.Config = 0, nil
			st := a.st
			a.mu.Unlock()
			_ = SaveState(a.cfg.DataDir, st)
			if err := a.enrollLoop(ctx); err != nil {
				return
			}
			backoff = time.Second
			continue
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		a.log.Warn("agent: panel session ended, reconnecting", slog.Any("err", err), slog.Duration("in", backoff))
		if !sleepCtx(ctx, jitter(backoff)) {
			return
		}
		backoff = min(backoff*2, a.cfg.MaxBackoff)
	}
}

func wsURL(panel string) string {
	switch {
	case strings.HasPrefix(panel, "https://"):
		return "wss://" + strings.TrimPrefix(panel, "https://") + agentproto.ConnectPath
	case strings.HasPrefix(panel, "http://"):
		return "ws://" + strings.TrimPrefix(panel, "http://") + agentproto.ConnectPath
	}
	return panel + agentproto.ConnectPath
}

func closeErr(err error) error {
	switch websocket.CloseStatus(err) {
	case agentproto.CloseRevoked:
		return errRevoked
	case agentproto.CloseAuthFailed:
		var ce websocket.CloseError
		errors.As(err, &ce)
		return fmt.Errorf("agent: the panel refused the session: %s", ce.Reason)
	}
	return err
}

func (a *Agent) runSession(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st := a.State()
	ws, _, err := websocket.Dial(ctx, wsURL(st.PanelURL), &websocket.DialOptions{HTTPClient: a.http})
	if err != nil {
		return err
	}
	defer ws.CloseNow()
	ws.SetReadLimit(agentproto.MaxMessage)
	read := func(dst *agentproto.Envelope) error {
		if err := wsjson.Read(ctx, ws, dst); err != nil {
			return closeErr(err)
		}
		return nil
	}
	var env agentproto.Envelope
	if err := read(&env); err != nil {
		return err
	}
	var ch agentproto.Challenge
	if env.Type != agentproto.TypeChallenge || env.Decode(&ch) != nil || len(ch.Nonce) != agentproto.NonceSize {
		return errors.New("agent: expected a challenge from the panel")
	}
	if d := time.Since(time.Unix(ch.ServerTime, 0)); d > 30*time.Second || d < -30*time.Second {
		a.log.Warn("agent: clock differs from the panel's", slog.Duration("skew", d.Round(time.Second)))
	}
	sess := &session{ws: ws}
	if err := sess.send(ctx, agentproto.TypeAuth, "", agentproto.Sign(a.key, ch.Nonce, st.AgentID, time.Now())); err != nil {
		return err
	}
	if err := read(&env); err != nil {
		return err
	}
	var wel agentproto.Welcome
	if env.Type != agentproto.TypeWelcome || env.Decode(&wel) != nil {
		return errors.New("agent: expected welcome")
	}
	a.log.Info("agent: connected to the panel", slog.String("relay", wel.RelayName), slog.Bool("approved", wel.Approved))
	var ver int64
	if st.Config != nil {
		ver = st.Config.Version
	}
	if err := sess.send(ctx, agentproto.TypeHello, "", agentproto.Hello{AgentVersion: buildinfo.Version,
		HBBRVersion: a.hbbrVersion(ctx), Hostname: a.hostname(), Arch: runtime.GOARCH, ConfigVersion: ver}); err != nil {
		return err
	}
	a.mu.Lock()
	a.session = sess
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		if a.session == sess {
			a.session = nil
		}
		a.mu.Unlock()
	}()
	a.sendAck(ctx) // the last applied version (it may not have been acked)
	go a.statusLoop(ctx, sess)
	for {
		if err := read(&env); err != nil {
			return err
		}
		switch env.Type {
		case agentproto.TypeConfig:
			var c agentproto.Config
			if err := env.Decode(&c); err != nil {
				return err
			}
			if err := a.setConfig(c); err != nil {
				a.log.Error("agent: persist config", slog.Any("err", err))
			}
		case agentproto.TypeCommand:
			var c agentproto.Command
			if err := env.Decode(&c); err != nil {
				return err
			}
			go func(id string) {
				res := a.command(ctx, c.Name)
				if err := sess.send(ctx, agentproto.TypeResult, id, res); err != nil {
					a.log.Debug("agent: send result", slog.Any("err", err))
				}
			}(env.ID)
		default:
			a.log.Debug("agent: ignoring message", slog.String("type", env.Type))
		}
	}
}

// setConfig persists a new config and hands it to the supervisor and the
// apply loop.
func (a *Agent) setConfig(c agentproto.Config) error {
	if c.Run && c.HBBSPublicKey == "" {
		a.log.Error("agent: config without the hbbs public key; refusing to start hbbr (it would generate a foreign key)")
		c.Run = false
	}
	a.mu.Lock()
	a.st.Config = &c
	st := a.st
	a.mu.Unlock()
	a.log.Info("agent: new config", slog.Int64("version", c.Version), slog.Bool("run", c.Run),
		slog.String("key_fp", agentproto.KeyFingerprint(c.HBBSPublicKey)))
	a.sup.Set(c.HBBSPublicKey, c.Run)
	notify(a.cfgCh)
	return SaveState(a.cfg.DataDir, st)
}

func (a *Agent) command(ctx context.Context, name string) agentproto.Result {
	switch name {
	case agentproto.CommandUsage:
		u, err := a.ctl.Usage(ctx)
		if err != nil {
			return agentproto.Result{Error: err.Error()}
		}
		return agentproto.Result{OK: true, Usage: u}
	case agentproto.CommandRestartHBBR:
		a.sup.Restart()
		return agentproto.Result{OK: true}
	}
	return agentproto.Result{Error: "unknown command " + strconv.Quote(name)}
}

func (a *Agent) sendAck(ctx context.Context) {
	a.mu.Lock()
	sess, ver := a.session, a.applied
	errs := map[string]string{}
	for k, v := range a.applyErrs {
		errs[k] = v
	}
	a.mu.Unlock()
	if sess == nil || ver == 0 {
		return
	}
	if err := sess.send(ctx, agentproto.TypeAck, "", agentproto.Ack{Version: ver, Errors: errs}); err != nil {
		a.log.Debug("agent: send ack", slog.Any("err", err))
	}
}

// applyLoop applies the desired hbbr settings whenever the config changes,
// hbbr (re)starts, and periodically (drift check).
func (a *Agent) applyLoop(ctx context.Context) {
	t := time.NewTicker(a.cfg.ApplyInterval)
	defer t.Stop()
	for {
		force := false
		select {
		case <-ctx.Done():
			return
		case <-a.cfgCh:
			force = true
		case <-a.sup.Started():
			force = true
		case <-t.C:
		}
		a.applyOnce(ctx, force)
	}
}

// applyOnce pushes the config to hbbr (exported via ApplyNow for tests).
func (a *Agent) applyOnce(ctx context.Context, force bool) {
	a.mu.Lock()
	c := a.st.Config
	a.mu.Unlock()
	if c == nil {
		return
	}
	s := c.Settings.Reconcile()
	errs := map[string]string{}
	if err := reconcile.WriteListFiles(a.cfg.DataDir, s); err != nil {
		errs["list_files"] = err.Error()
	}
	if c.Run {
		// hbbr needs a moment after a (re)start before its admin port answers.
		var rtt time.Duration
		var err error
		for i := 0; i < 20; i++ {
			if rtt, err = a.ctl.Ping(ctx, control.HBBR); err == nil || ctx.Err() != nil {
				break
			}
			if !sleepCtx(ctx, 500*time.Millisecond) {
				return
			}
		}
		if err != nil {
			errs["hbbr"] = "admin channel: " + err.Error()
		} else {
			a.mu.Lock()
			a.adminRTT = rtt
			a.mu.Unlock()
			for _, r := range reconcile.ApplyHBBR(ctx, a.ctl, s, force) {
				if r.Err != nil {
					errs[r.Key] = r.Err.Error()
				} else if r.Changed && !force {
					a.log.Info("agent: corrected drift", slog.String("key", r.Key))
				}
			}
		}
	}
	a.mu.Lock()
	changed := a.applied != c.Version || !sameErrs(a.applyErrs, errs)
	a.applied, a.applyErrs = c.Version, errs
	a.mu.Unlock()
	if len(errs) > 0 && changed {
		a.log.Warn("agent: apply errors", slog.Any("errors", errs))
	}
	if changed || force {
		a.sendAck(ctx)
	}
}

// ApplyNow runs one forced apply pass (tests).
func (a *Agent) ApplyNow(ctx context.Context) { a.applyOnce(ctx, true) }

func sameErrs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// status builds the current status report.
func (a *Agent) status(ctx context.Context) agentproto.Status {
	hb := a.sup.Status()
	var usage agentproto.UsageSummary
	if hb.Up {
		if u, err := a.ctl.Usage(ctx); err == nil {
			usage.RelaysActive = len(u)
			for _, r := range u {
				usage.TotalMB += r.TotalMB
				usage.CurrentKbps += float64(r.CurrentKbps)
			}
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usage = usage
	hb.AdminRTTMs = float64(a.adminRTT.Microseconds()) / 1000
	st := agentproto.Status{AgentVersion: buildinfo.Version, ConfigVersion: a.applied, HBBR: hb,
		Host: a.host.sample(time.Now()), Usage: usage}
	if len(a.applyErrs) > 0 {
		st.ApplyErrors = map[string]string{}
		for k, v := range a.applyErrs {
			st.ApplyErrors[k] = v
		}
	}
	return st
}

func (a *Agent) statusLoop(ctx context.Context, sess *session) {
	t := time.NewTicker(a.cfg.StatusInterval)
	defer t.Stop()
	for {
		if err := sess.send(ctx, agentproto.TypeStatus, "", a.status(ctx)); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// healthLoop keeps agent-health.json fresh for `rdpanel agent health`.
func (a *Agent) healthLoop(ctx context.Context) {
	t := time.NewTicker(a.cfg.HealthInterval)
	defer t.Stop()
	for {
		a.mu.Lock()
		h := Health{Time: time.Now().UTC(), Connected: a.session != nil, Run: a.st.Config != nil && a.st.Config.Run}
		a.mu.Unlock()
		h.HBBRUp = a.sup.Status().Up
		if raw, err := json.Marshal(h); err == nil {
			_ = writeAtomic(a.cfg.DataDir+string(os.PathSeparator)+HealthFile, raw, 0o644)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
