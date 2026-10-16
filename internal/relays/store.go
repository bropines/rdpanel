package relays

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bropines/rdpanel/internal/db"
	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/relays/agentproto"
	"github.com/bropines/rdpanel/internal/store"
)

// Kind of relay.
type Kind string

// Relay kinds.
const (
	KindLocal  Kind = "local"  // the hbbr co-located with hbbs and the panel
	KindStatic Kind = "static" // unmanaged relay, only probed
	KindAgent  Kind = "agent"  // remote hbbr driven by `rdpanel agent`
)

// AdminState is the admin's intent.
type AdminState string

// Admin states.
const (
	StateEnabled  AdminState = "enabled"
	StateDraining AdminState = "draining" // out of rotation, sessions continue
	StateDisabled AdminState = "disabled" // out of rotation, agent stops hbbr
)

// Health is the computed relay state (docs/design/relay-agents.md §6.4).
type Health string

// Health states.
const (
	HealthPending  Health = "pending"
	HealthActive   Health = "active"
	HealthDegraded Health = "degraded"
	HealthOffline  Health = "offline"
)

// MaxWeight bounds Relay.Weight (0 keeps a relay out of rotation).
const MaxWeight = 5

// Relay is one row of relays (plus relay_agents for kind agent).
type Relay struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Kind           Kind       `json:"kind"`
	PublicHost     string     `json:"public_host"`
	Region         string     `json:"region,omitempty"`
	Weight         int        `json:"weight"`
	InRotation     bool       `json:"in_rotation"`
	AdminState     AdminState `json:"admin_state"`
	Health         Health     `json:"health"`
	HealthSince    *time.Time `json:"health_since,omitempty"`
	LastProbeAt    *time.Time `json:"last_probe_at,omitempty"`
	LastProbeMs    *float64   `json:"last_probe_ms,omitempty"`
	LastProbeError string     `json:"last_probe_error,omitempty"`
	CreatedBy      *int64     `json:"created_by,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	Agent          *Agent     `json:"agent,omitempty"`
}

// Agent is the relay_agents row of an agent relay.
type Agent struct {
	TokenPending    bool               `json:"token_pending"`
	EnrollExpiresAt *time.Time         `json:"enroll_expires_at,omitempty"`
	RequireApproval bool               `json:"require_approval"`
	Pubkey          []byte             `json:"-"`
	EnrolledAt      *time.Time         `json:"enrolled_at,omitempty"`
	EnrolledFromIP  string             `json:"enrolled_from_ip,omitempty"`
	ApprovedAt      *time.Time         `json:"approved_at,omitempty"`
	RevokedAt       *time.Time         `json:"revoked_at,omitempty"`
	Hostname        string             `json:"hostname,omitempty"`
	AgentVersion    string             `json:"agent_version,omitempty"`
	HBBRVersion     string             `json:"hbbr_version,omitempty"`
	Arch            string             `json:"arch,omitempty"`
	DesiredVersion  int64              `json:"desired_version"`
	DesiredAt       *time.Time         `json:"desired_at,omitempty"`
	ConfigHash      string             `json:"-"`
	AckedVersion    int64              `json:"acked_version"`
	AckedAt         *time.Time         `json:"acked_at,omitempty"`
	AckErrors       map[string]string  `json:"ack_errors,omitempty"`
	LastSeenAt      *time.Time         `json:"last_seen_at,omitempty"`
	LastStatus      *agentproto.Status `json:"last_status,omitempty"`
}

// Enrolled reports whether an agent key is registered.
func (a *Agent) Enrolled() bool { return a != nil && len(a.Pubkey) == 32 }

// Approved reports whether the agent may receive a running config.
func (a *Agent) Approved() bool {
	return a != nil && a.Enrolled() && a.RevokedAt == nil && (!a.RequireApproval || a.ApprovedAt != nil)
}

// Store persists relays in the panel database.
type Store struct {
	DB  *db.DB
	Now func() time.Time
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func ms(t time.Time) int64 { return t.UTC().UnixMilli() }

func tm(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func mapErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return store.ErrNotFound
	case db.IsUniqueViolation(err):
		return errors.Join(store.ErrConflict, err)
	case db.IsCheckViolation(err):
		return errors.Join(store.ErrInvalid, err)
	}
	return err
}

const relayCols = `r.id, r.name, r.kind, r.public_host, r.region, r.weight, r.in_rotation, r.admin_state, r.health,
	r.health_since, r.last_probe_at, r.last_probe_ms, r.last_probe_error, r.created_by, r.created_at, r.updated_at,
	a.relay_id, a.enroll_token_hash IS NOT NULL, a.enroll_expires_at, a.require_approval, a.agent_pubkey,
	a.enrolled_at, a.enrolled_from_ip, a.approved_at, a.revoked_at, a.hostname, a.agent_version, a.hbbr_version,
	a.arch, a.desired_version, a.desired_at, a.config_hash, a.acked_version, a.acked_at, a.ack_errors,
	a.last_seen_at, a.last_status`

const relayFrom = ` FROM relays r LEFT JOIN relay_agents a ON a.relay_id = r.id`

type scanner interface{ Scan(dest ...any) error }

func scanRelay(sc scanner) (Relay, error) {
	var (
		r                                         Relay
		kind, state, health                       string
		region, probeErr                          sql.NullString
		healthSince, probeAt, createdBy           sql.NullInt64
		probeMs                                   sql.NullFloat64
		created, updated                          int64
		aID, enrollExp, enrolledAt, approvedAt    sql.NullInt64
		revokedAt, desiredAt, ackedAt, lastSeenAt sql.NullInt64
		tokenPending, requireApproval             sql.NullBool
		pubkey                                    []byte
		ip, host, ver, hver, arch, hash           sql.NullString
		ackErrors, lastStatus                     sql.NullString
		desiredV, ackedV                          sql.NullInt64
	)
	if err := sc.Scan(&r.ID, &r.Name, &kind, &r.PublicHost, &region, &r.Weight, &r.InRotation, &state, &health,
		&healthSince, &probeAt, &probeMs, &probeErr, &createdBy, &created, &updated,
		&aID, &tokenPending, &enrollExp, &requireApproval, &pubkey,
		&enrolledAt, &ip, &approvedAt, &revokedAt, &host, &ver, &hver,
		&arch, &desiredV, &desiredAt, &hash, &ackedV, &ackedAt, &ackErrors,
		&lastSeenAt, &lastStatus); err != nil {
		return Relay{}, mapErr(err)
	}
	r.Kind, r.AdminState, r.Health = Kind(kind), AdminState(state), Health(health)
	r.Region, r.LastProbeError = region.String, probeErr.String
	r.HealthSince, r.LastProbeAt = tm(healthSince), tm(probeAt)
	if probeMs.Valid {
		v := probeMs.Float64
		r.LastProbeMs = &v
	}
	if createdBy.Valid {
		v := createdBy.Int64
		r.CreatedBy = &v
	}
	r.CreatedAt, r.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(updated).UTC()
	if aID.Valid {
		a := &Agent{TokenPending: tokenPending.Bool, EnrollExpiresAt: tm(enrollExp), RequireApproval: requireApproval.Bool,
			Pubkey: pubkey, EnrolledAt: tm(enrolledAt), EnrolledFromIP: ip.String, ApprovedAt: tm(approvedAt),
			RevokedAt: tm(revokedAt), Hostname: host.String, AgentVersion: ver.String, HBBRVersion: hver.String,
			Arch: arch.String, DesiredVersion: desiredV.Int64, DesiredAt: tm(desiredAt), ConfigHash: hash.String,
			AckedVersion: ackedV.Int64, AckedAt: tm(ackedAt), LastSeenAt: tm(lastSeenAt)}
		if ackErrors.Valid {
			_ = json.Unmarshal([]byte(ackErrors.String), &a.AckErrors)
		}
		if lastStatus.Valid {
			var st agentproto.Status
			if json.Unmarshal([]byte(lastStatus.String), &st) == nil {
				a.LastStatus = &st
			}
		}
		r.Agent = a
	}
	return r, nil
}

// List returns every relay ordered by id (the rotation order).
func (s *Store) List(ctx context.Context) ([]Relay, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+relayCols+relayFrom+` ORDER BY r.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Relay{}
	for rows.Next() {
		r, err := scanRelay(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns one relay (store.ErrNotFound).
func (s *Store) Get(ctx context.Context, id int64) (Relay, error) {
	return scanRelay(s.DB.QueryRowContext(ctx, `SELECT `+relayCols+relayFrom+` WHERE r.id = ?`, id))
}

// NewRelay is the input of Create.
type NewRelay struct {
	Name       string
	Kind       Kind
	PublicHost string
	Region     string
	Weight     int
	InRotation bool
	CreatedBy  *int64
	// Agent only.
	TokenHash       []byte
	TokenExpires    time.Time
	RequireApproval bool
	// Health of a new relay (default pending).
	Health Health
}

// ValidateHost checks a relay public host the way hbbs will use it
// (host[:port], IPv6 in brackets).
func ValidateHost(h string) error {
	if strings.TrimSpace(h) != h || h == "" {
		return fmt.Errorf("%w: public_host is required (host[:port], no spaces)", store.ErrInvalid)
	}
	if _, err := control.NormalizeRelayServers([]string{h}); err != nil {
		return fmt.Errorf("%w: public_host: %v", store.ErrInvalid, err)
	}
	return nil
}

// ValidateName checks a relay name.
func ValidateName(n string) error {
	if n == "" || len(n) > 64 || strings.TrimSpace(n) != n {
		return fmt.Errorf("%w: name must be 1-64 characters without leading/trailing spaces", store.ErrInvalid)
	}
	return nil
}

// Create inserts a relay (and its relay_agents row for kind agent).
func (s *Store) Create(ctx context.Context, n NewRelay) (int64, error) {
	if err := ValidateName(n.Name); err != nil {
		return 0, err
	}
	if err := ValidateHost(n.PublicHost); err != nil {
		return 0, err
	}
	if n.Weight < 0 || n.Weight > MaxWeight {
		return 0, fmt.Errorf("%w: weight must be 0-%d", store.ErrInvalid, MaxWeight)
	}
	if n.Health == "" {
		n.Health = HealthPending
	}
	now := ms(s.now())
	var id int64
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO relays (name, kind, public_host, region, weight, in_rotation,
			admin_state, health, health_since, created_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, 'enabled', ?, ?, ?, ?, ?)`,
			n.Name, string(n.Kind), n.PublicHost, nullStr(n.Region), n.Weight, n.InRotation, string(n.Health), now,
			n.CreatedBy, now, now)
		if err != nil {
			return mapErr(err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if n.Kind == KindAgent {
			if _, err := tx.ExecContext(ctx, `INSERT INTO relay_agents (relay_id, enroll_token_hash, enroll_expires_at,
				require_approval) VALUES (?, ?, ?, ?)`, id, n.TokenHash, ms(n.TokenExpires), n.RequireApproval); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
	return id, err
}

// Patch holds optional relay changes (nil = unchanged).
type Patch struct {
	Name       *string
	PublicHost *string
	Region     *string
	Weight     *int
	InRotation *bool
	AdminState *AdminState
}

// Update applies p.
func (s *Store) Update(ctx context.Context, id int64, p Patch) error {
	var set []string
	var args []any
	if p.Name != nil {
		if err := ValidateName(*p.Name); err != nil {
			return err
		}
		set, args = append(set, "name = ?"), append(args, *p.Name)
	}
	if p.PublicHost != nil {
		if err := ValidateHost(*p.PublicHost); err != nil {
			return err
		}
		set, args = append(set, "public_host = ?"), append(args, *p.PublicHost)
	}
	if p.Region != nil {
		if len(*p.Region) > 32 {
			return fmt.Errorf("%w: region: at most 32 characters", store.ErrInvalid)
		}
		set, args = append(set, "region = ?"), append(args, nullStr(*p.Region))
	}
	if p.Weight != nil {
		if *p.Weight < 0 || *p.Weight > MaxWeight {
			return fmt.Errorf("%w: weight must be 0-%d", store.ErrInvalid, MaxWeight)
		}
		set, args = append(set, "weight = ?"), append(args, *p.Weight)
	}
	if p.InRotation != nil {
		set, args = append(set, "in_rotation = ?"), append(args, *p.InRotation)
	}
	if p.AdminState != nil {
		switch *p.AdminState {
		case StateEnabled, StateDraining, StateDisabled:
		default:
			return fmt.Errorf("%w: admin_state must be enabled, draining or disabled", store.ErrInvalid)
		}
		set, args = append(set, "admin_state = ?"), append(args, string(*p.AdminState))
	}
	if len(set) == 0 {
		_, err := s.Get(ctx, id)
		return err
	}
	set, args = append(set, "updated_at = ?"), append(args, ms(s.now()), id)
	return expectOne(s.DB.ExecContext(ctx, `UPDATE relays SET `+strings.Join(set, ", ")+` WHERE id = ?`, args...))
}

func expectOne(res sql.Result, err error) error {
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// Delete removes a relay (cascades to agent row and overrides).
func (s *Store) Delete(ctx context.Context, id int64) error {
	return expectOne(s.DB.ExecContext(ctx, `DELETE FROM relays WHERE id = ?`, id))
}

// SetHealth records a probe outcome and the computed health.
func (s *Store) SetHealth(ctx context.Context, id int64, h Health, changed bool, probeAt time.Time, probeMs *float64, probeErr string) error {
	now := ms(s.now())
	_, err := s.DB.ExecContext(ctx, `UPDATE relays SET health = ?,
		health_since = CASE WHEN ? THEN ? ELSE coalesce(health_since, ?) END,
		last_probe_at = ?, last_probe_ms = ?, last_probe_error = ? WHERE id = ?`,
		string(h), changed, now, now, ms(probeAt), probeMs, nullStr(probeErr), id)
	return err
}

// SetHealthOnly changes the computed health without a probe result.
func (s *Store) SetHealthOnly(ctx context.Context, id int64, h Health) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE relays SET health = ?, health_since = ? WHERE id = ? AND health <> ?`,
		string(h), ms(s.now()), id, string(h))
	return err
}

// TokenHash is the stored digest of an enrollment token.
func TokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// ResetEnrollment issues a new token hash for an agent relay and forgets the
// enrolled agent (its key stops working; approval is required again when
// requireApproval is set).
func (s *Store) ResetEnrollment(ctx context.Context, id int64, hash []byte, expires time.Time, requireApproval bool) error {
	return expectOne(s.DB.ExecContext(ctx, `UPDATE relay_agents SET enroll_token_hash = ?, enroll_expires_at = ?,
		require_approval = ?, agent_pubkey = NULL, enrolled_at = NULL, enrolled_from_ip = NULL, approved_at = NULL,
		approved_by = NULL, revoked_at = NULL, acked_version = 0, acked_at = NULL, ack_errors = NULL
		WHERE relay_id = ?`, hash, ms(expires), requireApproval, id))
}

// Errors of Enroll.
var (
	ErrTokenInvalid = errors.New("relays: enrollment token is invalid, expired or already used")
)

// EnrollInput is what an agent presents at enrollment.
type EnrollInput struct {
	TokenHash []byte
	Pubkey    []byte
	IP        string
	Hostname  string
	Version   string
	HBBR      string
	Arch      string
}

// Enroll burns the token and registers the agent key. It returns the relay.
func (s *Store) Enroll(ctx context.Context, in EnrollInput) (Relay, error) {
	now := s.now()
	var id int64
	err := s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		var exp sql.NullInt64
		var requireApproval bool
		err := tx.QueryRowContext(ctx, `SELECT relay_id, enroll_expires_at, require_approval FROM relay_agents
			WHERE enroll_token_hash = ?`, in.TokenHash).Scan(&id, &exp, &requireApproval)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTokenInvalid
		}
		if err != nil {
			return err
		}
		if !exp.Valid || now.UnixMilli() > exp.Int64 {
			return ErrTokenInvalid
		}
		var approved any
		if !requireApproval {
			approved = ms(now)
		}
		_, err = tx.ExecContext(ctx, `UPDATE relay_agents SET enroll_token_hash = NULL, enroll_expires_at = NULL,
			agent_pubkey = ?, enrolled_at = ?, enrolled_from_ip = ?, approved_at = ?, revoked_at = NULL,
			hostname = ?, agent_version = ?, hbbr_version = ?, arch = ?, acked_version = 0 WHERE relay_id = ?`,
			in.Pubkey, ms(now), nullStr(in.IP), approved, nullStr(trunc(in.Hostname, 255)),
			nullStr(trunc(in.Version, 64)), nullStr(trunc(in.HBBR, 64)), nullStr(trunc(in.Arch, 32)), id)
		return mapErr(err)
	})
	if err != nil {
		return Relay{}, err
	}
	return s.Get(ctx, id)
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Approve marks an enrolled agent as approved.
func (s *Store) Approve(ctx context.Context, id int64, by *int64) error {
	return expectOne(s.DB.ExecContext(ctx, `UPDATE relay_agents SET approved_at = ?, approved_by = ?
		WHERE relay_id = ? AND agent_pubkey IS NOT NULL AND revoked_at IS NULL`, ms(s.now()), by, id))
}

