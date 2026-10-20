package federation

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/relays"
	"github.com/bropines/rdpanel/internal/store"
)

// Federation is opt-in (RDPANEL_FEDERATION_ENABLED). A panel exposes its
// healthy relays at /api/v1/federation/{health,relays} to peers presenting
// its RDPANEL_FEDERATION_TOKEN, and pulls the relays of the peers registered
// in Server -> Federation with the token each of them gave out. Imported
// relays are listed for visibility only; they never enter hbbs' rotation.

// Limits on what a peer may answer.
const (
	maxResponseBytes   = 512 << 10
	maxAnnouncedRelays = 256
	initialSyncDelay   = 5 * time.Second
)

// limiterKeyPrefix keys failed inbound authentications per client IP.
const limiterKeyPrefix = "federation-ip:"

// PublicRelayDTO is what a panel announces to peers.
type PublicRelayDTO struct {
	Name       string `json:"name"`
	PublicHost string `json:"public_host"`
	Region     string `json:"region,omitempty"`
	Health     string `json:"health"`
}

// HealthDTO is the response to /api/v1/federation/health.
type HealthDTO struct {
	Status        string `json:"status"`
	Version       string `json:"version"`
	ActiveRelays  int    `json:"active_relays"`
	TimestampNano int64  `json:"timestamp_nano"`
}

// RelaySource provides active local relays to announce.
type RelaySource interface {
	List(ctx context.Context) ([]relays.View, relays.Rotation, error)
}

