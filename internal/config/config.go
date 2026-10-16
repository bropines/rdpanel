// Package config loads and validates rdpanel configuration.
//
// Precedence (lowest to highest): built-in defaults, the optional YAML file
// named by RDPANEL_CONFIG, then environment variables. Paths derived from the
// data directory (database, key files) are filled in after merging when left
// empty.
package config

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the effective rdpanel configuration.
type Config struct {
	// Listen is the HTTP bind address, e.g. ":21114" or "127.0.0.1:31114".
	Listen string `yaml:"listen"`
	// PublicURL is the externally visible base URL (scheme://host[:port][/prefix])
	// that RustDesk clients use as "API server". Used for OIDC redirect URIs and HSTS.
	PublicURL string `yaml:"public_url"`
	// AdminURL is the base URL of the panel (SPA + /api/v1/*) when it is served
	// from a different host than PublicURL (e.g. an internal-only admin vhost).
	// Panel-facing redirects (OIDC login/linking) use it. Default: PublicURL.
	AdminURL string `yaml:"admin_url"`
	// DataDir holds panel.db, secret.key and (by default) jwt.key.
	DataDir string `yaml:"data_dir"`
	// DBPath is the SQLite database file. Default: <DataDir>/panel.db.
	DBPath string `yaml:"db_path"`
	// SecretKey is a base64-encoded 32-byte AEAD key for secrets at rest. When
	// empty, the key is loaded from (or generated into) SecretKeyFile.
	SecretKey Secret `yaml:"secret_key"`
	// SecretKeyFile is the AEAD key file. Default: <DataDir>/secret.key.
	SecretKeyFile string `yaml:"secret_key_file"`
	// UpdateCheck lets the About page look up the latest rustdesk-panel
	// release on GitHub (server-side, cached for 6 h; never blocks anything).
	UpdateCheck bool `yaml:"update_check"`

	Auth AuthConfig `yaml:"auth"`
	HTTP HTTPConfig `yaml:"http"`
	HBBS HBBSConfig `yaml:"hbbs"`
	Log  LogConfig  `yaml:"log"`
}

// AuthConfig holds token and session settings.
type AuthConfig struct {
	// JWTKey is the HS256 key for client JWTs, read verbatim (raw bytes, no
	// base64) from RUSTDESK_API_JWT_KEY. It must be byte-identical to the key
	// hbbs uses for MUST_LOGIN and to the legacy rustdesk-api key.
	JWTKey Secret `yaml:"jwt_key"`
	// JWTKeyFile is used when JWTKey is empty: a random key is generated there
	// on first start. Default: <DataDir>/jwt.key.
	JWTKeyFile string `yaml:"jwt_key_file"`
	// TokenLifetime is the sliding lifetime of client tokens (DB view).
	TokenLifetime time.Duration `yaml:"token_lifetime"`
	// JWTLifetime is the fixed lifetime embedded in the JWT "exp" claim (the hbbs
	// view; hbbs cannot see revocation). Zero means "same as TokenLifetime".
	JWTLifetime time.Duration `yaml:"jwt_lifetime"`
	// SessionIdle is the idle timeout of panel (web) sessions.
	SessionIdle time.Duration `yaml:"session_idle"`
	// SessionAbsolute is the absolute lifetime of panel (web) sessions.
	SessionAbsolute time.Duration `yaml:"session_absolute"`
}

// HTTPConfig holds HTTP-layer settings.
type HTTPConfig struct {
	// TrustedProxies lists IPs/CIDRs whose forwarding headers are honoured.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// RealIPHeader is the header carrying the client IP when the peer is a
	// trusted proxy: X-Forwarded-For, X-Real-IP or CF-Connecting-IP.
	RealIPHeader string `yaml:"real_ip_header"`
	// MetricsAllow lists IPs/CIDRs (as resolved client IPs) allowed to scrape /metrics.
	MetricsAllow []string `yaml:"metrics_allow"`

	trustedProxies []netip.Prefix
	metricsAllow   []netip.Prefix
}

