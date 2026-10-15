package adminapi

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// maxJSONBody bounds panel request bodies handled by the auth endpoints.
const maxJSONBody = 64 << 10

// UserDTO is the API representation of a user (components.schemas.User).
type UserDTO struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	Email        *string    `json:"email"`
	DisplayName  string     `json:"display_name"`
	AvatarURL    string     `json:"avatar_url"`
	Note         string     `json:"note"`
	Role         store.Role `json:"role"`
	Status       string     `json:"status"`
	UserGroupID  *int64     `json:"user_group_id"`
	HasPassword  bool       `json:"has_password"`
	TOTPEnabled  bool       `json:"totp_enabled"`
	TOTPRequired bool       `json:"totp_required"`
	LastLoginAt  *time.Time `json:"last_login_at"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// NewUserDTO converts a user row.
func NewUserDTO(u *store.User, totpEnabled bool) UserDTO {
	d := UserDTO{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, AvatarURL: u.AvatarURL,
		Note: u.Note, Role: u.Role, Status: string(u.Status), UserGroupID: u.UserGroupID,
		HasPassword: u.HasPassword(), TOTPEnabled: totpEnabled, TOTPRequired: u.TOTPRequired,
		LastLoginAt: u.LastLoginAt, CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
	if u.Email != "" {
		e := u.Email
		d.Email = &e
	}
	return d
}

// meDTO is components.schemas.Me.
type meDTO struct {
	User           UserDTO `json:"user"`
	CSRFToken      string  `json:"csrf_token"`
	MustEnrollTOTP bool    `json:"must_enroll_totp"`
}

// sessionDTO is components.schemas.Session.
type sessionDTO struct {
	ID               int64      `json:"id"`
	Kind             string     `json:"kind"`
	DeviceRustdeskID *string    `json:"device_rustdesk_id"`
	DeviceUUID       *string    `json:"device_uuid"`
	ClientPlatform   *string    `json:"client_platform"`
	UserAgent        *string    `json:"user_agent"`
	IP               *string    `json:"ip"`
	CreatedAt        time.Time  `json:"created_at"`
	LastUsedAt       *time.Time `json:"last_used_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	Current          bool       `json:"current"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func newSessionDTO(t *store.AuthToken, currentID int64) sessionDTO {
	exp := t.ExpiresAt
	if t.AbsoluteExpiresAt != nil && t.AbsoluteExpiresAt.Before(exp) {
		exp = *t.AbsoluteExpiresAt
	}
	if t.JWTExpiresAt != nil && t.JWTExpiresAt.Before(exp) {
		exp = *t.JWTExpiresAt
	}
	return sessionDTO{ID: t.ID, Kind: string(t.Kind), DeviceRustdeskID: strPtr(t.DeviceRustdeskID),
		DeviceUUID: strPtr(t.DeviceUUID), ClientPlatform: strPtr(t.ClientPlatform), UserAgent: strPtr(t.UserAgent),
		IP: strPtr(t.IP), CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, ExpiresAt: exp, Current: t.ID == currentID}
}

// decodeJSON reads a bounded JSON body into dst, rejecting unknown fields.
// An empty body decodes to the zero value. It writes the error response and
// returns false on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		httpx.WriteError(w, http.StatusBadRequest, "validation_failed", "invalid JSON body")
		return false
	}
	return true
}

func badRequest(w http.ResponseWriter, msg string) {
	httpx.WriteError(w, http.StatusBadRequest, "validation_failed", msg)
}

func internalError(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusInternalServerError, "internal", "internal server error")
}

func rateLimited(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many attempts, try again later")
}

func clipStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// idParam parses a positive integer URL parameter.
func idParam(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil && id > 0
}
