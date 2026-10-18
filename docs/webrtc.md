# WebRTC

RustDesk 1.5.0 added a WebRTC transport (ICE + DTLS data channel) next to the classic TCP/UDP
hole punch and the hbbr relay. When it works, the session runs peer-to-peer even through NATs
where the classic punch fails, or through a TURN server you run. The rendezvous server only
carries the signaling. Upstream hbbs drops it, so out of the box RustDesk falls back to the
classic transports.

RD Panel's hbbs ([bropines/rustdesk-server](https://github.com/bropines/rustdesk-server), the
build in the image) forwards the signaling when you turn it on. It is **off by default**.

## What you need

| piece | where | default |
|---|---|---|
| WebRTC signaling in hbbs | **Server → Settings → WebRTC signaling**, or `WEBRTC=Y` in `rdpanel.env`, or Helm `server.webrtc: true` | off |
| WebRTC in the controlling client | RustDesk **Settings → General → Other → Enable WebRTC P2P connection** (desktop; mobile: **Settings**) on the device you connect *from* | off for self-hosted servers |
| ICE servers (optional TURN) | **Server → Settings → Client options (WebRTC)**, pushed to clients | RustDesk's public STUN servers |
| RustDesk version | 1.5.0 or newer on both sides | — |

Notes:

- `enable-webrtc` is a local option of the RustDesk UI. Clients keep it in the local config, which
  the server-side strategy cannot write, so the panel cannot switch it on for you. Each user turns
  it on in the client that starts connections (or you ship a custom client with it preset). The
  controlled side needs nothing: it answers any offer it can (not behind a SOCKS proxy).
- The page setting overrides `WEBRTC` from the environment, like every managed server setting. The
  hbbs admin command is `webrtc Y|N` (alias `wr`) in **Server → Console**.
- With **Always use relay** on, hbbs drops WebRTC offers, because a WebRTC pair can be direct.
  Connections then use the relay as before.
- **Require login** still applies: hbbs forwards an offer only after the login, key and online
  checks pass, and candidates only follow a forwarded offer.

## Client options: ICE servers and TURN

**Server → Settings → Client options (WebRTC)** sets two RustDesk options on every device that
uses this panel as its API server. The panel sends them in the heartbeat response (the
`strategy` the client already understands) and each client applies them on its next heartbeat:

| field | RustDesk option | value |
|---|---|---|
| ICE servers | `ice-servers` | STUN/TURN URLs, one per line (sent comma-separated). Empty = RustDesk's built-in STUN list |
| WebRTC congestion control | `allow-webrtc-congestion-control` | client default / on / off |

- Nothing is pushed until you save once. From then on the panel owns both keys on all clients; an
  empty value removes the option on the client, which restores the client default.
- TURN entries need credentials in the URL form `turn://user:pass@host:port` (or `turns://`).
  RustDesk ignores TURN entries without credentials, so the panel rejects them. Percent-encode
  special characters in the password (`p@ss` → `p%40ss`).
- The list is encrypted at rest with the panel's secret key, and the admin audit log records only
  how many servers were set. Every client that receives the list can read the TURN credentials, so
  use a TURN user that is only for RustDesk, or time-limited credentials that you rotate.
- The panel UI provides one-click presets (**Global**, **RU / Anti-Censorship**, **Cloudflare**,
  **Default**) and a **Test servers** button to probe candidate ICE servers (UDP STUN Binding Request
  RFC 5389) and report latency before saving. Applying a preset keeps custom TURN servers intact.
- If a list has only TURN servers, RustDesk adds its default STUN servers in front.

## TURN server (coturn)