// HBBSConfig describes how to reach the co-located hbbs/hbbr processes.
type HBBSConfig struct {
	// IDServer is the hbbs rendezvous TCP address (default 127.0.0.1:21116).
	// The hbbs admin channel listens on its port minus one.
	IDServer string `yaml:"id_server"`
	// RelayServer is the hbbr TCP address (default 127.0.0.1:21117); the hbbr
	// admin channel shares this port (loopback only).
	RelayServer string `yaml:"relay_server"`
	// PublicKeyFile is the hbbs public key served to clients. Default: <DataDir>/id_ed25519.pub.
	PublicKeyFile string `yaml:"public_key_file"`
	// Enabled turns on the hbbs/hbbr workers (presence poller, reconciler).
	Enabled bool `yaml:"enabled"`
	// PollInterval is the presence poll period (OnlineRequest to IDServer).
	PollInterval time.Duration `yaml:"poll_interval"`
	// ReconcileInterval is the admin-channel probe / drift-check period.
	ReconcileInterval time.Duration `yaml:"reconcile_interval"`
	// ListDir is hbbr's working directory; when set, the reconciler keeps
	// blacklist.txt / blocklist.txt there in sync with the desired lists
	// (/data in the s6 image). Empty disables writing the files.
	ListDir string `yaml:"list_dir"`
}

// LogConfig controls slog output.
type LogConfig struct {
	// Level is one of debug, info, warn, error.
	Level string `yaml:"level"`
	// Format is json (default) or text.
	Format string `yaml:"format"`
}

// Defaults returns the built-in configuration before files and env are applied.
func Defaults() *Config {
	return &Config{
		Listen:      ":21114",
		DataDir:     "/data",
		UpdateCheck: true,
		Auth: AuthConfig{
			TokenLifetime:   4380 * time.Hour,
			SessionIdle:     12 * time.Hour,
			SessionAbsolute: 7 * 24 * time.Hour,
		},
		HTTP: HTTPConfig{
			TrustedProxies: []string{"127.0.0.0/8", "::1/128"},
			RealIPHeader:   "X-Forwarded-For",
			MetricsAllow:   []string{"127.0.0.0/8", "::1/128"},
		},
		HBBS: HBBSConfig{
			IDServer:          "127.0.0.1:21116",
			RelayServer:       "127.0.0.1:21117",
			Enabled:           true,
			PollInterval:      5 * time.Second,
			ReconcileInterval: 15 * time.Second,
		},
		Log: LogConfig{Level: "info", Format: "json"},
	}
}

// LookupFunc matches os.LookupEnv; injected for tests.
type LookupFunc func(key string) (string, bool)

// Load builds the configuration from defaults, the YAML file named by
// RDPANEL_CONFIG (if any) and the environment, then validates it.
func Load(lookup LookupFunc) (*Config, error) {
	if lookup == nil {
		lookup = os.LookupEnv
	}
	cfg := Defaults()
	if path, ok := lookup("RDPANEL_CONFIG"); ok && path != "" {
		if err := cfg.loadFile(path); err != nil {
			return nil, err
		}
	}
	if err := cfg.applyEnv(lookup); err != nil {
		return nil, err
	}
	cfg.fillDerived()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) loadFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config: read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	// An empty file yields io.EOF, which is fine: defaults stay in place.
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("config: parse %s: %w", path, err)
	}
	return nil
}

type envVar struct {
	name  string
	apply func(string) error
}

