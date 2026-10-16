package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/bropines/rdpanel/internal/config"
	"github.com/bropines/rdpanel/internal/crypto"
	"github.com/bropines/rdpanel/internal/httpx"
	"github.com/bropines/rdpanel/internal/loginlog"
	"github.com/bropines/rdpanel/internal/metrics"
	"github.com/bropines/rdpanel/internal/store"
)

// Passkeys (WebAuthn) for panel login:
//
//   - usernameless sign-in with a discoverable credential and user
//     verification (POST /auth/passkey/{begin,finish}); it satisfies 2FA by
//     itself;
//   - second factor instead of TOTP after the password or OIDC step
//     (POST /auth/mfa/passkey/{begin,finish}, pending session);
//   - management under /me/security/passkeys and /users/{id}/passkeys.
//
// Ceremonies are stored server-side (webauthn_challenges, single use,
// passkeyCeremonyTTL) and referenced by an opaque ceremony_id that only the
// browser holds. RustDesk clients cannot do WebAuthn; they get passkeys via an
// OIDC IdP (Pocket ID, Authentik, ...).

const (
	// passkeyCeremonyTTL bounds a begin→finish round trip.
	passkeyCeremonyTTL = 5 * time.Minute
	// maxAnonymousCeremonies caps pending usernameless logins (unauthenticated
	// writes); beyond it begin answers 429.
	maxAnonymousCeremonies = 1000
	// passkeyFreshSession: a user without password and TOTP may add a passkey
	// only from a session this young (re-authentication by fresh sign-in).
	passkeyFreshSession = 10 * time.Minute
	// maxPasskeyName bounds the display name of a passkey (runes).
	maxPasskeyName = 64
	// defaultPasskeyName is used when the user leaves the name empty.
	defaultPasskeyName = "Passkey"
)

var errPasskeyClone = errors.New("passkey signature counter did not advance")

// passkeys is the WebAuthn relying party; wa is nil when passkeys are
// unavailable (no panel URL, IP-only host) and disabled says why.
type passkeys struct {
	wa       *webauthn.WebAuthn
	disabled error
}

func newPasskeys(cfg *config.Config) *passkeys {
	if cfg == nil {
		return &passkeys{disabled: config.ErrWebAuthnUnconfigured}
	}
	rpID, origins, err := cfg.WebAuthnRP()
	if err != nil {
		return &passkeys{disabled: err}
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          rpID,
		RPDisplayName: defaultTOTPIssuer,
		RPOrigins:     origins,
		// Passkeys: discoverable credentials so usernameless sign-in works.
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationPreferred,
		},
		AttestationPreference: protocol.PreferNoAttestation,
	})
	if err != nil {
		return &passkeys{disabled: err}
	}
	return &passkeys{wa: wa}
}

func (p *passkeys) available() bool { return p != nil && p.wa != nil }

// passkeyUser adapts a panel user to webauthn.User.
type passkeyUser struct {
	u      *store.User
	handle []byte
	stored []*store.WebAuthnCredential
}

func (p *passkeyUser) WebAuthnID() []byte   { return p.handle }
func (p *passkeyUser) WebAuthnName() string { return p.u.Username }
func (p *passkeyUser) WebAuthnDisplayName() string {
	if p.u.DisplayName != "" {
		return p.u.DisplayName
	}
	return p.u.Username
}

func (p *passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(p.stored))
	for _, s := range p.stored {
		out = append(out, toWebAuthnCredential(s))
	}
	return out
}

// find returns the stored passkey with WebAuthn credential id id.
func (p *passkeyUser) find(id []byte) *store.WebAuthnCredential {
	for _, s := range p.stored {
		if crypto.Equal(s.CredentialID, id) {
			return s
		}
	}
	return nil
}

func toWebAuthnCredential(s *store.WebAuthnCredential) webauthn.Credential {
	tr := make([]protocol.AuthenticatorTransport, 0, len(s.Transports))
	for _, t := range s.Transports {
		tr = append(tr, protocol.AuthenticatorTransport(t))
	}
	return webauthn.Credential{
		ID: s.CredentialID, PublicKey: s.PublicKey, AttestationType: s.AttestationType, Transport: tr,
		Flags: webauthn.CredentialFlags{UserPresent: true, UserVerified: s.UserVerified,
			BackupEligible: s.BackupEligible, BackupState: s.BackupState},
		Authenticator: webauthn.Authenticator{AAGUID: s.AAGUID, SignCount: s.SignCount, CloneWarning: s.CloneWarning,
			Attachment: protocol.AuthenticatorAttachment(s.Attachment)},
	}
}

