package adminapi

import (
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/buildinfo"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/updatecheck"
)

// AppName is the product name shown on the About page.
const AppName = "RD Panel"

// aboutHBBDTO is components.schemas.AboutHbb.
type aboutHBBDTO struct {
	Repo *string `json:"repo"`
	Ref  *string `json:"ref"`
}

// aboutUpdateDTO is components.schemas.UpdateCheck.
type aboutUpdateDTO struct {
	Status      string     `json:"status"`
	Latest      *string    `json:"latest"`
	LatestURL   *string    `json:"latest_url"`
	PublishedAt *time.Time `json:"published_at"`
	CheckedAt   *time.Time `json:"checked_at"`
	Error       *string    `json:"error"`
}

// aboutServerDTO is components.schemas.AboutServer (admins only).
type aboutServerDTO struct {
	StartedAt     time.Time       `json:"started_at"`
	UptimeS       int64           `json:"uptime_s"`
	SchemaVersion *int64          `json:"schema_version"`
	SchemaLatest  *int64          `json:"schema_latest"`
	OS            string          `json:"os"`
	Arch          string          `json:"arch"`
	Health        serverHealthDTO `json:"health"`
}

// aboutDTO is components.schemas.About.
type aboutDTO struct {
	Name    string          `json:"name"`
	Version versionDTO      `json:"version"`
	HBB     aboutHBBDTO     `json:"hbb"`
	Update  aboutUpdateDTO  `json:"update"`
	Server  *aboutServerDTO `json:"server"`
}

func (c *b2bCore) registerAbout(r chi.Router) {
	started := c.d.StartedAt
	if started.IsZero() {
		started = c.d.Store.Now()
	}
	r.With(RequireUser).Get("/about", func(w http.ResponseWriter, r *http.Request) { c.handleAbout(w, r, started) })
}

// updateStatus runs the (cached) release check and compares it with version.
// Errors are only spelled out for admins.
func (c *b2bCore) updateStatus(r *http.Request, version string, admin bool) aboutUpdateDTO {
	if c.d.Updates == nil {
		return aboutUpdateDTO{Status: "disabled"}
	}
	res := c.d.Updates.Latest(r.Context())
	out := aboutUpdateDTO{Status: string(updatecheck.StatusUnknown), CheckedAt: timePtr(res.CheckedAt)}
	if res.Latest != nil {
		out.Status = string(updatecheck.Compare(version, res.Latest.Version))
		out.Latest, out.LatestURL = strPtr(res.Latest.Version), strPtr(res.Latest.URL)
		out.PublishedAt = timePtr(res.Latest.PublishedAt)
	}
	if res.Err != "" {
		if out.Latest == nil {
			out.Status = "error"
		}
		if admin {
			out.Error = strPtr(res.Err)
		}
	}
	return out
}

func (c *b2bCore) handleAbout(w http.ResponseWriter, r *http.Request, started time.Time) {
	ctx := r.Context()
	admin := CurrentUser(ctx).IsAdmin()
	bi := buildinfo.Get()
	out := aboutDTO{
		Name:    AppName,
		Version: versionDTO{Panel: bi.Version, Commit: bi.Commit, BuildDate: bi.Date, Go: bi.GoVersion},
		// AGPL-3.0 §13: every user can see where the hbbs/hbbr sources are.
		HBB:    aboutHBBDTO{Repo: strPtr(bi.HBBRepo), Ref: strPtr(bi.HBBRef)},
		Update: c.updateStatus(r, bi.Version, admin),
	}
	if admin {
		now := c.d.Store.Now()
		s := &aboutServerDTO{StartedAt: started.UTC(), UptimeS: int64(now.Sub(started) / time.Second),
			OS: runtime.GOOS, Arch: runtime.GOARCH, Health: c.serverHealth(ctx)}
		if sc := c.d.Schema; sc != nil {
			if v, err := sc.SchemaVersion(ctx); err == nil {
				s.SchemaVersion = &v
			} else {
				c.log.WarnContext(ctx, "about: schema version", slog.Any("err", err))
			}
			if v, err := sc.LatestSchemaVersion(); err == nil {
				s.SchemaLatest = &v
			}
		}
		out.Server = s
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
