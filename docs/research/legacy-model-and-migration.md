# Legacy lejianwen/rustdesk-api v2.7: data model, auth semantics, and migration plan

Status: research note (2026-10-15). No implementation code.

Sources examined:
- `lejianwen/rustdesk-api` tag **v2.7** = commit `222792419f8b55c17b36bf5f423ec5ed7a53e2c4` (2025-09-28). `master` (`c5687e1`, 2025-09-29) differs only by a single README line, so everything below applies to both.
  Local clone: `C:\Users\bropi\AppData\Local\Temp\claude\F--bropi-Projects\0ba6621a-b09d-4d90-b695-21e6da920e81\scratchpad\src\rustdesk-api`
- `lejianwen/rustdesk-api-web` master `3998c2a` (2025-08-31), cloned next to it as `rustdesk-api-web`.
- Production schema: `docs/research/legacy-rustdeskapi-schema.sql`.
- Our hbbs fork: `F:\bropi\Projects\rustdesk-server\src\jwt.rs`, `src\rendezvous_server.rs`.

All `file:line` references below point into the rustdesk-api v2.7 tree unless another repo is named.

Stack facts that affect migration:
- ORM: GORM v1.25.10 with `gorm.io/driver/sqlite` v1.5.6, which uses **mattn/go-sqlite3 (CGO)** (`go.mod:33,73`). The DB file is `./data/rustdeskapi.db` (`lib/orm/sqlite.go:13`).
- The schema comes only from `AutoMigrate` (`cmd/apimain.go:289-313`). The `versions` table records the schema version, which is `DatabaseVersion = 265` (`cmd/apimain.go:26`). Data fix-ups exist only for versions below 245 and below 246 (`cmd/apimain.go:268-285`).
- Configuration comes from viper with the env prefix `RUSTDESK_API_`. Dots and dashes in keys become `_` (`config/config.go:66-69`). Examples: `RUSTDESK_API_JWT_KEY`, `RUSTDESK_API_JWT_EXPIRE_DURATION`, `RUSTDESK_API_APP_TOKEN_EXPIRE`.

---

## 1. Password hashing

**Current format: bcrypt, cost 10, with lazy upgrade from a legacy salted MD5.**

- `utils/password.go:10-16` `EncryptPassword` calls `bcrypt.GenerateFromPassword(pw, bcrypt.DefaultCost)` (cost 10). The stored value is the standard modular-crypt string `$2a$10$<53 chars>`, 60 chars in total. The string carries no version tag of its own; the `$2a$` prefix is the only marker.
- `utils/password.go:21-42` `VerifyPassword(hash, input)` works as follows:
  1. It runs `bcrypt.CompareHashAndPassword`. On success it returns `ok`.
  2. If bcrypt fails with `InvalidHashPrefixError` or `ErrHashTooShort` (the stored value is not bcrypt), it falls back to **`hash == md5_hex(input + "rustdesk-api")`** (`password.go:30`, `utils/tools.go:13-16` `Md5` = lowercase hex). On a match it also returns a new bcrypt hash.
  3. A mismatch returns false.
- Login path: `service/user.go:48-71` `InfoByUsernamePassword`:
  - When LDAP is enabled, it tries an LDAP bind first and falls back to the local DB on failure (`:49-56`).
  - It loads the user by exact `username` and calls `VerifyPassword`.
  - **If a new hash is returned, it rewrites `users.password` to bcrypt** (`:66-69`). That is the lazy MD5-to-bcrypt migration.
- Callers:
  - Client login: `http/controller/api/login.go:57`.
  - Admin login: `http/controller/admin/login.go:67`.
  - Change own password: `http/controller/admin/user.go:249`. The old password is skipped when the stored password is empty.
  - CLI: `reset-admin-pwd` and `reset-pwd` (`cmd/apimain.go:51-100`), both via `UpdatePassword`, which also deletes all of that user's tokens (`service/user.go:280-292`).

**Formats that can still appear in an old DB:**

| Stored value | Meaning | How to verify |
|---|---|---|
| `$2a$10$…` (60 chars) | bcrypt, the normal case | bcrypt. Go's `x/crypto/bcrypt` also accepts `$2b$`/`$2y$`. |
| 32 lowercase hex chars | **legacy** `md5(password + "rustdesk-api")`, from older releases; only rehashed on that user's next successful password login | md5 fallback, then rehash to bcrypt on success |
| `""` (empty) | user created by OAuth auto-register (`service/user.go:362-367`) or LDAP (`service/ldap.go:209-217`, "you don't store LDAP user passwords locally"). Password login is impossible: bcrypt returns `ErrHashTooShort`, then the md5 check fails. | Treat as "no local password". Never let an empty input match. |

Other password facts:
- Client login form validation is `username 2..32`, `password 4..32` (`http/request/api/user.go:37-38`). A password longer than 32 characters can be set in the admin panel but **cannot log in from the RustDesk client**. Our new server must not reintroduce that cap. bcrypt's own limit is 72 bytes.
- An auto-generated first admin password is logged in clear text once at bootstrap (`cmd/apimain.go:347-348`).

**Migration:** copy `users.password` verbatim into `password_hash`. Set `password_algo` by sniffing: `^\$2[aby]\$` gives `bcrypt`, `^[0-9a-f]{32}$` gives `legacy_md5_rustdesk_api`, an empty string gives `NULL` (no password). Implement the same verify-then-rehash fallback for `legacy_md5_rustdesk_api` so that no user notices. Re-hashing with bcrypt or argon2id after the first successful login is fine.

---

## 2. Client tokens (`user_tokens`)

### Creation
`service/user.go:97-113` `Login(u, llog)` is used by every login path: client password login, client OIDC, admin password, admin OIDC, and register-then-login.
- `token = GenerateToken(u)` (`service/user.go:89-94`):
  - **If `jwt.key` / `RUSTDESK_API_JWT_KEY` is non-empty**, the token is an **HS256 JWT** `{"user_id": <uint>, "exp": <unix>}` (`lib/jwt/jwt.go:26-44`). `exp` = now + `jwt.expire-duration` (`RUSTDESK_API_JWT_EXPIRE_DURATION`, default `168h` in `conf/config.yaml:63`). The header is the golang-jwt default `{"alg":"HS256","typ":"JWT"}`. `RegisteredClaims` fields other than `exp` are omitted (`omitempty`).
  - **Otherwise** the token is `md5_hex(username + time.Now().String())`, an opaque 32-hex string.
- A row is inserted with these values:
  - `user_id`.
  - `token` (stored **in plain text**).
  - `device_uuid` = the client-sent `uuid`.
  - `device_id` = the client-sent RustDesk `id`.
  - `expired_at` = now + `app.token-expire` (`RUSTDESK_API_APP_TOKEN_EXPIRE`, default `168h`; if 0, 604800 s) as **unix seconds** (`service/user.go:490-497`).