// Config configures the federation service.
type Config struct {
	Store        *Store
	Relays       RelaySource
	Version      string
	SyncInterval time.Duration
	Log          *slog.Logger
	// InboundToken is the bearer token peers must present to this panel's
	// federation endpoints (RDPANEL_FEDERATION_TOKEN). Empty refuses all.
	InboundToken string
	// AllowPrivate reports whether peers may live on private, loopback or
	// link-local addresses (the relays "allow private hosts" option).
	// nil: never.
	AllowPrivate func(context.Context) bool
	// HTTPClient overrides the outgoing client (tests). The default refuses
	// private addresses at dial time (unless AllowPrivate), ignores proxy
	// variables and does not follow redirects.
	HTTPClient *http.Client
	// Lookup resolves peer host names for the registration check
	// (default net.DefaultResolver.LookupNetIP).
	Lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Service manages federation peering, relay synchronization and public federation endpoints.
type Service struct {
	st           *Store
	relays       RelaySource
	version      string
	interval     time.Duration
	log          *slog.Logger
	client       *http.Client
	inbound      [sha256.Size]byte
	hasInbound   bool
	allowPrivate func(context.Context) bool
	lookup       func(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// NewService creates a new federation service.
func NewService(cfg Config) *Service {
	interval := cfg.SyncInterval
	if interval <= 0 {
		interval = 60 * time.Second
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		st:           cfg.Store,
		relays:       cfg.Relays,
		version:      cfg.Version,
		interval:     interval,
		log:          log,
		client:       cfg.HTTPClient,
		allowPrivate: cfg.AllowPrivate,
		lookup:       cfg.Lookup,
	}
	if s.allowPrivate == nil {
		s.allowPrivate = func(context.Context) bool { return false }
	}
	if s.lookup == nil {
		s.lookup = net.DefaultResolver.LookupNetIP
	}
	if s.client == nil {
		s.client = guardedClient()
	}
	if cfg.InboundToken != "" {
		s.inbound, s.hasInbound = sha256.Sum256([]byte(cfg.InboundToken)), true
	}
	return s
}

// Store returns the underlying store.
func (s *Service) Store() *Store { return s.st }

// ---- outgoing: SSRF guard ----

// allowPrivateKey carries the "allow private peers" decision to the dialer.
type allowPrivateKey struct{}

// errPrivatePeer is returned by the dialer for a refused address.
var errPrivatePeer = errors.New("federation: peer address is private or loopback")

// guardedClient dials only public addresses unless the request context
// allows private ones. The check runs on the address actually dialed, so DNS
// answers cannot rebind it; no proxy and no redirect can bypass it.
func guardedClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		ControlContext: func(ctx context.Context, _, address string, _ syscall.RawConn) error {
			if allow, _ := ctx.Value(allowPrivateKey{}).(bool); allow {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			a, err := netip.ParseAddr(host)
			if err != nil || relays.IsPrivate(a) {
				return errPrivatePeer
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			// One sync is two small requests a minute; fresh connections keep
			// the dial-time check authoritative when the option changes.
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// CreatePeer validates and registers a peer. Unless private peers are
// allowed, an api_url pointing at a private, loopback or link-local address
// (literally or via DNS) is refused.
func (s *Service) CreatePeer(ctx context.Context, in CreatePeerInput) (Peer, error) {
	apiURL, u, err := NormalizeAPIURL(in.APIURL)
	if err != nil {
		return Peer{}, err
	}
	if !s.allowPrivate(ctx) {
		if err := s.checkPublicHost(ctx, u.Hostname()); err != nil {
			return Peer{}, err
		}
	}
	in.APIURL = apiURL
	return s.st.Create(ctx, in)
}

var errPrivateAPIURL = fmt.Errorf("%w: api_url points to a private, loopback or link-local address; "+
	"enable \"Allow private relay hosts\" (Server -> Relays -> Options) to peer over a private network", store.ErrInvalid)

func (s *Service) checkPublicHost(ctx context.Context, host string) error {
	if a, err := netip.ParseAddr(host); err == nil {
		if relays.IsPrivate(a) {
			return errPrivateAPIURL
		}
		return nil
	}
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return errPrivateAPIURL
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := s.lookup(lctx, "ip", host)
	if err != nil {
		return nil // not resolvable now; the dial-time check still applies
	}
	for _, a := range addrs {
		if relays.IsPrivate(a) {
			return errPrivateAPIURL
		}
	}
	return nil
}

// ---- outgoing: sync ----

// syncError is a sync failure: msg is what Peer.LastError and the admin API
// show (no URLs, dial errors or response bodies); cause goes to the log.
type syncError struct {
	msg   string
	cause error
}

func (e *syncError) Error() string { return e.msg }
func (e *syncError) Unwrap() error { return e.cause }

func syncErr(cause error, format string, args ...any) error {
	return &syncError{msg: fmt.Sprintf(format, args...), cause: cause}
}

// SyncPeer checks a peer's health and, with sync_relays, imports the relays
// it announces. Any failure (unreachable, non-200, invalid or inconsistent
// answer) marks the peer unreachable with a short reason; the returned
// error's text is that reason.
func (s *Service) SyncPeer(ctx context.Context, id int64) error {
	p, err := s.st.Get(ctx, id)
	if err != nil {
		return err
	}
	token, err := s.st.Token(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNoToken) {
			return s.fail(ctx, p, syncErr(err, "no stored token for this peer: delete it and add it again"))
		}
		return s.fail(ctx, p, syncErr(err, "internal error (see the panel log)"))
	}
	ctx = context.WithValue(ctx, allowPrivateKey{}, s.allowPrivate(ctx))

	var health HealthDTO
	if err := s.getJSON(ctx, p.APIURL+"/api/v1/federation/health", token, &health); err != nil {
		return s.fail(ctx, p, err)
	}
	if health.Status != "ok" {
		return s.fail(ctx, p, syncErr(nil, "peer reports status %q", truncate(health.Status, 32)))
	}

	if p.SyncRelays {
		var announced []PublicRelayDTO
		if err := s.getJSON(ctx, p.APIURL+"/api/v1/federation/relays", token, &announced); err != nil {
			return s.fail(ctx, p, err)
		}
		federated, err := validateAnnounced(id, announced)
		if err != nil {
			return s.fail(ctx, p, err)
		}
		if err := s.st.ReplaceFederatedRelays(ctx, id, federated); err != nil {
			return s.fail(ctx, p, syncErr(err, "internal error (see the panel log)"))
		}
	}

	if err := s.st.UpdateStatus(ctx, id, StatusActive, nil); err != nil {
		return err
	}
	return nil
}

// getJSON fetches url with the bearer token and decodes a JSON body.
func (s *Service) getJSON(ctx context.Context, url, token string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return syncErr(err, "invalid peer URL")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "rdpanel-federation/"+s.version)
	resp, err := s.client.Do(req)
	if err != nil {
		if errors.Is(err, errPrivatePeer) {
			return syncErr(err, "peer address is private or loopback (allow private relay hosts to permit it)")
		}
		return syncErr(err, "peer unreachable")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return syncErr(nil, "peer rejected the token (HTTP %d)", resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound:
		return syncErr(nil, "peer has no federation endpoints (HTTP 404): is federation enabled there?")
	case resp.StatusCode == http.StatusTooManyRequests:
		return syncErr(nil, "peer rate-limited this panel (HTTP 429)")
	case resp.StatusCode != http.StatusOK:
		return syncErr(nil, "peer answered HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return syncErr(err, "peer unreachable")
	}
	if len(body) > maxResponseBytes {
		return syncErr(nil, "peer response is too large")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return syncErr(err, "invalid response from peer")
	}
	return nil
}

// validateAnnounced checks a peer's relay list: bounded, unique names,
// relay hosts in hbbs syntax.
func validateAnnounced(peerID int64, announced []PublicRelayDTO) ([]FederatedRelay, error) {
	if len(announced) > maxAnnouncedRelays {
		return nil, syncErr(nil, "peer announced more than %d relays", maxAnnouncedRelays)
	}
	seen := make(map[string]bool, len(announced))
	out := make([]FederatedRelay, 0, len(announced))
	for _, r := range announced {
		name := strings.TrimSpace(r.Name)
		if name == "" || len(name) > 64 || strings.ContainsFunc(name, unicode.IsControl) {
			return nil, syncErr(nil, "peer announced a relay with an invalid name")
		}
		if seen[name] {
			return nil, syncErr(nil, "peer announced relay %q twice", name)
		}
		seen[name] = true
		if relays.ValidateHost(r.PublicHost) != nil {
			return nil, syncErr(nil, "peer announced an invalid host for relay %q", name)
		}
		if len(r.Region) > 32 || strings.ContainsFunc(r.Region, unicode.IsControl) ||
			len(r.Health) > 16 || strings.ContainsFunc(r.Health, unicode.IsControl) {
			return nil, syncErr(nil, "peer announced invalid metadata for relay %q", name)
		}
		out = append(out, FederatedRelay{PeerID: peerID, RemoteName: name, PublicHost: r.PublicHost,
			Region: r.Region, Health: r.Health})
	}
	return out, nil
}

// fail marks peer p unreachable with err's public text and returns err
// (not when ctx ended: shutdown or an abandoned request says nothing about
// the peer).
func (s *Service) fail(ctx context.Context, p Peer, err error) error {
	if ctx.Err() != nil {
		return err
	}
	msg := err.Error()
	var se *syncError
	if errors.As(err, &se) && se.cause != nil {
		s.log.Warn("federation: sync failed", slog.String("peer", p.Name), slog.String("reason", msg), slog.Any("err", se.cause))
	} else {
		s.log.Warn("federation: sync failed", slog.String("peer", p.Name), slog.String("reason", msg))
	}
	if uerr := s.st.UpdateStatus(ctx, p.ID, StatusUnreachable, &msg); uerr != nil {
		s.log.Warn("federation: record sync status", slog.String("peer", p.Name), slog.Any("err", uerr))
	}
	return err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// SyncAll passes through all peers and attempts sync.
func (s *Service) SyncAll(ctx context.Context) {
	peers, err := s.st.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("federation: list peers", slog.Any("err", err))
		}
		return
	}
	for _, p := range peers {
		if ctx.Err() != nil {
			return
		}
		_ = s.SyncPeer(ctx, p.ID) // failures are recorded and logged
	}
}

// Run is the background sync loop (an app worker); it returns when ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTimer(initialSyncDelay)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SyncAll(ctx)
			t.Reset(s.interval)
		}
	}
}

// ---- incoming: public endpoints ----

// Authorized reports whether r carries this panel's federation token. The
// comparison is constant-time over SHA-256 digests.
func (s *Service) Authorized(r *http.Request) bool {
	if !s.hasInbound {
		return false
	}
	tok, ok := auth.BearerToken(r.Header.Get("Authorization"))
	if !ok {
		return false
	}
	got := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(got[:], s.inbound[:]) == 1
}

// RegisterPublic mounts GET /health and GET /relays (under
// /api/v1/federation). Both require the federation bearer token; failed
// attempts back off per client IP through limiter (nil: no limit).
func (s *Service) RegisterPublic(r chi.Router, limiter auth.Limiter) {
	r.Use(s.requireToken(limiter))
	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, s.PublicHealth(req.Context()))
	})
	r.Get("/relays", func(w http.ResponseWriter, req *http.Request) {
		res, err := s.PublicRelays(req.Context())
		if err != nil {
			s.log.ErrorContext(req.Context(), "federation: list relays", slog.Any("err", err))
			httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
			return
		}
		if res == nil {
			res = []PublicRelayDTO{}
		}
		httpx.WriteJSON(w, http.StatusOK, res)
	})
}

