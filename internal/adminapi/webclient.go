package adminapi

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/store"
)

// webClientTokenLifetime bounds the hbbs login token handed to the browser:
// it is only presented once, in the punch-hole request that starts a session.
const webClientTokenLifetime = 10 * time.Minute

// webClientConfigDTO is components.schemas.WebClientConfig.
type webClientConfigDTO struct {
	Enabled        bool       `json:"enabled"`
	RendezvousURL  string     `json:"rendezvous_url"`
	RelayURL       string     `json:"relay_url"`
	PublicKey      string     `json:"public_key"`
	Token          *string    `json:"token"`
	TokenExpiresAt *time.Time `json:"token_expires_at"`
	DisplayName    string     `json:"display_name"`
	MyID           string     `json:"my_id"`
	// Servers are other servers the admin offers in the web client.
	Servers []webClientServerDTO `json:"servers"`
	// CustomServers: users may enter their own servers.
	CustomServers bool `json:"custom_servers"`
}

// webClientServerDTO is components.schemas.WebClientServer.
type webClientServerDTO struct {
	Name     string `json:"name"`
	IDURL    string `json:"id_url"`
	RelayURL string `json:"relay_url"`
	Key      string `json:"key"`
}

func (c *b2bCore) registerWebClient(r chi.Router) {
	r.With(RequireUser).Get("/webclient/config", c.handleWebClientConfig)
	r.With(RequireUser).Get("/webclient/credentials/{peer}", c.handleWebClientCredentials)
	r.With(RequireUser).Put("/webclient/credentials/{peer}", c.handleWebClientRemember)
	r.With(RequireUser).Delete("/webclient/credentials/{peer}", c.handleWebClientForget)
	r.With(RequireUser).Get("/webclient/shells/{peer}", c.handleWebClientShells)
	r.With(RequireUser).Put("/webclient/shells/{peer}", c.handleWebClientShellSave)
	r.With(RequireUser).Post("/webclient/shells/{peer}/forget", c.handleWebClientShellForget)
}

// webClientRememberDTO is components.schemas.WebClientRemember.
type webClientRememberDTO struct {
	Hash string `json:"hash"`
}

func (c *b2bCore) webClientAllowed(w http.ResponseWriter, r *http.Request) (string, bool) {
	user := CurrentUser(r.Context())
	if cfg := c.d.Config; cfg != nil && (!cfg.WebClient.Enabled || (cfg.WebClient.AdminOnly && !user.IsAdmin())) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "web client is not available")
		return "", false
	}
	peer := strings.Join(strings.Fields(chi.URLParam(r, "peer")), "")
	if peer == "" || len(peer) > 64 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_peer", "invalid device id")
		return "", false
	}
	return peer, true
}