- A `login_logs` row is inserted. **Bug:** `llog.UserTokenId = ut.UserId` (`service/user.go:107`), so `login_logs.user_token_id` actually holds the **user id**, not the token id.
- If `uuid != ""`, the call `PeerService.UuidBindUserId` sets `peers.user_id` on the peer row with that uuid (`service/peer.go:36-52`). This binds the device to the user.
- Admin panel logins pass `Uuid: ""` and no device id (`http/controller/admin/login.go:89-96`). **Admin sessions are rows in the same `user_tokens` table with empty `device_uuid` and `device_id`.**

### Validation of the client `Authorization` header (`http/middleware/rustauth.go`)
1. `token := c.GetHeader("Authorization")` (`:13`). An empty header returns `401 {"error":"Unauthorized"}`.
2. A header of `len <= 7` returns 401 (`:21`). Otherwise **`token = token[7:]`** (`:30`). The `Bearer ` prefix is not checked; any 7-character prefix is stripped. Clients send `Bearer <token>`.
3. If the JWT key is set (`:35-44`), it calls `Jwt.ParseToken(token)`. golang-jwt v5 validates the signature and `exp`. **A JWT that is invalid or expired returns 401 even when the DB row is still valid.** If the key is empty, this step is skipped.
4. `InfoByAccessToken(token)` (`service/user.go:74-86`) runs `SELECT … FROM user_tokens WHERE token = ?` (exact, plain-text match on the indexed column). It fails if there is no row or `expired_at < now`. It then loads the user by `user_id`.
5. A user with `status != 1` returns 401 (`:54-61`).
6. It sets `curUser` and `token` in the context. It calls `AutoRefreshAccessToken(ut)` (`service/user.go:504-508`): **if `expired_at - now < TokenExpire/3`** (written as `TokenExpire.Milliseconds()/3000`, i.e. one third of the lifetime in seconds), `expired_at` slides to now + TokenExpire. If `app.token-expire` is 0, the threshold is 0 and the token never auto-refreshes.

Consequences:
- **Expiry has two layers.** `user_tokens.expired_at` slides. When JWT mode is on, the JWT `exp` is **fixed** at issue time + `jwt.expire-duration`. The effective client session lifetime is therefore `min(JWT exp, sliding DB expiry)`, which is normally 7 days after login regardless of activity. After that both `/api/*` and hbbs PunchHole reject the token and the client must log in again.
- Logout (`POST /api/logout`) deletes the matching row and unbinds the peer's `user_id` by uuid (`service/user.go:197-207`).
- Device-based revocation:
  - Deleting a peer in the admin panel deletes every token with that `device_uuid` (`service/peer.go:113-121,140-148`).
  - Changing a password deletes all of the user's tokens (`service/user.go:290`).
  - The admin "User Token" page can delete individual rows (`http/router/admin.go:376-382`).
- Tokens are never garbage-collected, which explains 58 rows for 2 users. Expired rows remain.

### Admin-panel session token vs. client token
| | Client (RustDesk app / web client) | Admin panel (Vue SPA) |
|---|---|---|
| Header | `Authorization: Bearer <t>` | `api-token: <t>` (`rustdesk-api-web/src/utils/request.js` interceptor; `http/middleware/admin.go:14`) |
| Middleware | `RustAuth` (JWT check if key set, then DB) | `BackendUserAuth`: **DB lookup only, no JWT verification** (`admin.go:20`), so admin sessions use only the sliding `expired_at`. Then `AdminPrivilege` checks `is_admin` for admin routes (`admin_privilege.go`). |
| Storage | `user_tokens` row with device uuid/id | `user_tokens` row with empty device fields |
| Token value | identical generator (JWT or md5) | identical generator |
| Client-side storage | RustDesk `LocalConfig access_token` | `localStorage['access_token']` **and** `localStorage['wc-option:local:access_token']` (shared with the bundled Flutter web client; `rustdesk-api-web/src/utils/auth.js` `setToken`) |
| Failure | HTTP 401 `{"error":"Unauthorized"}` | HTTP 200 with body `{"code":403,...}`. The SPA wipes the token and reloads. |

A token minted for one side works on the other side, because both look up the same table.

**Distinguishing them during migration:** there is no `kind` column. Use this heuristic: `device_uuid = '' AND device_id = ''` marks an admin/web session; anything else is a client. The `login_logs.user_token_id` join cannot be used because of the bug above.

---

## 3. JWT and hbbs compatibility

- Generation: `lib/jwt/jwt.go:26-44`. HS256, key = raw bytes of `RUSTDESK_API_JWT_KEY` (no base64 decoding). Claims `{"user_id": N, "exp": <unix seconds>}` with `user_id` as a JSON number. There is no `iat`, `sub`, `iss`, or `aud`.
- Our hbbs fork, `F:\bropi\Projects\rustdesk-server\src\jwt.rs`:
  - `SECRET` = env `RUSTDESK_API_JWT_KEY` (`:5-6`).
  - `Claims { user_id: u32, exp: usize }`, and `verify_token` uses `Validation::new(HS256)` (`:34-53`). The default `jsonwebtoken` validation already requires and validates `exp` (60 s leeway), and the code checks `exp > now` again.
  - `rendezvous_server.rs:903-923`:
    - If `MUST_LOGIN` is set and `PunchHoleRequest.token` is empty, it returns "Connection failed, please login!".
    - If `SECRET` is non-empty, it calls `verify_token`, and a failure returns "Token error, please log out and log back in!".
    - **If `SECRET` is empty, any non-empty token passes.**
  - `MUST_LOGIN` can be toggled at runtime with the `must-login`/`ml` admin command (`:1310-1318`), which the lejianwen panel exposes in `views/rustdesk/must_login.vue`.
- **The client token is the JWT.** The `access_token` returned by `/api/login` and `/api/oidc/auth-query` (`http/controller/api/login.go:87-91`, `ouath.go:132-136`) is exactly `user_tokens.token`. The RustDesk client stores it as `access_token` and puts that same string into `PunchHoleRequest.token` when connecting. No separate hbbs token exists.
- **Compatibility requirements for the new server:**
  1. Keep `RUSTDESK_API_JWT_KEY` byte-identical to production. If it differs, hbbs rejects every migrated JWT, and the new API will reject them too if it verifies them.
  2. New client tokens must remain **HS256 JWTs with `{"user_id": <u32>, "exp": <unix>}`**, signed with that key, while hbbs `MUST_LOGIN` is in use. Our user ids must therefore stay `<= 2^32-1` and be **the same numeric ids as legacy**, because migrated JWTs embed the legacy `user_id`.
  3. Extra claims (`jti`, `iat`, `kind`) are safe. `jsonwebtoken` ignores unknown fields when deserialising `Claims`. Adding a `jti` lets us key the DB row by `jti` instead of the full token.
  4. hbbs cannot check revocation. A revoked JWT keeps passing PunchHole until its `exp`. Pick a sensible `exp` (legacy: 7 days, not refreshable) and document it.
  5. **Check before cutover:** whether production actually sets `RUSTDESK_API_JWT_KEY`. Inspect the 58 tokens: values starting with `eyJ` (3 dot-separated parts) mean JWT mode, and 32-hex values mean md5 mode. Both may coexist if the key was added later. Under hbbs MUST_LOGIN with an empty secret, md5 tokens are accepted by hbbs as long as they are non-empty.

