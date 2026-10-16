# hbbs/hbbr integration research (for the Go panel)

Source analysed: `F:\bropi\Projects\rustdesk-server`, branch `merge-upstream-1.1.16`, HEAD `8a2a9fc`
("Merge upstream rustdesk-server 1.1.16 into forapi"), submodule `libs/hbb_common` @ `d6b1497`.
All `file:line` references are relative to that repo unless prefixed otherwise.

Other sources (read-only, for cross-checking):

- `lejianwen/rustdesk-api` @ `c5687e1` (2025-09-29), referred to as `api:<path>:<line>`.
- Upstream RustDesk client `rustdesk/rustdesk@master`, referred to as `client:<path>:<line>`.

---

## 1. Admin command channel

### 1.1 Where it listens

| Process | Admin port | How it is chosen | Code |
|---|---|---|---|
| hbbs | **rendezvous port − 1** (default **21115**, the "NAT test" port) | `nat_port = port - 1` | `src/rendezvous_server.rs:151`, `:203`, accept loop `:349-360` |
| hbbr | **the relay TCP port itself** (default **21117**), only on the non-WebSocket listener | the `!ws && ip.is_loopback()` branch | `src/relay_server.rs:366-380` |

There is no separate admin port. The same public port carries both protocols, and the only thing that separates them is a **check on the peer IP**:

- hbbs `handle_listener2` (`src/rendezvous_server.rs:1326-1364`) runs `try_into_v4(addr).ip().is_loopback()` (`:1328-1329`). If the peer is loopback, the connection is an admin session. Otherwise it is a framed protobuf session that accepts only `TestNatRequest` and `OnlineRequest` (`:1342-1363`).
- hbbr `handle_connection` (`src/relay_server.rs:359-391`) applies the same test, but only when `!ws` (`:367`). A loopback connection to the WS relay port 21119 is a normal relay connection.
- `try_into_v4` (`libs/hbb_common/src/lib.rs:117-128`) turns `::ffff:127.0.0.1` into `127.0.0.1`. Both `127.0.0.0/8` and `::1` therefore count as loopback. Listeners are dual-stack `[::]:port` with `SO_REUSEPORT` (`libs/hbb_common/src/tcp.rs:226-251`).
- hbbs WS port 21118 (`listener3`, `src/rendezvous_server.rs:361-372`) has **no** admin branch.

### 1.2 Wire protocol (both processes, identical)

hbbs: `src/rendezvous_server.rs:1330-1339`. hbbr: `src/relay_server.rs:369-378`.

```
client connects → server does ONE read() into a 1024-byte buffer, 1000 ms timeout
               → buffer must be valid UTF-8 (else silently nothing)
               → cmd.trim().split(' ')   (single-space split; double spaces create empty tokens)
               → response = check_cmd(...)  (String, may be empty)
               → ONE stream.write(response) (not write_all), then the socket is dropped (FIN)
```

- **No framing and no auth.** It is plain text. The request is "whatever arrives in the first read". Send the whole command in one `Write`, keep it under 1024 bytes and send it within 1 s of connecting. No newline is needed; `trim()` strips one if present.
- **Response.** Newline-terminated lines, followed by EOF. Setters and unknown commands return an **empty** response, so there is no error and no acknowledgement. The client should read until EOF with a deadline.
- lejianwen's client (`api:service/serverCmd.go:104-148`) writes `cmd + " " + arg`, sleeps 100 ms and does a **single 1024-byte `Read`**. Long outputs (`usage`, `b`, `B`, `ib`, `pr`) can be truncated. It tries `[::1]:port` first, then `127.0.0.1:port`.

### 1.3 hbbs commands (`check_cmd`, `src/rendezvous_server.rs:1149-1324`)

| Command (alias) | Args | Effect / exact response | Lines |
|---|---|---|---|
| `h` | none | Help text, 8 lines: `relay-servers(rs) <separated by ,>`, `reload-geo(rg)`, `ip-blocker(ib) [<ip>\|<number>] [-]`, `ip-changes(ic) [<id>\|<number>] [-]`, `punch-requests(pr) [<number>] [-]`, `always-use-relay(aur) [Y\|N]`, `test-geo(tg) <ip1> <ip2>`, `must-login(ml) [Y\|N]`, each ending in `\n`. **`rg` is listed but not implemented** (falls to `_ => {}`, so the response is empty). | 1155-1167 |
| `relay-servers` (`rs`) | `[host[:port],host2,...]` | With an arg, sends `Data::RelayServers0` to the io loop, where `parse_relay_servers` checks every entry by DNS (`common.rs:22-47`) and silently drops invalid ones. The response is empty and the change is asynchronous. Without an arg, the response is the current list, one host per line (this list may already be filtered by `check_relay_servers` when there is more than one). | 1168-1176, 1133-1137, 1543-1568 |
| `ip-blocker` (`ib`) | `[<ip>\|<start>] [-]` | First prunes stale entries. Line 1 is the entry count. With `<ip>`: `"{a}/{t1}s {n}/{t2}s"`, where a = RegisterPk attempts in the 60 s window, n = distinct IDs registered from that IP in 24 h. With trailing `-`, the IP is removed (unblocked). Without an arg or with a number: up to 10 lines `"{ip}: {a}/{t1}s {n}/{t2}s"` starting at offset. An IP is blocked (`TOO_FREQUENT`) when it makes >30 RegisterPk calls in 60 s, or after more than 300 distinct IDs per day for new IDs (`:1103-1131`). | 1177-1228 |
| `ip-changes` (`ic`) | `[<id>\|<start>] [-]` | Line 1 is the count. With an ID: `"{secs}s {ip→count map:?}"`, and `-` removes it. A paged list prints `"{id}: {secs}s {map:?}"`. **Gotcha:** an argument that parses as an int in `0..=10_000_000` is treated as a page offset, not an ID, so IDs of 8 digits or fewer that are ≤ 10,000,000 cannot be queried. | 1229-1262 |
| `punch-requests` (`pr`) | `[start] [page_size]` or `-` | `-` clears the list. Otherwise prints `"{RFC3339 UTC} {from_ip} -> {to_id}@{to_ip}"` lines (default page size 10). **The `PUNCH_REQS` Vec is never pruned** (`:113`, `:946-959`) and grows until `pr -`. The panel should page through it periodically, store the rows, then clear it. | 1263-1280 |
| `always-use-relay` (`aur`) | `[Y\|N]` | Sets `ALWAYS_USE_RELAY` (case-insensitive Y; anything else counts as N). Without an arg: `"ALWAYS_USE_RELAY: true\n"` or `false`. **Bug (also upstream):** line 1288 also sends `Data::RelayServers0("Y"/"N")`, which re-parses the relay list from the string `"Y"`. That fails DNS and **wipes the relay server list**. Always re-send `rs <list>` after `aur`. | 1281-1296 |
| `test-geo` (`tg`) | `<ip1> [<ip2>]` | `"{:?}"` of `get_relay_server(a,b)`, a quoted string with **no trailing newline**. This fork has no geo, so it is a round-robin pick. **Side effect:** it advances the rotation counter. | 1297-1309, 1139-1147 |
| `must-login` (`ml`) | `[Y\|N]` | Toggles `MUST_LOGIN` at runtime. Without an arg: `"MUST_LOGIN: true\n"` or `false`. | 1310-1320 |