// passkeyUserOf loads the WebAuthn view of user; create assigns a user handle
// when the user has none yet (registration).
func (c *authCore) passkeyUserOf(ctx context.Context, user *store.User, create bool) (*passkeyUser, error) {
	repo := c.d.Store.WebAuthnCredentials
	handle, err := repo.UserHandle(ctx, user.ID)
	if errors.Is(err, store.ErrNotFound) && create {
		var fresh []byte
		if fresh, err = crypto.RandomBytes(32); err == nil {
			handle, err = repo.EnsureUserHandle(ctx, user.ID, fresh)
		}
	}
	if err != nil {
		return nil, err
	}
	creds, err := repo.ListForUser(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	return &passkeyUser{u: user, handle: handle, stored: creds}, nil
}

// ---- ceremonies ----

type ceremonyDTO struct {
	CeremonyID string `json:"ceremony_id"`
	// Options is protocol.CredentialCreation ({publicKey: ...}) or
	// protocol.CredentialAssertion, ready for navigator.credentials.
	Options any `json:"options"`
}

func (c *authCore) saveCeremony(ctx context.Context, kind store.WebAuthnChallengeKind, userID, tokenID *int64,
	sd *webauthn.SessionData) (string, error) {
	plain, err := crypto.RandomToken(32)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(sd)
	if err != nil {
		return "", err
	}
	err = c.d.Store.WebAuthnChallenges.Create(ctx, &store.WebAuthnChallenge{IDSHA256: crypto.HashToken(plain),
		Kind: kind, UserID: userID, TokenID: tokenID, SessionData: data, ExpiresAt: c.d.Store.Now().Add(passkeyCeremonyTTL)})
	return plain, err
}

// takeCeremony consumes a ceremony of kind (ErrNotFound when unknown,
// expired or already used).
func (c *authCore) takeCeremony(ctx context.Context, id string, kind store.WebAuthnChallengeKind) (*store.WebAuthnChallenge, *webauthn.SessionData, error) {
	if id == "" || len(id) > 128 {
		return nil, nil, store.ErrNotFound
	}
	ch, err := c.d.Store.WebAuthnChallenges.Consume(ctx, crypto.HashToken(id), kind)
	if err != nil {
		return nil, nil, err
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(ch.SessionData, &sd); err != nil {
		return nil, nil, err
	}
	return ch, &sd, nil
}

// boundTo reports whether a ceremony belongs to the request's session.
func boundTo(ch *store.WebAuthnChallenge, s *SessionInfo) bool {
	return ch.UserID != nil && *ch.UserID == s.User.ID && ch.TokenID != nil && *ch.TokenID == s.Token.ID
}

// acceptAssertion persists the outcome of a verified assertion: a counter
// that did not advance flags the passkey as possibly cloned (errPasskeyClone,
// login refused); otherwise the counter, backup state and last use are saved.
func (c *authCore) acceptAssertion(ctx context.Context, r *http.Request, user *store.User, sc *store.WebAuthnCredential, cred *webauthn.Credential) error {
	repo := c.d.Store.WebAuthnCredentials
	if cred.Authenticator.CloneWarning {
		if err := repo.FlagClone(ctx, sc.ID); err != nil {
			return err
		}
		c.d.Log.WarnContext(ctx, "passkey clone warning", slog.Int64("user_id", user.ID), slog.Int64("passkey_id", sc.ID),
			slog.Uint64("stored_count", uint64(sc.SignCount)), slog.Uint64("count", uint64(cred.Authenticator.SignCount)))
		c.audit(ctx, r, "user.passkey_clone_warning", "user", idStr(user.ID), map[string]any{"username": user.Username,
			"passkey_id": sc.ID, "name": sc.Name})
		return errPasskeyClone
	}
	return repo.RecordUse(ctx, sc.ID, sc.SignCount, cred.Authenticator.SignCount, cred.Flags.BackupState, cred.Flags.UserVerified)
}

func passkeyFailed(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusUnauthorized, "passkey_failed", "passkey verification failed or timed out; try again")
}

func passkeysUnavailable(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusConflict, "passkeys_unavailable", "passkeys are not configured on this server")
}