---

## 4. OAuth / OIDC

### `oauths` table (provider configs) — `model/oauth.go:38-51`
| column | meaning |
|---|---|
| `op` | provider **instance name / slug**, unique by convention only (no DB constraint; `InfoByOp` takes the first match). Forced to `github`/`google`/`linuxdo` for those types; defaults to `oidc` for OIDC but may be any custom string, e.g. `authentik` (`FormatOauthInfo`, `model/oauth.go:54-87`). This is the key the client sees (`oidc/<op>` in login-options) and the key in `user_thirds.op`. |
| `oauth_type` | `github`, `google`, `oidc`, `linuxdo` (`webauth` is a pseudo-type and never stored) (`model/oauth.go:13-17`) |
| `client_id`, `client_secret` | OAuth app credentials, **secret in plain text** |
| `auto_register` | nullable bool (numeric). NULL makes the callback **panic** (`ouath.go:227` derefs `*AutoRegister`). |
| `scopes` | **comma-separated** string. Empty means `openid,profile,email` (`service/oauth.go` `constructScopes`). Ignored for github (fixed `read:user,user:email`) and linuxdo (`profile`). |
| `issuer` | OIDC issuer URL (discovery via go-oidc). Google is forced to `https://accounts.google.com`. |
| `pkce_enable` | nullable bool. `pkce_method` = `S256` (default) or `plain`. |

### `user_thirds` (identity links) — `model/userThird.go:7-26`, embedding `OauthUser` (`model/oauth.go:89-96`)
| column | meaning |
|---|---|
| `user_id` | local user |
| `open_id` | **provider subject**: OIDC/Google = `sub`; GitHub = numeric user id as string; linux.do = numeric id. **Legacy (before v245) Google rows used the email as open_id**. The v245 migration copied it into `users.email` (`cmd/apimain.go:274-281`) so the email-matching fallback can relink. |
| `name`, `username`, `email`, `verified_email`, `picture` | snapshot of IdP profile at link time (email lower-cased) |
| `union_id` | unused (always `''`) |
| `third_type` | **deprecated** pre-v245 type. The v245 fix-up copied it into `oauth_type` and `op` (`cmd/apimain.go:273`). |
| `oauth_type` | provider type at link time |
| `op` | provider instance slug. **Lookup key is `(op, open_id)`** (`service/oauth.go:356-360`). |

### Client OIDC flow (RustDesk desktop/mobile)
1. `GET /api/login-options` (`api/login.go:103-123`) returns a JSON array: `["common-oidc/[{\"name\":\"<op>\"},...]", "oidc/<op>", ...]`. When `app.web-sso` is true, the pseudo provider `webauth` is appended.
2. `POST /api/oidc/auth` with body `{op, id, uuid, deviceInfo:{name,os,type}}` (`api/ouath.go:29-61`; `request/api/oauth.go:3-8`):
   - It calls `BeginAuth(op)` (`service/oauth.go:98-130`). `state = RandomString(10) + unix_ts`. A random `nonce` is added as an auth param. With PKCE, a verifier and `S256`/`plain` challenge are used, plus `access_type=offline`.
   - For `op == "webauth"` the URL is `<api-server>/_admin/#/oauth/<state>` and no IdP is involved.
   - **The redirect URI is always `Config.Rustdesk.ApiServer + "/api/oidc/callback"`** (`service/oauth.go:181`). The IdP registrations in production contain exactly this URL, so **the new server must serve `GET /api/oidc/callback` on the same public base URL.** Aliases `/api/oauth/callback`, `/api/oauth/login`, and `/api/oidc/login` map to the same handler (`router/api.go:49-54`).
   - It stores `OauthCacheItem{Action:"login", Id, Op, Uuid, DeviceName, DeviceOs, DeviceType, Verifier, Nonce}` in an **in-process `sync.Map`** with a 5-minute TTL (`service/oauth.go:63-96`). The cache does not survive a restart.
   - It responds with `{"code": state, "url": authURL}`. The client opens `url` in a browser and polls with `code`.
3. The IdP redirects the browser to `GET /api/oidc/callback?state=..&code=..` (`api/ouath.go:148-269`):
   - It loads the cache by `state` and exchanges the code (with the PKCE verifier).
   - If an `id_token` is present, it verifies it via go-oidc and checks the nonce. It then fetches **userinfo** from the provider's userinfo endpoint (`service/oauth.go:235-300`).
   - GitHub also calls `/user/emails` and requires a primary verified email.
   - Login action:
     - It looks up the link with `UserThirdInfo(op, open_id)`.
     - If no link exists and `auto_register` is false, the profile is put into the cache and the browser goes to `/_admin/#/oauth/bind/<state>`. The user logs in to the panel, then `POST /api/admin/oauth/bindConfirm` links the account.
     - If `auto_register` is true, `RegisterByOauth` (`service/user.go:321-376`) runs. If the email is non-empty, it first **links to an existing user with the same email**: LDAP lookup, then local `users.email`. **`verified_email` is not checked.** Otherwise it creates a user with username = lowercased, de-spaced `preferred_username` (or email). Random digits are appended until the name is unique. The new user gets `group_id = 1` and an empty password.
     - It sets `cache.UserId`. Admin-origin requests (`DeviceType == "webadmin"`) redirect to `/_admin/#/`; the client sees `oauth_success.html`.
4. The client polls `GET /api/oidc/auth-query?code=<state>&id=..&uuid=..` (`api/ouath.go:63-137`). While `UserId == 0` it returns HTTP 200 `{"message":"Authorization in progress...","error":"No authed oidc is found"}`. Once set, it deletes the cache entry, calls `UserService.Login` (token creation, `login_logs` row with `type=oauth`, peer binding by uuid), and returns `{access_token, type:"access_token", user:{name,email,note,is_admin,status,info}}`.
   - `id` and `uuid` in the query are **not** compared with the cached values. The code alone is the bearer.
5. **webauth (SSO via panel session):** the client opens `/_admin/#/oauth/<state>`. The logged-in panel user clicks confirm, which calls `POST /api/admin/oauth/confirm {code}` and sets `cache.UserId = current user` (`admin/oauth.go:68-88`). The client's next auth-query receives a token for that user.

### Admin-panel OAuth
- Login page: `GET /api/admin/login-options` returns `{ops, register, need_captcha, disable_pwd, auto_oidc}` (`admin/login.go:159-175`). `POST /api/admin/oidc/auth` is the same as the client flow with `DeviceType:"webadmin"` (`:184-216`). The SPA polls `GET /api/admin/oidc/auth-query`, which returns an admin `LoginPayload` with the token (`:227-242`). The code is held in `localStorage oidc_code` with a 60 s expiry (`rustdesk-api-web/src/utils/auth.js`).
- Bind/unbind in "My info":
  - `POST /api/admin/oauth/bind {op}` (Action `bind` with the current `UserId`). The callback calls `BindOauthUser` after checking that `(op, open_id)` is not linked elsewhere.
  - `POST /api/admin/oauth/unbind {op}` deletes `user_thirds` rows `(user_id, op)`.
  - `POST /api/admin/user/myOauth` lists providers with bind status.
