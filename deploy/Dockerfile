# syntax=docker/dockerfile:1.7
#
# RD Panel all-in-one image: hbbs + hbbr (bropines/rustdesk-server, AGPL-3.0)
# and rdpanel (this repo, MIT) supervised by s6-overlay v3 on Alpine.
#
# Build context: the repository root.
#
#   docker buildx build -f deploy/Dockerfile --platform linux/amd64,linux/arm64 .
#
# hbbs/hbbr are NOT compiled here. They are prebuilt static musl binaries taken
# from the build context at deploy/bin/<TARGETARCH>/{hbbs,hbbr,rustdesk-utils}:
#   - CI (.github/workflows/image.yml) cross-compiles them from the server fork
#     and drops them there before the buildx step;
#   - locally, `make image` builds them with deploy/scripts/build-hbb.sh.
# Keeping the Rust build out of the Dockerfile keeps image builds fast and
# avoids emulating a ~10 minute LTO build under QEMU for the foreign arch.
#
# The web UI and rdpanel are built for the BUILD platform and cross-compiled
# (pure Go, CGO disabled), so no QEMU is needed for them either. The only RUN
# steps on the target platform are trivial (apk add, a doc file, --version).

ARG ALPINE_VERSION=3.24
# Newest stable Go for the build (stdlib security fixes); the code itself stays
# go1.23-compatible (go.mod), so GOTOOLCHAIN=local never downloads anything.
ARG GO_IMAGE=golang:1.27-alpine
ARG NODE_IMAGE=node:22-alpine

# ---------------------------------------------------------------- web (SPA)
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS web
# web/package-lock.json is resolved by npm 11; node 22 bundles npm 10, whose
# `npm ci` rejects that lock file (ajv/json-schema-traverse dedupe differs).
ARG NPM_VERSION=11.20.0
RUN --mount=type=cache,target=/root/.npm npm install -g --no-audit --no-fund "npm@${NPM_VERSION}"
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
COPY web/scripts ./scripts
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# ---------------------------------------------------------------- rdpanel
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS panel
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown
# Source of the bundled hbbs/hbbr, shown on the panel's About page (same values
# as the final stage's AGPL SOURCE note).
ARG HBB_REPO=https://github.com/bropines/rustdesk-server
ARG HBB_REF=unknown
ENV GOTOOLCHAIN=local CGO_ENABLED=0 GOFLAGS=-trimpath
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/embed.go
COPY --from=web /src/web/dist ./web/dist
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    PKG=github.com/bropines/rdpanel; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -ldflags "-s -w -X $PKG/internal/buildinfo.Version=$VERSION -X $PKG/internal/buildinfo.Commit=$COMMIT -X $PKG/internal/buildinfo.Date=$DATE -X $PKG/internal/buildinfo.HBBRepo=$HBB_REPO -X $PKG/internal/buildinfo.HBBRef=$HBB_REF" \
      -o /out/rdpanel ./cmd/rdpanel

# ---------------------------------------------------------------- s6-overlay
# Downloaded and verified on the build platform, then copied as a plain tree.
FROM --platform=$BUILDPLATFORM alpine:${ALPINE_VERSION} AS s6
ARG TARGETARCH
ARG S6_OVERLAY_VERSION=3.2.3.2
ARG S6_SHA256_NOARCH=5379750ed30a84bbd2e2dd74847ba6b5bd29cd0b2e3ea2ec58049b57eb2eda12
ARG S6_SHA256_X86_64=e6befcc96a437a3831386ecfc51808c5d3e939dc5fe3c02ae9284599e8aa2408
ARG S6_SHA256_AARCH64=b17f17a82e7a515c682a91edaf2ffdabb73f891981b6c1fd712115693a2f8b4c
RUN set -eu; \
    apk add --no-cache xz; \
    case "$TARGETARCH" in \
      amd64) arch=x86_64;  sum=$S6_SHA256_X86_64 ;; \
      arm64) arch=aarch64; sum=$S6_SHA256_AARCH64 ;; \
      *) echo "unsupported TARGETARCH=$TARGETARCH" >&2; exit 1 ;; \
    esac; \
    base="https://github.com/just-containers/s6-overlay/releases/download/v${S6_OVERLAY_VERSION}"; \
    cd /tmp; \
    wget -q "$base/s6-overlay-noarch.tar.xz" "$base/s6-overlay-${arch}.tar.xz"; \
    printf '%s  %s\n' "$S6_SHA256_NOARCH" s6-overlay-noarch.tar.xz "$sum" "s6-overlay-${arch}.tar.xz" | sha256sum -c -; \
    mkdir /s6root; \
    tar -C /s6root -Jxpf s6-overlay-noarch.tar.xz; \
    tar -C /s6root -Jxpf "s6-overlay-${arch}.tar.xz"