func (s *Service) requireToken(limiter auth.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := limiterKeyPrefix + httpx.ClientIPString(r)
			if limiter != nil {
				if ok, wait := limiter.Allow(key); !ok {
					w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
					httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
					return
				}
			}
			if !s.Authorized(r) {
				if limiter != nil {
					limiter.Failure(key)
				}
				w.Header().Set("WWW-Authenticate", `Bearer realm="rdpanel-federation"`)
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid federation token")
				return
			}
			if limiter != nil {
				limiter.Success(key)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// PublicHealth returns local health for peering verification.
func (s *Service) PublicHealth(ctx context.Context) HealthDTO {
	activeCount := 0
	if s.relays != nil {
		views, _, err := s.relays.List(ctx)
		if err == nil {
			for _, v := range views {
				if v.Health == relays.HealthActive {
					activeCount++
				}
			}
		}
	}
	return HealthDTO{
		Status:        "ok",
		Version:       s.version,
		ActiveRelays:  activeCount,
		TimestampNano: time.Now().UTC().UnixNano(),
	}
}

// PublicRelays returns local active relays for peering peers.
func (s *Service) PublicRelays(ctx context.Context) ([]PublicRelayDTO, error) {
	if s.relays == nil {
		return nil, nil
	}
	views, _, err := s.relays.List(ctx)
	if err != nil {
		return nil, err
	}
	var res []PublicRelayDTO
	for _, v := range views {
		if v.Health == relays.HealthActive && v.InRotation && v.AdminState == relays.StateEnabled {
			res = append(res, PublicRelayDTO{
				Name:       v.Name,
				PublicHost: v.PublicHost,
				Region:     v.Region,
				Health:     string(v.Health),
			})
		}
	}
	return res, nil
}