Everything is **in-memory only**. hbbs persists none of these settings, so a restart reverts to CLI/env values.

### 1.4 hbbr commands (`check_cmd`, `src/relay_server.rs:152-324`)

| Command (alias) | Args | Effect / response | Lines |
|---|---|---|---|
| `h` | none | 12 help lines | 158-174 |
| `blacklist-add` (`ba`) | `ip1\|ip2\|...` | Adds to BLACKLIST. Blacklisted IPs are **throttled** to `LIMIT_SPEED` (checked at relay start and every ≥1 s, `:539`). Empty response. | 175-181 |
| `blacklist-remove` (`br`) | `ip1\|ip2` or `all` | Removes the IPs, or clears with `all`. Empty response. | 182-192 |
| `blacklist` (`b`) | `[ip]` | With an IP: `"true\n"` or `"false\n"`. Without: one IP per line. | 193-201 |
| `blocklist-add` (`Ba`) | `ip1\|ip2` | Blocked IPs are **refused** at connect (`:382-385`, logs `"{ip} blocked"`) and **cut** from running relays within about 1 s (`:535-538`). | 202-208 |
| `blocklist-remove` (`Br`) | `ip...` or `all` | Same as `br`. | 209-219 |
| `blocklist` (`B`) | `[ip]` | Same as `b`. | 220-228 |
| `downgrade-threshold` (`dt`) | `[float>0]` | Without an arg: `"0.66\n"`. Applies to **new** relays (read at `:482`). | 229-242 |
| `downgrade-start-check` (`t`) | `[secs>0]` | Without an arg: `"1800s\n"`. Applies live (read every tick, `:551`). | 243-253 |
| `limit-speed` (`ls`) | `[Mb/s float]` | Speed for blacklisted or downgraded relays. Without an arg: `"32Mb/s\n"`. New relays only (`:481`). | 254-267 |
| `total-bandwidth` (`tb`) | `[Mb/s]` | Global limiter. Applies **immediately** (`limiter.set_speed_limit`, `:273`). Without an arg: `"1024Mb/s\n"`. | 268-282 |
| `single-bandwidth` (`sb`) | `[Mb/s]` | Per-relay limit. New relays only (`:479`). Without an arg: `"128Mb/s\n"`. | 283-296 |
| `usage` (`u`) | none | One line per active relay, sorted by total bytes descending: `"{ip}:{port}: {elapsed_s}s {total:.2}MB {highest}kb/s {avg}kb/s {current}kb/s"`. | 297-320 |

- "Mb/s" is really Mibit/s (`v * 1024 * 1024` bits, `:258`). Values `<= 0` or that fail to parse are silently ignored.
- Defaults are at `:40-44`, and env overrides are read in `check_params` (`:98-150`): `DOWNGRADE_THRESHOLD`, `DOWNGRADE_START_CHECK`, `LIMIT_SPEED`, `TOTAL_BANDWIDTH`, `SINGLE_BANDWIDTH`.
- Black/blocklists are loaded **once at startup** from `blacklist.txt` / `blocklist.txt` in the CWD (`:45-46`, `:51-80`). The format is one entry per line, first space-separated token. `/data` is the CWD in the s6 image (`docker/rootfs/etc/s6-overlay/s6-rc.d/hbbr/run:2`). Runtime `ba`/`Ba` changes are **not written back**.

### 1.5 Docker networking and the loopback-only check

| Panel placement | What hbbs/hbbr see as the source IP | Admin works? |
|---|---|---|
| Same container (s6 image) | 127.0.0.1 / ::1 | **Yes** |
| Separate container with `network_mode: "service:<hbbs-container>"` (shared netns) | 127.0.0.1 | **Yes** |
| Both containers `network_mode: host` | 127.0.0.1 | **Yes**, but so does every other host-network process. |
| Separate container on a bridge network calling `hbbs:21115` | the panel container IP (172.x) | **No.** hbbs treats it as a NAT-test/Online request and ignores text. hbbr waits 30 s for a protobuf `RequestRelay` (`src/relay_server.rs:427`) and drops it. |
| Bridge container calling the host's published `127.0.0.1:21115` | the docker gateway IP, through docker-proxy or MASQUERADE | **No** |

