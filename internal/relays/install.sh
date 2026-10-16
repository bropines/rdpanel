#!/bin/sh
# RD Panel relay agent installer (docs/relay-agents.md).
#
# Served by the panel at <public URL>/agent/install.sh with the panel URL and
# the image tag filled in:
#
#   curl -fsSL https://desk-api.example.com/agent/install.sh | sudo sh -s -- --token <TOKEN>
#
# Writes <dir>/compose.yml (host networking, same image as the panel with
# RDPANEL_MODE=agent), keeps the one-time token in <dir>/agent.env (mode 600)
# and the agent identity in <dir>/data, then runs `docker compose up -d`.
# Re-running it updates the image and keeps the identity; --uninstall removes
# the container (and with --purge also the files).
set -eu

# Filled in by the panel when it serves the script.
DEFAULT_PANEL_URL='@@RDPANEL_PANEL_URL@@'
DEFAULT_IMAGE='@@RDPANEL_IMAGE@@'

case $DEFAULT_PANEL_URL in @@*) DEFAULT_PANEL_URL= ;; esac
case $DEFAULT_IMAGE in @@*) DEFAULT_IMAGE=ghcr.io/bropines/rdpanel:latest ;; esac

CONTAINER=rdpanel-relay
NL='
'

TOKEN=
PANEL_URL=
DIR=/opt/rdpanel-agent
IMAGE=
PORT=
NAME=
WAIT=120
ACTION=install
PURGE=0
INSTALL_DOCKER=0

info() { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die() {
	printf 'error: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<EOF
RD Panel relay agent installer

Usage: curl -fsSL <panel>/agent/install.sh | sudo sh -s -- [options]

  --token TOKEN      one-time enrollment token from Server -> Relays (first install)
  --panel URL        panel public URL (default: ${DEFAULT_PANEL_URL:-none})
  --dir DIR          install directory (default: /opt/rdpanel-agent)
  --image IMAGE      container image (default: $DEFAULT_IMAGE)
  --port PORT        relay port, TCP (default: 21117)
  --name NAME        name reported to the panel (default: host name)
  --wait SECONDS     how long to wait for the container to become healthy (default: 120)
  --install-docker   install Docker from https://get.docker.com when it is missing
  --uninstall        stop and remove the agent container (keeps <dir>)
  --purge            with --uninstall: also delete <dir> (the agent identity is lost)
  -h, --help         this help

Re-run to update the image; the agent identity in <dir>/data is kept.
EOF
}

# matches REGEX VALUE: VALUE is a single non-empty line matching REGEX.
matches() {
	case $2 in '' | *"$NL"*) return 1 ;; esac
	printf '%s\n' "$2" | grep -Eq "$1"
}

# saved KEY: a value from <dir>/install.conf (written by a previous run).
saved() {
	[ -f "$DIR/install.conf" ] || return 0
	sed -n "s/^$1=//p" "$DIR/install.conf" | head -n 1
}

while [ $# -gt 0 ]; do
	case $1 in
	--token | --panel | --dir | --image | --port | --name | --wait)
		[ $# -ge 2 ] || die "$1 needs a value"
		opt=$1
		val=$2
		shift 2
		;;
	--token=* | --panel=* | --dir=* | --image=* | --port=* | --name=* | --wait=*)
		opt=${1%%=*}
		val=${1#*=}
		shift
		;;
	--uninstall)
		ACTION=uninstall
		shift
		continue
		;;
	--purge)
		PURGE=1
		shift
		continue
		;;
	--install-docker)
		INSTALL_DOCKER=1
		shift
		continue
		;;
	-h | --help)
		usage
		exit 0
		;;
	*) die "unknown option: $1 (see --help)" ;;
	esac
	case $opt in
	--token) TOKEN=$val ;;
	--panel) PANEL_URL=$val ;;
	--dir) DIR=$val ;;
	--image) IMAGE=$val ;;
	--port) PORT=$val ;;
	--name) NAME=$val ;;
	--wait) WAIT=$val ;;
	esac
done

# ---- validation ----

