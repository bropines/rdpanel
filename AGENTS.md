# AGENTS.md

Guide for AI coding agents and contributors working on **RD Panel** (`rdpanel`): a Go API
server + React admin panel for RustDesk, shipped in one s6 image with hbbs/hbbr built
from [bropines/rustdesk-server](https://github.com/bropines/rustdesk-server).
Product spec: [`SPEC.md`](SPEC.md). User-facing docs: [`README.md`](README.md),
[`docs/`](docs).

## Layout

| path | what |
|---|---|
| `cmd/rdpanel` | entry point; CLI lives in `internal/app/cli*.go` |
| `internal/app` | wiring: config → DB → services → HTTP server and workers |
| `internal/clientapi` | RustDesk client API (`/api/*`), must stay wire-compatible |
| `internal/adminapi` | panel JSON API (`/api/v1/admin/*`), contract in `api/openapi.yaml` |
| `internal/oidc` | OIDC for panel and client, split-host hand-off |
| `internal/auth`, `internal/crypto` | passwords (argon2id), tokens, TOTP, AEAD secrets |
| `internal/store`, `internal/db` | SQLite (modernc, WAL) access; goose migrations `internal/db/migrations/000NN_*.sql` |
| `internal/hbbs` | hbbs/hbbr loopback admin channel (`control`), presence poller, reconciler, relay stats, wire format |
| `internal/legacy` | importer for lejianwen/rustdesk-api v2.7 (`rdpanel migrate-legacy`) |
| `internal/metrics`, `internal/httpx` | Prometheus metrics, HTTP middleware |
| `web/` | React + TS + Vite, shadcn/ui, TanStack Query/Router, i18n en/ru, MSW mocks, Playwright |
| `api/openapi.yaml` | admin API contract; `web/src/api/schema.d.ts` is generated from it |
| `deploy/` | Dockerfile (s6), compose example, env example, Helm chart, monitoring |
| `docs/research` | client API and hbbs protocol notes, legacy schema |
| `docs/design` | design docs for upcoming features |

## Toolchain

- Go 1.23 (CI also runs Go stable), Node 20.19+ with npm. The maintainer's Go, Docker,
  gcc and Helm live in **WSL (Debian)**, not on Windows — run Go/make/docker there and
  never install toolchains on the Windows host.
- No CGO: the binary is built with `CGO_ENABLED=0`.

## Commands

```sh
make web build          # web/dist, then bin/rdpanel with the UI embedded
make test               # go vet + go test -race + web vitest
make lint               # go vet + staticcheck (pinned version) + web typecheck/eslint
make gen                # regenerate web/src/api/schema.d.ts after editing api/openapi.yaml
cd web && npm run dev:mock      # UI against in-memory MSW mocks (admin/admin, TOTP 123456)
cd web && npm run e2e           # Playwright smoke against the mock build
scripts/e2e-server.sh && (cd web && npm run e2e:real)   # Playwright against a real backend
```

## Rules

- **Client API compatibility comes first.** Changes under `internal/clientapi` must keep
  official RustDesk clients working; tolerate unknown fields, keep field names and
  types, and add/extend the contract tests. Notes: `docs/research/client-api.md`.
- **Admin API is contract-first:** edit `api/openapi.yaml`, run `make gen`, implement the
  handler, keep the router-vs-spec contract test green, add an MSW mock for the UI.
- **Migrations are append-only:** add the next `000NN_` file, never edit a released one.
- **Security defaults stay on:** tokens hashed, secrets AEAD-encrypted, CSRF on cookie
  sessions, rate limits on auth endpoints, trusted-proxy-only client IPs. No secrets in
  logs.
- **hbbs/hbbr behaviour changes go to the fork**
  ([bropines/rustdesk-server](https://github.com/bropines/rustdesk-server)) and must be
  **opt-in** with upstream behaviour as the default. The image pins the fork by commit
  SHA in `.github/workflows/image.yml` (`SERVER_REF`); bump it deliberately.
- **UI:** every string in both `en` and `ru` locales (a parity test enforces it); reuse
  existing cards/tables/drawers; check light, dark and mobile.
- **Metrics:** low-cardinality labels only (no users, IPs, device ids unless opt-in).
  Grafana dashboard and alert rules exist twice (`deploy/monitoring` and the Helm chart's
  `files/`); a Go test fails if they drift.
- **Config:** new settings get an `RDPANEL_*` env var plus YAML key, a row in the README
  config table and in `docs/deploy.md`, and an entry in `deploy/rdpanel.env.example`
  when operators are likely to set it.
- **Naming:** the product is **RD Panel** (slug `rdpanel`). Use "RustDesk" only
  descriptively ("for RustDesk", "RustDesk clients"), never as our product name.
  Some identifiers keep the former name `rustdesk-panel` on purpose and must not be
  renamed: the Helm chart's resource base name (existing releases' StatefulSet
  selectors and PVCs), the Grafana dashboard uid/tag, the Prometheus job and
  `RustDeskPanel*` alert names, and the legacy-import UUID namespace.
- Before finishing: `make lint` and `make test` green (staticcheck included).

## Git

- Commit author: `bropines <bropines@gmail.com>`.
- Conventional-style subjects: `feat(scope): …`, `fix(scope): …`, `docs: …`, `ci: …`.
- Releases: tag `vX.Y.Z` on `main`; CI publishes `ghcr.io/bropines/rdpanel:X.Y.Z`,
  `:X.Y`, `:latest` and the Helm chart.
