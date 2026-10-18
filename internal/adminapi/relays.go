package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/relays"
	"github.com/bropines/rdpanel/internal/store"
)

// Relays tab (/relays/*, admin only): local, static and agent relays, the
// computed hbbs relay list, agent enrollment and per-relay overrides.

// RelayManager is implemented by *relays.Service.
type RelayManager interface {
	List(ctx context.Context) ([]relays.View, relays.Rotation, error)
	Get(ctx context.Context, id int64) (relays.View, error)
	Observed(ctx context.Context) ([]string, error)
	Create(ctx context.Context, in relays.CreateInput) (relays.View, *relays.Enrollment, error)
	Update(ctx context.Context, id int64, p relays.Patch) (relays.View, error)
	Delete(ctx context.Context, id int64) error
	ResetEnrollment(ctx context.Context, id int64) (*relays.Enrollment, error)
	Approve(ctx context.Context, id int64, by *int64) (relays.View, error)
	Revoke(ctx context.Context, id int64) (relays.View, error)
	ProbeNow(ctx context.Context, id int64) (relays.View, error)
	Settings(ctx context.Context, id int64) (relays.SettingsView, error)
	SaveSettings(ctx context.Context, id int64, ov reconcile.Settings, by *int64) (relays.SettingsView, error)
	Usage(ctx context.Context, id int64) ([]control.RelayUsage, error)
	Traffic(ctx context.Context, id int64, since time.Time) ([]relays.TrafficSample, error)
	GetOptions(ctx context.Context) (relays.Options, error)
	SetOptions(ctx context.Context, o relays.Options, by *int64) error
	HBBSKeyFingerprint() string
	ListPrefixRules(ctx context.Context) ([]relays.PrefixRule, error)
	CreatePrefixRule(ctx context.Context, rule *relays.PrefixRule) error
	DeletePrefixRule(ctx context.Context, id int64) error
}

func (c *b2bCore) registerRelays(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Get("/relays", c.handleListRelays)
		r.Post("/relays", c.handleCreateRelay)
		r.Get("/relays/rotation", c.handleRelayRotation)
		r.Get("/relays/options", c.handleGetRelayOptions)
		r.Put("/relays/options", c.handlePutRelayOptions)
		r.Get("/relays/prefix-rules", c.handleListPrefixRules)
		r.Post("/relays/prefix-rules", c.handleCreatePrefixRule)
		r.Delete("/relays/prefix-rules/{id}", c.handleDeletePrefixRule)
		r.Get("/relays/{id}", c.handleGetRelay)
		r.Patch("/relays/{id}", c.handlePatchRelay)
		r.Delete("/relays/{id}", c.handleDeleteRelay)
		r.Post("/relays/{id}/enroll-token", c.handleRelayEnrollToken)
		r.Post("/relays/{id}/approve", c.handleApproveRelay)
		r.Post("/relays/{id}/revoke", c.handleRevokeRelay)
		r.Post("/relays/{id}/probe", c.handleProbeRelay)
		r.Get("/relays/{id}/settings", c.handleGetRelaySettings)
		r.Put("/relays/{id}/settings", c.handlePutRelaySettings)
		r.Get("/relays/{id}/usage", c.handleRelayUsageByID)
		r.Get("/relays/{id}/traffic", c.handleRelayTraffic)
	})
}

func (c *b2bCore) needRelays(w http.ResponseWriter) bool {
	if c.d.Relays == nil {
		unavailable(w, "relay management is not configured")
		return false
	}
	return true
}

// relayErr maps relays/store errors.
func (c *b2bCore) relayErr(w http.ResponseWriter, r *http.Request, what string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "not_found", what+" not found")
	case errors.Is(err, store.ErrInvalid):
		msg := err.Error()
		if i := strings.Index(msg, store.ErrInvalid.Error()+": "); i >= 0 {
			msg = msg[i+len(store.ErrInvalid.Error())+2:]
		}
		if j := strings.IndexByte(msg, '\n'); j >= 0 {
			msg = msg[:j]
		}
		badRequest(w, msg)
	case errors.Is(err, store.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "conflict", "a relay with this name already exists")
	case errors.Is(err, relays.ErrNoUsage):
		unavailable(w, err.Error())
	default:
		c.fail(w, r, what, err)
	}
}

