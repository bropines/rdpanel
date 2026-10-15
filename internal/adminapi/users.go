package adminapi

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/httpx"
	"github.com/bropines/rustdesk-panel/internal/store"
)

// Errors of the last-admin / self protections (409).
var (
	errLastAdmin = errors.New("last active admin")
	errSelf      = errors.New("cannot delete yourself")
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,128}$`)

// userListItemDTO is components.schemas.UserListItem.
type userListItemDTO struct {
	UserDTO
	UserGroup   *groupRefDTO `json:"user_group"`
	DeviceCount int          `json:"device_count"`
	IsLastAdmin bool         `json:"is_last_admin"`
}

// userDetailDTO is components.schemas.UserDetail.
type userDetailDTO struct {
	userListItemDTO
	Identities       []IdentityDTO `json:"identities"`
	AddressBookCount int           `json:"address_book_count"`
}

func newUserListItem(u *store.UserRow, activeAdmins int) userListItemDTO {
	o := userListItemDTO{UserDTO: NewUserDTO(u.User, u.TOTPEnabled), DeviceCount: u.DeviceCount,
		IsLastAdmin: u.IsAdmin() && u.IsActive() && activeAdmins <= 1}
	if u.UserGroupID != nil {
		o.UserGroup = &groupRefDTO{ID: *u.UserGroupID, Name: u.GroupName}
	}
	return o
}

func (a *resources) userItem(ctx context.Context, id int64) (userListItemDTO, error) {
	row, err := a.d.Store.Users.GetRow(ctx, id)
	if err != nil {
		return userListItemDTO{}, err
	}
	n, err := a.d.Store.Users.CountActiveAdmins(ctx)
	if err != nil {
		return userListItemDTO{}, err
	}
	return newUserListItem(row, n), nil
}

func (a *resources) handleListUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := newQuery(r)
	pg := q.paging()
	p := store.UserSearch{Query: q.search(), Role: store.Role(q.enum("role", "admin", "user")),
		Status: store.UserStatus(q.enum("status", "active", "disabled")), UserGroupID: q.id("user_group_id"),
		Limit: pg.Size, Offset: pg.offset()}
	p.Sort, p.Desc = q.sort("username", "username", "created_at", "last_login_at", "role")
	if !q.ok(w) {
		return
	}
	rows, total, err := a.d.Store.Users.Search(ctx, p)
	if err != nil {
		a.fail500(w, r, "list users", err)
		return
	}
	admins, err := a.d.Store.Users.CountActiveAdmins(ctx)
	if err != nil {
		a.fail500(w, r, "list users", err)
		return
	}
	out := make([]userListItemDTO, 0, len(rows))
	for _, u := range rows {
		out = append(out, newUserListItem(u, admins))
	}
	httpx.WriteJSON(w, http.StatusOK, newListPage(out, total, pg))
}

// userInput is components.schemas.UserCreate / UserUpdate.
type userInput struct {
	Username     *string     `json:"username"`
	Password     *string     `json:"password"`
	Email        opt[string] `json:"email"`
	DisplayName  *string     `json:"display_name"`
	Note         *string     `json:"note"`
	Role         *string     `json:"role"`
	Status       *string     `json:"status"`
	UserGroupID  opt[int64]  `json:"user_group_id"`
	TOTPRequired *bool       `json:"totp_required"`
}

func validPassword(pw string) bool {
	return utf8.RuneCountInString(pw) >= MinPasswordChars && len(pw) <= auth.MaxPasswordBytes
}

// validate checks the provided fields (create requires username and role).
func (a *resources) validateUser(ctx context.Context, in *userInput, create bool) (map[string]string, error) {
	f := map[string]string{}
	if in.Username != nil {
		*in.Username = strings.TrimSpace(*in.Username)
		if !usernameRe.MatchString(*in.Username) {
			f["username"] = "1-128 characters: letters, digits, '.', '_', '@' or '-'"
		}
	} else if create {
		f["username"] = "required"
	}
	if in.Password != nil && !validPassword(*in.Password) {
		f["password"] = "must be at least 8 characters"
	}
	if in.Email.Set && !in.Email.Null {
		e := strings.TrimSpace(in.Email.V)
		in.Email.V = e
		if e != "" {
			if addr, err := mail.ParseAddress(e); err != nil || addr.Address != e || len(e) > 254 {
				f["email"] = "must be a valid email address"
			}
		}
	}
	if in.DisplayName != nil && runes(*in.DisplayName) > 128 {
		f["display_name"] = "must be at most 128 characters"
	}
	if in.Note != nil && runes(*in.Note) > 4096 {
		f["note"] = "must be at most 4096 characters"
	}
	if in.Role != nil {
		if *in.Role != string(store.RoleAdmin) && *in.Role != string(store.RoleUser) {
			f["role"] = "must be admin or user"
		}
	} else if create {
		f["role"] = "required"
	}
	if in.Status != nil && *in.Status != string(store.UserActive) && *in.Status != string(store.UserDisabled) {
		f["status"] = "must be active or disabled"
	}
	if in.UserGroupID.Set && !in.UserGroupID.Null {
		if _, err := a.d.Store.UserGroups.Get(ctx, in.UserGroupID.V); errors.Is(err, store.ErrNotFound) {
			f["user_group_id"] = "user group does not exist"
		} else if err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (a *resources) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in userInput
	if !decodeJSON(w, r, &in) {
		return
	}
	fields, err := a.validateUser(ctx, &in, true)
	if err != nil {
		a.fail500(w, r, "create user", err)
		return
	}
	if len(fields) > 0 {
		validationFailed(w, "invalid user", fields)
		return
	}
	u := &store.User{Username: *in.Username, Role: store.Role(*in.Role), Status: store.UserActive}
	if in.Email.Set && !in.Email.Null {
		u.Email = in.Email.V
	}
	if in.DisplayName != nil {
		u.DisplayName = strings.TrimSpace(*in.DisplayName)
	}
	if in.Note != nil {
		u.Note = *in.Note
	}
	if in.Status != nil {
		u.Status = store.UserStatus(*in.Status)
	}
	if in.UserGroupID.Set {
		u.UserGroupID = in.UserGroupID.ptr()
	}
	if in.TOTPRequired != nil {
		u.TOTPRequired = *in.TOTPRequired
	}
	if in.Password != nil {
		h, algo, err := a.d.Hasher.Hash(*in.Password)
		if err != nil {
			validationFailed(w, "invalid user", map[string]string{"password": "invalid password"})
			return
		}
		u.PasswordHash, u.PasswordAlgo = h, string(algo)
		now := a.d.Store.Now()
		u.PasswordChangedAt = &now
	}
	if err := a.d.Store.Users.Create(ctx, u); err != nil {
		if errors.Is(err, store.ErrConflict) {
			conflict(w, "conflict", "a user with this username already exists")
			return
		}
		a.writeStoreErr(w, r, "create user", err)
		return
	}
	a.audit(ctx, r, "user.create", "user", idStr(u.ID), map[string]any{"username": u.Username, "role": string(u.Role),
		"status": string(u.Status), "has_password": u.HasPassword(), "user_group_id": ptrVal(u.UserGroupID)})
	item, err := a.userItem(ctx, u.ID)
	if err != nil {
		a.fail500(w, r, "create user", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, item)
}

// loadUser loads {user_id} (404 otherwise).
func (a *resources) loadUser(w http.ResponseWriter, r *http.Request) *store.User {
	id, ok := pathID(w, r, "user_id")
	if !ok {
		return nil
	}
	u, err := a.d.Store.Users.GetByID(r.Context(), id)
	if err != nil {
		a.writeStoreErr(w, r, "load user", err)
		return nil
	}
	return u
}

func (a *resources) handleGetUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := a.loadUser(w, r)
	if u == nil {
		return
	}
	item, err := a.userItem(ctx, u.ID)
	if err != nil {
		a.writeStoreErr(w, r, "get user", err)
		return
	}
	ids, err := IdentitiesOf(ctx, a.d.Store, u.ID)
	if err != nil {
		a.fail500(w, r, "get user", err)
		return
	}
	out := userDetailDTO{userListItemDTO: item, Identities: ids}
	if out.AddressBookCount, err = a.d.Store.AddressBooks.CountOwned(ctx, u.ID); err != nil {
		a.fail500(w, r, "get user", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// guardLastAdmin fails with errLastAdmin when u is an active admin that
// would stop being one and no other active admin exists.
func guardLastAdmin(ctx context.Context, tx *store.Store, u *store.User, stillAdmin bool) error {
	if !u.IsAdmin() || !u.IsActive() || stillAdmin {
		return nil
	}
	n, err := tx.Users.CountActiveAdmins(ctx)
	if err != nil {
		return err
	}
	if n <= 1 {
		return errLastAdmin
	}
	return nil
}

func (a *resources) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "user_id")
	if !ok {
		return
	}
	var in userInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Password != nil {
		validationFailed(w, "invalid user", map[string]string{"password": "use /users/{id}/password"})
		return
	}
	fields, err := a.validateUser(ctx, &in, false)
	if err != nil {
		a.fail500(w, r, "update user", err)
		return
	}
	if len(fields) > 0 {
		validationFailed(w, "invalid user", fields)
		return
	}
	ch := diff{}
	var revoked int64
	err = a.d.Store.Tx(ctx, func(tx *store.Store) error {
		u, err := tx.Users.GetByID(ctx, id)
		if err != nil {
			return err
		}
		old := *u
		if in.Username != nil {
			u.Username = *in.Username
		}
		if in.Email.Set {
			u.Email = ""
			if !in.Email.Null {
				u.Email = in.Email.V
			}
		}
		if in.DisplayName != nil {
			u.DisplayName = strings.TrimSpace(*in.DisplayName)
		}
		if in.Note != nil {
			u.Note = *in.Note
		}
		if in.Role != nil {
			u.Role = store.Role(*in.Role)
		}
		if in.Status != nil {
			u.Status = store.UserStatus(*in.Status)
		}
		if in.UserGroupID.Set {
			u.UserGroupID = in.UserGroupID.ptr()
		}
		if in.TOTPRequired != nil {
			u.TOTPRequired = *in.TOTPRequired
		}
		if err := guardLastAdmin(ctx, tx, &old, u.IsAdmin() && u.IsActive()); err != nil {
			return err
		}
		if err := tx.Users.Update(ctx, u); err != nil {
			return err
		}
		ch.val("username", old.Username, u.Username)
		ch.val("email", old.Email, u.Email)
		ch.val("display_name", old.DisplayName, u.DisplayName)
		ch.text("note", old.Note, u.Note)
		ch.val("role", string(old.Role), string(u.Role))
		ch.val("status", string(old.Status), string(u.Status))
		ch.val("totp_required", old.TOTPRequired, u.TOTPRequired)
		if !eqID(old.UserGroupID, u.UserGroupID) {
			ch["user_group_id"] = []any{ptrVal(old.UserGroupID), ptrVal(u.UserGroupID)}
		}
		if old.IsActive() && !u.IsActive() {
			if revoked, err = tx.AuthTokens.RevokeForUser(ctx, u.ID, "", "user disabled"); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, errLastAdmin):
		conflict(w, "last_admin", "cannot demote or disable the last active admin")
		return
	case errors.Is(err, store.ErrConflict):
		conflict(w, "conflict", "a user with this username already exists")
		return
	case err != nil:
		a.writeStoreErr(w, r, "update user", err)
		return
	}
	if len(ch) > 0 {
		details := map[string]any{"changes": ch}
		if revoked > 0 {
			details["revoked_sessions"] = revoked
		}
		a.audit(ctx, r, "user.update", "user", idStr(id), details)
	}
	item, err := a.userItem(ctx, id)
	if err != nil {
		a.fail500(w, r, "update user", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, item)
}

func (a *resources) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r, "user_id")
	if !ok {
		return
	}
	var username string
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		u, err := tx.Users.GetByID(ctx, id)
		if err != nil {
			return err
		}
		if u.ID == CurrentUser(ctx).ID {
			return errSelf
		}
		if err := guardLastAdmin(ctx, tx, u, false); err != nil {
			return err
		}
		username = u.Username
		return tx.Users.Delete(ctx, id)
	})
	switch {
	case errors.Is(err, errSelf):
		conflict(w, "self", "you cannot delete your own account")
	case errors.Is(err, errLastAdmin):
		conflict(w, "last_admin", "cannot delete the last active admin")
	case err != nil:
		a.writeStoreErr(w, r, "delete user", err)
	default:
		a.audit(ctx, r, "user.delete", "user", idStr(id), map[string]any{"username": username})
		w.WriteHeader(http.StatusNoContent)
	}
}

type passwordReset struct {
	Password       string `json:"password"`
	RevokeSessions *bool  `json:"revoke_sessions"`
}

func (a *resources) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := a.loadUser(w, r)
	if u == nil {
		return
	}
	var in passwordReset
	if !decodeJSON(w, r, &in) {
		return
	}
	if !validPassword(in.Password) {
		validationFailed(w, "invalid password", map[string]string{"password": "must be at least 8 characters"})
		return
	}
	hash, algo, err := a.d.Hasher.Hash(in.Password)
	if err != nil {
		validationFailed(w, "invalid password", map[string]string{"password": "invalid password"})
		return
	}
	revoke := in.RevokeSessions == nil || *in.RevokeSessions
	current := CurrentSession(ctx).Token.ID
	var revoked int64
	err = a.d.Store.Tx(ctx, func(tx *store.Store) error {
		if err := tx.Users.SetPassword(ctx, u.ID, hash, string(algo)); err != nil {
			return err
		}
		if !revoke {
			return nil
		}
		toks, err := tx.AuthTokens.ListActiveForUser(ctx, u.ID, "")
		if err != nil {
			return err
		}
		for _, t := range toks {
			if t.ID == current {
				continue // an admin resetting their own password keeps this session
			}
			if err := tx.AuthTokens.Revoke(ctx, t.ID, "password reset by admin"); err != nil {
				return err
			}
			revoked++
		}
		return nil
	})
	if err != nil {
		a.writeStoreErr(w, r, "reset password", err)
		return
	}
	a.audit(ctx, r, "user.password_reset", "user", idStr(u.ID), map[string]any{"username": u.Username,
		"revoked_sessions": revoked})
	w.WriteHeader(http.StatusNoContent)
}

func (a *resources) handleResetTOTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := a.loadUser(w, r)
	if u == nil {
		return
	}
	if err := a.totp.ResetUser(ctx, u.ID); err != nil {
		a.fail500(w, r, "reset totp", err)
		return
	}
	a.audit(ctx, r, "user.totp_reset", "user", idStr(u.ID), map[string]any{"username": u.Username})
	w.WriteHeader(http.StatusNoContent)
}

func (a *resources) handleListUserSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := a.loadUser(w, r)
	if u == nil {
		return
	}
	toks, err := a.d.Store.AuthTokens.ListActiveForUser(ctx, u.ID, "")
	if err != nil {
		a.fail500(w, r, "list sessions", err)
		return
	}
	now := a.d.Store.Now()
	current := CurrentSession(ctx).Token.ID
	out := make([]sessionDTO, 0, len(toks))
	for _, t := range toks {
		if t.MFAPending || (t.JWTExpiresAt != nil && !t.JWTExpiresAt.After(now)) {
			continue
		}
		out = append(out, newSessionDTO(t, current))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsDTO[sessionDTO]{Items: out})
}

type revokeRequest struct {
	Kind string `json:"kind"`
}

type revokeResultDTO struct {
	Revoked int64 `json:"revoked"`
}

func (a *resources) handleRevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := a.loadUser(w, r)
	if u == nil {
		return
	}
	var in revokeRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	var kind store.TokenKind
	switch in.Kind {
	case "", "all":
	case "web":
		kind = store.TokenWeb
	case "client":
		kind = store.TokenClient
	default:
		validationFailed(w, "invalid kind", map[string]string{"kind": "must be all, web or client"})
		return
	}
	n, err := a.d.Store.AuthTokens.RevokeForUser(ctx, u.ID, kind, "revoked by admin")
	if err != nil {
		a.fail500(w, r, "revoke sessions", err)
		return
	}
	if u.ID == CurrentUser(ctx).ID && kind != store.TokenClient {
		a.sess.clearCookies(w)
	}
	kindName := in.Kind
	if kindName == "" {
		kindName = "all"
	}
	a.audit(ctx, r, "user.sessions_revoke", "user", idStr(u.ID), map[string]any{"username": u.Username,
		"kind": kindName, "revoked": n})
	httpx.WriteJSON(w, http.StatusOK, revokeResultDTO{Revoked: n})
}

func (a *resources) handleRevokeUserSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := a.loadUser(w, r)
	if u == nil {
		return
	}
	sid, ok := pathID(w, r, "session_id")
	if !ok {
		return
	}
	t, err := a.d.Store.AuthTokens.Get(ctx, sid)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (t.UserID != u.ID || t.RevokedAt != nil)) {
		httpx.NotFound(w, r)
		return
	}
	if err != nil {
		a.fail500(w, r, "revoke session", err)
		return
	}
	if err := a.d.Store.AuthTokens.Revoke(ctx, sid, "revoked by admin"); err != nil {
		a.writeStoreErr(w, r, "revoke session", err)
		return
	}
	if sid == CurrentSession(ctx).Token.ID {
		a.sess.clearCookies(w)
	}
	a.audit(ctx, r, "user.session_revoke", "auth_token", idStr(sid), map[string]any{"user_id": u.ID,
		"username": u.Username, "kind": string(t.Kind)})
	w.WriteHeader(http.StatusNoContent)
}

func (a *resources) handleUnlinkUserIdentity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := a.loadUser(w, r)
	if u == nil {
		return
	}
	iid, ok := pathID(w, r, "identity_id")
	if !ok {
		return
	}
	errLastMethod := errors.New("last login method")
	var slug string
	err := a.d.Store.Tx(ctx, func(tx *store.Store) error {
		ident, err := tx.UserIdentities.Get(ctx, iid)
		if err != nil || ident.UserID != u.ID {
			return store.ErrNotFound
		}
		if !u.HasPassword() {
			n, err := tx.UserIdentities.CountForUser(ctx, u.ID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return errLastMethod
			}
		}
		if p, err := tx.IdentityProviders.Get(ctx, ident.ProviderID); err == nil {
			slug = p.Slug
		}
		return tx.UserIdentities.DeleteForUser(ctx, u.ID, iid)
	})
	switch {
	case errors.Is(err, errLastMethod):
		conflict(w, "last_login_method", "this identity is the user's only way to sign in; set a password first")
	case err != nil:
		a.writeStoreErr(w, r, "unlink identity", err)
	default:
		a.audit(ctx, r, "user.identity_unlink", "user", idStr(u.ID), map[string]any{"username": u.Username,
			"identity_id": iid, "provider": slug})
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---- lookups ----

func (a *resources) handleLookupUsers(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	s := q.search()
	if !q.ok(w) {
		return
	}
	users, err := a.d.Store.Users.Lookup(r.Context(), s, 20)
	if err != nil {
		a.fail500(w, r, "lookup users", err)
		return
	}
	out := make([]userRefDTO, 0, len(users))
	for _, u := range users {
		out = append(out, newUserRef(u))
	}
	httpx.WriteJSON(w, http.StatusOK, itemsDTO[userRefDTO]{Items: out})
}

func (a *resources) handleLookupUserGroups(w http.ResponseWriter, r *http.Request) {
	q := newQuery(r)
	s := q.search()
	if !q.ok(w) {
		return
	}
	groups, err := a.d.Store.UserGroups.Lookup(r.Context(), s, 50)
	if err != nil {
		a.fail500(w, r, "lookup user groups", err)
		return
	}
	out := make([]groupRefDTO, 0, len(groups))
	for _, g := range groups {
		out = append(out, groupRefDTO{ID: g.ID, Name: g.Name})
	}
	httpx.WriteJSON(w, http.StatusOK, itemsDTO[groupRefDTO]{Items: out})
}