There is **no way to reach the admin channel from a bridge-network container without putting something inside the hbbs/hbbr network namespace.** The options are:

1. Same container (s6), or a shared netns (`network_mode: service:`). **Recommended.**
2. A small forwarder inside the hbbs container that listens on a **unix socket on a shared volume** and proxies to `127.0.0.1:21115` / `:21117` (for example `socat UNIX-LISTEN:/run/hbb/admin-hbbs.sock,mode=660,fork TCP:127.0.0.1:21115`). File permissions then act as the auth. Do **not** use a TCP forwarder bound to the bridge IP, because it turns the channel into an unauthenticated network service.

**Security warning for the deployment.** Because loopback is the only guard, anything on the same host or netns that turns remote traffic into local connections becomes a remote admin console. Examples are an nginx `stream {}` or haproxy or Caddy-L4 TCP proxy, frp, cloudflared or ssh `-L` forwarding to `127.0.0.1:21115` or `127.0.0.1:21117`. WebSocket reverse proxies must point at **21118/21119**, never at 21115/21117. Any process or user on a `network_mode: host` machine has full admin access.

### 1.6 How lejianwen/rustdesk-api does it

- Model `ServerCmd{Cmd, Alias, Option, Explain, Target}`, with `Target` either `"21115"` (`ServerCmdTargetIdServer`) or `"21117"` (`ServerCmdTargetRelayServer`). It ships built-in lists `SysIdServerCmds` / `SysRelayServerCmds`: `api:model/serverCmd.go:3-61`. These are labels, **not ports**.
- The port mapping is `api:http/controller/admin/rustdesk.go:123-129`. ID server: `Admin.IdServerPort - 1`, default 21116 − 1 = 21115. Relay: `Admin.RelayServerPort`, default 21117 (`api:config/config.go:31-32,53-57`).
- `SendCmd(port, cmd, arg)` sends `cmd+" "+arg` to `[::1]` and then `127.0.0.1` (`api:service/serverCmd.go:104-148`). Any admin can send **any** cmd and option string; there is no arg validation.
- That is why lejianwen's compose runs everything in **one s6 container** (`docker-compose.yml`, `docker/Dockerfile:1` is `FROM lejianwen/rustdesk-api:latest`).

### 1.7 Proposed design for our panel

- **Deployment:** the panel runs inside the same s6 container (our image, §5). A shared netns (`network_mode: service:rustdesk`) is the alternative for split images.
- **Go package `hbbadmin`:**
  - `Exec(ctx, target, cmd string, args ...string) (string, error)`: dial `127.0.0.1:<port>` (fallback `[::1]`), one `Write` of `strings.Join(append([]string{cmd}, args...), " ")`, `CloseWrite` is optional, then `io.ReadAll` with a 2–3 s deadline and a 64 KiB cap. Hold a per-target mutex to serialize calls.
  - Admin port = configured hbbs port − 1. Relay admin port = hbbr port. Make both configurable, never hardcoded.
  - **Typed API, no raw passthrough:**
    - Commands are allowlisted (full names, not the case-sensitive aliases `b`/`B`).
    - Args are validated: `net.ParseIP` / `netip.ParsePrefix` for IPs; `strconv.ParseFloat > 0` for speeds; `^[A-Za-z0-9_-]{6,100}$` for IDs; host:port with no spaces for relay servers.
    - Values never contain spaces, and `|` is built by us. Keep the total under 1 KiB, splitting batches of IPs into multiple calls.
    - An optional "raw console" can be offered for superadmins only, and it should be audited.
  - **Read-back verification:** setters return nothing, so after `ls 10` call `ls` and compare. After `aur Y|N` always re-send `rs <list>` (the bug in §1.3).
- **Desired-state reconciliation:** store the relay servers, `aur`, `ml`, bandwidths, blacklist and blocklist in the panel DB. Re-apply them on panel start and every ~60 s, or when drift is detected. Also write `/data/blacklist.txt` and `/data/blocklist.txt` atomically so hbbr picks them up after a restart.
- **Housekeeping:** every N minutes run `pr 0 1000` → store the rows → `pr -`. This collects connection-attempt history, which hbbs does not keep, and bounds hbbs memory.
- **Audit log:** record admin user, target, command, args, response hash and time for every call.

---

## 2. Online status

### 2.1 What hbbs knows

- **In-memory `Peer`** (`src/peer.rs:32-42`):
  - `socket_addr` is the last UDP/TCP source.
  - `last_reg_time: Instant` is monotonic, not wall-clock.
  - `guid`, `uuid`, `pk`.
  - `info.ip` (`PeerInfo`, `:26-30`).
  - `reg_pk` is a rate counter.
- **Online rule:** `last_reg_time.elapsed() < REG_TIMEOUT = 30_000 ms` (`src/rendezvous_server.rs:59`, `:668-673`, `:932-943`). The fork uses `i64` here, with the upstream #676 overflow fix.
- **Refreshing `last_reg_time`:**
  - UDP/TCP `RegisterPeer` refreshes it only if the pk is known and the IP is unchanged (`update_addr`, `:779-816`, specifically `:789-792`).
  - `RegisterPk` → `update_pk` (`src/peer.rs:93-133`, `:108`) also refreshes it. Over WS this happens on every RegisterPk (`:765-768`, the fork comment says it exists to fix the online time on TCP).
- **Map behaviour:** the map (`PeerMap.map`, `src/peer.rs:62-66`) is filled lazily from SQLite on `get()` (`:136-155`) and never evicted. `peers_online_state` uses `get_in_memory` (`src/rendezvous_server.rs:667`), so a peer that has not registered since the hbbs start counts as offline.
- **Client cadence:** `REG_INTERVAL = 15_000 ms` (`libs/hbb_common/src/config.rs:38`; `client:src/rendezvous_mediator.rs:448,637`).

