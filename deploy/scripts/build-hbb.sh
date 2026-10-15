#!/bin/sh
# Build static (musl) hbbs, hbbr and rustdesk-utils from a local checkout of
# github.com/bropines/rustdesk-server inside a rust:alpine container, and copy
# them to <out-dir> (normally deploy/bin/amd64) for `make image`.
#
#   deploy/scripts/build-hbb.sh ~/build/rustdesk-server deploy/bin/amd64
#
# The checkout must contain libs/hbb_common (git submodule) and the committed
# .env + db_v2.sqlite3 (sqlx compile-time query checking needs DATABASE_URL).
# Cargo registry/git caches and the target dir live in named docker volumes
# (rdpanel-hbb-*) so rebuilds are incremental and the checkout stays untouched
# apart from what cargo/sqlx write next to the sources.
#
# Only builds for the host architecture; CI cross-compiles both arches.
set -eu

SRC=${1:?usage: build-hbb.sh <rustdesk-server-src> <out-dir>}
OUT=${2:?usage: build-hbb.sh <rustdesk-server-src> <out-dir>}
RUST_IMAGE=${RUST_IMAGE:-rust:1-alpine}

SRC=$(cd "$SRC" && pwd)
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

[ -f "$SRC/Cargo.toml" ] || { echo "build-hbb: $SRC is not a rustdesk-server checkout" >&2; exit 1; }
[ -f "$SRC/libs/hbb_common/Cargo.toml" ] || { echo "build-hbb: libs/hbb_common missing (git submodule update --init --recursive)" >&2; exit 1; }
[ -f "$SRC/db_v2.sqlite3" ] || { echo "build-hbb: db_v2.sqlite3 missing (needed by sqlx macros)" >&2; exit 1; }

docker run --rm \
	-v "$SRC":/src -w /src \
	-v "$OUT":/out \
	-v rdpanel-hbb-registry:/usr/local/cargo/registry \
	-v rdpanel-hbb-git:/usr/local/cargo/git \
	-v rdpanel-hbb-target:/target \
	-e CARGO_TARGET_DIR=/target \
	-e DATABASE_URL=sqlite:///src/db_v2.sqlite3 \
	-e HOST_UID="$(id -u)" -e HOST_GID="$(id -g)" \
	"$RUST_IMAGE" sh -euc '
		apk add --no-cache musl-dev make perl >/dev/null
		cargo build --release --locked --bin hbbs --bin hbbr --bin rustdesk-utils
		for b in hbbs hbbr rustdesk-utils; do
			install -m 0755 -o "$HOST_UID" -g "$HOST_GID" "/target/release/$b" "/out/$b"
		done
	'
ls -l "$OUT"
