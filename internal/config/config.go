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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
	// DBType is the database engine: "sqlite" (default) or "postgres".
	DBType string `yaml:"db_type"`
	// DBPath is the SQLite database file. Default: <DataDir>/panel.db.
	DBPath string `yaml:"db_path"`
	// DatabaseURL is the PostgreSQL connection string, e.g.
	// postgres://user:pass@host:5432/dbname?sslmode=require. It usually
	// carries a password, so it is a Secret (redacted when printed).
	DatabaseURL Secret `yaml:"database_url"`
	// DatabaseURLFile is read when DatabaseURL is empty (Docker/Kubernetes
	// secrets).
	DatabaseURLFile string `yaml:"database_url_file"`
	// SecretKey is a base64-encoded 32-byte AEAD key for secrets at rest. When
	// empty, the key is loaded from (or generated into) SecretKeyFile.
	SecretKey Secret `yaml:"secret_key"`
	// SecretKeyFile is the AEAD key file. Default: <DataDir>/secret.key.
	SecretKeyFile string `yaml:"secret_key_file"`
	// UpdateCheck lets the About page look up the latest RD Panel
	// release on GitHub (server-side, cached for 6 h; never blocks anything).
	UpdateCheck bool `yaml:"update_check"`
	// APIDocs controls the interactive admin API docs at /api/v1/docs and the
	// spec at /api/v1/openapi.{yaml,json}: "auth" (signed-in users, default),
	// "public" (readable without login) or "off" (404).
	APIDocs string `yaml:"api_docs"`

	Auth       AuthConfig       `yaml:"auth"`
	WebAuthn   WebAuthnConfig   `yaml:"webauthn"`
	HTTP       HTTPConfig       `yaml:"http"`
	HBBS       HBBSConfig       `yaml:"hbbs"`
	Log        LogConfig        `yaml:"log"`
	Logs       LogsConfig       `yaml:"logs"`
	SMTP       SMTPConfig       `yaml:"smtp"`
	Webhook    WebhookConfig    `yaml:"webhook"`
	Federation FederationConfig `yaml:"federation"`
	// WebClient configures the browser remote desktop client (/connect).
	WebClient WebClientConfig `yaml:"webclient"`
	// ClientDownloads configures the "Install client" page.
	ClientDownloads ClientDownloadsConfig `yaml:"client_downloads"`
	// ShareLinks configures public server share links (/s/<token>).
	ShareLinks ShareLinksConfig `yaml:"share_links"`
}

// ShareLinksConfig controls public server share links: pages at
// <public_url>/s/<token> where anyone with the link gets the client set up
// for this server and its connection settings. Off by default: /s/* answers
// 404 and the panel hides the feature.
type ShareLinksConfig struct {
	// Enabled serves /s/* and lets admins (and user groups an admin allows)
	// create links.
	Enabled bool `yaml:"enabled"`
}

// ClientDownloadsConfig configures the "Install client" page: the official
// RustDesk clients, set up for this server.
type ClientDownloadsConfig struct {
	// Enabled looks up RustDesk releases on GitHub (cached for 6 h) and serves
	// the Windows installer under a file name that carries this server's
	// settings. false: the page offers only the settings (config string, QR
	// code) and no outbound request is made. Default: true.
	Enabled bool `yaml:"enabled"`
	// Version pins a RustDesk release ("1.4.2"); empty: the latest one.
	Version string `yaml:"version"`
}

// WebClientConfig configures the browser remote desktop client.
type WebClientConfig struct {
	// Enabled offers the web client to signed-in users. Default: true.
	Enabled bool `yaml:"enabled"`
	// AdminOnly limits the web client to panel admins.
	AdminOnly bool `yaml:"admin_only"`
	// IDURL / RelayURL are the WebSocket URLs of hbbs (21118) and hbbr
	// (21119) as seen by browsers. Empty: same origin as the panel,
	// /ws/id and /ws/relay.
	IDURL    string `yaml:"id_url"`
	RelayURL string `yaml:"relay_url"`
	// Servers are other RustDesk-compatible servers offered in the web
	// client next to this one (their WebSocket origins are allowed by CSP).
	Servers []WebClientServer `yaml:"servers"`
	// CustomServers lets users enter any server (CSP then allows every wss:
	// origin for WebSocket connections).
	CustomServers bool `yaml:"custom_servers"`
}

