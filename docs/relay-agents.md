# Relays and relay agents

RD Panel decides which relay servers (hbbr) hbbs hands out to RustDesk clients.
Relays are managed on **Server → Relays**:

| Kind | What it is | What the panel can do |
|---|---|---|
| **local** | the hbbr in the same container as hbbs and the panel | probe it, weight it, drain it; limits and lists come from Server → Settings |
| **agent** | an hbbr on another host, run by `rdpanel agent` | everything: probe, weight, drain, per-relay limits and lists, live sessions, status, revoke |
| **static** | any other hbbr (third party, legacy, unmanaged) | probe it, weight it, drain it |

Design background and the reasoning behind the choices below:
[`design/relay-agents.md`](design/relay-agents.md).

## How the relay list is built

hbbs' relay list (`relay-servers`, admin command `rs`) is **computed** from the
Relays tab; the free-text "Relay servers" field on Server → Settings is read-only
and shows the result.

- A relay is in the list when it is **enabled**, **in rotation**, has
  **weight > 0** and its health is **active** or **degraded**.
- **Weights** (0-5) are duplicates: hbbs picks a relay for every new session
  round-robin over the list and keeps repeated entries, so `fra-1 ×3, hel-2`
  sends 75 % of new sessions to `fra-1`. Entries are interleaved
  (`a,a,b,a`). hbbs reads one admin command with a single 1 KiB read: when the
  list does not fit, weights shrink proportionally (shown as "weights reduced").
- hbbs never gets an empty list: when nothing is healthy, the local relay is
  used anyway ("fallback"). Without any relay at all (no local relay, nothing
  added) the panel leaves hbbs' own `-r` / `RELAY` list alone.
- The list is pushed when it changes and re-sent whenever hbbs restarts (the
  reconciler's restart detection). Running sessions are never touched: draining
  or removing a relay only affects new sessions.
- Selection is weighted round-robin only. hbbs ignores client IPs, so there is
  no geo or latency based choice (see the design doc, phase 3).

On upgrade, migration `00010` turns the existing Server → Settings relay list
into static relays (they start as active) and the local relay is created from
`RDPANEL_HBBS_RELAY_PUBLIC_HOST`, or else from the first entry of `RELAY`. A
static relay with the same host becomes the local relay.

## Health

Every relay except a disabled one (and an agent that is not approved yet) is
probed **end to end** every 60 s: the panel opens two TCP connections to its
public host, sends `RequestRelay` with the same random UUID and the **hbbs
public key** on both, and 32 random bytes written on one must arrive on the
other. This proves DNS, reachability from outside, the relay key and data flow,
and measures the round trip. hbbs' own check only connects to the port.

| State | Meaning | In the list? |
|---|---|---|
| pending | not probed yet, or an agent that has not enrolled / is not approved | no |
| active | probe passes (agent: also connected and its config acknowledged) | yes |
| degraded | probe passes but the agent session is down or its config lags more than 5 minutes; or 1-2 failed probes | yes |
| offline | 3 failed probes in a row | no |

The local relay is never taken out while its loopback admin channel answers
(probing your own public address can fail behind hairpin NAT): it is degraded at
worst. A local relay whose public host is a loopback address is not probed; its
health follows the admin channel.

Probe errors you may see:

| Error | Usual cause |
|---|---|
| `relay closed the connection before relaying data (wrong key...)` | the relay runs with another key (`-k`) than this hbbs: every client would fail |
| `timeout: no data relayed` | relay blackholes, the panel's IP is in its blocklist, or it is not an hbbr |
| `connect ...: connection refused / i/o timeout` | firewall, wrong port, host down |
| `relay host resolves to a private ... address` | SSRF guard; enable "Allow private relay hosts" for LAN relays |

## Add a relay agent

The agent runs in the **same image** as the panel with `RDPANEL_MODE=agent`:
s6 then runs only `rdpanel agent`, which starts hbbr as its child with
`-k <hbbs public key>`. The private key never leaves the panel host. The agent
dials **out** to the panel over WebSocket; the relay host needs no inbound
management port, only the relay port (TCP 21117) for clients.

1. **Panel:** Server → Relays → *Add relay* → *Relay agent*: name, public host
   (`relay-de.example.com[:21117]` as clients will reach it), region, weight.
   The dialog shows a **one-time enrollment token** (single use, valid 1 hour,
   stored only as a sha256) inside ready-to-copy commands, and follows the agent
   live: enrolled → approved → connected → probe passed.
2. **Relay host** (Linux with Docker and the Compose plugin), as root:

   ```sh
   curl -fsSL https://desk-api.example.com/agent/install.sh | sudo sh -s -- --token <TOKEN>
   ```

   That's it. The installer writes `/opt/rdpanel-agent/compose.yml` (host
   networking, the panel's image version), puts the token into
   `/opt/rdpanel-agent/agent.env` (mode 600, not on a command line), keeps the
   agent identity in `/opt/rdpanel-agent/data`, runs `docker compose up -d` and
   waits until the container is healthy.
3. **Panel:** *Approve* the relay (Relays tab, row menu). New agents need an
   admin's approval by default ("Approve new agents" on the Relays tab; turn it
   off only if every relay host is yours and tokens never leave your hands).
   Only an approved agent gets its configuration and starts hbbr. It joins the
   relay list as soon as the probe passes.

