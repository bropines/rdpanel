package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/clientdl"
	"github.com/bropines/rdpanel/internal/httpx"
)

// ClientDownloads looks up RustDesk releases and keeps verified installer
// copies (*clientdl.Service).
type ClientDownloads interface {
	Release(ctx context.Context) clientdl.Result
	Open(ctx context.Context, id string) (*os.File, clientdl.Asset, error)
}

// clientDownloadsDTO is components.schemas.ClientDownloads.
type clientDownloadsDTO struct {
	Enabled         bool             `json:"enabled"`
	Version         string           `json:"version,omitempty"`
	ReleaseURL      string           `json:"release_url,omitempty"`
	PublishedAt     *time.Time       `json:"published_at,omitempty"`
	Error           string           `json:"error,omitempty"`
	IDServer        string           `json:"id_server"`
	RelayServer     string           `json:"relay_server"`
	APIServer       string           `json:"api_server"`
	PublicKey       string           `json:"public_key"`
	ConfigString    string           `json:"config_string"`
	WindowsFileName string           `json:"windows_file_name,omitempty"`
	Assets          []clientdl.Asset `json:"assets"`
}

// windowsInstallers maps the {arch} of /clients/windows/{arch} to assets.
var windowsInstallers = map[string]string{"x64": "windows-x64", "arm64": "windows-arm64"}

func (c *b2bCore) registerClients(r chi.Router) {
	r.With(RequireUser).Get("/clients", c.handleClients)
	r.With(RequireUser).Get("/clients/windows/{arch}", c.handleClientWindows)
}

func (c *b2bCore) handleClients(w http.ResponseWriter, r *http.Request) {
	cs := c.clientServerSettings(r)
	cfg, _ := EncodeRustdeskConfig(cs.Host, cs.Relay, cs.API, cs.Key)
	out := clientDownloadsDTO{
		IDServer: cs.Host, RelayServer: cs.Relay, APIServer: cs.API, PublicKey: cs.Key,
		ConfigString: cfg, Assets: []clientdl.Asset{},
	}
	if c.d.Clients == nil {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	out.Enabled = true
	if name, err := clientdl.WindowsFileName(cs); err == nil && cs.Key != "" {
		out.WindowsFileName = name
	}
	res := c.d.Clients.Release(r.Context())
	out.Error = res.Err
	if rel := res.Release; rel != nil {
		out.Version, out.ReleaseURL, out.Assets = rel.Version, rel.URL, rel.Assets
		if !rel.PublishedAt.IsZero() {
			t := rel.PublishedAt
			out.PublishedAt = &t
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleClientWindows serves the official Windows installer under a file
// name carrying this server's settings, which the client applies on start.
func (c *b2bCore) handleClientWindows(w http.ResponseWriter, r *http.Request) {
	id, ok := windowsInstallers[chi.URLParam(r, "arch")]
	if !ok || c.d.Clients == nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "no such client download")
		return
	}
	cs := c.clientServerSettings(r)
	if cs.Key == "" {
		httpx.WriteError(w, http.StatusConflict, "no_server_key", "the server public key is not available yet")
		return
	}
	name, err := clientdl.WindowsFileName(cs)
	if err != nil {
		httpx.WriteError(w, http.StatusConflict, "file_name", err.Error())
		return
	}
	f, _, err := c.d.Clients.Open(r.Context(), id)
	if err != nil {
		c.log.Warn("client download failed", "asset", id, "err", err)
		httpx.WriteError(w, http.StatusBadGateway, "client_unavailable", "the RustDesk installer could not be fetched from GitHub; try again later")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "internal", "cannot read the stored installer")
		return
	}
	w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeContent(w, r, "", st.ModTime(), f)
}