// ---- DTOs (components.schemas.Relay*) ----

type relayAgentStatusDTO struct {
	ReportedAt   *time.Time `json:"reported_at"`
	HBBRUp       bool       `json:"hbbr_up"`
	HBBRRestarts int        `json:"hbbr_restarts"`
	HBBRError    *string    `json:"hbbr_error"`
	KeyFP        *string    `json:"key_fp"`
	KeyMatches   *bool      `json:"key_matches"`
	AdminRTTMs   float64    `json:"admin_rtt_ms"`
	ConfigVer    int64      `json:"config_version"`
	ApplyErrors  any        `json:"apply_errors"`
	RelaysActive int        `json:"relays_active"`
	TotalMB      float64    `json:"total_mb"`
	CurrentKbps  float64    `json:"current_kbps"`
	RxBps        float64    `json:"rx_bps"`
	TxBps        float64    `json:"tx_bps"`
	Load1        float64    `json:"load1"`
	MemUsedPct   float64    `json:"mem_used_pct"`
}

type relayAgentDTO struct {
	Enrolled        bool                 `json:"enrolled"`
	TokenPending    bool                 `json:"token_pending"`
	EnrollExpiresAt *time.Time           `json:"enroll_expires_at"`
	RequireApproval bool                 `json:"require_approval"`
	Approved        bool                 `json:"approved"`
	ApprovedAt      *time.Time           `json:"approved_at"`
	RevokedAt       *time.Time           `json:"revoked_at"`
	EnrolledAt      *time.Time           `json:"enrolled_at"`
	EnrolledFromIP  *string              `json:"enrolled_from_ip"`
	Hostname        *string              `json:"hostname"`
	AgentVersion    *string              `json:"agent_version"`
	HBBRVersion     *string              `json:"hbbr_version"`
	Arch            *string              `json:"arch"`
	Connected       bool                 `json:"connected"`
	ConnectedAt     *time.Time           `json:"connected_at"`
	RemoteIP        *string              `json:"remote_ip"`
	LastSeenAt      *time.Time           `json:"last_seen_at"`
	DesiredVersion  int64                `json:"desired_version"`
	AckedVersion    int64                `json:"acked_version"`
	AckedAt         *time.Time           `json:"acked_at"`
	AckErrors       map[string]string    `json:"ack_errors"`
	Status          *relayAgentStatusDTO `json:"status"`
}

type relayDTO struct {
	ID             int64          `json:"id"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	PublicHost     string         `json:"public_host"`
	MgmtHost       *string        `json:"mgmt_host"`
	Region         *string        `json:"region"`
	Weight         int            `json:"weight"`
	InRotation     bool           `json:"in_rotation"`
	AdminState     string         `json:"admin_state"`
	Health         string         `json:"health"`
	HealthSince    *time.Time     `json:"health_since"`
	LastProbeAt    *time.Time     `json:"last_probe_at"`
	LastProbeMs    *float64       `json:"last_probe_ms"`
	LastProbeError *string        `json:"last_probe_error"`
	RotationWeight int            `json:"rotation_weight"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	Agent          *relayAgentDTO `json:"agent"`
}

