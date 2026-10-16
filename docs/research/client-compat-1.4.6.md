# Client compatibility audit: RustDesk 1.4.6 → 1.5.0 → master

Status: audit written 2026-10-16 on branch `feat/client-compat`. Companion to
[`client-api.md`](client-api.md) (the normative wire contract); this file
records what the current clients call, what we answer, and the gaps.

## 0. Email login codes (`email_check`): what the client expects

**Read this first if you work on `feat/email-codes`.** This branch does not
implement email codes; it documents the exact contract so the SMTP work can
match it. The flow is identical in 1.4.6, 1.5.0 and master (no diff in
`hbbs.dart`, and the login dialogs only changed cosmetically).

### Step 1: password accepted, server wants an email code
Request (unchanged): `POST /api/login` with `{"username","password","id","uuid","autoLogin"?,"type":"account","deviceInfo":{os,type,name}}`.

Response, HTTP **200**:
```jsonc
{
  "type": "email_check",          // R:flutter/lib/common/hbbs/hbbs.dart:18 (kAuthResTypeEmailCheck)
  "tfa_type": "email_check",      // or omit / null: both mean "email code"; "tfa_check" means TOTP
  "secret": "<opaque string>",    // echoed back verbatim in step 2; the server's only handle on the challenge
  "user": {
    "name": "<username>",         // REQUIRED: step 2 sends it back as `username` (Flutter: user?.name, Sciter: last_msg.user.name)
    "email": "<address or masked address>" // shown read-only in the dialog; Sciter shows it unconditionally
    // other UserPayload fields optional here
  }
}
```
- Flutter decides email vs TOTP at `R:flutter/lib/common/widgets/login.dart:790-799` (1.4.6: `R@1.4.6:…/login.dart:446-455`):
  `tfa_type == null || tfa_type == "email_check"` → email dialog; `tfa_type == "tfa_check"` → TOTP dialog; **any other `tfa_type` → "Failed, bad tfa type from server"**.
- Sciter: `R:src/ui/index.tis:1548` (`data.type == 'email_check'`) and `on_2fa_check` at `1569-1600` (1.4.6: `1491`, `1512-1536`): `isEmailCheck = !tfa_type || tfa_type == 'email_check'`. **Sciter dereferences `last_msg.user.name` and `last_msg.user.email` without a null check, so `user` must be an object** (an absent `email` just renders "undefined").
- Flutter hides the email field when `user.email == null` (`login.dart:1070`). Send the address (or a masked form such as `a***@example.com`) so the user knows where to look.
- Do not send `access_token` in this response.

### Step 2: the code
Flutter `verificationCodeDialog` (`R:flutter/lib/common/widgets/login.dart:1003-1024`; 1.4.6: `623-644`) posts `POST /api/login`:
```jsonc
{
  "type": "email_code",            // ALWAYS "email_code", for email AND for TOTP (kAuthReqTypeEmailCode)
  "verificationCode": "<code>",    // the typed code, both flows
  "tfaCode": null,                 // Flutter: key omitted for email (null values are dropped); TOTP: = code
  "secret": "<from step 1>",
  "username": "<user.name from step 1>",
  "id": "<device id>", "uuid": "<base64 uuid>", "autoLogin": true,
  "deviceInfo": {"os","type","name"}
  // NO password
}
```
Sciter sends the same keys but `tfaCode: ''` (empty string) for email and the code for TOTP, and no `autoLogin` (`R:src/ui/index.tis:1589-1598`).

**Telling the two second factors apart:** the request `type` is the same (`email_code`) for both. Use `tfaCode`: non-empty means TOTP, empty/absent means email. More robust: record the challenge kind inside the sealed `secret` and ignore the request hints. Our `secret` is already an AEAD-sealed `tfaChallenge{u,n,e}` (`internal/clientapi/account.go:43-47`, `228-254`); add a kind field there. Today the `email_code`/`tfa_code` branch (`account.go:144-176`) always verifies TOTP; the email agent must branch on the challenge kind before calling `h.TOTP.Verify`.

Code format:
- Flutter email field: **exactly 6 characters, any characters** (`DialogEmailCodeField.isReady => text.length == 6`, `R:flutter/lib/common/widgets/dialog.dart:652-695`; 1.4.6: `543-583`). It auto-submits at 6 characters. The keyboard is `visiblePassword`, so letters are allowed, but 6 digits is the safe choice.
- Flutter TOTP field: exactly 6 **digits** (`dialog.dart:578-626`).
- Sciter: at least 6 characters (`index.tis:1583`).
- The dialog shows the translated hint `verification_tip` ("A verification code has been sent to the registered email address, enter the verification code to continue logging in.", `R:src/lang/en.rs:54`).