// WebClientServer is an additional server offered in the web client.
type WebClientServer struct {
	Name string `yaml:"name" json:"name"`
	// IDURL is hbbs' WebSocket URL as browsers reach it (wss://host/ws/id).
	IDURL string `yaml:"id_url" json:"id_url"`
	// RelayURL is hbbr's WebSocket URL; empty: IDURL with /ws/relay.
	RelayURL string `yaml:"relay_url" json:"relay_url"`
	// Key is the server's public key (id_ed25519.pub, base64).
	Key string `yaml:"key" json:"key"`
}

// MinFederationTokenLen is the shortest accepted federation token.
const MinFederationTokenLen = 16

// FederationConfig controls panel federation (Server -> Federation). Off by
// default: when off, /api/v1/federation/* is not served and no peer is synced.
type FederationConfig struct {
	// Enabled serves /api/v1/federation/{health,relays} to peer panels and
	// runs the peer sync loop.
	Enabled bool `yaml:"enabled"`
	// Token is the bearer token peer panels must present to this panel's
	// federation endpoints (at least MinFederationTokenLen characters;
	// required when Enabled). Peers store it as their "auth token" for this
	// panel.
	Token Secret `yaml:"token"`
}

// WebhookConfig specifies an optional webhook endpoint for critical alerts.
type WebhookConfig struct {
	URL    string `yaml:"url"`
	Secret Secret `yaml:"secret"`
}

// Enabled reports whether webhooks are configured.
func (w WebhookConfig) Enabled() bool { return w.URL != "" }

// SMTP encryption modes.
const (
	SMTPEncryptionNone     = "none"
	SMTPEncryptionSTARTTLS = "starttls"
	SMTPEncryptionTLS      = "tls"
)

// SMTPConfig is the outgoing mail server (e-mail verification, e-mail codes,
// password reset). E-mail features are off when Host is empty here and in the
// panel settings; a non-empty Host makes the whole block authoritative (the
// panel's Settings -> Email form becomes read-only).
type SMTPConfig struct {
	Host string `yaml:"host"`
	// Port defaults to 587 (starttls), 465 (tls) or 25 (none).
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password Secret `yaml:"password"`
	// PasswordFile is read when Password is empty (Docker/Kubernetes secrets).
	PasswordFile string `yaml:"password_file"`
	// From is the sender address, e.g. "RD Panel <panel@example.com>".
	From string `yaml:"from"`
	// Encryption is none, starttls (default) or tls (implicit TLS, "SMTPS").
	Encryption         string `yaml:"encryption"`
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify"`
}

// Enabled reports whether the configuration defines a mail server.
func (s SMTPConfig) Enabled() bool { return s.Host != "" }