### 2.2 SQLite `db_v2.sqlite3`

- **Path:** `DB_URL` env, else `./db_v2.sqlite3` in the CWD (`src/peer.rs:70-84`), which is `/data/db_v2.sqlite3` in the s6 image.
- **Pool:** `MAX_DATABASE_CONNECTIONS` (default 1) (`src/database.rs:54-57`). Default journal mode, not WAL.

Schema (`src/database.rs:72-89`):

```sql
create table if not exists peer (
    guid blob primary key not null,   -- 16-byte UUIDv4, generated by hbbs (database.rs:113)
    id varchar(100) not null,         -- RustDesk ID (unique index index_peer_id)
    uuid blob not null,               -- device uuid from RegisterPk
    pk blob not null,                 -- device ed25519 public key (32 bytes)
    created_at datetime not null default(current_timestamp),  -- first registration (UTC)
    user blob,                        -- never written by hbbs
    status tinyint,                   -- read but ignored ("disabled" commented out, peer.rs:147)
    note varchar(300),                -- never written/read by hbbs
    info text not null                -- JSON {"ip":"<last ip>"}
) without rowid;
-- indexes: index_peer_id (unique), index_peer_user, index_peer_created_at, index_peer_status
```

- hbbs writes only on insert, or when the pk, uuid or IP changes (`update_pk` → `insert_peer` / `update_pk`, `src/database.rs:106-144`). **There is no last-seen column**, so the DB cannot give online status.
- The panel may open it **read-only** (`file:/data/db_v2.sqlite3?mode=ro`, busy_timeout ~5 s) to list known IDs, `created_at`, last IP and pk.
- **Never write to it.** hbbs caches `guid`; a deleted row stays cached and later `UPDATE ... where guid=?` becomes a silent no-op.

### 2.3 `OnlineRequest`: can the panel ask hbbs?

**Yes.** It is unauthenticated, and **no licence key or token is checked**.

Proto (`libs/hbb_common/protos/rendezvous.proto:175-182`, envelope `:192-216`):

```proto
message OnlineRequest  { string id = 1; repeated string peers = 2; }   // id is ignored by hbbs
message OnlineResponse { bytes states = 1; }
// RendezvousMessage.oneof: online_request = 23, online_response = 24, key_exchange = 25
```

- **Bitmap:** `states` has `ceil(n/8)` bytes. Peer `i` is online when `states[i/8] & (0x80 >> (i%8))` is set, i.e. MSB-first (`src/rendezvous_server.rs:664-678`).
- **Framing (TCP):** `hbb_common::bytes_codec` (`libs/hbb_common/src/bytes_codec.rs`), a little-endian variable header. If `len <= 0x3F`, the header is one byte, `len<<2`. If `<= 0x3FFF`, it is a u16le, `(len<<2)|1`. If `<= 0x3FFFFF`, it is 3 bytes, `(len<<2)|2`. Otherwise it is a u32le, `(len<<2)|3`. The low 2 bits of byte 0 give the header length minus 1.
- **Hand-encoding** without protoc, via `google.golang.org/protobuf/encoding/protowire`:
  - Outer tag for field 23, length-delimited: `0xBA 0x01`. Inner `peers` (field 2) tag is `0x12`.
  - Response: outer `0xC2 0x01` (field 24), inner `states` tag `0x0A`.
  - KeyExchange outer tag (field 25): `0xCA 0x01`.

Where it is accepted:

| Endpoint | Path | Notes |
|---|---|---|
| TCP **21116** (`handle_listener` → `handle_tcp`, `:649-657`) | Works from loopback too. | hbbs **first sends an unsolicited `KeyExchange`** whenever a server key exists (`:1420-1423`, `:1517-1540`). That is always the case: the default `-k -` / docker `-k _` generate one (`src/main.rs:36`, `:1485-1491`). The client may ignore it and send a plaintext `OnlineRequest`, because encryption is only enabled once the client sends its own KeyExchange (`:615-647`). Read frames, skipping `KeyExchange`, until `OnlineResponse`. hbbs then closes, because `handle_tcp` returns `false`. |
| TCP **21115** (`handle_listener2`, `:1356-1358`) | **Only from non-loopback.** From loopback, 21115 is the text admin channel. | No KeyExchange, 30 s read timeout. Usable from a bridge-network container. |
| WS **21118** | Binary WS message | Same as 21116, minus the KeyExchange. |

**Panel recipe (same container):** dial `127.0.0.1:21116`, send one framed `RendezvousMessage{online_request{peers:[...all IDs...]}}`, then read frames, skipping KeyExchange, until `OnlineResponse` (2 s deadline). One round trip covers thousands of IDs. The cost on the hbbs side is a read-lock per ID.

Side note: anyone on the internet can do this for any ID list. It is upstream behaviour and an ID-enumeration oracle.

### 2.4 Client heartbeat to the API server

`client:src/hbbs_http/sync.rs`:

- The loop ticks every **3 s** (`TIME_CONN`, `:20`, `:88-97`). It POSTs `{api}/api/heartbeat` with `{id, uuid(base64), ver, conns?, modified_at}` when it has active connections, or otherwise every **15 s** (`TIME_HEARTBEAT`, `:18`, `:232-246`).
- The response can carry `sysinfo` (forces a sysinfo upload), `disconnect: [conn_ids]` (kicks sessions) and `strategy` (pushes config options).
- It is **only sent if an API server is configured and is not public RustDesk** (`heartbeat_url`, `:281-290`).
- lejianwen's handler stores `last_online_time` / `last_online_ip`, at most once per 30 s (`api:http/controller/api/index.go:41-63`).

### 2.5 Recommendation for "live device status"