type passkeyFinishRequest struct {
	CeremonyID string          `json:"ceremony_id"`
	Credential json.RawMessage `json:"credential"`
}

// ---- usernameless login ----

func (c *authCore) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !c.pk.available() {
		passkeysUnavailable(w)
		return
	}
	ipKey := limitIP + httpx.ClientIPString(r)
	if ok, wait := c.allow(ipKey); !ok {
		c.d.Telemetry.Login(metrics.SurfacePanel, metrics.MethodPasskey, metrics.ResultRateLimited)
		rateLimited(w, wait)
		return
	}
	if n, err := c.d.Store.WebAuthnChallenges.CountActiveAnonymous(ctx); err != nil {
		internalError(w)
		return
	} else if n >= maxAnonymousCeremonies {
		rateLimited(w, 30*time.Second)
		return
	}
	opts, sd, err := c.pk.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		c.d.Log.ErrorContext(ctx, "passkey login begin", slog.Any("err", err))
		internalError(w)
		return
	}
	id, err := c.saveCeremony(ctx, store.WebAuthnLogin, nil, nil, sd)
	if err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ceremonyDTO{CeremonyID: id, Options: opts})
}

func (c *authCore) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !c.pk.available() {
		passkeysUnavailable(w)
		return
	}
	var req passkeyFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ipKey := limitIP + httpx.ClientIPString(r)
	if ok, wait := c.allow(ipKey); !ok {
		c.d.Telemetry.Login(metrics.SurfacePanel, metrics.MethodPasskey, metrics.ResultRateLimited)
		rateLimited(w, wait)
		return
	}
	var (
		owner *store.User
		pu    *passkeyUser
	)
	fail := func(reason string) {
		keys := []string{ipKey}
		ev := loginlog.Event{Method: loginlog.MethodPasskey, FailureReason: reason}
		if owner != nil {
			keys = append(keys, userKey(owner.Username))
			ev.UserID, ev.UsernameAttempted = &owner.ID, owner.Username
		}
		c.fail(keys...)
		c.record(ctx, r, ev)
		passkeyFailed(w)
	}
	_, sd, err := c.takeCeremony(ctx, req.CeremonyID, store.WebAuthnLogin)
	if errors.Is(err, store.ErrNotFound) {
		fail("passkey_expired")
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		fail("passkey_invalid")
		return
	}
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		sc, err := c.d.Store.WebAuthnCredentials.GetByCredentialID(ctx, rawID)
		if err != nil {
			return nil, err
		}
		u, err := c.d.Store.Users.GetByID(ctx, sc.UserID)
		if err != nil {
			return nil, err
		}
		if pu, err = c.passkeyUserOf(ctx, u, false); err != nil {
			return nil, err
		}
		owner = u
		return pu, nil
	}
	_, cred, err := c.pk.wa.ValidatePasskeyLogin(handler, *sd, parsed)
	if err != nil {
		c.d.Log.DebugContext(ctx, "passkey login rejected", slog.Any("err", err))
		fail("passkey_invalid")
		return
	}
	if !owner.IsActive() {
		fail("disabled")
		return
	}
	sc := pu.find(cred.ID)
	if sc == nil {
		fail("passkey_invalid")
		return
	}
	if err := c.acceptAssertion(ctx, r, owner, sc, cred); err != nil {
		if errors.Is(err, errPasskeyClone) {
			fail("passkey_clone_warning")
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			fail("passkey_replayed")
			return
		}
		internalError(w)
		return
	}
	c.completeLogin(w, r, owner, loginlog.MethodPasskey, metrics.MethodPasskey, ipKey, userKey(owner.Username))
}

// ---- second factor ----

