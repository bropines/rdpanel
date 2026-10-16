# Configuration reference

Every setting of the `rdpanel` binary: environment variable, YAML key and default.
For operators tuning a deployment. Container-only variables (hbbs/hbbr, `PUID`, `RELAY`, …)
are in [deploy.md → Environment reference](deploy.md#environment-reference).

- [How settings are resolved](#how-settings-are-resolved)
- [Settings](#settings) — [core](#core), [auth and sessions](#auth-and-sessions),
  [passkeys](#passkeys), [HTTP and metrics](#http-and-metrics), [hbbs/hbbr](#hbbshbbr),
  [logging and updates](#logging-and-updates), [e-mail (SMTP)](#e-mail-smtp)
- [Example YAML](#example-yaml)

## How settings are resolved

1. Built-in defaults (the tables below).
2. The optional YAML file named by `RDPANEL_CONFIG` (YAML keys in parentheses).
3. Environment variables — they override both.

`rdpanel config print` shows the effective configuration with secrets redacted.
Defaults are the binary's; the container image changes a few (for example
`RDPANEL_DATA_DIR=/data/panel`), see [deploy.md](deploy.md#environment-reference).

## Settings

### Core

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

### Auth and sessions

| variable (yaml key) | default | description |
|---|---|---|
| `RUSTDESK_API_JWT_KEY` (`auth.jwt_key`) | — | HS256 key for client tokens, used verbatim; must equal hbbs' key and the legacy rustdesk-api key |
| `RDPANEL_JWT_KEY_FILE` (`auth.jwt_key_file`) | `<data>/jwt.key` | generated key used when `RUSTDESK_API_JWT_KEY` is unset |
| `RDPANEL_TOKEN_LIFETIME` (`auth.token_lifetime`) | `4380h` | sliding lifetime of client tokens |
| `RDPANEL_JWT_LIFETIME` (`auth.jwt_lifetime`) | = token lifetime | fixed JWT `exp` (what hbbs enforces; hbbs cannot see revocation) |
| `RDPANEL_SESSION_IDLE` (`auth.session_idle`) | `12h` | panel session idle timeout |
| `RDPANEL_SESSION_ABSOLUTE` (`auth.session_absolute`) | `168h` | panel session absolute lifetime |

### Passkeys

See [deploy.md → Passkeys](deploy.md#passkeys) for how the RP id and origins are chosen.

| variable (yaml key) | default | description |
|---|---|---|
| `RDPANEL_WEBAUTHN_RP_ID` (`webauthn.rp_id`) | host of the admin URL | passkey relying party id: the panel host or a parent domain of every origin (passkeys are bound to it — changing it invalidates registered passkeys) |
| `RDPANEL_WEBAUTHN_ORIGINS` (`webauthn.origins`) | admin URL origin (+ public URL origin when within the RP id) | comma-separated origins (`scheme://host[:port]`) the panel is opened from; passkeys are off when neither this nor a URL is set, or the host is an IP address |

### HTTP and metrics

| variable (yaml key) | default | description |
|---|---|---|
| `RDPANEL_TRUSTED_PROXIES` (`http.trusted_proxies`) | `127.0.0.0/8,::1/128` | IPs/CIDRs whose forwarding headers are trusted |
| `RDPANEL_REAL_IP_HEADER` (`http.real_ip_header`) | `X-Forwarded-For` | or `X-Real-IP`, `CF-Connecting-IP` |
| `RDPANEL_METRICS_ALLOW` (`http.metrics_allow`) | `127.0.0.0/8,::1/128` | client IPs allowed to read `/metrics` |
| `RDPANEL_METRICS_PER_DEVICE` (`http.metrics_per_device`) | `false` | also export `rdpanel_presence_device_online{id}`, one series per device (cardinality = fleet size) |

### hbbs/hbbr

| variable (yaml key) | default | description |
|---|---|---|
| `RDPANEL_HBB_REF` | — (set by the image) | hbbs/hbbr source ref reported as `hbb_ref` in `rdpanel_build_info` |
| `RDPANEL_HBBS_ID_SERVER` (`hbbs.id_server`) | `127.0.0.1:21116` | hbbs rendezvous address (admin channel = port − 1) |
| `RDPANEL_HBBS_RELAY_SERVER` (`hbbs.relay_server`) | `127.0.0.1:21117` | hbbr address (admin channel on the same port) |
| `RDPANEL_HBBS_PUBLIC_KEY_FILE` (`hbbs.public_key_file`) | `<data>/id_ed25519.pub` | hbbs public key served to clients |
| `RDPANEL_HBBS_ENABLED` (`hbbs.enabled`) | `true` | presence poller + hbbs/hbbr reconciler workers |
| `RDPANEL_HBBS_POLL_INTERVAL` (`hbbs.poll_interval`) | `5s` | presence poll period (batched `OnlineRequest`) |
| `RDPANEL_HBBS_RECONCILE_INTERVAL` (`hbbs.reconcile_interval`) | `15s` | admin-channel probe / drift-correction period |
| `RDPANEL_HBBS_LIST_DIR` (`hbbs.list_dir`) | empty | hbbr working dir; when set, `blacklist.txt`/`blocklist.txt` are kept in sync there (`/data` in the s6 image) |

### Logging and updates

| variable (yaml key) | default | description |
|---|---|---|
| `RDPANEL_LOG_LEVEL` (`log.level`) | `info` | `debug`, `info`, `warn`, `error` |
| `RDPANEL_LOG_FORMAT` (`log.format`) | `json` | `json` or `text` |
| `RDPANEL_UPDATE_CHECK` (`update_check`) | `true` | About page "update available" check: the server asks the GitHub releases API at most every 6 h (failures tolerated); `false` = no outbound request |

### E-mail (SMTP)

Leave `RDPANEL_SMTP_HOST` empty to configure e-mail in **Settings → E-mail** instead (or
not at all). Feature overview: [deploy.md → E-mail](deploy.md#e-mail-verification-codes-and-password-reset).

| variable (yaml key) | default | description |
|---|---|---|
| `RDPANEL_SMTP_HOST` (`smtp.host`) | empty | outgoing mail server; empty = e-mail is configured in Settings → E-mail (or off). When set, the whole `smtp.*` block comes from the configuration and is read-only in the panel |
| `RDPANEL_SMTP_PORT` (`smtp.port`) | 587 / 465 / 25 | default follows the encryption |
| `RDPANEL_SMTP_ENCRYPTION` (`smtp.encryption`) | `starttls` | `starttls`, `tls` (implicit TLS, SMTPS) or `none` |
| `RDPANEL_SMTP_USERNAME`, `RDPANEL_SMTP_PASSWORD` (`smtp.username`, `smtp.password`) | empty | AUTH PLAIN or LOGIN; `RDPANEL_SMTP_PASSWORD_FILE` (`smtp.password_file`) reads the password from a file |
| `RDPANEL_SMTP_FROM` (`smtp.from`) | — | sender, e.g. `RD Panel <panel@example.com>` (required with a host) |
| `RDPANEL_SMTP_INSECURE_SKIP_VERIFY` (`smtp.insecure_skip_verify`) | `false` | accept any TLS certificate (self-signed relays only) |

## Example YAML

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