Step 2 success: `200 {"type":"access_token","access_token":"…","user":{UserPayload}}`. `user` is needed: Flutter stores it through `getLoginResponseFromAuthBody` → `_parseAndUpdateUser` (`R:flutter/lib/models/user_model.dart:216-235`); Sciter calls `set_local_user_info(data.user)`.

Step 2 failure: non-200 with `{"error":"…"}` (Flutter shows `translate(error)` under the code field and lets the user retry with the same `secret`), or 200 with `{"error"}`. Any other `type` → "Failed, bad response from server". There is **no resend-code request**: the client cannot ask for a new code; the user cancels and logs in again (step 1 must send a fresh code each time). Rate-limit step 2 per `secret`/user, because the dialog allows unlimited retries.

There is no "trust this device" flag on the wire (the checkbox is commented out in `login.dart`). `UserInfo.email_verification` in the user payload (`R:src/hbbs_http/account.rs` `UserSettings`) is informational only; the client never reads it to decide anything.

Other `type` constants exist in `HttpType` (`mobile`, `sms_code`, `tfa_code`, `kAuthResTypeTfaCheck`) but **no official client sends `mobile`, `sms_code` or `tfa_code`**; we accept `tfa_code` as an alias anyway.

---

## 1. Sources

| Alias | Repository | Commit / tag |
|---|---|---|
| `R@1.4.6` | rustdesk/rustdesk | tag `1.4.6`, `1abc897c451c8b5bbff3792509a7fef9d12f2ce3` (2026-03-05) |
| `R@1.5.0` | rustdesk/rustdesk | tag `1.5.0`, `fada664df7a294d1d1a9ca3e7cd3637069122f17` (2026-09-30), latest release |
| `R` | rustdesk/rustdesk | master `e400bb75546cebf4f72a5ef3cf6e4a3efa2c3aa7` (2026-10-15, 1.5.1-dev) |
| `H` | rustdesk/hbb_common | main `5d52530b636fb296a324260b074780cf559e0093` (2026-10-14) |

Method: every `/api/` string in `src/`, `flutter/lib/`, `libs/` of the three trees was enumerated and diffed (`grep -o "/api/[A-Za-z_{}$/.-]+"`), then each caller was read. Files diffed 1.4.6 ↔ master: `hbbs.dart` (identical), `group_model.dart` (cache flag only), `user_model.dart`, `ab_model.dart` (error handling), `account.rs`, `sync.rs`, `http_service.dart`, `connection.rs` audit functions, `get_sysinfo()` (identical).

Our code: paths are relative to the repo root on this branch.

## 2. What changed for the API server between 1.4.6 and master

| Change | First version | Impact on us |
|---|---|---|
| `POST /api/devices/deploy` | 1.4.7 | implemented |
| Raw-TCP fallback via hbbs `HttpProxyRequest` on 5xx/connection error | 1.4.7 | n/a (needs custom hbbs); a 5xx is still seen by the client |
| `apiDomain` in `/api/oidc/auth`; OIDC requests go through `post_request_sync`/`http_request_sync` instead of a dedicated reqwest client | 1.5.0 | ignored field; no wire change |
| `nonce` + retry/back-off for `/api/audit/{conn,file,alarm}`; `conn_id` added to file/alarm audits | 1.5.0 | implemented (nonce dedupe, empty-body success) |
| `POST /api/switch-grant` | 1.5.0 | implemented |
| `/api/ab/shared/profiles` 404 tolerated (1.4.6 treats 404 as an error) | 1.5.0 | we always implement it |
| `/api/login-options` non-2xx now throws (1.4.6 silently showed no OIDC buttons) | 1.5.0 | we always answer 200 + array |
| Heartbeat `strategy.config_options[""]` honours `DEFAULT_SETTINGS` | 1.5.0/master | client-side only |
| `/api/sysinfo_ver` | 1.4.0 only for self-hosted (dead code since 1.4.1) | implemented anyway |

No new endpoint, login `type`, or user/peer field appeared in master after 1.5.0.