func (c *authCore) handlePasskeyMFABegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	if s == nil || !s.MFAPending {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "no pending login")
		return
	}
	if !c.pk.available() {
		passkeysUnavailable(w)
		return
	}
	pu, err := c.passkeyUserOf(ctx, s.User, false)
	if errors.Is(err, store.ErrNotFound) {
		httpx.WriteError(w, http.StatusConflict, "no_passkeys", "no passkey is registered for this account")
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	if len(pu.stored) == 0 {
		httpx.WriteError(w, http.StatusConflict, "no_passkeys", "no passkey is registered for this account")
		return
	}
	opts, sd, err := c.pk.wa.BeginLogin(pu, webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		c.d.Log.ErrorContext(ctx, "passkey mfa begin", slog.Any("err", err))
		internalError(w)
		return
	}
	id, err := c.saveCeremony(ctx, store.WebAuthnMFA, &s.User.ID, &s.Token.ID, sd)
	if err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ceremonyDTO{CeremonyID: id, Options: opts})
}

func (c *authCore) handlePasskeyMFAFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	if s == nil || !s.MFAPending {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "no pending login")
		return
	}
	if !c.pk.available() {
		passkeysUnavailable(w)
		return
	}
	var req passkeyFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ipKey, uKey := limitIP+httpx.ClientIPString(r), userKey(s.User.Username)
	if ok, wait := c.allow(ipKey, uKey); !ok {
		c.d.Telemetry.Login(metrics.SurfacePanel, metrics.MethodPasskey, metrics.ResultRateLimited)
		rateLimited(w, wait)
		return
	}
	fail := func(reason string) {
		c.fail(ipKey, uKey)
		c.record(ctx, r, loginlog.Event{UserID: &s.User.ID, Method: loginlog.MethodPasskey,
			UsernameAttempted: s.User.Username, FailureReason: reason})
		passkeyFailed(w)
	}
	ch, sd, err := c.takeCeremony(ctx, req.CeremonyID, store.WebAuthnMFA)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !boundTo(ch, s)) {
		fail("passkey_expired")
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	pu, err := c.passkeyUserOf(ctx, s.User, false)
	if err != nil {
		internalError(w)
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes(req.Credential)
	if err != nil {
		fail("passkey_invalid")
		return
	}
	cred, err := c.pk.wa.ValidateLogin(pu, *sd, parsed)
	if err != nil {
		c.d.Log.DebugContext(ctx, "passkey mfa rejected", slog.Any("err", err))
		fail("passkey_invalid")
		return
	}
	sc := pu.find(cred.ID)
	if sc == nil {
		fail("passkey_invalid")
		return
	}
	if err := c.acceptAssertion(ctx, r, s.User, sc, cred); err != nil {
		switch {
		case errors.Is(err, errPasskeyClone):
			fail("passkey_clone_warning")
		case errors.Is(err, store.ErrNotFound):
			fail("passkey_replayed")
		default:
			internalError(w)
		}
		return
	}
	// sess.create revokes the pending session (rotation).
	c.completeLogin(w, r, s.User, loginlog.MethodPasskey, metrics.MethodPasskey, ipKey, uKey)
}

// mfaMethods lists the second factors a pending login can use.
func (c *authCore) mfaMethods(ctx context.Context, userID int64) []string {
	out := []string{"totp"}
	if c.pk.available() {
		if n, err := c.d.Store.WebAuthnCredentials.CountForUser(ctx, userID); err == nil && n > 0 {
			out = append(out, "passkey")
		}
	}
	return out
}

// ---- my passkeys ----

// PasskeyDTO is components.schemas.Passkey.
type PasskeyDTO struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at"`
	BackupEligible bool       `json:"backup_eligible"`
	Synced         bool       `json:"synced"`
	CloneWarning   bool       `json:"clone_warning"`
	Transports     []string   `json:"transports"`
}

func newPasskeyDTO(s *store.WebAuthnCredential) PasskeyDTO {
	tr := s.Transports
	if tr == nil {
		tr = []string{}
	}
	return PasskeyDTO{ID: s.ID, Name: s.Name, CreatedAt: s.CreatedAt, LastUsedAt: s.LastUsedAt,
		BackupEligible: s.BackupEligible, Synced: s.BackupState, CloneWarning: s.CloneWarning, Transports: tr}
}

