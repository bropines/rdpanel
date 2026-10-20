# RD Panel

**RD Panel — API server and admin panel for RustDesk.** Self-hosted, compatible with
official RustDesk clients, shipped as one container together with hbbs/hbbr.
Formerly `rustdesk-panel`.

> Not affiliated with or endorsed by RustDesk / Purslane Ltd. "RustDesk" is used only to
> describe compatibility.

[![release](https://img.shields.io/github/v/release/bropines/rdpanel?sort=semver)](https://github.com/bropines/rdpanel/releases/latest)
[![ci](https://github.com/bropines/rdpanel/actions/workflows/ci.yml/badge.svg)](https://github.com/bropines/rdpanel/actions/workflows/ci.yml)
[![image](https://github.com/bropines/rdpanel/actions/workflows/image.yml/badge.svg)](https://github.com/bropines/rdpanel/pkgs/container/rdpanel)
[![license](https://img.shields.io/badge/license-MIT%20%2B%20AGPL--3.0%20(hbbs%2Fhbbr)-blue)](#license)

A Go backend + React UI for [RustDesk](https://rustdesk.com), and a drop-in replacement for
[lejianwen/rustdesk-api](https://github.com/lejianwen/rustdesk-api) and the
`lejianwen/rustdesk-server-s6` image:

- official RustDesk clients (1.3–1.5) keep working against the same API URL;
- users, tokens, devices and address books migrate with one command, without re-login.

![Dashboard](docs/screenshots/dashboard-light.jpg)

## Contents

- [Features](#features) · [Screenshots](#screenshots) · [Quick start](#quick-start-docker)
- [Documentation](#documentation) · [Building from source](#building-from-source) ·
  [Commands](#commands) · [HTTP surface](#http-surface) · [Development](#development)
- [Links](#links) · [License](#license)

## Features

- **Client API** (`/api/*`), wire-compatible with RustDesk 1.3–1.5:
  - password and OIDC login, heartbeat/sysinfo;
  - address books (legacy and personal/shared);
  - audit (connections, files, alarms, session notes);
  - deploy and switch-grant, custom-client deployment presets;
  - every endpoint has a contract test; audited against 1.4.6, 1.5.0 and master
    ([compat notes](docs/research/client-compat-1.4.6.md)).
- **Admin panel:**
  - devices with live online status from hbbs (pushed over SSE);
  - users and groups, unified address books with sharing and coloured tags;
  - browser web client: connect to devices from the panel, no install ([rdweb](https://github.com/bropines/rdweb), end-to-end verified and encrypted);
  - connection access control (ACL) enforced by hbbs before a connection starts, with
    control roles that restrict sessions on RustDesk 1.4.5+ devices
    ([how](docs/acl-policy.md#3-enforcement));
  - client MDM strategies with heartbeat push (security presets, group and device overrides);
  - "Install client" with the official clients preconfigured for this server, and opt-in
    public share links (`/s/<token>`) that give anyone the same without an account
    ([share links](docs/share-links.md));
  - audit logs, identity providers, hbbs/hbbr control;
  - English and Russian, light and dark themes, mobile layout.
- **Sign-in & Directory:**
  - password, TOTP 2FA with recovery codes, passkeys (WebAuthn) for the panel;
  - LDAP / Active Directory integration with auto-provisioning, group mapping and periodic background sync;
  - e-mailed sign-in codes (panel and RustDesk client), password reset by e-mail;
  - OIDC (GitHub, Google, any OIDC provider) for both the panel and the RustDesk client;
  - the public client API and the admin panel can live on different hosts (e.g. panel
    only on a VPN/tailnet).
- **Server control & Routing:**
  - relay servers, GeoIP and CIDR prefix routing, dynamic STUN fetch;
  - opt-in, token-authenticated panel federation to see peer panels' relays;
  - always-use-relay, must-login, IP blocker, block/blacklists, bandwidth;
  - opt-in keepalive for TCP/WebSocket clients;
  - a reconciler stores the desired state and re-applies it after hbbs/hbbr restarts.
- **Secure defaults:** hashed tokens, AEAD-encrypted secrets at rest, argon2id passwords,
  PKCE, CSRF-protected cookie sessions, strict CSP, login rate limiting,
  trusted-proxy-only client IPs, no account linking by unverified e-mail.
- **Ops:**
  - single static binary, SQLite (WAL), `/healthz`, Prometheus `/metrics`, JSON logs;
  - multi-arch image (amd64/arm64, ~48 MB, ~15–30 MB RAM for the whole stack);
  - Helm chart.

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

Then:

1. Put a TLS reverse proxy in front of the panel
   ([example](docs/deploy.md#reverse-proxy)).
2. Configure RustDesk clients:

   | client field | value |
   |---|---|
   | ID server | `<host>` |
   | Relay server | `<host>` |
   | API server | `RDPANEL_PUBLIC_URL` |
   | Key | contents of `data/id_ed25519.pub` |

Image: `ghcr.io/bropines/rdpanel` — `:X.Y.Z`, `:X.Y`, `:latest` for releases, `:edge` for
`main`. hbbs/hbbr are built from the `rdpanel` branch of
[bropines/rustdesk-server](https://github.com/bropines/rustdesk-server) (upstream 1.1.16 +
the lejianwen API/login patches + hardening and keepalive fixes).

## Documentation

| guide | for |
|---|---|
| [`docs/deploy.md`](docs/deploy.md) | full Docker guide: reverse proxy, split public/internal hosts, passkeys, e-mail, WebSocket clients, image tags, building the image |
| [`docs/configuration.md`](docs/configuration.md) | every `RDPANEL_*` setting with its YAML key and default |
| [`deploy/helm/rdpanel`](deploy/helm/rdpanel) | Kubernetes (Helm chart) |
| [`docs/api.md`](docs/api.md) | admin API for scripts: personal API tokens, curl examples; interactive docs (Swagger UI) at `/api/v1/docs` on the admin host |
| [`docs/web-client.md`](docs/web-client.md) | browser remote desktop client: proxy requirements, who can use it, security model |
| [`docs/install-client.md`](docs/install-client.md), [`docs/share-links.md`](docs/share-links.md) | preconfigured client downloads; public share links for people without an account |
| [`docs/acl-policy.md`](docs/acl-policy.md) | declarative Access Policy (Tailscale-style HuJSON), roles, test assertions and GitOps workflow |
| [`docs/oidc.md`](docs/oidc.md) | single sign-on: Authentik, Keycloak, Pocket ID, GitHub, Google |
| [`docs/monitoring.md`](docs/monitoring.md) | metrics, Grafana dashboard, alert rules |
| [`docs/relay-agents.md`](docs/relay-agents.md) | extra relays on other hosts (relay agents) |
| [`docs/webrtc.md`](docs/webrtc.md) | WebRTC for RustDesk 1.5+: hbbs signaling, ICE/TURN servers pushed to clients, coturn |
| [`docs/migration.md`](docs/migration.md) | coming from lejianwen/rustdesk-api |
| [`docs/schema.md`](docs/schema.md) | database schema |

**Coming from lejianwen/rustdesk-api?** Keep the same `RUSTDESK_API_JWT_KEY` and hbbs key
pair, then run `rdpanel migrate-legacy --from rustdeskapi.db --dry-run`.

## Building from source

Requirements: Go 1.26+, Node 20.19+ (only to build the UI).

```sh
make web build                      # builds web/dist, then bin/rdpanel with the UI embedded
export RDPANEL_DATA_DIR=./data
export RUSTDESK_API_JWT_KEY='...'   # the same value hbbs uses for MUST_LOGIN
./bin/rdpanel user create --admin admin   # prompts for the password
./bin/rdpanel serve                       # http://localhost:21114
```

Without `make web` the binary still builds and serves a placeholder page instead of the UI.

### UI development

| command | what it does |
|---|---|
| `rdpanel serve` + `cd web && npm run dev` | Vite dev server; proxies `/api` to the backend |
| `npm run dev:mock` | no backend: the whole admin API from an in-memory dataset (Mock Service Worker; sign in as `admin`/`admin`, TOTP `123456`) |
| `npm run build:mock` | mock build to `web/dist-mock` (never embedded) |
| `npm run e2e`, `npm run screenshots` | Playwright against the mock build |

### End-to-end against the real backend

1. `make web build && scripts/e2e-server.sh` starts `bin/rdpanel` on `:31114` with a fresh
   database, an admin and demo data seeded through the client API (`scripts/seed-demo`).
   Add `HBB_BIN=<dir with hbbs, hbbr>` for live hbbs/hbbr.
2. `cd web && npm run e2e:real` runs `web/e2e/real` (set `E2E_HBBS=1` with hbbs).
3. `npm run screenshots:real` refreshes `docs/screenshots/`.

## Commands

| command | purpose |
|---|---|
| `rdpanel serve` | run the HTTP server (graceful shutdown on SIGINT/SIGTERM) |
| `rdpanel user create [--admin] [--password-stdin] [--email E] [--display-name N] <username>` | create a user; password from the terminal prompt or stdin, never logged |
| `rdpanel user reset-password [--password-stdin] <username>` | set a new password and revoke all of the user's sessions/tokens |
| `rdpanel migrate-legacy --from <rustdeskapi.db> [--dry-run] [--report F] [--force]` | import a lejianwen/rustdesk-api v2.7 database (users, passwords, OAuth links, address books, devices, tokens, audit) |
| `rdpanel config print` | effective configuration, secrets redacted |
| `rdpanel version` | build information |

## HTTP surface

| path | description |
|---|---|
| `/api/*` | RustDesk client API |
| `/api/v1/admin/*` | panel JSON API (contract: [`api/openapi.yaml`](api/openapi.yaml)) |
| `/api/v1/events` | Server-Sent Events (presence, audit) |
| `/s/*` | public share link pages (`RDPANEL_SHARE_LINKS_ENABLED`, [share-links.md](docs/share-links.md)) |
| `/healthz` | liveness + DB ping |
| `/metrics` | Prometheus metrics (restricted by `RDPANEL_METRICS_ALLOW`) |
| `/*` | embedded single-page app |

Settings: [`docs/configuration.md`](docs/configuration.md). Metrics, Grafana dashboard,
alert rules, a Prometheus + Grafana compose example and the Helm `PrometheusRule` /
dashboard options: [`docs/monitoring.md`](docs/monitoring.md).

## Development

```sh
make test     # go vet + go test -race + web tests
make lint     # go vet + staticcheck + web typecheck/lint
make gen      # regenerate web/src/api/schema.d.ts from api/openapi.yaml
# the database-backed Go suites on PostgreSQL (the role needs CREATEDB):
RDPANEL_TEST_PG_DSN='postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable' go test ./...
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