func (c *b2bCore) relayDTO(v relays.View) relayDTO {
	d := relayDTO{ID: v.ID, Name: v.Name, Kind: string(v.Kind), PublicHost: v.PublicHost, MgmtHost: strPtr(v.MgmtHost), Region: strPtr(v.Region),
		Weight: v.Weight, InRotation: v.InRotation, AdminState: string(v.AdminState), Health: string(v.Health),
		HealthSince: v.HealthSince, LastProbeAt: v.LastProbeAt, LastProbeMs: v.LastProbeMs,
		LastProbeError: strPtr(v.LastProbeError), RotationWeight: v.RotationWeight, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt}
	if a := v.Agent; a != nil {
		ad := &relayAgentDTO{Enrolled: a.Enrolled(), TokenPending: a.TokenPending, EnrollExpiresAt: a.EnrollExpiresAt,
			RequireApproval: a.RequireApproval, Approved: a.Approved(), ApprovedAt: a.ApprovedAt, RevokedAt: a.RevokedAt,
			EnrolledAt: a.EnrolledAt, EnrolledFromIP: strPtr(a.EnrolledFromIP), Hostname: strPtr(a.Hostname),
			AgentVersion: strPtr(a.AgentVersion), HBBRVersion: strPtr(a.HBBRVersion), Arch: strPtr(a.Arch),
			Connected: v.Connected, ConnectedAt: v.ConnectedAt, RemoteIP: strPtr(v.RemoteIP), LastSeenAt: a.LastSeenAt,
			DesiredVersion: a.DesiredVersion, AckedVersion: a.AckedVersion, AckedAt: a.AckedAt, AckErrors: a.AckErrors}
		if ad.AckErrors == nil {
			ad.AckErrors = map[string]string{}
		}
		st := v.Live
		reported := a.LastSeenAt
		if st == nil {
			st = a.LastStatus
		}
		if st != nil {
			sd := &relayAgentStatusDTO{ReportedAt: reported, HBBRUp: st.HBBR.Up, HBBRRestarts: st.HBBR.Restarts,
				HBBRError: strPtr(st.HBBR.LastError), KeyFP: strPtr(st.HBBR.KeyFP), AdminRTTMs: st.HBBR.AdminRTTMs,
				ConfigVer: st.ConfigVersion, ApplyErrors: st.ApplyErrors, RelaysActive: st.Usage.RelaysActive,
				TotalMB: st.Usage.TotalMB, CurrentKbps: st.Usage.CurrentKbps, RxBps: st.Host.RxBps, TxBps: st.Host.TxBps,
				Load1: st.Host.Load1, MemUsedPct: st.Host.MemUsedPct}
			if sd.ApplyErrors == nil {
				sd.ApplyErrors = map[string]string{}
			}
			if fp := c.d.Relays.HBBSKeyFingerprint(); fp != "" && st.HBBR.KeyFP != "" {
				m := fp == st.HBBR.KeyFP
				sd.KeyMatches = &m
			}
			ad.Status = sd
		}
		d.Agent = ad
	}
	return d
}

type relayRotationDTO struct {
	Servers       []string        `json:"servers"`
	Members       []relays.Member `json:"members"`
	Fallback      bool            `json:"fallback"`
	Shrunk        bool            `json:"shrunk"`
	Error         *string         `json:"error"`
	Observed      []string        `json:"observed"`
	ObservedError *string         `json:"observed_error"`
}

func (c *b2bCore) rotationDTO(ctx context.Context, rot relays.Rotation) relayRotationDTO {
	d := relayRotationDTO{Servers: rot.Servers, Members: rot.Members, Fallback: rot.Fallback, Shrunk: rot.Shrunk,
		Error: strPtr(rot.Error)}
	if d.Servers == nil {
		d.Servers = []string{}
	}
	if d.Members == nil {
		d.Members = []relays.Member{}
	}
	obs, err := c.d.Relays.Observed(ctx)
	if err != nil {
		d.ObservedError = strPtr(err.Error())
	} else {
		d.Observed = obs
		if d.Observed == nil {
			d.Observed = []string{}
		}
	}
	return d
}

// ---- handlers ----

func (c *b2bCore) handleListRelays(w http.ResponseWriter, r *http.Request) {
	if !c.needRelays(w) {
		return
	}
	views, rot, err := c.d.Relays.List(r.Context())
	if err != nil {
		c.relayErr(w, r, "list relays", err)
		return
	}
	items := make([]relayDTO, 0, len(views))
	for _, v := range views {
		items = append(items, c.relayDTO(v))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "rotation": c.rotationDTO(r.Context(), rot)})
}

