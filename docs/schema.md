# Database schema (v1)

SQLite, opened by `internal/db` with `journal_mode=WAL`, `foreign_keys=ON`,
`busy_timeout=5000`, `synchronous=NORMAL` and `BEGIN IMMEDIATE` transactions.
Migrations are embedded goose files in `internal/db/migrations/` and are applied
automatically on start (`rdpanel serve`, `rdpanel user ...`).

| file | contents |
|---|---|
| `00001_identity.sql` | user_groups, users, auth_tokens, identity_providers, user_identities, oauth_states, totp_secrets, recovery_codes |
| `00002_devices_addressbooks.sql` | device_groups, devices, address_books, address_book_shares, address_book_entries, address_book_tags, address_book_entry_tags, share_links |
| `00003_audit_settings.sql` | login_events, connection_audits, file_transfer_audits, alarm_audits, audit_nonces, admin_audit, server_commands, server_settings, settings, migration_runs |
| `00004_device_public_key.sql` | devices.pk |
| `00005_b2a_panel.sql` | panel_assets (branding logo), presence_samples (dashboard online series) |

Later streams add new numbered files; never edit an applied migration.

## Conventions

- **Timestamps**: `INTEGER` UTC unix **milliseconds** (`*_at`). `NULL` = never/unset.
  Go helpers: `store.toMs` / `store.fromMs`.
- **Booleans**: `INTEGER` 0/1 with `CHECK (x IN (0, 1))`.
- **Enums**: `TEXT` with `CHECK (x IN (...))`.
- **JSON**: `TEXT` with `CHECK (json_valid(x))`.
- **Ids**: `INTEGER PRIMARY KEY AUTOINCREMENT` — ids are never reused, which matters
  because client JWTs embed `user_id` and hbbs cannot see revocation. The legacy
  importer preserves ids of `users`, `user_groups`, `address_books` (collections),
  `device_groups` and `identity_providers`, then the sequence continues after `max(id)`.
- **Secrets**: verify-only secrets are stored as `sha256` digests (`*_sha256 BLOB`, 32 bytes);
  secrets that must be returned are AEAD-encrypted (`*_enc BLOB`, XChaCha20-Poly1305,
  key from `secret.key` / `RDPANEL_SECRET_KEY`) with associated data `"<scope>:<row id>"`
  so ciphertexts cannot be moved between rows or columns. The scopes are fixed in
  `internal/crypto/ad.go` (always use its helpers): `ab_entry.password`, `ab_entry.hash`,
  `address_book.password`, `identity_provider.client_secret`, `share_link.password`,
  `oauth_state.pkce_verifier`, `totp` (keyed by user id).
- **Foreign keys** are real, with explicit `ON DELETE` rules (CASCADE for owned rows,
  SET NULL for optional references, so audit trails survive deletions).
- **Case**: usernames and group names are unique case-insensitively (`COLLATE NOCASE`);
  the legacy importer must report case-only username collisions instead of renaming.

## Identity

### `user_groups`
User groups (legacy `groups`).

