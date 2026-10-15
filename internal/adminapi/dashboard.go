package adminapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/bropines/rustdesk-panel/internal/buildinfo"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// componentHealthDTO is components.schemas.ComponentHealth.
type componentHealthDTO struct {
	Status    string     `json:"status"`
	Address   string     `json:"address,omitempty"`
	LatencyMS *int64     `json:"latency_ms"`
	CheckedAt time.Time  `json:"checked_at"`
	UpSince   *time.Time `json:"up_since"`
	Error     *string    `json:"error"`
}

type presenceHealthDTO struct {
	Source         string     `json:"source"`
	LastPollAt     *time.Time `json:"last_poll_at"`
	PollDurationMS *int64     `json:"poll_duration_ms"`
	DevicesOnline  int        `json:"devices_online"`
}

// ServerHealthDTO is components.schemas.ServerHealth.
type ServerHealthDTO struct {
	HBBS           componentHealthDTO `json:"hbbs"`
	HBBR           componentHealthDTO `json:"hbbr"`
	Presence       presenceHealthDTO  `json:"presence"`
	SettingsInSync *bool              `json:"settings_in_sync,omitempty"`
}

func ms(d time.Duration) *int64 {
	v := d.Milliseconds()
	return &v
}

func channelHealth(c ChannelStatus, addr string, now time.Time) componentHealthDTO {
	h := componentHealthDTO{Status: "unknown", Address: addr, CheckedAt: now}
	if !c.Known {
		return h
	}
	if c.Reachable {
		h.Status, h.LatencyMS = "up", ms(c.RTT)
		if !c.Since.IsZero() {
			since := c.Since.UTC()
			h.UpSince = &since
		}
		return h
	}
	h.Status, h.Error = "down", strPtr(c.LastError)
	return h
}

// serverHealth assembles hbbs/hbbr health from the presence poller (hbbs
// rendezvous port) and the reconciler (admin channels). devicesOnline is the
// current number of online devices.
func (a *resources) serverHealth(ctx context.Context, devicesOnline int) ServerHealthDTO {
	now := a.d.Store.Now()
	var hbbsAddr, hbbrAddr string
	if a.d.Config != nil {
		hbbsAddr, hbbrAddr = a.d.Config.HBBS.IDServer, a.d.Config.HBBS.RelayAdminAddr()
	}
	out := ServerHealthDTO{
		HBBS:     componentHealthDTO{Status: "unknown", Address: hbbsAddr, CheckedAt: now},
		HBBR:     componentHealthDTO{Status: "unknown", Address: hbbrAddr, CheckedAt: now},
		Presence: presenceHealthDTO{Source: "heartbeat", DevicesOnline: devicesOnline},
	}
	var hbbsCh, hbbrCh ChannelStatus
	if a.d.Servers != nil {
		hbbsCh, hbbrCh = a.d.Servers.ChannelStatus()
		out.HBBR = channelHealth(hbbrCh, hbbrAddr, now)
		if in, err := a.d.Servers.SettingsInSync(ctx); err == nil {
			out.SettingsInSync = &in
		} else {
			a.d.Log.WarnContext(ctx, "settings sync state", slog.Any("err", err))
		}
	}
	if a.d.Presence != nil {
		ph := a.d.Presence.PresenceHealth()
		out.HBBS = channelHealth(ChannelStatus{Known: ph.Polls > 0, Reachable: ph.Reachable, Since: ph.Since,
			RTT: ph.RTT, LastError: ph.LastError}, hbbsAddr, now)
		if ph.Reachable {
			out.Presence.Source = "hbbs"
		}
		if !ph.LastOK.IsZero() {
			last := ph.LastOK.UTC()
			out.Presence.LastPollAt = &last
		}
		if ph.Polls > 0 {
			out.Presence.PollDurationMS = ms(ph.RTT)
		}
	} else if a.d.Servers != nil {
		out.HBBS = channelHealth(hbbsCh, hbbsAddr, now)
	}
	return out
}

type seriesPointDTO struct {
	T time.Time `json:"t"`
	V int       `json:"v"`
}

type dashboardSeriesDTO struct {
	Logins      []seriesPointDTO `json:"logins"`
	Connections []seriesPointDTO `json:"connections"`
	Online      []seriesPointDTO `json:"online"`
}