- Admins have CRUD on `oauths` (`/api/admin/oauth/list|detail|create|update|delete`). List and detail **return `client_secret`**.

**Migration constraints for OIDC links:** keep `op` slugs exactly, or keep a mapping table, because user_thirds are keyed by `(op, open_id)`. Keep `open_id` verbatim. Keep `issuer` and `client_id`. Keep the callback path `/api/oidc/callback`. The 2 `oauths` rows and 3 `user_thirds` rows should be inspected by hand: check for pre-v245 Google rows with email-as-open_id, and for `op` values that differ from `oauth_type`.

---

## 5. Address books

### Concepts
- **Personal address book** = `address_books` rows with `collection_id = 0` for a `user_id`, and `tags` rows with `collection_id = 0`.
- **Additional named books ("collections")** = `address_book_collections` (`id, user_id, name`). The owner always has full control.
- **Sharing rules** = `address_book_collection_rules` (`model/addressBook.go:58-79`):
  - `collection_id`.
  - `user_id` = collection owner.
  - `type` 1 = to a **user** (`to_id` = users.id) or 2 = to a **user group** (`to_id` = groups.id).
  - `rule` 1 = read, 2 = read/write, 3 = full control. Effective permission = max(personal rule, group rule) (`service/addressBook.go:233-271`).
  - Production has 0 collections, 0 rules, and 0 tags, so all 34 entries are personal books.
- The **guid** exposed to the client is `"<owner.group_id>-<owner.id>-<collection_id>"`, so a personal book is `"<gid>-<uid>-0"` (`api/ab.go:479-481`). `CheckGuid` (`api/ab.go:484-521`) re-validates that the owner's current group matches. A guid becomes invalid if the user's group changes.
- Personal AB is only advertised when `rustdesk.personal == 1` (`api/ab.go:337-358`). Otherwise the client falls back to the legacy single-blob AB API.

### Client endpoints
- Legacy blob API:
  - `GET /api/ab` returns `{"data": "<json string of {peers:[...], tags:[names], tag_colors:"<json string {name:argb}>"}>"}` (`api/ab.go:32-56`).
  - `POST /api/ab {data:"<same>"}` replaces the personal book (`api/ab.go:69-99`; `service/addressBook.go:55-103`). **Bug:** the diff loads `WHERE user_id = ?` **without `collection_id`**, so it deletes the user's entries in other collections that the payload lacks. It also uses struct `Updates`, so it cannot clear fields to empty or false.
