package adminapi

import (
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

// webClientShellDTO is components.schemas.WebClientShell: terminal sessions a
// device keeps running for the caller (reattachable from any browser).
type webClientShellDTO struct {
	ServiceID string    `json:"service_id"`
	Hostname  string    `json:"hostname"`
	Tabs      int       `json:"tabs"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// webClientShellSaveDTO is components.schemas.WebClientShellSave.
type webClientShellSaveDTO struct {
	ServiceID string `json:"service_id"`
	Hostname  string `json:"hostname"`
	Tabs      int    `json:"tabs"`
}

func validServiceID(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for _, r := range s {
		if r > unicode.MaxASCII || !unicode.IsPrint(r) || r == ' ' {
			return false
		}
	}
	return true
}

func (c *b2bCore) handleWebClientShells(w http.ResponseWriter, r *http.Request) {
	peer, ok := c.webClientAllowed(w, r)
	if !ok {
		return
	}
	list, err := c.d.Store.TerminalShells.List(r.Context(), CurrentUser(r.Context()).ID, peer)
	if err != nil {
		internalError(w)
		return
	}
	out := make([]webClientShellDTO, 0, len(list))
	for _, s := range list {
		out = append(out, webClientShellDTO{ServiceID: s.ServiceID, Hostname: s.Hostname, Tabs: s.Tabs, CreatedAt: s.CreatedAt.UTC(), UpdatedAt: s.UpdatedAt.UTC()})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (c *b2bCore) handleWebClientShellSave(w http.ResponseWriter, r *http.Request) {
	peer, ok := c.webClientAllowed(w, r)
	if !ok {
		return
	}
	var in webClientShellSaveDTO
	if !decodeBody(w, r, &in, 1<<12) {
		return
	}
	in.Hostname = strings.TrimSpace(in.Hostname)
	if !validServiceID(in.ServiceID) || in.Tabs < 0 || in.Tabs > 64 || len(in.Hostname) > 255 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_shell", "invalid terminal session")
		return
	}
	sh := &store.TerminalShell{UserID: CurrentUser(r.Context()).ID, RustdeskID: peer, ServiceID: in.ServiceID, Hostname: in.Hostname, Tabs: in.Tabs}
	if err := c.d.Store.TerminalShells.Save(r.Context(), sh); err != nil {
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *b2bCore) handleWebClientShellForget(w http.ResponseWriter, r *http.Request) {
	peer, ok := c.webClientAllowed(w, r)
	if !ok {
		return
	}
	var in struct {
		ServiceID string `json:"service_id"`
	}
	if !decodeBody(w, r, &in, 1<<12) {
		return
	}
	if !validServiceID(in.ServiceID) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_shell", "invalid terminal session")
		return
	}
	if err := c.d.Store.TerminalShells.Delete(r.Context(), CurrentUser(r.Context()).ID, peer, in.ServiceID); err != nil {
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
