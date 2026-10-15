package legacy

import (
	"cmp"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bropines/rustdesk-panel/internal/auth"
	"github.com/bropines/rustdesk-panel/internal/crypto"
)

func sortedKeys[K cmp.Ordered, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// uniqueName makes a group name valid (1..128 chars) and unique
// case-insensitively among used, reporting every change.
func (im *importer) uniqueName(table string, id int64, name string, used map[string]bool) string {
	orig := name
	name = strings.TrimSpace(name)
	if name == "" {
		name = fmt.Sprintf("Group %d", id)
	}
	if utf8.RuneCountInString(name) > 120 {
		name = string([]rune(name)[:120])
	}
	if used[strings.ToLower(name)] {
		name = fmt.Sprintf("%s (%d)", name, id)
	}
	used[strings.ToLower(name)] = true
	if name != orig {
		im.rep.warn("renamed", table, id, "name %q stored as %q (empty, too long or a case-insensitive duplicate)", orig, name)
	}
	return name
}

// groups -> user_groups (ids kept; type 1 self, 2 shared).
func (im *importer) importGroups() error {
	used := map[string]bool{}
	c := im.count("groups")
	err := im.eachRow("groups", `SELECT id, COALESCE(CAST(name AS TEXT), ''), COALESCE(CAST(type AS INTEGER), 1),
		CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM "groups" ORDER BY id`, func(r *sql.Rows) error {
		var id, typ int64
		var name string
		var created, updated sql.NullString
		if err := r.Scan(&id, &name, &typ, &created, &updated); err != nil {
			return err
		}
		vis := "self"
		switch typ {
		case 2:
			vis = "shared"
		case 1:
		default:
			im.rep.warn("unknown_group_type", "groups", id, "group type %d mapped to 'self'", typ)
		}
		cr, up := im.stamps("groups", created, updated)
		if _, err := im.exec(`INSERT INTO user_groups (id, name, visibility, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			id, im.uniqueName("groups", id, name, used), vis, cr, up); err != nil {
			return err
		}
		im.groups[id] = true
		c.Imported++
		return nil
	})
	if err != nil {
		return err
	}
	// Legacy gives new/orphaned users group 1 ("Default group").
	if im.groups[1] {
		im.defaultGroupID = sql.NullInt64{Int64: 1, Valid: true}
	}
	return im.bumpSequence("user_groups", "groups")
}

// device_groups -> device_groups (ids kept).
func (im *importer) importDeviceGroups() error {
	used := map[string]bool{}
	c := im.count("device_groups")
	err := im.eachRow("device_groups", `SELECT id, COALESCE(CAST(name AS TEXT), ''),
		CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM device_groups ORDER BY id`, func(r *sql.Rows) error {
		var id int64
		var name string
		var created, updated sql.NullString
		if err := r.Scan(&id, &name, &created, &updated); err != nil {
			return err
		}
		cr, up := im.stamps("device_groups", created, updated)
		if _, err := im.exec(`INSERT INTO device_groups (id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`,
			id, im.uniqueName("device_groups", id, name, used), cr, up); err != nil {
			return err
		}
		im.deviceGroups[id] = true
		c.Imported++
		return nil
	})
	if err != nil {
		return err
	}
	return im.bumpSequence("device_groups", "device_groups")
}

// users -> users (ids kept).
func (im *importer) importUsers() error {
	c := im.count("users")
	byLower := map[string]int64{}
	err := im.eachRow("users", `SELECT id, COALESCE(CAST(username AS TEXT), ''), COALESCE(CAST(email AS TEXT), ''),
		COALESCE(CAST(password AS TEXT), ''), COALESCE(CAST(nickname AS TEXT), ''), COALESCE(CAST(avatar AS TEXT), ''),
		COALESCE(CAST(group_id AS INTEGER), 0), CAST(is_admin AS TEXT), COALESCE(CAST(status AS INTEGER), 0),
		COALESCE(CAST(remark AS TEXT), ''), CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM users ORDER BY id`, func(r *sql.Rows) error {
		var (
			id, groupID, status                                 int64
			username, email, password, nickname, avatar, remark string
			isAdmin, created, updated                           sql.NullString
		)
		if err := r.Scan(&id, &username, &email, &password, &nickname, &avatar, &groupID, &isAdmin, &status,
			&remark, &created, &updated); err != nil {
			return err
		}
		if id < 1 || id > 4294967295 {
			c.Skipped++
			im.rep.fail("user_id_range", "users", id, "user id %d does not fit u32; client JWTs embed it", id)
			return nil
		}
		if username == "" || utf8.RuneCountInString(username) > 128 {
			c.Skipped++
			im.rep.fail("invalid_username", "users", id, "username %q is empty or longer than 128 characters", username)
			return nil
		}
		lower := strings.ToLower(username)
		if other, dup := byLower[lower]; dup {
			c.Skipped++
			im.rep.fail("username_case_collision", "users", id,
				"username %q collides case-insensitively with user %d; rename one of them in the legacy panel and re-run", username, other)
			return nil
		}
		byLower[lower] = id

		algo, known := passwordAlgo(password)
		hash := nullStr(password)
		if !known {
			im.rep.warn("unknown_password_format", "users", id,
				"user %q has a password in an unrecognised format; it was not imported (password login disabled)", username)
			hash = sql.NullString{}
		}
		statusText := "active"
		switch status {
		case 1:
		case 2:
			statusText = "disabled"
		default:
			statusText = "disabled"
			im.rep.warn("unknown_user_status", "users", id, "user %q has status %d; imported as disabled", username, status)
		}
		role := "user"
		if parseBool(isAdmin) {
			role = "admin"
		}
		group := sql.NullInt64{Int64: groupID, Valid: true}
		if !im.groups[groupID] {
			group = im.defaultGroupID
			if groupID != 0 {
				im.rep.warn("orphan_user_group", "users", id, "user %q references missing group %d; assigned the default group", username, groupID)
			}
		}
		cr, up := im.stamps("users", created, updated)
		if _, err := im.exec(`INSERT INTO users (id, username, email, display_name, avatar_url, note, password_hash,
			password_algo, role, status, user_group_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, username, nullStr(strings.TrimSpace(email)), nickname, avatar, remark, hash, nullStr(algoIf(hash.Valid, algo)),
			role, statusText, group, cr, up); err != nil {
			return err
		}
		im.users[id] = &userInfo{username: username, legacyGroupID: groupID, active: statusText == "active", hasPassword: hash.Valid}
		c.Imported++
		return nil
	})
	if err != nil {
		return err
	}
	return im.bumpSequence("users", "users")
}

func algoIf(ok bool, algo string) string {
	if !ok {
		return ""
	}
	return algo
}

var providerKinds = map[string]bool{"github": true, "google": true, "oidc": true, "linuxdo": true}

// oauths -> identity_providers (ids kept; client_secret encrypted).
func (im *importer) importProviders() error {
	c := im.count("oauths")
	err := im.eachRow("oauths", `SELECT id, COALESCE(CAST(op AS TEXT), ''), COALESCE(CAST(oauth_type AS TEXT), ''),
		COALESCE(CAST(client_id AS TEXT), ''), COALESCE(CAST(client_secret AS TEXT), ''), CAST(auto_register AS TEXT),
		COALESCE(CAST(scopes AS TEXT), ''), COALESCE(CAST(issuer AS TEXT), ''), CAST(pkce_enable AS TEXT),
		COALESCE(CAST(pkce_method AS TEXT), ''), CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
		FROM oauths ORDER BY id`, func(r *sql.Rows) error {
		var (
			id                                                     int64
			op, kind, clientID, secret, scopes, issuer, pkceMethod string
			autoReg, pkce, created, updated                        sql.NullString
		)
		if err := r.Scan(&id, &op, &kind, &clientID, &secret, &autoReg, &scopes, &issuer, &pkce, &pkceMethod,
			&created, &updated); err != nil {
			return err
		}
		kind = strings.ToLower(strings.TrimSpace(kind))
		if !providerKinds[kind] {
			c.Skipped++
			im.rep.warn("unknown_provider_type", "oauths", id, "provider %q has unsupported type %q; skipped", op, kind)
			return nil
		}
		slug := op
		if slug == "" {
			slug = kind
		}
		if len(slug) > 64 {
			c.Skipped++
			im.rep.warn("invalid_provider_slug", "oauths", id, "provider op %q is longer than 64 characters; skipped", slug)
			return nil
		}
		if prev, dup := im.providers[slug]; dup {
			c.Deduped++
			im.rep.warn("duplicate_provider", "oauths", id,
				"provider op %q duplicates provider %d; legacy used the first one, so this row was dropped", slug, prev.id)
			return nil
		}
		if kind == "google" && issuer == "" {
			issuer = "https://accounts.google.com"
		}
		method := "S256"
		if strings.EqualFold(strings.TrimSpace(pkceMethod), "plain") {
			method = "plain"
		}
		if !autoReg.Valid {
			im.rep.warn("null_auto_register", "oauths", id, "provider %q had auto_register NULL (legacy callback panicked); imported as false", slug)
		}
		if parseBool(autoReg) {
			im.rep.warn("email_linking_changed", "oauths", id,
				"provider %q auto-registers users; legacy linked existing users by email without checking email_verified, "+
					"the new server does so only when link_by_verified_email is enabled (imported disabled)", slug)
		}
		cr, up := im.stamps("oauths", created, updated)
		var secretEnc []byte
		if secret != "" {
			var err error
			if secretEnc, err = im.opts.AEAD.SealString(secret, crypto.IdentityProviderClientSecretAD(id)); err != nil {
				return err
			}
		}
		if _, err := im.exec(`INSERT INTO identity_providers (id, slug, kind, display_name, client_id, client_secret_enc,
			issuer, scopes, pkce_enabled, pkce_method, auto_register, link_by_verified_email, enabled, sort_order,
			created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 1, ?, ?, ?)`,
			id, slug, kind, slug, clientID, secretEnc, nullStr(issuer), splitScopes(scopes), boolInt(parseBool(pkce)),
			method, boolInt(parseBool(autoReg)), id, cr, up); err != nil {
			return err
		}
		p := providerInfo{id: id, slug: slug, kind: kind}
		im.providers[slug] = p
		im.providerByID[id] = p
		c.Imported++
		return nil
	})
	if err != nil {
		return err
	}
	return im.bumpSequence("identity_providers", "oauths")
}

// user_thirds -> user_identities.
func (im *importer) importIdentities() error {
	c := im.count("user_thirds")
	type subjKey struct {
		provider int64
		subject  string
	}
	seen := map[subjKey]int64{}
	return im.eachRow("user_thirds", `SELECT id, COALESCE(CAST(user_id AS INTEGER), 0), COALESCE(CAST(open_id AS TEXT), ''),
		COALESCE(CAST(name AS TEXT), ''), COALESCE(CAST(username AS TEXT), ''), COALESCE(CAST(email AS TEXT), ''),
		CAST(verified_email AS TEXT), COALESCE(CAST(picture AS TEXT), ''), COALESCE(CAST(union_id AS TEXT), ''),
		COALESCE(CAST(third_type AS TEXT), ''), COALESCE(CAST(oauth_type AS TEXT), ''), COALESCE(CAST(op AS TEXT), ''),
		CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM user_thirds ORDER BY id`, func(r *sql.Rows) error {
		var (
			id, userID                                                                int64
			openID, name, username, email, picture, unionID, thirdType, oauthType, op string
			verified, created, updated                                                sql.NullString
		)
		if err := r.Scan(&id, &userID, &openID, &name, &username, &email, &verified, &picture, &unionID,
			&thirdType, &oauthType, &op, &created, &updated); err != nil {
			return err
		}
		if im.users[userID] == nil {
			c.Orphaned++
			im.rep.warn("orphan_identity", "user_thirds", id, "identity references missing user %d; dropped", userID)
			return nil
		}
		var p providerInfo
		found := false
		for _, key := range []string{op, oauthType, thirdType} {
			if key == "" {
				continue
			}
			if p, found = im.providers[key]; found {
				break
			}
		}
		if !found {
			c.Orphaned++
			im.rep.warn("orphan_identity", "user_thirds", id,
				"identity of user %d references unknown provider (op %q, oauth_type %q, third_type %q); dropped", userID, op, oauthType, thirdType)
			return nil
		}
		if openID == "" {
			c.Skipped++
			im.rep.warn("empty_subject", "user_thirds", id, "identity of user %d has an empty open_id; dropped", userID)
			return nil
		}
		k := subjKey{p.id, openID}
		if prev, dup := seen[k]; dup {
			c.Deduped++
			im.rep.warn("duplicate_identity", "user_thirds", id,
				"subject already linked by identity %d for provider %q; dropped (legacy used the first)", prev, p.slug)
			return nil
		}
		seen[k] = id
		if p.kind == "google" && strings.Contains(openID, "@") {
			im.rep.warn("google_email_subject", "user_thirds", id,
				"Google identity of user %d uses an email as subject (pre-v245 row); it will not match Google's `sub` "+
					"on the next login, so the user must re-link the account once (admin relink)", userID)
		}
		cr, up := im.stamps("user_thirds", created, updated)
		if _, err := im.exec(`INSERT INTO user_identities (user_id, provider_id, subject, email, email_verified, display_name,
			username, avatar_url, legacy_union_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			userID, p.id, openID, nullStr(strings.ToLower(strings.TrimSpace(email))), boolInt(parseBool(verified)),
			name, username, picture, nullStr(unionID), cr, up); err != nil {
			return err
		}
		im.userHasIdent[userID] = true
		c.Imported++
		return nil
	})
}

// user_tokens -> auth_tokens (unexpired only; sha256 stored).
func (im *importer) importTokens() error {
	c := im.count("user_tokens")
	type tok struct {
		id, userID, expiredAt int64
		deviceUUID, deviceID  string
		token                 string
		created               sql.NullString
		jwtExp                sql.NullInt64
		isJWT                 bool
	}
	// Collect first: identical token strings (two logins in the same second
	// mint the same JWT) are deduplicated keeping the latest expiry.
	var keep []*tok
	byToken := map[string]*tok{}
	err := im.eachRow("user_tokens", `SELECT id, COALESCE(CAST(user_id AS INTEGER), 0), COALESCE(CAST(device_uuid AS TEXT), ''),
		COALESCE(CAST(device_id AS TEXT), ''), COALESCE(CAST(token AS TEXT), ''), COALESCE(CAST(expired_at AS INTEGER), 0),
		CAST(created_at AS TEXT) FROM user_tokens ORDER BY id`, func(r *sql.Rows) error {
		t := &tok{}
		if err := r.Scan(&t.id, &t.userID, &t.deviceUUID, &t.deviceID, &t.token, &t.expiredAt, &t.created); err != nil {
			return err
		}
		u := im.users[t.userID]
		switch {
		case u == nil:
			c.Orphaned++
			return nil
		case t.token == "":
			c.Skipped++
			c.extra("empty", 1)
			return nil
		case !u.active:
			c.Skipped++
			c.extra("disabled_user", 1)
			return nil
		case t.expiredAt*1000 <= im.nowMs:
			c.Skipped++
			c.extra("expired", 1)
			return nil
		}
		if strings.Count(t.token, ".") == 2 {
			claims, err := auth.ParseUnverified(t.token)
			if err == nil {
				t.isJWT = true
				if claims.ExpiresAt != nil {
					t.jwtExp = sql.NullInt64{Int64: claims.ExpiresAt.Unix() * 1000, Valid: true}
					if t.jwtExp.Int64 <= im.nowMs {
						c.Skipped++
						c.extra("expired_jwt", 1)
						return nil
					}
				} else {
					im.rep.warn("jwt_without_exp", "user_tokens", t.id, "JWT has no exp claim; hbbs rejects it")
				}
				if int64(claims.UserID) != t.userID {
					c.Skipped++
					c.extra("jwt_user_mismatch", 1)
					im.rep.warn("jwt_user_mismatch", "user_tokens", t.id,
						"JWT user_id %d differs from the row's user_id %d; dropped", claims.UserID, t.userID)
					return nil
				}
			}
		}
		if prev, dup := byToken[t.token]; dup {
			c.Deduped++
			if t.expiredAt > prev.expiredAt {
				*prev = *t
			}
			return nil
		}
		byToken[t.token] = t
		keep = append(keep, t)
		return nil
	})
	if err != nil {
		return err
	}
	opaqueClient := 0
	for _, t := range keep {
		kind := "client"
		if t.deviceUUID == "" && t.deviceID == "" {
			kind = "web"
		}
		expires := t.expiredAt * 1000
		var jwtExp, absolute sql.NullInt64
		if kind == "client" {
			jwtExp = t.jwtExp
			if !t.isJWT {
				opaqueClient++
			}
		} else {
			absolute = sql.NullInt64{Int64: expires, Valid: true}
			if t.jwtExp.Valid && t.jwtExp.Int64 < expires {
				absolute.Int64 = t.jwtExp.Int64
			}
		}
		created, ok := im.ms("user_tokens", t.created)
		if !ok {
			created = im.nowMs
		}
		if _, err := im.exec(`INSERT INTO auth_tokens (user_id, kind, token_sha256, jwt_expires_at, expires_at,
			absolute_expires_at, device_rustdesk_id, device_uuid, created_at, legacy_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			t.userID, kind, crypto.HashToken(t.token), jwtExp, expires, absolute, nullStr(t.deviceID),
			nullStr(t.deviceUUID), created, t.id); err != nil {
			return err
		}
		c.Imported++
		effective := expires
		if t.jwtExp.Valid && t.jwtExp.Int64 < effective {
			effective = t.jwtExp.Int64
		}
		if effective-im.nowMs < tokenExpiryWarnWindow.Milliseconds() {
			im.rep.warn("token_expires_soon", "user_tokens", t.id, "%s token of user %d expires at %s (within 7 days)",
				kind, t.userID, time.UnixMilli(effective).UTC().Format(time.RFC3339))
		}
	}
	if opaqueClient > 0 {
		c.extra("opaque_client_tokens", opaqueClient)
		im.rep.warn("opaque_client_tokens", "user_tokens", nil,
			"%d unexpired client token(s) are opaque md5 tokens (legacy ran without RUSTDESK_API_JWT_KEY); "+
				"the new server requires JWT client tokens, so these devices must log in again", opaqueClient)
	}
	return nil
}