TURN is optional. It helps when both sides are behind symmetric NATs or UDP-hostile firewalls,
where ICE finds no direct pair. Without TURN, such sessions use the hbbr relay as usual. The
RD Panel image does not include a TURN server; run [coturn](https://github.com/coturn/coturn)
next to it:

```sh
docker run -d --name coturn --network host --restart unless-stopped \
  coturn/coturn \
  -n --log-file=stdout \
  --listening-port=3478 --fingerprint --lt-cred-mech \
  --realm=turn.example.com \
  --user=rustdesk:CHANGE-ME-long-random \
  --external-ip=203.0.113.10 \
  --min-port=49160 --max-port=49200 \
  --no-cli --no-multicast-peers \
  --denied-peer-ip=10.0.0.0-10.255.255.255 \
  --denied-peer-ip=172.16.0.0-172.31.255.255 \
  --denied-peer-ip=192.168.0.0-192.168.255.255
```

Open `3478/udp`, `3478/tcp` and the relay range `49160-49200/udp` on the TURN host. For TLS
(`turns:`, port 5349) add `--tls-listening-port=5349 --cert=… --pkey=…`. Then set the ICE servers
in the panel, for example:

```
stun:turn.example.com:3478
turn://rustdesk:CHANGE-ME-long-random@turn.example.com:3478
```

The `--denied-peer-ip` ranges stop the TURN server from being used to reach your private
networks; add your own internal ranges and drop the ones peers legitimately use.

## How the signaling flows

A is the controlling client, B the controlled one.

1. A sends `PunchHoleRequest` with `webrtc_sdp_offer` on its TCP or WebSocket rendezvous
   connection and keeps that connection open.
2. hbbs forwards the offer to B in `PunchHole.webrtc_sdp_offer` (with `force_relay`, which sets
   the answer's ICE policy), over B's UDP or WebSocket registration.
3. B answers on a new key-exchanged TCP connection: `PunchHoleSent.webrtc_sdp_answer`, or
   `RelayResponse.webrtc_sdp_answer` when it relays. hbbs delivers it to A in
   `PunchHoleResponse` / `RelayResponse` and keeps A's connection for the trickle.
4. Both sides trickle `IceCandidate` messages: B on one more TCP connection, addressed by A's
   address from `PunchHole`; A on its punch connection, addressed by B's id. hbbs forwards them
   only along the route created in step 2 (at most 256 per direction, for 60 s).
5. ICE and DTLS run between the peers (or via TURN). RustDesk binds the DTLS fingerprint to the
   peer's signed identity, so a rendezvous server cannot take over the channel.

So even clients registered over UDP talk to hbbs over TCP (port 21116) for WebRTC, and the
answer and candidates go only over an encrypted (key-exchanged) connection. hbbs details:
the fork's README, section `WEBRTC`.

## Checking that it works

- **Client:** the connection indicator in the session toolbar says
  *Direct and encrypted connection (WebRTC)*.
- **Client log** (desktop `%APPDATA%\RustDesk\log\RustDesk_rCURRENT.log`): the punch type
  contains `WebRTC`, for example `#1 UDP+TCP+WebRTC punch attempt`, and later
  `used to establish WebRTC connection`.
- **hbbs:** every WebRTC session ends with one INFO line in **Server → Logs**:
  `WebRTC session <controller> -> <id>: … offers forwarded, answer: …, ICE to peer: n, ICE to controller: m`.
  With `RUST_LOG=info,hbbs=debug` each offer, answer and candidate is logged (sizes and counts
  only, never SDP contents).
- **Server → Console**: `webrtc` prints `WEBRTC: true|false`.

## Troubleshooting: the client never tries WebRTC

If the client log shows `#1 TCP punch attempt` (no `WebRTC` in the list), the client did not
create an offer at all. Check, on the **controlling** device:

| cause | fix |
|---|---|
| "Enable WebRTC P2P connection" is off (default for self-hosted servers) | Settings → General → Other (desktop) or Settings (mobile) |
| **"Always connect via relay"** is on for that peer (`force-always-relay`) | uncheck it in the peer's menu (⋮ / long tap). With it on, WebRTC is only allowed through a TURN server from `ice-servers` |
| A SOCKS5/HTTP proxy is configured in the client | the client never offers WebRTC through a proxy |
| A TUN VPN on the device (e.g. own address `198.18.0.1`) | ICE may only see the tunnel address; exclude RustDesk from the VPN |

Peers behind the same public IP (one LAN) get the offer too since fork `d74a26a`; the classic
LAN path (`FetchLocalAddr`) is kept for the parallel offer-less request.

## Limitations

- Behind a reverse proxy, hbbs identifies WebSocket clients by `X-Real-IP` with port 0. Two
  controllers behind the same public IP that connect over WebSocket at the same moment share one
  punch route (the same limit applies to the classic punch).
- The panel cannot turn on `enable-webrtc` for clients (see above).
- Per-device or per-group client options are not supported; the options are server-wide.