- **Primary signal is hbbs `OnlineRequest`**, batched over all known IDs every 5 s from the panel, with change events pushed to the UI via SSE/WebSocket. It is authoritative ("can I connect now"), it is the same signal the RustDesk address book uses, and it works for clients that have no API server configured.
  - Latency: online about ≤15 s after the client starts. A client registers immediately and then every 15 s.
  - Offline: 30–45 s after the device disappears (REG_TIMEOUT 30 s plus up to one 15 s interval).
  - Add the poll interval to both.
  - After an hbbs restart, everything shows offline for up to about 15 s.
- **Secondary signal is `/api/heartbeat`** (implement it anyway). It gives active connections (`conns`), the client version and the uuid binding, and enables remote disconnect and strategy push.
  - Use offline threshold = 45 s. Keep `last_seen` in memory and flush it to the DB every 30–60 s rather than on every beat.
  - Show "online (hbbs)" and "API heartbeat age" separately. Treat the device as online if hbbs says online. Use the heartbeat as a fallback only when hbbs is unreachable.
- Do **not** derive status from `db_v2.sqlite3`; it has no timestamp.

---

## 3. MUST_LOGIN and JWT

### 3.1 Enabling it

- **Startup:** `--must-login Y`. This becomes env `MUST-LOGIN` via `init_args` / `arg_name` (`src/common.rs:49-84`) and can also be set from `.env` or `-c` config. If that is empty, the fallback is env `MUST_LOGIN=Y` (`src/rendezvous_server.rs:222-241`). The value is logged as `MUST_LOGIN=Y|N`.
- **Runtime:** the `ml Y|N` admin command (`:1310-1320`).

### 3.2 Where it is checked

The check is in `handle_punch_hole_request`, `src/rendezvous_server.rs:903-924`. It runs after the licence-key check (`:893-901`) and before the peer lookup:

1. `MUST_LOGIN` false → no check.
2. `ph.token` empty → `PunchHoleResponse{ other_failure: "Connection failed, please login!" }` (`:904-910`).
3. Otherwise, **only if `jwt::SECRET` is non-empty** → `jwt::verify_token(token)`. On error → `other_failure: "Token error, please log out and log back in!"` (`:911-922`).
4. **If `RUSTDESK_API_JWT_KEY` is unset, any non-empty token string passes.**

Scope:

- The check applies only to `PunchHoleRequest` over **TCP 21116 and WS 21118** (`:523-530`). UDP `PunchHoleRequest` is ignored entirely in this fork (`:442-445`).
- **Not checked:** `RequestRelay` sent to hbbs (`:531-544`, forwarded to the target without any token or key check) and hbbr itself (`src/relay_server.rs:425-462`, which checks only the licence key). MUST_LOGIN is therefore a UX gate, not a hard security boundary. The target's password or 2FA remains the real control.
- **Not logged** (see §4).

### 3.3 `verify_token` (`src/jwt.rs:34-54`)

- `SECRET` = env `RUSTDESK_API_JWT_KEY`, read lazily once per process (`src/jwt.rs:5-6`).
- Algorithm: **HS256 only** (`Validation::new(Algorithm::HS256)`). jsonwebtoken 8 defaults apply: `exp` is required and validated with 60 s leeway, and there is no iss/aud/nbf check. There is also an explicit `exp > now` check (`:45-49`).
- Claims struct (`:9-13`): `{ user_id: u32, exp: usize }`. `user_id` is **required** and must fit u32, otherwise decoding fails with "Invalid token". Extra claims are ignored.
- There is no revocation. A logged-out token keeps working in hbbs until `exp` (lejianwen default `jwt.expire-duration: 168h`).

### 3.4 Is it the same token as the API client token?

**Yes, by design.** The client puts `LocalConfig::get_option("access_token")`, the token returned by the API `/api/login`, into `PunchHoleRequest.token` (`client:src/ui_session_interface.rs:1984` → `client:src/client.rs:953-955`). It shows `other_failure` verbatim (`client:src/client.rs:1003-1006`). Because `failure` defaults to `ID_NOT_EXIST = 0` (`rendezvous.proto:94-100`), older clients that do not check `other_failure` would show "ID does not exist".

lejianwen/rustdesk-api issues a JWT **only if `jwt.key` / `RUSTDESK_API_JWT_KEY` is set**. Otherwise it issues an MD5 random token (`api:service/user.go:89-94`), which hbbs would reject when its SECRET is set. Its claims are `{user_id: uint, exp}` HS256 (`api:lib/jwt/jwt.go:14-44`), which matches the Rust struct.

**Our panel must:**

- Issue `/api/login` access tokens as HS256 JWTs with numeric `user_id` (≤ 2^32−1) and `exp`.
- Sign them with the same secret, exported to hbbs as `RUSTDESK_API_JWT_KEY`. In the s6 image, a single env var on the container is seen by both, through `with-contenv`.
- Keep the token TTL moderate, because hbbs cannot see revocation.

### 3.5 The `generate_token` bug

`src/jwt.rs:15-32` has `println!("secret: {:}", SECRET.to_string())` at **line 16**, which would print the JWT secret to stdout and therefore to docker logs. **It is not called anywhere.** The only `jwt::` uses in the crate are `jwt::SECRET` at `src/rendezvous_server.rs:911` and `jwt::verify_token` at `:913`. It is `pub` in a `pub mod jwt` (`src/lib.rs:6`), so the compiler gives no dead-code warning. The current risk is zero, but it should be deleted (or the `println!` removed) in our fork.

---

## 4. Other API-server dependencies and logging

### 4.1 Environment and ports in the Rust code

**API-related env:**

- `RUSTDESK_API_JWT_KEY` is the only `RUSTDESK_API_*` variable read by the Rust code (`src/jwt.rs:6`).
- The `RUSTDESK_API_RUSTDESK_ID_SERVER` / `_RELAY_SERVER` / `_API_SERVER` entries in `docker-compose.yml` are consumed only by the Go `rustdesk-api` (viper prefix `RUSTDESK_API`, `api:config/config.go:67-69`).
- `PORT_FOR_API` is **set** at `src/rendezvous_server.rs:200` but read nowhere, so it is vestigial.

