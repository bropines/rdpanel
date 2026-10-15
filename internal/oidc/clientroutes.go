package oidc

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/bropines/rustdesk-panel/internal/crypto"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// Exact wire strings of the RustDesk client contract (docs/research/client-api.md §9).
const (
	// PendingError keeps the client polling (substring match on the client side).
	PendingError = "No authed oidc is found"
	// InvalidCodeError ends the attempt (unknown, expired, used or foreign code).
	InvalidCodeError = "Invalid or expired code"
)

// maxClientBody bounds client request bodies.
const maxClientBody = 64 << 10

// RouteDeps are the collaborators of the client OIDC routes.
type RouteDeps struct {
	Service *Service
	Log     *slog.Logger
}

// Register mounts the RustDesk client OIDC endpoints on r (already scoped to
// /api). Stream A's clientapi must not register these paths:
//
//	GET  /login-options
//	POST /oidc/auth
//	GET  /oidc/auth-query
//	GET  /oidc/callback   (+ legacy alias /oauth/callback)
func Register(r chi.Router, d RouteDeps) {
	h := &clientHandlers{s: d.Service, log: d.Log}
	if h.log == nil {
		h.log = d.Service.log
	}
	r.Get("/login-options", h.loginOptions)
	r.Post("/oidc/auth", h.auth)
	r.Get("/oidc/auth-query", h.authQuery)
	r.Get("/oidc/callback", d.Service.HandleCallback)
	r.Get("/oauth/callback", d.Service.HandleCallback)
}

type clientHandlers struct {
	s   *Service
	log *slog.Logger
}

// writeClientJSON writes a client-API JSON body (no panel error envelope).
func writeClientJSON(w http.ResponseWriter, status int, v any) {
	httpx.WriteJSON(w, status, v)
}

func clientError(w http.ResponseWriter, status int, msg string) {
	writeClientJSON(w, status, map[string]string{"error": msg})
}

// LoginOptions returns the login-options array: one "common-oidc/<json>" item
// (wins in the client) plus one "oidc/<slug>" per enabled provider.
func (s *Service) LoginOptions(ctx context.Context) ([]string, error) {
	provs, err := s.st.IdentityProviders.List(ctx, true)
	if err != nil {
		return nil, err
	}
	type item struct {
		Name string `json:"name"`
	}
	items := []item{}
	ops := []string{}
	for _, p := range provs {
		if !SupportedKind(p.Kind) {
			continue
		}
		items = append(items, item{Name: p.Slug})
		ops = append(ops, "oidc/"+p.Slug)
	}
	out := []string{}
	if len(items) == 0 {
		return out, nil
	}
	common, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	out = append(out, "common-oidc/"+string(common))
	return append(out, ops...), nil
}

