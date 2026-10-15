#!/usr/bin/env bash
# Starts bin/rdpanel on a fresh data directory with an admin account and demo
# data (scripts/seed-demo), for the real-backend Playwright suite
# (web/e2e/real) and documentation screenshots.
#
#   make web build && scripts/e2e-server.sh            # then: cd web && npm run e2e:real
#   scripts/e2e-server.sh wait                          # same, but stay in the foreground
#   scripts/e2e-server.sh stop                          # stop server + heartbeat loop
#
# Environment: PORT (31114), E2E_ADMIN_PASSWORD (test value below),
# STATE (/tmp/rdpanel-e2e: data dir, logs, pids),
# HBB_BIN: directory with hbbs + hbbr (e.g. a rustdesk-server target/release);
#   they are started (hbbs on HBBS_PORT, default 21116, so 21115/21118 too;
#   hbbr on 21117/21119) and the panel's hbbs integration is enabled. HBB_IMAGE runs them in that docker image with host
#   networking instead (e.g. rust:1-bookworm when the host glibc is too old).
# With hbbs running, presence comes from hbbs: the keepalive loop then also
# registers the seven "online" fake devices with hbbs (RegisterPk once, then
# RegisterPeer over UDP like a client).
set -euo pipefail
cd "$(dirname "$0")/.."

PORT=${PORT:-31114}
STATE=${STATE:-/tmp/rdpanel-e2e}
ADMIN_PW=${E2E_ADMIN_PASSWORD:-e2e-admin-password-1}

stop() {
	docker rm -f rdpanel-e2e-hbbs rdpanel-e2e-hbbr >/dev/null 2>&1 || true
	for f in seed server hbbs hbbr; do
		if [ -f "$STATE/$f.pid" ]; then
			kill "$(cat "$STATE/$f.pid")" 2>/dev/null || true
			rm -f "$STATE/$f.pid"
		fi
	done
}

if [ "${1:-}" = stop ]; then
	stop
	exit 0
fi

stop
rm -rf "$STATE"
mkdir -p "$STATE/data"
export RDPANEL_DATA_DIR="$STATE/data" RDPANEL_LISTEN="127.0.0.1:$PORT" RDPANEL_LOG_FORMAT=text
export RDPANEL_PUBLIC_URL="${RDPANEL_PUBLIC_URL:-http://localhost:$PORT}"
export RDPANEL_HBBS_ENABLED=false
if [ -n "${HBB_BIN:-}" ]; then
	mkdir -p "$STATE/hbb"
	HBB_BIN=$(cd "$HBB_BIN" && pwd)
	for d in hbbr hbbs; do
		args=()
		[ "$d" = hbbs ] && args=(-p "${HBBS_PORT:-21116}" -r 127.0.0.1:21117)
		if [ -n "${HBB_IMAGE:-}" ]; then
			docker run -d --rm --name "rdpanel-e2e-$d" --network host --user "$(id -u):$(id -g)" \
				-v "$HBB_BIN:/hbb:ro" -v "$STATE/hbb:/data" -w /data "$HBB_IMAGE" "/hbb/$d" "${args[@]}" >/dev/null
		else
			(cd "$STATE/hbb" && nohup "$HBB_BIN/$d" "${args[@]}" >"$STATE/$d.log" 2>&1 & echo $! >"$STATE/$d.pid")
		fi
	done
	for _ in $(seq 1 50); do [ -s "$STATE/hbb/id_ed25519.pub" ] && break; sleep 0.2; done
	export RDPANEL_HBBS_ENABLED=true RDPANEL_HBBS_PUBLIC_KEY_FILE="$STATE/hbb/id_ed25519.pub" RDPANEL_HBBS_LIST_DIR="$STATE/hbb"
	export RDPANEL_HBBS_ID_SERVER="127.0.0.1:${HBBS_PORT:-21116}"
fi

printf '%s\n' "$ADMIN_PW" | bin/rdpanel user create --admin --password-stdin --display-name "Alexei Volkov" \
	--email admin@example.com admin
nohup bin/rdpanel serve >"$STATE/server.log" 2>&1 &
echo $! >"$STATE/server.pid"
for _ in $(seq 1 50); do
	curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
	sleep 0.2
done
curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null

go build -o "$STATE/seed-demo" ./scripts/seed-demo
RDPANEL_SEED_ADMIN_PASSWORD="$ADMIN_PW" "$STATE/seed-demo" -url "http://127.0.0.1:$PORT"
# Keep 7 of the 10 fake devices online (heartbeat fallback without hbbs).
HBBS_FLAG=()
[ -n "${HBB_BIN:-}" ] && HBBS_FLAG=(-hbbs "127.0.0.1:${HBBS_PORT:-21116}")
RDPANEL_SEED_ADMIN_PASSWORD="$ADMIN_PW" nohup "$STATE/seed-demo" -url "http://127.0.0.1:$PORT" -keepalive "${HBBS_FLAG[@]}" \
	>"$STATE/seed.log" 2>&1 &
echo $! >"$STATE/seed.pid"
echo "rdpanel on http://127.0.0.1:$PORT (state: $STATE)"
if [ "${1:-}" = wait ]; then
	# Stay in the foreground (e.g. under WSL, where the distro may stop once
	# no command runs); stopping this script stops both processes.
	trap stop EXIT INT TERM
	wait "$(cat "$STATE/server.pid")"
fi