// Revoke disables an agent's key (re-enable it by issuing a new token).
func (s *Store) Revoke(ctx context.Context, id int64) error {
	return expectOne(s.DB.ExecContext(ctx, `UPDATE relay_agents SET revoked_at = ?, enroll_token_hash = NULL,
		enroll_expires_at = NULL WHERE relay_id = ?`, ms(s.now()), id))
}

// SetDesired records a new desired config version.
func (s *Store) SetDesired(ctx context.Context, id, version int64, hash string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE relay_agents SET desired_version = ?, config_hash = ?, desired_at = ?
		WHERE relay_id = ?`, version, hash, ms(s.now()), id)
	return err
}

// SetAck records an agent's ACK.
func (s *Store) SetAck(ctx context.Context, id, version int64, errs map[string]string) error {
	var raw any
	if len(errs) > 0 {
		b, _ := json.Marshal(errs)
		raw = string(b)
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE relay_agents SET acked_version = ?, acked_at = ?, ack_errors = ?
		WHERE relay_id = ?`, version, ms(s.now()), raw, id)
	return err
}

// SetHello records the versions an agent reports when it connects.
func (s *Store) SetHello(ctx context.Context, id int64, h agentproto.Hello) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE relay_agents SET agent_version = ?, hbbr_version = coalesce(?, hbbr_version),
		hostname = coalesce(?, hostname), arch = coalesce(?, arch), last_seen_at = ? WHERE relay_id = ?`,
		nullStr(trunc(h.AgentVersion, 64)), nullStr(trunc(h.HBBRVersion, 64)), nullStr(trunc(h.Hostname, 255)),
		nullStr(trunc(h.Arch, 32)), ms(s.now()), id)
	return err
}

// SetStatus stores the latest agent status snapshot.
func (s *Store) SetStatus(ctx context.Context, id int64, st agentproto.Status) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE relay_agents SET last_status = ?, last_seen_at = ? WHERE relay_id = ?`,
		string(b), ms(s.now()), id)
	return err
}