func (c *authCore) listPasskeys(w http.ResponseWriter, r *http.Request, userID int64) {
	creds, err := c.d.Store.WebAuthnCredentials.ListForUser(r.Context(), userID)
	if err != nil {
		internalError(w)
		return
	}
	out := make([]PasskeyDTO, 0, len(creds))
	for _, s := range creds {
		out = append(out, newPasskeyDTO(s))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsDTO[PasskeyDTO]{Items: out})
}

func (c *authCore) handleListMyPasskeys(w http.ResponseWriter, r *http.Request) {
	c.listPasskeys(w, r, CurrentSession(r.Context()).User.ID)
}

// passkeyReauth confirms the user before adding a passkey: the password or a
// TOTP/recovery code when the account has one of them, otherwise (OIDC-only
// accounts) a session signed in within passkeyFreshSession.
func (c *authCore) passkeyReauth(w http.ResponseWriter, r *http.Request, s *SessionInfo, req reauthRequest) bool {
	if s.User.HasPassword() || s.TOTPEnabled {
		return c.reauth(w, r, s.User, req)
	}
	if c.d.Store.Now().Sub(s.Token.CreatedAt) > passkeyFreshSession {
		httpx.WriteError(w, http.StatusUnauthorized, "reauth_required", "sign in again to add a passkey")
		return false
	}
	return true
}

func (c *authCore) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	if !c.pk.available() {
		passkeysUnavailable(w)
		return
	}
	var req reauthRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !c.passkeyReauth(w, r, s, req) {
		return
	}
	pu, err := c.passkeyUserOf(ctx, s.User, true)
	if err != nil {
		internalError(w)
		return
	}
	if len(pu.stored) >= store.MaxPasskeysPerUser {
		httpx.WriteError(w, http.StatusConflict, "passkey_limit", "too many passkeys; remove one first")
		return
	}
	rpName, _ := store.GetOr(ctx, c.d.Store.Settings, store.SettingBrandingTitle, defaultTOTPIssuer)
	if rpName == "" {
		rpName = defaultTOTPIssuer
	}
	opts, sd, err := c.pk.wa.BeginRegistration(pu,
		webauthn.WithExclusions(webauthn.Credentials(pu.WebAuthnCredentials()).CredentialDescriptors()),
		webauthn.WithRegistrationRelyingPartyName(rpName))
	if err != nil {
		c.d.Log.ErrorContext(ctx, "passkey register begin", slog.Any("err", err))
		internalError(w)
		return
	}
	id, err := c.saveCeremony(ctx, store.WebAuthnRegister, &s.User.ID, &s.Token.ID, sd)
	if err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ceremonyDTO{CeremonyID: id, Options: opts})
}

type passkeyRegisterFinishRequest struct {
	CeremonyID string          `json:"ceremony_id"`
	Name       string          `json:"name"`
	Credential json.RawMessage `json:"credential"`
}

func cleanPasskeyName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = defaultPasskeyName
	}
	return name, utf8.RuneCountInString(name) <= maxPasskeyName && utf8.ValidString(name)
}

