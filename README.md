# rustdesk-panel

A self-hosted **API server + admin panel for [RustDesk](https://rustdesk.com)**, written in
Go with a React UI. It is a drop-in replacement for
[lejianwen/rustdesk-api](https://github.com/lejianwen/rustdesk-api): official RustDesk
clients (1.3.x–1.4.x) keep working against the same API URL, and existing users, tokens,
devices and address books can be migrated without re-login.

> **Status: early development.** The foundation (config, database schema, crypto, auth
> primitives, HTTP server, web scaffold) is in place; the RustDesk client API, the admin
> API, hbbs integration and the legacy importer are being built. See [`SPEC.md`](SPEC.md).

## Highlights (target)

- Wire-compatible client API (`/api/*`): login (password + OIDC), address books, heartbeat,
  sysinfo, audit — with the legacy security holes closed.
- Admin panel: devices with live presence from hbbs, users and groups, unified address books
  with sharing, audit logs, OIDC providers, hbbs/hbbr server control, 2FA (TOTP).
- Single static binary (`CGO_ENABLED=0`), SQLite (WAL), embedded UI, `/healthz`, Prometheus
  `/metrics`, structured JSON logs.
- Secure defaults: hashed tokens, AEAD-encrypted secrets at rest, argon2id passwords,
  CSRF-protected cookie sessions, strict CSP, trusted-proxy-only client IPs.

## Quick start

Requirements: Go 1.23+, Node 20.19+ (only to build the UI).

```sh
make web build                      # builds web/dist, then bin/rdpanel with the UI embedded
export RDPANEL_DATA_DIR=./data
export RUSTDESK_API_JWT_KEY='...'   # the same value hbbs uses for MUST_LOGIN
./bin/rdpanel user create --admin admin   # prompts for the password
./bin/rdpanel serve                       # http://localhost:21114
```

Without `make web` the binary still builds and serves a placeholder page instead of the UI.
For UI development run `rdpanel serve` and `cd web && npm run dev` (Vite proxies `/api`).
Without a backend, `npm run dev:mock` serves the whole admin API from an in-memory dataset
(Mock Service Worker; sign in as `admin`/`admin`, TOTP `123456`). `npm run build:mock` writes
a mock build to `web/dist-mock` (never embedded); `npm run e2e` and `npm run screenshots`
drive it with Playwright (`docs/screenshots/`).

### Commands

| command | purpose |
|---|---|
| `rdpanel serve` | run the HTTP server (graceful shutdown on SIGINT/SIGTERM) |
| `rdpanel user create [--admin] [--password-stdin] [--email E] [--display-name N] <username>` | create a user; password from the terminal prompt or stdin, never logged |
| `rdpanel user reset-password [--password-stdin] <username>` | set a new password and revoke all of the user's sessions/tokens |
| `rdpanel config print` | effective configuration, secrets redacted |
| `rdpanel version` | build information |

## Configuration

Environment variables override the optional YAML file named by `RDPANEL_CONFIG`
(keys in parentheses), which overrides the defaults.

| variable (yaml key) | default | description |
|---|---|---|
| `RDPANEL_CONFIG` | — | path to a YAML config file |
| `RDPANEL_LISTEN` (`listen`) | `:21114` | HTTP bind address (production: `127.0.0.1:31114` behind a reverse proxy) |
| `RDPANEL_PUBLIC_URL` (`public_url`) | — | external base URL, e.g. `https://desk.example.com`; used for OIDC callbacks, enables HSTS when https |
| `RDPANEL_DATA_DIR` (`data_dir`) | `/data` | data directory |
| `RDPANEL_DB_PATH` (`db_path`) | `<data>/panel.db` | SQLite database |
| `RDPANEL_SECRET_KEY` (`secret_key`) | — | base64 32-byte key for secrets at rest; otherwise read/generated in the key file |
| `RDPANEL_SECRET_KEY_FILE` (`secret_key_file`) | `<data>/secret.key` | generated with mode 0600 on first start — **back it up** |
| `RUSTDESK_API_JWT_KEY` (`auth.jwt_key`) | — | HS256 key for client tokens, used verbatim; must equal hbbs' key and the legacy rustdesk-api key |
| `RDPANEL_JWT_KEY_FILE` (`auth.jwt_key_file`) | `<data>/jwt.key` | generated key used when `RUSTDESK_API_JWT_KEY` is unset |
| `RDPANEL_TOKEN_LIFETIME` (`auth.token_lifetime`) | `4380h` | sliding lifetime of client tokens |
| `RDPANEL_JWT_LIFETIME` (`auth.jwt_lifetime`) | = token lifetime | fixed JWT `exp` (what hbbs enforces; hbbs cannot see revocation) |
| `RDPANEL_SESSION_IDLE` (`auth.session_idle`) | `12h` | panel session idle timeout |
| `RDPANEL_SESSION_ABSOLUTE` (`auth.session_absolute`) | `168h` | panel session absolute lifetime |
| `RDPANEL_TRUSTED_PROXIES` (`http.trusted_proxies`) | `127.0.0.0/8,::1/128` | IPs/CIDRs whose forwarding headers are trusted |
| `RDPANEL_REAL_IP_HEADER` (`http.real_ip_header`) | `X-Forwarded-For` | or `X-Real-IP`, `CF-Connecting-IP` |
| `RDPANEL_METRICS_ALLOW` (`http.metrics_allow`) | `127.0.0.0/8,::1/128` | client IPs allowed to read `/metrics` |
| `RDPANEL_HBBS_ID_SERVER` (`hbbs.id_server`) | `127.0.0.1:21116` | hbbs rendezvous address (admin channel = port − 1) |
| `RDPANEL_HBBS_RELAY_SERVER` (`hbbs.relay_server`) | `127.0.0.1:21117` | hbbr address (admin channel on the same port) |
| `RDPANEL_HBBS_PUBLIC_KEY_FILE` (`hbbs.public_key_file`) | `<data>/id_ed25519.pub` | hbbs public key served to clients |
| `RDPANEL_HBBS_ENABLED` (`hbbs.enabled`) | `true` | presence poller + hbbs/hbbr reconciler workers |
| `RDPANEL_HBBS_POLL_INTERVAL` (`hbbs.poll_interval`) | `5s` | presence poll period (batched `OnlineRequest`) |
| `RDPANEL_HBBS_RECONCILE_INTERVAL` (`hbbs.reconcile_interval`) | `15s` | admin-channel probe / drift-correction period |
| `RDPANEL_HBBS_LIST_DIR` (`hbbs.list_dir`) | empty | hbbr working dir; when set, `blacklist.txt`/`blocklist.txt` are kept in sync there (`/data` in the s6 image) |
| `RDPANEL_LOG_LEVEL` (`log.level`) | `info` | `debug`, `info`, `warn`, `error` |
| `RDPANEL_LOG_FORMAT` (`log.format`) | `json` | `json` or `text` |

Example YAML:

```yaml
listen: 127.0.0.1:31114
public_url: https://desk.example.com
data_dir: /data
auth:
  token_lifetime: 4380h
http:
  trusted_proxies: [127.0.0.1/32]
  real_ip_header: X-Real-IP
log:
  level: info
```

## HTTP surface

| path | description |
|---|---|
| `/api/*` | RustDesk client API |
| `/api/v1/admin/*` | panel JSON API (contract: [`api/openapi.yaml`](api/openapi.yaml)) |
| `/api/v1/events` | Server-Sent Events (presence, audit) |
| `/healthz` | liveness + DB ping |
| `/metrics` | Prometheus metrics (restricted by `RDPANEL_METRICS_ALLOW`) |
| `/*` | embedded single-page app |

## Development

```sh
make test     # go vet + go test -race + web tests
make lint     # go vet + staticcheck + web typecheck/lint
make gen      # regenerate web/src/api/schema.d.ts from api/openapi.yaml
```

Layout: `cmd/rdpanel` (entry point), `internal/{app,config,db,store,auth,crypto,httpx,web,...}`,
`web/` (React + Vite), `api/openapi.yaml`, `docs/` (schema and research notes).
The database schema is documented in [`docs/schema.md`](docs/schema.md).

## License

MIT — see [LICENSE](LICENSE) and [NOTICE](NOTICE). Not affiliated with RustDesk.