// DefaultSMTPPort returns the conventional port of an encryption mode.
func DefaultSMTPPort(encryption string) int {
	switch encryption {
	case SMTPEncryptionTLS:
		return 465
	case SMTPEncryptionNone:
		return 25
	}
	return 587
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

// WebAuthnConfig overrides the relying party of panel passkeys. Both default
// from AdminURL (which itself defaults to PublicURL): the RP ID is its host
// name and the allowed origin is its origin.
type WebAuthnConfig struct {
	// RPID is the WebAuthn relying party id: a host name equal to, or a
	// registrable parent domain of, every origin's host (e.g. "example.com" to
	// share passkeys between panel.example.com and admin.example.com).
	RPID string `yaml:"rp_id"`
	// Origins are the exact origins (scheme://host[:port]) the panel SPA is
	// served from. Default: the AdminURL origin, plus the PublicURL origin
	// when its host is within the RP ID.
	Origins []string `yaml:"origins"`
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
	// MetricsPerDevice exports rdpanel_presence_device_online{id=...}, one
	// series per device. Off by default: cardinality grows with the fleet.
	MetricsPerDevice bool `yaml:"metrics_per_device"`

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
	// RelayPublicHost is the public host[:port] of the co-located hbbr as
	// clients reach it; the "local" relay on the Relays tab is seeded from it.
	// Default: the first entry of RELAY (the hbbs -r value in the s6 image).
	RelayPublicHost string `yaml:"relay_public_host"`
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
	// WebProxy controls whether RD Panel proxies browser WebSocket connections
	// (/ws/id and /ws/relay) to local hbbs/hbbr WebSocket listeners. Default: true.
	WebProxy bool `yaml:"web_proxy"`
	// WSIDServer is an explicit override for hbbs WebSocket address (default: IDServer port + 2).
	WSIDServer string `yaml:"ws_id_server"`
	// WSRelayServer is an explicit override for hbbr WebSocket address (default: RelayServer port + 2).
	WSRelayServer string `yaml:"ws_relay_server"`
	// Authz serves POST /internal/hbbs/authorize, where hbbs (AUTHZ_URL)
	// asks the panel whether a connection may start and which control
	// permissions it gets (connection rules and control roles). Loopback
	// callers presenting the secret in AuthzSecretFile only. Default: true.
	Authz bool `yaml:"authz"`
	// AuthzSecretFile holds the bearer secret hbbs presents to the
	// authorization endpoint; generated (32 random bytes, hex, mode 0600)
	// when missing. Default: <DataDir>/hbbs-authz.key.
	AuthzSecretFile string `yaml:"authz_secret_file"`
}

// WSIDAddr returns the hbbs WebSocket address (WSIDServer if set, else IDServer port + 2).
func (h HBBSConfig) WSIDAddr() string {
	if h.WSIDServer != "" {
		return h.WSIDServer
	}
	host, portStr, err := net.SplitHostPort(h.IDServer)
	if err != nil {
		return "127.0.0.1:21118"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "127.0.0.1:21118"
	}
	return net.JoinHostPort(host, strconv.Itoa(port+2))
}

// WSRelayAddr returns the hbbr WebSocket address (WSRelayServer if set, else RelayServer port + 2).
func (h HBBSConfig) WSRelayAddr() string {
	if h.WSRelayServer != "" {
		return h.WSRelayServer
	}
	host, portStr, err := net.SplitHostPort(h.RelayServer)
	if err != nil {
		return "127.0.0.1:21119"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "127.0.0.1:21119"
	}
	return net.JoinHostPort(host, strconv.Itoa(port+2))
}

// LogConfig controls slog output.
type LogConfig struct {
	// Level is one of debug, info, warn, error.
	Level string `yaml:"level"`
	// Format is json (default) or text.
	Format string `yaml:"format"`
}

// LogsConfig controls the in-memory log buffer behind Server -> Logs.
type LogsConfig struct {
	// Buffer is the number of recent lines kept per source (hbbs, hbbr,
	// panel); 0 disables the buffer and the Logs page.
	Buffer int `yaml:"buffer"`
	// Dir holds one s6-log directory per source (<dir>/<source>/current),
	// written by the s6 image's log pipelines (/run/rdpanel/logs there). When
	// empty, only the panel's own lines are captured.
	Dir string `yaml:"dir"`
}

// API docs modes (Config.APIDocs).
const (
	APIDocsAuth   = "auth"
	APIDocsPublic = "public"
	APIDocsOff    = "off"
)

// clientVersionRe matches RustDesk release tags (client_downloads.version).
var clientVersionRe = regexp.MustCompile(`^\d{1,3}\.\d{1,3}\.\d{1,3}$`)

// Defaults returns the built-in configuration before files and env are applied.
func Defaults() *Config {
	return &Config{
		Listen:          ":21114",
		DataDir:         "/data",
		UpdateCheck:     true,
		APIDocs:         APIDocsAuth,
		WebClient:       WebClientConfig{Enabled: true},
		ClientDownloads: ClientDownloadsConfig{Enabled: true},
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
			WebProxy:          true,
			Authz:             true,
			PollInterval:      5 * time.Second,
			ReconcileInterval: 15 * time.Second,
		},
		Log:  LogConfig{Level: "info", Format: "json"},
		Logs: LogsConfig{Buffer: 2000},
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
	if cfg.HBBS.RelayPublicHost == "" {
		// The s6 image hands RELAY to hbbs (-r); its first entry is the
		// co-located hbbr.
		if v, ok := lookup("RELAY"); ok {
			if l := splitList(v); len(l) > 0 && !strings.Contains(l[0], "=") {
				cfg.HBBS.RelayPublicHost = l[0]
			}
		}
	}
	if err := cfg.loadSecretFiles(); err != nil {
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
	integer := func(dst *int) func(string) error {
		return func(v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return err
			}
			*dst = n
			return nil
		}
	}
	return []envVar{
		{"RDPANEL_LISTEN", str(&c.Listen)},
		{"RDPANEL_PUBLIC_URL", str(&c.PublicURL)},
		{"RDPANEL_ADMIN_URL", str(&c.AdminURL)},
		{"RDPANEL_DATA_DIR", str(&c.DataDir)},
		{"RDPANEL_DB_TYPE", str(&c.DBType)},
		{"RDPANEL_DB_PATH", str(&c.DBPath)},
		{"RDPANEL_DATABASE_URL", func(v string) error { c.DatabaseURL = Secret(strings.TrimSpace(v)); return nil }},
		{"RDPANEL_DATABASE_URL_FILE", str(&c.DatabaseURLFile)},
		{"RDPANEL_SECRET_KEY", sec(&c.SecretKey)},
		{"RDPANEL_SECRET_KEY_FILE", str(&c.SecretKeyFile)},
		{"RUSTDESK_API_JWT_KEY", sec(&c.Auth.JWTKey)},
		{"RDPANEL_JWT_KEY_FILE", str(&c.Auth.JWTKeyFile)},
		{"RDPANEL_TOKEN_LIFETIME", dur(&c.Auth.TokenLifetime)},
		{"RDPANEL_JWT_LIFETIME", dur(&c.Auth.JWTLifetime)},
		{"RDPANEL_SESSION_IDLE", dur(&c.Auth.SessionIdle)},
		{"RDPANEL_SESSION_ABSOLUTE", dur(&c.Auth.SessionAbsolute)},
		{"RDPANEL_WEBAUTHN_RP_ID", str(&c.WebAuthn.RPID)},
		{"RDPANEL_WEBAUTHN_ORIGINS", list(&c.WebAuthn.Origins)},
		{"RDPANEL_TRUSTED_PROXIES", list(&c.HTTP.TrustedProxies)},
		{"RDPANEL_REAL_IP_HEADER", str(&c.HTTP.RealIPHeader)},
		{"RDPANEL_METRICS_ALLOW", list(&c.HTTP.MetricsAllow)},
		{"RDPANEL_METRICS_PER_DEVICE", boolean(&c.HTTP.MetricsPerDevice)},
		{"RDPANEL_HBBS_ID_SERVER", str(&c.HBBS.IDServer)},
		{"RDPANEL_HBBS_RELAY_SERVER", str(&c.HBBS.RelayServer)},
		{"RDPANEL_HBBS_RELAY_PUBLIC_HOST", str(&c.HBBS.RelayPublicHost)},
		{"RDPANEL_HBBS_PUBLIC_KEY_FILE", str(&c.HBBS.PublicKeyFile)},
		{"RDPANEL_HBBS_ENABLED", boolean(&c.HBBS.Enabled)},
		{"RDPANEL_HBBS_WEB_PROXY", boolean(&c.HBBS.WebProxy)},
		{"RDPANEL_HBBS_WS_ID_SERVER", str(&c.HBBS.WSIDServer)},
		{"RDPANEL_HBBS_WS_RELAY_SERVER", str(&c.HBBS.WSRelayServer)},
		{"RDPANEL_HBBS_POLL_INTERVAL", dur(&c.HBBS.PollInterval)},
		{"RDPANEL_HBBS_RECONCILE_INTERVAL", dur(&c.HBBS.ReconcileInterval)},
		{"RDPANEL_HBBS_LIST_DIR", str(&c.HBBS.ListDir)},
		{"RDPANEL_HBBS_AUTHZ", boolean(&c.HBBS.Authz)},
		{"RDPANEL_HBBS_AUTHZ_SECRET_FILE", str(&c.HBBS.AuthzSecretFile)},
		{"RDPANEL_LOG_LEVEL", str(&c.Log.Level)},
		{"RDPANEL_LOG_FORMAT", str(&c.Log.Format)},
		{"RDPANEL_LOGS_BUFFER", integer(&c.Logs.Buffer)},
		{"RDPANEL_LOGS_DIR", str(&c.Logs.Dir)},
		{"RDPANEL_UPDATE_CHECK", boolean(&c.UpdateCheck)},
		{"RDPANEL_API_DOCS", str(&c.APIDocs)},
		{"RDPANEL_SMTP_HOST", str(&c.SMTP.Host)},
		{"RDPANEL_SMTP_PORT", integer(&c.SMTP.Port)},
		{"RDPANEL_SMTP_USERNAME", str(&c.SMTP.Username)},
		{"RDPANEL_SMTP_PASSWORD", sec(&c.SMTP.Password)},
		{"RDPANEL_SMTP_PASSWORD_FILE", str(&c.SMTP.PasswordFile)},
		{"RDPANEL_SMTP_FROM", str(&c.SMTP.From)},
		{"RDPANEL_SMTP_ENCRYPTION", str(&c.SMTP.Encryption)},
		{"RDPANEL_SMTP_INSECURE_SKIP_VERIFY", boolean(&c.SMTP.InsecureSkipVerify)},
		{"RDPANEL_WEBHOOK_URL", str(&c.Webhook.URL)},
		{"RDPANEL_WEBHOOK_SECRET", sec(&c.Webhook.Secret)},
		{"RDPANEL_WEBCLIENT_ENABLED", boolean(&c.WebClient.Enabled)},
		{"RDPANEL_WEBCLIENT_ADMIN_ONLY", boolean(&c.WebClient.AdminOnly)},
		{"RDPANEL_WEBCLIENT_ID_URL", str(&c.WebClient.IDURL)},
		{"RDPANEL_WEBCLIENT_RELAY_URL", str(&c.WebClient.RelayURL)},
		{"RDPANEL_WEBCLIENT_SERVERS", func(v string) error {
			var servers []WebClientServer
			if strings.TrimSpace(v) == "" {
				c.WebClient.Servers = nil
				return nil
			}
			if err := json.Unmarshal([]byte(v), &servers); err != nil {
				return fmt.Errorf("JSON array of {name,id_url,relay_url,key}: %w", err)
			}
			c.WebClient.Servers = servers
			return nil
		}},
		{"RDPANEL_WEBCLIENT_CUSTOM_SERVERS", boolean(&c.WebClient.CustomServers)},
		{"RDPANEL_CLIENT_DOWNLOADS_ENABLED", boolean(&c.ClientDownloads.Enabled)},
		{"RDPANEL_CLIENT_DOWNLOADS_VERSION", str(&c.ClientDownloads.Version)},
		{"RDPANEL_SHARE_LINKS_ENABLED", boolean(&c.ShareLinks.Enabled)},
		{"RDPANEL_FEDERATION_ENABLED", boolean(&c.Federation.Enabled)},
		{"RDPANEL_FEDERATION_TOKEN", sec(&c.Federation.Token)},
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

// loadSecretFiles reads *_FILE secrets that are not set directly.
func (c *Config) loadSecretFiles() error {
	if !c.DatabaseURL.IsSet() && c.DatabaseURLFile != "" {
		raw, err := os.ReadFile(c.DatabaseURLFile)
		if err != nil {
			return fmt.Errorf("config: database_url_file: %w", err)
		}
		c.DatabaseURL = Secret(strings.TrimSpace(string(raw)))
	}
	if !c.SMTP.Password.IsSet() && c.SMTP.PasswordFile != "" {
		raw, err := os.ReadFile(c.SMTP.PasswordFile)
		if err != nil {
			return fmt.Errorf("config: smtp.password_file: %w", err)
		}
		c.SMTP.Password = Secret(strings.TrimRight(string(raw), "\r\n"))
	}
	return nil
}

func (c *Config) fillDerived() {
	c.DBType = strings.ToLower(strings.TrimSpace(c.DBType))
	if c.DBType == "" {
		if u := c.DatabaseURL.Reveal(); strings.HasPrefix(u, "postgres://") || strings.HasPrefix(u, "postgresql://") {
			c.DBType = "postgres"
		} else {
			c.DBType = "sqlite"
		}
	} else if c.DBType == "postgresql" {
		c.DBType = "postgres"
	}
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
	if c.HBBS.AuthzSecretFile == "" {
		c.HBBS.AuthzSecretFile = filepath.Join(c.DataDir, "hbbs-authz.key")
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
	c.APIDocs = strings.ToLower(strings.TrimSpace(c.APIDocs))
	if c.APIDocs == "" {
		c.APIDocs = APIDocsAuth
	}
	c.SMTP.Host = strings.TrimSpace(c.SMTP.Host)
	c.SMTP.Encryption = strings.ToLower(strings.TrimSpace(c.SMTP.Encryption))
	if c.SMTP.Encryption == "" {
		c.SMTP.Encryption = SMTPEncryptionSTARTTLS
	}
	if c.SMTP.Port == 0 {
		c.SMTP.Port = DefaultSMTPPort(c.SMTP.Encryption)
	}
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
	switch c.DBType {
	case "sqlite":
	case "postgres":
		if !c.DatabaseURL.IsSet() {
			add("database_url (or RDPANEL_DATABASE_URL / RDPANEL_DATABASE_URL_FILE) is required when db_type is postgres")
		}
	default:
		add("db_type %q: must be sqlite or postgres", c.DBType)
	}
	if c.WebAuthn.RPID != "" || len(c.WebAuthn.Origins) > 0 {
		if _, _, err := c.WebAuthnRP(); err != nil {
			add("webauthn: %v", err)
		}
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
	if h := c.HBBS.RelayPublicHost; h != "" && (strings.ContainsAny(h, " ,;/") || len(h) > 255) {
		add("hbbs.relay_public_host %q: must be host[:port]", h)
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
	switch c.APIDocs {
	case APIDocsAuth, APIDocsPublic, APIDocsOff:
	default:
		add("api_docs %q: must be auth, public or off", c.APIDocs)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		add("log.format %q: must be json or text", c.Log.Format)
	}
	if c.Logs.Buffer < 0 || c.Logs.Buffer > 100000 {
		add("logs.buffer %d: must be 0-100000 lines per source (0 disables the Logs page)", c.Logs.Buffer)
	}
	switch c.SMTP.Encryption {
	case SMTPEncryptionNone, SMTPEncryptionSTARTTLS, SMTPEncryptionTLS:
	default:
		add("smtp.encryption %q: must be none, starttls or tls", c.SMTP.Encryption)
	}
	if c.SMTP.Port < 1 || c.SMTP.Port > 65535 {
		add("smtp.port %d: must be 1-65535", c.SMTP.Port)
	}
	if c.SMTP.Enabled() {
		if c.SMTP.From == "" {
			add("smtp.from is required when smtp.host is set")
		} else if _, err := mail.ParseAddress(c.SMTP.From); err != nil {
			add("smtp.from %q: %v", c.SMTP.From, err)
		}
	}
	if c.Webhook.Enabled() {
		u, err := url.ParseRequestURI(c.Webhook.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			add("webhook.url %q: must be an http or https URL", c.Webhook.URL)
		}
	}
	if c.Federation.Enabled {
		if tok := c.Federation.Token.Reveal(); strings.TrimSpace(tok) != tok || len(tok) < MinFederationTokenLen {
			add("federation.token (RDPANEL_FEDERATION_TOKEN) must be at least %d characters without surrounding spaces when federation is enabled",
				MinFederationTokenLen)
		}
	}
	for i, ws := range c.WebClient.Servers {
		if strings.TrimSpace(ws.Name) == "" {
			add("webclient.servers[%d]: name is required", i)
		}
		for _, u := range []struct{ name, v string }{{"id_url", ws.IDURL}, {"relay_url", ws.RelayURL}} {
			if u.v == "" && u.name == "relay_url" {
				continue
			}
			pu, err := url.Parse(u.v)
			if err != nil || pu.Scheme != "wss" || pu.Host == "" {
				add("webclient.servers[%d].%s %q: must be a wss:// URL", i, u.name, u.v)
			}
		}
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(ws.Key)); err != nil || len(raw) != 32 {
			add("webclient.servers[%d].key: must be the base64 server public key (32 bytes)", i)
		}
	}
	if v := c.ClientDownloads.Version; v != "" && !clientVersionRe.MatchString(v) {
		add("client_downloads.version %q: must be a RustDesk release such as 1.4.2", v)
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

// ErrWebAuthnUnconfigured means no panel URL is known, so passkeys are off.
var ErrWebAuthnUnconfigured = errors.New("no admin_url/public_url and no webauthn.origins: passkeys are disabled")

// WebAuthnRP returns the effective passkey relying party id and allowed
// origins (see WebAuthnConfig). Browsers reject IP addresses as RP ids, so a
// panel reached only by IP cannot use passkeys.
func (c *Config) WebAuthnRP() (rpID string, origins []string, err error) {
	seen := map[string]bool{}
	addOrigin := func(raw string) error {
		u, err := url.Parse(strings.TrimSpace(raw))
		switch {
		case err != nil:
			return fmt.Errorf("origin %q: %v", raw, err)
		case u.Scheme != "http" && u.Scheme != "https":
			return fmt.Errorf("origin %q: scheme must be http or https", raw)
		case u.Hostname() == "":
			return fmt.Errorf("origin %q: missing host", raw)
		case u.User != nil || u.RawQuery != "" || u.Fragment != "":
			return fmt.Errorf("origin %q: must not contain userinfo, query or fragment", raw)
		}
		o := strings.ToLower(u.Scheme + "://" + u.Host)
		if !seen[o] {
			seen[o] = true
			origins = append(origins, o)
		}
		return nil
	}
	explicit := len(c.WebAuthn.Origins) > 0
	if explicit {
		for _, o := range c.WebAuthn.Origins {
			if err := addOrigin(o); err != nil {
				return "", nil, err
			}
		}
	} else if c.AdminURL != "" {
		// Only the panel host serves the SPA; the public URL may be a different
		// (client API) host.
		if err := addOrigin(c.AdminURL); err != nil {
			return "", nil, err
		}
	}
	if len(origins) == 0 {
		return "", nil, ErrWebAuthnUnconfigured
	}
	hostOf := func(o string) string { u, _ := url.Parse(o); return u.Hostname() }
	rpID = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(c.WebAuthn.RPID), "."))
	if rpID == "" {
		rpID = hostOf(origins[0])
	}
	if strings.ContainsAny(rpID, ":/ ") {
		return "", nil, fmt.Errorf("rp_id %q must be a bare host name (no scheme, port or path)", rpID)
	}
	if _, err := netip.ParseAddr(strings.Trim(rpID, "[]")); err == nil {
		return "", nil, fmt.Errorf("rp_id %q: an IP address cannot be a WebAuthn relying party id; serve the panel on a host name", rpID)
	}
	within := func(h string) bool { return h == rpID || strings.HasSuffix(h, "."+rpID) }
	for _, o := range origins {
		if h := hostOf(o); !within(h) {
			return "", nil, fmt.Errorf("origin %q is not within rp_id %q", o, rpID)
		}
	}
	if !explicit && c.PublicURL != "" {
		if u, err := url.Parse(c.PublicURL); err == nil && within(u.Hostname()) {
			_ = addOrigin(c.PublicURL)
		}
	}
	return rpID, origins, nil
}

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
