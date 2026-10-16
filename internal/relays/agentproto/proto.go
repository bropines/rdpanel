// Package agentproto is the wire protocol between `rdpanel agent` (on a relay
// host, next to hbbr) and the panel's agent hub (/agent/v1/*).
//
// Enrollment: POST /agent/v1/enroll with a one-time token and the agent's
// ed25519 public key. Session: GET /agent/v1/connect upgrades to a WebSocket;
// the panel sends a challenge (32-byte nonce), the agent answers with
// ed25519(SignedMessage(nonce, agent_id, ts)). After "welcome" every frame is
// one JSON Envelope:
//
//	agent -> panel: hello, status, ack, result
//	panel -> agent: config, command
//
// config is the complete desired hbbr state with a monotonically increasing
// version; the agent persists it, applies it and answers with ack.
package agentproto

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
)

// Paths, relative to the panel's public URL.
const (
	EnrollPath  = "/agent/v1/enroll"
	ConnectPath = "/agent/v1/connect"
)

// Protocol constants.
const (
	// SigContext is mixed into every signature (domain separation).
	SigContext = "rdpanel-agent-v1"
	// MaxMessage caps one WebSocket message (both directions).
	MaxMessage = 1 << 20
	// NonceSize is the challenge size in bytes.
	NonceSize = 32
	// DefaultClockSkew is the accepted |ts - now| of an auth message.
	DefaultClockSkew = 60 * time.Second
)

// Message types.
const (
	TypeChallenge = "challenge"
	TypeAuth      = "auth"
	TypeWelcome   = "welcome"
	TypeHello     = "hello"
	TypeConfig    = "config"
	TypeAck       = "ack"
	TypeStatus    = "status"
	TypeCommand   = "command"
	TypeResult    = "result"
)

// Commands the panel may send (allow-listed on the agent).
const (
	CommandUsage       = "usage"
	CommandRestartHBBR = "restart-hbbr"
)

// WebSocket close reasons sent by the panel (close code 4000 + n).
const (
	CloseAuthFailed = 4001 // bad signature, unknown agent, clock skew
	CloseRevoked    = 4003 // agent revoked or reset; re-enroll
	CloseSuperseded = 4009 // a newer connection of the same agent took over
)

// EnrollRequest is the body of POST /agent/v1/enroll.
type EnrollRequest struct {
	Token string `json:"token"`
	// AgentPubkey is the agent's ed25519 public key (32 bytes, base64).
	AgentPubkey []byte `json:"agent_pubkey"`
	Hostname    string `json:"hostname,omitempty"`
	Version     string `json:"version,omitempty"`
	HBBRVersion string `json:"hbbr_version,omitempty"`
	Arch        string `json:"arch,omitempty"`
}

// EnrollResponse is the 200 answer to an enrollment.
type EnrollResponse struct {
	AgentID   int64  `json:"agent_id"`
	RelayName string `json:"relay_name"`
	// Approved is false while the relay waits for an admin's approval; the
	// agent connects anyway and gets its config once approved.
	Approved bool `json:"approved"`
}

