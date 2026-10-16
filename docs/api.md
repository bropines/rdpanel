# Admin API for scripts

Everything the panel UI does goes through its JSON API under `/api/v1/admin`. Scripts and
automation (inventory exports, audit shipping, provisioning users) can call the same API
with a **personal API token** instead of a browser session.

The full contract (every path, parameter and schema) is [`api/openapi.yaml`](../api/openapi.yaml)
(OpenAPI 3.1; load it into Swagger UI, Insomnia or a client generator).

## Where the API lives

The admin API is served on the panel's host: `RDPANEL_ADMIN_URL` when the panel has its
own (internal) host, otherwise `RDPANEL_PUBLIC_URL`. With split hosts
([deploy.md](deploy.md#split-hosts-public-client-api--internal-admin-panel)) scripts must
reach the internal admin host; the public host only serves the RustDesk client API, and
API tokens are **not** accepted there (`/api/*` keeps using client tokens).

## Create a token

Profile & security → **API tokens** → **Create token**:

- **Name**: to recognise it later ("backup script", "Grafana").
- **Access**: *Read only* allows only `GET`/`HEAD` requests (any other method answers 403
  `insufficient_scope`); *Full access* can do everything you can do in the panel.
- **Expires**: 30, 90 or 365 days, or never.

The token (`rdp_` followed by 40 letters and digits) is shown **once**; copy it into your
secrets manager. The panel stores only its SHA-256 hash and shows the first characters
(`rdp_7Hq2xK…`), the last use time and IP.

A token acts as you with your **current** role: if you are demoted to `user`, it loses admin
endpoints; if your account is disabled, or the token is expired or revoked, requests get 401.
Revoke tokens in the same card; admins can list and revoke anyone's tokens in the user's
drawer (Users → user → Security). Creating and revoking tokens is recorded in the admin
audit log, and actions performed with a token carry `via_api_token` in their details.

Limits by design: a token cannot create or revoke tokens, and cannot use the
`/me/security/*` endpoints (password, 2FA, passkeys, sessions); those answer 403
`cookie_session_required`. Failed token attempts are rate-limited per client IP like logins
(429 with `Retry-After`). Requests with a token need no CSRF header.

## Examples

```sh
export RDPANEL=https://desk-admin.internal.example.com
export RDPANEL_TOKEN=rdp_...   # from the panel; keep it out of shell history and logs
```

Who am I:

```sh
curl -fsS -H "Authorization: Bearer $RDPANEL_TOKEN" "$RDPANEL/api/v1/admin/auth/me"
```

List online devices (paginated: `page`, `page_size` up to 500):

```sh
curl -fsS -H "Authorization: Bearer $RDPANEL_TOKEN" \
  "$RDPANEL/api/v1/admin/devices?online=true&page_size=500" | jq '.items[] | {rustdesk_id, hostname, os}'
```

Admin audit since a date (admin role):

```sh
curl -fsS -H "Authorization: Bearer $RDPANEL_TOKEN" \
  "$RDPANEL/api/v1/admin/audit/admin?from=2026-10-01T00:00:00Z&page_size=100"
```

Connection audit:

```sh
curl -fsS -H "Authorization: Bearer $RDPANEL_TOKEN" "$RDPANEL/api/v1/admin/audit/connections?page_size=50"
```

Live events (Server-Sent Events; presence changes, device updates, connections):

```sh
curl -N -H "Authorization: Bearer $RDPANEL_TOKEN" "$RDPANEL/api/v1/events"
```

A write call with a *Full access* token (no CSRF header needed):

```sh
curl -fsS -X POST -H "Authorization: Bearer $RDPANEL_TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"ops"}' "$RDPANEL/api/v1/admin/user-groups"
```

Errors always have the body `{"error":{"code":"...","message":"..."}}`; the codes are
listed at the top of `api/openapi.yaml`.

## Monitoring

`rdpanel_auth_api_token_requests_total{result="success|failure|rate_limited"}` counts
requests that presented a token ([monitoring.md](monitoring.md)). Tokens never appear in
logs.