func (c *Config) envVars() []envVar {
	str := func(dst *string) func(string) error {
		return func(v string) error { *dst = strings.TrimSpace(v); return nil }
	}
	// Secrets are taken verbatim: the JWT key must be byte-identical to hbbs'.
	sec := func(dst *Secret) func(string) error {
		return func(v string) error { *dst = Secret(v); return nil }
	}
	dur := func(dst *time.Duration) func(string) error {
		return func(v string) error {
			d, err := time.ParseDuration(strings.TrimSpace(v))
			if err != nil {
				return err
			}
			*dst = d
			return nil
		}
	}
	boolean := func(dst *bool) func(string) error {
		return func(v string) error {
			b, err := strconv.ParseBool(strings.TrimSpace(v))
			if err != nil {
				return err
			}
			*dst = b
			return nil
		}
	}
	list := func(dst *[]string) func(string) error {
		return func(v string) error { *dst = splitList(v); return nil }
	}
	return []envVar{
		{"RDPANEL_LISTEN", str(&c.Listen)},
		{"RDPANEL_PUBLIC_URL", str(&c.PublicURL)},
		{"RDPANEL_ADMIN_URL", str(&c.AdminURL)},
		{"RDPANEL_DATA_DIR", str(&c.DataDir)},
		{"RDPANEL_DB_PATH", str(&c.DBPath)},
		{"RDPANEL_SECRET_KEY", sec(&c.SecretKey)},
		{"RDPANEL_SECRET_KEY_FILE", str(&c.SecretKeyFile)},
		{"RUSTDESK_API_JWT_KEY", sec(&c.Auth.JWTKey)},
		{"RDPANEL_JWT_KEY_FILE", str(&c.Auth.JWTKeyFile)},
		{"RDPANEL_TOKEN_LIFETIME", dur(&c.Auth.TokenLifetime)},
		{"RDPANEL_JWT_LIFETIME", dur(&c.Auth.JWTLifetime)},
		{"RDPANEL_SESSION_IDLE", dur(&c.Auth.SessionIdle)},
		{"RDPANEL_SESSION_ABSOLUTE", dur(&c.Auth.SessionAbsolute)},
		{"RDPANEL_TRUSTED_PROXIES", list(&c.HTTP.TrustedProxies)},
		{"RDPANEL_REAL_IP_HEADER", str(&c.HTTP.RealIPHeader)},
		{"RDPANEL_METRICS_ALLOW", list(&c.HTTP.MetricsAllow)},
		{"RDPANEL_HBBS_ID_SERVER", str(&c.HBBS.IDServer)},
		{"RDPANEL_HBBS_RELAY_SERVER", str(&c.HBBS.RelayServer)},
		{"RDPANEL_HBBS_PUBLIC_KEY_FILE", str(&c.HBBS.PublicKeyFile)},
		{"RDPANEL_HBBS_ENABLED", boolean(&c.HBBS.Enabled)},
		{"RDPANEL_HBBS_POLL_INTERVAL", dur(&c.HBBS.PollInterval)},
		{"RDPANEL_HBBS_RECONCILE_INTERVAL", dur(&c.HBBS.ReconcileInterval)},
		{"RDPANEL_HBBS_LIST_DIR", str(&c.HBBS.ListDir)},
		{"RDPANEL_LOG_LEVEL", str(&c.Log.Level)},
		{"RDPANEL_LOG_FORMAT", str(&c.Log.Format)},
		{"RDPANEL_UPDATE_CHECK", boolean(&c.UpdateCheck)},
	}
}

// EnvNames lists every environment variable Load consults (for docs and help).
func EnvNames() []string {
	vars := Defaults().envVars()
	names := make([]string, 0, len(vars)+1)
	names = append(names, "RDPANEL_CONFIG")
	for _, v := range vars {
		names = append(names, v.name)
	}
	return names
}

func (c *Config) applyEnv(lookup LookupFunc) error {
	for _, v := range c.envVars() {
		val, ok := lookup(v.name)
		if !ok {
			continue
		}
		if err := v.apply(val); err != nil {
			return fmt.Errorf("config: %s: %w", v.name, err)
		}
	}
	return nil
}

func (c *Config) fillDerived() {
	if c.DBPath == "" {
		c.DBPath = filepath.Join(c.DataDir, "panel.db")
	}
	if c.SecretKeyFile == "" {
		c.SecretKeyFile = filepath.Join(c.DataDir, "secret.key")
	}
	if c.Auth.JWTKeyFile == "" {
		c.Auth.JWTKeyFile = filepath.Join(c.DataDir, "jwt.key")
	}
	if c.HBBS.PublicKeyFile == "" {
		c.HBBS.PublicKeyFile = filepath.Join(c.DataDir, "id_ed25519.pub")
	}
	if c.Auth.JWTLifetime == 0 {
		c.Auth.JWTLifetime = c.Auth.TokenLifetime
	}
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	c.AdminURL = strings.TrimRight(c.AdminURL, "/")
	if c.AdminURL == "" {
		c.AdminURL = c.PublicURL
	}
	c.HTTP.RealIPHeader = http.CanonicalHeaderKey(c.HTTP.RealIPHeader)
	c.Log.Level = strings.ToLower(c.Log.Level)
	c.Log.Format = strings.ToLower(c.Log.Format)
}