type versionDTO struct {
	Panel     string `json:"panel"`
	Commit    string `json:"commit,omitempty"`
	BuildDate string `json:"build_date,omitempty"`
	Go        string `json:"go,omitempty"`
}

type dashboardDTO struct {
	DevicesTotal      int                `json:"devices_total"`
	DevicesOnline     int                `json:"devices_online"`
	UsersTotal        int                `json:"users_total"`
	AddressBooksTotal int                `json:"address_books_total"`
	Logins24h         int                `json:"logins_24h"`
	FailedLogins24h   int                `json:"failed_logins_24h"`
	Connections24h    int                `json:"connections_24h"`
	ActiveConnections int                `json:"active_connections"`
	Series            dashboardSeriesDTO `json:"series"`
	Health            *ServerHealthDTO   `json:"health"`
	Version           versionDTO         `json:"version"`
}

// dashboardHours is the length of the hourly series.
const dashboardHours = 24

func series(start time.Time, vals map[int64]int) []seriesPointDTO {
	out := make([]seriesPointDTO, dashboardHours)
	for i := range out {
		t := start.Add(time.Duration(i) * time.Hour)
		out[i] = seriesPointDTO{T: t, V: vals[t.UnixMilli()]}
	}
	return out
}

func (a *resources) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := CurrentUser(ctx)
	now := a.d.Store.Now()
	start := store.HourStart(now).Add(-(dashboardHours - 1) * time.Hour)
	day := now.Add(-24 * time.Hour)
	var scope *int64 // nil = everything (admin)
	if !user.IsAdmin() {
		scope = &user.ID
	}
	var out dashboardDTO
	fail := func(err error) { a.fail500(w, r, "dashboard", err) }

	dp := store.DeviceSearch{OwnerUserID: scope}
	var err error
	if out.DevicesTotal, err = a.d.Store.Devices.Count(ctx, dp); err != nil {
		fail(err)
		return
	}
	a.onlineSet(&dp)
	online := true
	dp.Online = &online
	if out.DevicesOnline, err = a.d.Store.Devices.Count(ctx, dp); err != nil {
		fail(err)
		return
	}
	if out.UsersTotal, err = a.d.Store.Users.Count(ctx); err != nil {
		fail(err)
		return
	}
	if out.AddressBooksTotal, err = a.d.Store.AddressBooks.CountVisible(ctx, scope); err != nil {
		fail(err)
		return
	}
	logins, failed, _, err := a.d.Store.LoginEvents.LoginStats(ctx, day, scope)
	if err != nil {
		fail(err)
		return
	}
	_, _, loginHours, err := a.d.Store.LoginEvents.LoginStats(ctx, start, scope)
	if err != nil {
		fail(err)
		return
	}
	out.Logins24h, out.FailedLogins24h = logins, failed
	conns, _, _, err := a.d.Store.ConnectionAudits.ConnectionStats(ctx, day, scope)
	if err != nil {
		fail(err)
		return
	}
	_, active, connHours, err := a.d.Store.ConnectionAudits.ConnectionStats(ctx, start, scope)
	if err != nil {
		fail(err)
		return
	}
	out.Connections24h, out.ActiveConnections = conns, active

	onlineHours := map[int64]int{}
	if scope == nil {
		if onlineHours, err = a.d.Store.PresenceSamples().Since(ctx, start); err != nil {
			fail(err)
			return
		}
	}
	cur := store.HourStart(now).UnixMilli()
	onlineHours[cur] = max(onlineHours[cur], out.DevicesOnline)
	out.Series = dashboardSeriesDTO{Logins: series(start, loginHours), Connections: series(start, connHours),
		Online: series(start, onlineHours)}

	if scope == nil {
		h := a.serverHealth(ctx, out.DevicesOnline)
		out.Health = &h
	}
	bi := buildinfo.Get()
	out.Version = versionDTO{Panel: bi.Version, Commit: bi.Commit, BuildDate: bi.Date, Go: bi.GoVersion}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// SampleOnline records the current number of online devices into the
// hourly presence samples (dashboard "online" series). Call it periodically.
func SampleOnline(ctx context.Context, st *store.Store, p PresenceReader) error {
	if p == nil {
		return nil
	}
	return st.PresenceSamples().Record(ctx, st.Now(), len(p.OnlineIDs()))
}
