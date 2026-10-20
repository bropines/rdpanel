package authz

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rdpanel/internal/auth"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/httpx"
)

// Path is where the endpoint is mounted on the panel's root router (outside
// /api: it is not part of the admin API contract).
const Path = "/internal/hbbs/authorize"

// MaxBody bounds the request body.
const MaxBody = 4 << 10

// decideTimeout bounds one decision; hbbs proceeds without permissions
// when the panel does not answer in time anyway.
const decideTimeout = 3 * time.Second

// Metric results.
const (
	ResultAllow = "allow"
	ResultDeny  = "deny"
	ResultError = "error"
)

// forwardingHeaders are set by reverse proxies, never by hbbs. A proxy on
// the same host connects from loopback, so their presence marks a request
// that did not come from hbbs.
var forwardingHeaders = []string{"X-Forwarded-For", "X-Real-Ip", "Forwarded", "X-Forwarded-Host", "Cf-Connecting-Ip", "True-Client-Ip"}

// Response is the body returned to hbbs.
type Response struct {
	Allow bool `json:"allow"`
	// Permissions is the ControlPermissions bitmap as a decimal string (u64
	// does not survive JSON numbers in every parser).
	Permissions string `json:"permissions"`
	// Reason is shown to the controller when Allow is false.
	Reason string `json:"reason"`
}

// Handler serves POST Path for hbbs. Anything but a POST from a loopback
// TCP peer without proxy headers and with the shared secret as bearer token
// gets the same 404 as an unknown path.
type Handler struct {
	svc          *Service
	secretDigest []byte
	realIPHeader string
	log          *slog.Logger
	total        *prometheus.CounterVec
}

// NewHandler returns the endpoint for svc. secret is the bearer secret hbbs
// presents (AUTHZ_SECRET_FILE); realIPHeader is the panel's configured
// client-IP header (also refused); reg may be nil.
func NewHandler(svc *Service, secret, realIPHeader string, reg prometheus.Registerer, log *slog.Logger) (*Handler, error) {
	if svc == nil || secret == "" {
		return nil, errors.New("authz: service and secret are required")
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	h := &Handler{
		svc:          svc,
		secretDigest: crypto.HashToken(secret),
		realIPHeader: http.CanonicalHeaderKey(realIPHeader),
		log:          log,
		total: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "hbbs", Name: "authz_total",
			Help: "Connection authorization requests from hbbs (/internal/hbbs/authorize), by result (allow|deny|error). Errors leave hbbs without the panel's decision (fail-open).",
		}, []string{"result"}),
	}
	for _, r := range []string{ResultAllow, ResultDeny, ResultError} {
		h.total.WithLabelValues(r)
	}
	if reg != nil {
		if err := reg.Register(h.total); err != nil {
			return nil, err
		}
	}
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !loopbackPeer(r.RemoteAddr) || h.proxied(r) {
		httpx.NotFound(w, r)
		return
	}
	tok, ok := auth.BearerToken(r.Header.Get("Authorization"))
	if !ok || !crypto.Equal(crypto.HashToken(tok), h.secretDigest) {
		// From loopback this is a misconfigured hbbs (wrong AUTHZ_SECRET_FILE):
		// it then runs without the panel's decisions.
		h.total.WithLabelValues(ResultError).Inc()
		h.log.WarnContext(r.Context(), "hbbs authorize: missing or wrong secret (check AUTHZ_SECRET_FILE of hbbs)")
		httpx.NotFound(w, r)
		return
	}

	var req Request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBody))
	if err := dec.Decode(&req); err != nil {
		h.total.WithLabelValues(ResultError).Inc()
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			httpx.WriteError(w, http.StatusRequestEntityTooLarge, "too_large", "request body too large")
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if req.TargetID == "" {
		h.total.WithLabelValues(ResultError).Inc()
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "target_id is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), decideTimeout)
	defer cancel()
	d, err := h.svc.Authorize(ctx, req)
	if err != nil {
		h.total.WithLabelValues(ResultError).Inc()
		h.log.ErrorContext(ctx, "hbbs authorize failed", slog.String("target", req.TargetID), slog.Any("err", err))
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	attrs := []slog.Attr{
		slog.String("target", req.TargetID), slog.String("kind", req.Kind), slog.Int("conn_type", int(req.ConnType)),
		slog.String("controller_ip", req.ControllerIP), slog.String("cause", d.Cause), slog.Int64("user_id", d.UserID),
		slog.String("rule", d.Rule), slog.String("role", d.Role),
	}
	if d.Allow {
		h.total.WithLabelValues(ResultAllow).Inc()
		h.log.LogAttrs(ctx, slog.LevelDebug, "hbbs authorize: allowed", append(attrs, slog.String("permissions", d.Permissions.String()))...)
	} else {
		h.total.WithLabelValues(ResultDeny).Inc()
		h.log.LogAttrs(ctx, slog.LevelInfo, "hbbs authorize: denied", attrs...)
	}
	httpx.WriteJSON(w, http.StatusOK, Response{Allow: d.Allow, Permissions: d.Permissions.String(), Reason: d.Reason})
}

// proxied reports whether r carries a reverse proxy's forwarding header.
func (h *Handler) proxied(r *http.Request) bool {
	for _, k := range forwardingHeaders {
		if _, ok := r.Header[k]; ok {
			return true
		}
	}
	if h.realIPHeader != "" {
		if _, ok := r.Header[h.realIPHeader]; ok {
			return true
		}
	}
	return false
}

// loopbackPeer reports whether the TCP peer address is a loopback address
// (forwarding headers are deliberately ignored).
func loopbackPeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
}