// Error is the JSON body of a non-2xx /agent/v1 answer.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Envelope frames every WebSocket message.
type Envelope struct {
	Type string          `json:"type"`
	ID   string          `json:"id,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// NewEnvelope marshals data into an Envelope.
func NewEnvelope(typ, id string, data any) (Envelope, error) {
	e := Envelope{Type: typ, ID: id}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return e, err
		}
		e.Data = raw
	}
	return e, nil
}

// Decode unmarshals e.Data into dst.
func (e Envelope) Decode(dst any) error {
	if len(e.Data) == 0 {
		return errors.New("agentproto: empty message data")
	}
	return json.Unmarshal(e.Data, dst)
}

// Challenge is the panel's first message.
type Challenge struct {
	Nonce      []byte `json:"nonce"`
	ServerTime int64  `json:"server_time"`
}

// Auth answers the challenge.
type Auth struct {
	AgentID int64  `json:"agent_id"`
	TS      int64  `json:"ts"`
	Sig     []byte `json:"sig"`
}

// SignedMessage is what the agent signs: context, nonce, agent id and unix
// time, newline separated. The nonce is fresh per connection, so a captured
// signature cannot be replayed on another connection.
func SignedMessage(nonce []byte, agentID, ts int64) []byte {
	b := make([]byte, 0, len(SigContext)+len(nonce)+48)
	b = append(b, SigContext...)
	b = append(b, '\n')
	b = append(b, nonce...)
	b = append(b, '\n')
	b = strconv.AppendInt(b, agentID, 10)
	b = append(b, '\n')
	b = strconv.AppendInt(b, ts, 10)
	return b
}

// Sign builds the Auth answer to nonce.
func Sign(key ed25519.PrivateKey, nonce []byte, agentID int64, now time.Time) Auth {
	ts := now.Unix()
	return Auth{AgentID: agentID, TS: ts, Sig: ed25519.Sign(key, SignedMessage(nonce, agentID, ts))}
}

// Errors of Verify.
var (
	ErrBadSignature = errors.New("agentproto: bad signature")
	ErrClockSkew    = errors.New("agentproto: timestamp outside the accepted window (check the agent's clock)")
)

// Verify checks an Auth answer against nonce and the agent's public key.
func Verify(pub ed25519.PublicKey, nonce []byte, a Auth, now time.Time, skew time.Duration) error {
	if len(pub) != ed25519.PublicKeySize || len(a.Sig) != ed25519.SignatureSize {
		return ErrBadSignature
	}
	if d := now.Sub(time.Unix(a.TS, 0)); d > skew || d < -skew {
		return ErrClockSkew
	}
	if !ed25519.Verify(pub, SignedMessage(nonce, a.AgentID, a.TS), a.Sig) {
		return ErrBadSignature
	}
	return nil
}

// Welcome confirms the session.
type Welcome struct {
	RelayName string `json:"relay_name"`
	Approved  bool   `json:"approved"`
}

// Hello is the agent's first message after welcome.
type Hello struct {
	AgentVersion string `json:"agent_version"`
	HBBRVersion  string `json:"hbbr_version,omitempty"`
	Hostname     string `json:"hostname,omitempty"`
	Arch         string `json:"arch,omitempty"`
	// ConfigVersion is the version the agent currently enforces.
	ConfigVersion int64 `json:"config_version"`
}

// RelaySettings is the hbbr part of the desired state (same JSON as the
// admin API's server settings; null = unmanaged).
type RelaySettings struct {
	Blacklist               []string `json:"blacklist"`
	Blocklist               []string `json:"blocklist"`
	LimitSpeedMbps          *float64 `json:"limit_speed_mbps"`
	TotalBandwidthMbps      *float64 `json:"total_bandwidth_mbps"`
	SingleBandwidthMbps     *float64 `json:"single_bandwidth_mbps"`
	DowngradeThreshold      *float64 `json:"downgrade_threshold"`
	DowngradeStartCheckSecs *int64   `json:"downgrade_start_check_secs"`
}

// FromReconcile copies the hbbr keys of s.
func FromReconcile(s reconcile.Settings) RelaySettings {
	return RelaySettings{Blacklist: s.Blacklist, Blocklist: s.Blocklist, LimitSpeedMbps: s.LimitSpeedMbps,
		TotalBandwidthMbps: s.TotalBandwidthMbps, SingleBandwidthMbps: s.SingleBandwidthMbps,
		DowngradeThreshold: s.DowngradeThreshold, DowngradeStartCheckSecs: s.DowngradeStartCheckSecs}
}

// Reconcile converts back (hbbs keys stay nil).
func (r RelaySettings) Reconcile() reconcile.Settings {
	return reconcile.Settings{Blacklist: r.Blacklist, Blocklist: r.Blocklist, LimitSpeedMbps: r.LimitSpeedMbps,
		TotalBandwidthMbps: r.TotalBandwidthMbps, SingleBandwidthMbps: r.SingleBandwidthMbps,
		DowngradeThreshold: r.DowngradeThreshold, DowngradeStartCheckSecs: r.DowngradeStartCheckSecs}
}

// Config is the complete desired state of one relay.
type Config struct {
	Version int64 `json:"version"`
	// Run false tells the agent to stop hbbr (relay disabled or not approved).
	Run bool `json:"run"`
	// HBBSPublicKey is passed to hbbr as -k (clients present it in
	// RequestRelay; hbbr refuses other keys).
	HBBSPublicKey string        `json:"hbbs_public_key"`
	Settings      RelaySettings `json:"settings"`
}

// Ack reports the outcome of applying a config.
type Ack struct {
	Version int64 `json:"version"`
	// Errors maps setting keys (reconcile.Key*) to apply errors.
	Errors map[string]string `json:"errors,omitempty"`
}

// HBBRStatus describes the supervised hbbr.
type HBBRStatus struct {
	Up         bool      `json:"up"`
	PID        int       `json:"pid,omitempty"`
	Restarts   int       `json:"restarts"`
	Since      time.Time `json:"since,omitempty"`
	AdminRTTMs float64   `json:"admin_rtt_ms,omitempty"`
	// KeyFP is the first 8 characters of the key hbbr runs with.
	KeyFP     string `json:"key_fp,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// HostStatus is sampled from /proc (Linux); zero when unavailable.
type HostStatus struct {
	Load1      float64 `json:"load1"`
	MemUsedPct float64 `json:"mem_used_pct"`
	RxBps      float64 `json:"rx_bps"`
	TxBps      float64 `json:"tx_bps"`
}

// UsageSummary aggregates hbbr "usage".
type UsageSummary struct {
	RelaysActive int     `json:"relays_active"`
	TotalMB      float64 `json:"total_mb"`
	CurrentKbps  float64 `json:"current_kbps"`
}

// Status is sent every status interval and after every apply.
type Status struct {
	AgentVersion  string            `json:"agent_version"`
	ConfigVersion int64             `json:"config_version"`
	ApplyErrors   map[string]string `json:"apply_errors,omitempty"`
	HBBR          HBBRStatus        `json:"hbbr"`
	Host          HostStatus        `json:"host"`
	Usage         UsageSummary      `json:"usage"`
}

// Command is a panel request; the agent answers with a Result of the same id.
type Command struct {
	Name string `json:"name"`
}

// Result answers a Command.
type Result struct {
	OK    bool                 `json:"ok"`
	Error string               `json:"error,omitempty"`
	Usage []control.RelayUsage `json:"usage,omitempty"`
}

// KeyFingerprint is the short key id shown in the UI and status.
func KeyFingerprint(key string) string {
	if len(key) > 8 {
		return key[:8]
	}
	return key
}