| column | notes |
|---|---|
| id | preserved from legacy |
| name | unique, case-insensitive |
| visibility | `self` (members see only their own devices; legacy type 1) / `shared` (members see each other's devices and users in the client; legacy type 2) |
| note | admin note |
| created_at, updated_at | |

### `users`

| column | notes |
|---|---|
| id | `1..2^32-1` (CHECK) — embedded as `user_id: u32` in client JWTs; preserved from legacy |
| username | unique `NOCASE`, 1–128 chars |
| email | optional; indexed (non-unique) for OIDC linking |
| display_name, avatar_url, note | profile (legacy nickname/avatar/remark) |
| password_hash, password_algo | both NULL = no local password (OIDC-only). algo: `argon2id` (new), `bcrypt`, `legacy_md5_rustdesk_api` (`md5_hex(pw+"rustdesk-api")`); rehashed to argon2id on successful login |
| role | `admin` / `user` |
| status | `active` / `disabled` (clients get 1 / 0) |
| user_group_id | → user_groups, `SET NULL` (NULL behaves like a `self` group) |
| totp_required | admin-forced 2FA enrolment |
| password_changed_at, last_login_at | |
| created_at, updated_at | |

### `auth_tokens`
Client tokens and panel sessions. The plain token is never stored.

| column | notes |
|---|---|
| user_id | → users, CASCADE |
| kind | `client` (RustDesk app, HS256 JWT `{"user_id","exp"}`) / `web` (panel cookie session, opaque 256-bit) |
| token_sha256 | unique lookup key |
| jwt_expires_at | client: the fixed JWT `exp` (what hbbs enforces) |
| expires_at | client: sliding expiry (extended when < 1/3 lifetime left); web: idle expiry |
| absolute_expires_at | web: hard cap (default 7 d) |
| device_rustdesk_id, device_uuid, client_platform | client tokens: device the login came from (device deletion revokes by uuid) |
| user_agent, ip | for the sessions UI |
| mfa_pending | web: password accepted, TOTP step still owed |
| created_at, last_used_at | |
| revoked_at, revoke_reason | revocation (logout, password reset, admin) |
| legacy_id | `user_tokens.id` for migrated rows |

A token is active when `revoked_at IS NULL` and every non-NULL expiry is in the future
(`store.AuthTokens.GetActive`). The janitor deletes rows inactive for more than 24 h.

### `identity_providers`
OAuth/OIDC providers (legacy `oauths`).

| column | notes |
|---|---|
| id | preserved from legacy |
| slug | unique; legacy `op` verbatim — clients see `oidc/<slug>`; identities key on it |
| kind | `github`, `google`, `oidc`, `linuxdo` |
| display_name | button label |
| client_id, client_secret_enc | secret AEAD-encrypted, write-only in the UI |
| issuer | OIDC discovery issuer |
| scopes | JSON array of strings (legacy comma list split) |
| pkce_enabled, pkce_method | `S256` / `plain` |
| auto_register | create a local user on first login |
| link_by_verified_email | link to an existing user by email **only** if the IdP asserts `email_verified` |
| enabled, sort_order | |

### `user_identities`
Links between local users and IdP subjects (legacy `user_thirds`).
`UNIQUE(provider_id, subject)`; `subject` = OIDC `sub` / GitHub numeric id (legacy `open_id` verbatim).
Columns: user_id (CASCADE), provider_id (CASCADE), subject, email, email_verified,
display_name, username, avatar_url, legacy_union_id, last_login_at, timestamps.

### `oauth_states`
Pending OAuth flows (replaces the legacy in-memory map), TTL 5 min, purged by the janitor.

| column | notes |
|---|---|
| state_sha256 | sha256 of the state (= the client's poll `code`) |
| action | `login`, `bind` (link identity to the logged-in panel user), `webauth` (approve a client login from a panel session) |
| flow | `client` (RustDesk app polling `/api/oidc/auth-query`) / `web` (panel) |
| provider_id | NULL only for `webauth` |
| pkce_verifier_enc, nonce | PKCE verifier (AEAD), OIDC nonce |
| device_rustdesk_id, device_uuid, device_name, device_os, device_type | requesting device; the poll must present the same id/uuid |
| redirect_path | panel return path |
| initiator_user_id | `bind`/`webauth`: panel user |
| result_user_id, result_error | set by the callback/approval, consumed by the poll |
| profile_json | IdP profile awaiting manual bind |
| consumed_at, created_at, expires_at | |

### `totp_secrets`
One row per user: `secret_enc` (AEAD, AD `totp:<user_id>`), `confirmed_at` (NULL while
enrolling), `last_used_step` (replay protection: codes at or before this 30 s step are refused).

### `recovery_codes`
`code_sha256` of normalised one-time codes (`auth.HashRecoveryCode`), `used_at`.

## Devices and address books

### `device_groups`
`name` (unique NOCASE), `note`. Ids preserved from legacy.

### `devices`
One row per RustDesk ID (legacy `peers`, deduplicated).

| column | notes |
|---|---|
| rustdesk_id | unique |
| uuid | client machine uuid (base64); changes restricted (SPEC §3) |
| hostname, os, cpu, memory, os_username, client_version | from `/api/sysinfo` |
| alias, note | admin-set |
| owner_user_id | → users, SET NULL (bound at client login) |
| device_group_id | → device_groups, SET NULL |
| last_seen_at, last_seen_ip | last observation (hbbs presence or heartbeat), throttled writes |
| last_heartbeat_at | last `/api/heartbeat` |
| sysinfo_at | last sysinfo upload |
| sysinfo_requested_at | heartbeat replies with `sysinfo` while `sysinfo_at` is older |

### `address_books`

| column | notes |
|---|---|
| id | preserved legacy collection id (personal books get new ids) |
| guid | stable public id (`?ab=<guid>`), never embeds a group id |
| owner_user_id | → users, CASCADE |
| name, note | |
| is_personal | at most one personal book per user (partial unique index) |
| legacy_collection_id | legacy collection id; 0 = legacy personal book |
| legacy_guid | `"<group_id>-<user_id>-<collection_id>"`, still accepted from cached clients |
| default_password_enc | shared books: default password (`info.password` in `shared/profiles`) |

### `address_book_shares`
Exactly one of `grantee_user_id` / `grantee_group_id` (CHECK), both CASCADE;
`permission` `read` / `read_write` / `full`; unique per (book, grantee).
Effective permission = max over the user's own share and their group's share; owners have `full`.

### `address_book_entries`
`UNIQUE(address_book_id, rustdesk_id)`.

| column | client JSON key |
|---|---|
| rustdesk_id | `id` |
| alias | `alias` |
| os_username | `username` |
| hostname | `hostname` |
| platform | `platform` |
| login_name | `loginName` |
| note | `note` |
| password_enc | `password` (AEAD, returned byte-for-byte; shared books) |
| hash_enc | `hash` (AEAD, returned byte-for-byte; personal books) |
| force_always_relay | `forceAlwaysRelay` |
| rdp_port, rdp_username | `rdpPort`, `rdpUsername` |
| same_server | `sameServer` |

The legacy `online` column is dropped (stale client value).

### `address_book_tags`
`UNIQUE(address_book_id, name)`; `color_argb` = Flutter ARGB uint32 (`0..4294967295`).

### `address_book_entry_tags`
Join table (`WITHOUT ROWID`), both CASCADE. A trigger rejects linking a tag to an entry of another book.

### `share_links`
Web-client share links: `token_sha256` (unique), `device_rustdesk_id`, `password_mode`
(`once`/`fixed`), `password_enc`, `expires_at`, `used_at`, owner `user_id` (CASCADE).

## Audit

### `login_events`
Every login attempt: `user_id` (SET NULL), `username_attempted` (failed attempts),
`token_id` (SET NULL), `method` (`password`/`oidc`/`webauth`), `result`
(`success`/`failure`), `failure_reason`, `client_kind` (legacy `client`), device id/uuid,
`platform`, `ip`, `user_agent`, `hidden_by_user` (legacy soft delete), `created_at`.

### `connection_audits`
From `/api/audit/conn` (controlled side). `guid` (unique; returned by
`/api/audit/conn/active`, used by `PUT /api/audit` notes), device id/uuid, `conn_id`
(client-local counter), `session_id` (u64 as TEXT), `conn_type` (0 desktop, 1 file,
2 port forward, 3 camera, 4 terminal), `primary_auth`, `two_factor`, `conn_audit_ref`,
controller id/name/ip, `note`, `started_at`, `ended_at`.
The legacy importer must generate a `guid` for every migrated row.

### `file_transfer_audits`
From `/api/audit/file`: device id/uuid, controller id/name/ip, `direction`
(0 remote→local, 1 local→remote), `is_file`, `path`, `file_count`, `info` (client JSON;
non-JSON legacy text wrapped as `{"raw": "..."}`).

### `alarm_audits`
From `/api/audit/alarm` (client ≥ 1.5.0): `alarm_type`, `conn_id`, `conn_audit_ref`, `info` JSON.

### `audit_nonces`
Client audit retry de-duplication: `nonce` primary key, kept 15 minutes (janitor).

### `admin_audit`
Every admin mutation (panel or CLI): `actor_user_id` (SET NULL) + `actor_name` snapshot
(`cli` for command-line actions), `actor_ip`, `action` (dotted verb, e.g. `user.create`),
`target_type`, `target_id`, `details` JSON (never secrets), `created_at`.

## Server control and settings

### `server_commands`
Saved custom console commands (legacy `server_cmds`): `target` (`hbbs`/`hbbr`; legacy
`"21115"`/`"21117"`), `cmd`, `alias`, `option_hint`, `description`. Unique (target, cmd).

### `server_settings`
Desired hbbs/hbbr runtime state re-applied by the reconciler. PK (`target`, `key`);
`value` JSON; `updated_by`, `updated_at`, `applied_at`, `last_error`.

### `settings`
Panel key/value settings (`key` PK, `value` JSON, `updated_by`, `updated_at`).
Well-known keys are constants in `internal/store/settings.go`.

Keys written by `PATCH /settings` are constants in `internal/store/panel.go`
(`panel.public_url`, `branding.*`, `auth.*`, `retention.*_days`, `rate_limits.*`,
`http.trusted_proxies`); unset keys fall back to the configuration / built-in defaults.

### `panel_assets`
Small binary assets served by the panel: `name` PK (`branding.logo`), `content_type`
(`image/png`/`image/jpeg`/`image/webp`, SVG is refused), `data` (≤ 256 KiB), `sha256`
(cache-busting `?v=`), `updated_by`, `updated_at`.

### `presence_samples`
Hourly maximum of online devices (`hour_start` unix ms PK, `max_online`), written every
minute by the presence sampler and kept 7 days; feeds the dashboard `series.online`.

### `migration_runs`
Legacy import bookkeeping: `source`, `source_path`, `source_version` (expect 265),
`dry_run`, `status` (`running`/`succeeded`/`failed`), `report` JSON, `started_at`, `finished_at`.
