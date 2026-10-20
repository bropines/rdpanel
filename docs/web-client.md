# Web client

RD Panel includes a browser remote desktop client at `/connect` (also linked
from a device's drawer as **Connect in browser**). It is built on
[rdweb](https://github.com/bropines/rdweb) and needs no plugins: it talks to
hbbs/hbbr over WebSocket and decodes video with WebCodecs.

## Requirements

- The reverse proxy in front of the panel forwards, with WebSocket upgrade:
  - `/ws/id` → hbbs WebSocket port (21118)
  - `/ws/relay` → hbbr WebSocket port (21119)

  The panel itself also proxies these paths to the local hbbs/hbbr when
  `RDPANEL_HBBS_WEB_PROXY=true` (default), so a proxy that sends everything
  to the panel works too. If browsers reach hbbs/hbbr somewhere else, set
  `RDPANEL_WEBCLIENT_ID_URL` / `RDPANEL_WEBCLIENT_RELAY_URL`.
- The server public key file (`RDPANEL_HBBS_PUBLIC_KEY_FILE`) must be
  readable: the client refuses to connect to a device it cannot verify.
- A current Chrome, Edge, Safari (17+) or Firefox (130+). H.265 depends on
  the platform's hardware decoders; the client only offers codecs the
  browser can decode.

## Who can use it

Every signed-in panel user, or only admins with
`RDPANEL_WEBCLIENT_ADMIN_ONLY=true`; `RDPANEL_WEBCLIENT_ENABLED=false` turns it
off. Access to a device is still decided by the device itself (its password,
or the remote user accepting the request), exactly as for desktop clients.

Each browser session announces a panel-issued id (`web-…`) as its own id. The
device reports it back in its connection events, so
[connection rules](acl-policy.md) apply to the panel user who opened the
session. With `MUST_LOGIN=Y`, the panel hands the browser a short-lived hbbs
login token for the signed-in user.

## Security model

- The server key vouches for the device's long-term key, and the device signs
  its per-session key with it; the client verifies both and refuses anything
  else (there is no fallback to an unauthenticated connection).
- The session is end-to-end encrypted between the browser and the device; the
  relay only forwards ciphertext.
- Passwords never appear in URLs. "Remember on this browser" stores the
  salted hash the desktop client stores (`SHA256(password + salt)`) in the
  browser's local storage, bound to the server key and device id. It logs in
  to that device but does not reveal the password. **Actions → Forget saved
  password** removes it.

## Other servers

The web client can also reach devices registered on other RustDesk-compatible
servers (for example a second office, or a customer's own server).

**Offered by the admin** — safe default, only these origins are allowed by the
page's Content-Security-Policy:

```sh
RDPANEL_WEBCLIENT_SERVERS='[{"name":"Office","id_url":"wss://rd.office.example/ws/id","key":"MEbIUN8…H+A="}]'
```

or in YAML:

```yaml
webclient:
  servers:
    - name: Office
      id_url: wss://rd.office.example/ws/id
      relay_url: wss://rd.office.example/ws/relay   # optional, this is the default
      key: MEbIUN8…H+A=                             # that server's id_ed25519.pub
```

**Entered by users** — `RDPANEL_WEBCLIENT_CUSTOM_SERVERS=true` adds "Other
server…" to the connect form: users type a host (or paste a RustDesk config
string, the one the RustDesk client's ID/Relay server dialog and RD Panel's
Server page export) and the key. The CSP then allows WebSocket connections to
every `wss:` origin, which a cross-site scripting bug could use to send data
anywhere over WebSocket — leave it off unless you need it.

Sessions to other servers are plain RustDesk sessions: the panel's login
token, web session id, connection rules, control roles and address-book
passwords do not apply there (the other server does not know this panel).

### What the other server needs

The browser only speaks WebSocket over TLS, so the other server must expose
hbbs and hbbr through a reverse proxy with a certificate:

| path | upstream | |
|---|---|---|
| `wss://<host>/ws/id` | hbbs `21118` (WebSocket) | rendezvous |
| `wss://<host>/ws/relay` | hbbr `21119` (WebSocket) | relay |

Example (nginx / Angie):

```nginx
location /ws/id {
    proxy_pass http://127.0.0.1:21118;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header X-Real-IP $remote_addr;
    proxy_read_timeout 86400s;
}
location /ws/relay {
    proxy_pass http://127.0.0.1:21119;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header X-Real-IP $remote_addr;
    proxy_read_timeout 86400s;
}
```

Also on that server:

- **Key:** copy its `id_ed25519.pub` (the public key RustDesk clients use);
  the web client refuses servers it cannot verify.
- **Relay:** hbbs must announce a relay that browsers can reach through
  `/ws/relay` on the same host (its own hbbr). With RD Panel's image this is
  automatic (`WS_RELAY_SERVERS`); with plain hbbs, use a single local relay.
- **Login:** with `MUST_LOGIN=Y` on that server, browser sessions without its
  token are refused — leave it off for web access.
- hbbs/hbbr from upstream RustDesk 1.1.11+ (or this panel's image) serve
  WebSocket on 21118/21119; the ports only need to be reachable by the proxy.

## Toolbar

The session toolbar floats at the top of the screen and folds into a small
handle a few seconds after the pointer leaves it; the pin keeps it open, and on
desktops the grip moves it along the top edge. Pick the buttons you want on it
in **⋯ → Customize toolbar**; everything else stays in the ⋯ menu. The choice,
the pin and the position are remembered in the browser.

## Files

**Files** (toolbar or ⋯ menu) opens a separate file-transfer connection with
the same login: browse folders (drives on Windows), download files and folders,
upload files and folders (buttons or drag and drop; existing files can be
replaced, skipped or resumed), create, rename and delete. Chrome and Edge save
downloaded folders straight into a folder you pick; Safari and Firefox get a
zip archive (up to 4 GiB). The control role's file-transfer permission
applies.

## Remembered passwords

"Remember in my account" stores the device's salted password hash in your
personal address book, exactly as the desktop client's address book does:
afterwards neither the web client (in any browser) nor RustDesk apps signed in
to the same account ask for that device's password. **⋯ → Forget saved
password** removes it again. Devices in shared address books with a password
log in with that password. RustDesk devices only accept their own password or a
click on "Accept" — a signed-in account alone does not unlock a device.

## Features

Display: fit / original size / stretch, monitor selection, image quality
presets or custom quality and frame rate, codec (VP8, VP9, AV1, H.264, H.265),
remote resolution, 4:4:4 true color, remote cursor (as the local pointer, or
drawn for touch input and "show remote cursor"), follow remote cursor/window,
quality monitor.

Control: mouse, wheel and smooth touchpad scrolling; on touch screens a
touchpad mode (relative pointer) or direct touch, pinch to zoom; keyboard in
Map (physical keys), Translate (local layout) or Legacy mode, on-screen
keyboard with an extra keys row; view only, mute, clipboard on/off, lock after
session end, privacy mode, block user input, Ctrl+Alt+Del, lock screen,
restart, send/type text, chat, screenshot, 2FA and "accept on the remote side"
logins.

Not yet: file transfer, audio input, recording.
