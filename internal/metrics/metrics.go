// Package metrics holds the panel's business counters (logins, client
// tokens, client audit records) and the build-info gauge. Every label has a
// fixed, small value set: never put user names, ids or IPs in a label.
//
// All Panel methods are safe on a nil *Panel, so packages that receive it
// through their Deps work unchanged in tests that do not wire metrics.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bropines/rdpanel/internal/buildinfo"
)

// Surfaces (where a login happened).
const (
	SurfacePanel  = "panel"  // web admin panel
	SurfaceClient = "client" // RustDesk client (/api/login, OIDC auth-query)
)

// Login methods: the factor that decided the attempt.
const (
	MethodPassword = "password"
	MethodTOTP     = "totp"
	MethodEmail    = "email"
	MethodOIDC     = "oidc"
	MethodWebAuth  = "webauth"
	MethodPasskey  = "passkey" // panel only (WebAuthn)
	MethodOther    = "other"
)

// Login results.
const (
	ResultSuccess     = "success"
	ResultFailure     = "failure"
	ResultRateLimited = "rate_limited"
)

// Client audit record kinds (/api/audit/{conn,file,alarm}).
const (
	AuditConn  = "conn"
	AuditFile  = "file"
	AuditAlarm = "alarm"
)

// Client audit results.
const (
	AuditStored   = "stored"
	AuditRejected = "rejected" // unknown device / uuid mismatch / bad request
	AuditError    = "error"    // storage failure (the client retries)
)

// Panel is the set of business counters.
type Panel struct {
	logins *prometheus.CounterVec
	tokens *prometheus.CounterVec
	audits *prometheus.CounterVec
	apiTok *prometheus.CounterVec
}

// New registers the counters on reg (which may be nil) and pre-initialises
// every label combination to zero so rate() and alerts see the series from
// the first scrape.
func New(reg prometheus.Registerer) *Panel {
	p := &Panel{
		logins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "auth", Name: "login_attempts_total",
			Help: "Login attempts by surface (panel|client), deciding factor (password|totp|email|oidc|webauth|passkey) and result (success|failure|rate_limited).",
		}, []string{"surface", "method", "result"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "auth", Name: "client_tokens_issued_total",
			Help: "RustDesk client tokens issued, by login method.",
		}, []string{"method"}),
		audits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "audit", Name: "events_total",
			Help: "Client audit records received (/api/audit/*), by type (conn|file|alarm) and result (stored|rejected|error).",
		}, []string{"type", "result"}),
		apiTok: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rdpanel", Subsystem: "auth", Name: "api_token_requests_total",
			Help: "Admin API requests presenting a personal API token (Bearer rdp_...), by result (success|failure|rate_limited).",
		}, []string{"result"}),
	}
	for _, s := range []string{SurfacePanel, SurfaceClient} {
		for _, m := range []string{MethodPassword, MethodTOTP, MethodEmail, MethodOIDC} {
			for _, r := range []string{ResultSuccess, ResultFailure} {
				p.logins.WithLabelValues(s, m, r)
			}
		}
		p.logins.WithLabelValues(s, MethodPassword, ResultRateLimited)
	}
	for _, r := range []string{ResultSuccess, ResultFailure} {
		p.logins.WithLabelValues(SurfacePanel, MethodPasskey, r)
	}
	for _, m := range []string{MethodPassword, MethodOIDC} {
		p.tokens.WithLabelValues(m)
	}
	for _, t := range []string{AuditConn, AuditFile, AuditAlarm} {
		for _, r := range []string{AuditStored, AuditRejected, AuditError} {
			p.audits.WithLabelValues(t, r)
		}
	}
	for _, r := range []string{ResultSuccess, ResultFailure, ResultRateLimited} {
		p.apiTok.WithLabelValues(r)
	}
	if reg != nil {
		reg.MustRegister(p.logins, p.tokens, p.audits, p.apiTok)
	}
	return p
}

func oneOf(v, fallback string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return fallback
}

func method(m string) string {
	return oneOf(m, MethodOther, MethodPassword, MethodTOTP, MethodEmail, MethodOIDC, MethodWebAuth, MethodPasskey)
}

// Login counts one login attempt.
func (p *Panel) Login(surface, m, result string) {
	if p == nil {
		return
	}
	p.logins.WithLabelValues(oneOf(surface, SurfaceClient, SurfacePanel, SurfaceClient), method(m),
		oneOf(result, ResultFailure, ResultSuccess, ResultFailure, ResultRateLimited)).Inc()
}

// TokenIssued counts one client token minted by a login with method m.
func (p *Panel) TokenIssued(m string) {
	if p == nil {
		return
	}
	p.tokens.WithLabelValues(method(m)).Inc()
}

// Audit counts one client audit record.
func (p *Panel) Audit(kind, result string) {
	if p == nil {
		return
	}
	p.audits.WithLabelValues(oneOf(kind, "other", AuditConn, AuditFile, AuditAlarm),
		oneOf(result, AuditError, AuditStored, AuditRejected, AuditError)).Inc()
}

// APITokenAuth counts one request authenticated (or rejected) by a
// personal API token.
func (p *Panel) APITokenAuth(result string) {
	if p == nil {
		return
	}
	p.apiTok.WithLabelValues(oneOf(result, ResultFailure, ResultSuccess, ResultFailure, ResultRateLimited)).Inc()
}

// RegisterBuildInfo registers rdpanel_build_info (constant 1) labelled with
// the build metadata and the hbbs/hbbr source ref ("" when unknown).
func RegisterBuildInfo(reg prometheus.Registerer, bi buildinfo.Info, hbbRef string) {
	if reg == nil {
		return
	}
	if hbbRef == "" {
		hbbRef = "unknown"
	}
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "rdpanel", Name: "build_info",
		Help: "Build metadata of rdpanel (constant 1): version, commit, Go version and the hbbs/hbbr source ref.",
	}, []string{"version", "commit", "go_version", "hbb_ref"})
	commit := bi.Commit
	if commit == "" {
		commit = "unknown"
	}
	g.WithLabelValues(bi.Version, commit, bi.GoVersion, hbbRef).Set(1)
	reg.MustRegister(g)
}