func (c *authCore) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	if !c.pk.available() {
		passkeysUnavailable(w)
		return
	}
	var req passkeyRegisterFinishRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, ok := cleanPasskeyName(req.Name)
	if !ok {
		validationFailed(w, "invalid passkey", map[string]string{"name": "must be at most 64 characters"})
		return
	}
	ch, sd, err := c.takeCeremony(ctx, req.CeremonyID, store.WebAuthnRegister)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !boundTo(ch, s)) {
		passkeyFailed(w)
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBytes(req.Credential)
	if err != nil {
		passkeyFailed(w)
		return
	}
	pu, err := c.passkeyUserOf(ctx, s.User, false)
	if err != nil {
		internalError(w)
		return
	}
	cred, err := c.pk.wa.CreateCredential(pu, *sd, parsed)
	if err != nil {
		c.d.Log.DebugContext(ctx, "passkey registration rejected", slog.Any("err", err))
		passkeyFailed(w)
		return
	}
	tr := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		tr = append(tr, string(t))
	}
	row := &store.WebAuthnCredential{UserID: s.User.ID, CredentialID: cred.ID, PublicKey: cred.PublicKey,
		AttestationType: cred.AttestationType, AAGUID: cred.Authenticator.AAGUID, SignCount: cred.Authenticator.SignCount,
		Transports: tr, Attachment: string(cred.Authenticator.Attachment), UserVerified: cred.Flags.UserVerified,
		BackupEligible: cred.Flags.BackupEligible, BackupState: cred.Flags.BackupState, Name: name}
	err = c.d.Store.Tx(ctx, func(tx *store.Store) error {
		n, err := tx.WebAuthnCredentials.CountForUser(ctx, s.User.ID)
		if err != nil {
			return err
		}
		if n >= store.MaxPasskeysPerUser {
			return errTooManyPasskeys
		}
		return tx.WebAuthnCredentials.Create(ctx, row)
	})
	switch {
	case errors.Is(err, errTooManyPasskeys):
		httpx.WriteError(w, http.StatusConflict, "passkey_limit", "too many passkeys; remove one first")
		return
	case errors.Is(err, store.ErrConflict):
		httpx.WriteError(w, http.StatusConflict, "conflict", "this passkey is already registered")
		return
	case err != nil:
		internalError(w)
		return
	}
	c.audit(ctx, r, "user.passkey_add", "user", idStr(s.User.ID), map[string]any{"passkey_id": row.ID, "name": row.Name,
		"synced": row.BackupState})
	httpx.WriteJSON(w, http.StatusCreated, newPasskeyDTO(row))
}

var errTooManyPasskeys = errors.New("too many passkeys")

type passkeyRenameRequest struct {
	Name string `json:"name"`
}

func (c *authCore) handleRenamePasskey(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := CurrentSession(ctx)
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		httpx.NotFound(w, r)
		return
	}
	var req passkeyRenameRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, ok := cleanPasskeyName(req.Name)
	if !ok || strings.TrimSpace(req.Name) == "" {
		validationFailed(w, "invalid passkey", map[string]string{"name": "must be 1 to 64 characters"})
		return
	}
	old, err := c.d.Store.WebAuthnCredentials.GetForUser(ctx, s.User.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	if err := c.d.Store.WebAuthnCredentials.Rename(ctx, s.User.ID, id, name); err != nil {
		internalError(w)
		return
	}
	c.audit(ctx, r, "user.passkey_rename", "user", idStr(s.User.ID), map[string]any{"passkey_id": id,
		"changes": map[string]any{"name": []string{old.Name, name}}})
	fresh, err := c.d.Store.WebAuthnCredentials.GetForUser(ctx, s.User.ID, id)
	if err != nil {
		internalError(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, newPasskeyDTO(fresh))
}

// deletePasskey removes passkey id of user and writes 204 (404 when it is
// not theirs).
func (c *authCore) deletePasskey(w http.ResponseWriter, r *http.Request, user *store.User, id int64, action string) {
	ctx := r.Context()
	old, err := c.d.Store.WebAuthnCredentials.GetForUser(ctx, user.ID, id)
	if err == nil {
		err = c.d.Store.WebAuthnCredentials.DeleteForUser(ctx, user.ID, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		internalError(w)
		return
	}
	c.audit(ctx, r, action, "user", idStr(user.ID), map[string]any{"username": user.Username, "passkey_id": id,
		"name": old.Name})
	w.WriteHeader(http.StatusNoContent)
}

func (c *authCore) handleDeleteMyPasskey(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(chi.URLParam(r, "id"))
	if !ok {
		httpx.NotFound(w, r)
		return
	}
	c.deletePasskey(w, r, CurrentSession(r.Context()).User, id, "user.passkey_remove")
}

// ---- admin ----

func (a *resources) handleListUserPasskeys(w http.ResponseWriter, r *http.Request) {
	u := a.loadUser(w, r)
	if u == nil {
		return
	}
	a.listPasskeys(w, r, u.ID)
}

func (a *resources) handleDeleteUserPasskey(w http.ResponseWriter, r *http.Request) {
	u := a.loadUser(w, r)
	if u == nil {
		return
	}
	id, ok := pathID(w, r, "passkey_id")
	if !ok {
		return
	}
	a.deletePasskey(w, r, u, id, "user.passkey_revoke")
}