# ---------------------------------------------------------------- final
FROM alpine:${ALPINE_VERSION}
ARG TARGETARCH
ARG VERSION=dev
ARG HBB_REPO=https://github.com/bropines/rustdesk-server
ARG HBB_REF=unknown

RUN apk add --no-cache ca-certificates tzdata

COPY --from=s6 /s6root/ /
COPY deploy/rootfs/ /
COPY --chmod=0755 deploy/bin/${TARGETARCH}/hbbs deploy/bin/${TARGETARCH}/hbbr deploy/bin/${TARGETARCH}/rustdesk-utils /usr/bin/
COPY --from=panel /out/rdpanel /usr/bin/rdpanel

# AGPL-3.0 §6: say where the exact hbbs/hbbr sources are.
RUN set -eu; \
    mkdir -p /usr/share/doc/rustdesk-server; \
    printf 'hbbs, hbbr and rustdesk-utils in this image are built from\n%s at %s\nLicense: AGPL-3.0 (see that repository).\n' \
      "$HBB_REPO" "$HBB_REF" > /usr/share/doc/rustdesk-server/SOURCE; \
    chmod 0755 /usr/local/bin/rdpanel /usr/libexec/rdpanel/* /etc/s6-overlay/s6-rc.d/*/run; \
    /usr/bin/rdpanel version; /usr/bin/hbbs --version

LABEL org.opencontainers.image.title="rdpanel" \
      org.opencontainers.image.description="RD Panel: rdpanel API server and admin panel with hbbs/hbbr, for RustDesk clients (s6-overlay). Not affiliated with RustDesk." \
      org.opencontainers.image.source="https://github.com/bropines/rdpanel" \
      org.opencontainers.image.licenses="MIT AND AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      io.github.bropines.rustdesk-server.ref="${HBB_REF}"

# s6-overlay behaviour: stop the container if the key-secret oneshot fails,
# give services time to shut down, never time out waiting for the oneshot.
ENV PATH=/command:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    S6_BEHAVIOUR_IF_STAGE2_FAILS=2 \
    S6_CMD_WAIT_FOR_SERVICES_MAXTIME=0 \
    S6_SERVICES_GRACETIME=8000 \
    S6_KILL_GRACETIME=2000 \
    S6_VERBOSITY=1 \
    PUID=10001 \
    PGID=10001 \
    ENCRYPTED_ONLY=0 \
    RDPANEL_DATA_DIR=/data/panel \
    RDPANEL_HBBS_PUBLIC_KEY_FILE=/data/id_ed25519.pub \
    RDPANEL_HBB_REF=${HBB_REF}

EXPOSE 21114 21115 21116 21116/udp 21117 21118 21119
VOLUME /data
WORKDIR /data
HEALTHCHECK --interval=15s --timeout=5s --start-period=20s --retries=3 CMD ["/usr/libexec/rdpanel/healthcheck"]
STOPSIGNAL SIGTERM
ENTRYPOINT ["/init"]