DIR=${DIR%/}
matches '^/[A-Za-z0-9._/-]+$' "$DIR" || die "--dir must be an absolute path (letters, digits, . _ - /): $DIR"
case /$DIR/ in */../* | */./*) die "--dir must not contain . or .. components: $DIR" ;; esac
case $DIR in /bin | /boot | /dev | /etc | /home | /lib | /lib64 | /opt | /proc | /root | /run | /sbin | /srv | /sys | /tmp | /usr | /var)
	die "--dir must be a dedicated directory, not $DIR" ;;
esac
[ "$PURGE" = 0 ] || [ "$ACTION" = uninstall ] || die "--purge only works together with --uninstall"
matches '^[0-9]{1,4}$' "$WAIT" || die "--wait must be a number of seconds: $WAIT"

if [ "$(id -u)" -ne 0 ] && [ "${RDPANEL_INSTALL_ALLOW_NONROOT:-}" != 1 ]; then
	die "run as root, e.g.: curl -fsSL <panel>/agent/install.sh | sudo sh -s -- --token <TOKEN>
       (rootless Docker: set RDPANEL_INSTALL_ALLOW_NONROOT=1 and use a --dir you can write)"
fi

# ---- docker ----

ensure_docker() {
	if ! command -v docker >/dev/null 2>&1; then
		if [ "$INSTALL_DOCKER" = 1 ]; then
			command -v curl >/dev/null 2>&1 || die "curl is required to install Docker"
			info "Installing Docker from https://get.docker.com"
			curl -fsSL https://get.docker.com | sh
			command -v docker >/dev/null 2>&1 || die "Docker installation failed"
		else
			die "Docker is not installed. Install Docker Engine with the Compose plugin
       (https://docs.docker.com/engine/install/) or re-run this command with --install-docker
       to use the official script from https://get.docker.com."
		fi
	fi
	docker info >/dev/null 2>&1 || die "cannot talk to the Docker daemon (is it running? try: systemctl start docker)"
	docker compose version >/dev/null 2>&1 || die "the Docker Compose plugin is missing (e.g. apt-get install docker-compose-plugin)"
}

# container_project prints the compose project label of $CONTAINER; fails when
# the container does not exist.
container_project() {
	docker container inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$CONTAINER" 2>/dev/null
}

# ---- uninstall ----

if [ "$ACTION" = uninstall ]; then
	ensure_docker
	if [ -f "$DIR/compose.yml" ]; then
		info "Stopping and removing the relay agent ($DIR/compose.yml)"
		docker compose -f "$DIR/compose.yml" down --remove-orphans
	elif container_project >/dev/null; then
		info "Removing container $CONTAINER"
		docker rm -f "$CONTAINER" >/dev/null
	else
		info "No relay agent container found"
	fi
	if [ "$PURGE" = 1 ]; then
		info "Deleting $DIR"
		rm -rf "$DIR/data"
		rm -f "$DIR/compose.yml" "$DIR/agent.env" "$DIR/install.conf"
		rmdir "$DIR" 2>/dev/null || warn "$DIR is not empty, left in place"
		echo "Done. Delete the relay in the panel (Server -> Relays) if you no longer need it."
	else
		echo "Done. $DIR is kept (agent identity in $DIR/data); re-run the installer to start it again,"
		echo "or add --purge to delete it."
	fi
	exit 0
fi

# ---- install / update ----

[ -n "$PANEL_URL" ] || PANEL_URL=$(saved PANEL_URL)
[ -n "$PANEL_URL" ] || PANEL_URL=$DEFAULT_PANEL_URL
[ -n "$PORT" ] || PORT=$(saved PORT)
[ -n "$PORT" ] || PORT=21117
[ -n "$NAME" ] || NAME=$(saved NAME)
[ -n "$IMAGE" ] || IMAGE=$DEFAULT_IMAGE

[ -n "$PANEL_URL" ] || die "--panel is required (the panel's public URL, e.g. https://desk-api.example.com)"
PANEL_URL=${PANEL_URL%/}
matches '^https?://[][A-Za-z0-9._~:/%@+=-]+$' "$PANEL_URL" || die "--panel must be an http(s) URL: $PANEL_URL"
matches '^[A-Za-z0-9][A-Za-z0-9._/:@-]{0,254}$' "$IMAGE" || die "invalid --image: $IMAGE"
if ! matches '^[0-9]{1,5}$' "$PORT" || [ "$PORT" -lt 1 ] || [ "$PORT" -gt 65533 ]; then
	die "--port must be 1-65533: $PORT"
fi
[ -z "$NAME" ] || matches '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$' "$NAME" || die "--name may use letters, digits, . _ - (up to 63): $NAME"
[ -z "$TOKEN" ] || matches '^[A-Za-z0-9_-]{20,128}$' "$TOKEN" || die "--token does not look like an enrollment token"

if [ -z "$TOKEN" ] && [ ! -f "$DIR/data/agent-state.json" ] && ! grep -q '^RDPANEL_AGENT_ENROLL_TOKEN=.' "$DIR/agent.env" 2>/dev/null; then
	die "--token is required for the first install (Server -> Relays -> Add relay -> Relay agent)"
fi

ensure_docker

if project=$(container_project) && [ -z "$project" ]; then
	die "a container named $CONTAINER exists that was not created by this installer; remove it first: docker rm -f $CONTAINER"
fi

info "Writing $DIR/compose.yml"
mkdir -p "$DIR/data"
chmod 755 "$DIR"

# write_file PATH MODE: stdin -> PATH, atomically.
write_file() {
	tmp="$1.tmp.$$"
	(umask 077 && cat >"$tmp")
	chmod "$2" "$tmp"
	mv -f "$tmp" "$1"
}

if [ -n "$TOKEN" ]; then
	printf 'RDPANEL_AGENT_ENROLL_TOKEN=%s\n' "$TOKEN" | write_file "$DIR/agent.env" 600
elif [ ! -f "$DIR/agent.env" ]; then
	printf '# RDPANEL_AGENT_ENROLL_TOKEN=\n' | write_file "$DIR/agent.env" 600
else
	chmod 600 "$DIR/agent.env"
fi

{
	printf 'PANEL_URL=%s\n' "$PANEL_URL"
	printf 'PORT=%s\n' "$PORT"
	printf 'NAME=%s\n' "$NAME"
} | write_file "$DIR/install.conf" 644

{
	cat <<EOF
# Written by the RD Panel relay agent installer; re-run it to update
# (docs/relay-agents.md). The one-time token is in agent.env (mode 600),
# the agent identity in data/ (keep it).
services:
  relay:
    image: "$IMAGE"
    container_name: $CONTAINER
    # hbbr must see real client IPs (black/blocklists, usage).
    network_mode: host
    restart: unless-stopped
    environment:
      RDPANEL_MODE: agent
      RDPANEL_AGENT_PANEL_URL: "$PANEL_URL"
      RDPANEL_AGENT_RELAY_PORT: "$PORT"
EOF
	[ -z "$NAME" ] || printf '      RDPANEL_AGENT_HOSTNAME: "%s"\n' "$NAME"
	cat <<EOF
    env_file:
      - "$DIR/agent.env"
    volumes:
      - "$DIR/data:/data"
    stop_grace_period: 20s
    ulimits:
      nofile: { soft: 1048576, hard: 1048576 }
    logging:
      driver: json-file
      options: { max-size: 20m, max-file: "5" }
EOF
} | write_file "$DIR/compose.yml" 644

info "Pulling $IMAGE"
docker compose -f "$DIR/compose.yml" pull
info "Starting the relay agent"
docker compose -f "$DIR/compose.yml" up -d --remove-orphans

info "Waiting for the container to become healthy (up to ${WAIT}s)"
state=
elapsed=0
while :; do
	state=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}/{{.State.Status}}' "$CONTAINER" 2>/dev/null || echo unknown)
	case $state in
	healthy/* | none/running) break ;;
	*/exited | */dead | unhealthy/*) break ;;
	esac
	[ "$elapsed" -lt "$WAIT" ] || break
	sleep 2
	elapsed=$((elapsed + 2))
done

case $state in
healthy/* | none/running)
	cat <<EOF

RD Panel relay agent is running.
  container  $CONTAINER ($IMAGE)
  panel      $PANEL_URL
  files      $DIR/compose.yml, $DIR/agent.env, $DIR/data/
  logs       docker logs -f $CONTAINER

Next: in the panel open Server -> Relays and approve this relay (row menu ->
Approve) unless "Approve new agents" is off. It joins the relay list once the
end-to-end probe passes; clients must reach TCP $PORT on this host.
EOF
	;;
*)
	warn "the container is not healthy yet (state: $state). Last log lines:"
	docker logs --tail 30 "$CONTAINER" >&2 2>&1 || true
	cat >&2 <<EOF

Check: docker logs -f $CONTAINER
A rejected token (expired after 1 hour or already used) needs a new one from
Server -> Relays (row menu -> New enrollment token); re-run with --token.
EOF
	exit 1
	;;
esac
