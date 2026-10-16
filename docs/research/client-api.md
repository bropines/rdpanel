# RustDesk client <-> API server: wire contract

Status: research notes, written 2026-10-15. Nothing in this document is implementation code.

This document covers every HTTP call the official RustDesk client (Flutter desktop/mobile, plus the legacy Sciter desktop UI) makes to the server configured as **"API Server"**. Where the client source and the reference Go implementation [`lejianwen/rustdesk-api`](https://github.com/lejianwen/rustdesk-api) disagree, the client wins. Each disagreement is marked with **⚠ lejianwen**.

## 0. Sources and citation format

| Alias | Repository | Commit / tag | Notes |
|---|---|---|---|
| `R` | rustdesk/rustdesk | `e400bb75546cebf4f72a5ef3cf6e4a3efa2c3aa7` (master, 2026-10-15, `Cargo.toml` version 1.5.1) | Primary source. |
| `R@<tag>` | rustdesk/rustdesk | tags `1.3.0`, `1.3.9`, `1.4.0`…`1.4.9`, `1.5.0` | Used to check version differences. |
| `L` | lejianwen/rustdesk-api | `c5687e150615bfb00d9d105f0883a2802750a8ad` (master, 2025-09-29; latest release v2.7) | Reference implementation. |
| `H` | rustdesk/hbb_common | `main` (git submodule of R) | Shared helpers and protobuf. |

A citation `R:src/hbbs_http/sync.rs:236-274` maps to
`https://github.com/rustdesk/rustdesk/blob/e400bb75546cebf4f72a5ef3cf6e4a3efa2c3aa7/src/hbbs_http/sync.rs#L236-L274`.
A citation `L:http/controller/api/ab.go:536-557` maps to
`https://github.com/lejianwen/rustdesk-api/blob/c5687e150615bfb00d9d105f0883a2802750a8ad/http/controller/api/ab.go#L536-L557`.

A per-version compatibility audit (1.4.6, 1.5.0, master) with the exact email-code (`email_check`) contract is in [`client-compat-1.4.6.md`](client-compat-1.4.6.md).

The latest client release on 2026-10-15 is **1.5.0** (2026-09-30). The 1.4.x line runs 1.4.0 (2025-05-11) to 1.4.9 (2026-07-06). Master is 1.5.1-dev.

---

## 1. Cross-cutting transport rules

### 1.1 Base URL ("API server") resolution
`get_api_server(api, custom)` (`R:src/common.rs:1222-1260`) resolves the base URL in this order:
1. Windows only: an `api` value embedded in the executable name (custom-client "license").
2. The `api-server` option, if it is set.
3. Otherwise, the custom rendezvous server host with port `21116 - 2 = 21114` over plain `http://` (`http://<host>:21114`).
4. Otherwise `https://admin.rustdesk.com`.

After resolution:
- A trailing `/` is removed.
- `https://…:21114` is rewritten to drop `:21114`, unless the built-in option `allow-https-21114 = Y` is set (`R:src/common.rs:1231-1236`).
- If `Config::no_register_device()` is true, the result is empty and every API feature is off.
- `is_public(url)` (`R:src/common.rs:1262-1273`) is true for `rustdesk.com` and `*.rustdesk.com`. Heartbeat, sysinfo, audit, and switch-grant are skipped when the URL is public.

Path prefixes are preserved. Paginated Flutter calls rebuild the URI from `scheme`, `host`, `port`, and `path` (for example `R:flutter/lib/models/ab_model.dart:441-451`), so an API server under a sub-path such as `https://x/rd/api/...` works. User-info and fragment are dropped.

### 1.2 Authentication header
- Flutter: `getHttpHeaders()` returns `{'Authorization': 'Bearer ' + LocalConfig['access_token']}` (`R:flutter/lib/common.dart:2729-2733`). If the user is not logged in this is sent as `Bearer ` with an empty token.
- Sciter: `"Authorization: Bearer " + access_token` (`R:src/ui/index.tis:1675-1677`).
- **Device-side calls send no Authorization header.** This covers heartbeat, sysinfo, sysinfo_ver, audit/conn, audit/file, audit/alarm, record, switch-grant, oidc/auth, and oidc/auth-query (`post_request(url, body, "")`). The device is identified only by `id` plus `uuid` in the body.
- Deploy and assign send `Authorization: Bearer <admin-issued token>` (`R:src/ui_interface.rs:1103`, `R:src/core_main.rs:562`).

### 1.3 Token storage
- `access_token` is stored in `LocalConfig` under key `access_token`.
- User info is stored as JSON under `user_info` (`R:flutter/lib/models/user_model.dart:138-160`, `R:src/hbbs_http/account.rs:304-320`).
- The token is opaque to the client. Nothing parses it as a JWT.
- **The same `access_token` is also sent to hbbs** in rendezvous `PunchHoleRequest.token` / `RequestRelay.token` (`R:src/ui_session_interface.rs:1984`, `R:src/client.rs:1790-1830`). The OSS hbbs ignores it. A Pro-like "login required to connect" feature would need hbbs to validate this token against the API server.

### 1.4 HTTP plumbing (headers, bodies, timeouts, fallbacks)
- Flutter `HttpService` (`R:flutter/lib/utils/http_service.dart:11-123`):
  - If no headers are given, the default is `{'Content-Type': 'application/json'}` (line 18). This is the case for `/api/login` and `/api/login-options`.
  - On desktop, requests go through Rust (`bind.mainHttpRequest`) when the `enable-flutter-http-on-rust` option is set or a proxy is configured. Otherwise they use Dart `http`, with a 30 s timeout.
  - **DELETE requests carry a JSON body.** The server must read the body on DELETE.
- Several POSTs send **no body** with `Content-Length: 0` (`_setEmptyBody`, `R:flutter/lib/models/ab_model.dart:2028-2031`). The server must not require a JSON body on `/api/ab/personal`, `/api/ab/settings`, `/api/ab/shared/profiles`, `/api/ab/peers`, or `/api/ab/tags/{guid}`.
- Rust `post_request` (`R:src/common.rs:1630-1638`, `1702-1793`):
  - Always sets `Content-Type: application/json`.
  - Uses a 12 s timeout.
  - On a TLS or request error it retries with "accept invalid cert", then with native-tls. A self-signed certificate is therefore silently accepted.
- **Raw-TCP fallback (client ≥ 1.4.7):** on a connection error or HTTP 5xx, the client re-sends the same request over the rendezvous TCP connection to hbbs as protobuf `HttpProxyRequest` (`R:src/common.rs:1407-1471`, `1572-1622`). The fields are in `H:protos/rendezvous.proto` (`HttpProxyRequest{method,path,headers,body}`, `HttpProxyResponse{status,headers,body,error}`). With built-in option `use-raw-tcp-for-api = Y` the client uses the TCP path only. The OSS `rustdesk-server` hbbs does **not** implement `HttpProxyRequest`, so the fallback fails and the original HTTP result is returned. A panel paired with a custom hbbs could implement this.
- Responses are decoded as UTF-8 with `allowMalformed: true` (`R:flutter/lib/common.dart:4216-4225`).

### 1.5 Error conventions the client understands
The common shape is a JSON object `{"error": "<string>"}`.

| Shape / status | Where | Client behaviour |
|---|---|---|
| `{"error": "..."}` (any status) | Everywhere JSON is parsed | Shown to the user. Treated as failure. |
| HTTP **401** | currentUser, ab/*, users, peers, device-group | Logs the user out: `userModel.reset(resetOther: true)` clears the token, `user_info`, the address-book cache, and the group cache (`R:flutter/lib/models/user_model.dart:85-88`, `ab_model.dart:220-228`, `1046-1050`, `1490-1494`, `group_model.dart:57-58`). |
| HTTP **400** on `/api/currentUser` | currentUser | **Also logs out**, but keeps the ab/group caches (`R:flutter/lib/models/user_model.dart:85-88`). ⚠ lejianwen returns 400 for every `response.Error` (`L:http/response/response.go:45-49`). Do not return 400 from currentUser for transient errors. |
| HTTP **404** on `/api/ab/personal` | ab | Switches to **legacy** address-book mode (§3.2). |
| HTTP 404 on `/api/ab/settings` or `/api/ab/shared/profiles` | ab | The step is skipped. |
| `{"error":"No authed oidc is found"}` | oidc/auth-query | Means "keep polling" (substring match). |
| Action endpoints (§5.3) | ab mutations | **Success requires exactly HTTP 200 with an empty body.** Anything else is an error (§5.3.0). |

---

## 2. Endpoint index

The "Since" column gives the earliest client release where the call appears in the tags checked (1.3.0, 1.4.0, 1.4.1-1.4.9, 1.5.0). "≤1.3.0" means it was already present in 1.3.0.

| # | Method & path | Auth | Caller | Since | In lejianwen? |
|---|---|---|---|---|---|
| 1 | `GET /api/login-options` | none | Flutter login dialog; Rust TLS warm-up | ≤1.3.0 | yes |
| 2 | `POST /api/login` | none | Flutter + Sciter | ≤1.3.0 | yes (no 2FA/email) |
| 3 | `POST /api/currentUser` | Bearer | Flutter + Sciter | ≤1.3.0 | yes |
| 4 | `POST /api/logout` | Bearer | Flutter + Sciter | ≤1.3.0 | yes |
| 5 | `POST /api/oidc/auth` | none | Rust (`account.rs`) | ≤1.3.0 | yes |
| 6 | `GET /api/oidc/auth-query` | none | Rust | ≤1.3.0 | yes |
| 7 | `GET /api/ab` | Bearer | Flutter (legacy AB) | ≤1.3.0 | yes |
| 8 | `POST /api/ab` | Bearer | Flutter + Sciter (legacy AB) | ≤1.3.0 | yes |
| 9 | `POST /api/ab/get` | Bearer | Sciter only (legacy AB) | ≤1.3.0 | **no** |
| 10 | `POST /api/ab/personal` | Bearer | Flutter | ≤1.3.0 | yes |
| 11 | `POST /api/ab/settings` | Bearer | Flutter | ≤1.3.0 | yes |
| 12 | `POST /api/ab/shared/profiles?current&pageSize` | Bearer | Flutter | ≤1.3.0 | yes |
| 13 | `POST /api/ab/peers?current&pageSize&ab` | Bearer | Flutter | ≤1.3.0 | yes |
| 14 | `POST /api/ab/tags/{guid}` | Bearer | Flutter | ≤1.3.0 | yes |
| 15 | `POST /api/ab/peer/add/{guid}` | Bearer | Flutter | ≤1.3.0 | yes |
| 16 | `PUT /api/ab/peer/update/{guid}` | Bearer | Flutter | ≤1.3.0 | yes (partial) |
| 17 | `DELETE /api/ab/peer/{guid}` | Bearer | Flutter | ≤1.3.0 | yes |
| 18 | `POST /api/ab/tag/add/{guid}` | Bearer | Flutter | ≤1.3.0 | yes |
| 19 | `PUT /api/ab/tag/rename/{guid}` | Bearer | Flutter | ≤1.3.0 | yes |
| 20 | `PUT /api/ab/tag/update/{guid}` | Bearer | Flutter | ≤1.3.0 | yes |
| 21 | `DELETE /api/ab/tag/{guid}` | Bearer | Flutter | ≤1.3.0 | yes |
| 22 | `GET /api/users?current&pageSize&accessible&status=1` | Bearer | Flutter "Accessible devices" tab | ≤1.3.0 | yes (wrong page param) |
| 23 | `GET /api/peers?current&pageSize&accessible&status=1` | Bearer | Flutter | ≤1.3.0 | yes (wrong page param) |
| 24 | `GET /api/device-group/accessible?current&pageSize` | Bearer | Flutter | 1.4.0 | yes (admin only) |
| 25 | `POST /api/heartbeat` | none | Rust service, every 3 s/15 s | ≤1.3.0 | yes (minimal) |
| 26 | `POST /api/sysinfo` | none | Rust service | ≤1.3.0 | yes |
| 27 | `POST /api/sysinfo_ver` | none | Rust service (1.4.0 only for self-hosted; see §7.3) | 1.4.0 | yes |
| 28 | `POST /api/audit/conn` | none | Rust (controlled side + controller note) | ≤1.3.0 | yes (bad success body) |
| 29 | `POST /api/audit/file` | none | Rust (controlled side) | ≤1.3.0 | yes (bad success body) |
| 30 | `POST /api/audit/alarm` | none | Rust (controlled side) | ≤1.3.0 | **no** |
| 31 | `GET /api/audit/conn/active?id&session_id&conn_type` | Bearer | Flutter (controller side) | 1.4.4 | **no** |
| 32 | `PUT /api/audit` | Bearer | Flutter (controller side) | 1.4.4 | **no** |
| 33 | `POST /api/record?type&file&offset&length` | none | Rust (dead code in OSS builds, §8) | old | **no** |
| 34 | `POST /api/devices/deploy` | Bearer (admin token) | CLI `--deploy`, Android UI | 1.4.7 | **no** |
| 35 | `POST /api/devices/cli` | Bearer (admin token) | CLI `--assign` | ≤1.3.0 | **no** |
| 36 | `POST /api/switch-grant` | none (Ed25519-signed body) | Rust (switch sides) | 1.5.0 | **no** |

That is 36 distinct method+path pairs: 35 used by the Flutter client and the Rust core, plus `/api/ab/get` used only by Sciter. lejianwen also exposes routes the official client never calls: `/api/`, `/api/version`, `/api/user/info`, `/api/oauth/*`, `/api/oidc/callback`, `/api/oidc/login`, `/api/oidc/msg`, `/api/shared-peer`, `/api/server-config*` (`L:http/router/api.go:24-148`). Those are lejianwen's own OAuth callback pages and web client.

---

## 3. Version differences

### 3.1 Feature timeline (client side)
| Feature | First tag with it | Evidence |
|---|---|---|
| New AB API (`/api/ab/personal` and the rest) and legacy fallback | ≤1.3.0 | `R@1.3.0:flutter/lib/models/ab_model.dart` |
| `/api/device-group/accessible`, `device_group_name` in peers | 1.4.0 | absent in `R@1.3.0`, present in `R@1.4.0` |
| `/api/sysinfo_ver` + client-side sysinfo hash cache | 1.4.0 (used for all servers) | `R@1.4.0:src/hbbs_http/sync.rs:107-133` |
| sysinfo_ver/hash only when `is_public(url)` (dead for self-hosted) | 1.4.1 | `R@1.4.1:src/hbbs_http/sync.rs:156-163` |
| `preset-device-group-name` in sysinfo | 1.4.0 | `R@1.4.0:src/hbbs_http/sync.rs:104-107` |
| Sysinfo re-upload when OS username changes; `sysinfo` key in heartbeat response forces re-upload | 1.4.x (present in 1.4.9) | `R:src/hbbs_http/sync.rs:126-131`, `251-255` |
| `/api/audit/conn/active` + `PUT /api/audit` (note at end of session) | 1.4.4 | grep of `conn/active` in `model.dart` per tag |
| Raw-TCP API fallback via hbbs | 1.4.7 | `tcp_proxy_request` first appears in `R@1.4.7:src/common.rs` |
| `/api/devices/deploy` | 1.4.7 | `R@1.4.7:src/ui_interface.rs` |
| `apiDomain` field in `/api/oidc/auth` body | 1.5.0 | `R@1.5.0:src/hbbs_http/account.rs` |
| `nonce` in every audit record + retry/back-off on audit posts | 1.5.0 | `R@1.5.0:src/server/connection.rs` (`RETRY_BACKOFF_SECS`) |
| `primary_auth` / `two_factor` / `conn_audit_ref` in conn audit | ≤1.4.9 | `R@1.4.9:src/server/connection.rs` |
| `/api/switch-grant` | 1.5.0 | `R@1.5.0:src/hbbs_http/sync.rs:325-338` |
| `preset-address-book-alias/password/note`, `preset-device-username`, `preset-device-name`, `preset-note` in sysinfo | 1.4.x–1.5 | `R:src/hbbs_http/sync.rs:144-179` |

### 3.2 Old vs new address-book API: how the client picks
Source: `R:flutter/lib/models/ab_model.dart:142-218` and `262-293`.

1. On every full pull (`ForcePullAb.listAndCurrent`, e.g. after login or `refreshCurrentUser`), the client calls `POST /api/ab/personal`. The cached guid is reset first, so a server upgrade is picked up without a restart.
2. **HTTP 404** means legacy mode. `_personalAbGuid` stays null, the only address book is "Legacy address book", and it uses `GET/POST /api/ab` with one JSON blob (§5.1).
3. HTTP 200 with JSON `{"guid": "<non-null string>", ...}` means new mode. The client then:
   - calls `POST /api/ab/settings` once, if `max_peer_one_ab` has not been fetched yet;
   - builds the list as: personal AB (name hard-coded "My address book", owner = current user, rule = read) plus `POST /api/ab/shared/profiles` pages;
   - pulls the current AB with `POST /api/ab/peers` pages and `POST /api/ab/tags/{guid}`.
4. HTTP 200 **without** `guid` (e.g. `{}`) gives `_personalAbGuid = null`, which is also legacy mode. Line 154 sets `legacyMode = _personalAbGuid == null`.
5. Any other failure stops the pull and shows an error. This includes non-JSON bodies, `{"error":…}`, non-200 statuses other than 404, and a literal `null` body (`jsonDecode("null")` is not a Map, so `_jsonDecodeRespMap` throws).
   - ⚠ lejianwen returns `200 null` when personal AB is disabled in config (`L:http/controller/api/ab.go:354-356`). That breaks the AB tab instead of falling back to legacy. Return **404** instead.
6. Sciter (legacy desktop UI) only knows the legacy blob, and reads it via `POST /api/ab/get` (`R:src/ui/ab.tis:646-672`). It expects `{data: "<json string>", updated_at?}`.

### 3.3 Sciter UI
The Sciter UI (`src/ui/*.tis`) is still in the tree. It calls `/api/login`, `/api/logout`, `/api/currentUser`, `/api/ab/get`, and `POST /api/ab`. It sends only POST, through Rust `post_request` with `Content-Type: application/json`. It also calls `handler.verify_login(data.verifier, token)`, but `verify_login` is hard-wired to `true` (`R:src/common.rs:2661-2686`), so `verifier` is not needed.

---

## 4. Account endpoints

### 4.1 `GET /api/login-options`
- **Auth:** none. Flutter sends `Content-Type: application/json` and no body (`R:flutter/lib/models/user_model.dart:240-264`).
- **Also used** by Rust only as a TLS probe URL. It warms the TLS-type cache and the response is ignored (`R:src/hbbs_http/account.rs:149-158`, `R:src/hbbs_http/record_upload.rs:33-35`).
- **Success:** HTTP 2xx, body = **JSON array of strings**. Recognised items:
  - `"common-oidc/<json>"`: when present, it **wins**. `<json>` is parsed as a JSON array of objects `{"name": "<op>", "icon"?: "<raw SVG markup>"}`.
  - `"oidc/<op>"`: used only when no `common-oidc/` item exists. Each becomes `{"name": "<op>"}`.
  - Other strings are ignored.
- **Errors:** non-2xx throws `RequestException(status, reasonPhrase)` and the dialog shows an error. A non-array body throws too.
- **Button rendering:** `name` is the `op` passed to `/api/oidc/auth`. Labels and icons are built-in for github, gitlab, google, apple, okta, facebook, azure (shown as "Microsoft"), auth0, and microsoft. Other names get `toCapitalized(op)` and a default icon. `icon`, if given, is rendered with `SvgPicture.string` (`R:flutter/lib/common/widgets/login.dart:17-88`, `905-907`).
- lejianwen (`L:http/controller/api/login.go:103-123`) returns `["common-oidc/[{\"name\":\"github\"},…]", "oidc/github", …]`. This is compatible.

### 4.2 `POST /api/login`
Client: `R:flutter/lib/models/user_model.dart:192-235` and `R:flutter/lib/common/hbbs/hbbs.dart:133-197`. Dialog logic: `R:flutter/lib/common/widgets/login.dart:774-826` and `1003-1040`. Sciter: `R:src/ui/index.tis:1541`, `1585-1610`.

**Request headers:** `Content-Type: application/json`, no Authorization.

**Request body:** keys are omitted when null.
```jsonc
{
  "username": "string",
  "password": "string",            // plain text
  "id": "string",                  // this device's RustDesk ID
  "uuid": "string",                // base64 of machine uuid bytes
  "autoLogin": true,
  "type": "account",               // "account" | "email_code" (2nd step) | (constants also define "mobile","sms_code","tfa_code")
  "verificationCode": "string",    // step 2 only
  "tfaCode": "string",             // step 2 only, equals verificationCode for TOTP, absent for email
  "secret": "string",              // step 2 only, echo of `secret` from step 1
  "deviceInfo": { "os": "windows|linux|macos|android|ios", "type": "client", "name": "<hostname>" }
}
```
`deviceInfo` comes from `get_login_device_info()` (`R:src/ui_interface.rs:1329-1336`). `os` is Rust `std::env::consts::OS`. The web client sends `type: "browser"`.

**Success response** (HTTP 200):
```jsonc
{
  "type": "access_token",          // or "email_check" (2FA required)
  "access_token": "string",        // when type=access_token
  "tfa_type": "email_check" | "tfa_check", // when type=email_check; null/absent ⇒ email code
  "secret": "string",              // opaque, echoed back in step 2
  "user": { /* UserPayload, §4.3 */ }
}
```
Client behaviour:
- `type == "access_token"` with a non-null `access_token`: the client stores the token and `user_info`, then refreshes AB and groups.
- `type == "email_check"`: the client shows a code dialog. It is an email code unless `tfa_type == "tfa_check"`, which means a TOTP code. Step 2 re-posts with `type: "email_code"`, `verificationCode`, `tfaCode` (TOTP only), `secret`, and `username = user.name`.
- Any other `type`: "Failed, bad response from server".

**Errors:**
- Non-200 status: `RequestException(status, body.error ?? '')`.
- 200 with an `error` key: error.
- Non-JSON body: when the status is not 200, a red toast "HTTP <status>" is shown, then the client rethrows.

⚠ lejianwen ignores `type`, `verificationCode`, and `tfaCode`, so 2FA is not supported (`L:http/controller/api/login.go:29-92`). Its validator requires a 2–32-char username and a 4–32-char password (`L:http/request/api/user.go:31-39`).

### 4.3 UserPayload (as parsed)
- **Dart** `UserPayload.fromJson` (`R:flutter/lib/common/hbbs/hbbs.dart:26-75`) reads:
  - `name`, `display_name`, `avatar`, `email`, `note`, `verifier` (web only);
  - `status`: `0` = disabled, `-1` = unverified, anything else = normal;
  - `is_admin` (must be literal `true`).
- **Rust** `UserPayload` (used **only** for the OIDC auth-query body; `R:src/hbbs_http/account.rs:24-108`) is stricter:
  - `name: String`: **required**.
  - `info`: **required object**. It contains `email_verification: bool`, `email_alarm_notification: bool`, and optionally `login_device_whitelist: [{data, info:{os,type,name}, exp}]` and `other: {string:string}`. Each whitelist item needs all three fields.
  - `status`: **must be integer 0, 1 or -1**, or absent (defaults to 1). It is `Deserialize_repr`, so any other value fails.
  - Optional: `display_name`, `avatar`, `email`, `note`, `is_admin`, `third_auth_type`.
- `avatar` may be absolute, or a path starting with `/`, which the client prefixes with the API server (`R:src/ui_interface.rs:254-263`). `display_name` and `avatar` are sent to the controlled peer in the login message (`R:src/client.rs:3783-3815`).
- ⚠ lejianwen's user `status` is 1 = enabled, **2 = disabled** (`L:model/model.go:10-11`). It is emitted raw (`L:http/response/api/user.go:21-35`). Map it to 0/1/-1. lejianwen also never sends `display_name` or `avatar`.

### 4.4 `POST /api/currentUser`
- **Request:** headers `Content-Type: application/json` and `Authorization: Bearer <token>`. Body `{"id": "<device id>", "uuid": "<base64 uuid>"}` (`R:flutter/lib/models/user_model.dart:53-114`).
- **When:** at app start (`R:flutter/lib/main.dart:143,183`), when another window requests it, and after login.
- **Success:** 200 with a UserPayload object (§4.3). The client stores `{name, display_name, avatar, status}` into `user_info`.
- **Behaviour:**
  - 401 or 400: token reset (logout). 401 also clears the AB and group caches.
  - `{"error": ...}`: shown as a server error.
  - Transport errors or non-JSON bodies set `networkError`. While `networkError` is non-empty, **AB and group pulls are skipped** (`ab_model.dart:124`, `group_model.dart:42`).
  - The `finally` block always triggers `pullAb(listAndCurrent)` and `groupModel.pull()`.
- Sciter additionally resets the token when `error == "Invalid token"` (`R:src/ui/index.tis:1646-1672`).
- lejianwen: `L:http/controller/api/user.go:39-43`. It is compatible apart from `status` mapping.

### 4.5 `POST /api/logout`
- **Request:** Bearer token and `Content-Type: application/json`. Body `{"id": "...", "uuid": "..."}`. The client applies a **2 s timeout** (`R:flutter/lib/models/user_model.dart:170-190`).
- **Response:** ignored. Local state is reset whatever the server returns.
- The server should revoke the token. lejianwen returns `200 null` (`L:http/controller/api/login.go:134-141`).

---

## 5. Address book

### 5.1 Legacy address book (single blob)

#### `GET /api/ab` (Flutter legacy pull)
- **Request headers:** Bearer token, `Content-Type: application/json`, `Accept-Encoding: gzip` (`R:flutter/lib/models/ab_model.dart:1007-1053`).
- **Response:**
  - Literal body `null` (case-insensitive) means an empty AB.
  - Otherwise a JSON object:
    ```jsonc
    { "data": "<JSON-encoded string of AbBlob>", "licensed_devices": 0 /* optional int; >0 caps peers count */ }
    ```
  - `AbBlob` = `{"tags": ["t1", …], "peers": [Peer…], "tag_colors": "<JSON-encoded string of {tag: int ARGB}>"}`. Note that `data` and `tag_colors` are **strings containing JSON** (double encoding).
  - An `error` key means failure. A 401 together with a pull error means logout.
- **Peer object:** `R:flutter/lib/models/peer_model.dart:33-83`.
  - The client writes `id`, `username`, `hostname`, `platform`, `alias`, `tags`, `hash` (`toCustomJson(includingHash: true)`).
  - The client reads, in addition: `password`, `forceAlwaysRelay` (string `"true"`), `rdpPort`, `rdpUsername`, `loginName`, `device_group_name`, `note`, `same_server`.

#### `POST /api/ab` (legacy push, Flutter + Sciter)
- **Body:** `{"data": "<JSON-encoded AbBlob>"}`. The client always sends the **entire** address book; it is a full replace (`R:flutter/lib/models/ab_model.dart:1055-1096`).
- **Success:** 200 with an empty body, `null`, or any JSON object without `error`.
- **Error:** `{"error": …}`, or a non-200 status: "HTTP <code>".
- The push happens on every add, delete, alias, tag, or colour change, and during "sync from recent sessions" (`_syncFromRecent`, a 3 s timer while the main window is open, gated by local option `sync-ab-with-recent-sessions = Y`; `ab_model.dart:80-96`, `539-592`).

#### `POST /api/ab/get` (Sciter only)
- Body `{}`. The response is the same as `GET /api/ab`; `updated_at` is read but unused (`R:src/ui/ab.tis:646-665`).
- ⚠ Not implemented by lejianwen.

### 5.2 New address-book API: read side

#### `POST /api/ab/personal`
- No body (`Content-Length: 0`), Bearer token.
- **200:** `{"guid": "<string>"}`. Other fields are ignored for personal (lejianwen also returns `name` and `rule`).
- **404:** legacy mode (§3.2).

#### `POST /api/ab/settings`
- No body.
- **200:** `{"max_peer_one_ab": <int>}`. `0` or absent means unlimited. When the limit is reached the client refuses adds with "exceed_max_devices" (`ab_model.dart:1386-1390`).
- **404:** ignored. Any other error: list pull error.

#### `POST /api/ab/shared/profiles?current=<n>&pageSize=100`
- No body. Pagination is 1-based `current`, with `pageSize` fixed at 100.
- **200:** `{"total": <int>, "data": [AbProfile…]}`.
- **Pagination loop** (shared by every list endpoint): `do { current++ ; … } while (current * pageSize < total)`, where `total` is taken from the **first** page only. If `total` is 0 or absent, only one page is read, so lejianwen's `total: 0` works but is capped at one page.
- **AbProfile** (`R:flutter/lib/common/hbbs/hbbs.dart:258-275`):
  ```jsonc
  { "guid": "string", "name": "string", "owner": "string", "note": "string?", "rule": 1|2|3, "info": { "password"?: "default shared password" } }
  ```
  - `rule`: 1 = read, 2 = read/write, 3 = full control. Write is allowed for 2 and 3; full control is needed only for the UI's "full control" actions.
  - `info.password` pre-fills new peers in a shared AB (`ab_model.dart:843-863`).
- Profiles are keyed by **name**. A shared profile named "My address book" would collide with the personal one.

#### `POST /api/ab/peers?current=<n>&pageSize=100&ab=<guid>`
- No body.
- **200:** `{"total": <int>, "data": [Peer…]}`. Peers are deduped by `id`. A non-200 status or an `error` key fails, and 401 logs out.
- Peer fields are read as in §5.1.
  - In a **personal** AB the client expects `hash`: base64 of the stored peer password hash, used for auto-login (`R:src/client.rs:4764-4787`).
  - In a **shared** AB it expects `password` in plain text.
  - `note` must be a string, otherwise it is ignored.
  - `same_server: true` stops the client from overwriting `username`, `hostname`, and `platform` from recent sessions.
- ⚠ lejianwen also returns its DB fields `row_id`, `user_id`, `collection_id`, `online`, `sameServer` (camelCase; the client reads `same_server`), and `licensed_devices` (`L:http/controller/api/ab.go:536-557`, `L:model/addressBook.go`). Unknown fields are harmless.

#### `POST /api/ab/tags/{guid}`
- No body.
- **200:** a bare **JSON array** `[{"name": "string", "color": <int ARGB, e.g. 4288585374>}, …]`. It is not wrapped (`R:flutter/lib/models/ab_model.dart:1499-1544`). An error object at 200 fails JSON-list parsing and is surfaced.

### 5.3 New address-book API: write side ("action" endpoints)

#### 5.3.0 Success rule (critical)
`_jsonDecodeActionResp` (`R:flutter/lib/models/ab_model.dart:2002-2026`) treats a response as success **only when `status == 200 && body.isEmpty`**. For any other response:
- If the JSON has an `error` key, its value is the error.
- Bodies `{}`, `{"code":0}`, or `null` produce the error text `"null"`. The cause is Dart `null.toString()`, plus appending the body text.
- A non-JSON body produces the error `body` text.

So **every mutation must return exactly `200` with an empty body**. lejianwen does this (`c.String(200, "")`).

All write calls send `Authorization: Bearer` and `Content-Type: application/json`, and run once per item. The UI re-pulls the AB after each change (`pullNonLegacyAfterChange`, `ab_model.dart:790-801`).

| Endpoint | Body | Notes |
|---|---|---|
| `POST /api/ab/peer/add/{guid}` | Peer map: `{"id", "alias", "tags":[…], "password"? (shared only), "hash"? (personal only), "note"?, "username"?, "hostname"?, "platform"?}` | One request per peer. The client strips `password` for personal and `hash` for shared, and drops tags unknown to this AB (`ab_model.dart:1548-1578`, `375-417`). |
| `PUT /api/ab/peer/update/{guid}` | `{"id": "...", <one or more of> "tags":[…], "alias", "note", "hash", "password", "username", "hostname", "platform"}` | Partial update; absent keys are unchanged. Sync-from-recent sends `username`, `hostname`, `platform`, and `hash` (personal only) (`ab_model.dart:1580-1749`). |
| `DELETE /api/ab/peer/{guid}` | JSON array of ids `["id1","id2"]` | Body is required on DELETE (`ab_model.dart:1751-1771`). |
| `POST /api/ab/tag/add/{guid}` | `{"name": "t", "color": <int>}` | One request per tag (`ab_model.dart:1775-1802`). |
| `PUT /api/ab/tag/rename/{guid}` | `{"old": "t1", "new": "t2"}` | The server should also rewrite peers' tag lists. |
| `PUT /api/ab/tag/update/{guid}` | `{"name": "t", "color": <int>}` | Colour change. |
| `DELETE /api/ab/tag/{guid}` | `["t"]` | The server should also remove the tag from peers. |

⚠ lejianwen `PeerUpdate` only applies `password`, `hash`, `tags`, and `alias` (`L:http/controller/api/ab.go:709-716`). It silently ignores **`note`**, `username`, `hostname`, and `platform`, but still returns success. Note editing and sync-from-recent are therefore lost.

⚠ lejianwen `TagRename` and `TagDel` do not rewrite peers' `tags` arrays (`L:http/controller/api/ab.go:190-324`). The client fixes this only locally until the next pull.

Permission semantics the client assumes:
- personal AB: all operations allowed;
- shared `rule = 1`: read-only;
- shared `rule = 2`: add, update, and tags;
- shared `rule = 3`: additionally delete peers and tags.

---

## 6. Accessible devices ("Group" tab)
Source: `R:flutter/lib/models/group_model.dart:39-282`. All three calls are `GET` with Bearer only (no Content-Type), and use the same `current`/`pageSize=100`/`total` pagination as §5.2. They run sequentially: device groups, then users, then peers. **If users fails, peers is not fetched.** Device-group failures are ignored because older servers lack the endpoint.

The tab is hidden when the local option `disable-group-panel = Y` is set. If an error message contains "disabled", for example "Retrieving accessible devices is disabled.", the lists are cleared (`group_model.dart:369-377`).

### 6.1 `GET /api/device-group/accessible?current=1&pageSize=100`
- **200:** `{"total": n, "data": [{"name": "string"}, …]}`. Only `name` is used.
- ⚠ lejianwen returns 400 "Permission denied" for non-admins and `total: 0` (`L:http/controller/api/group.go:138-150`). The client tolerates this because errors here are swallowed.

### 6.2 `GET /api/users?current=n&pageSize=100&accessible=&status=1`
- `accessible` is present with an empty value. `status=1` asks for enabled users only.
- **200:** `{"total": n, "data": [UserPayload…]}`. Only `name` and `display_name` are used. The current user is moved first.
- Special errors: `"Admin required!"`, or text containing `"ambiguous column name: status"`, is shown as "upgrade server" (`group_model.dart:184-193`).

### 6.3 `GET /api/peers?current=n&pageSize=100&accessible=&status=1`
- **200:** `{"total": n, "data": [PeerPayload…]}`.
- **PeerPayload** (`R:flutter/lib/common/hbbs/hbbs.dart:77-131`):
  ```jsonc
  {
    "id": "123456789",
    "info": { "username": "os user", "os": "windows / Windows 11 Pro - 10.0.22631", "device_name": "HOST" },
    "status": 1,                  // read, not used
    "user": "string",             // read, not used
    "user_name": "owner login",   // shown as "loginName"; used to filter by user
    "device_group_name": "string",// used to filter by device group
    "note": "string"
  }
  ```
  - The platform icon comes from `info.os`. The client splits it on `" / "` and matches the first token case-insensitively against `windows`, `linux`, `macos`, `android`. If the string contains "linux" it falls back to Linux.
  - Store `os` **exactly as sysinfo sends it** (§7.2). `info.device_name` is the hostname shown.
- ⚠ **Pagination param mismatch:** lejianwen binds `page` (`form:"page"`), but the client sends `current` (`L:http/request/api/user.go:41-53`). With more than 100 users or peers, lejianwen keeps returning the same page and later rows are never shown. Use `current`.
- Which users and peers are "accessible" is a server policy decision. The client just renders what it receives.

---

## 7. Heartbeat & sysinfo (device side, Rust service)
Source: `R:src/hbbs_http/sync.rs:18-290`. This runs in the background service on desktop. It is skipped on iOS, when `stop-service = Y`, when the URL is empty or public, or when the device is "no register device".

### 7.1 Timing
- The loop ticks every **3 s** (`TIME_CONN`).
- The heartbeat is sent when there are active incoming connections (**every 3 s**), or when 15 s (`TIME_HEARTBEAT`) have passed since the last one. Size your server for one POST per 3 s per device that is being controlled.
- Sysinfo is uploaded before the heartbeat in the same tick when needed. After a failed or unrecognised response it is retried no sooner than **120 s** later (`UPLOAD_SYSINFO_TIMEOUT`).

### 7.2 `POST /api/sysinfo`
Body (JSON, no auth), built from `get_sysinfo()` (`R:src/common.rs:928-973`) plus extra fields (`R:src/hbbs_http/sync.rs:126-180`):
```jsonc
{
  "cpu": "Intel(R) Core(TM) i7…, 3.6GHz, 8/4 cores",   // "<brand>, <GHz>GHz, <logical>/<physical> cores"; android: "8/8 cores"
  "memory": "15.87GB",
  "os": "windows / Windows 11 Pro - 10.0.22631",        // "<distribution_id> / <long_os_version>[ - <os_version> on Windows]"
  "hostname": "HOST",                                  // overridden by option preset-device-name
  "username": "alice",                                 // omitted if empty or (Windows) "SYSTEM"; overridden by preset-device-username
  "version": "1.5.0",
  "id": "123456789",
  "uuid": "base64",
  // Optional, only when set (deployment presets):
  "preset-address-book-name": "…", "preset-address-book-tag": "…",
  "preset-address-book-alias": "…", "preset-address-book-password": "…", "preset-address-book-note": "…",
  "preset-user-name": "…", "preset-strategy-name": "…", "preset-device-group-name": "…",
  "preset-note": "…"
}
```
The key strings are in `R:libs/base/src/config/keys.rs:79-115`. The presets let an admin-built client auto-assign the device to a user, strategy, device group, or address book on first contact. A compatible server should honour them. Note that "first contact" must include a device pre-registered by `POST /api/devices/deploy` (§10.1), which exists before its first sysinfo; this server applies the presets on the first sysinfo of a device (see [`client-compat-1.4.6.md`](client-compat-1.4.6.md) §4).

**Response:** plain text, compared byte-for-byte. Do not JSON-quote it.
- `SYSINFO_UPDATED`: success. The client will not upload again until the URL, ID, or OS username changes, or the server asks for it.
- `ID_NOT_FOUND`: retry on the next tick (3 s). Pro uses this when hbbs has not registered the ID yet.
- Anything else, including a JSON error: retry after 120 s.

lejianwen always answers `SYSINFO_UPDATED`, or 400 JSON on a bind error (`L:http/controller/api/peer.go:26-59`).

### 7.3 `POST /api/sysinfo_ver`
- Body empty, response plain text (opaque version string). The client stores it and skips the sysinfo upload when both the hash of (url + sysinfo JSON) and this version are unchanged.
- **In client 1.4.0 only** this ran for every server (`R@1.4.0:src/hbbs_http/sync.rs:109-133`). From **1.4.1** it is wrapped in `if is_public(&url)`, but `heartbeat_url()` already returns "" for public URLs. The branch is therefore unreachable for self-hosted servers in ≥1.4.1.
- Implement it anyway for 1.4.0. lejianwen returns `"<app version>\n<server start time>"`, so a server restart forces re-upload (`L:http/controller/api/peer.go:70-76`).
- Any transport error makes the client treat it as unsupported and upload.

### 7.4 `POST /api/heartbeat`
**Request body** (no auth):
```jsonc
{
  "id": "123456789",
  "uuid": "base64",
  "ver": 1005000,          // get_version_number("1.5.0"): parts*1000 each, last part ×10, plus "-N" suffix (H:src/lib.rs:264-288). 1.4.9 → 1004090
  "conns": [3, 7],         // i32 connection ids of currently alive incoming connections; key ABSENT when none
  "modified_at": 0         // i64, last strategy timestamp the client applied (LocalConfig "strategy_timestamp"), 0 if none
}
```

**Response:** a JSON object. Every key is optional and unknown keys are ignored. A non-JSON body is ignored silently.
| Key | Type | Client effect |
|---|---|---|
| `sysinfo` | any (presence matters) | Forces sysinfo re-upload on the next tick and clears the cached hash (`sync.rs:251-255`). |
| `disconnect` | `[i32 conn_id, …]` | Every alive connection whose id is listed is closed. The remote side sees the reason "Closed manually by web console" (`R:src/server/connection.rs:1067-1073`, `1384-1388`). Use the ids from `conns`. |
| `modified_at` | i64 | If it differs from the request value, it is stored as the new `strategy_timestamp` and echoed in later heartbeats. |
| `strategy` | `{"config_options": {string:string}, "extra": {string:string}}` | `config_options` is merged into the device's `Config` options (`handle_config_options`, `sync.rs:292-309`). An empty value **removes** the key, unless there is a non-empty default in `DEFAULT_SETTINGS`, in which case `""` is stored. `extra` is parsed but unused. |

Recommended server behaviour (Pro-like):
- Send `strategy` together with its `modified_at` only when the client's `modified_at` differs from the strategy's current timestamp. The client re-applies `config_options` on every heartbeat that carries them.
- Always return `{}` (or other valid JSON) otherwise.
- lejianwen returns `{}` always and only updates last-online (`L:http/controller/api/index.go:41-63`).

`config_options` keys are ordinary RustDesk option names, the same set as custom-client "default/override settings". Examples: `approve-mode`, `verification-method`, `enable-keyboard`, `allow-remote-config-modification`, `whitelist`, `direct-server`. They are applied without validation.

---

## 8. Audit & recording

### 8.0 Response contract for `/api/audit/{conn,file,alarm}` (client ≥ 1.5.0)
`post_audit_async` (`R:src/server/connection.rs:1669-1766`):
- **2xx with an empty (whitespace-only) body = stored.**
- 2xx with `{"error":"..."}` is retryable. The server is expected to release that record's `nonce` when its write fails.
- 2xx with **any other non-empty body is retryable**, treated as a proxy page.
- 5xx, 408, and 429 are retryable. Other 4xx statuses are dropped immediately.
- There are up to 3 attempts, with back-off of 10 s and then 30 s, and a 120 s overall deadline.
- **Dedupe by `nonce`** for at least 5 minutes. The client comments assume the server remembers a nonce for 5 minutes.
- ⚠ lejianwen answers `200 {"code":0,"message":"success","data":""}` (`L:http/controller/api/audit.go:58,83`). A 1.5.0+ client sees "unexpected response body", retries twice, and logs "Audit post dropped". lejianwen also has no nonce dedupe, so every record is **stored three times**.
- Clients ≤1.4.9 ignore the response entirely.

All audit endpoints are unauthenticated device calls. `id` is the **controlled** device's ID and `uuid` its base64 uuid.

### 8.1 `POST /api/audit/conn` (controlled side)
Built by `post_conn_audit` (`R:src/server/connection.rs:1558-1571`), which always adds `id`, `uuid`, `conn_id` (i32), `session_id` (**u64**), and `nonce` (UUIDv4 string, ≥1.5.0). Three event shapes are sent, in order:

1. **New TCP connection** (`on_open`, `connection.rs:1505-1531`):
   `{"action":"new", "ip":"1.2.3.4", "conn_audit_ref"?: "string", id, uuid, conn_id, session_id: 0?, nonce}`.
   `session_id` is usually 0 here because no login message has arrived yet. `conn_audit_ref` is an opaque value supplied by hbbs in the rendezvous message (`ControlledContext`), when present.
2. **Authorized** (`connection.rs:1908-1939`); **no `action` key**:
   `{"peer": ["<controller id>", "<controller name>"], "type": <conn_type>, "primary_auth"?: n, "two_factor"?: n, id, uuid, conn_id, session_id, nonce}`.
   - `type`: 0 = remote desktop, 1 = file transfer, 2 = port forward/RDP, 3 = view camera, 4 = terminal.
   - `primary_auth`: 1 = click-to-accept, 2 = temporary password, 3 = permanent password, 4 = switch sides.
   - `two_factor`: 1 = TOTP, 2 = trusted device. It is omitted when the value is 0 (`connection.rs:288-336`).
   - `peer` is a 2-element JSON **array** (Rust tuple).
3. **Closed** (`connection.rs:1235-1237`):
   `{"action":"close", id, uuid, conn_id, session_id, nonce}`.

The server correlates events by (`id`, `conn_id`), and by `session_id` after authorization.

**`session_id` is a random u64.** Parse it as unsigned 64-bit, or as `json.Number`/a string. ⚠ lejianwen parses it into `float64` and loses precision (`L:http/request/api/audit.go:10-19`).

4. **Controller-side note** (the "Note" button during a session; `R:src/ui_session_interface.rs:591-599`, `2108-2111`). It is posted to the same URL and sent only if the controller is logged in, but **without** an Authorization header:
   `{"id": "<controlled peer id>", "session_id": <u64>, "note": "text ≤256 chars"}`.
   There is no `action` and no `nonce`, and the response is ignored. The server matches the record by `id` and `session_id`.

### 8.2 `POST /api/audit/file`
Built by `post_file_audit` (`R:src/server/connection.rs:1589-1624`):
```jsonc
{
  "id": "<controlled id>", "uuid": "…", "peer_id": "<controller id>", "conn_id": 3,
  "type": 0,                // 0 = controlled → controller (remote send/download), 1 = controller → controlled (remote receive/upload, incl. clipboard file paste)
  "path": "C:\\dir",        // "Remote print" for printer jobs; "" for clipboard files
  "is_file": false,         // true when exactly one entry with empty name (path is the file itself)
  "info": "{\"ip\":\"…\",\"name\":\"<controller name>\",\"num\":12,\"files\":[[\"name\",12345],…]}", // JSON STRING; files = top-10 by size
  "nonce": "uuid"
}
```
`FileAuditType` is defined at `connection.rs:6515-6518`.

### 8.3 `POST /api/audit/alarm` (not in lejianwen)
Built by `post_alarm_audit` (`R:src/server/connection.rs:1626-1650`):
```jsonc
{ "id": "…", "uuid": "…", "typ": <int>, "info": "<JSON string>", "conn_id": 3, "nonce": "uuid", "conn_audit_ref"?: "…" /* only typ 0 and 10 */ }
```
`typ` values (`connection.rs:6501-6513`):
- 0 = IP whitelist block. Info `{ip}`.
- 1 = more than 30 failed attempts. Info `{ip,id,name}`.
- 2 = 6 attempts within 1 minute.
- 6 = IPv6 prefix attempts exceeded.
- 7 = terminal OS-login back-off.
- 8 = terminal OS-login concurrency.
- 9 = session-scope violation. Info `{id,name,ip,conn_type,message}`.
- 10 = ID whitelist block. Info `{id,ip,name}`.

### 8.4 `GET /api/audit/conn/active?id=<peer>&session_id=<u64>&conn_type=<0..4>` (controller side, ≥1.4.4)
- **When:** after a session's peer info arrives (`R:flutter/lib/models/model.dart:1253-1335`). It runs only if the user is logged in, the local option `allow-ask-for-note` is enabled, and no GUID is cached.
- **Request:** Bearer token and `Content-Type: application/json`.
- **200:** body is a **JSON string** containing the audit record GUID, e.g. `"5f0c…"`. An empty string or `null` means "not yet": the client retries at 1, 1, 2, 2, 3, 3 s, up to 6 attempts. **Any non-200 status stops retrying.**
- The server returns the GUID of the conn-audit record where `id = peer` (the controlled device) and `session_id` matches, created by §8.1 from the other side.

### 8.5 `PUT /api/audit` (controller side, ≥1.4.4)
- Sent when the session ends and the user types a note (`R:flutter/lib/common/widgets/dialog.dart:1650-1706`).
- Bearer token, `Content-Type: application/json`. Body `{"guid": "<from 8.4>", "note": "text"}`. The client times out after 6 s.
- Only status 200 counts as success; the result is logged only.

### 8.6 `POST /api/record?type=new|part|tail|remove&file=<name>[&offset=<u64>&length=<n>]`
- Streams a session recording: raw bytes in the body, no Content-Type override, no auth (`R:src/hbbs_http/record_upload.rs:83-211`).
  - `new` and `remove`: empty body.
  - `part`: appended bytes, sent every ≥1 s and ≥1 MiB.
  - `tail`: the first ≤1024 bytes of the file, with `offset=0`, so the server rewrites the container header.
- A JSON body with `error` aborts the upload.
- **Dead code in OSS builds:** `record_upload::ENABLE` is never set to true anywhere, so `is_enable()` is always false (`record_upload.rs:19-25`, `R:src/server/video_service.rs:1188`). This is optional and low priority.

---

## 9. OIDC login flow

### 9.1 Endpoints

#### `POST /api/oidc/auth`
- **Request:** no auth. Sent via Rust `post_request_sync`, so `Content-Type: application/json` and a 12 s timeout (`R:src/hbbs_http/account.rs:160-177`). Body:
  ```jsonc
  { "op": "<name from login-options>", "id": "<device id>", "uuid": "<base64 uuid>",
    "deviceInfo": {"os":"windows","type":"client","name":"HOST"},
    "apiDomain": "https://desk-ru-api.example"  /* ≥1.5.0: the API base URL the client used */ }
  ```
- **Response:** the status code is ignored and only the body is parsed (`HbbHttpResponse::parse`, `R:src/hbbs_http.rs:24-39`).
  - Success: `{"code": "<opaque session code>", "url": "<absolute URL to open in browser>"}`. `url` must be parseable by Rust `url::Url`; a relative URL yields "Invalid auth response".
  - Error: `{"error": "msg"}`, shown and ends the attempt.

#### `GET /api/oidc/auth-query?code=<code>&id=<id>&uuid=<uuid>`
- No auth; header JSON `{}` (`R:src/hbbs_http/account.rs:179-199`). The body of the HTTP response is parsed; the status code is ignored.
- **Pending:** `{"error": "No authed oidc is found"}`. A substring match keeps polling.
- **Any other `error`:** the attempt ends with that message.
- **Unparseable or unexpected shape:** ignored, and polling continues until the timeout. This is a trap: a malformed success body looks like "still pending" for 3 minutes.
- **Success:** an AuthBody that the **Rust** struct can deserialize (`account.rs:99-108`):
  ```jsonc
  { "access_token": "string",          // required
    "type": "access_token",             // required; anything else is accepted but token not stored
    "tfa_type": "", "secret": "",       // optional
    "user": { "name": "alice", "info": {}, "status": 1, "display_name": "Alice", "avatar": "…", "email": "…", "note": "…", "is_admin": false } }
  ```
  `user.name` and `user.info` (an object, `{}` is fine) are required. `status` must be 0, 1 or -1 (§4.3).

### 9.2 Step by step
1. The login dialog calls `GET /api/login-options` and renders a button per op (§4.1).
2. The user clicks a provider. Flutter calls `bind.mainAccountAuth(op, rememberMe: true)` (`R:flutter/lib/common/widgets/login.dart:164-198`). Rust `OidcSession::account_auth` reads `id` and `uuid` and starts a background thread (`R:src/flutter_ffi.rs:2451-2455`, `R:src/hbbs_http/account.rs:362-382`).
3. Rust sends `POST /api/oidc/auth`. On `{code,url}` the state becomes "Waiting account auth" and `url` is exposed to the UI.
4. Flutter polls `mainAccountAuthResult()` every 1 s. It opens `url` in the external browser with a "copy link" fallback (`login.dart:289-440`).
5. The **server** redirects the browser to the IdP (state = `code` or a mapping to it). It handles the IdP callback at a URL of its choosing; the client never sees it. lejianwen uses `/api/oidc/callback` and `/api/oauth/callback` (`L:http/router/api.go:43-56`, `L:http/controller/api/ouath.go:148-269`). The server then marks the session `code` as authenticated for a user, auto-registering or binding if needed, and shows an HTML success page.
6. Meanwhile Rust polls `GET /api/oidc/auth-query` **every 1 s for up to 180 s** (`QUERY_INTERVAL_SECS`, `QUERY_TIMEOUT_SECS`, `account.rs:17-18`, `291-347`). While pending the server returns `{"error":"No authed oidc is found"}`. On timeout the UI shows "timeout".
7. On success with `type == "access_token"`, Rust stores `access_token` and `user_info={name,display_name,avatar,status}` (`account.rs:299-325`). Flutter receives `auth_body`, runs `getLoginResponseFromAuthBody`, stores `user_info` again, closes the dialog, and refreshes AB and groups.
8. The server should invalidate `code` after a successful query; lejianwen deletes its cache entry (`L:http/controller/api/ouath.go:95-96`). It should bind the token to the device `id`/`uuid` from step 3. A session lifetime of about 5 minutes is reasonable; lejianwen uses 5 × 60 s.
9. **Cancel:** the client stops polling and sends nothing to the server, so the server must expire stale codes itself.

Flutter **web** runs the same flow through the browser bridge. `apiDomain` (≥1.5.0) lets the server know which public base URL the client used, which is useful for building callback URLs behind proxies.

---

## 10. Device provisioning & misc (not in lejianwen)

### 10.1 `POST /api/devices/deploy` (≥1.4.7)
- **Callers:** `rustdesk --deploy --token <tok> [--id <new id>]` (installed, root), and the Android deploy dialog `flutter/lib/mobile/widgets/deploy_dialog.dart` (`R:src/core_main.rs:634-675`, `R:src/ui_interface.rs:1083-1149`).
- **Headers:** `Authorization: Bearer <tok>` (an admin-issued deploy token, not a user session) and `Content-Type: application/json`.
- **Body:** `{"id": "<id to deploy>", "uuid": "<base64>", "pk": "<base64 Ed25519 public key>"}`.
- **Response:** JSON `{"result": "OK" | "NOT_ENABLED" | "INVALID_INPUT" | "ID_TAKEN"}`. Any other body is shown verbatim as the error. CLI exit codes: OK 0, NOT_ENABLED 3, INVALID_INPUT 5, ID_TAKEN 6, other 1.
- **Context:** hbbs may answer `RegisterPkResponse.NOT_DEPLOYED`, after which the client logs "run `rustdesk --deploy`" (`R:src/rendezvous_mediator.rs:503-532`). This only matters if the panel's hbbs enforces deployment.

### 10.2 `POST /api/devices/cli` (`rustdesk --assign`)
- **Headers:** `Authorization: Bearer <tok>`.
- **Body** (`R:src/core_main.rs:529-626`): `{"id","uuid"}` plus at least one of:
  - `user_name`, `strategy_name`, `device_group_name`, `note`, `device_username`, `device_name`;
  - `address_book_name`, and with it optionally `address_book_tag`, `address_book_alias`, `address_book_password`, `address_book_note`.
- **Response:** an empty body prints "Done!". Any other text is printed verbatim.

### 10.3 `POST /api/switch-grant` (1.5.0)
- **When:** fire-and-forget during "switch sides" (`R:src/hbbs_http/sync.rs:317-412`; triggered from `R:src/ipc.rs:1092`).
- **Body:**
  ```jsonc
  { "id": "<device id>", "switch_code_verifier": "<b64(sha256(\"switch-grant-verifier\\0\" + b64(sha256(switch_uuid))))>",
    "timestamp": "<unix seconds as string>", "signature": "<b64 Ed25519 detached sig>" }
  ```
- **Signed message:** `b"switch-grant\0" + id + "\0" + switch_code_verifier + "\0" + timestamp`, signed with the device's key pair. Verify it against the device `pk` that hbbs or deploy recorded.
- **Response:** JSON `{"accepted": true}`, or `{"accepted": false, "server_time": <i64>}`. On the latter the client retries once with the server time. If the field is missing, the call is logged and abandoned.
- Its purpose is to pre-authorise the `switch_code` that the client later sends to hbbs in `RequestRelay`. It is only meaningful with an hbbs that enforces switch codes. A minimal server can answer `{"accepted": true}` or 404; both are harmless.

---

## 11. Notable gotchas (summary)
1. **Action endpoints must return `200` with an empty body.** A body of `{}`, `null`, or `{"code":0}` is shown to the user as error "null" (§5.3.0).
2. **Audit endpoints must return 2xx with an empty body** and dedupe by `nonce`. Otherwise 1.5.0+ clients retry, which causes duplicates with lejianwen (§8.0).
3. `/api/ab/personal` returning `200 null` breaks the AB tab. Use **404** to mean "legacy only" (§3.2).
4. `/api/currentUser` returning **400** logs the user out (§1.5).
5. OIDC `auth-query` success **must** include `user.info` (an object) and `user.status ∈ {0,1,-1}`. Otherwise the client silently polls for 3 minutes and times out (§9.1).
6. List endpoints use `current` (1-based) and `pageSize`, not `page`. `total` is read from the first page only (§5.2, §6.3).
7. `sysinfo` responses are compared as **plain text**: `SYSINFO_UPDATED` or `ID_NOT_FOUND` (§7.2).
8. Device calls (heartbeat, sysinfo, audits) carry no auth. Trust is `id`+`uuid` (+`pk` for switch-grant and deploy). Consider cross-checking with hbbs registration.
9. `session_id` values are u64. Do not use float64 (§8.1).
10. DELETE requests carry JSON bodies, and POSTs with `Content-Length: 0` must be accepted.
11. Heartbeat runs every **3 s** while a device has active incoming sessions (§7.1).
12. Legacy AB `data` and `tag_colors` are double-encoded JSON strings (§5.1).

---

## 12. Licensing

| Project | License | Evidence |
|---|---|---|
| rustdesk/rustdesk (client) | **AGPL-3.0** | `R:LICENCE` (file header "GNU AFFERO GENERAL PUBLIC LICENSE Version 3"); GitHub API `license.spdx_id = AGPL-3.0` |
| rustdesk/rustdesk-server (OSS hbbs/hbbr) | **AGPL-3.0** | GitHub API `license.spdx_id = AGPL-3.0` |
| rustdesk/hbb_common | no license detected by the GitHub API; it is published as part of the AGPL RustDesk project | GitHub API returned no `spdx_id` |
| lejianwen/rustdesk-api | **MIT** ("Copyright (c) 2024-present Lejianwen and contributors") | `L:LICENSE` |

What this means for a new, compatible Go server (practical reading, not legal advice):
- **Interoperating with the client does not make the server a derivative of the client.** We are reproducing a network protocol: endpoint paths, JSON field names, status-code semantics. Interface facts like these are generally treated as functional and not copyrightable expression. A clean-room server that speaks this protocol does not need to adopt AGPL-3.0.
- **Do not copy AGPL code** from `rustdesk/rustdesk`, `rustdesk-server`, or `hbb_common` into the Go server. That includes Rust code ported line by line, Dart snippets, and the structure of `handle_config_options` and similar functions. Write the server from this spec, which describes behaviour and cites sources for verification only. If the project later links to or embeds AGPL code (for example by vendoring hbbs to add `HttpProxyRequest` or deploy enforcement), that component and the combined work fall under AGPL-3.0, including §13 network-use source obligations.
- **Protobuf definitions** (`hbb_common/protos/*.proto`) are needed only for the optional raw-TCP proxy (§1.4) or hbbs integration. Re-declaring the few messages by hand from their documented fields is safer than copying the AGPL `.proto` files. If they are copied, keep them in a separately licensed AGPL module.
- **lejianwen/rustdesk-api is MIT.** Its code may be reused or adapted under any license, including a proprietary one, provided the MIT copyright and permission notice is kept in copies or substantial portions (e.g. a `THIRD_PARTY_NOTICES` file). A clean-room implementation that only reads it for behaviour needs no notice. If any file or function is copied, keep attribution. Note that lejianwen depends on third-party Go modules with their own licenses; check `go.mod` if code is lifted.
- **Branding:** "RustDesk" is a trademark of the RustDesk project. Name the panel so it does not imply official affiliation, e.g. "compatible with RustDesk".
- **Recommended:** license the new server under MIT or Apache-2.0, and keep this document as the clean-room specification of record. Any optional hbbs patches should live in a separate AGPL repository.
