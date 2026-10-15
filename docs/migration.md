# Migrating from lejianwen/rustdesk-api

`rdpanel migrate-legacy` imports the SQLite database of
[lejianwen/rustdesk-api](https://github.com/lejianwen/rustdesk-api) v2.7 (`data/rustdeskapi.db`,
schema version 265) into a fresh rustdesk-panel database. The mapping is specified in
`docs/research/legacy-model-and-migration.md` §7.4. This page is the operator guide.

```
rdpanel migrate-legacy --from <rustdeskapi.db> [--dry-run] [--report <file>] [--force]
                       [--allow-version N] [--legacy-tz Z]
```

| flag | meaning |
|---|---|
| `--from` | legacy database. It is opened **read-only and immutable** and is never modified. |
| `--dry-run` | performs the whole import in a transaction and rolls it back. The report is still produced and nothing is written to the target. |
| `--report` | writes the JSON report (counts and warnings, never secrets) to this file. The file is created with mode 0600. |
| `--force` | imports even if the target already has users or a completed import. Legacy ids are preserved, so any id conflict still aborts the import. |
| `--allow-version N` | accepts legacy schema version `N` instead of 265. |
| `--legacy-tz` | time zone for legacy timestamps that carry **no** UTC offset. rustdesk-api always writes an offset, so the default `UTC` is normally correct. |

The target is the database configured for `rdpanel serve` (`RDPANEL_DATA_DIR` / `RDPANEL_DB_PATH`).
Migrations are applied first. The import uses the same secrets key (`secret.key` / `RDPANEL_SECRET_KEY`)
as the server, so **run it with the same configuration as the server**. Otherwise the server cannot decrypt
the imported OAuth client secrets and address-book passwords.

Exit codes: 0 success, 1 failure (nothing committed), 2 usage error.

## Before you start

1. **Keep `RUSTDESK_API_JWT_KEY` byte-identical.** Migrated client logins are HS256 JWTs signed with it,
   and hbbs verifies them with the same key when `MUST_LOGIN` is on. If the key changes, every RustDesk
   client has to log in again.
2. Keep the public URL, so that the OAuth redirect URI `<public url>/api/oidc/callback` registered at
   GitHub/Google/your IdP stays valid.
3. Use a **new, empty** `panel-data` directory. The importer refuses a target that already has users.

## 1. Back up and freeze the legacy database

Stop rustdesk-api so that nothing writes to the file. Then copy the database:

```sh
docker stop rustdesk-api            # keep the container; do not remove it
cp -a ./data/rustdeskapi.db ./rustdeskapi-$(date +%F).db
sha256sum ./data/rustdeskapi.db ./rustdeskapi-$(date +%F).db
```

If a non-empty `rustdeskapi.db-wal` exists next to the file, the importer refuses to run, because
immutable mode would ignore the transactions in the WAL. Either checkpoint it
(`sqlite3 rustdeskapi.db 'PRAGMA wal_checkpoint(TRUNCATE)'`) or make a consistent copy with
`sqlite3 rustdeskapi.db ".backup rustdeskapi-copy.db"`, and import the copy.

## 2. Dry run

```sh
rdpanel migrate-legacy --from ./rustdeskapi-copy.db --dry-run --report dry-run.json
```

The summary lists, per legacy table: rows **read**, **imported**, **deduped** (merged into another row
with the same natural key), **orphaned** (the referenced parent is missing, so the row was dropped) and
**skipped** (dropped on purpose, e.g. expired tokens). `read = imported + deduped + orphaned + skipped`.

## 3. Read the report

`errors` block the import. Nothing is committed while any error is present, including in dry-run mode.

| error code | what to do |
|---|---|
| `username_case_collision` | Two users differ only in letter case (`Alice` / `alice`). Usernames are case-insensitive now. Rename or delete one of them in the legacy panel, copy the DB again and re-run. |
| `invalid_username`, `user_id_range` | Fix the user in the legacy DB. |
| `version` | The schema is not v265. Check the rustdesk-api version. Use `--allow-version` only after reviewing the differences. |

`warnings` do not block the import. They tell you what changed:

| warning code | meaning / action |
|---|---|
| `google_email_subject` | Google link created before rustdesk-api v245, keyed by e-mail instead of Google `sub`. It will not match on the next Google login. The user must link Google again once (Profile → linked accounts), or the admin re-links it. |
| `email_linking_changed` | Legacy auto-register linked IdP logins to existing users **by e-mail without checking `email_verified`**. The new provider is imported with `link_by_verified_email = off`. Enable it in the provider settings if you want verified-e-mail linking. |
| `user_no_login_method` | The user has no local password and no linked identity, so set a password (`rdpanel user reset-password`). |
| `unknown_password_format` | The stored password is neither bcrypt nor legacy md5. The user cannot log in with a password until it is reset. |
| `unknown_user_status` | The status was not 1/2. The user was imported as disabled. |
| `orphan_user_group` | The user's group no longer exists. The user was assigned the default group (id 1), or none if there is no group 1. |
| `token_expires_soon` | This login will expire within 7 days of the import, so expect that device to log in again soon. |
| `opaque_client_tokens` | Legacy ran without a JWT key, so its client tokens are opaque md5 strings. The new server requires JWT client tokens, so those devices log in again. |
| `jwt_user_mismatch`, `jwt_without_exp` | Inconsistent tokens. They are dropped (mismatch) or reported. |
| `duplicate_peer` | Several legacy rows had the same RustDesk ID. The row seen most recently wins, and empty fields are filled from the others. |
| `duplicate_ab_entry`, `duplicate_provider`, `duplicate_identity` | Duplicates were merged. The kept row is named in the message. |
| `orphan_*` | References to deleted users, collections or groups. These rows are dropped, or the reference is cleared. |
| `invalid_tags_json` | An address-book entry had unparsable `tags`. It was imported without tags, as legacy did. |
| `renamed` | A group name was empty or a case-insensitive duplicate, so `(id)` was appended. |
| `bad_timestamp` | Some timestamps could not be parsed. The import time, or a sibling timestamp, was used instead. |
| `null_auto_register` | Legacy would have crashed on this provider's callback. It was imported with auto-register off. |
| `invalid_audit_info` | Non-JSON file-audit info was stored as `{"raw": "..."}`. |

The `extra` object of each table gives further numbers, such as `expired`, `expired_jwt`,
`disabled_user`, `personal_books_created`, `tags_created_from_entries` and `unparseable_timestamps`.

## 4. Run

```sh
rdpanel migrate-legacy --from ./rustdeskapi-copy.db --report import.json
```

The whole import is one transaction. Either everything is committed or nothing is. A successful run
is recorded in `migration_runs` together with its report. A failed run is recorded with `status = failed`
and leaves no other data behind. A second run is refused while a completed import exists.

## 5. Verify

- Compare `import.json` with the dry-run report. The counts should be identical.
- Spot-check with `sqlite3 panel-data/panel.db`:
  ```sql
  SELECT id, username, role, status, password_algo FROM users;
  SELECT slug, kind, json(scopes) FROM identity_providers;
  SELECT count(*), kind FROM auth_tokens GROUP BY kind;
  SELECT rustdesk_id, owner_user_id, datetime(last_seen_at/1000, 'unixepoch') FROM devices ORDER BY last_seen_at DESC LIMIT 10;
  SELECT status, dry_run, source_version, started_at FROM migration_runs;
  ```
- Start `rdpanel serve`, then log in to the panel with a migrated account. Open a RustDesk client that was
  logged in before. It should still show the account and its address book, with no new login.
- OAuth: log in with each provider once.

## Rollback

The legacy side is never modified. To roll back:

1. Stop rustdesk-panel.
2. `docker start rustdesk-api` (the container you stopped in step 1, still on the original `data/`).
3. Restore `rustdeskapi.db` from the backup if anything else touched it.

Logins made on the new server after the cutover do not exist on the old one. Clients that logged in
again since the cutover must do so once more. To retry the import, delete the new `panel-data`
directory (or only `panel.db*`, keeping `secret.key`) and start again at step 2.

## What is imported

| legacy | new | notes |
|---|---|---|
| `versions` | `migration_runs.source_version` | must be 265 |
| `groups` | `user_groups` | ids kept; type 1 → `self`, 2 → `shared` |
| `users` | `users` | ids kept (client JWTs embed them); password hash copied verbatim (bcrypt / legacy md5, rehashed on next login); `is_admin` → role; status 1 → active, else disabled |
| `oauths` | `identity_providers` | ids kept; `op` → slug verbatim; scopes split; client secret encrypted |
| `user_thirds` | `user_identities` | subject = `open_id` verbatim; provider by `op`, then `oauth_type`, then `third_type` |
| `peers` | `devices` | one row per RustDesk ID |
| `address_book_collections` | `address_books` | ids kept; legacy guid `<group>-<user>-<collection>` kept for cached clients |
| (implicit) | `address_books` (personal) | one per user, new ids after the legacy collection sequence |
| `address_book_collection_rules` | `address_book_shares` | rule 1/2/3 → read/read_write/full; duplicates keep the highest |
| `tags` + `address_books.tags` | `address_book_tags` + links | tags only named in entries get colour `0xFF9E9E9E` |
| `address_books` | `address_book_entries` | password/hash encrypted, returned byte-for-byte; `online` dropped |
| `user_tokens` | `auth_tokens` | only unexpired (DB expiry and JWT `exp`); only sha256 stored; no device → `web`, else `client` |
| `login_logs` | `login_events` | account → password, oauth → oidc; `token_id` NULL (legacy stored the user id there) |
| `audit_conns` | `connection_audits` | `guid` = UUIDv5 of the legacy id (stable across re-runs); `session_id` text verbatim |
| `audit_files` | `file_transfer_audits` | invalid JSON info wrapped as `{"raw": ...}` |
| `share_records` | `share_links` | token hashed, password encrypted, expired links skipped |
| `server_cmds` | `server_commands` | target 21115 → hbbs, 21117 → hbbr |

AUTOINCREMENT sequences are raised to the legacy `sqlite_sequence` values, so ids of deleted legacy
users or collections are never handed out again. This matters because old JWTs and cached
address-book guids may still carry them.