### "translate_mode capability gate at >=1.4.6"
This is **not an API-server feature**. In the 1.4.6 tag the desktop toolbar hides "Translate mode" when the *remote peer's* version (`PeerInfo.version`, sent peer-to-peer in the login response) is older than 1.4.6: `R@1.4.6:flutter/lib/desktop/widgets/remote_toolbar.dart:1871-1875` (`versionCmp(pi.version, '1.4.6') < 0`). Master dropped that explicit check and relies on `is_keyboard_mode_supported` (`R:src/common.rs`, peer version ≥ 1.2.0). Nothing travels through `/api/*`; a server has nothing to implement. lantongxue/rustdesk-api-server-pro's claim describes client UI behaviour, not a server contract.

### "auth payload: strict required fields, tolerant unknown fields"
This refers to the Rust serde structs used to parse the **OIDC `auth-query` success body** (`R@1.4.6:src/hbbs_http/account.rs:74-108`, same in master):
- `AuthBody{access_token: String, type: String, user: UserPayload}` are required (no `#[serde(default)]`); `tfa_type`, `secret` default to "".
- `UserPayload{name: String, info: UserInfo}` are required; `status` is `Deserialize_repr` over `{0, 1, -1}` (any other integer fails); `display_name/avatar/email/note/third_auth_type` optional; `is_admin` defaults to false.
- None of the structs use `deny_unknown_fields`, so extra keys are ignored.
- A body that fails to deserialize is `HbbHttpResponse::DataTypeFormat`, which the poll loop **ignores and keeps polling** for 180 s (`account.rs:291-347`). So a missing `user.info` looks like "still pending, then timeout".

We comply: `internal/oidc/clientroutes.go:166-204` always emits `info:{}` and `status ∈ {0,1}`; the password login (`internal/clientapi/auth.go:245-285`) uses the same shape. The Flutter side (`UserPayload.fromJson`) is lenient about everything.

Our own decoding (Go `encoding/json`) is tolerant of unknown keys on every endpoint; a known key with the wrong JSON type is a 400. No official client sends a type different from the ones listed below (checked in all three trees); `TestLoginClientShapes` and the heartbeat test pin the unknown-key tolerance.

## 3. Endpoint table

Status: **ok** = matches what 1.4.6, 1.5.0 and master expect; **partial** = works, some optional semantics missing; **n/a** = not a server concern. Client lines are master (`R`) unless tagged; 1.4.6 lines are given where they differ materially.