// Overrides returns the per-relay hbbr overrides (unset keys nil).
func (s *Store) Overrides(ctx context.Context, id int64) (reconcile.Settings, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT key, value FROM relay_settings WHERE relay_id = ?`, id)
	if err != nil {
		return reconcile.Settings{}, err
	}
	defer rows.Close()
	var out reconcile.Settings
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return out, err
		}
		if err := setKey(&out, k, json.RawMessage(v)); err != nil {
			return out, fmt.Errorf("relays: override %s: %w", k, err)
		}
	}
	return out, rows.Err()
}

// SaveOverrides stores ov as the complete override set of a relay.
func (s *Store) SaveOverrides(ctx context.Context, id int64, ov reconcile.Settings, by *int64) error {
	now := ms(s.now())
	return s.DB.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM relay_settings WHERE relay_id = ?`, id); err != nil {
			return err
		}
		for _, k := range reconcile.RelayKeys {
			v := getKey(ov, k)
			if v == nil {
				continue
			}
			raw, err := json.Marshal(v)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO relay_settings (relay_id, key, value, updated_by, updated_at)
				VALUES (?, ?, ?, ?, ?)`, id, k, string(raw), by, now); err != nil {
				return mapErr(err)
			}
		}
		return nil
	})
}

func getKey(s reconcile.Settings, k string) any {
	switch k {
	case reconcile.KeyBlacklist:
		if s.Blacklist != nil {
			return s.Blacklist
		}
	case reconcile.KeyBlocklist:
		if s.Blocklist != nil {
			return s.Blocklist
		}
	case reconcile.KeyLimitSpeed:
		if s.LimitSpeedMbps != nil {
			return *s.LimitSpeedMbps
		}
	case reconcile.KeyTotalBandwidth:
		if s.TotalBandwidthMbps != nil {
			return *s.TotalBandwidthMbps
		}
	case reconcile.KeySingleBandwidth:
		if s.SingleBandwidthMbps != nil {
			return *s.SingleBandwidthMbps
		}
	case reconcile.KeyDowngradeThreshold:
		if s.DowngradeThreshold != nil {
			return *s.DowngradeThreshold
		}
	case reconcile.KeyDowngradeStartCheck:
		if s.DowngradeStartCheckSecs != nil {
			return *s.DowngradeStartCheckSecs
		}
	}
	return nil
}

func setKey(s *reconcile.Settings, k string, raw json.RawMessage) error {
	switch k {
	case reconcile.KeyBlacklist:
		return json.Unmarshal(raw, &s.Blacklist)
	case reconcile.KeyBlocklist:
		return json.Unmarshal(raw, &s.Blocklist)
	case reconcile.KeyLimitSpeed:
		return json.Unmarshal(raw, &s.LimitSpeedMbps)
	case reconcile.KeyTotalBandwidth:
		return json.Unmarshal(raw, &s.TotalBandwidthMbps)
	case reconcile.KeySingleBandwidth:
		return json.Unmarshal(raw, &s.SingleBandwidthMbps)
	case reconcile.KeyDowngradeThreshold:
		return json.Unmarshal(raw, &s.DowngradeThreshold)
	case reconcile.KeyDowngradeStartCheck:
		return json.Unmarshal(raw, &s.DowngradeStartCheckSecs)
	}
	return nil // unknown key (newer panel): ignore
}

// Merge overlays the non-nil keys of ov onto the global hbbr profile.
func Merge(global, ov reconcile.Settings) reconcile.Settings {
	out := reconcile.HBBROnly(global)
	if ov.Blacklist != nil {
		out.Blacklist = ov.Blacklist
	}
	if ov.Blocklist != nil {
		out.Blocklist = ov.Blocklist
	}
	if ov.LimitSpeedMbps != nil {
		out.LimitSpeedMbps = ov.LimitSpeedMbps
	}
	if ov.TotalBandwidthMbps != nil {
		out.TotalBandwidthMbps = ov.TotalBandwidthMbps
	}
	if ov.SingleBandwidthMbps != nil {
		out.SingleBandwidthMbps = ov.SingleBandwidthMbps
	}
	if ov.DowngradeThreshold != nil {
		out.DowngradeThreshold = ov.DowngradeThreshold
	}
	if ov.DowngradeStartCheckSecs != nil {
		out.DowngradeStartCheckSecs = ov.DowngradeStartCheckSecs
	}
	return out
}

// HasOverrides reports whether ov sets any key.
func HasOverrides(ov reconcile.Settings) bool {
	for _, k := range reconcile.RelayKeys {
		if getKey(ov, k) != nil {
			return true
		}
	}
	return false
}
