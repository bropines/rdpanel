package clientapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/metrics"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// tfaChallengeTTL bounds the time between the password and TOTP steps.
const tfaChallengeTTL = 5 * time.Minute

// tfaChallengeAD binds sealed login challenges to their purpose.
const tfaChallengeAD = "clientapi.login_tfa"

type loginRequest struct {
	Username         string `json:"username"`
	Password         string `json:"password"`
	ID               string `json:"id"`
	UUID             string `json:"uuid"`
	AutoLogin        bool   `json:"autoLogin"`
	Type             string `json:"type"`
	VerificationCode string `json:"verificationCode"`
	TfaCode          string `json:"tfaCode"`
	Secret           string `json:"secret"`
	DeviceInfo       struct {
		OS   string `json:"os"`
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"deviceInfo"`
}

type tfaChallenge struct {
	UserID   int64  `json:"u"`
	Username string `json:"n"`
	Expires  int64  `json:"e"`
}

var (
	dummyHashOnce sync.Once
	dummyHash     string
)

// login implements POST /api/login: password, then (when the user has 2FA)
// a TOTP step answered as {"type":"email_check","tfa_type":"tfa_check"}.
func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := decodeBody(w, r, &req, maxBody); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request")
		return
	}
	ip := httpx.ClientIPString(r)
	ipKey := "clientapi.login.ip:" + ip
	userKey := "clientapi.login.user:" + strings.ToLower(strings.TrimSpace(req.Username))
	// factor is the step this request performs (metrics label).
	factor := metrics.MethodPassword
	if req.Type == "email_code" || req.Type == "tfa_code" {
		factor = metrics.MethodTOTP
	}
	for _, k := range []string{ipKey, userKey} {
		if ok, wait := h.Limiter.Allow(k); !ok {
			h.Telemetry.Login(metrics.SurfaceClient, factor, metrics.ResultRateLimited)
			secs := int(math.Ceil(wait.Seconds()))
			w.Header().Set("Retry-After", fmt.Sprint(secs))
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("Too many failed attempts, try again in %d s", secs))
			return
		}
	}
	lc := LoginContextFromRequest(r, store.LoginMethodPassword)
	lc.DeviceID, lc.DeviceUUID = req.ID, req.UUID
	lc.Platform, lc.ClientKind = req.DeviceInfo.OS, req.DeviceInfo.Type

	fail := func(u *store.User, reason, msg string) {
		h.Limiter.Failure(ipKey)
		h.Limiter.Failure(userKey)
		h.Telemetry.Login(metrics.SurfaceClient, factor, metrics.ResultFailure)
		ev := &store.LoginEvent{UsernameAttempted: clip(req.Username, 128), Method: store.LoginMethodPassword,
			Result: store.LoginFailure, FailureReason: reason, ClientKind: clip(lc.ClientKind, 64),
			DeviceRustdeskID: clip(lc.DeviceID, maxIDLen), DeviceUUID: clip(lc.DeviceUUID, 256),
			Platform: clip(lc.Platform, 64), IP: lc.IP, UserAgent: lc.UserAgent}
		if u != nil {
			ev.UserID = &u.ID
		}
		if err := h.st.LoginEvents.Record(r.Context(), ev); err != nil {
			h.log.WarnContext(r.Context(), "record login failure", slog.Any("err", err))
		}
		writeError(w, http.StatusBadRequest, msg)
	}

	var u *store.User
	switch req.Type {
	case "", "account":
		var (
			ok  bool
			err error
		)
		if u, ok, err = h.checkPassword(r, req.Username, req.Password); err != nil {
			h.log.ErrorContext(r.Context(), "load user", slog.Any("err", err))
			internalError(w)
			return
		}
		if !ok {
			fail(u, "bad_credentials", "Invalid username or password")
			return
		}
		if !u.IsActive() {
			fail(u, "disabled", "User is disabled")
			return
		}
		if allowed, err := h.st.Settings.PasswordLoginAllowed(r.Context(), u); err != nil {
			internalError(w)
			return
		} else if !allowed {
			fail(u, "password_login_disabled", "Password login is disabled, sign in with an identity provider")
			return
		}
		enrolled, err := h.TOTP.Enrolled(r.Context(), u.ID)
		if err != nil {
			h.log.ErrorContext(r.Context(), "totp enrolled", slog.Any("err", err))
			internalError(w)
			return
		}
		if enrolled {
			secret, err := h.sealChallenge(u)
			if err != nil {
				h.log.ErrorContext(r.Context(), "seal tfa challenge", slog.Any("err", err))
				internalError(w)
				return
			}
			writeJSON(w, http.StatusOK, AuthBody{Type: "email_check", TfaType: "tfa_check", Secret: secret,
				User: &UserPayload{Name: u.Username, Status: 1}})
			return
		}
	case "email_code", "tfa_code":
		ch, ok := h.openChallenge(req.Secret)
		if !ok || !strings.EqualFold(ch.Username, strings.TrimSpace(req.Username)) {
			fail(nil, "bad_challenge", "Verification expired, please log in again")
			return
		}
		var err error
		if u, err = h.st.Users.GetByID(r.Context(), ch.UserID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				fail(nil, "bad_challenge", "Verification expired, please log in again")
				return
			}
			internalError(w)
			return
		}
		if !u.IsActive() {
			fail(u, "disabled", "User is disabled")
			return
		}
		code := req.TfaCode
		if code == "" {
			code = req.VerificationCode
		}
		valid, err := h.TOTP.Verify(r.Context(), u.ID, code)
		if err != nil {
			h.log.ErrorContext(r.Context(), "totp verify", slog.Any("err", err))
			internalError(w)
			return
		}
		if !valid {
			fail(u, "bad_tfa_code", "Invalid verification code")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "Unsupported login type")
		return
	}

	token, err := h.tokens.Issue(r.Context(), u, lc)
	if err != nil {
		h.log.ErrorContext(r.Context(), "issue client token", slog.Any("err", err))
		internalError(w)
		return
	}
	h.Limiter.Success(ipKey)
	h.Limiter.Success(userKey)
	h.Telemetry.Login(metrics.SurfaceClient, factor, metrics.ResultSuccess)
	p := NewUserPayload(u)
	writeJSON(w, http.StatusOK, AuthBody{Type: "access_token", AccessToken: token, User: &p})
}

// checkPassword returns the user and whether password matches (the user is
// nil for unknown usernames). err is set only for storage failures.
func (h *handler) checkPassword(r *http.Request, username, password string) (*store.User, bool, error) {
	username = strings.TrimSpace(username)
	var u *store.User
	if username != "" {
		var err error
		u, err = h.st.Users.GetByUsername(r.Context(), username)
		if errors.Is(err, store.ErrNotFound) {
			u = nil
		} else if err != nil {
			return nil, false, err
		}
	}
	if u == nil || !u.HasPassword() {
		// Spend comparable time so unknown usernames are not distinguishable.
		dummyHashOnce.Do(func() { dummyHash, _, _ = h.Hasher.Hash("rdpanel-dummy-password") })
		_ = h.Hasher.Verify(dummyHash, password)
		return u, false, nil
	}
	if !h.Hasher.Verify(u.PasswordHash, password) {
		return u, false, nil
	}
	if h.Hasher.NeedsRehash(u.PasswordHash) {
		if hash, algo, err := h.Hasher.Hash(password); err == nil {
			if err := h.st.Users.RehashPassword(r.Context(), u.ID, hash, string(algo)); err != nil {
				h.log.WarnContext(r.Context(), "rehash password", slog.Any("err", err))
			}
		}
	}
	return u, true, nil
}

func (h *handler) sealChallenge(u *store.User) (string, error) {
	if h.AEAD == nil {
		return "", errors.New("clientapi: no AEAD configured")
	}
	b, _ := json.Marshal(tfaChallenge{UserID: u.ID, Username: u.Username, Expires: h.Now().Add(tfaChallengeTTL).Unix()})
	ct, err := h.AEAD.Seal(b, []byte(tfaChallengeAD))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(ct), nil
}

func (h *handler) openChallenge(secret string) (tfaChallenge, bool) {
	var ch tfaChallenge
	if h.AEAD == nil || secret == "" || len(secret) > 1024 {
		return ch, false
	}
	ct, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return ch, false
	}
	pt, err := h.AEAD.Open(ct, []byte(tfaChallengeAD))
	if err != nil || json.Unmarshal(pt, &ch) != nil {
		return ch, false
	}
	return ch, ch.UserID > 0 && h.Now().Unix() <= ch.Expires
}

// currentUser implements POST /api/currentUser (body {id, uuid} is ignored).
func (h *handler) currentUser(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, NewUserPayload(sessionFrom(r.Context()).user))
}

// logout implements POST /api/logout: revoke the token and unbind the device
// from the user. The client ignores the response (2 s timeout); success is
// 200 with an empty body, also for already-invalid tokens.
func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		UUID string `json:"uuid"`
	}
	_ = decodeBody(w, r, &req, maxBody)
	s, err := h.authenticate(r, false)
	if err != nil {
		if !errors.Is(err, errUnauthorized) {
			h.log.ErrorContext(r.Context(), "logout auth", slog.Any("err", err))
			internalError(w)
			return
		}
		writeEmpty(w)
		return
	}
	devID, devUUID := s.token.DeviceRustdeskID, s.token.DeviceUUID
	if devID == "" {
		devID, devUUID = req.ID, req.UUID
	}
	err = h.st.Tx(r.Context(), func(tx *store.Store) error {
		if err := tx.AuthTokens.Revoke(r.Context(), s.token.ID, "logout"); err != nil {
			return err
		}
		return tx.Devices.UnbindOwner(r.Context(), devID, devUUID, s.user.ID)
	})
	if err != nil {
		h.log.ErrorContext(r.Context(), "logout", slog.Any("err", err))
		internalError(w)
		return
	}
	writeEmpty(w)
}