| # | Endpoint | Client expects | We do | Status |
|---|---|---|---|---|
| 1 | `GET /api/login-options` | 200 + JSON array of strings; `common-oidc/<json [{name,icon?}]>` wins, else `oidc/<op>`. 1.4.6 swallows errors; 1.5+ throws on non-2xx. `R:flutter/lib/models/user_model.dart:240-264`, `R@1.4.6:…/user_model.dart:222-245` | `["common-oidc/[{name}]", "oidc/<slug>"…]`, `[]` when none. `internal/oidc/clientroutes.go:71-108` | ok |
| 2 | `POST /api/login` (`type` account / absent) | 200 `{type:"access_token",access_token,user}`; errors `{error}` with any status. `R:flutter/lib/models/user_model.dart:192-235`, `R:src/ui/index.tis:1541` | `internal/clientapi/account.go:56-193`; 400 `{error}` on bad credentials, 429 + Retry-After when throttled | ok |
| 2a | `POST /api/login` → TOTP second factor | step 1 `{type:"email_check",tfa_type:"tfa_check",secret,user:{name}}`; step 2 `type:"email_code"`, `tfaCode`=`verificationCode`=6 digits. `R:flutter/lib/common/widgets/login.dart:790-818`, `1003-1024` | `account.go:133-176` (sealed 5-minute challenge, user/IP backoff) | ok |
| 2b | `POST /api/login` → email code | see §0 | not implemented here (feat/email-codes) | missing (other branch) |
| 2c | `POST /api/login` `type` mobile / sms_code | never sent by official clients (constants only, `R:flutter/lib/common/hbbs/hbbs.dart:10-20`) | 400 "Unsupported login type" (`account.go:177-179`) | n/a |
| 3 | `POST /api/currentUser` | Bearer; 200 UserPayload; **401 and 400 both log out** (400 keeps caches). `R@1.4.6:flutter/lib/models/user_model.dart:49-100` | `account.go:257-259`; auth failures are 401, storage errors 500, never 400 (`auth.go:39-57`) | ok |
| 4 | `POST /api/logout` | response ignored, 2 s timeout. `user_model.dart:170-190` | revoke token + unbind device, 200 empty (`account.go:264-296`) | ok |
| 5 | `POST /api/oidc/auth` | `{code,url}` (absolute URL) or `{error}`; status ignored. 1.4.6 parses with `resp.json()` (`R@1.4.6:src/hbbs_http.rs:24-42` `TryFrom<Response>`); 1.5+ via `post_request_sync` + `HbbHttpResponse::parse` | `internal/oidc/clientroutes.go:132-158` | ok |
| 6 | `GET /api/oidc/auth-query` | pending `{error:"No authed oidc is found"}`; success AuthBody with strict `user.name`+`user.info`, status ∈ {0,1,-1} (§2) | `clientroutes.go:160-253` | ok |
| 7–9 | `GET/POST /api/ab`, `POST /api/ab/get` (legacy blob) | `{data:"<json string>"}` or `null`; push = full replace, success 200 empty/`null`/object without error. `R@1.4.6:flutter/lib/models/ab_model.dart:960-1050`, `R:src/ui/ab.tis:646-672` | `internal/clientapi/ab_legacy.go:20-146` | ok |
| 10 | `POST /api/ab/personal` | no body; 200 `{guid}` (new API) or **404** (legacy). `R@1.4.6:ab_model.dart:227-252` | `internal/clientapi/ab.go:232-249`; 404 when `clientapi.ab_legacy_only` | ok |
| 11 | `POST /api/ab/settings` | `{max_peer_one_ab}` (0 = unlimited) | `ab.go:252-259` | ok |
| 12 | `POST /api/ab/shared/profiles?current&pageSize` | `{total,data:[{guid,name,owner,note,rule 1-3,info:{password?}}]}`; 1.4.6 errors on 404 | `ab.go:262-278` | ok |
| 13 | `POST /api/ab/peers?current&pageSize&ab` | `{total,data:[Peer]}`; `hash` in personal, `password` in shared, `same_server` | `ab.go:281-297` | ok |
| 14 | `POST /api/ab/tags/{guid}` | bare array `[{name,color}]` | `ab.go:300-315` | ok |
| 15–21 | AB mutations (`peer/add`, `peer/update`, `DELETE peer`, `tag/add`, `tag/rename`, `tag/update`, `DELETE tag`) | **exactly 200 + empty body** (`_jsonDecodeActionResp`, `R@1.4.6:ab_model.dart:1952-1976`); DELETE carries a JSON array body | `ab.go:319-528`, `writeEmpty` (`respond.go:46-50`) | ok |
| 22 | `GET /api/users?current&pageSize&accessible=&status=1` | `{total,data:[{name,display_name}]}`; total from page 1. `R@1.4.6:flutter/lib/models/group_model.dart:160-222` | `internal/clientapi/accessible.go:65-104` (admins: all; shared user group: group; else self) | ok |
| 23 | `GET /api/peers?current&pageSize&accessible=&status=1` | `{total,data:[{id,info:{username,os,device_name},user_name,device_group_name,note}]}`; platform icon from `info.os` prefix. `group_model.dart:224-280`, `hbbs.dart:77-131` | `accessible.go:125-149` | ok |
| 24 | `GET /api/device-group/accessible?current&pageSize` | `{total,data:[{name}]}`; errors swallowed; used to filter peers by `device_group_name`. `group_model.dart:104-150` | `accessible.go:157-175` (admins: all groups; others: groups of accessible devices) | ok |
| 25 | `POST /api/heartbeat` | body `{id,uuid,ver,conns?,modified_at}`; response keys `sysinfo`, `disconnect:[i32]`, `modified_at`, `strategy:{config_options,extra}`, all optional, unknown keys ignored. `R@1.4.6:src/hbbs_http/sync.rs:233-275` | `internal/clientapi/device.go:52-98`; kicks via `disconnect`; `strategy` only when a `StrategyProvider` is wired (none yet) | partial (no strategies) |
| 26 | `POST /api/sysinfo` | plain text `SYSINFO_UPDATED` / `ID_NOT_FOUND`; body = `get_sysinfo()` + `version,id,uuid` + `preset-*` keys. `R@1.4.6:sync.rs:131-226` | `device.go:142-285`. **Fixed here:** presets now also apply on the first sysinfo of a device pre-registered by `--deploy`, and `preset-address-book-{name,tag,alias,password,note}` are honoured | ok (`preset-strategy-name` ignored) |
| 27 | `POST /api/sysinfo_ver` | plain text version; only reachable for self-hosted in 1.4.0 | `device.go:287-289` | ok |
| 28 | `POST /api/audit/conn` | `new`/authorized/`close` events + controller note `{id,session_id,note}`; ≥1.5.0: 2xx empty = stored, nonce dedupe; ≤1.4.9 ignores response. `R@1.4.6:src/server/connection.rs:1255-1266`, `1463-1465` | `internal/clientapi/audit.go:132-224`; u64 session ids (`audit.go:29-50`) | ok |
| 29 | `POST /api/audit/file` | `{id,uuid,peer_id,type,path,is_file,info:"<json string>",conn_id?,nonce?}` (1.4.6 has no conn_id/nonce) | `audit.go:263-283` | ok |
| 30 | `POST /api/audit/alarm` | `{id,uuid,typ,info:"<json string>",conn_id?,nonce?,conn_audit_ref?}` | `audit.go:296-308` | ok |
| 31 | `GET /api/audit/conn/active?id&session_id&conn_type` | JSON string guid, `""` = retry. `R@1.4.6:flutter/lib/models/model.dart:1187-1240` | `audit.go:313-341` | ok |
| 32 | `PUT /api/audit` | `{guid,note}`, 200 = ok. `R@1.4.6:flutter/lib/common/widgets/dialog.dart:1664` | `audit.go:344-363` | ok |
| 33 | `POST /api/record?type&file…` | dead code in OSS builds (`record_upload::ENABLE` never set) | `{error}` → client aborts (`provisioning.go:316-318`) | ok |
| 34 | `POST /api/devices/deploy` (≥1.4.7) | `{result: OK/NOT_ENABLED/INVALID_INPUT/ID_TAKEN}` | `internal/clientapi/provisioning.go:65-124` | ok |
| 35 | `POST /api/devices/cli` (`--assign`) | empty body = "Done!", else printed. `R@1.4.6:src/core_main.rs:560-620` | `provisioning.go:189-271`; `strategy_name` → "Strategies are not supported by this server" | partial (no strategies) |
| 36 | `POST /api/switch-grant` (≥1.5.0) | `{accepted:true}` or `{accepted:false,server_time}` | `provisioning.go:277-311` (Ed25519 vs deploy key) | ok |
| — | `/lic/web/api/plugin-sign` | only with the `plugin_framework` cargo feature (`R@1.4.6:src/plugin/callback_msg.rs:283`), not in release builds; gone from master | not implemented | n/a |
| — | Software update check | `POST https://api.rustdesk.com/version/latest` (hard-coded in `H:src/lib.rs` `version_check_request`, `R:src/common.rs:1132-1172`); never the API server | — | n/a |
| — | Telegram 2FA notifications | `api.telegram.org` (`R:src/auth_2fa.rs:158-170`) | — | n/a |

