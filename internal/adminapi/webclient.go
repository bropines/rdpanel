package adminapi

import (
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rdpanel/internal/httpx"
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
}

func (c *b2bCore) registerWebClient(r chi.Router) {
	r.With(RequireUser).Get("/webclient/config", c.handleWebClientConfig)
	r.With(RequireUser).Get("/webclient/credentials/{peer}", c.handleWebClientCredentials)
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
	if cfg != nil {
		out.RendezvousURL = cfg.WebClient.IDURL
		out.RelayURL = cfg.WebClient.RelayURL
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
