# Admin API for scripts

Everything the panel UI does goes through its JSON API under `/api/v1/admin`. Scripts and
automation (inventory exports, audit shipping, provisioning users) can call the same API
with a **personal API token** instead of a browser session.

The full contract (every path, parameter and schema) is [`api/openapi.yaml`](../api/openapi.yaml)
(OpenAPI 3.1; load it into Swagger UI, Insomnia or a client generator).

## Interactive docs

The panel serves Swagger UI for this contract at **`/api/v1/docs`** on the admin host
(linked from the user menu, About and Profile → API tokens), and the spec itself at
`/api/v1/openapi.yaml` and `/api/v1/openapi.json`. Everything is bundled into the binary
(no CDN) and works under the panel's CSP.

- **Try it out** runs requests as you, with your browser session; the page adds the
  `X-CSRF-Token` header itself. **Authorize** → `bearerAuth` takes a personal API token
  (`rdp_…`) instead, to check what a script with that token may do.
- When the spec is requested via the `RDPANEL_ADMIN_URL` host, its `servers` URLs are
  absolute (`https://desk-admin.internal.example.com/api/v1/admin`), ready for client generators.
- `RDPANEL_API_DOCS` (YAML `api_docs`): `auth` (default) requires a signed-in session or
  an API token (browsers are redirected to the login and back, other clients get 401);
  `public` makes the page and spec readable without login (API calls still need auth);
  `off` answers 404.
- The routes live under `/api/v1/`, so your reverse proxy decides where they are
  reachable: with [split hosts](deploy.md#split-hosts-public-client-api--internal-admin-panel)
  the public vhost 404s `/api/v1/*` and the docs stay internal.

```sh
curl -H "Authorization: Bearer $RDPANEL_TOKEN" https://desk-admin.internal.example.com/api/v1/openapi.json -o rdpanel-openapi.json
```

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

## Declarative Access Policy (HuJSON)

Manage all connection ACL rules, roles, and automated test assertions in a single version-controlled HuJSON file:

- **Get current policy:** `GET /api/v1/admin/acl/policy`
- **Dry-run validation / test:** `POST /api/v1/admin/acl/policy/test`
- **Atomic update:** `PUT /api/v1/admin/acl/policy`

```sh
# Dry-run validate policy with assertions
curl -fsS -X POST -H "Authorization: Bearer $RDPANEL_TOKEN" -H "Content-Type: application/json" \
  -d "{\"policy\": $(jq -Rs . < policy.hujson)}" \
  "$RDPANEL/api/v1/admin/acl/policy/test"

# Apply policy
curl -fsS -X PUT -H "Authorization: Bearer $RDPANEL_TOKEN" -H "Content-Type: application/json" \
  -d "{\"policy\": $(jq -Rs . < policy.hujson)}" \
  "$RDPANEL/api/v1/admin/acl/policy"
```

Full specification and GitOps workflow guide: [`docs/acl-policy.md`](acl-policy.md).

Errors always have the body `{"error":{"code":"...","message":"..."}}`; the codes are
listed at the top of `api/openapi.yaml`.

## Monitoring

`rdpanel_auth_api_token_requests_total{result="success|failure|rate_limited"}` counts
requests that presented a token ([monitoring.md](monitoring.md)). Tokens never appear in
logs.

