# RD Panel — specification

A self-hosted **API server + admin panel** for RustDesk, wire-compatible with official RustDesk
clients (1.3.x–1.4.x) and with the hbbs/hbbr fork at
[bropines/rustdesk-server](https://github.com/bropines/rustdesk-server) (branch `rdpanel`).
It replaces [lejianwen/rustdesk-api](https://github.com/lejianwen/rustdesk-api) v2.7 (+ rustdesk-api-web)
and ships as one s6 container together with hbbs/hbbr.

Research this spec is based on (read them, they are normative where this file defers to them):

| doc | covers |
|---|---|
| `docs/research/client-api.md` | every HTTP endpoint the RustDesk client calls, exact JSON |
| `docs/research/legacy-model-and-migration.md` | legacy DB, auth semantics, migration mapping, critique |
| `docs/research/hbbs-integration.md` | hbbs/hbbr admin channel, online status, JWT/MUST_LOGIN, build |
| `docs/research/legacy-rustdeskapi-schema.sql` | production legacy schema + row counts |

## 1. Goals / non-goals

Goals
1. **Drop-in replacement**: same API base URL (`https://desk-api.example.com`), clients keep working
   without re-login after migration (same user ids, same `RUSTDESK_API_JWT_KEY`, migrated tokens).
2. **Better panel**: fast, clean UI (React), proper roles, live device status, audit, 2FA, OIDC.
3. **Secure by default**: fixes every item in the legacy critique (§8 of the migration doc).
4. **Operable**: single static binary, SQLite, zero external deps, one-command legacy migration,
   health endpoint, structured logs, Prometheus metrics.

Non-goals (v1)
- Bundling the RustDesk Flutter web client (AGPL) — not shipped. A link-out to an external web client URL is allowed.
- Multi-instance / Postgres (keep the DB layer portable, but only SQLite is tested in v1).
- LDAP, linux.do provider, self-registration (keep data model ready; UI hidden).

## 2. Architecture

```
┌──────────────── s6 container (network_mode: host) ────────────────┐
│ hbbs (21115 admin-loopback, 21116 tcp/udp, 21118 ws)                │
│ hbbr (21117, 21119 ws)                                              │
│ rdpanel  ── HTTP :21114 (bind addr configurable, prod 127.0.0.1:31114)│
│    ├─ /api/*            client API (RustDesk wire contract)          │
│    ├─ /api/v1/admin/*   panel JSON API (cookie session + CSRF)       │
│    ├─ /api/v1/events    SSE: presence + audit stream                 │
│    ├─ /metrics /healthz                                              │
│    └─ /*                embedded React SPA                           │
│    workers: presence poller (OnlineRequest → 127.0.0.1:21116, 5 s),  │
│             hbbs/hbbr control (loopback admin ports, reconciler),    │
│             janitor (expired tokens/states, retention)               │
└────────────────────────────────────────────────────────────────────┘
       data volume: /data  → panel.db (SQLite WAL), secret.key, hbbs key + db_v2.sqlite3
```

### Backend (Go 1.26, module `github.com/bropines/rdpanel`)
- `CGO_ENABLED=0`, SQLite via `modernc.org/sqlite`, WAL, `foreign_keys=ON`, `busy_timeout=5000`.
- Router: `github.com/go-chi/chi/v5`. No global singletons: an `app.App` struct with injected deps; `context.Context` everywhere.
- DB access: `database/sql` + small typed repositories (no ORM). Migrations: embedded SQL files, applied by
  `pressly/goose/v3` at startup. All timestamps stored as UTC unix milliseconds `INTEGER` (no TZ ambiguity).
- Config: env vars `RDPANEL_*` + optional YAML file; `rdpanel config print` shows effective config with secrets redacted.
- Logging: `log/slog` JSON. Every admin mutation also writes an `admin_audit` row.
- Layout:
  ```
  cmd/rdpanel/            main: serve | migrate-legacy | user (create/reset-pw/2fa-reset) | config print
  internal/app/           wiring, lifecycle
  internal/config/
  internal/db/            open, goose migrations (internal/db/migrations/*.sql), tx helper
  internal/store/         repositories per aggregate (users, tokens, devices, addressbooks, ...)
  internal/auth/          passwords (bcrypt/argon2id + legacy md5 verify), JWT, sessions, TOTP, rate limiter
  internal/crypto/        AEAD (XChaCha20-Poly1305) for secrets at rest, key file mgmt
  internal/oidc/          providers (github, google, generic oidc), state store (DB-backed), PKCE
  internal/clientapi/     RustDesk wire API handlers (/api/*) — one file per area
  internal/adminapi/      panel API handlers (/api/v1/admin/*)
  internal/hbbs/          presence poller (OnlineRequest protobuf, hand-encoded), command client, reconciler
  internal/events/        in-process pub/sub → SSE
  internal/legacy/        lejianwen v2.7 importer (dry-run + report)
  internal/web/           go:embed of web/dist, SPA fallback, security headers
  web/                    React app
  deploy/                 Dockerfile (s6), rootfs, compose example
  ```

### Frontend (`web/`)
React 18 + TypeScript (strict) + Vite, TanStack Router + TanStack Query, Tailwind CSS + shadcn/ui (Radix),
react-hook-form + zod, i18n (en, ru; ru default when browser is ru). Light/dark. Built to `web/dist`, embedded.
Typed API client generated from `api/openapi.yaml` (openapi-typescript) — the OpenAPI file is the contract
between backend and frontend for `/api/v1/admin/*`.

## 3. Client API (RustDesk wire contract)

Implement **exactly** what `docs/research/client-api.md` documents (paths, JSON field names, envelopes,
status codes, polling behaviour). Compatibility rules that override "cleanliness":
- Client tokens are HS256 JWT `{"user_id": <u32>, "exp": <unix>}` signed with `RUSTDESK_API_JWT_KEY`
  (the same key hbbs uses for MUST_LOGIN). DB stores only `sha256(token)`. Validation = JWT signature+exp
  **and** DB row exists, not revoked, sliding `expires_at` (extend when < 1/3 lifetime left, default lifetime 4380h).
- `Authorization: Bearer <token>` (accept case-insensitive scheme; reject anything else).
- Passwords: verify bcrypt `$2a$/$2b$`, legacy `md5_hex(pw+"rustdesk-api")`, argon2id; rehash to argon2id on success; empty hash never matches; no 32-char cap.
- OIDC for clients: `/api/login-options`, `/api/oidc/auth` → `{"code","url"}`, `/api/oidc/auth-query` polling,
  callback `<public_url>/api/oidc/callback`. State/PKCE stored in DB (`oauth_states`, 5 min TTL), bound to the
  requesting device `id`/`uuid`; the query must present the same `id`/`uuid`.
- Address-book entries: `password`/`hash` returned byte-for-byte (stored AEAD-encrypted); legacy JSON key spellings kept.
- Legacy address-book guid `<group_id>-<user_id>-<collection_id>` must still resolve after migration;
  new books use stable guids that do not embed the group id.
- Heartbeat/sysinfo hardening (legacy endpoints were unauthenticated): accept unauthenticated calls as the
  client does, but (a) never let a heartbeat create a device or change its `uuid`; (b) sysinfo may create a
  device, may change `uuid` only if the device has no bound user or the request carries a valid token for the
  owner; (c) per-IP rate limits; (d) audit endpoints require the reporting device `uuid` to match the stored one.
- Heartbeat response supports `modified_at` (sysinfo re-upload trigger) and `disconnect` (admin "kick" from panel).
- Critical wire gotchas (details + citations in client-api.md; legacy got these wrong):
  - address-book write calls (add/update/delete/tags) → **HTTP 200 with an empty body** (`{}`/`null` shows error "null");
  - audit uploads → 2xx **empty body**; de-duplicate on `nonce` (≥5 min) because 1.5.0 retries otherwise;
  - `/api/ab/personal` → **404** to put the client into legacy address-book mode only when that mode is intended;
  - `/api/currentUser` 400 and 401 both log the user out — return 401 only for real auth failures;
  - OIDC `auth-query` success must include `user.info` object and `user.status` ∈ {0 disabled, 1 normal, -1 unverified};
  - paging params are `current` + `pageSize`; `total` read from the first page;
  - `session_id` is u64 — store as TEXT/INTEGER exactly, never float;
  - `/api/sysinfo` responds plain text `SYSINFO_UPDATED` / `ID_NOT_FOUND`;
  - DELETE requests carry JSON bodies; some POSTs have `Content-Length: 0`.
  - Also implement endpoints legacy lacks: `PUT /api/audit` (session note), `POST /api/audit/alarm`,
    `GET /api/audit/conn/active`, `POST /api/devices/deploy`, `POST /api/devices/cli`, `POST /api/switch-grant`.
- Licensing: clean-room implementation; do **not** copy AGPL code (RustDesk client/server, incl. `.proto` files) —
  hand-encode the few protobuf messages needed. MIT for this repo; keep lejianwen's MIT notice only if code is lifted.

## 4. Admin panel

Roles: `admin`, `user`. Users see "My" area; admins also see "System". Session = HttpOnly Secure SameSite=Lax
cookie holding an opaque token (sha256 in DB, `kind=web`), 12 h idle / 7 d absolute; CSRF: double-submit
header `X-CSRF-Token` on all non-GET. Login: password (+ TOTP if enabled) or OIDC; per-IP + per-username
exponential backoff; optional "require 2FA for admins". First admin created by `rdpanel user create --admin`
(password from prompt/stdin, never logged).

Pages (MVP):
1. **Dashboard** — online devices now, logins/connections last 24 h, hbbs/hbbr health, version.
2. **Devices** — live table (SSE presence), filters (online, owner, group, OS, version, last seen), device drawer
   (sysinfo, owner, group, alias, note, recent connections), bulk: assign owner/group, add to address book,
   delete (revokes bound tokens), kick (heartbeat `disconnect`). CSV export.
3. **Users** — CRUD, roles, enable/disable, reset password, force 2FA reset, linked identities, active
   sessions/tokens with revoke, per-user devices and address books. Last admin protection.
4. **User groups / device groups** — CRUD.
5. **Address books** — one unified page: book list (personal + shared) → entries grid with tags; share dialog
   (user/group, read / read-write / full); bulk add from devices; tag manager with colors.
6. **Audit** — connections, file transfers, logins, admin actions; filters + retention settings.
7. **Identity providers** — GitHub, Google, generic OIDC (discovery), PKCE, auto-register (links existing users
   only when `email_verified` and setting allows), test button. Secrets write-only in UI.
8. **Server** — hbbs/hbbr settings via typed controls: relay servers, always-use-relay, must-login, IP
   blocker/changes viewers, punch-requests viewer, blacklist/blocklist editor, bandwidth limits. Desired state is
   stored in DB and re-applied when hbbs/hbbr restarts (reconciler). Raw console for admins (allow-listed commands).
9. **Settings** — public URL, branding (title, logo, sanitized markdown welcome text), token lifetimes,
   retention, rate limits, trusted proxies.
10. **My** — profile, password, 2FA (TOTP enroll with QR + recovery codes), linked identities, my devices,
    my address books, my sessions.

## 5. Live presence

`internal/hbbs` polls hbbs every 5 s with one batched `OnlineRequest` (TCP `127.0.0.1:21116`, skip the
unsolicited `KeyExchange`, bitmap MSB-first) for all known device ids (chunk 500). Transitions emit events →
SSE → UI. `devices.last_seen_at` updated on online observation (throttled to 60 s writes). Client heartbeats are a
secondary signal (also used for connection counts/version). If hbbs is unreachable, presence falls back to
"heartbeat within 45 s" and the dashboard shows hbbs as down.

## 6. Data model

Start from `legacy-model-and-migration.md` §7.3, adapted to SQLite (unix-ms INTEGER timestamps, TEXT enums
with CHECK constraints, real FKs with ON DELETE rules, UNIQUE constraints). Additional tables:
`sessions` (or `auth_tokens.kind='web'`), `oauth_states`, `totp_secrets` (encrypted) + `recovery_codes` (hashed),
`admin_audit`, `server_settings` (desired hbbs/hbbr state), `settings` (key/value), `migration_runs`.
Legacy ids preserved for users, user_groups, address books (collections), device_groups, identity providers.

## 7. Legacy migration

`rdpanel migrate-legacy --from /path/rustdeskapi.db [--dry-run] [--report out.json]` implements §7.4 of the
migration doc in one transaction, idempotent refusal if the target DB is non-empty (unless `--force`).
Operator guide: `docs/migration.md`.
Reports duplicates, orphans, collisions, pre-v245 Google identities. Production volumes: users 2, tokens 58
(45 client JWT / 2 unexpired), peers 53, address-book entries 34, groups 2, oauths 2 (github, google),
user_thirds 3, login_logs 73, audit_conns 1086, audit_files 54.

## 8. Packaging & deploy

- `deploy/Dockerfile`: alpine + s6-overlay; copies `hbbs`, `hbbr` (built from bropines/rustdesk-server) and
  `rdpanel`. s6 services: `key-secret` (oneshot), `hbbs`, `hbbr`, `panel`. Env passthrough: `RELAY`,
  `ENCRYPTED_ONLY`, `MUST_LOGIN`, `ALWAYS_USE_RELAY`, `RUSTDESK_API_JWT_KEY`, `RDPANEL_*`.
- `.github/workflows/ci.yml`: go vet + test (race), web typecheck + lint + test + build, govulncheck.
- `.github/workflows/image.yml`: on tag/main → build hbbs/hbbr (cross, amd64+arm64) from the server repo ref,
  build web+panel, buildx multi-arch → `ghcr.io/bropines/rdpanel:{version,latest}` using `GITHUB_TOKEN` only.
- Production: compose with `network_mode: host`, panel bound to `127.0.0.1:31114` (Angie vhost
  `desk-api.example.com` already proxies there), data dirs `hbbs-data` + new `panel-data`.

## 9. Quality bar

- Go: `go vet`, `staticcheck`, unit tests per package, HTTP contract tests for **every** client endpoint using
  golden JSON derived from client-api.md, migration tests against a synthetic legacy DB built from
  `legacy-rustdeskapi-schema.sql`, race detector in CI.
- Web: `tsc --noEmit`, ESLint, Vitest for logic, accessibility (Radix), no `dangerouslySetInnerHTML` except the
  sanitized welcome markdown (DOMPurify).
- Security: CSP (`default-src 'self'`), HSTS when public URL is https, `X-Frame-Options: DENY`, secrets never in
  logs, constant-time comparisons, trusted-proxy list for client IP (default: loopback only).

## 10. Work breakdown

| # | stream | depends on |
|---|---|---|
| F | Foundation: module, config, db+migrations (full schema), crypto, auth core, app wiring, chi server, web scaffold + embed, OpenAPI skeleton, Makefile, CI | — |
| A | Client API (`/api/*`) + contract tests | F |
| B | Admin API (`/api/v1/admin/*`) + sessions/CSRF/2FA + OIDC + OpenAPI | F |
| C | hbbs integration: presence poller, command client, reconciler, SSE | F |
| D | Web UI (all pages) against OpenAPI | F (+B contract) |
| E | Legacy migration tool + tests | F |
| G | Packaging: Dockerfile/s6, image workflow, compose, docs | F |