func (c *b2bCore) handleRelayRotation(w http.ResponseWriter, r *http.Request) {
	if !c.needRelays(w) {
		return
	}
	_, rot, err := c.d.Relays.List(r.Context())
	if err != nil {
		c.relayErr(w, r, "relay rotation", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c.rotationDTO(r.Context(), rot))
}

type relayCreateDTO struct {
	Kind       string  `json:"kind"`
	Name       string  `json:"name"`
	PublicHost string  `json:"public_host"`
	MgmtHost   *string `json:"mgmt_host"`
	Region     *string `json:"region"`
	Weight     *int    `json:"weight"`
	InRotation *bool   `json:"in_rotation"`
}

func (c *b2bCore) handleCreateRelay(w http.ResponseWriter, r *http.Request) {
	if !c.needRelays(w) {
		return
	}
	var in relayCreateDTO
	if !decodeBody(w, r, &in, 16<<10) {
		return
	}
	ci := relays.CreateInput{Kind: relays.Kind(in.Kind), Name: strings.TrimSpace(in.Name),
		PublicHost: strings.TrimSpace(in.PublicHost), Weight: 1, InRotation: true}
	if in.MgmtHost != nil {
		ci.MgmtHost = strings.TrimSpace(*in.MgmtHost)
	}
	if in.Region != nil {
		ci.Region = strings.TrimSpace(*in.Region)
		if len(ci.Region) > 32 {
			badRequest(w, "region: at most 32 characters")
			return
		}
	}
	if in.Weight != nil {
		ci.Weight = *in.Weight
	}
	if in.InRotation != nil {
		ci.InRotation = *in.InRotation
	}
	if u := CurrentUser(r.Context()); u != nil {
		id := u.ID
		ci.CreatedBy = &id
	}
	v, enr, err := c.d.Relays.Create(r.Context(), ci)
	if err != nil {
		c.relayErr(w, r, "create relay", err)
		return
	}
	c.audit(r, "relay.create", "relay", v.ID, map[string]any{"name": v.Name, "kind": string(v.Kind), "public_host": v.PublicHost, "mgmt_host": v.MgmtHost})
	out := map[string]any{"relay": c.relayDTO(v), "enrollment": enr}
	httpx.WriteJSON(w, http.StatusCreated, out)
}

func (c *b2bCore) handleGetRelay(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	v, err := c.d.Relays.Get(r.Context(), id)
	if err != nil {
		c.relayErr(w, r, "get relay", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, c.relayDTO(v))
}

func (c *b2bCore) handlePatchRelay(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	var raw map[string]json.RawMessage
	if !decodeBody(w, r, &raw, 16<<10) {
		return
	}
	var p relays.Patch
	var keys []string
	for k, v := range raw {
		var err error
		switch k {
		case "name":
			err = json.Unmarshal(v, &p.Name)
		case "public_host":
			err = json.Unmarshal(v, &p.PublicHost)
		case "mgmt_host":
			empty := ""
			p.MgmtHost = &empty
			if string(v) != "null" {
				err = json.Unmarshal(v, p.MgmtHost)
			}
		case "region":
			empty := ""
			p.Region = &empty
			if string(v) != "null" {
				err = json.Unmarshal(v, p.Region)
			}
		case "weight":
			err = json.Unmarshal(v, &p.Weight)
		case "in_rotation":
			err = json.Unmarshal(v, &p.InRotation)
		case "admin_state":
			err = json.Unmarshal(v, &p.AdminState)
		default:
			badRequest(w, "unknown field "+k)
			return
		}
		if err != nil || (string(v) == "null" && k != "region" && k != "mgmt_host") {
			badRequest(w, k+": invalid value")
			return
		}
		keys = append(keys, k)
	}
	for _, s := range []*string{p.Name, p.PublicHost, p.MgmtHost, p.Region} {
		if s != nil {
			*s = strings.TrimSpace(*s)
		}
	}
	v, err := c.d.Relays.Update(r.Context(), id, p)
	if err != nil {
		c.relayErr(w, r, "update relay", err)
		return
	}
	details := map[string]any{"name": v.Name, "fields": keys}
	if p.AdminState != nil {
		details["admin_state"] = string(*p.AdminState)
	}
	c.audit(r, "relay.update", "relay", id, details)
	httpx.WriteJSON(w, http.StatusOK, c.relayDTO(v))
}

func (c *b2bCore) handleDeleteRelay(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	v, err := c.d.Relays.Get(r.Context(), id)
	if err != nil {
		c.relayErr(w, r, "delete relay", err)
		return
	}
	if err := c.d.Relays.Delete(r.Context(), id); err != nil {
		c.relayErr(w, r, "delete relay", err)
		return
	}
	c.audit(r, "relay.delete", "relay", id, map[string]any{"name": v.Name, "kind": string(v.Kind)})
	w.WriteHeader(http.StatusNoContent)
}

func (c *b2bCore) handleRelayEnrollToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	enr, err := c.d.Relays.ResetEnrollment(r.Context(), id)
	if err != nil {
		c.relayErr(w, r, "relay enroll token", err)
		return
	}
	c.audit(r, "relay.enroll_token", "relay", id, map[string]any{"expires_at": enr.ExpiresAt})
	httpx.WriteJSON(w, http.StatusOK, enr)
}

func (c *b2bCore) relayAction(w http.ResponseWriter, r *http.Request, action string,
	fn func(ctx context.Context, id int64) (relays.View, error)) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	v, err := fn(r.Context(), id)
	if err != nil {
		c.relayErr(w, r, action, err)
		return
	}
	if action != "" {
		c.audit(r, action, "relay", id, map[string]any{"name": v.Name})
	}
	httpx.WriteJSON(w, http.StatusOK, c.relayDTO(v))
}

