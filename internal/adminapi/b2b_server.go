package adminapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/hbbs/control"
	"github.com/bropines/rdpanel/internal/hbbs/reconcile"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

// Server pages (/server/*, admin only): health, desired settings via the
// reconciler, hbbs tables, hbbr relay usage and the allow-listed console.

// Admin-audit actions written for admin-channel calls.
const (
	ActionServerCommand = "server.command" // mutating typed call (reconciler, unblock, ...)
	ActionServerConsole = "server.console" // raw console line
)

// maxConsoleOutput bounds the console output returned to the browser.
const maxConsoleOutput = 64 << 10

// consoleExcerpt bounds the output kept in the audit trail.
const consoleExcerpt = 1024

func (c *b2bCore) registerServer(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(RequireAdmin)
		r.Get("/server/health", c.handleServerHealth)
		r.Get("/server/settings", c.handleGetServerSettings)
		r.Patch("/server/settings", c.handlePatchServerSettings)
		r.Post("/server/settings/reapply", c.handleReapplyServerSettings)
		r.Get("/server/client-options", c.handleGetClientOptions)
		r.Put("/server/client-options", c.handlePutClientOptions)
		r.Get("/server/client-config", c.handleGetClientConfig)
		r.Get("/server/ip-blocker", c.handleIPBlocker)
		r.Delete("/server/ip-blocker/{ip}", c.handleUnblockIP)
		r.Get("/server/ip-changes", c.handleIPChanges)
		r.Delete("/server/ip-changes/{rustdesk_id}", c.handleClearIPChanges)
		r.Get("/server/punch-requests", c.handlePunchRequests)
		r.Get("/server/relay-usage", c.handleRelayUsage)
		r.Get("/server/console/commands", c.handleConsoleCommands)
		r.Post("/server/console", c.handleConsole)
		r.Get("/server/console/history", c.handleConsoleHistory)
		c.registerLogs(r)
	})
}

func unavailable(w http.ResponseWriter, msg string) {
	httpx.WriteError(w, http.StatusServiceUnavailable, "server_unavailable", msg)
}

// actorCtx attaches the request's admin to ctx for the admin-channel audit.
func actorCtx(r *http.Request) context.Context {
	a := control.Actor{Name: "anonymous", IP: httpx.ClientIPString(r)}
	if u := CurrentUser(r.Context()); u != nil {
		id := u.ID
		a.UserID, a.Name = &id, u.Username
	}
	return control.WithActor(r.Context(), a)
}

// controlErr maps an admin-channel error: validation errors are 400, the rest
// (dial/timeout/parse) 503.
func (c *b2bCore) controlErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, control.ErrInvalidArgs) || errors.Is(err, control.ErrNotAllowed) {
		badRequest(w, err.Error())
		return
	}
	c.log.WarnContext(r.Context(), "hbbs admin channel", slog.Any("err", err))
	unavailable(w, err.Error())
}

func (c *b2bCore) needControl(w http.ResponseWriter) bool {
	if c.d.Control == nil {
		unavailable(w, "the hbbs/hbbr admin channel is not configured")
		return false
	}
	return true
}

func (c *b2bCore) needReconciler(w http.ResponseWriter) bool {
	if c.d.Reconciler == nil {
		unavailable(w, "the hbbs/hbbr admin channel is not configured")
		return false
	}
	return true
}

// ---- health ----

// b2bComponentHealthDTO is components.schemas.ComponentHealth.
type b2bComponentHealthDTO struct {
	Status    string     `json:"status"`
	Address   string     `json:"address,omitempty"`
	LatencyMs *int64     `json:"latency_ms"`
	CheckedAt time.Time  `json:"checked_at"`
	UpSince   *time.Time `json:"up_since"`
	Error     *string    `json:"error"`
}

type b2bPresenceHealthDTO struct {
	Source         string     `json:"source"`
	LastPollAt     *time.Time `json:"last_poll_at"`
	PollDurationMs *int64     `json:"poll_duration_ms"`
	DevicesOnline  int        `json:"devices_online"`
}