**Other hbbs env:**

- `DB_URL`, `MAX_DATABASE_CONNECTIONS`, `ALWAYS_USE_RELAY`, `MUST_LOGIN`, `TEST_HBBS` (`:205`), `RUST_LOG` (flexi_logger).
- CLI args mapped to env `UPPER-WITH-DASHES` (e.g. `RELAY-SERVERS`, `KEY`, `PORT`, `MASK`, `LOCAL-IP`, `SOFTWARE-URL`) via `arg_name` (`src/common.rs:51-53`). Note that `RELAY_SERVERS` with an underscore is **not** read; use `-r` or `.env`.
- `./.env` in the CWD is loaded by both binaries (`src/common.rs:63-69`, `src/hbbr.rs:26-30`; hbbr uses raw key names).

**Other hbbr env:**

- `KEY` and `PORT`. **Gotcha:** hbbr's port is `PORT + 1` if `PORT` is set (`src/hbbr.rs:31-37`).
- The bandwidth vars listed in §1.4.

**Ports:**

| Port | Use |
|---|---|
| 21114 | API (Go) |
| 21115 | hbbs NAT-test, OnlineRequest, loopback admin |
| 21116 | hbbs TCP+UDP |
| 21117 | hbbr TCP, loopback admin |
| 21118 | hbbs WS (`port + 2`, `src/rendezvous_server.rs:152`) |
| 21119 | hbbr WS (`port + 2`, `src/relay_server.rs:83`) |

**WebSocket and web client:**

- Both WS listeners **trust `X-Real-IP` / `X-Forwarded-For` unconditionally** (`src/rendezvous_server.rs:1386-1400`, `src/relay_server.rs:402-416`). If 21118/21119 are exposed directly, clients can spoof their IP in logs, `ib`, `pr`, `usage` and blocklist checks inside the relay loop. Expose them only behind a reverse proxy that overwrites these headers (paths `/ws/id` → 21118 and `/ws/relay` → 21119, per `libs/hbb_common/src/websocket.rs:198+` and its tests at `:275-299`).
- lejianwen's web client gets `ws-host` / `webclient-magic-queryonline` from its config (`api:config/rustdesk.go:22-23`, `api:http/controller/web/index.go:18-25`).
- MUST_LOGIN applies equally to WS clients.

### 4.2 Log lines useful for fail2ban

The format is flexi_logger 0.22 `opt_format` (`src/main.rs:11-15`, `src/hbbr.rs:10-14`, stdout): `[YYYY-MM-DD HH:MM:SS.ffffff +TZ] LEVEL [src/file.rs:LINE] message`.

| Process | Level | Message | Where | IP format |
|---|---|---|---|---|
| hbbs | WARN | `Authentication failed from {addr} for peer {id} - invalid key` | `src/rendezvous_server.rs:894` | raw accept addr: `[::ffff:1.2.3.4]:port` (dual-stack), `[v6]:port`, or `1.2.3.4:0` from a WS `X-Real-IP` |
| hbbr | WARN | `Relay authentication failed from {addr} - invalid key` | `src/relay_server.rs:431` | same as above |
| hbbr | INFO | `{ip} blocked` | `src/relay_server.rs:383`, `:536` | plain IP (v4-mapped is normalised at `:383`) |
| hbbs | WARN | `Peer {id} ip/pk mismatch: {ip}/{pk:?} vs {old_ip}/{old_pk:?}` | `src/rendezvous_server.rs:706-713` | plain IP (ID-hijack attempt) |
| hbbs | WARN | `Peer {id} uuid mismatch: ...` | `:719-724` | **no IP** |
| hbbs | ERROR | `dec tcp data from {addr} err: ...` | `:1429` | addr |
| hbbs | ERROR | `Handshake failed: invalid phase 2 key exchange message` | `:618` | **no IP** |

Example regex: `Authentication failed from \[?(?:::ffff:)?<HOST>\]?:\d+ for peer`.

**Not logged at all:** MUST_LOGIN rejections (empty or invalid token, `:904-922`), `TOO_FREQUENT` IP-blocker hits, `ID_NOT_EXIST`, `OFFLINE`. For brute-force or ban logic, the panel's own `/api/login` must log failures in a fail2ban-friendly format with the client IP. If we patch hbbs, add `log::warn!` at `:904` and `:914` including `addr` and `ph.id`.

**Pre-auth crash (worth fixing in the fork).** `KeyExchange` handling does `ex.keys[0].to_vec().try_into().unwrap()` (`:623-624`), and `get_symetric_key_from_msg` panics on a bad box (`:1656`). Cargo builds with `panic = 'abort'` (`Cargo.toml:75`), so a malformed `KeyExchange` from any client **aborts the whole hbbs process** (s6 restarts it, and every peer goes "offline" for up to 15 s). The fix is to replace these with error returns.

---

## 5. Build and image

### 5.1 How the repo builds

- Cargo package `hbbs` 1.1.16 with bins `hbbs`, `hbbr` and `rustdesk-utils` (`Cargo.toml:1-16`). The release profile uses LTO, codegen-units=1, panic=abort and strip (`:72-76`).
- **Requirements:**
  - `submodules: recursive` for `libs/hbb_common`.
  - Network access for the git deps `async-speed-limit` and `rustdesk-org/reqwest`.
  - `sqlx::query!` compile-time checking needs `DATABASE_URL`. It is provided by the committed `.env` and `db_v2.sqlite3` in the repo root. Do not delete them.
- The upstream approach is `cross build --release --all-features --target {x86_64,aarch64}-unknown-linux-musl`, which produces static binaries.