func (c *b2bCore) handleApproveRelay(w http.ResponseWriter, r *http.Request) {
	var by *int64
	if u := CurrentUser(r.Context()); u != nil {
		id := u.ID
		by = &id
	}
	c.relayAction(w, r, "relay.approve", func(ctx context.Context, id int64) (relays.View, error) {
		return c.d.Relays.Approve(ctx, id, by)
	})
}

func (c *b2bCore) handleRevokeRelay(w http.ResponseWriter, r *http.Request) {
	c.relayAction(w, r, "relay.revoke", c.d.Relays.Revoke)
}

func (c *b2bCore) handleProbeRelay(w http.ResponseWriter, r *http.Request) {
	c.relayAction(w, r, "", c.d.Relays.ProbeNow)
}

// relaySettingsValuesDTO is components.schemas.RelaySettingsValues.
type relaySettingsValuesDTO struct {
	Blacklist            []string `json:"blacklist"`
	Blocklist            []string `json:"blocklist"`
	LimitSpeedMbps       *float64 `json:"limit_speed_mbps"`
	TotalBandwidthMbps   *float64 `json:"total_bandwidth_mbps"`
	SingleBandwidthMbps  *float64 `json:"single_bandwidth_mbps"`
	DowngradeThreshold   *float64 `json:"downgrade_threshold"`
	DowngradeStartCheckS *int64   `json:"downgrade_start_check_s"`
}

func relaySettingsDTO(s reconcile.Settings) relaySettingsValuesDTO {
	return relaySettingsValuesDTO{Blacklist: s.Blacklist, Blocklist: s.Blocklist, LimitSpeedMbps: s.LimitSpeedMbps,
		TotalBandwidthMbps: s.TotalBandwidthMbps, SingleBandwidthMbps: s.SingleBandwidthMbps,
		DowngradeThreshold: s.DowngradeThreshold, DowngradeStartCheckS: s.DowngradeStartCheckSecs}
}

func (d relaySettingsValuesDTO) settings() reconcile.Settings {
	return reconcile.Settings{Blacklist: d.Blacklist, Blocklist: d.Blocklist, LimitSpeedMbps: d.LimitSpeedMbps,
		TotalBandwidthMbps: d.TotalBandwidthMbps, SingleBandwidthMbps: d.SingleBandwidthMbps,
		DowngradeThreshold: d.DowngradeThreshold, DowngradeStartCheckSecs: d.DowngradeStartCheckS}
}

func writeRelaySettings(w http.ResponseWriter, v relays.SettingsView) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"overrides": relaySettingsDTO(v.Overrides),
		"global": relaySettingsDTO(v.Global), "effective": relaySettingsDTO(v.Effective)})
}

func (c *b2bCore) handleGetRelaySettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	v, err := c.d.Relays.Settings(r.Context(), id)
	if err != nil {
		c.relayErr(w, r, "relay settings", err)
		return
	}
	writeRelaySettings(w, v)
}