// handleWebClientRemember stores the device's salted password hash in the
// caller's personal address book, where signed-in desktop clients look for
// it too (the address book's "remember password").
func (c *b2bCore) handleWebClientRemember(w http.ResponseWriter, r *http.Request) {
	peer, ok := c.webClientAllowed(w, r)
	if !ok {
		return
	}
	var in webClientRememberDTO
	if !decodeBody(w, r, &in, 1<<12) {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(in.Hash)
	if err != nil || len(raw) != 32 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_hash", "hash must be base64 of 32 bytes")
		return
	}
	ctx := r.Context()
	user := CurrentUser(ctx)
	book, err := c.d.Store.AddressBooks.EnsurePersonal(ctx, user.ID)
	if err != nil {
		internalError(w)
		return
	}
	hash := in.Hash
	if _, err := c.d.Store.AddressBooks.UpsertEntry(ctx, book.ID, peer, store.ABEntryPatch{Hash: &hash}); err != nil {
		internalError(w)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleWebClientForget clears a remembered hash (personal book only).
func (c *b2bCore) handleWebClientForget(w http.ResponseWriter, r *http.Request) {
	peer, ok := c.webClientAllowed(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	user := CurrentUser(ctx)
	book, err := c.d.Store.AddressBooks.GetPersonal(ctx, user.ID)
	if err == nil {
		if _, err := c.d.Store.AddressBooks.GetEntry(ctx, book.ID, peer); err == nil {
			empty := ""
			if err := c.d.Store.AddressBooks.PatchEntry(ctx, book.ID, peer, store.ABEntryPatch{Hash: &empty}); err != nil {
				internalError(w)
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// webClientCredentialsDTO is components.schemas.WebClientCredentials.
type webClientCredentialsDTO struct {
	Hash     *string `json:"hash"`
	Password *string `json:"password"`
	Book     *string `json:"book"`
}

// handleWebClientCredentials returns the saved login for a device from the
// caller's address books, exactly what a signed-in desktop client receives
// through /api/ab/peers: the personal book's remembered hash, else a shared
// book's password (the entry's, else the book's default password).
func (c *b2bCore) handleWebClientCredentials(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := CurrentUser(ctx)
	if cfg := c.d.Config; cfg != nil && (!cfg.WebClient.Enabled || (cfg.WebClient.AdminOnly && !user.IsAdmin())) {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "web client is not available")
		return
	}
	peer := strings.Join(strings.Fields(chi.URLParam(r, "peer")), "")
	out := webClientCredentialsDTO{}
	if peer == "" || len(peer) > 64 {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	books := c.d.Store.AddressBooks
	if personal, err := books.GetPersonal(ctx, user.ID); err == nil {
		if e, err := books.GetEntry(ctx, personal.ID, peer); err == nil {
			if e.Hash != "" {
				out.Hash = &e.Hash
			} else if e.Password != "" {
				out.Password = &e.Password
			}
		}
	}
	if out.Hash == nil && out.Password == nil {
		shared, _, err := books.ListShared(ctx, user.ID, 1000, 0)
		if err != nil {
			c.d.Log.WarnContext(ctx, "web client credentials", slog.Any("err", err))
		}
		for _, b := range shared {
			e, err := books.GetEntry(ctx, b.ID, peer)
			if err != nil {
				continue
			}
			pw := e.Password
			if pw == "" {
				pw = b.DefaultPassword
			}
			if e.Hash != "" || pw != "" {
				name := b.Name
				out.Book = &name
				if e.Hash != "" {
					out.Hash = &e.Hash
				} else {
					out.Password = &pw
				}
				break
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, out)
}

// serverPublicKey returns the hbbs public key (id_ed25519.pub), or "".
func (c *b2bCore) serverPublicKey() string {
	if c.d.Config == nil || c.d.Config.HBBS.PublicKeyFile == "" {
		return ""
	}
	raw, err := os.ReadFile(c.d.Config.HBBS.PublicKeyFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func (c *b2bCore) handleWebClientConfig(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := CurrentUser(ctx)
	cfg := c.d.Config
	enabled := cfg == nil || (cfg.WebClient.Enabled && (!cfg.WebClient.AdminOnly || user.IsAdmin()))
	if !enabled {
		httpx.WriteJSON(w, http.StatusOK, webClientConfigDTO{Enabled: false})
		return
	}
	out := webClientConfigDTO{
		Enabled:     true,
		PublicKey:   c.serverPublicKey(),
		DisplayName: user.Username,
	}
	if user.DisplayName != "" {
		out.DisplayName = user.DisplayName
	}
	out.Servers = []webClientServerDTO{}
	if cfg != nil {
		out.RendezvousURL = cfg.WebClient.IDURL
		out.RelayURL = cfg.WebClient.RelayURL
		out.CustomServers = cfg.WebClient.CustomServers
		for _, s := range cfg.WebClient.Servers {
			out.Servers = append(out.Servers, webClientServerDTO{Name: s.Name, IDURL: s.IDURL, RelayURL: s.RelayURL, Key: strings.TrimSpace(s.Key)})
		}
	}
	if c.d.WebSessions != nil {
		if id, err := c.d.WebSessions.Issue(user.ID); err == nil {
			out.MyID = id
		}
	}
	// A login token lets the browser pass hbbs' MUST_LOGIN check, like a
	// signed-in desktop client does.
	if c.d.JWT != nil && user.ID > 0 && user.ID <= int64(^uint32(0)) {
		exp := c.d.Store.Now().Add(webClientTokenLifetime)
		if tok, err := c.d.JWT.Mint(uint32(user.ID), exp); err == nil {
			out.Token, out.TokenExpiresAt = &tok, &exp
		} else {
			c.d.Log.WarnContext(ctx, "web client token", slog.Any("err", err))
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
