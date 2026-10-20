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