Installer options (`sh -s -- --help`):

| Option | Default | |
|---|---|---|
| `--token TOKEN` | — | enrollment token; needed for the first install and after *New enrollment token* |
| `--panel URL` | the panel's `RDPANEL_PUBLIC_URL` | panel public URL |
| `--dir DIR` | `/opt/rdpanel-agent` | install directory |
| `--image IMAGE` | `ghcr.io/bropines/rdpanel:<panel version>` (`latest` for dev builds) | container image |
| `--port PORT` | `21117` | relay port (`RDPANEL_AGENT_RELAY_PORT`) |
| `--name NAME` | host name | name reported to the panel (`RDPANEL_AGENT_HOSTNAME`) |
| `--install-docker` | off | install Docker from the official `https://get.docker.com` script when it is missing (otherwise the installer only says how) |
| `--uninstall [--purge]` | | stop and remove the container; `--purge` also deletes the directory and the agent identity |

Re-running the one-liner (the token may be left out) updates the image and
keeps the identity, port and name. The panel fills in the defaults when it
serves the script; the script itself is static and contains no secrets.

**Without the installer**, the dialog's other tabs give the same setup as a
plain `docker run` command or as a compose file (see also
[`deploy/agent/compose.example.yml`](../deploy/agent/compose.example.yml)):

```yaml
services:
  relay:
    image: ghcr.io/bropines/rdpanel:latest
    network_mode: host        # hbbr must see real client IPs
    restart: unless-stopped
    environment:
      RDPANEL_MODE: agent
      RDPANEL_AGENT_PANEL_URL: https://desk-api.example.com
      RDPANEL_AGENT_ENROLL_TOKEN: "<one-time token>"
    volumes:
      - ./relay-data:/data
```

The agent creates its identity key (`/data/agent.key`), enrolls, and connects.
After enrollment the token is no longer used, so it can stay in the file.

### Reverse proxy: expose `/agent/v1/` and `/agent/install.sh` on the public host

