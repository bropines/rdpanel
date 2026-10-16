# RD Panel

**RD Panel — API server and admin panel for RustDesk.** Compatible with official RustDesk
clients. Formerly `rustdesk-panel`.

> Not affiliated with or endorsed by RustDesk / Purslane Ltd. "RustDesk" is used only to
> describe compatibility.

[![release](https://img.shields.io/github/v/release/bropines/rdpanel?sort=semver)](https://github.com/bropines/rdpanel/releases/latest)
[![ci](https://github.com/bropines/rdpanel/actions/workflows/ci.yml/badge.svg)](https://github.com/bropines/rdpanel/actions/workflows/ci.yml)
[![image](https://github.com/bropines/rdpanel/actions/workflows/image.yml/badge.svg)](https://github.com/bropines/rdpanel/pkgs/container/rdpanel)
[![license](https://img.shields.io/badge/license-MIT%20%2B%20AGPL--3.0%20(hbbs%2Fhbbr)-blue)](#license)

A self-hosted **API server + admin panel for [RustDesk](https://rustdesk.com)**: Go backend,
React UI, shipped as **one container** together with hbbs/hbbr. It is a drop-in replacement
for [lejianwen/rustdesk-api](https://github.com/lejianwen/rustdesk-api) and the
`lejianwen/rustdesk-server-s6` image: official RustDesk clients (1.3–1.5) keep working
against the same API URL, and users, tokens, devices and address books migrate with one
command, without re-login.

![Dashboard](docs/screenshots/dashboard-light.jpg)

## Features

- **Client API** (`/api/*`), wire-compatible with RustDesk 1.3–1.5: password and OIDC login,
  address books (legacy and personal/shared), heartbeat/sysinfo, audit (connections, files,
  alarms, session notes), deploy and switch-grant, custom-client deployment presets. Every
  endpoint has a contract test; audited against 1.4.6, 1.5.0 and master
  ([compat notes](docs/research/client-compat-1.4.6.md)).
- **Admin panel:** devices with live online status from hbbs (pushed over SSE), users and
  groups, unified address books with sharing and coloured tags, audit logs, identity
  providers, hbbs/hbbr control. English and Russian, light and dark themes, mobile layout.
- **Sign-in:** password, TOTP 2FA with recovery codes, e-mailed sign-in codes (panel and
  RustDesk client), password reset by e-mail, passkeys (WebAuthn) for the panel,
  OIDC (GitHub, Google, any OIDC provider) for both the panel and the RustDesk client. The public client API and the
  admin panel can live on different hosts (e.g. panel only on a VPN/tailnet).
- **Server control:** relay servers, always-use-relay, must-login, opt-in keepalive for
  TCP/WebSocket clients, IP blocker, block/blacklists, bandwidth. A reconciler stores the
  desired state and re-applies it after hbbs/hbbr restarts.
- **Secure defaults:** hashed tokens, AEAD-encrypted secrets at rest, argon2id passwords,
  PKCE, CSRF-protected cookie sessions, strict CSP, login rate limiting,
  trusted-proxy-only client IPs, no account linking by unverified e-mail.
- **Ops:** single static binary, SQLite (WAL), `/healthz`, Prometheus `/metrics`, JSON
  logs, multi-arch image (amd64/arm64, ~48 MB, ~15–30 MB RAM for the whole stack), Helm
  chart.

## Screenshots

| | |
|---|---|
| ![Devices](docs/screenshots/devices-light.jpg) | ![Devices, dark](docs/screenshots/devices-dark.jpg) |
| Devices with live presence | Dark theme |
| ![Device details](docs/screenshots/device-drawer.jpg) | ![Address books](docs/screenshots/address-books.jpg) |
| Device details | Address books with sharing and tags |
| ![Users](docs/screenshots/users.jpg) | ![Groups](docs/screenshots/groups.jpg) |
| Users | Groups |
| ![Audit](docs/screenshots/audit.jpg) | ![Logins](docs/screenshots/audit-logins.jpg) |
| Connection audit | Login audit |
| ![Server](docs/screenshots/server.jpg) | ![Server settings](docs/screenshots/settings.jpg) |
| hbbs/hbbr status and control | Panel settings |
| ![Identity providers](docs/screenshots/identity-providers.jpg) | ![Profile](docs/screenshots/profile.jpg) |
| OIDC identity providers | Profile, 2FA |
| ![Login](docs/screenshots/login.jpg) | ![Mobile](docs/screenshots/dashboard-mobile.jpg) |
| Sign-in | Mobile layout |

<details>
<summary>More</summary>

![Server console](docs/screenshots/server-console.jpg)
![My devices](docs/screenshots/my-devices.jpg)
![My sessions](docs/screenshots/my-sessions.jpg)
![User details](docs/screenshots/user-drawer.jpg)
![Russian UI](docs/screenshots/devices-ru.jpg)

</details>

## Quick start (Docker)

```sh
mkdir -p /opt/rustdesk && cd /opt/rustdesk
curl -fsSL https://raw.githubusercontent.com/bropines/rdpanel/main/deploy/compose.example.yml -o compose.yml
curl -fsSL https://raw.githubusercontent.com/bropines/rdpanel/main/deploy/rdpanel.env.example -o rdpanel.env
chmod 600 rdpanel.env && $EDITOR rdpanel.env        # at least RELAY and RDPANEL_PUBLIC_URL
docker compose up -d
docker compose exec rustdesk rdpanel user create --admin admin
```

Put a TLS reverse proxy in front of the panel, then configure clients with ID server =
`<host>`, relay = `<host>`, API server = `RDPANEL_PUBLIC_URL`, key = `data/id_ed25519.pub`.

- **Full guide:** [`docs/deploy.md`](docs/deploy.md) (reverse proxy, split public/internal
  hosts, WebSocket clients, image tags, building).
- **Kubernetes:** Helm chart in [`deploy/helm/rdpanel`](deploy/helm/rdpanel).
- **Single sign-on:** [`docs/oidc.md`](docs/oidc.md) (Authentik, Keycloak, Pocket ID,
  GitHub, Google). **Monitoring:** [`docs/monitoring.md`](docs/monitoring.md).
- **Extra relays on other hosts (relay agents):** [`docs/relay-agents.md`](docs/relay-agents.md).
- **Coming from lejianwen/rustdesk-api:** [`docs/migration.md`](docs/migration.md). Keep the
  same `RUSTDESK_API_JWT_KEY` and hbbs key pair, then
  `rdpanel migrate-legacy --from rustdeskapi.db --dry-run`.

Image: `ghcr.io/bropines/rdpanel` — `:X.Y.Z`, `:X.Y`, `:latest` for releases,
`:edge` for `main`. hbbs/hbbr are built from
[bropines/rustdesk-server](https://github.com/bropines/rustdesk-server) (upstream 1.1.16 +
the lejianwen API/login patches + hardening and keepalive fixes).

## Building from source

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
drive it with Playwright.

End-to-end against the real backend: `make web build && scripts/e2e-server.sh` starts
`bin/rdpanel` on `:31114` with a fresh database, an admin and demo data seeded through the
client API (`scripts/seed-demo`; add `HBB_BIN=<dir with hbbs, hbbr>` for live hbbs/hbbr),
then `cd web && npm run e2e:real` (set `E2E_HBBS=1` with hbbs) runs `web/e2e/real`, and
`npm run screenshots:real` refreshes `docs/screenshots/`.

### Commands

| command | purpose |
|---|---|
| `rdpanel serve` | run the HTTP server (graceful shutdown on SIGINT/SIGTERM) |
| `rdpanel user create [--admin] [--password-stdin] [--email E] [--display-name N] <username>` | create a user; password from the terminal prompt or stdin, never logged |
| `rdpanel user reset-password [--password-stdin] <username>` | set a new password and revoke all of the user's sessions/tokens |
| `rdpanel migrate-legacy --from <rustdeskapi.db> [--dry-run] [--report F] [--force]` | import a lejianwen/rustdesk-api v2.7 database (users, passwords, OAuth links, address books, devices, tokens, audit) |
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
| `RDPANEL_ADMIN_URL` (`admin_url`) | = public URL | base URL of the panel (SPA + `/api/v1/*`) when it is served from another (e.g. internal) host |
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
| `RDPANEL_WEBAUTHN_RP_ID` (`webauthn.rp_id`) | host of the admin URL | passkey relying party id: the panel host or a parent domain of every origin (passkeys are bound to it — changing it invalidates registered passkeys) |
| `RDPANEL_WEBAUTHN_ORIGINS` (`webauthn.origins`) | admin URL origin (+ public URL origin when within the RP id) | comma-separated origins (`scheme://host[:port]`) the panel is opened from; passkeys are off when neither this nor a URL is set, or the host is an IP address |
| `RDPANEL_TRUSTED_PROXIES` (`http.trusted_proxies`) | `127.0.0.0/8,::1/128` | IPs/CIDRs whose forwarding headers are trusted |
| `RDPANEL_REAL_IP_HEADER` (`http.real_ip_header`) | `X-Forwarded-For` | or `X-Real-IP`, `CF-Connecting-IP` |
| `RDPANEL_METRICS_ALLOW` (`http.metrics_allow`) | `127.0.0.0/8,::1/128` | client IPs allowed to read `/metrics` |
| `RDPANEL_METRICS_PER_DEVICE` (`http.metrics_per_device`) | `false` | also export `rdpanel_presence_device_online{id}`, one series per device (cardinality = fleet size) |
| `RDPANEL_HBB_REF` | — (set by the image) | hbbs/hbbr source ref reported as `hbb_ref` in `rdpanel_build_info` |
| `RDPANEL_HBBS_ID_SERVER` (`hbbs.id_server`) | `127.0.0.1:21116` | hbbs rendezvous address (admin channel = port − 1) |
| `RDPANEL_HBBS_RELAY_SERVER` (`hbbs.relay_server`) | `127.0.0.1:21117` | hbbr address (admin channel on the same port) |
| `RDPANEL_HBBS_RELAY_PUBLIC_HOST` (`hbbs.relay_public_host`) | first entry of `RELAY` | public `host[:port]` of the co-located hbbr; seeds the local relay of Server → Relays ([relay agents](docs/relay-agents.md)) |
| `RDPANEL_HBBS_PUBLIC_KEY_FILE` (`hbbs.public_key_file`) | `<data>/id_ed25519.pub` | hbbs public key served to clients |
| `RDPANEL_HBBS_ENABLED` (`hbbs.enabled`) | `true` | presence poller + hbbs/hbbr reconciler workers |
| `RDPANEL_HBBS_POLL_INTERVAL` (`hbbs.poll_interval`) | `5s` | presence poll period (batched `OnlineRequest`) |
| `RDPANEL_HBBS_RECONCILE_INTERVAL` (`hbbs.reconcile_interval`) | `15s` | admin-channel probe / drift-correction period |
| `RDPANEL_HBBS_LIST_DIR` (`hbbs.list_dir`) | empty | hbbr working dir; when set, `blacklist.txt`/`blocklist.txt` are kept in sync there (`/data` in the s6 image) |
| `RDPANEL_LOG_LEVEL` (`log.level`) | `info` | `debug`, `info`, `warn`, `error` |
| `RDPANEL_LOG_FORMAT` (`log.format`) | `json` | `json` or `text` |
| `RDPANEL_UPDATE_CHECK` (`update_check`) | `true` | About page "update available" check: the server asks the GitHub releases API at most every 6 h (failures tolerated); `false` = no outbound request |
| `RDPANEL_SMTP_HOST` (`smtp.host`) | empty | outgoing mail server; empty = e-mail is configured in Settings → E-mail (or off). When set, the whole `smtp.*` block comes from the configuration and is read-only in the panel |
| `RDPANEL_SMTP_PORT` (`smtp.port`) | 587 / 465 / 25 | default follows the encryption |
| `RDPANEL_SMTP_ENCRYPTION` (`smtp.encryption`) | `starttls` | `starttls`, `tls` (implicit TLS, SMTPS) or `none` |
| `RDPANEL_SMTP_USERNAME`, `RDPANEL_SMTP_PASSWORD` (`smtp.username`, `smtp.password`) | empty | AUTH PLAIN or LOGIN; `RDPANEL_SMTP_PASSWORD_FILE` (`smtp.password_file`) reads the password from a file |
| `RDPANEL_SMTP_FROM` (`smtp.from`) | — | sender, e.g. `RD Panel <panel@example.com>` (required with a host) |
| `RDPANEL_SMTP_INSECURE_SKIP_VERIFY` (`smtp.insecure_skip_verify`) | `false` | accept any TLS certificate (self-signed relays only) |

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

Monitoring: the metric list, a Grafana dashboard, Prometheus alert rules, a
Prometheus + Grafana compose example and the Helm `PrometheusRule` / dashboard
options are described in [`docs/monitoring.md`](docs/monitoring.md).

## Development

```sh
make test     # go vet + go test -race + web tests
make lint     # go vet + staticcheck + web typecheck/lint
make gen      # regenerate web/src/api/schema.d.ts from api/openapi.yaml
```

Layout: `cmd/rdpanel` (entry point), `internal/{app,config,db,store,auth,crypto,httpx,web,...}`,
`web/` (React + Vite), `api/openapi.yaml`, `docs/` (schema and research notes).
The database schema is documented in [`docs/schema.md`](docs/schema.md).

## Links

- Releases: <https://github.com/bropines/rdpanel/releases>
- Container image: <https://github.com/bropines/rdpanel/pkgs/container/rdpanel>
- Issues: <https://github.com/bropines/rdpanel/issues>
- hbbs/hbbr fork: <https://github.com/bropines/rustdesk-server>
- RustDesk: <https://github.com/rustdesk/rustdesk>, <https://github.com/rustdesk/rustdesk-server>
- Prior art: [lejianwen/rustdesk-api](https://github.com/lejianwen/rustdesk-api),
  [lejianwen/rustdesk-server](https://github.com/lejianwen/rustdesk-server)

## License

The panel is MIT — see [LICENSE](LICENSE) and [NOTICE](NOTICE). The hbbs/hbbr binaries
bundled in the container image are AGPL-3.0 (source:
[bropines/rustdesk-server](https://github.com/bropines/rustdesk-server)). RD Panel is an
independent project, not affiliated with or endorsed by RustDesk / Purslane Ltd.