### 5.2 Existing workflows

| File | What it does |
|---|---|
| `.github/workflows/build.yaml` | On tags: musl builds for amd64/arm64v8/armv7/i386 via the **archived** `actions-rs/toolchain` + `actions-rs/cargo` (`use-cross: true`) (`:26-75`). Windows build and NSIS installer (`:77-166`). Draft GH release (`:169-208`). Docker Hub push using `secrets.DOCKER_USERNAME/PASSWORD/DOCKER_IMAGE(_CLASSIC)` (`:211-462`). Per-arch images, then `Noelware/docker-manifest-action@master`. .deb packages (`:465-516`). |
| `.github/workflows/build-test.yaml` | The same for `test.*` tags, Docker Hub via secrets. |
| `.github/workflows/ghcr.yml` | Upstream's GHCR variant. It already uses only `GITHUB_TOKEN` (`permissions: packages: write`, `:16-18`) and pushes `ghcr.io/${{ github.repository }}-s6` and `ghcr.io/${{ github.repository }}`. However, the s6 image it builds is `FROM lejianwen/rustdesk-api:latest`, so it would ship **lejianwen's API binary**. |

### 5.3 s6 image (`docker/`)

- **`Dockerfile`:**
  - Base is `lejianwen/rustdesk-api:latest`, which provides `/app/apimain`.
  - Adds s6-overlay 3.2.0.0 (noarch plus `${S6_ARCH}`).
  - `COPY rootfs /`. hbbs, hbbr and rustdesk-utils are dropped into `docker/rootfs/usr/bin` by CI before the build.
  - `ENV RELAY`, `ENCRYPTED_ONLY`. `EXPOSE 21114-21119`. Healthcheck. `VOLUME /app/data /data`. `ENTRYPOINT /init`.
- **s6-rc services** (`docker/rootfs/etc/s6-overlay/s6-rc.d/`):
  - `key-secret` (oneshot, `up.real`): copies keys from `/run/secrets/key_{pub,priv}` or `KEY_PUB`/`KEY_PRIV` into `/data`, enforces both-or-neither, runs `chmod 600` and checks the pair with `rustdesk-utils validatekeypair`. On failure it halts.
  - `hbbr` (longrun, depends on key-secret): `cd /data; hbbr [-k _ if ENCRYPTED_ONLY=1]`.
  - `hbbs` (longrun, depends on key-secret and hbbr): `sleep 2; cd /data; hbbs -r $RELAY [-k _]`.
  - `api` (longrun, depends on key-secret and hbbs): `sleep 3; cd /app; ./apimain`.
  - `user/contents.d/{api,hbbr,hbbs,key-secret}` enables all four.
  - `usr/bin/healthcheck.sh` runs `s6-svstat` on hbbr and hbbs only.

### 5.4 Proposed workflow: one multi-arch image, `GITHUB_TOKEN` only

Put this workflow in the **panel repo** (`bropines/rdpanel`). It checks out the public server fork as a second repo, so there are no cross-repo secrets and no monorepo. It builds both architectures natively, without QEMU for the Rust build, and then runs a single buildx multi-platform build. Keep a copy of `docker/rootfs` in the panel repo under `deploy/s6/` with `api` renamed to `panel`.

`.github/workflows/image.yml`:

```yaml
name: image
on:
  workflow_dispatch:
  push:
    branches: [main]
    tags: ['v*']
permissions:
  contents: read
  packages: write
env:
  IMAGE: ghcr.io/bropines/rdpanel        # pick the final <name>
  SERVER_REPO: bropines/rustdesk-server
  SERVER_REF: merge-upstream-1.1.16

jobs:
  hbb:
    strategy:
      matrix:
        include:
          - { arch: amd64, target: x86_64-unknown-linux-musl }
          - { arch: arm64, target: aarch64-unknown-linux-musl }
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
        with: { repository: '${{ env.SERVER_REPO }}', ref: '${{ env.SERVER_REF }}', submodules: recursive }
      - uses: dtolnay/rust-toolchain@stable
      - uses: Swatinem/rust-cache@v2
        with: { key: '${{ matrix.target }}' }
      - uses: taiki-e/install-action@v2
        with: { tool: cross }
      - run: cross build --release --target ${{ matrix.target }} --bin hbbs --bin hbbr --bin rustdesk-utils
      - uses: actions/upload-artifact@v4
        with:
          name: hbb-${{ matrix.arch }}
          path: |
            target/${{ matrix.target }}/release/hbbs
            target/${{ matrix.target }}/release/hbbr
            target/${{ matrix.target }}/release/rustdesk-utils
          if-no-files-found: error

  panel:
    strategy:
      matrix: { arch: [amd64, arm64] }
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version-file: go.mod }
      # if the panel has a web UI: build it here first (npm ci && npm run build) and go:embed it
      - run: CGO_ENABLED=0 GOOS=linux GOARCH=${{ matrix.arch }} go build -trimpath -ldflags "-s -w -X main.version=${GITHUB_REF_NAME}" -o out/panel ./cmd/panel
      - uses: actions/upload-artifact@v4
        with: { name: panel-${{ matrix.arch }}, path: out/panel, if-no-files-found: error }

  image:
    needs: [hbb, panel]
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - uses: actions/download-artifact@v4
        with: { pattern: 'hbb-*', path: deploy/s6/bin }      # -> bin/hbb-amd64/hbbs ...
      - uses: actions/download-artifact@v4
        with: { pattern: 'panel-*', path: deploy/s6/bin }    # -> bin/panel-amd64/panel
      - run: chmod -R a+x deploy/s6/bin
      - uses: docker/setup-qemu-action@v3                    # only needed for RUN steps of the foreign arch
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with: { registry: ghcr.io, username: '${{ github.actor }}', password: '${{ secrets.GITHUB_TOKEN }}' }
      - id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.IMAGE }}
          tags: |
            type=ref,event=branch
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=raw,value=latest,enable={{is_default_branch}}
            type=sha
      - uses: docker/build-push-action@v6
        with:
          context: deploy/s6
          platforms: linux/amd64,linux/arm64
          push: ${{ github.event_name != 'pull_request' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

`deploy/s6/Dockerfile`:

```dockerfile
FROM alpine:3.20
ARG TARGETARCH
ARG S6_OVERLAY_VERSION=3.2.0.0
RUN apk add --no-cache ca-certificates tzdata xz
RUN set -eux; case "$TARGETARCH" in amd64) S6=x86_64;; arm64) S6=aarch64;; *) exit 1;; esac; \
    wget -qO- https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-noarch.tar.xz | tar -C / -Jxp; \
    wget -qO- https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}/s6-overlay-${S6}.tar.xz | tar -C / -Jxp
