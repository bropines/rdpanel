# Share links

A share link lets someone without a panel account connect to **this** server as simply as
possible: whoever opens `<public_url>/s/<token>` gets

- a big download button for their platform: on Windows the official installer, served by the
  panel under a file name that carries this server's settings (the same file as on
  [Install client](install-client.md)); on macOS, Linux, Android and iPhone/iPad the official
  packages from GitHub or the App Store, with short steps;
- a QR code (`config=<config string>`) for the RustDesk mobile app's scanner;
- the manual settings (ID server, relay, API server, key, config string) with copy buttons.

Nothing else is on the page: no owner, label, counters or expiry, and no other files.

Share links are **off by default**. Turn them on in **Settings → Branding → Share page**;
while off, everything under `/s/` answers 404, no link can be created, and the rest of the
section is hidden.

`RDPANEL_SHARE_LINKS_ENABLED=true` (YAML `share_links.enabled: true`) is the starting
value, for a deployment that wants them on from the first boot. Once the switch has been
touched in the panel it decides, and the environment no longer does — so a deployment
started with the variable on can be turned off without a redeploy, and the other way
round.

## Creating links

**My → Install client → Share this server** (shown when share links are on and you may
create them):

- **Permanent** (until revoked) or **temporary**: 1 hour, 24 hours, 7 days, 30 days or a
  custom date (up to one year).
- **Max downloads** (optional): after this many downloads of the preconfigured Windows
  installer the link stops working. Other platforms link to GitHub / the App Store and are
  not counted.
- **Label** (optional, only shown in the panel).

After creating a link the panel shows its URL and a QR code of the URL (to open it on a
phone). The list shows each link's status (active, expired, used up, suspended), views
(openings of the page), downloads, expiry, and buttons to copy, show the QR code, open and
revoke. A link can be copied again later: the token is kept encrypted with the panel's
secret key. At most 100 open links per user.

Admins also get an **All links** tab with every user's links and their owners, and may
revoke any of them. Every creation and revocation is in the admin audit log
(`share_link.create`, `share_link.revoke`; the token is never logged).

### Who may share

- Admins always.
- Regular users only when their user group allows it: **Groups → user group → Can share
  server links**. Taking the right away (or disabling the user) suspends their existing
  links immediately; deleting the user deletes them.

### Page texts

**Settings → Branding → Share page**: the page title (default "Connect to `<host>`") and a
short description (up to 500 characters). Both are plain text and shown escaped.

## Reverse proxy

The page lives on the **public** host (`RDPANEL_PUBLIC_URL`), next to the RustDesk client
API, and needs only `/s/` (the page, its data and its static files are all under it). With
the split-host setup from [deploy.md](deploy.md#split-hosts-public-client-api--internal-admin-panel),
add one location to the public server block:

```nginx
# Public host: client API + share links.
location /s/ {
    include rdpanel-proxy.conf;
    # The token is in the path: keep it out of the access log (or log without $request_uri).
    access_log off;
}
```

On a single host (everything proxied to the panel) nothing changes. The panel's own request
log replaces the token with `[token]`.

## Endpoints

| path | what |
|---|---|
| `GET /s/{token}` | the page (200; 404 for unknown and 410 for revoked, expired, used up or suspended links, with a "This link is no longer valid" page) |
| `GET /s/{token}/info` | the page's data: connection settings, page texts, downloads (JSON) |
| `GET /s/{token}/windows/{arch}` | the preconfigured Windows installer (`x64`, `arm64`); a request that starts a download counts one download, resumed ranges are free |
| `GET /s/assets/*`, `/s/favicon.svg` | the page's static files |

All of them work without a session, are sent with `Cache-Control: no-store` (static files:
cached), `X-Robots-Tag: noindex` and `Referrer-Policy: no-referrer`, and are rate limited per
client IP (pages and data about 1/s with bursts of 30, installer downloads one per 10 s with
bursts of 10). Unknown tokens count towards a per-IP backoff (the one failed logins use:
three free attempts, then 1 s doubling up to 15 min). Tokens
have 128 random bits; the database keeps their SHA-256 for lookups and an AEAD-encrypted
copy for the creator.

The admin API (`/api/v1/admin/share-links`, `GET`/`POST`, `DELETE /{id}`) is in
[`api/openapi.yaml`](../api/openapi.yaml) (tag `share`).
