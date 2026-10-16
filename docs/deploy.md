# Deploying rustdesk-panel

The release artifact is one multi-arch container image,
`ghcr.io/bropines/rustdesk-panel` (linux/amd64, linux/arm64), that runs three
services under [s6-overlay](https://github.com/just-containers/s6-overlay) v3:

| Service | Binary | Ports | Source |
|---|---|---|---|
| `hbbs` | ID/rendezvous server | 21115/tcp (NAT test + loopback admin), 21116/tcp+udp, 21118/tcp (WebSocket) | [bropines/rustdesk-server](https://github.com/bropines/rustdesk-server) (AGPL-3.0) |
| `hbbr` | relay server | 21117/tcp (+ loopback admin), 21119/tcp (WebSocket) | same |
| `panel` | `rdpanel serve`: client API + admin UI | 21114/tcp (`RDPANEL_LISTEN`) | this repository (MIT) |

All three share one container because the hbbs/hbbr admin channel only accepts
loopback connections (see `docs/research/hbbs-integration.md` §1.5).

## Image tags

| Tag | Built from |
|---|---|
| `X.Y.Z`, `X.Y`, `latest` | release tag `vX.Y.Z` (pre-release tags such as `v1.0.0-rc.1` only get `X.Y.Z-rc.1`) |
| `edge`, `sha-<short>` | every push to `main` |

Each image carries build provenance and an SBOM (buildx attestations) plus a
GitHub build-provenance attestation (`gh attestation verify oci://ghcr.io/bropines/rustdesk-panel:<tag> -R bropines/rustdesk-panel`).
The exact hbbs/hbbr source commit is in the `io.github.bropines.rustdesk-server.ref`
label and in `/usr/share/doc/rustdesk-server/SOURCE` inside the image.

## Run

```sh
mkdir -p /opt/rustdesk && cd /opt/rustdesk
curl -fsSLO https://raw.githubusercontent.com/bropines/rustdesk-panel/main/deploy/compose.example.yml
curl -fsSL  https://raw.githubusercontent.com/bropines/rustdesk-panel/main/deploy/rdpanel.env.example -o rdpanel.env
mv compose.example.yml compose.yml
chmod 600 rdpanel.env && $EDITOR rdpanel.env      # at least RELAY and RDPANEL_PUBLIC_URL
docker compose up -d
docker compose logs -f                              # lines are prefixed [hbbs] [hbbr] [panel] [key-secret]
docker compose exec rustdesk rdpanel user create --admin admin
```

`deploy/compose.example.yml` uses `network_mode: host` (hbbs needs real client
IPs and UDP), mounts `./data:/data` and binds the panel to `127.0.0.1:21114`.
Put a TLS reverse proxy in front of the panel (below).

Clients are configured with: ID server = `<host>`, relay = `<host>`, API server =
`RDPANEL_PUBLIC_URL`, key = contents of `data/id_ed25519.pub` (also logged at
start-up by `key-secret`).

### What happens on start

1. `key-secret` (oneshot, root):
   - hbbs key pair `/data/id_ed25519{,.pub}`: existing files win, then docker
     secrets `key_priv`/`key_pub`, then env `KEY_PRIV`/`KEY_PUB`, otherwise a new
     pair is generated with `rustdesk-utils genkeypair` (same libsodium ed25519
     format hbbs writes). Both halves or neither, and a provided pair is
     validated. `KEY_PRIV`/`KEY_PUB` (and the secrets) are raw file contents
     and are only written when the file does not exist yet: existing keys are
     never overwritten; if a provided key differs from the existing one a
     warning is logged and the existing key is kept (delete the file to
     replace it). Permissions: private 0600, public 0644. Generating here instead
     of letting hbbs do it avoids hbbs and hbbr racing to create the files on
     first start and avoids a world-readable private key.
   - if `RUSTDESK_API_JWT_KEY` is unset, makes sure `$RDPANEL_DATA_DIR/jwt.key`
     exists; the hbbs run script exports its content as `RUSTDESK_API_JWT_KEY`
     so the panel and hbbs (MUST_LOGIN) always use the same key.
   - chowns `/data` and the panel data dir to `PUID:PGID` (default
     `10001:10001`; skipped when `PUID=0`). Data dirs created by root-run images
     therefore keep working.
   - any failure stops the container (`S6_BEHAVIOUR_IF_STAGE2_FAILS=2`).
2. `hbbs`, `hbbr` (cwd `/data`) and then `panel` (after hbbs and hbbr), each as
   `PUID:PGID` with no supplementary groups. All three are one trust domain (the
   panel has full control of hbbs/hbbr through the loopback admin channel), so
   one unprivileged uid for all of them is enough; none needs root because all
   ports are above 1024.
3. Each service's stdout/stderr goes through an s6-rc pipeline logger that
   prefixes lines with `[hbbs] `, `[hbbr] `, `[panel] ` and writes them to the
   container's stdout. Services restart automatically when they exit.

`docker stop` sends SIGTERM to s6, which stops the panel (graceful HTTP
shutdown, SIGKILL after 7 s) and hbbs/hbbr (SIGKILL after 5 s) and exits within
about 8 s; the compose example sets `stop_grace_period: 20s`.

`docker exec <container> rdpanel ...` (and `kubectl exec`, or the Helm
migration Job, which runs `rdpanel` without s6) uses the wrapper
`/usr/local/bin/rdpanel`: as root it first creates/chowns the data dirs to
`PUID:PGID`, then drops to that user before calling `/usr/bin/rdpanel`, so the
CLI never leaves root-owned files in the data dir. Files the CLI only reads
(e.g. the legacy `rustdeskapi.db`) must be readable by `PUID` (mode 0644 or
owned by it).

Required privileges: the container starts as root (s6) but only needs the
capabilities `CHOWN, DAC_OVERRIDE, FOWNER, SETGID, SETUID, KILL`; it works with
all others dropped and `no-new-privileges` / `allowPrivilegeEscalation: false`
(this is the Helm chart's default securityContext; verified with
`docker run --cap-drop ALL --cap-add ... --security-opt no-new-privileges`).
All ports are above 1024 and no binary has file capabilities. Shutdown
completes in ~3 s (worst case ~10 s with kill timeouts), well inside
Kubernetes' default `terminationGracePeriodSeconds: 30`.

### Health check

`/usr/libexec/rdpanel/healthcheck` (Docker `HEALTHCHECK`, every 15 s) requires:
s6 reports `hbbs`, `hbbr` and `panel` up; TCP ports 21115-21119 are in LISTEN
state (read from `/proc/net/tcp*`, no probe connections; override with
`HEALTHCHECK_PORTS`); `GET /healthz` on `RDPANEL_LISTEN` (which pings the panel
DB) succeeds.

## Environment reference

`deploy/rdpanel.env.example` documents every variable with its default. Summary:

| Variable | Used by | Default (image) | Meaning |
|---|---|---|---|
| `PUID`, `PGID` | all | `10001` | uid/gid the services run as; `0` = root |
| `TZ` | all | UTC | time zone |
| `RELAY` | hbbs | — | `-r`: relay server(s) handed to clients |
| `ENCRYPTED_ONLY` | hbbs, hbbr | `0` | `1` adds `-k _` (only clients with the public key). hbbs requires the key by default anyway; this mainly makes hbbr check it |
| `ALWAYS_USE_RELAY` | hbbs | `N` | `Y` disables P2P |
| `MUST_LOGIN` | hbbs | `N` | `Y` requires a panel login token for connections |
| `TCP_KEEPALIVE` | hbbs | `N` | `Y` keeps TCP/WebSocket-mode clients online (see [WebSocket and TCP-mode clients](#websocket-and-tcp-mode-clients)) |
| `RUSTDESK_API_JWT_KEY` | panel, hbbs | generated `jwt.key` | HS256 key of client tokens (raw string) |
| `KEY_PRIV`, `KEY_PUB` | key-secret | — | initial key pair (also docker secrets `key_priv`/`key_pub`) |
| `LIMIT_SPEED`, `SINGLE_BANDWIDTH`, `TOTAL_BANDWIDTH`, `DOWNGRADE_THRESHOLD`, `DOWNGRADE_START_CHECK` | hbbr | hbbr defaults | relay bandwidth limits |
| `RUST_LOG` | hbbs, hbbr | `info` | log filter |
| `RDPANEL_LISTEN` | panel | `:21114` | bind address (`127.0.0.1:21114` in the env example) |
| `RDPANEL_PUBLIC_URL` | panel | — | external base URL RustDesk clients use as "API server"; the OIDC redirect URI is `<public_url>/api/oidc/callback` |
| `RDPANEL_ADMIN_URL` | panel | = public URL | base URL of the panel (SPA + `/api/v1/*`) when it is served from another host; OIDC panel logins end there, panel cookies are `Secure` when it is https |
| `RDPANEL_DATA_DIR` | panel, key-secret | `/data/panel` | panel.db, secret.key, jwt.key |
| `RDPANEL_DB_PATH`, `RDPANEL_SECRET_KEY`, `RDPANEL_SECRET_KEY_FILE`, `RDPANEL_JWT_KEY_FILE` | panel | derived from data dir | storage and keys |
| `RDPANEL_TOKEN_LIFETIME`, `RDPANEL_JWT_LIFETIME`, `RDPANEL_SESSION_IDLE`, `RDPANEL_SESSION_ABSOLUTE` | panel | `4380h`, = token, `12h`, `168h` | token/session lifetimes |
| `RDPANEL_TRUSTED_PROXIES`, `RDPANEL_REAL_IP_HEADER` | panel | loopback, `X-Forwarded-For` | client-IP resolution |
| `RDPANEL_METRICS_ALLOW` | panel | loopback | who may read `/metrics` |
| `RDPANEL_METRICS_PER_DEVICE` | panel | `false` | per-device presence series (see [monitoring](monitoring.md)) |
| `RDPANEL_HBB_REF` | panel | image `HBB_REF` build arg | hbbs/hbbr source ref shown in `rdpanel_build_info` |
| `RDPANEL_HBBS_ID_SERVER`, `RDPANEL_HBBS_RELAY_SERVER`, `RDPANEL_HBBS_PUBLIC_KEY_FILE` | panel | `127.0.0.1:21116`, `127.0.0.1:21117`, `/data/id_ed25519.pub` | where hbbs/hbbr are |
| `RDPANEL_LOG_LEVEL`, `RDPANEL_LOG_FORMAT`, `RDPANEL_CONFIG` | panel | `info`, `json`, — | logging, optional YAML file |
| `RDPANEL_UPDATE_CHECK` | panel | `true` | About page release check against the GitHub API (see below); `false` = no outbound request |
| `HEALTHCHECK_PORTS` | healthcheck | `21115 21116 21117 21118 21119` | ports that must listen |

Do not set `PORT` (hbbr would listen on `PORT+1`) or `KEY` (overrides the key
file for both daemons) unless you mean it.

### About page and update check

The panel's **About** page (user menu, or the version in the sidebar footer;
every signed-in user) shows the panel version, commit and build date, and the
source of the bundled hbbs/hbbr: the image bakes the `HBB_REPO`/`HBB_REF` build
args into `rdpanel` (see [Building the image](#building-the-image)). Admins
also see uptime, the database schema version, the platform and live hbbs/hbbr
status.

To say whether a newer release exists, `rdpanel` asks
`https://api.github.com/repos/bropines/rustdesk-panel/releases/latest` when
the page is opened, at most once per 6 hours (a failed check is retried after
30 minutes, 5 s timeout; the last good answer is kept). Nothing else depends on
it: without internet access the page just says the check failed. Set
`RDPANEL_UPDATE_CHECK=false` (YAML `update_check: false`) on air-gapped hosts or
if the panel must not make outbound requests.

## Data layout

```
/data/                       PUID:PGID, same layout as the upstream/lejianwen images
  id_ed25519                 hbbs private key (0600)
  id_ed25519.pub             public key given to clients (0644)
  db_v2.sqlite3              hbbs peer table
  blacklist.txt, blocklist.txt   hbbr lists (written by the panel)
  panel/                     RDPANEL_DATA_DIR
    panel.db (+ -wal, -shm)  panel database (SQLite WAL)
    secret.key               AEAD key for secrets at rest (back it up with panel.db!)
    jwt.key                  client token key when RUSTDESK_API_JWT_KEY is unset
```

Back up the whole `/data` (stop the container or use `sqlite3 .backup` for the
databases). Losing `id_ed25519` means every client must be given the new public
key; losing `secret.key` makes stored secrets (address-book passwords, OIDC
client secrets, TOTP seeds) unreadable.

## Reverse proxy

Only the panel (21114) should be behind the HTTP reverse proxy. Example (nginx;
Angie is identical):

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name rustdesk-api.example.com;
    # ssl_certificate ...

    location / {
        proxy_pass http://127.0.0.1:21114;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;   # overwrite, do not append client input
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_buffering off;              # /api/v1/events is Server-Sent Events
        proxy_read_timeout 1h;
    }
}
```

### Split hosts: public client API + internal admin panel

The panel can serve RustDesk clients from a public host while the panel UI and
its API (`/api/v1/*`) are reachable only on an internal host (VPN/tailnet):

```sh
RDPANEL_PUBLIC_URL=https://desk-api.example.com            # clients' "API server"
RDPANEL_ADMIN_URL=https://desk-admin.internal.example.com     # panel (tailnet only)
```

Register only `<public_url>/api/oidc/callback` at GitHub/Google/OIDC providers.
Panel OIDC logins and identity linking start on the admin host (which sets an
HttpOnly binding cookie there), the IdP returns to the callback on the public
host, which exchanges the code and redirects the browser to
`<admin_url>/api/v1/admin/auth/oidc/complete?ticket=…`; that endpoint checks the
binding cookie (constant time), accepts each ticket once and only within 2
minutes, and only then creates the session or links the identity. Nothing
panel-related needs to be reachable on the public host.

```nginx
# Public host: client API only.
server {
    listen 443 ssl;
    http2 on;
    server_name desk-api.example.com;
    # ssl_certificate ...

    location ^~ /api/v1/ { return 404; }               # panel API + SSE: internal only
    location = /api/oidc/callback { include rdpanel-proxy.conf; }
    location /api/ { include rdpanel-proxy.conf; }      # RustDesk client API
    location = /metrics { return 404; }
    location / { return 302 https://desk-admin.internal.example.com$request_uri; }
}

# Admin host (tailnet only): everything.
server {
    listen 100.64.0.10:443 ssl;                         # tailnet address
    http2 on;
    server_name desk-admin.internal.example.com;
    # ssl_certificate ...

    location / { include rdpanel-proxy.conf; }
}

# rdpanel-proxy.conf
#   proxy_pass http://127.0.0.1:31114;
#   proxy_set_header Host $host;
#   proxy_set_header X-Forwarded-For $remote_addr;   # overwrite, never append
#   proxy_set_header X-Forwarded-Proto $scheme;
#   proxy_http_version 1.1;
#   proxy_buffering off;                              # /api/v1/events (SSE)
#   proxy_read_timeout 1h;
```

`/metrics` is answered only when the resolved client IP **and** every address in
any forwarding header (`X-Forwarded-For`, `X-Real-IP`, `Forwarded`, …) are in
`RDPANEL_METRICS_ALLOW`, so a loopback proxy never makes remote scrapers look
local; blocking it on the public vhost (as above) is still recommended.
Metrics, the Grafana dashboard, alert rules and a ready Prometheus + Grafana
compose file are in [monitoring.md](monitoring.md).

- **Trusted proxies.** The panel only honours `RDPANEL_REAL_IP_HEADER` from
  peers in `RDPANEL_TRUSTED_PROXIES` (default: loopback). With host networking
  and a proxy on the same host that is correct. If the proxy is elsewhere, add
  its address, and make sure it overwrites the header (as above): otherwise rate
  limits, audit logs and login backoff see attacker-chosen IPs. Behind
  Cloudflare use `RDPANEL_REAL_IP_HEADER=CF-Connecting-IP` and list Cloudflare's
  ranges (or the local tunnel) as trusted.
- **WebSocket ports 21118/21119** (web client and WS-capable clients) trust
  `X-Real-IP`/`X-Forwarded-For` **unconditionally** (hbbs/hbbr code, not the
  panel). If they are reachable directly, any client can spoof its IP in hbbs/
  hbbr logs, the IP blocker and the relay blocklist. Either do not expose them
  (firewall them; nothing else needs them), or expose them only through a
  reverse proxy that **overwrites** both headers:

  ```nginx
  location /ws/id    { proxy_pass http://127.0.0.1:21118; include ws.conf; }
  location /ws/relay { proxy_pass http://127.0.0.1:21119; include ws.conf; }
  # ws.conf:
  #   proxy_http_version 1.1;
  #   proxy_set_header Upgrade $http_upgrade;
  #   proxy_set_header Connection "upgrade";
  #   proxy_set_header X-Real-IP $remote_addr;
  #   proxy_set_header X-Forwarded-For $remote_addr;
  #   proxy_read_timeout 1h;
  ```
- **Never** forward anything (stream/L4 proxies, ssh `-L`, frp, cloudflared) to
  `127.0.0.1:21115` or `127.0.0.1:21117`: the hbbs/hbbr admin channel has no
  authentication besides "connection comes from loopback". With
  `network_mode: host`, every process on the host can use it.

### WebSocket and TCP-mode clients

RustDesk clients normally register with hbbs over UDP every few seconds. Some
use a long-lived TCP or WebSocket rendezvous connection instead: the "Use
WebSocket" option (RustDesk 1.4+), the web client, clients behind a proxy or
with UDP blocked. Such a client registers once and afterwards only echoes the
empty frames the server sends. Upstream hbbs never sends any, so the client
times out about every 90 s, reconnects and re-registers, and hbbs (and the
panel) show it online for only 30 s after each registration: in practice the
device flickers online about a third of the time.

The hbbs in this image (bropines/rustdesk-server) has an opt-in fix:
`TCP_KEEPALIVE=Y` (or `--tcp-keepalive=Y`, or the admin command
`tcp-keepalive Y|N`) makes hbbs send an empty frame every 12 s on every
registered TCP/WebSocket rendezvous connection. The client echoes it, which
also refreshes its registration, so it stays online continuously. The
**Keepalive for TCP/WebSocket clients** toggle on the panel's Server page sets
the same flag at runtime and re-applies it after an hbbs restart (it then
overrides the env value).

- **When to enable:** you have (or expect) clients with "Use WebSocket"
  enabled, behind HTTP proxies, or on networks that block UDP, and they show
  up as offline or flapping. Leave it `N` if all your clients use UDP: nothing
  changes for them either way.
- **Cost:** one empty frame per connection every 12 s (a few bytes; the timer
  runs only for registered TCP/WebSocket connections).
- **Trade-off of WebSocket mode itself** (not of the keepalive): WebSocket-mode
  clients always connect through the relay (hbbr, port 21119 or your `/ws/relay`
  proxy); there is no P2P hole punching, so all their traffic goes through
  your server. Size `TOTAL_BANDWIDTH`/`SINGLE_BANDWIDTH` accordingly.
- A reverse proxy in front of 21118 must not time out idle WebSocket
  connections faster than ~30 s (`proxy_read_timeout 1h` above is fine).
- Upstream hbbs does not know the option; with an upstream binary the panel
  shows the setting as unknown and reports an apply error if it is managed.

## Upgrading from the lejianwen s6 image (`lejianwen/rustdesk-server-s6` + `rustdesk-api`)

The hbbs/hbbr data layout is the same (`/data/id_ed25519{,.pub}`,
`db_v2.sqlite3`), so clients keep working with their configured key.

1. Note the old container's env: `RELAY`, `ENCRYPTED_ONLY`, `MUST_LOGIN`,
   `ALWAYS_USE_RELAY` and especially `RUSTDESK_API_JWT_KEY` (or `jwt.key` in the
   old API config). Put the same values in `rdpanel.env`; the same JWT key keeps
   already-issued client tokens valid.
2. Stop the old container. Copy the old API database (`/app/data/rustdeskapi.db`
   in the old container) somewhere readable, e.g. `./rustdeskapi.db` (mode 0644
   or owned by `PUID`: the CLI runs as `PUID`).
3. Mount the old hbbs data dir as `/data` (and optionally a new dir as
   `/data/panel`), see the upgrade example at the end of `deploy/compose.example.yml`. On first
   start `key-secret` validates the existing key pair and chowns the files to
   `PUID:PGID` (the old image ran as root).
4. Start the container, then import once, **before** creating users in the new
   panel (the import refuses a non-empty target DB):

   ```sh
   docker compose exec rustdesk rdpanel migrate-legacy --from /import/rustdeskapi.db --dry-run --report /data/panel/migrate-report.json
   docker compose exec rustdesk rdpanel migrate-legacy --from /import/rustdeskapi.db
   ```
5. Point the reverse proxy at the new `RDPANEL_LISTEN` (e.g.
   `127.0.0.1:31114`, already the Angie upstream of `desk-api.example.com`).
6. Rollback: stop the new container and start the old one on the same hbbs data
   dir; it only needs root to own the files again (`chown -R 0:0 hbbs-data`).

## Building the image

### CI

`.github/workflows/image.yml` (tags `v*`, pushes to `main`, manual dispatch):

1. `hbb` job (matrix amd64/arm64): checks out `bropines/rustdesk-server` at
   `server_ref` (default `merge-upstream-1.1.16`) with submodules, builds
   `hbbs`, `hbbr`, `rustdesk-utils` with `cross` for
   `{x86_64,aarch64}-unknown-linux-musl` (static), cargo cache via
   `Swatinem/rust-cache`. The server repo must keep its committed `.env`
   (`DATABASE_URL=sqlite://./db_v2.sqlite3`) and `db_v2.sqlite3`: `sqlx::query!`
   checks queries against that database at compile time.
2. `image` job: puts the binaries into `deploy/bin/<arch>/`, runs one buildx
   multi-platform build of `deploy/Dockerfile`, pushes to GHCR with
   `GITHUB_TOKEN` (`packages: write`) only, with provenance (`mode=max`) and
   SBOM attestations and OCI labels/annotations.

### Locally (`make image`)

Requirements: Docker with buildx, a checkout of the server fork with
submodules (`HBB_SRC`, default `~/build/rustdesk-server`).

```sh
make image                                  # -> rustdesk-panel:local for the host arch
make image HBB_SRC=/path/to/rustdesk-server IMAGE=rustdesk-panel:test
make hbb                                    # only (re)build deploy/bin/<arch>/hbb*
```

`make hbb` (`deploy/scripts/build-hbb.sh`) compiles the static musl binaries in
a `rust:1-alpine` container, with cargo caches in the docker volumes
`rdpanel-hbb-registry`, `rdpanel-hbb-git` and `rdpanel-hbb-target` (delete them
to reclaim space). A glibc build (`cargo build` on Debian) does **not** work in
the Alpine image. `make image` only runs `make hbb` when `deploy/bin/<arch>/`
is empty; run `make hbb` yourself after changing the server sources.

### Dockerfile design

- Stages: `web` (node 22, `npm ci && npm run build`), `panel` (newest stable
  Go image, `CGO_ENABLED=0`, `GOTOOLCHAIN=local`, cross-compiled with
  `GOOS/GOARCH` from the build platform; the code stays go1.23-compatible),
  `s6` (downloads s6-overlay, verifies pinned SHA-256 sums), final
  `alpine` with `ca-certificates` and `tzdata`.
- hbbs/hbbr come prebuilt from `deploy/bin/<TARGETARCH>/` in the build context
  rather than being compiled in the Dockerfile: the Rust LTO build takes
  minutes per arch and would run under QEMU for the foreign arch; CI compiles it
  natively with `cross` and caches it, and the local path reuses a cached
  build. Only trivial `RUN` steps execute on the target platform.
- `HBB_REPO`/`HBB_REF` (the server fork and the commit the binaries were built
  from) are written to `/usr/share/doc/rustdesk-server/SOURCE` (AGPL §6) and
  linked into `rdpanel` (`internal/buildinfo.HBBRepo/HBBRef` ldflags) for the
  About page and `rdpanel version`.
- Bump `ALPINE_VERSION`, `GO_IMAGE`, `NODE_IMAGE`, `S6_OVERLAY_VERSION` (+ the
  three `S6_SHA256_*` values from the release's `.sha256` files) as build args
  or in the Dockerfile.