Not called by any of the three client trees (despite appearing in third-party servers): `/api/ab/rules`, `/api/users?accessible` variants other than above, `/api/user/info`, `/api/version`, `/api/server-config*`, `/api/shared-peer`.

## 4. Fixed on this branch

1. **Sysinfo presets after deploy.** A device created by `rustdesk --deploy` (`provisioning.go:101-113`) has no `sysinfo_at`; previously its first `/api/sysinfo` went through the update path and every `preset-*` key was dropped. Now the first sysinfo (`d.SysinfoAt == nil`) applies the presets to unset fields only (`device.go:177-192`, `228-262`).
2. **Address-book presets.** `preset-address-book-name/-tag/-alias/-password/-note` (sent since ≤1.4.6, `R@1.4.6:src/hbbs_http/sync.rs:135-154`) were ignored. They now upsert the device into the named shared address book on first sysinfo, using the same rules as `rustdesk --assign` (shared helper `applyABPreset`, `provisioning.go:147-185`). An unknown or ambiguous book name is logged and skipped; the device is still registered.
3. Contract tests: `TestSysinfoPresetsAfterDeploy` (`internal/clientapi/device_test.go`), `TestLoginClientShapes` (Sciter / web / type-less login bodies, unknown keys) and an unknown-key heartbeat case.

## 5. Left open

- **Strategies** (`preset-strategy-name`, `strategy_name` in `--assign`, heartbeat `strategy`): the plumbing (`StrategyProvider`, `clientapi.go:62-75`) exists, but there is no strategy model or admin UI. Clients work without it.
- **Email login codes**: §0, owned by `feat/email-codes`.
- **`ID_NOT_FOUND` sysinfo answer**: we never return it (we create the device). Only useful if the panel were to require hbbs registration first.
- **Raw-TCP API fallback** (≥1.4.7) and token checks in hbbs: need a custom hbbs; out of scope for the HTTP server.
- **Device enable/disable** in `/api/peers` (`status`): the client reads the field but does not use it; we always send 1.