func (c *b2bCore) handlePutRelaySettings(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	var in relaySettingsValuesDTO
	if !decodeBody(w, r, &in, 1<<20) {
		return
	}
	if len(in.Blacklist) > reconcile.MaxListEntries || len(in.Blocklist) > reconcile.MaxListEntries {
		badRequest(w, "lists: too many entries")
		return
	}
	var by *int64
	if u := CurrentUser(r.Context()); u != nil {
		uid := u.ID
		by = &uid
	}
	v, err := c.d.Relays.SaveSettings(r.Context(), id, in.settings(), by)
	if err != nil {
		c.relayErr(w, r, "save relay settings", err)
		return
	}
	c.audit(r, "relay.settings.update", "relay", id, map[string]any{"overridden": overriddenKeys(v.Overrides)})
	writeRelaySettings(w, v)
}

func overriddenKeys(s reconcile.Settings) []string {
	out := []string{}
	add := func(set bool, k string) {
		if set {
			out = append(out, k)
		}
	}
	add(s.Blacklist != nil, "blacklist")
	add(s.Blocklist != nil, "blocklist")
	add(s.LimitSpeedMbps != nil, "limit_speed_mbps")
	add(s.TotalBandwidthMbps != nil, "total_bandwidth_mbps")
	add(s.SingleBandwidthMbps != nil, "single_bandwidth_mbps")
	add(s.DowngradeThreshold != nil, "downgrade_threshold")
	add(s.DowngradeStartCheckSecs != nil, "downgrade_start_check_s")
	return out
}

func (c *b2bCore) handleRelayUsageByID(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	usage, err := c.d.Relays.Usage(actorCtx(r), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, relays.ErrNoUsage) {
			c.relayErr(w, r, "relay usage", err)
			return
		}
		unavailable(w, err.Error())
		return
	}
	out := make([]relayUsageDTO, 0, len(usage))
	for _, u := range usage {
		out = append(out, relayUsageDTO{Peer: u.Addr, ElapsedS: int64(u.Elapsed / time.Second), TotalMB: u.TotalMB,
			HighestKbps: float64(u.HighestKbps), AvgKbps: float64(u.AvgKbps), CurrentKbps: float64(u.CurrentKbps)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"checked_at": c.d.Store.Now().UTC(), "items": out})
}