COPY rootfs /
COPY bin/hbb-${TARGETARCH}/hbbs bin/hbb-${TARGETARCH}/hbbr bin/hbb-${TARGETARCH}/rustdesk-utils /usr/bin/
COPY bin/panel-${TARGETARCH}/panel /usr/bin/panel
ENV RELAY=relay.example.com ENCRYPTED_ONLY=1 S6_BEHAVIOUR_IF_STAGE2_FAILS=2
EXPOSE 21114 21115 21116 21116/udp 21117 21118 21119
VOLUME /data
HEALTHCHECK --interval=10s --timeout=5s CMD /usr/bin/healthcheck.sh
ENTRYPOINT ["/init"]
```

Rootfs changes compared with `docker/rootfs`:

- Rename `s6-rc.d/api` to `s6-rc.d/panel`, with `run`: `#!/command/with-contenv sh` / `cd /data` / `exec /usr/bin/panel`. Dependencies: `key-secret`; adding `hbbs` is optional, because the panel should tolerate hbbs being down.
- Replace `user/contents.d/api` with `user/contents.d/panel`.
- Use `exec` in the hbbs and hbbr `run` scripts, so signals go to the binary rather than `sh`.
- Add `s6-svstat /run/s6-rc/servicedirs/panel` to the healthcheck.
- Data lives in `/data` only: hbbs keys, `db_v2.sqlite3`, `blacklist.txt`/`blocklist.txt`, and the panel DB.
- Pass `RUSTDESK_API_JWT_KEY` (and `MUST_LOGIN`) as container env so hbbs and the panel share them.

Notes:

- `ghcr.io/bropines/<name>` is created on first push. Make it public under the package settings if required. A `GITHUB_TOKEN` push only works when the workflow runs in a repo owned by `bropines`.
- For a faster arm64 build, `runs-on: ubuntu-24.04-arm` (free for public repos) can build `aarch64-unknown-linux-musl` natively with `cross` or with `rustup target add` + `musl-tools`.
- Keep the panel `CGO_ENABLED=0`. If it uses SQLite, use `modernc.org/sqlite`.

---

## Summary of recommendations

1. **Admin commands:** run the panel **in the same s6 container**, or `network_mode: service:` (shared netns). The text channel on `127.0.0.1:21115` (hbbs, i.e. rendezvous port − 1) and `127.0.0.1:21117` (hbbr) has no auth and is protected only by a loopback check. It cannot be reached from a bridge-network container.
   - Wrap it in a typed, allowlisted and audited Go client that reads until EOF.
   - Always follow `aur` with `rs <list>` (the relay-list wipe bug).
   - Periodically drain `pr` (unbounded memory).
   - Reconcile desired state, because hbbs/hbbr keep everything in memory.
   - Persist black/blocklists to `/data/*.txt`.
   - Never L4-proxy public traffic to 127.0.0.1:21115 or 21117.
2. **Live status:** poll hbbs with a single batched `OnlineRequest` to `127.0.0.1:21116`, skipping its unsolicited `KeyExchange`, every ~5 s. Expect ≤15 s to show online and 30–45 s to show offline. Use `/api/heartbeat` (15 s idle / 3 s with active sessions) for connections, version and disconnect, and as a fallback. `db_v2.sqlite3` has no last-seen column; use it read-only for the inventory at most.
3. **MUST_LOGIN:** our `/api/login` must return an **HS256 JWT `{user_id:u32, exp}`** signed with `RUSTDESK_API_JWT_KEY`, the same env var given to hbbs. Without that env var, hbbs accepts any non-empty token. The check covers only TCP/WS PunchHoleRequest (not RequestRelay or hbbr) and logs nothing. Keep the TTL modest, because there is no revocation.
4. **Fork hygiene** (small patches to `rustdesk-server`, separate from the panel):
   - Remove `println!` of the secret in `jwt.rs:16` (dead code).
   - Add `log::warn!` with the IP on MUST_LOGIN rejections.
   - Replace the `unwrap()`/`panic!` in KeyExchange (`rendezvous_server.rs:623-624,1656`), which is a pre-auth remote crash under `panic=abort`.
   - Fix the `aur` → `RelayServers0` bug.
   - Drop the unused `PORT_FOR_API`.
   - Only expose WS ports 21118/21119 behind a header-overwriting reverse proxy (they trust `X-Real-IP` blindly).
5. **Build:** one workflow in the panel repo that builds hbbs/hbbr with `cross` (musl, amd64 and arm64) from `bropines/rustdesk-server@merge-upstream-1.1.16` and the panel with Go `CGO_ENABLED=0`. It then assembles an `alpine` + s6-overlay image (TARGETARCH-selected binaries) and pushes multi-arch to `ghcr.io/bropines/<name>` using only `GITHUB_TOKEN`. Drop the `FROM lejianwen/rustdesk-api` base and the Docker Hub secret workflows.