Agents talk to `<RDPANEL_AGENT_PANEL_URL>/agent/v1/enroll` (POST) and
`/agent/v1/connect` (WebSocket); the installer is downloaded from
`/agent/install.sh`. Use the **public client-API host** (the one in
`RDPANEL_PUBLIC_URL`); the admin host can stay internal. Next to the
`location /api/` block of [docs/deploy.md](deploy.md#split-hosts-public-client-api--internal-admin-panel)
(nginx and Angie):

```nginx
# Public host: relay agents (enrollment + WebSocket session). No cookies, no
# CSRF; authenticated by one-time tokens and ed25519 signatures, rate limited.
location ^~ /agent/v1/ {
    include rdpanel-proxy.conf;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 1h;                 # long-lived session (pinged every 30 s)
}
# The relay agent installer (static script, no secrets).
location = /agent/install.sh {
    include rdpanel-proxy.conf;
}
```

The panel trusts `X-Forwarded-For` only from `RDPANEL_TRUSTED_PROXIES`, so the
enrolling IP shown on the Relays tab and the per-IP rate limit see real agent
addresses.

### Agent configuration

| Variable | Default | Meaning |
|---|---|---|
| `RDPANEL_MODE` | `panel` | `agent` = relay host mode of the image (s6 runs only the agent; hbbs, hbbr and the panel services idle) |
| `RDPANEL_AGENT_PANEL_URL` | — | panel public URL, e.g. `https://desk-api.example.com` (required for the first start; then stored in `agent-state.json`) |
| `RDPANEL_AGENT_ENROLL_TOKEN` | — | one-time token from the panel; ignored once enrolled |
| `RDPANEL_AGENT_RELAY_PORT` | `21117` | hbbr port (`hbbr -p`); its loopback admin channel is the same port, its WebSocket port +2 |
| `RDPANEL_AGENT_DATA_DIR` | `/data` | `agent.key`, `agent-state.json`, `agent-health.json`, `blacklist.txt`, `blocklist.txt` |
| `RDPANEL_AGENT_HBBR_BIN` | `/usr/bin/hbbr` (image) | hbbr executable |
| `RDPANEL_AGENT_PANEL_CA` | — | PEM file with extra CAs for a panel certificate that is not publicly trusted |
| `RDPANEL_AGENT_HOSTNAME` | host name | name reported to the panel |
| `RDPANEL_AGENT_STATUS_INTERVAL` | `10s` | status report period |
| `RDPANEL_LOG_LEVEL`, `RDPANEL_LOG_FORMAT` | `info`, `json` | agent logs; hbbr's output is logged with `source=hbbr` |

Never set `PORT` (hbbr would listen on `PORT+1`); `KEY` is not passed to hbbr.
Outside Docker, run `rdpanel agent` next to an hbbr binary the same way; the
agent is hbbr's parent and restarts it with backoff (1 s up to 30 s).

`rdpanel agent health` (the image's healthcheck in agent mode) checks that the
agent is running and hbbr is up whenever it should be.

### What the agent does

- Keeps an outbound WebSocket session to the panel (reconnects with jittered
  backoff from 1 s to 60 s). Each session starts with an **ed25519 challenge**:
  the panel sends a fresh 32-byte nonce, the agent signs it with its key
  (registered at enrollment) and the current time (accepted window ±60 s). A
  captured signature is useless on another connection.
- Persists the configuration it gets (`agent-state.json`) and keeps enforcing it
  when the panel is unreachable, also across restarts.
- Starts hbbr with `-k <hbbs public key>` (never `-k _`, which would make hbbr
  generate a foreign key pair on a fresh host), applies limits and lists through
  hbbr's loopback admin channel (the same code as the panel's reconciler), writes
  `blacklist.txt` / `blocklist.txt` so hbbr has them on restart, re-applies after
  every hbbr restart and corrects drift every 15 s, then acknowledges the config
  version (per-key errors are shown in the relay drawer).
- Reports status every 10 s: hbbr up / restarts / key fingerprint, active
  sessions and throughput from hbbr `usage`, host load and NIC rates from
  `/proc`.
- Answers two allow-listed commands: live `usage` (relay drawer) and
  `restart-hbbr`.

## Limits and lists

The **global relay profile** is the hbbr part of Server → Settings (bandwidth
limits, downgrade settings, blacklist and blocklist). It applies to the local
relay and to every agent. In the relay drawer of an agent, *Limits and lists*
overrides single keys for that relay (empty = inherit). Every change of the
effective configuration bumps the agent's config version; the drawer shows
`applied / desired`.

Static relays cannot be configured: their admin channel only answers loopback.

## Day-to-day operations

| Action | Effect |
|---|---|
| Weight 0 / *In rotation* off | out of hbbs' list, still probed |
| *Drain* | out of hbbs' list, running sessions continue; *Enable* brings it back |
| *Disable* | out of the list and, for an agent, hbbr is stopped |
| *Probe now* | immediate end-to-end probe |
| *New enrollment token* | the agent's key stops working and it is disconnected; re-run the installer with `--token <new>` (or put the new token into `RDPANEL_AGENT_ENROLL_TOKEN` and restart the agent): it re-enrolls by itself (its old identity is dropped when the panel refuses it) |
| *Revoke agent* | the key stops working, the session is closed and the agent stops hbbr; a new token re-enables the relay |
| Delete | removes a static or agent relay; the local relay can only be disabled |
| hbbs key rotation | the panel sends the new public key; agents restart hbbr with it (short relay outage) |

Every action is in Audit → Admin; agent events (`relay.agent.enroll`,
`relay.agent.connect`, `relay.agent.superseded`, `relay.agent.config_error`) are
recorded with the actor `agent:<id>`.

## Security notes

- **Relays are public bandwidth.** hbbr checks only the hbbs *public* key, which
  every client knows; anybody who has it can use any of your relays, exactly like
  the local one. Use bandwidth limits and blocklists. (Relay-side
  authentication would need client support; see the design doc.)
- Agents get no secrets: no hbbs private key, no JWT key, no admin session. An
  agent identity can only use `/agent/v1/*` for its own relay.
- Enrollment tokens are single use, expire after 1 hour and are stored as sha256.
  Enrollment and session attempts are rate limited per IP and back off after
  failures.
- The probe refuses private, loopback and link-local targets unless *Allow
  private relay hosts* is on (SSRF guard). The local relay is exempt.
- Run agents with **host networking** (`network_mode: host`, or Kubernetes
  `hostNetwork`): otherwise hbbr sees the Docker proxy's address, and lists and
  usage are useless. Never forward the relay port from a loopback address: hbbr
  treats loopback peers as its admin console.

## Metrics

Labelled by relay name (bounded by what the admin configures):

| Metric | Meaning |
|---|---|
| `rdpanel_relay_up{relay,kind}` | last end-to-end probe passed |
| `rdpanel_relay_health{relay,state}` | 1 for the current health state |
| `rdpanel_relay_probe_seconds{relay}` | data round trip of the last good probe |
| `rdpanel_relay_probes_total{relay,result}` | probes by result |
| `rdpanel_relay_rotation_weight{relay}` | entries in hbbs' list (0 = out) |
| `rdpanel_relay_active_sessions{relay}` | sessions reported by an agent |
| `rdpanel_relay_config_lag{relay}` | desired minus acknowledged config version |
| `rdpanel_relay_agents_connected`, `rdpanel_relay_rotation_entries` | totals |
| `rdpanel_relay_enroll_failures_total`, `rdpanel_relay_agent_auth_failures_total` | rejected enrollments / sessions |

## Clients with their own relay setting

A RustDesk client whose network settings name a "Relay server" is believed to
use it instead of the relay hbbs assigns. The computed rotation then only covers
clients that leave the field empty (or use the panel's provisioning). This is
not enforced by the panel.

## Not yet available

- WebSocket relay (port 21119) on agents: hbbr listens on it, but it needs a TLS
  reverse proxy that overwrites `X-Real-IP` on every relay host; not set up or
  managed by the panel.
- Traffic history (hourly per-relay bytes and sessions), charts and traffic
  alerts; exact byte counters need an hbbr `stats` patch.
- A Helm chart for agents (use the compose file; one replica per relay, host
  networking).
- Geo or latency based relay selection; panel federation.