func (c *b2bCore) handleGetRelayOptions(w http.ResponseWriter, r *http.Request) {
	if !c.needRelays(w) {
		return
	}
	o, err := c.d.Relays.GetOptions(r.Context())
	if err != nil {
		c.relayErr(w, r, "relay options", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, o)
}

func (c *b2bCore) handlePutRelayOptions(w http.ResponseWriter, r *http.Request) {
	if !c.needRelays(w) {
		return
	}
	var o relays.Options
	if !decodeBody(w, r, &o, 4<<10) {
		return
	}
	var by *int64
	if u := CurrentUser(r.Context()); u != nil {
		id := u.ID
		by = &id
	}
	if err := c.d.Relays.SetOptions(r.Context(), o, by); err != nil {
		c.relayErr(w, r, "relay options", err)
		return
	}
	recordAudit(r.Context(), c.d, r, "relay.options.update", "settings", "relays",
		map[string]any{"require_approval": o.RequireApproval, "allow_private_hosts": o.AllowPrivateHosts})
	httpx.WriteJSON(w, http.StatusOK, o)
}

type relayTrafficSummaryDTO struct {
	RelayID       int64                  `json:"relay_id"`
	Hours         int                    `json:"hours"`
	TotalBytes    int64                  `json:"total_bytes"`
	TotalSessions int                    `json:"total_sessions"`
	MaxActive     int                    `json:"max_active"`
	Items         []relayTrafficPointDTO `json:"items"`
}

type relayTrafficPointDTO struct {
	HourStart time.Time `json:"hour_start"`
	Bytes     int64     `json:"bytes"`
	Sessions  int       `json:"sessions"`
	MaxActive int       `json:"max_active"`
}

func (c *b2bCore) handleRelayTraffic(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	hours := 24
	if q := r.URL.Query().Get("hours"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n >= 1 && n <= 168 {
			hours = n
		}
	}
	now := c.d.Store.Now().UTC()
	start := store.HourStart(now).Add(-time.Duration(hours-1) * time.Hour)
	samples, err := c.d.Relays.Traffic(r.Context(), id, start)
	if err != nil {
		c.relayErr(w, r, "relay traffic", err)
		return
	}

	byHour := make(map[int64]relays.TrafficSample, len(samples))
	for _, s := range samples {
		byHour[store.HourStart(s.HourStart).UnixMilli()] = s
	}

	items := make([]relayTrafficPointDTO, hours)
	var (
		totalBytes    int64
		totalSessions int
		maxActive     int
	)
	for i := range items {
		t := start.Add(time.Duration(i) * time.Hour)
		s := byHour[t.UnixMilli()]
		items[i] = relayTrafficPointDTO{
			HourStart: t,
			Bytes:     s.Bytes,
			Sessions:  s.Sessions,
			MaxActive: s.MaxActive,
		}
		totalBytes += s.Bytes
		totalSessions += s.Sessions
		if s.MaxActive > maxActive {
			maxActive = s.MaxActive
		}
	}

	out := relayTrafficSummaryDTO{
		RelayID:       id,
		Hours:         hours,
		TotalBytes:    totalBytes,
		TotalSessions: totalSessions,
		MaxActive:     maxActive,
		Items:         items,
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

type relayPrefixRuleDTO struct {
	ID        int64  `json:"id"`
	CIDR      string `json:"cidr"`
	RelayHost string `json:"relay_host"`
	Comment   string `json:"comment"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type relayPrefixRuleInput struct {
	CIDR      string `json:"cidr"`
	RelayHost string `json:"relay_host"`
	Comment   string `json:"comment"`
}

func newRelayPrefixRuleDTO(r relays.PrefixRule) relayPrefixRuleDTO {
	return relayPrefixRuleDTO{
		ID:        r.ID,
		CIDR:      r.CIDR,
		RelayHost: r.RelayHost,
		Comment:   r.Comment,
		CreatedAt: r.CreatedAt.Format(time.RFC3339),
		UpdatedAt: r.UpdatedAt.Format(time.RFC3339),
	}
}

func (c *b2bCore) handleListPrefixRules(w http.ResponseWriter, r *http.Request) {
	if !c.needRelays(w) {
		return
	}
	rules, err := c.d.Relays.ListPrefixRules(r.Context())
	if err != nil {
		c.fail(w, r, "list prefix rules", err)
		return
	}
	out := make([]relayPrefixRuleDTO, 0, len(rules))
	for _, rule := range rules {
		out = append(out, newRelayPrefixRuleDTO(rule))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (c *b2bCore) handleCreatePrefixRule(w http.ResponseWriter, r *http.Request) {
	if !c.needRelays(w) {
		return
	}
	var in relayPrefixRuleInput
	if !decodeBody(w, r, &in, 64<<10) {
		return
	}
	rule := relays.PrefixRule{
		CIDR:      in.CIDR,
		RelayHost: in.RelayHost,
		Comment:   in.Comment,
	}
	if err := rule.Validate(); err != nil {
		validationFailed(w, "invalid prefix rule", map[string]string{"cidr": err.Error()})
		return
	}
	if err := c.d.Relays.CreatePrefixRule(r.Context(), &rule); err != nil {
		c.fail(w, r, "create prefix rule", err)
		return
	}
	c.audit(r, "relays.prefix_rule.create", "relay_prefix_rule", rule.ID,
		map[string]any{"cidr": rule.CIDR, "relay_host": rule.RelayHost})
	httpx.WriteJSON(w, http.StatusCreated, newRelayPrefixRuleDTO(rule))
}

func (c *b2bCore) handleDeletePrefixRule(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok || !c.needRelays(w) {
		return
	}
	if err := c.d.Relays.DeletePrefixRule(r.Context(), id); err != nil {
		c.relayErr(w, r, "delete prefix rule", err)
		return
	}
	c.audit(r, "relays.prefix_rule.delete", "relay_prefix_rule", id, nil)
	w.WriteHeader(http.StatusNoContent)
}