// serverHealthDTO is components.schemas.ServerHealth.
type serverHealthDTO struct {
	HBBS           b2bComponentHealthDTO `json:"hbbs"`
	HBBR           b2bComponentHealthDTO `json:"hbbr"`
	Presence       b2bPresenceHealthDTO  `json:"presence"`
	SettingsInSync *bool                 `json:"settings_in_sync,omitempty"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	t = t.UTC() // API timestamps are UTC (worker clocks use time.Now)
	return &t
}

func millis(d time.Duration) *int64 {
	ms := d.Milliseconds()
	return &ms
}

func componentHealth(up bool, since time.Time, rtt time.Duration, lastErr string, now time.Time) b2bComponentHealthDTO {
	h := b2bComponentHealthDTO{Status: "down", CheckedAt: now, Error: strPtr(lastErr)}
	switch {
	case up:
		h.Status, h.LatencyMs, h.UpSince = "up", millis(rtt), timePtr(since)
	case since.IsZero() && lastErr == "":
		h.Status = "unknown"
	}
	return h
}

// serverHealth assembles ServerHealth (also usable by the dashboard).
func (c *b2bCore) serverHealth(ctx context.Context) serverHealthDTO {
	now := c.d.Store.Now().UTC()
	out := serverHealthDTO{
		HBBS:     b2bComponentHealthDTO{Status: "unknown", CheckedAt: now},
		HBBR:     b2bComponentHealthDTO{Status: "unknown", CheckedAt: now},
		Presence: b2bPresenceHealthDTO{Source: "heartbeat"},
	}
	if p := c.d.PresenceSrc; p != nil {
		h := p.Health()
		out.HBBS = componentHealth(h.Reachable, h.Since, h.RTT, h.LastError, now)
		if h.Reachable {
			out.Presence.Source = "hbbs"
		}
		out.Presence.LastPollAt = timePtr(h.LastOK)
		if h.Polls > 0 {
			out.Presence.PollDurationMs = millis(h.RTT)
		}
		out.Presence.DevicesOnline = h.Online
	}
	if rc := c.d.Reconciler; rc != nil {
		st := rc.Status()
		if c.d.PresenceSrc == nil {
			out.HBBS = componentHealth(st.HBBS.Reachable, st.HBBS.Since, st.HBBS.RTT, st.HBBS.LastError, now)
		}
		out.HBBR = componentHealth(st.HBBR.Reachable, st.HBBR.Since, st.HBBR.RTT, st.HBBR.LastError, now)
		if keys, err := rc.KeyStatus(ctx); err == nil {
			sync := true
			for _, k := range keys {
				if k.AppliedAt == nil || k.LastError != "" {
					sync = false
				}
			}
			out.SettingsInSync = &sync
		}
	}
	if ctl := c.d.Control; ctl != nil {
		out.HBBS.Address, out.HBBR.Address = ctl.Addr(control.HBBS), ctl.Addr(control.HBBR)
	}
	return out
}

func (c *b2bCore) handleServerHealth(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, c.serverHealth(r.Context()))
}

// ---- settings ----

// serverSettingsValuesDTO is components.schemas.ServerSettingsValues.
type serverSettingsValuesDTO struct {
	RelayServers         []string `json:"relay_servers"`
	AlwaysUseRelay       *bool    `json:"always_use_relay"`
	MustLogin            *bool    `json:"must_login"`
	TCPKeepalive         *bool    `json:"tcp_keepalive"`
	WebRTC               *bool    `json:"webrtc"`
	Blacklist            []string `json:"blacklist"`
	Blocklist            []string `json:"blocklist"`
	LimitSpeedMbps       *float64 `json:"limit_speed_mbps"`
	TotalBandwidthMbps   *float64 `json:"total_bandwidth_mbps"`
	SingleBandwidthMbps  *float64 `json:"single_bandwidth_mbps"`
	DowngradeThreshold   *float64 `json:"downgrade_threshold"`
	DowngradeStartCheckS *int64   `json:"downgrade_start_check_s"`
}

func newSettingsValuesDTO(s reconcile.Settings) serverSettingsValuesDTO {
	return serverSettingsValuesDTO{RelayServers: s.RelayServers, AlwaysUseRelay: s.AlwaysUseRelay,
		MustLogin: s.MustLogin, TCPKeepalive: s.TCPKeepalive, WebRTC: s.WebRTC, Blacklist: s.Blacklist, Blocklist: s.Blocklist, LimitSpeedMbps: s.LimitSpeedMbps,
		TotalBandwidthMbps: s.TotalBandwidthMbps, SingleBandwidthMbps: s.SingleBandwidthMbps,
		DowngradeThreshold: s.DowngradeThreshold, DowngradeStartCheckS: s.DowngradeStartCheckSecs}
}

// settingStatusDTO is components.schemas.ServerSettingStatus.
type settingStatusDTO struct {
	Key       string     `json:"key"`
	Target    string     `json:"target"`
	InSync    bool       `json:"in_sync"`
	Managed   bool       `json:"managed"`
	UpdatedAt *time.Time `json:"updated_at"`
	UpdatedBy *string    `json:"updated_by"`
	AppliedAt *time.Time `json:"applied_at"`
	LastError *string    `json:"last_error"`
}

// serverSettingsStateDTO is components.schemas.ServerSettingsState.
type serverSettingsStateDTO struct {
	Desired   serverSettingsValuesDTO `json:"desired"`
	Applied   serverSettingsValuesDTO `json:"applied"`
	Keys      []settingStatusDTO      `json:"keys"`
	CheckedAt *time.Time              `json:"checked_at"`
}

// settingKey maps an API key (ServerSettingKey) to the server_settings row.
type settingKey struct {
	api    string
	target control.Target
	key    string
}

var settingKeys = []settingKey{
	{"relay_servers", control.HBBS, reconcile.KeyRelayServers},
	{"always_use_relay", control.HBBS, reconcile.KeyAlwaysUseRelay},
	{"must_login", control.HBBS, reconcile.KeyMustLogin},
	{"tcp_keepalive", control.HBBS, reconcile.KeyTCPKeepalive},
	{"webrtc", control.HBBS, reconcile.KeyWebRTC},
	{"blacklist", control.HBBR, reconcile.KeyBlacklist},
	{"blocklist", control.HBBR, reconcile.KeyBlocklist},
	{"limit_speed_mbps", control.HBBR, reconcile.KeyLimitSpeed},
	{"total_bandwidth_mbps", control.HBBR, reconcile.KeyTotalBandwidth},
	{"single_bandwidth_mbps", control.HBBR, reconcile.KeySingleBandwidth},
	{"downgrade_threshold", control.HBBR, reconcile.KeyDowngradeThreshold},
	{"downgrade_start_check_s", control.HBBR, reconcile.KeyDowngradeStartCheck},
}

// settingsState reads desired, observed (live read-back) and per-key status.
func (c *b2bCore) settingsState(ctx context.Context) (serverSettingsStateDTO, error) {
	rc := c.d.Reconciler
	desired, err := rc.Desired(ctx)
	if err != nil {
		return serverSettingsStateDTO{}, err
	}
	statuses, err := rc.KeyStatus(ctx)
	if err != nil {
		return serverSettingsStateDTO{}, err
	}
	obs, _ := rc.Current(ctx) // unreachable targets leave their fields nil
	now := c.d.Store.Now().UTC()
	byKey := map[string]reconcile.KeyStatus{}
	for _, s := range statuses {
		byKey[string(s.Target)+"/"+s.Key] = s
	}
	names := map[int64]*string{}
	out := serverSettingsStateDTO{Desired: newSettingsValuesDTO(desired), Applied: newSettingsValuesDTO(obs.Settings),
		Keys: make([]settingStatusDTO, 0, len(settingKeys)), CheckedAt: &now}
	for _, k := range settingKeys {
		managed, inSync := reconcile.KeyInSync(k.target, k.key, desired, obs.Settings)
		d := settingStatusDTO{Key: k.api, Target: string(k.target), Managed: managed, InSync: inSync}
		if st, ok := byKey[string(k.target)+"/"+k.key]; ok {
			d.UpdatedAt, d.AppliedAt, d.LastError = timePtr(st.UpdatedAt), st.AppliedAt, strPtr(st.LastError)
			if st.LastError != "" {
				d.InSync = false
			}
			if st.UpdatedBy != nil {
				name, seen := names[*st.UpdatedBy]
				if !seen {
					if u, err := c.d.Store.Users.GetByID(ctx, *st.UpdatedBy); err == nil {
						name = &u.Username
					}
					names[*st.UpdatedBy] = name
				}
				d.UpdatedBy = name
			}
		}
		out.Keys = append(out.Keys, d)
	}
	return out, nil
}

func (c *b2bCore) writeSettingsState(w http.ResponseWriter, r *http.Request) {
	st, err := c.settingsState(actorCtx(r))
	if err != nil {
		c.fail(w, r, "server settings", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (c *b2bCore) handleGetServerSettings(w http.ResponseWriter, r *http.Request) {
	if c.needReconciler(w) {
		c.writeSettingsState(w, r)
	}
}

// applySettingsPatch decodes ServerSettingsPatch onto s. A null value makes
// the key unmanaged again (the server's CLI/env default applies).
func applySettingsPatch(s *reconcile.Settings, patch map[string]json.RawMessage) (keys []string, msg string) {
	var noWS = regexp.MustCompile(`^[^\s,]+$`)
	for name, raw := range patch {
		null := string(raw) == "null"
		var err error
		switch name {
		case "relay_servers":
			s.RelayServers = nil
			if !null {
				if err = json.Unmarshal(raw, &s.RelayServers); err == nil {
					if len(s.RelayServers) > 32 {
						return nil, "relay_servers: at most 32 entries"
					}
					for _, v := range s.RelayServers {
						if !noWS.MatchString(v) {
							return nil, "relay_servers: entries are host[:port] without spaces or commas"
						}
					}
				}
			}
		case "always_use_relay":
			s.AlwaysUseRelay = nil
			if !null {
				err = json.Unmarshal(raw, &s.AlwaysUseRelay)
			}
		case "must_login":
			s.MustLogin = nil
			if !null {
				err = json.Unmarshal(raw, &s.MustLogin)
			}
		case "tcp_keepalive":
			s.TCPKeepalive = nil
			if !null {
				err = json.Unmarshal(raw, &s.TCPKeepalive)
			}
		case "webrtc":
			s.WebRTC = nil
			if !null {
				err = json.Unmarshal(raw, &s.WebRTC)
			}
		case "blacklist", "blocklist":
			dst := &s.Blacklist
			if name == "blocklist" {
				dst = &s.Blocklist
			}
			*dst = nil
			if !null {
				if err = json.Unmarshal(raw, dst); err == nil && len(*dst) > reconcile.MaxListEntries {
					return nil, name + ": too many entries"
				}
			}
		case "limit_speed_mbps", "total_bandwidth_mbps", "single_bandwidth_mbps", "downgrade_threshold":
			dst := map[string]**float64{"limit_speed_mbps": &s.LimitSpeedMbps, "total_bandwidth_mbps": &s.TotalBandwidthMbps,
				"single_bandwidth_mbps": &s.SingleBandwidthMbps, "downgrade_threshold": &s.DowngradeThreshold}[name]
			*dst = nil
			if !null {
				err = json.Unmarshal(raw, dst)
			}
		case "downgrade_start_check_s":
			s.DowngradeStartCheckSecs = nil
			if !null {
				err = json.Unmarshal(raw, &s.DowngradeStartCheckSecs)
			}
		default:
			return nil, "unknown setting " + name
		}
		if err != nil {
			return nil, name + ": invalid value"
		}
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys, ""
}

func (c *b2bCore) handlePatchServerSettings(w http.ResponseWriter, r *http.Request) {
	if !c.needReconciler(w) {
		return
	}
	var patch map[string]json.RawMessage
	if !decodeBody(w, r, &patch, 1<<20) {
		return
	}
	ctx := actorCtx(r)
	desired, err := c.d.Reconciler.Desired(ctx)
	if err != nil {
		c.fail(w, r, "server settings", err)
		return
	}
	if _, ok := patch["relay_servers"]; ok && c.d.Relays != nil {
		badRequest(w, "relay_servers is computed from the Relays tab (Server → Relays); add, weight or drain relays there")
		return
	}
	keys, msg := applySettingsPatch(&desired, patch)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	u := CurrentUser(ctx)
	if err := c.d.Reconciler.Apply(ctx, desired, &u.ID); err != nil {
		if errors.Is(err, reconcile.ErrInvalid) {
			badRequest(w, strings.TrimPrefix(err.Error(), reconcile.ErrInvalid.Error()+": "))
			return
		}
		// Saved; the push failed (server down). Per-key last_error shows it and
		// the reconciler retries when the server is back.
		c.log.WarnContext(ctx, "apply server settings", slog.Any("err", err))
	}
	recordAudit(ctx, c.d, r, "server.settings.update", "server_settings", "", map[string]any{"keys": keys})
	c.writeSettingsState(w, r)
}

func (c *b2bCore) handleReapplyServerSettings(w http.ResponseWriter, r *http.Request) {
	if !c.needReconciler(w) {
		return
	}
	ctx := actorCtx(r)
	err := c.d.Reconciler.Reconcile(ctx)
	if err != nil {
		c.log.WarnContext(ctx, "reapply server settings", slog.Any("err", err))
	}
	details := map[string]any{"ok": err == nil}
	if err != nil {
		details["error"] = err.Error()
	}
	recordAudit(ctx, c.d, r, "server.settings.reapply", "server_settings", "", details)
	c.writeSettingsState(w, r)
}

// ---- hbbs tables ----

// fetchPaged collects one API page from hbbs' 10-row pages starting at the
// API page's offset.
func fetchPaged[T any](p b2bPaging, fetch func(start int) (int, []T, error)) ([]T, int, error) {
	out := []T{}
	start := p.offset()
	total, items, err := fetch(start)
	if err != nil {
		return nil, 0, err
	}
	for calls := 1; ; calls++ {
		out = append(out, items...)
		start += len(items)
		if len(out) >= p.Size || len(items) == 0 || start >= total || calls >= 100 {
			break
		}
		if total, items, err = fetch(start); err != nil {
			return nil, 0, err
		}
	}
	if len(out) > p.Size {
		out = out[:p.Size]
	}
	return out, total, nil
}

// ipBlockerDTO is components.schemas.IpBlockerEntry.
type ipBlockerDTO struct {
	IP              string `json:"ip"`
	Attempts        int    `json:"attempts"`
	AttemptsWindowS int64  `json:"attempts_window_s"`
	IDs             int    `json:"ids"`
	IDsWindowS      int64  `json:"ids_window_s"`
	Blocked         bool   `json:"blocked"`
}

// hbbs refuses registrations from an IP after > 30 RegisterPk calls per
// minute or > 300 distinct new ids per day (rendezvous_server.rs).
const (
	blockerMaxAttempts = 30
	blockerMaxIDs      = 300
)

func (c *b2bCore) handleIPBlocker(w http.ResponseWriter, r *http.Request) {
	q := newListQuery(r)
	p := q.b2bPaging()
	if !q.ok(w) || !c.needControl(w) {
		return
	}
	ctx := actorCtx(r)
	items, total, err := fetchPaged(p, func(start int) (int, []control.IPBlockerEntry, error) {
		pg, err := c.d.Control.IPBlocker(ctx, start)
		return pg.Total, pg.Entries, err
	})
	if err != nil {
		c.controlErr(w, r, err)
		return
	}
	out := make([]ipBlockerDTO, 0, len(items))
	for _, e := range items {
		out = append(out, ipBlockerDTO{IP: e.IP, Attempts: e.Attempts, AttemptsWindowS: int64(e.AttemptsAge / time.Second),
			IDs: e.DistinctIDs, IDsWindowS: int64(e.IDsAge / time.Second),
			Blocked: e.Attempts > blockerMaxAttempts || e.DistinctIDs > blockerMaxIDs})
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

func (c *b2bCore) handleUnblockIP(w http.ResponseWriter, r *http.Request) {
	if !c.needControl(w) {
		return
	}
	ip := chi.URLParam(r, "ip")
	if _, err := c.d.Control.IPBlockerLookup(actorCtx(r), ip, true); err != nil {
		c.controlErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ipChangesDTO is components.schemas.IpChangesEntry.
type ipChangesDTO struct {
	RustdeskID string         `json:"rustdesk_id"`
	Seconds    int64          `json:"seconds"`
	IPs        map[string]int `json:"ips"`
}

func (c *b2bCore) handleIPChanges(w http.ResponseWriter, r *http.Request) {
	q := newListQuery(r)
	p := q.b2bPaging()
	if !q.ok(w) || !c.needControl(w) {
		return
	}
	ctx := actorCtx(r)
	items, total, err := fetchPaged(p, func(start int) (int, []control.IPChange, error) {
		pg, err := c.d.Control.IPChanges(ctx, start)
		return pg.Total, pg.Entries, err
	})
	if err != nil {
		c.controlErr(w, r, err)
		return
	}
	out := make([]ipChangesDTO, 0, len(items))
	for _, e := range items {
		ips := e.IPs
		if ips == nil {
			ips = map[string]int{}
		}
		out = append(out, ipChangesDTO{RustdeskID: e.ID, Seconds: int64(e.Age / time.Second), IPs: ips})
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

func (c *b2bCore) handleClearIPChanges(w http.ResponseWriter, r *http.Request) {
	if !c.needControl(w) {
		return
	}
	if _, err := c.d.Control.IPChangesLookup(actorCtx(r), chi.URLParam(r, "rustdesk_id"), true); err != nil {
		c.controlErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// punchRequestDTO is components.schemas.PunchRequest.
type punchRequestDTO struct {
	ID           int64     `json:"id"`
	At           time.Time `json:"at"`
	FromIP       string    `json:"from_ip"`
	ToRustdeskID string    `json:"to_rustdesk_id"`
	ToIP         *string   `json:"to_ip"`
}

func (c *b2bCore) handlePunchRequests(w http.ResponseWriter, r *http.Request) {
	q := newListQuery(r)
	p := q.b2bPaging()
	f := q.auditQuery(p, true)
	if !q.ok(w) {
		return
	}
	rows, total, err := c.d.Store.PunchRequests.ListPanel(r.Context(), f)
	if err != nil {
		c.fail(w, r, "list punch requests", err)
		return
	}
	out := make([]punchRequestDTO, 0, len(rows))
	for _, p := range rows {
		out = append(out, punchRequestDTO{ID: p.ID, At: p.At, FromIP: p.FromIP, ToRustdeskID: p.ToRustdeskID, ToIP: strPtr(p.ToIP)})
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

// relayUsageDTO is components.schemas.RelayUsage.
type relayUsageDTO struct {
	Peer        string  `json:"peer"`
	ElapsedS    int64   `json:"elapsed_s"`
	TotalMB     float64 `json:"total_mb"`
	HighestKbps float64 `json:"highest_kbps"`
	AvgKbps     float64 `json:"avg_kbps"`
	CurrentKbps float64 `json:"current_kbps"`
}

func (c *b2bCore) handleRelayUsage(w http.ResponseWriter, r *http.Request) {
	if !c.needControl(w) {
		return
	}
	usage, err := c.d.Control.Usage(actorCtx(r))
	if err != nil {
		c.controlErr(w, r, err)
		return
	}
	out := make([]relayUsageDTO, 0, len(usage))
	for _, u := range usage {
		out = append(out, relayUsageDTO{Peer: u.Addr, ElapsedS: int64(u.Elapsed / time.Second), TotalMB: u.TotalMB,
			HighestKbps: float64(u.HighestKbps), AvgKbps: float64(u.AvgKbps), CurrentKbps: float64(u.CurrentKbps)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"checked_at": c.d.Store.Now().UTC(), "items": out})
}

// ---- console ----

// consoleCommandDTO is components.schemas.ConsoleCommand.
type consoleCommandDTO struct {
	Target      string `json:"target"`
	Cmd         string `json:"cmd"`
	Alias       string `json:"alias,omitempty"`
	ArgsHint    string `json:"args_hint,omitempty"`
	ArgPattern  string `json:"arg_pattern,omitempty"`
	MaxArgs     int    `json:"max_args"`
	Description string `json:"description"`
	Mutating    bool   `json:"mutating"`
}

type consoleMeta struct {
	desc     string
	maxArgs  int
	pattern  string
	mutating bool // some argument forms change server state
}

const (
	ipArg    = `^[0-9A-Fa-f:.]+$`
	ipsArg   = `^[0-9A-Fa-f:.|]+$`
	numArg   = `^[0-9]+(\.[0-9]+)?$`
	uintArg  = `^[0-9]+$`
	ynArg    = `^[YyNn]$`
	noArgPat = ``
)

// consoleCommands documents the allow-list of control.Commands() for the UI.
// The control package validates arguments again before sending.
var consoleCommands = map[string]consoleMeta{
	"hbbs/h":                     {"Show the hbbs help text.", 0, noArgPat, false},
	"hbbs/relay-servers":         {"Show the relay servers hbbs hands out, or replace them (comma-separated).", 1, `^[^\s]+$`, true},
	"hbbs/ip-blocker":            {"Registration rate-limiter table (page offset or IP); '<ip> -' unblocks.", 2, `^([0-9A-Fa-f:.]+|-)$`, true},
	"hbbs/ip-changes":            {"IDs registered from several IPs (page offset or id); '<id> -' forgets one.", 2, `^([A-Za-z0-9_-]+|-)$`, true},
	"hbbs/punch-requests":        {"Recorded connection attempts ([start] [page_size]); '-' clears the list.", 2, `^([0-9]+|-)$`, true},
	"hbbs/always-use-relay":      {"Show or set ALWAYS_USE_RELAY (Y/N). Prefer the server settings page.", 1, ynArg, true},
	"hbbs/test-geo":              {"Relay hbbs would pick for one or two IPs (advances its rotation).", 2, ipArg, false},
	"hbbs/must-login":            {"Show or set MUST_LOGIN (Y/N). Prefer the server settings page.", 1, ynArg, true},
	"hbbs/tcp-keepalive":         {"Show or set TCP_KEEPALIVE (Y/N): keepalives for TCP/WebSocket clients. Prefer the server settings page.", 1, ynArg, true},
	"hbbs/webrtc":                {"Show or set WEBRTC (Y/N): WebRTC signaling for RustDesk 1.5+ clients. Prefer the server settings page.", 1, ynArg, true},
	"hbbr/h":                     {"Show the hbbr help text.", 0, noArgPat, false},
	"hbbr/blacklist-add":         {"Throttle IPs to limit-speed (ip|ip|...).", 1, ipsArg, true},
	"hbbr/blacklist-remove":      {"Remove throttled IPs (ip|ip|... or all).", 1, `^([0-9A-Fa-f:.|]+|all)$`, true},
	"hbbr/blacklist":             {"List throttled IPs, or check one IP.", 1, ipArg, false},
	"hbbr/blocklist-add":         {"Refuse IPs and cut their running relays (ip|ip|...).", 1, ipsArg, true},
	"hbbr/blocklist-remove":      {"Remove refused IPs (ip|ip|... or all).", 1, `^([0-9A-Fa-f:.|]+|all)$`, true},
	"hbbr/blocklist":             {"List refused IPs, or check one IP.", 1, ipArg, false},
	"hbbr/downgrade-threshold":   {"Show or set the downgrade threshold ratio (> 0).", 1, numArg, true},
	"hbbr/downgrade-start-check": {"Show or set the downgrade start delay in seconds.", 1, uintArg, true},
	"hbbr/limit-speed":           {"Show or set the speed of throttled/downgraded relays (Mb/s).", 1, numArg, true},
	"hbbr/total-bandwidth":       {"Show or set the global relay bandwidth (Mb/s).", 1, numArg, true},
	"hbbr/single-bandwidth":      {"Show or set the per-relay bandwidth (Mb/s).", 1, numArg, true},
	"hbbr/usage":                 {"Active relays with traffic counters.", 0, noArgPat, false},
}

func consoleCommandList() []consoleCommandDTO {
	specs := control.Commands()
	out := make([]consoleCommandDTO, 0, len(specs))
	for _, s := range specs {
		m, ok := consoleCommands[string(s.Target)+"/"+s.Name]
		if !ok {
			m = consoleMeta{desc: s.Name, maxArgs: 2, pattern: `^\S+$`, mutating: true}
		}
		out = append(out, consoleCommandDTO{Target: string(s.Target), Cmd: s.Name, Alias: s.Alias, ArgsHint: s.Usage,
			ArgPattern: m.pattern, MaxArgs: m.maxArgs, Description: m.desc, Mutating: m.mutating})
	}
	return out
}

func (c *b2bCore) handleConsoleCommands(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": consoleCommandList()})
}

type consoleReq struct {
	Target string   `json:"target"`
	Cmd    string   `json:"cmd"`
	Args   []string `json:"args"`
}

// consoleResultDTO is components.schemas.ConsoleResult.
type consoleResultDTO struct {
	Output     string    `json:"output"`
	Truncated  bool      `json:"truncated"`
	DurationMs int64     `json:"duration_ms"`
	ExecutedAt time.Time `json:"executed_at"`
}

var argRe = regexp.MustCompile(`^\S+$`)

// validateConsole checks a console request against the documented allow-list
// (full command names only, argument patterns, size) and returns the line.
func validateConsole(req consoleReq) (string, string) {
	t := control.Target(req.Target)
	if !t.Valid() {
		return "", "target must be hbbs or hbbr"
	}
	var cmd *consoleCommandDTO
	for _, cc := range consoleCommandList() {
		if cc.Target == req.Target && cc.Cmd == req.Cmd {
			cmd = &cc
			break
		}
	}
	if cmd == nil {
		return "", "command not allowed (use a full command name from /server/console/commands)"
	}
	if len(req.Args) > cmd.MaxArgs || len(req.Args) > 8 {
		return "", "too many arguments"
	}
	pat := regexp.MustCompile(cmd.ArgPattern)
	for _, a := range req.Args {
		if !argRe.MatchString(a) || (cmd.ArgPattern != "" && !pat.MatchString(a)) {
			return "", "invalid argument " + strconvQuote(a)
		}
	}
	line := strings.Join(append([]string{req.Cmd}, req.Args...), " ")
	if len(line) >= 1024 {
		return "", "command longer than 1 KiB"
	}
	return line, ""
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(clipStr(s, 64))
	return string(b)
}

type consoleCaptureKey struct{}

// consoleCapture receives the CommandRecord of a console call so the handler
// can write one audit row that also carries an output excerpt.
type consoleCapture struct{ rec *control.CommandRecord }

func (c *b2bCore) handleConsole(w http.ResponseWriter, r *http.Request) {
	if !c.needControl(w) {
		return
	}
	var req consoleReq
	if !decodeJSON(w, r, &req) {
		return
	}
	line, msg := validateConsole(req)
	if msg != "" {
		badRequest(w, msg)
		return
	}
	u := CurrentUser(r.Context())
	if ok, wait := c.console.allow(u.ID); !ok {
		rateLimited(w, wait)
		return
	}
	capture := &consoleCapture{}
	ctx := context.WithValue(actorCtx(r), consoleCaptureKey{}, capture)
	start := time.Now()
	out, err := c.d.Control.ExecLine(ctx, control.Target(req.Target), line)
	dur := time.Since(start)
	if errors.Is(err, control.ErrInvalidArgs) || errors.Is(err, control.ErrNotAllowed) {
		badRequest(w, err.Error())
		return
	}
	rec := control.CommandRecord{Time: start.UTC(), Target: control.Target(req.Target), Command: req.Cmd,
		Args: req.Args, Raw: true, Actor: control.ActorFrom(ctx), Duration: dur, ResponseBytes: len(out)}
	if capture.rec != nil {
		rec = *capture.rec
	}
	if err != nil && rec.Err == "" {
		rec.Err = err.Error()
	}
	details := commandDetails(rec)
	excerpt := out
	if len(excerpt) > consoleExcerpt {
		excerpt = truncateUTF8(excerpt, consoleExcerpt)
	}
	details["output_excerpt"] = excerpt
	recordAudit(ctx, c.d, r, ActionServerConsole, "server", req.Target, details)
	if err != nil {
		c.controlErr(w, r, err)
		return
	}
	res := consoleResultDTO{Output: out, DurationMs: dur.Milliseconds(), ExecutedAt: start.UTC()}
	if len(out) > maxConsoleOutput {
		res.Output, res.Truncated = truncateUTF8(out, maxConsoleOutput), true
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// consoleHistoryDTO is components.schemas.ConsoleHistoryItem.
type consoleHistoryDTO struct {
	ID            int64     `json:"id"`
	ActorName     string    `json:"actor_name"`
	Target        string    `json:"target"`
	Cmd           string    `json:"cmd"`
	Args          []string  `json:"args"`
	OutputExcerpt *string   `json:"output_excerpt"`
	Error         *string   `json:"error"`
	CreatedAt     time.Time `json:"created_at"`
}

func detailString(d map[string]any, k string) string {
	s, _ := d[k].(string)
	return s
}

func (c *b2bCore) handleConsoleHistory(w http.ResponseWriter, r *http.Request) {
	q := newListQuery(r)
	p := q.b2bPaging()
	if !q.ok(w) {
		return
	}
	rows, total, err := c.d.Store.AdminAudit.ListPanel(r.Context(), store.AdminAuditQuery{Action: ActionServerConsole,
		AuditQuery: store.AuditQuery{Desc: true, Limit: p.Size, Offset: p.offset()}})
	if err != nil {
		c.fail(w, r, "console history", err)
		return
	}
	out := make([]consoleHistoryDTO, 0, len(rows))
	for _, e := range rows {
		h := consoleHistoryDTO{ID: e.ID, ActorName: e.ActorName, Target: detailString(e.Details, "target"),
			Cmd: detailString(e.Details, "cmd"), Args: []string{}, CreatedAt: e.CreatedAt,
			Error: strPtr(detailString(e.Details, "error"))}
		if v, ok := e.Details["output_excerpt"].(string); ok {
			h.OutputExcerpt = &v
		}
		if args, ok := e.Details["args"].([]any); ok {
			for _, a := range args {
				if s, ok := a.(string); ok {
					h.Args = append(h.Args, s)
				}
			}
		}
		out = append(out, h)
	}
	httpx.WriteJSON(w, http.StatusOK, newPagedList(out, total, p))
}

// ---- admin-channel audit sink ----

// CommandAudit implements control.AuditSink on admin_audit: mutating typed
// calls become "server.command" rows and raw console lines "server.console"
// rows (the console handler writes those itself, with an output excerpt).
// Read-only typed calls are not persisted (they are logged by control).
type CommandAudit struct {
	Store *store.Store
	Log   *slog.Logger
}

// NewCommandAudit returns the sink to install with control.Client.SetAudit.
func NewCommandAudit(st *store.Store, log *slog.Logger) *CommandAudit {
	return &CommandAudit{Store: st, Log: log}
}

// PunchCollectorActor names the system actor of the punch-request collector,
// whose periodic "pr -" is housekeeping and is not audited.
const PunchCollectorActor = "punch-collector"

func commandDetails(rec control.CommandRecord) map[string]any {
	args := rec.Args
	if args == nil {
		args = []string{}
	}
	d := map[string]any{"target": string(rec.Target), "cmd": rec.Command, "args": args, "mutating": rec.Mutating,
		"raw": rec.Raw, "duration_ms": rec.Duration.Milliseconds(), "response_bytes": rec.ResponseBytes}
	if rec.ResponseSHA256 != "" {
		d["response_sha256"] = rec.ResponseSHA256
	}
	if rec.Err != "" {
		d["error"] = rec.Err
	}
	return d
}

// RecordCommand implements control.AuditSink.
func (a *CommandAudit) RecordCommand(ctx context.Context, rec control.CommandRecord) {
	if c, ok := ctx.Value(consoleCaptureKey{}).(*consoleCapture); ok && rec.Raw {
		c.rec = &rec
		return
	}
	if !rec.Mutating && !rec.Raw {
		return
	}
	if rec.Actor.UserID == nil && rec.Actor.Name == PunchCollectorActor {
		return
	}
	action := ActionServerCommand
	if rec.Raw {
		action = ActionServerConsole
	}
	e := &store.AuditEntry{ActorUserID: rec.Actor.UserID, ActorName: rec.Actor.Name, ActorIP: rec.Actor.IP,
		Action: action, TargetType: "server", TargetID: string(rec.Target), Details: commandDetails(rec),
		CreatedAt: rec.Time}
	if err := a.Store.AdminAudit.Record(context.WithoutCancel(ctx), e); err != nil && a.Log != nil {
		a.Log.WarnContext(ctx, "admin audit: server command", slog.Any("err", err))
	}
}

// clientConfigDTO is components.schemas.ClientConfig.
type clientConfigDTO struct {
	IDServer       string `json:"id_server"`
	RelayServer    string `json:"relay_server"`
	APIServer      string `json:"api_server"`
	PublicKey      string `json:"public_key"`
	ConfigString   string `json:"config_string"`
	IDServerIP     string `json:"id_server_ip,omitempty"`
	RelayServerIP  string `json:"relay_server_ip,omitempty"`
	ConfigStringIP string `json:"config_string_ip,omitempty"`
}

type rustdeskCustomServer struct {
	Host  string `json:"host,omitempty"`
	Relay string `json:"relay,omitempty"`
	Key   string `json:"key,omitempty"`
	API   string `json:"api,omitempty"`
}

// EncodeRustdeskConfig serializes the connection parameters into RustDesk's
// URL-safe base64 reversed JSON config string format.
func EncodeRustdeskConfig(host, relay, api, key string) (string, error) {
	b, err := json.Marshal(rustdeskCustomServer{
		Host:  host,
		Relay: relay,
		Key:   key,
		API:   api,
	})
	if err != nil {
		return "", err
	}
	b64 := base64.RawURLEncoding.EncodeToString(b)
	runes := []rune(b64)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes), nil
}

// DecodeRustdeskConfig decodes a RustDesk config string back into its parameters.
func DecodeRustdeskConfig(s string) (host, relay, api, key string, err error) {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	b, err := base64.RawURLEncoding.DecodeString(string(runes))
	if err != nil {
		b, err = base64.URLEncoding.DecodeString(string(runes))
		if err != nil {
			return "", "", "", "", err
		}
	}
	var cs rustdeskCustomServer
	if err := json.Unmarshal(b, &cs); err != nil {
		return "", "", "", "", err
	}
	return cs.Host, cs.Relay, cs.API, cs.Key, nil
}

func (c *b2bCore) handleGetClientConfig(w http.ResponseWriter, r *http.Request) {
	var publicKey string
	if c.d.Config != nil && c.d.Config.HBBS.PublicKeyFile != "" {
		if raw, err := os.ReadFile(c.d.Config.HBBS.PublicKeyFile); err == nil {
			publicKey = strings.TrimSpace(string(raw))
		}
	}

	var apiServer, idServer, relayServer string
	if c.d.Config != nil && c.d.Config.PublicURL != "" {
		apiServer = strings.TrimRight(c.d.Config.PublicURL, "/")
		if u, err := url.Parse(apiServer); err == nil && u.Hostname() != "" {
			idServer = u.Hostname()
		}
	}
	if apiServer == "" {
		scheme := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			scheme = "https"
		}
		host := r.Host
		if host == "" {
			host = "127.0.0.1:21114"
		}
		apiServer = scheme + "://" + host
		if h, _, err := net.SplitHostPort(host); err == nil {
			idServer = h
		} else {
			idServer = host
		}
	}

	if idServer == "" {
		if c.d.Config != nil && c.d.Config.HBBS.IDServer != "" {
			if h, _, err := net.SplitHostPort(c.d.Config.HBBS.IDServer); err == nil && h != "127.0.0.1" && h != "localhost" && h != "::1" {
				idServer = h
			}
		}
	}
	if idServer == "" {
		idServer = "127.0.0.1"
	}

	if c.d.Config != nil && c.d.Config.HBBS.RelayPublicHost != "" {
		relayServer = c.d.Config.HBBS.RelayPublicHost
	} else {
		relayServer = idServer
	}

	configStr, _ := EncodeRustdeskConfig(idServer, relayServer, apiServer, publicKey)

	idIP := resolveHostIP(r.Context(), idServer)
	relayIP := resolveHostIP(r.Context(), relayServer)
	var configStrIP string
	if idIP != "" || relayIP != "" {
		effID := idServer
		if idIP != "" {
			effID = idIP
		}
		effRelay := relayServer
		if relayIP != "" {
			effRelay = relayIP
		}
		configStrIP, _ = EncodeRustdeskConfig(effID, effRelay, apiServer, publicKey)
	}

	httpx.WriteJSON(w, http.StatusOK, clientConfigDTO{
		IDServer:       idServer,
		RelayServer:    relayServer,
		APIServer:      apiServer,
		PublicKey:      publicKey,
		ConfigString:   configStr,
		IDServerIP:     idIP,
		RelayServerIP:  relayIP,
		ConfigStringIP: configStrIP,
	})
}

func resolveHostIP(ctx context.Context, hostPort string) string {
	if hostPort == "" {
		return ""
	}
	h, p, err := net.SplitHostPort(hostPort)
	if err != nil {
		h = hostPort
		p = ""
	}
	if ip := net.ParseIP(h); ip != nil {
		return ""
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(c, "ip4", h)
	if err != nil || len(ips) == 0 {
		return ""
	}
	ipStr := ips[0].String()
	if p != "" {
		return net.JoinHostPort(ipStr, p)
	}
	return ipStr
}