- New API:
  - `POST /api/ab/personal`, `/api/ab/settings` (returns `max_peer_one_ab:0`), and `/api/ab/shared/profiles` (lists own collections with rule 3 plus collections shared to the user or the user's group).
  - `POST /api/ab/peers?ab=<guid>` returns `{total, data:[AddressBook...], licensed_devices:99999}` (page size hard-capped at 1000).
  - `POST /api/ab/tags/<guid>`.
  - `POST /api/ab/peer/add/<guid>`.
  - `DELETE /api/ab/peer/<guid>` (body: JSON array of ids).
  - `PUT /api/ab/peer/update/<guid>`, which only allows `password`, `hash`, `tags`, `alias` (`api/ab.go:710`).
  - `POST /api/ab/tag/add|PUT rename|PUT update|DELETE /<guid>`.
  - Permission checks: read for listing, write for add/update/tag add, full for delete (`api/ab.go`).

### `address_books` row and client peer JSON (`model/addressBook.go:21-41`)
The JSON keys are exactly what the client receives, because rows are serialised directly:

| column | JSON key | meaning |
|---|---|---|
| `row_id` | `row_id` | surrogate PK |
| `id` | `id` | **RustDesk peer ID** (text; default `"0"`) |
| `username` | `username` | OS username on the remote machine |
| `password` | `password` | **plain-text connection password**, used by shared books (client "shared ab password") |
| `hostname` | `hostname` | remote hostname |
| `alias` | `alias` | user-chosen display name |
| `platform` | `platform` | `Windows`/`Linux`/`Mac OS`/`Android`/… (`PlatformFromOs`, `service/addressBook.go:167-181`) |
| `tags` | `tags` | **JSON array of tag names, stored as TEXT** (`custom_types.AutoJson`). Empty or invalid text reads as `[]` (`auto_json.go:30-40`). |
| `hash` | `hash` | **client-computed password hash** for personal books: the saved salted-SHA256 password, an opaque base64 string. Equivalent to a credential, because it allows connecting without typing the password. |
| `user_id` | `user_id` | owner |
| `force_always_relay` | `forceAlwaysRelay` | bool. The client sends `"true"`/`"false"` strings on `peer/add` (`request/api/peer.go:41-58`). |
| `rdp_port`, `rdp_username` | `rdpPort`, `rdpUsername` | RDP port-forward settings |
| `online` | `online` | **client-reported, stale; meaningless server-side** |
| `login_name` | `loginName` | RustDesk account name of the peer's logged-in user |
| `same_server` | `sameServer` | bool |
| `collection_id` | `collection_id` | 0 = personal |
| `created_at`/`updated_at` | same | |

Missing compared with current RustDesk clients: there is **no `note` field**, so notes are lost on round-trip. There is no per-entry `device_group_name`.

### `tags` (`model/tag.go:3-11`)
`name`, `user_id` (book owner), `collection_id` (0 = personal), and `color`. `color` is a **Flutter ARGB uint32** such as `4288585374` = `0xFF9E9E9E`. It is stored as SQLite integer and can exceed int32, so it needs `bigint`. There is no uniqueness constraint. **Entries can reference tag names that have no `tags` row.** Production has 34 entries and 0 tags, so any tags in use exist only inside `address_books.tags`.

---

## 6. Peers and online status

- `peers` is populated **only** by `POST /api/sysinfo` (`api/peer.go:26-63`), which is unauthenticated. The body is `{id, uuid, cpu, memory, os, hostname, username, version}`.
  - It looks up `WHERE id = ?`. If no row exists, it creates one. `user_id` is guessed from the most recent `login_logs` row with the same `(uuid, device_id)` (`service/user.go:399-403`).
  - Otherwise it updates the fields. The update uses struct `Updates`, so empty values do not overwrite.
  - It replies with the text `SYSINFO_UPDATED`.
  - `POST /api/sysinfo_ver` returns `"<resources/version>\n<server start time>"`. The client re-uploads sysinfo when this changes.
  - Admins can also create and edit peers manually (`/api/admin/peer/create|update`).
- `POST /api/heartbeat` (`api/index.go:41-62`), unauthenticated, body `{id, uuid, ver}`:
  - If a peer with that `id` exists and **`now - last_online_time >= 30 s`**, it sets `last_online_time = now()` (unix seconds) and `last_online_ip = ClientIP()`.
  - It always replies `{}`. It never asks the client to re-send sysinfo and never sends `disconnect` or `strategy`.
  - Unknown ids are ignored, so a device appears only after its first sysinfo.
  - The uuid is not checked against the row.
- **Last online:** `peers.last_online_time` (integer unix seconds, UTC) and `peers.last_online_ip`.
- **"Online" in the panel** is computed in the browser: `now - last_online_time < 60` s gives a green dot, otherwise red (`rustdesk-api-web/src/views/peer/index.vue:84`, `views/my/peer/index.vue:46`). The admin list filter `time_ago` is `>0` for "offline longer than N s" and `<0` for "seen within N s" (`http/controller/admin/peer.go:94-101`).
- `peers.user_id` = owner/bound user. It is set at login by uuid (`UuidBindUserId`), cleared at logout and at user deletion (`service/peer.go:55-65`), or guessed from login_logs.
- `peers.group_id` = **device group** (`device_groups.id`), not the user group (`api/group.go:97-115`).
- `peers.alias` = admin-set alias.
- `GET /api/peers` (client "Accessible devices" tab) returns:
  - for admins and members of a `type=2` (share) user group: all peers owned by users of the same user group;
  - otherwise: own peers.
  - The response contains no status/online info.

---

## 7. Every table: meaning, then migration mapping

### 7.1 Column semantics (legacy)

**`versions`**: `version` = schema version (expect 265). Migration metadata only.

**`users`** (`model/user.go`)

| col | meaning |
|---|---|
| id | PK. **Must be preserved** (embedded in JWTs, AB guids, all FKs). |
| username | unique, lowercased and de-spaced only when created via admin/OAuth. Exact-match login. |
| email | non-unique index, may be `''`, used for OAuth auto-linking |
| password | see §1 |
| nickname, avatar | display name and avatar URL (from OAuth) |
| group_id | user group (`groups.id`), default 1 |
| is_admin | numeric bool 0/1 |
| status | 1 = enabled, 2 = disabled (the client API also shows 2 as "status"; RustDesk expects 0 = disabled, 1 = normal, -1 = unverified) |
| remark | admin note |

**`user_tokens`**: see §2. `expired_at` is unix seconds.

**`groups`** (user groups): `name`, `type` 1 = default (members see only themselves), 2 = share (members see each other's devices and users in the client). Production has 2 rows: "Default group" (type 1) and "Share group" (type 2), localized names created at bootstrap (`cmd/apimain.go:318-335`).

**`device_groups`**: `name`, referenced by `peers.group_id`.

**`peers`**: see §6. Other fields:
- `id` = RustDesk ID. It is **not unique**: duplicates are possible from races or manual creation.
- `uuid` = client machine uuid (base64).
- `cpu`, `memory`, `os` = free text from sysinfo.
- `username` = OS user.
- `version` = client version.

**`address_book_collections`**, **`address_book_collection_rules`**, **`address_books`**, **`tags`**: see §5.

**`oauths`**, **`user_thirds`**: see §4.

**`login_logs`** (`model/loginLog.go`)

| col | meaning |
|---|---|
| user_id | the user who logged in |
| client | `webadmin`, or the RustDesk-sent `deviceInfo.type` (e.g. `client`), or `webclient` when a Referer header was present |
| device_id | RustDesk ID |
| uuid | device uuid |
| ip | gin ClientIP (spoofable, see §8) |
| type | `account` or `oauth` |
| platform | client-sent OS string |
| user_token_id | **actually holds user_id (bug)** |
| is_deleted | 0/1 soft-delete, hiding the row from "My login logs" |

**`share_records`** (web-client share links, 0 rows):
- `user_id`, `peer_id`.
- `share_token` = UUIDv4, the bearer secret.
- `password_type` = `once` or `fixed`.
- `password` = **plain-text peer connection password**.
- `expire` = **seconds after `created_at`** (0 = never) (`api/webClient.go:60-67`).

**`audit_conns`**: posted by the controlled RustDesk client to `POST /api/audit/conn` (unauthenticated).
- `action`: `new`, `close`, or `""` (later update with peer info).
- `conn_id`: the client-local connection counter. It is not globally unique; the row is located by `(peer_id, conn_id)`, which can collide after a client restart.
- `peer_id`: the controlled device's RustDesk ID.
- `from_peer`, `from_name`: the controller's ID and name, from `peer[0]` and `peer[1]`.
- `ip`: the controller IP as seen by the controlled side.
- `session_id`: a **u64 parsed as float64 then printed**, so values above 2^53 have already lost precision (`request/api/audit.go:16,30`).
- `type`: connection type from the client: 0 remote desktop, 1 file transfer, 2 port forward, 3 view camera, 4 terminal. These follow RustDesk's `AuthConnType` and should be checked against the client version.
- `uuid`: the controlled side's uuid.
- `close_time`: unix seconds, 0 = still open or unknown.
- The start time is `created_at`.

**`audit_files`**: `POST /api/audit/file` (unauthenticated).
- `peer_id`: the device whose files were touched.
- `from_peer`: the controller id.
- `info`: **raw JSON text from the client** containing `{ip, name, num, files:[...]}`. `ip`, `from_name`, and `num` are denormalised from it.
- `is_file`: bool.
- `path`.
- `type`: 0 = remote→local / 1 = local→remote, following RustDesk's `FileAuditType`; to be verified.
- `uuid`.

**`server_cmds`** (0 rows): custom hbbs/hbbr console commands.
- `cmd`, `alias`, `option`, `explain`.
- `target` = `"21115"` (hbbs) or `"21117"` (hbbr), stored as a **port string used as an enum**. The commands are sent over plain TCP to `[::1]` or `127.0.0.1` at `id-server-port - 1` and `relay-server-port` (`admin/rustdesk.go:104-131`, `service/serverCmd.go:43-90`).

### 7.2 Data-type hazards (all tables)

- **`created_at` / `updated_at` (`timestamp`)** are written by mattn/go-sqlite3 as TEXT in format **`2006-01-02 15:04:05.999999999-07:00`**: local wall time **with an explicit offset**. The container's `TZ` decides the offset; `docker-compose.yaml:6` ships `TZ=Asia/Shanghai`, so expect `+08:00`, or `+00:00` if TZ is unset.
  - The offset makes conversion to UTC lossless.
  - The parser must accept: an optional fractional part; offsets like `+08:00` and `Z`; formats without an offset, which should be assumed to be the configured legacy TZ (rows written by other tools, e.g. a manual sqlite3 edit); a `T` separator; and **NULL** (`AutoTime` writes NULL for zero time, `custom_types/auto_time.go:11-18`).
  - The legacy JSON API printed `"2006-01-02 15:04:05"` without a zone (`auto_time.go:20-24`).
- **Unix-second integers:** `user_tokens.expired_at`, `peers.last_online_time`, `audit_conns.close_time`. These are already UTC. 0 means unset or never.
- **Duration integer:** `share_records.expire` (seconds relative to `created_at`).
- **Booleans as `numeric`:** `users.is_admin`, `address_books.force_always_relay|online|same_server`, `audit_files.is_file`, `user_thirds.verified_email` (nullable), `oauths.auto_register|pkce_enable` (nullable). Values are 0/1. Treat NULL as false. Defensively accept `'true'`/`'false'` text as well.
- **Integer enums:** `users.status` (1/2), `groups.type` (1/2), `address_book_collection_rules.type` (1/2) and `.rule` (1..3), `audit_*.type`, `login_logs.is_deleted`.
- **JSON in TEXT:** `address_books.tags` (array of strings, may be `''`, `null`, or invalid, which read as `[]`), `audit_files.info` (object, may be invalid). **Comma lists:** `oauths.scopes`.
- **Large unsigned:** `tags.color` (uint32 ARGB, up to 4294967295), which needs `bigint`.
- **Soft references without FKs:** every `*_id` column; `0` means none. Expect orphans, such as tokens or address book rows of deleted users and rules pointing at deleted collections.
- **No uniqueness** on `peers.id`, on `(user_id, collection_id, address_books.id)`, on `(user_id, collection_id, tags.name)`, or on `oauths.op`. Dedupe during migration.
- **Plain-text secrets:** `user_tokens.token`, `oauths.client_secret`, `address_books.password|hash`, `share_records.password|share_token`.

### 7.3 Proposed clean schema (normalised, timestamps `timestamptz`/UTC)

Conventions:
- `id bigint` PKs. **Legacy ids are preserved** for `users`, `user_groups`, `address_books` (collections), `device_groups`, and `identity_providers`, so that JWTs and guids stay valid.
- Every table has `created_at`/`updated_at timestamptz NOT NULL`.
- Secrets are encrypted at rest with an app key (AEAD) unless they only need to be verified, in which case they are hashed.

```
user_groups(id, name, visibility ENUM('self','shared'), created_at, updated_at)
users(id, username CITEXT UNIQUE, email CITEXT NULL, display_name, avatar_url, note,
      password_hash NULL, password_algo ENUM('bcrypt','legacy_md5_rustdesk_api','argon2id') NULL,
      is_admin bool, status ENUM('active','disabled'), user_group_id FK,
      last_login_at NULL, created_at, updated_at)
auth_tokens(id, user_id FK, kind ENUM('client','web_admin'),
      token_sha256 BYTEA UNIQUE,          -- lookup key; never store plaintext
      jwt_jti NULL, jwt_expires_at NULL,  -- fixed JWT exp (hbbs view)
      expires_at timestamptz,             -- sliding expiry (API view)
      device_rustdesk_id NULL, device_uuid NULL, client_platform NULL,
      created_at, last_used_at, revoked_at NULL, legacy_id NULL)
identity_providers(id, slug UNIQUE /*legacy op*/, kind ENUM('github','google','oidc','linuxdo'),
      display_name, client_id, client_secret_enc, issuer NULL, scopes TEXT[],
      pkce_enabled bool, pkce_method ENUM('S256','plain'), auto_register bool,
      enabled bool, created_at, updated_at)
user_identities(id, user_id FK, provider_id FK, subject /*legacy open_id*/,
      email NULL, email_verified bool, display_name, username, avatar_url,
      legacy_union_id NULL, created_at, updated_at, UNIQUE(provider_id, subject))
device_groups(id, name UNIQUE, created_at, updated_at)
devices(id, rustdesk_id UNIQUE, uuid NULL, hostname, os, cpu, memory, os_username,
      client_version, alias, owner_user_id FK NULL, device_group_id FK NULL,
      last_seen_at timestamptz NULL, last_seen_ip INET NULL, created_at, updated_at)
address_books(id, owner_user_id FK, name, is_personal bool, legacy_collection_id NULL,
      created_at, updated_at, UNIQUE(owner_user_id) WHERE is_personal)
address_book_shares(id, address_book_id FK, grantee_kind ENUM('user','group'), grantee_id,
      permission ENUM('read','read_write','full'), created_at, updated_at,
      UNIQUE(address_book_id, grantee_kind, grantee_id))
address_book_entries(id, address_book_id FK, rustdesk_id, alias, os_username, hostname,
      platform, login_name, note NULL, password_enc NULL, hash_enc NULL,
      force_always_relay bool, rdp_port, rdp_username, same_server bool,
      created_at, updated_at, UNIQUE(address_book_id, rustdesk_id))
address_book_tags(id, address_book_id FK, name, color_argb BIGINT, created_at, updated_at,
      UNIQUE(address_book_id, name))
address_book_entry_tags(entry_id FK, tag_id FK, PRIMARY KEY(entry_id, tag_id))
login_events(id, user_id FK NULL, token_id FK NULL, method ENUM('password','oidc','webauth'),
      client_kind, device_rustdesk_id NULL, device_uuid NULL, platform, ip INET,
      hidden_by_user bool, created_at)
connection_audits(id, device_rustdesk_id, conn_id BIGINT, session_id TEXT, conn_type SMALLINT,
      controller_rustdesk_id, controller_name, controller_ip, device_uuid,
      started_at, ended_at NULL, created_at, updated_at)
file_transfer_audits(id, device_rustdesk_id, controller_rustdesk_id, controller_name,
      controller_ip, direction SMALLINT, is_file bool, path, file_count INT,
      info JSONB, device_uuid, created_at)
share_links(id, user_id FK, device_rustdesk_id, token_sha256 UNIQUE, password_mode ENUM('once','fixed'),
      password_enc, expires_at NULL, created_at, updated_at)
server_commands(id, target ENUM('hbbs','hbbr'), cmd, alias, option_hint, description, created_at, updated_at)
```

### 7.4 Table-by-table migration mapping

| legacy | new | notes / lossy or tricky |
|---|---|---|
| `versions` | none (record source version in a `migration_runs` audit row) | Abort if version ≠ 265 unless explicitly allowed. |
| `groups` | `user_groups` | `type 1→'self'`, `2→'shared'`. Keep ids. |
| `users` | `users` | Keep `id`. `nickname→display_name`, `avatar→avatar_url`, `remark→note`, `group_id→user_group_id`. A `group_id` of 0 or an orphan maps to the default group. `status 1→active, 2→disabled`, anything else becomes disabled (flag it). `is_admin` numeric→bool. Password per §1. `email ''→NULL`. Usernames that collide only by case (unique was case-sensitive): **detect and report**. Do not change the login name silently; use CITEXT only if no collisions exist. |
| `user_tokens` | `auth_tokens` | **Drop rows with `expired_at < now`.** For each kept row: `token_sha256 = sha256(token)`; `kind = web_admin` if device fields are empty, else `client`; `expires_at = to_timestamp(expired_at)`; if the token parses as a JWT (do not require a valid signature at migration time), set `jwt_expires_at = exp` and also drop it if `exp < now`; copy `device_id`/`device_uuid`; `legacy_id = id`. Drop orphans (user missing or disabled). The plain token is never stored. |
| `oauths` | `identity_providers` | `op→slug` **verbatim**, `oauth_type→kind`. Split `scopes` on `,` and trim. Encrypt `client_secret`. NULL bools become false. Keep ids. Check there are no duplicate `op` values. |
| `user_thirds` | `user_identities` | Resolve `provider_id` by `op` (fallback `oauth_type`, then `third_type` for pre-v245 rows). `open_id→subject` **verbatim**. Lowercase `email`. `verified_email` NULL becomes false. Drop orphans. Report Google rows whose subject looks like an email (pre-v245): they will **not** match the new `sub` and will relink by email only if we keep the email-fallback rule, which is not recommended. Instead, offer a one-time admin relink. |
| `device_groups` | `device_groups` | Keep ids (0 rows). |
| `peers` | `devices` | **Dedupe by `id`**: keep the row with the greatest `last_online_time`, then the greatest `updated_at`. Merge non-empty fields and report the duplicates. `last_online_time` > 0 becomes `last_seen_at = to_timestamp(...)`, 0 becomes NULL. Empty `last_online_ip` becomes NULL. `user_id` 0 or orphan becomes NULL. `group_id` 0 becomes NULL. Keep `uuid` (used for token revocation and binding). |
| `address_book_collections` | `address_books` (`is_personal=false`, `legacy_collection_id = id`) | Keep the id. **Also create one personal `address_books` row per user** that owns any personal entries or tags (`legacy_collection_id = 0`), or eagerly for every user. |
| `address_book_collection_rules` | `address_book_shares` | `type 1→user, 2→group`; `rule 1→read, 2→read_write, 3→full`; `rule 0` → drop. Dedupe by keeping the max. Drop orphans. |
| `address_books` | `address_book_entries` | The target book is the personal book of `user_id` if `collection_id = 0`, otherwise the collection. **Dedupe by (book, `id`)**, keeping the latest `updated_at`. `id→rustdesk_id`. Encrypt `password`/`hash` (keep exact bytes; the client needs them back verbatim). `force_always_relay`/`same_server` become bool. **Drop `online`** (stale client value). Parse `tags` JSON (invalid input becomes `[]`), then upsert `address_book_tags` by name (default color for names missing from `tags`), then insert links. `note` becomes NULL (not present in legacy). |
| `tags` | `address_book_tags` | Book resolved as above. `color` becomes bigint. Dedupe by (book, name). Drop orphans (0 rows in production). |
| `login_logs` | `login_events` | `type account→password, oauth→oidc` (webauth logins are also logged as `oauth`, so they are indistinguishable). `client→client_kind`. **`user_token_id` is a user id (bug), so set `token_id = NULL`.** `is_deleted 1→hidden_by_user = true`. Empty or NULL text becomes NULL. Map `created_at` to UTC. |
| `share_records` | `share_links` | `token_sha256 = sha256(share_token)` (only if the web-client share URL lookup is rehashed accordingly; otherwise keep the token encrypted). If `expire > 0`, `expires_at = created_at + expire` seconds. Encrypt `password`. 0 rows in production. |
| `audit_conns` | `connection_audits` | `created_at→started_at`; `close_time > 0 → ended_at`. `session_id` stays TEXT (already lossy above 2^53). Rows with `action=''` are not separate events (the legacy code updates in place), so copy as-is. |
| `audit_files` | `file_transfer_audits` | `info` becomes JSONB if it parses, otherwise store `{"raw": "<text>"}`. `num→file_count`, `type→direction`. |
| `server_cmds` | `server_commands` | `target "21115"→hbbs, "21117"→hbbr`. 0 rows. |
| `sqlite_sequence` | none | Set new sequences to `max(id)+1` after the copy. |

Data volume is tiny (about 1.4k rows), so the migration can be a single transaction with a dry-run and report mode.

---

## 8. Admin panel feature inventory (rustdesk-api-web + `/api/admin/*`)

Routes: `http/router/admin.go`. SPA routes: `rustdesk-api-web/src/router/index.js`. Admins see everything (`AdminRouteNames = ["*"]`). Normal users only see the "My" section (`model/user.go:32-35`).

**Auth and shell**
- Password login with an IP-based failure counter. A captcha appears after `captcha-threshold` failures (default 3), and an IP ban applies after `ban-threshold` failures (default 0 = off), for a 10-minute window and a 30-minute ban (`cmd/apimain.go:206-211`, `utils/login_limiter.go`).
- OIDC/OAuth login buttons. `auto_oidc` redirects automatically when password login is disabled and there is exactly one provider.
- Optional self-registration (`app.register`, with status pending or active).
- `disable-pwd-login`.
- Configurable title and "hello" markdown with `{{username}}` (`admin/config.go:62-97`).
- 7 UI languages, light/dark settings, tag-tabs layout.

**My (every user)**
- Profile: change password, bind or unbind OAuth providers, hello message.
- My devices: peers bound to me, online dot, add to address book.
- My address book collections: create, rename, delete, and share rules to users/groups with read, read/write, or full permission.
- My address book entries: CRUD, batch add from my peers, batch tag edit, open in the web client, create a web-client share link (once or fixed password, with expiry).
- My tags (with colors).
- My share records.
- My login logs (soft-delete).

**System (admin)**
- Devices (peers):
  - List with filters (id, hostname, username, IP, alias, last-seen window) and a column chooser.
  - Create, edit (alias, device group, owner), delete, batch delete. Deleting a device also revokes its tokens.
  - Batch add to any user's address book.
  - CSV export/import (client-side).
  - "Connect via web client" link.
- User groups (default/share), device groups.
- Users: CRUD, reset password, enable/disable, admin flag, remark, jump to that user's address books and tags. The last admin cannot be removed or demoted.
- Address book collections (all users), address book entries (all users, with owner/collection filters), tags (all users).
- OAuth providers: CRUD for GitHub, Google, OIDC, and linux.do; PKCE; auto-register; scopes.
- User tokens: list (masked in UI, full in API), delete, batch delete.
- Login logs, connection audit log, file-transfer audit log: list, delete, batch delete.
- Share records (all users).
- Server control: sends console commands to hbbs/hbbr over local TCP.
  - Presets: relay servers, always-use-relay, must-login, IP blacklist and blocklist, usage.
  - Custom saved commands.
- Bundled web clients: `/webclient` (Flutter, v1) and `/webclient2`. The Vue panel also contains a TS rendezvous client used to check whether a peer is online over WebSocket at `ws(s)://<id_server>:21118`.
- Swagger UI (optional).

### Critique: weak points our version should fix

**Security**
1. **Unauthenticated device endpoints.** `/api/sysinfo`, `/api/heartbeat`, `/api/audit/conn`, and `/api/audit/file` accept anything (`router/api.go:31,60,71-73`).
   - Anyone can create or overwrite any peer's hostname, OS, or uuid.
   - Anyone can mark any id online and set its IP.
   - Anyone can forge audit logs.
   - Overwriting `uuid` also affects token revocation and peer↔user binding.
   - Fix: verify uuid continuity and/or sign with the device key; rate-limit; never trust uuid changes silently.
2. **Proxy trust.** `X-Forwarded-For` is trusted from any proxy by default (`http/http.go:17-24`; `trust-proxy` empty means gin trusts all). This allows bypassing the login limiter and falsifying login-log and last-seen IPs.
3. **Client login is not brute-force-protected by default.** `/api/login` has no captcha, and `ban-threshold` defaults to 0.
4. **Tokens stored in plain text** and returned in full by `/api/admin/user_token/list` (masked only in the UI). There is no `Bearer` prefix check. Admin sessions are not JWT-checked and are kept in `localStorage`, readable by any XSS, together with the bundled web client on the same origin.
5. **OAuth account takeover risk.** Auto-register links an IdP identity to an existing local user **by email without requiring `email_verified`** (`service/user.go:333-353`). A malicious or misconfigured OIDC IdP, or any provider that allows unverified emails, can claim an admin account.
6. **webauth / OIDC poll codes are bearer secrets with no binding.** `auth-query` ignores `id`/`uuid`. webauth lets a logged-in user approve a code that an attacker generated, so a phishing link yields a token for the victim (device-code-phishing pattern). `/api/admin/oauth/info?code=` returns the cache item, including the PKCE verifier, to any logged-in user.
7. **Plain-text secrets at rest:** `oauths.client_secret` (also returned by list/detail), `address_books.password|hash`, and `share_records.password`. Share links are unauthenticated bearer URLs that return the peer password (`api/webClient.go:47-89`).
8. **Server key exposure.** `/api/server-config` returns the hbbs public key and id server (fine). There is no CSRF concern because headers are used. CORS reflects any `Origin` with credentials (`middleware/cors.go`), although that middleware is currently not mounted.
9. **"Hello" markdown rendered with `v-html`** (`rustdesk-api-web/src/views/my/info.vue:34`) with the username substituted. Usernames from OAuth auto-registration are IdP-controlled, which is a stored-XSS vector unless sanitised.
10. **First admin password** is printed to logs. There is no forced change and no 2FA/TOTP: the client's `tfa_check` flow is unimplemented although `LoginRes` has the fields.
11. **OAuth state in an in-memory `sync.Map`.** It is lost on restart, does not scale beyond one instance, and expires via timers. Its absence also means a nil `AutoRegister` panics (`ouath.go:227`).

**Correctness / data model**
1. The legacy `POST /api/ab` deletes entries from other collections (`service/addressBook.go:61`, missing `collection_id`). Struct `Updates` cannot clear fields.
2. `login_logs.user_token_id` stores the user id (`service/user.go:107`).
3. There are no unique constraints: duplicate peers, address book entries, and tags are possible. There are no FKs, so orphans accumulate and tokens are never purged.
4. Two independent expiries (JWT fixed and DB sliding). `TokenExpire = 0` disables refresh. A missing `jwt.expire-duration` would mint already-expired JWTs (`exp = now`).
5. The address book guid embeds the owner's *group id*, so moving a user between groups breaks every client's cached guid.
6. `session_id` is parsed as float64 and loses precision. Audit "close" is matched by a non-unique `conn_id`.
7. Heartbeat ignores unknown ids and never requests sysinfo. It returns `{}`, so newer client features (`modified_at`, `disconnect`, strategies) are unsupported. Online status is a client-side 60-second guess, with no server-side presence and no hbbs integration.
8. User status code 2 = disabled is sent to clients that expect 0 = disabled.
9. Address book entries have no `note` field. Hard cap of 1000 entries per book. `licensed_devices` is hard-coded.
10. Timestamps are stored in local TZ and printed without a zone in the API.
11. Client password field max 32 characters.

**UX**
- Online status is shown as only a dot computed in the browser (no server push, no "last seen" sorting by default).
- Address book management is split into five pages (collections, entries, tags, rules, my×4) with numeric-id selectors.
- Errors are returned as HTTP 200 with `code != 0`. Some error messages are raw untranslated `err.Error()` concatenations.
- There is no audit of admin actions. Admin edits leave no trace.
- No device-to-user assignment UX beyond raw `user_id`. No bulk token revocation per user from the users page.
- Mixed Chinese and English hard-coded labels remain (`views/address_book/index.vue:152` label "在线").

**Code quality**
- Errors are ignored almost everywhere (`DB.Where(...).First(x)` and checking `x.Id == 0`). Transactions are committed without checking per-statement errors (`service/addressBook.go:58-101`, `service/tag.go:377-405`).
- Business logic lives in controllers. `service.AllService` is a global singleton. There is no context propagation and no tests for HTTP flows.
- Reflection-heavy "SafeGo" helpers. The Swagger docs are generated but the behaviour diverges from them.
- The Vue SPA is derived from the `vue-manage-system` template. It has a single giant `views` folder and duplicated "my" versus "admin" pages (e.g. `my/address_book/index.vue` and `indexv2.vue`).

---

## 9. Licenses

- **lejianwen/rustdesk-api**: **MIT**, "Copyright (c) 2024-present Lejianwen and contributors" (`LICENSE`). GitHub API reports `MIT`.
- **lejianwen/rustdesk-api-web**: **MIT** (GitHub API reports `MIT`). The `LICENSE` file still carries the template's notice "Copyright (c) 2016-2021 vue-manage-system", inherited from the upstream template.
- Implication: we may reuse ideas, API shapes, and even code from both repos, provided we keep the MIT notices for any copied code. A clean-room reimplementation needs no attribution, but an acknowledgement in `NOTICE`/README is courteous. The bundled RustDesk Flutter web client in `resources/web` is from RustDesk (AGPL-3.0). **Do not redistribute it** inside our MIT/Apache artifact without an AGPL review.

---

## 10. Must-keep compatibility constraints (summary)

1. **Keep the same user ids.** JWTs embed `user_id` (u32), and address book guids embed `<group_id>-<user_id>-<collection_id>`. Keep group and collection ids too, or at least accept legacy guids.
2. **Keep the same `RUSTDESK_API_JWT_KEY`**, and keep minting HS256 `{user_id, exp}` JWTs as client tokens, because hbbs validates `PunchHoleRequest.token` with that key when `MUST_LOGIN` is on. First check whether production even has the key set: inspect the token format (`eyJ…` versus 32-hex).
3. **Migrate unexpired tokens** (hashed). Accept `Authorization: <any 7 chars><token>` (practically `Bearer `), check DB expiry, keep the sliding refresh (threshold of one third of the lifetime), and honour the JWT `exp`. Admin sessions use the separate `api-token` header semantics, but our new panel may switch to cookies.
4. **Passwords:** accept bcrypt `$2a$` and legacy `md5(pw+"rustdesk-api")` hex, rehash on login, and never allow login with an empty hash. Do not cap password length at 32.
5. **OIDC:** keep provider slugs (`op`), subjects (`open_id`), issuer, client id and secret, and the redirect URI **`<api-server>/api/oidc/callback`**. Keep the `/api/login-options`, `/api/oidc/auth`, and `/api/oidc/auth-query` contract, including the `{"code","url"}` response and the "in progress" response while polling.
6. **Address book entries:** return `password` and `hash` byte-for-byte, keep tags as names and colors as ARGB uint32, and keep the JSON key spellings (`forceAlwaysRelay`, `rdpPort`, `loginName`, `sameServer`).
7. **Timestamps:** convert offset-bearing TEXT to UTC. Unix-second columns are already UTC. Booleans are 0/1 numeric. Parse the JSON-in-TEXT columns leniently.