// Validate checks the configuration and pre-parses derived values.
func (c *Config) Validate() error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		add("listen %q: %v", c.Listen, err)
	}
	checkURL := func(name, v string) {
		if v == "" {
			return
		}
		u, err := url.Parse(v)
		switch {
		case err != nil:
			add("%s: %v", name, err)
		case u.Scheme != "http" && u.Scheme != "https":
			add("%s %q: scheme must be http or https", name, v)
		case u.Host == "":
			add("%s %q: missing host", name, v)
		case u.RawQuery != "" || u.Fragment != "" || u.User != nil:
			add("%s %q: must not contain userinfo, query or fragment", name, v)
		}
	}
	checkURL("public_url", c.PublicURL)
	checkURL("admin_url", c.AdminURL)
	if c.AdminURL != "" && c.PublicURL == "" {
		add("admin_url requires public_url (the OIDC redirect URI host)")
	}
	if c.DataDir == "" {
		add("data_dir must not be empty")
	}
	if c.SecretKey.IsSet() {
		if n := decodedLen(c.SecretKey.Reveal()); n != 32 {
			add("RDPANEL_SECRET_KEY must be base64 of exactly 32 bytes")
		}
	}
	if c.Auth.TokenLifetime <= 0 {
		add("auth.token_lifetime must be > 0")
	}
	if c.Auth.JWTLifetime < 0 {
		add("auth.jwt_lifetime must be >= 0")
	}
	if c.Auth.SessionIdle <= 0 || c.Auth.SessionAbsolute <= 0 {
		add("auth.session_idle and auth.session_absolute must be > 0")
	} else if c.Auth.SessionIdle > c.Auth.SessionAbsolute {
		add("auth.session_idle must not exceed auth.session_absolute")
	}
	var err error
	if c.HTTP.trustedProxies, err = ParsePrefixes(c.HTTP.TrustedProxies); err != nil {
		add("http.trusted_proxies: %v", err)
	}
	if c.HTTP.metricsAllow, err = ParsePrefixes(c.HTTP.MetricsAllow); err != nil {
		add("http.metrics_allow: %v", err)
	}
	switch c.HTTP.RealIPHeader {
	case "X-Forwarded-For", "X-Real-Ip", "Cf-Connecting-Ip":
	default:
		add("http.real_ip_header %q: must be X-Forwarded-For, X-Real-IP or CF-Connecting-IP", c.HTTP.RealIPHeader)
	}
	if p, err := portOf(c.HBBS.IDServer); err != nil {
		add("hbbs.id_server: %v", err)
	} else if p < 2 {
		add("hbbs.id_server: port must be >= 2 (admin port is port-1)")
	}
	if _, err := portOf(c.HBBS.RelayServer); err != nil {
		add("hbbs.relay_server: %v", err)
	}
	if c.HBBS.PollInterval < time.Second {
		add("hbbs.poll_interval must be >= 1s")
	}
	if c.HBBS.ReconcileInterval < time.Second {
		add("hbbs.reconcile_interval must be >= 1s")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		add("log.level %q: must be debug, info, warn or error", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("log.format %q: must be json or text", c.Log.Format)
	}
	if len(errs) > 0 {
		return fmt.Errorf("config: %w", errors.Join(errs...))
	}
	return nil
}

// TrustedProxyPrefixes returns the parsed trusted proxy networks (valid after Validate).
func (h *HTTPConfig) TrustedProxyPrefixes() []netip.Prefix { return h.trustedProxies }

// MetricsAllowPrefixes returns the parsed /metrics allow-list (valid after Validate).
func (h *HTTPConfig) MetricsAllowPrefixes() []netip.Prefix { return h.metricsAllow }

// AdminAddr returns the hbbs loopback admin address (rendezvous port - 1).
func (h HBBSConfig) AdminAddr() string {
	host, port := mustSplit(h.IDServer)
	return net.JoinHostPort(host, strconv.Itoa(port-1))
}

// RelayAdminAddr returns the hbbr loopback admin address (same as the relay port).
func (h HBBSConfig) RelayAdminAddr() string { return h.RelayServer }

// HTTPS reports whether the public or the admin URL uses https (enables HSTS).
func (c *Config) HTTPS() bool {
	return strings.HasPrefix(c.PublicURL, "https://") || strings.HasPrefix(c.AdminURL, "https://")
}

// AdminHTTPS reports whether the panel (admin) URL uses https: panel cookies
// (session, CSRF, OIDC binding) get the Secure attribute.
func (c *Config) AdminHTTPS() bool { return strings.HasPrefix(c.AdminURL, "https://") }

// Redacted renders the effective configuration as YAML with secrets redacted.
func (c *Config) Redacted() ([]byte, error) {
	return yaml.Marshal(c)
}

// ParsePrefixes parses IPs ("10.0.0.1", "::1") and CIDRs ("10.0.0.0/8").
func ParsePrefixes(items []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(items))
	for _, s := range items {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return nil, err
			}
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, err
		}
		a = a.Unmap()
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func splitList(v string) []string {
	parts := strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
	if parts == nil {
		return []string{}
	}
	return parts
}

func portOf(addr string) (int, error) {
	host, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	if host == "" {
		return 0, fmt.Errorf("%q: missing host", addr)
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%q: invalid port", addr)
	}
	return port, nil
}

func mustSplit(addr string) (string, int) {
	host, p, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(p)
	return host, port
}

func decodedLen(s string) int {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(strings.TrimSpace(s)); err == nil {
			return len(b)
		}
	}
	return -1
}