func (h *clientHandlers) loginOptions(w http.ResponseWriter, r *http.Request) {
	opts, err := h.s.LoginOptions(r.Context())
	if err != nil {
		h.log.ErrorContext(r.Context(), "login-options", slog.Any("err", err))
		clientError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeClientJSON(w, http.StatusOK, opts)
}

type authRequest struct {
	Op         string `json:"op"`
	ID         string `json:"id"`
	UUID       string `json:"uuid"`
	DeviceInfo struct {
		OS   string `json:"os"`
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"deviceInfo"`
	// APIDomain (client >= 1.5.0) is ignored: redirect URIs come only from
	// the configured public URL.
	APIDomain string `json:"apiDomain"`
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (h *clientHandlers) auth(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxClientBody)).Decode(&req); err != nil {
		clientError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if req.Op == "" || req.ID == "" || req.UUID == "" {
		clientError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	dev := ClientDevice{ID: clip(req.ID, 128), UUID: clip(req.UUID, 256), Name: clip(req.DeviceInfo.Name, 256),
		OS: clip(req.DeviceInfo.OS, 64), Type: clip(req.DeviceInfo.Type, 64)}
	code, url, err := h.s.BeginClient(r.Context(), req.Op, dev)
	switch {
	case errors.Is(err, ErrUnknownProvider), errors.Is(err, ErrNotConfigured):
		clientError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, ErrProviderUnavailable):
		clientError(w, http.StatusBadGateway, err.Error())
		return
	case err != nil:
		h.log.ErrorContext(r.Context(), "oidc/auth", slog.Any("err", err))
		clientError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeClientJSON(w, http.StatusOK, map[string]string{"code": code, "url": url})
}

func (h *clientHandlers) authQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	body := h.s.Poll(r.Context(), q.Get("code"), q.Get("id"), q.Get("uuid"), r)
	writeClientJSON(w, http.StatusOK, body)
}

// UserInfo is the required "info" object of the client UserPayload.
type UserInfo struct {
	EmailVerification      bool `json:"email_verification"`
	EmailAlarmNotification bool `json:"email_alarm_notification"`
}

// UserPayload is the user object of the client auth body. The Rust client
// requires "name" and "info" and accepts status only in {0, 1, -1}.
type UserPayload struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"display_name"`
	Avatar      string   `json:"avatar"`
	Email       string   `json:"email"`
	Note        string   `json:"note"`
	IsAdmin     bool     `json:"is_admin"`
	Status      int      `json:"status"`
	Info        UserInfo `json:"info"`
}

// AuthBody is the auth-query success body.
type AuthBody struct {
	AccessToken string      `json:"access_token"`
	Type        string      `json:"type"`
	User        UserPayload `json:"user"`
}

// ClientUserStatus maps a user status to the client enum (1 normal, 0 disabled).
func ClientUserStatus(u *store.User) int {
	if u.IsActive() {
		return 1
	}
	return 0
}

// NewUserPayload builds the client user object.
func NewUserPayload(u *store.User) UserPayload {
	return UserPayload{Name: u.Username, DisplayName: u.DisplayName, Avatar: u.AvatarURL, Email: u.Email,
		Note: u.Note, IsAdmin: u.IsAdmin(), Status: ClientUserStatus(u), Info: UserInfo{}}
}

// Poll implements GET /api/oidc/auth-query. It always returns a JSON body:
// {"error":"No authed oidc is found"} while pending, {"error": msg} on
// failure, or an AuthBody once (the code is consumed on the first answer
// that is not "pending"). The poll must present the same device id and uuid
// that started the flow; otherwise the code is not touched.
func (s *Service) Poll(ctx context.Context, code, id, uuid string, r *http.Request) any {
	if code == "" || len(code) > 256 {
		return map[string]string{"error": InvalidCodeError}
	}
	st, err := s.st.OAuthStates.GetActive(ctx, crypto.HashToken(code))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.ErrorContext(ctx, "auth-query: load state", slog.Any("err", err))
		}
		return map[string]string{"error": InvalidCodeError}
	}
	idOK := crypto.EqualString(st.DeviceRustdeskID, id)
	uuidOK := crypto.EqualString(st.DeviceUUID, uuid)
	if st.Flow != store.OAuthFlowClient || st.Action != store.OAuthLogin || !idOK || !uuidOK {
		return map[string]string{"error": InvalidCodeError}
	}
	if st.ResultUserID == nil && (st.ResultError == "" || st.ResultError == store.OAuthStateProcessing) {
		return map[string]string{"error": PendingError}
	}
	if err := s.st.OAuthStates.Consume(ctx, st.ID); err != nil {
		return map[string]string{"error": InvalidCodeError}
	}
	if st.ResultUserID == nil {
		return map[string]string{"error": st.ResultError}
	}
	user, err := s.st.Users.GetByID(ctx, *st.ResultUserID)
	if err != nil || !user.IsActive() {
		return map[string]string{"error": errDisabled.Msg}
	}
	tok, tokenID, err := s.issueClientToken(ctx, user, st, r)
	if err != nil {
		s.log.ErrorContext(ctx, "auth-query: issue token", slog.Any("err", err))
		return map[string]string{"error": "internal error"}
	}
	s.recordEvent(ctx, st, &user.ID, &tokenID, user.Username, true, "", r)
	if err := s.st.Users.TouchLastLogin(ctx, user.ID); err != nil {
		s.log.WarnContext(ctx, "auth-query: touch last login", slog.Any("err", err))
	}
	if s.onClientLogin != nil {
		s.onClientLogin(ctx, user, tokenID, st)
	}
	return AuthBody{AccessToken: tok, Type: "access_token", User: NewUserPayload(user)}
}

// issueClientToken mints a client JWT and stores its digest in auth_tokens,
// bound to the device that started the flow.
func (s *Service) issueClientToken(ctx context.Context, user *store.User, st *store.OAuthState, r *http.Request) (string, int64, error) {
	now := s.now()
	jwtExp := now.Add(s.jwtLifetime)
	tok, err := s.jwt.Mint(uint32(user.ID), jwtExp) //nolint:gosec // users.id is CHECKed to fit u32
	if err != nil {
		return "", 0, err
	}
	jwtExp = jwtExp.Truncate(1e9)
	row := &store.AuthToken{UserID: user.ID, Kind: store.TokenClient, TokenSHA256: crypto.HashToken(tok),
		JWTExpiresAt: &jwtExp, ExpiresAt: now.Add(s.tokenLifetime), DeviceRustdeskID: st.DeviceRustdeskID,
		DeviceUUID: st.DeviceUUID, ClientPlatform: st.DeviceOS}
	if r != nil {
		row.IP, row.UserAgent = httpx.ClientIPString(r), clip(r.UserAgent(), 512)
	}
	if err := s.st.AuthTokens.Create(ctx, row); err != nil {
		return "", 0, err
	}
	return tok, row.ID, nil
}
