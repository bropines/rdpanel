# Deploying RD Panel

How to run RD Panel with Docker: the all-in-one image (hbbs + hbbr + panel), reverse
proxy, optional features, upgrades and building the image yourself. For operators of a
single host. Kubernetes: see the [Helm chart](../deploy/helm/rdpanel/README.md); every
`RDPANEL_*` setting: [configuration.md](configuration.md).

## Contents

- [Quick start](#quick-start)
- [What runs in the container](#what-runs-in-the-container)
- [Reverse proxy](#reverse-proxy) — [single host](#single-host),
  [split hosts](#split-hosts-public-client-api--internal-admin-panel),
  [security checklist](#security-checklist-proxies-and-ports)
- [Environment reference](#environment-reference)
- [Data layout and backups](#data-layout-and-backups)
- Optional features: [passkeys](#passkeys), [e-mail](#e-mail-verification-codes-and-password-reset),
  [WebSocket and TCP-mode clients](#websocket-and-tcp-mode-clients),
  [About page and update check](#about-page-and-update-check), [server logs](#server-logs)
- [Image tags and upgrades](#image-tags-and-upgrades) —
  [from the lejianwen s6 image](#upgrading-from-the-lejianwen-s6-image-lejianwenrustdesk-server-s6--rustdesk-api)
- [Container internals](#container-internals)
- [Building the image](#building-the-image)

## Quick start

1. Download the compose file and the env file:

   ```sh
   mkdir -p /opt/rustdesk && cd /opt/rustdesk
   curl -fsSL https://raw.githubusercontent.com/bropines/rdpanel/main/deploy/compose.example.yml -o compose.yml
   curl -fsSL https://raw.githubusercontent.com/bropines/rdpanel/main/deploy/rdpanel.env.example -o rdpanel.env
   ```

2. Edit `rdpanel.env` — at least `RELAY` and `RDPANEL_PUBLIC_URL`. It holds secrets, so
   keep it private:

   ```sh
   chmod 600 rdpanel.env && $EDITOR rdpanel.env
   ```

3. Start the stack and watch the logs (lines are prefixed `[hbbs]`, `[hbbr]`, `[panel]`,
   `[key-secret]`):

   ```sh
   docker compose up -d
   docker compose logs -f
   ```

4. Create the first admin:

   ```sh
   docker compose exec rustdesk rdpanel user create --admin admin
   ```

5. Put a TLS [reverse proxy](#reverse-proxy) in front of the panel.

6. Configure RustDesk clients:

   | client field | value |
   |---|---|
   | ID server | `<host>` |
   | Relay server | `<host>` |
   | API server | `RDPANEL_PUBLIC_URL` |
   | Key | contents of `data/id_ed25519.pub` (also logged at start-up by `key-secret`) |

What the compose example does:

- `network_mode: host` — hbbs needs real client IPs and UDP;
- mounts `./data:/data`;
- binds the panel to `127.0.0.1:21114`.

## What runs in the container

The release artifact is one multi-arch image, `ghcr.io/bropines/rdpanel` (linux/amd64,
linux/arm64). It runs three services under
[s6-overlay](https://github.com/just-containers/s6-overlay) v3:

| Service | Binary | Ports | Source |
|---|---|---|---|
| `hbbs` | ID/rendezvous server | 21115/tcp (NAT test + loopback admin), 21116/tcp+udp, 21118/tcp (WebSocket) | [bropines/rustdesk-server](https://github.com/bropines/rustdesk-server), branch `rdpanel` (AGPL-3.0) |
| `hbbr` | relay server | 21117/tcp (+ loopback admin), 21119/tcp (WebSocket) | same |
| `panel` | `rdpanel serve`: client API + admin UI | 21114/tcp (`RDPANEL_LISTEN`) | this repository (MIT) |

All three share one container because the hbbs/hbbr admin channel only accepts loopback
connections (see `docs/research/hbbs-integration.md` §1.5). Start-up, shutdown,
privileges and the health check are described under
[Container internals](#container-internals).

## Reverse proxy

Only the panel (21114) belongs behind the HTTP reverse proxy. The examples are nginx;
Angie is identical.

### Single host

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name desk-api.example.com;
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

RustDesk clients use a public host, while the panel UI and its API (`/api/v1/*`) are
reachable only on an internal host (VPN/tailnet):

```sh
RDPANEL_PUBLIC_URL=https://desk-api.example.com               # clients' "API server"
RDPANEL_ADMIN_URL=https://desk-admin.internal.example.com     # panel (tailnet only)
```

Register only `<public_url>/api/oidc/callback` at GitHub/Google/OIDC providers
(step-by-step, including Authentik: [`oidc.md`](oidc.md)). Nothing panel-related needs to
be reachable on the public host.

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
    # Relay agents: /agent/v1/ (WebSocket) and /agent/install.sh, see relay-agents.md.
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

<details>
<summary>How the OIDC hand-off between the hosts works</summary>

1. Panel OIDC logins and identity linking start on the admin host, which sets an
   HttpOnly binding cookie there.
2. The IdP returns to the callback on the public host. It exchanges the code and
   redirects the browser to `<admin_url>/api/v1/admin/auth/oidc/complete?ticket=…`.
3. That endpoint checks the binding cookie (constant time) and accepts each ticket once,
   only within 2 minutes. Only then does it create the session or link the identity.

</details>

### Security checklist: proxies and ports

- **Trusted proxies.** The panel only honours `RDPANEL_REAL_IP_HEADER` from peers in
  `RDPANEL_TRUSTED_PROXIES` (default: loopback). With host networking and a proxy on the
  same host that is correct.
  - If the proxy is elsewhere, add its address, and make sure it **overwrites** the header
    (as above). Otherwise rate limits, audit logs and login backoff see attacker-chosen IPs.
  - Behind Cloudflare use `RDPANEL_REAL_IP_HEADER=CF-Connecting-IP` and list Cloudflare's
    ranges (or the local tunnel) as trusted.
- **`/metrics`** is answered only when the resolved client IP **and** every address in any
  forwarding header (`X-Forwarded-For`, `X-Real-IP`, `Forwarded`, …) are in
  `RDPANEL_METRICS_ALLOW`. So a loopback proxy never makes remote scrapers look local.
  Blocking it on the public vhost (as above) is still recommended. Metrics, the Grafana
  dashboard, alert rules and a ready Prometheus + Grafana compose file are in
  [monitoring.md](monitoring.md).
- **WebSocket ports 21118/21119** (web client and WS-capable clients) trust
  `X-Real-IP`/`X-Forwarded-For` **unconditionally** (hbbs/hbbr code, not the panel). If
  they are reachable directly, any client can spoof its IP in hbbs/hbbr logs, the IP
  blocker and the relay blocklist. Either:
  - do not expose them (firewall them; nothing else needs them), or
  - expose them only through a reverse proxy that **overwrites** both headers:

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
  `127.0.0.1:21115` or `127.0.0.1:21117`. The hbbs/hbbr admin channel has no
  authentication besides "connection comes from loopback". With `network_mode: host`,
  every process on the host can use it.

## Environment reference

`deploy/rdpanel.env.example` documents the commonly set variables with their defaults.
The panel's own settings are explained in full in [configuration.md](configuration.md).
Summary:

| Variable | Used by | Default (image) | Meaning |
|---|---|---|---|
| `PUID`, `PGID` | all | `10001` | uid/gid the services run as; `0` = root |
| `TZ` | all | UTC | time zone |
| `RELAY` | hbbs | — | `-r`: relay server(s) handed to clients |
| `ENCRYPTED_ONLY` | hbbs, hbbr | `0` | `1` adds `-k _` (only clients with the public key). hbbs requires the key by default anyway; this mainly makes hbbr check it |
| `ALWAYS_USE_RELAY` | hbbs | `N` | `Y` disables P2P |
| `MUST_LOGIN` | hbbs | `N` | `Y` requires a panel login token for connections |
| `TCP_KEEPALIVE` | hbbs | `N` | `Y` keeps TCP/WebSocket-mode clients online (see [WebSocket and TCP-mode clients](#websocket-and-tcp-mode-clients)) |
| `WEBRTC` | hbbs | `N` | `Y` forwards WebRTC signaling for RustDesk 1.5+ clients (see [WebRTC](webrtc.md)) |
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
| `RDPANEL_WEBAUTHN_RP_ID`, `RDPANEL_WEBAUTHN_ORIGINS` | panel | admin URL host / origin | passkey relying party (see [Passkeys](#passkeys)) |
| `RDPANEL_TRUSTED_PROXIES`, `RDPANEL_REAL_IP_HEADER` | panel | loopback, `X-Forwarded-For` | client-IP resolution |
| `RDPANEL_METRICS_ALLOW` | panel | loopback | who may read `/metrics` |
| `RDPANEL_METRICS_PER_DEVICE` | panel | `false` | per-device presence series (see [monitoring](monitoring.md)) |
| `RDPANEL_HBB_REF` | panel | image `HBB_REF` build arg | hbbs/hbbr source ref shown in `rdpanel_build_info` |
| `RDPANEL_HBBS_ID_SERVER`, `RDPANEL_HBBS_RELAY_SERVER`, `RDPANEL_HBBS_PUBLIC_KEY_FILE` | panel | `127.0.0.1:21116`, `127.0.0.1:21117`, `/data/id_ed25519.pub` | where hbbs/hbbr are |
| `RDPANEL_HBBS_RELAY_PUBLIC_HOST`, `RDPANEL_MODE`, `RDPANEL_AGENT_*` | panel, agent | first `RELAY` entry, `panel` | local relay host; `RDPANEL_MODE=agent` turns the image into a relay agent, see [relay-agents.md](relay-agents.md) |
| `RDPANEL_LOG_LEVEL`, `RDPANEL_LOG_FORMAT`, `RDPANEL_CONFIG` | panel | `info`, `json`, — | logging, optional YAML file |
| `RDPANEL_LOGS_BUFFER`, `RDPANEL_LOGS_DIR` | panel, log pipelines | `2000`, `/run/rdpanel/logs` | Server → Logs: lines kept per source (`0` = off) and the s6-log capture dir (see [Server logs](#server-logs)) |
| `RDPANEL_UPDATE_CHECK` | panel | `true` | About page release check against the GitHub API (see [below](#about-page-and-update-check)); `false` = no outbound request |
| `RDPANEL_API_DOCS` | panel | `auth` | Swagger UI at `/api/v1/docs` + spec: `auth`, `public` or `off` (see [api.md](api.md#interactive-docs)); being under `/api/v1/`, a public vhost that 404s `/api/v1/*` keeps it internal |
| `RDPANEL_SMTP_HOST`, `_PORT`, `_ENCRYPTION`, `_USERNAME`, `_PASSWORD`, `_PASSWORD_FILE`, `_FROM`, `_INSECURE_SKIP_VERIFY` | panel | off | outgoing e-mail (see [E-mail](#e-mail-verification-codes-and-password-reset)); otherwise configurable in Settings → E-mail |
| `HEALTHCHECK_PORTS` | healthcheck | `21115 21116 21117 21118 21119` | ports that must listen |

> Do not set `PORT` (hbbr would listen on `PORT+1`) or `KEY` (overrides the key file for
> both daemons) unless you mean it.

## Data layout and backups

```
/data/                       PUID:PGID, same layout as the upstream/lejianwen images
  id_ed25519                 hbbs private key (0600)
  id_ed25519.pub             public key given to clients (0644)
  db_v2.sqlite3              hbbs peer table
  blacklist.txt, blocklist.txt   hbbr lists (written by the panel)
  machine-id                 /etc/machine-id links here (created on first start, 0644)
  panel/                     RDPANEL_DATA_DIR
    panel.db (+ -wal, -shm)  panel database (SQLite WAL)
    secret.key               AEAD key for secrets at rest (back it up with panel.db!)
    jwt.key                  client token key when RUSTDESK_API_JWT_KEY is unset
```

Back up the whole `/data`: stop the container, or use `sqlite3 .backup` for the databases.

| if you lose | then |
|---|---|
| `id_ed25519` | every client must be given the new public key |
| `secret.key` | stored secrets (address-book passwords, OIDC client secrets, TOTP seeds) are unreadable |

## Passkeys

Users add passkeys (Touch ID, Windows Hello, phones, security keys) under
**Profile & security**. Then they can:

- use **Sign in with a passkey** (usernameless, with user verification — it counts as two
  factors), or
- use a passkey instead of the TOTP code after the password/OIDC step.

Admins see and revoke a user's passkeys in the user drawer. Additions, removals and
sign-ins are audited.

**Relying party.** WebAuthn binds every passkey to a *relying party id* (a host name) and
checks the page origin. The panel derives both from where the SPA is served:

- RP id = host of `RDPANEL_ADMIN_URL` (fallback `RDPANEL_PUBLIC_URL`);
- allowed origin = that URL's origin (plus the public URL's origin when its host is within
  the RP id).

With the [split-host layout](#split-hosts-public-client-api--internal-admin-panel),
passkeys belong to the internal admin host (`desk-admin.internal.example.com`); nothing
passkey-related touches the public host. To share passkeys between several panel hosts,
set a common parent domain and list every origin:

```sh
RDPANEL_WEBAUTHN_RP_ID=internal.example.com
RDPANEL_WEBAUTHN_ORIGINS=https://desk-admin.internal.example.com,https://panel.internal.example.com
```

**Limits:**

- Browsers only allow WebAuthn over https (or `http://localhost`) and never for an IP
  address. A panel reached as `http://10.0.0.5:21114` has passkeys disabled (logged at
  start-up).
- Changing the RP id later makes existing passkeys unusable (users re-add them).
- RustDesk desktop/mobile clients cannot do WebAuthn. To use passkeys there, sign in
  through an OIDC provider that supports them (e.g. Pocket ID, Authentik, Keycloak) — the
  panel's OIDC support covers both panel and client logins.

## E-mail: verification, codes and password reset

With an SMTP server configured the panel offers three features.

| feature | what it does |
|---|---|
| **Address verification** | *Profile & security → E-mail* sends a 6-digit code to the user's address. Changing the address clears the verification (and the e-mail second factor). Admins can mark an address verified in the user form. |
| **E-mailed sign-in codes** | Per-user opt-in (verified address only): a code is asked for after the password — on the panel (also after an OIDC login) and in the RustDesk client. TOTP wins when both are enabled. |
| **Forgot password?** | On the login page. Sends a single-use link (`<admin_url>/reset-password?token=…`, 30 minutes) to active accounts with a password and a **verified** address. |

Details:

- In the RustDesk client, `/api/login` answers `{"type":"email_check","tfa_type":"email_check"}`
  and the client shows its built-in code dialog.
- *Settings → Security → Require e-mailed codes without TOTP* makes sign-in codes
  mandatory for every user without TOTP who has a verified address (users without one are
  not locked out). If SMTP is removed later the factor is not enforced.
- Password reset never reveals whether an account exists. A reset revokes every panel
  session and client token of the user. The link needs `RDPANEL_ADMIN_URL` or
  `RDPANEL_PUBLIC_URL`.

<details>
<summary>Code lifetime, rate limits, audit and language</summary>

- Codes are 6 random digits, stored as hashes, valid 10 minutes, invalidated after 5 wrong
  guesses or by a newer code.
- Sending has a 60 s cooldown and hourly caps (10 e-mails per user, 30 per client IP);
  login backoff applies to wrong codes.
- Failed codes appear in the sign-in audit (`email_code_invalid`); the
  `rdpanel_auth_login_attempts_total` metric uses `method="email"`.
- E-mails are plain text + minimal HTML, in English or Russian (the user's choice in the
  profile, else Settings → E-mail → default language).

</details>

**Configure SMTP** in one of two ways:

- in **Settings → E-mail** — the password is stored AEAD-encrypted; a *Send test e-mail*
  button shows the server's error;
- with the environment, which then makes the form read-only:

  ```sh
  RDPANEL_SMTP_HOST=smtp.example.com
  RDPANEL_SMTP_PORT=587                 # default by encryption: 587 / 465 / 25
  RDPANEL_SMTP_ENCRYPTION=starttls      # starttls | tls | none
  RDPANEL_SMTP_USERNAME=panel@example.com
  RDPANEL_SMTP_PASSWORD_FILE=/run/secrets/smtp_password   # or RDPANEL_SMTP_PASSWORD
  RDPANEL_SMTP_FROM="RD Panel <panel@example.com>"
  ```

> `none` sends credentials in clear text — use it only for a relay on a trusted network.
> `RDPANEL_SMTP_INSECURE_SKIP_VERIFY=true` accepts self-signed certificates.

## WebSocket and TCP-mode clients

**Symptom:** clients that use "Use WebSocket", sit behind an HTTP proxy or have UDP blocked
show up as offline or flapping (online about a third of the time).

**Fix:** enable keepalive, in any of these ways:

- `TCP_KEEPALIVE=Y` in `rdpanel.env` (or hbbs `--tcp-keepalive=Y`);
- the admin command `tcp-keepalive Y|N`;
- the **Keepalive for TCP/WebSocket clients** toggle on the panel's Server page. It sets
  the flag at runtime and re-applies it after an hbbs restart (it then overrides the env
  value).

hbbs then sends an empty frame every 12 s on every registered TCP/WebSocket rendezvous
connection. The client echoes it, which also refreshes its registration, so it stays
online continuously. This is an opt-in feature of the hbbs in this image
(bropines/rustdesk-server).

- **When to enable:** you have (or expect) clients with "Use WebSocket" enabled, behind
  HTTP proxies, or on networks that block UDP. Leave it `N` if all your clients use UDP:
  nothing changes for them either way.
- **Cost:** one empty frame per connection every 12 s (a few bytes; the timer runs only
  for registered TCP/WebSocket connections).
- **Trade-off of WebSocket mode itself** (not of the keepalive): WebSocket-mode clients
  always connect through the relay (hbbr, port 21119 or your `/ws/relay` proxy). There is
  no P2P hole punching, so all their traffic goes through your server. Size
  `TOTAL_BANDWIDTH`/`SINGLE_BANDWIDTH` accordingly.
- A reverse proxy in front of 21118 must not time out idle WebSocket connections faster
  than ~30 s (`proxy_read_timeout 1h` above is fine).
- Upstream hbbs does not know the option. With an upstream binary the panel shows the
  setting as unknown and reports an apply error if it is managed.

<details>
<summary>Why TCP/WebSocket clients flap without it</summary>

RustDesk clients normally register with hbbs over UDP every few seconds. Some use a
long-lived TCP or WebSocket rendezvous connection instead: the "Use WebSocket" option
(RustDesk 1.4+), the web client, clients behind a proxy or with UDP blocked.

Such a client registers once and afterwards only echoes the empty frames the server
sends. Upstream hbbs never sends any, so the client times out about every 90 s,
reconnects and re-registers. hbbs (and the panel) show it online for only 30 s after each
registration: in practice the device flickers online about a third of the time.

</details>

## About page and update check

The panel's **About** page (user menu, or the version in the sidebar footer; every
signed-in user) shows:

- the panel version, commit and build date;
- the source of the bundled hbbs/hbbr — the image bakes the `HBB_REPO`/`HBB_REF` build
  args into `rdpanel` (see [Building the image](#building-the-image));
- for admins also: uptime, the database schema version, the platform and live hbbs/hbbr
  status.

To say whether a newer release exists, `rdpanel` asks
`https://api.github.com/repos/bropines/rdpanel/releases/latest` when the page is opened:

- at most once per 6 hours; a failed check is retried after 30 minutes (5 s timeout), and
  the last good answer is kept;
- nothing else depends on it — without internet access the page just says the check
  failed;
- set `RDPANEL_UPDATE_CHECK=false` (YAML `update_check: false`) on air-gapped hosts or if
  the panel must not make outbound requests.

## Server logs

**Server → Logs** (admins) shows recent hbbs, hbbr and panel lines: source chips,
minimum level, search, a live tail (scrolling up pauses auto-scroll), copy line and a
text download.

- **Capture:** each service's log pipeline writes every line to the container's stdout
  (`docker logs` is unchanged) and also to `s6-log` under
  `$RDPANEL_LOGS_DIR/<service>/` (`/run/rdpanel/logs`; `current` rotated at 1 MB, 2 old
  files kept, ~3 MB per service). The panel tails those files, so a slow or restarting
  panel never blocks hbbs/hbbr, and the page is refilled after a panel restart.
- **Memory:** the panel keeps the last `RDPANEL_LOGS_BUFFER` lines per source
  (default `2000`). `0` turns the page and the file copies off.
- **Secrets are redacted before storing:** the hbbs key pair, the JWT and secrets-at-rest
  keys, the SMTP password, JWTs, bearer/basic credentials, `Authorization`/`Cookie`
  headers, `password=`/`token=`/`secret=`-style pairs and credentials in URLs. Use
  `docker logs` for unredacted output.
- **Outside the image** (plain `rdpanel serve`, `RDPANEL_LOGS_DIR` empty) only the
  panel's own lines appear.
- **API:** `GET /api/v1/admin/server/logs` (filters `source`, `level`, `q`, paging
  `limit`/`before`), `…/logs/stream` (SSE) and `…/logs/download`, see
  [api/openapi.yaml](../api/openapi.yaml). Metric: `rdpanel_logs_dropped_total{source}`
  counts lines a too-slow live viewer skipped.

## Image tags and upgrades

| Tag | Built from |
|---|---|
| `X.Y.Z`, `X.Y`, `latest` | release tag `vX.Y.Z` (pre-release tags such as `v1.0.0-rc.1` only get `X.Y.Z-rc.1`) |
| `edge`, `sha-<short>` | every push to `main` |

To upgrade, pull the new tag and recreate the container (`docker compose pull && docker
compose up -d`); the database is migrated on start.

**Renamed image and chart.** Releases up to 0.1.x were published as
`ghcr.io/bropines/rustdesk-panel` (the project's former name). Those tags stay available,
but new releases are only published as `ghcr.io/bropines/rdpanel`:

- switch the `image:` in your compose file or Helm values when you upgrade;
- the Helm chart was renamed the same way
  (`oci://ghcr.io/bropines/charts/rustdesk-panel` → `oci://ghcr.io/bropines/charts/rdpanel`);
  see the [chart README](../deploy/helm/rdpanel/README.md#upgrading-from-the-rustdesk-panel-chart--01x)
  for the in-place upgrade.

**Verifying an image.**

- Each image carries build provenance and an SBOM (buildx attestations) plus a GitHub
  build-provenance attestation:
  `gh attestation verify oci://ghcr.io/bropines/rdpanel:<tag> -R bropines/rdpanel`.
- The exact hbbs/hbbr source commit is in the `io.github.bropines.rustdesk-server.ref`
  label and in `/usr/share/doc/rustdesk-server/SOURCE` inside the image.

### Upgrading from the lejianwen s6 image (`lejianwen/rustdesk-server-s6` + `rustdesk-api`)

The hbbs/hbbr data layout is the same (`/data/id_ed25519{,.pub}`, `db_v2.sqlite3`), so
clients keep working with their configured key. Full importer guide:
[migration.md](migration.md).

1. Note the old container's env: `RELAY`, `ENCRYPTED_ONLY`, `MUST_LOGIN`,
   `ALWAYS_USE_RELAY` and especially `RUSTDESK_API_JWT_KEY` (or `jwt.key` in the old API
   config). Put the same values in `rdpanel.env`; the same JWT key keeps already-issued
   client tokens valid.
2. Stop the old container. Copy the old API database (`/app/data/rustdeskapi.db` in the
   old container) somewhere readable, e.g. `./rustdeskapi.db`. It must be mode 0644 or
   owned by `PUID`: the CLI runs as `PUID`.
3. Mount the old hbbs data dir as `/data` (and optionally a new dir as `/data/panel`); see
   the upgrade example at the end of `deploy/compose.example.yml`. On first start
   `key-secret` validates the existing key pair and chowns the files to `PUID:PGID` (the
   old image ran as root).
4. Start the container, then import once, **before** creating users in the new panel (the
   import refuses a non-empty target DB):

   ```sh
   docker compose exec rustdesk rdpanel migrate-legacy --from /import/rustdeskapi.db --dry-run --report /data/panel/migrate-report.json
   docker compose exec rustdesk rdpanel migrate-legacy --from /import/rustdeskapi.db
   ```
5. Point the reverse proxy at the new `RDPANEL_LISTEN` (e.g. `127.0.0.1:31114`).
6. **Rollback:** stop the new container and start the old one on the same hbbs data dir.
   It only needs root to own the files again (`chown -R 0:0 hbbs-data`).

## Container internals

Background for troubleshooting and hardening; not needed for a normal install.

<details>
<summary>What happens on start</summary>

1. `key-secret` (oneshot, root):
   - **hbbs key pair** `/data/id_ed25519{,.pub}`. Sources, first match wins: existing
     files, then docker secrets `key_priv`/`key_pub`, then env `KEY_PRIV`/`KEY_PUB`,
     otherwise a new pair is generated with `rustdesk-utils genkeypair` (same libsodium
     ed25519 format hbbs writes).
     - Both halves or neither; a provided pair is validated.
     - `KEY_PRIV`/`KEY_PUB` (and the secrets) are raw file contents and are only written
       when the file does not exist yet. Existing keys are never overwritten: if a
       provided key differs, a warning is logged and the existing key is kept (delete the
       file to replace it).
     - Permissions: private 0600, public 0644.
     - Generating here instead of letting hbbs do it avoids hbbs and hbbr racing to create
       the files on first start, and avoids a world-readable private key.
   - **JWT key:** if `RUSTDESK_API_JWT_KEY` is unset, makes sure
     `$RDPANEL_DATA_DIR/jwt.key` exists. The hbbs run script exports its content as
     `RUSTDESK_API_JWT_KEY`, so the panel and hbbs (MUST_LOGIN) always use the same key.
   - **Machine id:** `/etc/machine-id` is a link to `/data/machine-id`, created once
     with 32 random hex chars (hbbs/hbbr read it; a non-empty `/etc/machine-id` you
     mount yourself is left alone).
   - **Ownership:** chowns `/data` and the panel data dir to `PUID:PGID` (default
     `10001:10001`; skipped when `PUID=0`). Data dirs created by root-run images therefore
     keep working.
   - Any failure stops the container (`S6_BEHAVIOUR_IF_STAGE2_FAILS=2`).
2. `hbbs`, `hbbr` (cwd `/data`) and then `panel` (after hbbs and hbbr) start, each as
   `PUID:PGID` with no supplementary groups. All three are one trust domain (the panel has
   full control of hbbs/hbbr through the loopback admin channel), so one unprivileged uid
   for all of them is enough. None needs root because all ports are above 1024.
3. Each service's stdout/stderr goes through an s6-rc pipeline logger that prefixes lines
   with `[hbbs] `, `[hbbr] `, `[panel] ` and writes them to the container's stdout
   (and, for Server → Logs, unprefixed to `s6-log` under `$RDPANEL_LOGS_DIR/<service>/`).
   Services restart automatically when they exit.

</details>

<details>
<summary>Shutdown</summary>

`docker stop` sends SIGTERM to s6, which stops the panel (graceful HTTP shutdown, SIGKILL
after 7 s) and hbbs/hbbr (SIGKILL after 5 s). Shutdown usually completes in ~3 s; the
worst case with kill timeouts is about 8–10 s. That is well inside the compose example's
`stop_grace_period: 20s` and Kubernetes' default `terminationGracePeriodSeconds: 30`.

</details>

<details>
<summary>Running the CLI (<code>docker exec … rdpanel</code>)</summary>

`docker exec <container> rdpanel ...` (and `kubectl exec`, or the Helm migration Job,
which runs `rdpanel` without s6) uses the wrapper `/usr/local/bin/rdpanel`:

1. as root it first creates/chowns the data dirs to `PUID:PGID`;
2. then drops to that user before calling `/usr/bin/rdpanel`.

So the CLI never leaves root-owned files in the data dir. Files the CLI only reads (e.g.
the legacy `rustdeskapi.db`) must be readable by `PUID` (mode 0644 or owned by it).

</details>

<details>
<summary>Required privileges</summary>

- The container starts as root (s6) but only needs the capabilities
  `CHOWN, DAC_OVERRIDE, FOWNER, SETGID, SETUID, KILL`.
- It works with all others dropped and `no-new-privileges` /
  `allowPrivilegeEscalation: false`. This is the Helm chart's default securityContext;
  verified with `docker run --cap-drop ALL --cap-add ... --security-opt no-new-privileges`.
- All ports are above 1024 and no binary has file capabilities.

</details>

<details>
<summary>Health check</summary>

`/usr/libexec/rdpanel/healthcheck` (Docker `HEALTHCHECK`, every 15 s) requires:

- s6 reports `hbbs`, `hbbr` and `panel` up;
- TCP ports 21115-21119 are in LISTEN state (read from `/proc/net/tcp*`, no probe
  connections; override with `HEALTHCHECK_PORTS`);
- `GET /healthz` on `RDPANEL_LISTEN` (which pings the panel DB) succeeds.

</details>

## Building the image

### CI

`.github/workflows/image.yml` runs on tags `v*`, pushes to `main` and manual dispatch:

1. **`hbb` job** (matrix amd64/arm64):
   - checks out `bropines/rustdesk-server` at `server_ref` with submodules. The default is
     the commit pinned in `SERVER_REF` (from the fork's `rdpanel` branch);
   - builds `hbbs`, `hbbr`, `rustdesk-utils` with `cross` for
     `{x86_64,aarch64}-unknown-linux-musl` (static); cargo cache via `Swatinem/rust-cache`.
   - The server repo must keep its committed `.env`
     (`DATABASE_URL=sqlite://./db_v2.sqlite3`) and `db_v2.sqlite3`: `sqlx::query!` checks
     queries against that database at compile time.
2. **`image` job:**
   - puts the binaries into `deploy/bin/<arch>/`;
   - runs one buildx multi-platform build of `deploy/Dockerfile`;
   - pushes to GHCR with `GITHUB_TOKEN` (`packages: write`) only, with provenance
     (`mode=max`) and SBOM attestations and OCI labels/annotations.

### Locally (`make image`)

Requirements: Docker with buildx, a checkout of the server fork (`rdpanel` branch) with
submodules (`HBB_SRC`, default `~/build/rustdesk-server`).

```sh
make image                                  # -> rdpanel:local for the host arch
make image HBB_SRC=/path/to/rustdesk-server IMAGE=rdpanel:test
make hbb                                    # only (re)build deploy/bin/<arch>/hbb*
```

- `make hbb` (`deploy/scripts/build-hbb.sh`) compiles the static musl binaries in a
  `rust:1-alpine` container. Cargo caches live in the docker volumes
  `rdpanel-hbb-registry`, `rdpanel-hbb-git` and `rdpanel-hbb-target` (delete them to
  reclaim space).
- A glibc build (`cargo build` on Debian) does **not** work in the Alpine image.
- `make image` only runs `make hbb` when `deploy/bin/<arch>/` is empty. Run `make hbb`
  yourself after changing the server sources.

<details>
<summary>Dockerfile design</summary>

- **Stages:**
  - `web` (node 22, `npm ci && npm run build`);
  - `panel` (newest stable Go image, `CGO_ENABLED=0`, `GOTOOLCHAIN=local`, cross-compiled
    with `GOOS/GOARCH` from the build platform; the code stays go1.23-compatible);
  - `s6` (downloads s6-overlay, verifies pinned SHA-256 sums);
  - final `alpine` with `ca-certificates` and `tzdata`.
- **Prebuilt hbbs/hbbr.** They come from `deploy/bin/<TARGETARCH>/` in the build context
  rather than being compiled in the Dockerfile. The Rust LTO build takes minutes per arch
  and would run under QEMU for the foreign arch; CI compiles it natively with `cross` and
  caches it, and the local path reuses a cached build. Only trivial `RUN` steps execute on
  the target platform.
- **Source record.** `HBB_REPO`/`HBB_REF` (the server fork and the commit the binaries were
  built from) are written to `/usr/share/doc/rustdesk-server/SOURCE` (AGPL §6) and linked
  into `rdpanel` (`internal/buildinfo.HBBRepo/HBBRef` ldflags) for the About page and
  `rdpanel version`.
- **Bumping base images:** `ALPINE_VERSION`, `GO_IMAGE`, `NODE_IMAGE`,
  `S6_OVERLAY_VERSION` (+ the three `S6_SHA256_*` values from the release's `.sha256`
  files), as build args or in the Dockerfile.

</details>
