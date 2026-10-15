# rustdesk-panel build targets. Requires Go 1.23 and Node 20.19+ (npm).
#
#   make web      build the SPA into web/dist (embedded by the Go binary)
#   make build    build bin/rdpanel (static, CGO disabled)
#   make all      web + build
#   make test     go vet + go test (race) + web tests
#   make lint     go vet + staticcheck + web lint/typecheck
#   make gen      regenerate web/src/api/schema.d.ts from api/openapi.yaml

GO        ?= go
NPM       ?= npm
BIN       ?= bin/rdpanel
PKG       := github.com/bropines/rustdesk-panel
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w \
	-X $(PKG)/internal/buildinfo.Version=$(VERSION) \
	-X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
	-X $(PKG)/internal/buildinfo.Date=$(DATE)
STATICCHECK_VERSION ?= 2024.1.1
GOVULNCHECK_VERSION ?= v1.1.4

.PHONY: all web web-deps build test test-go test-web lint lint-go lint-web vuln gen run clean

all: web build

web-deps:
	cd web && $(NPM) ci

web: web-deps
	cd web && $(NPM) run build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/rdpanel

test: test-go test-web

test-go:
	$(GO) vet ./...
	$(GO) test -race ./...

test-web:
	cd web && $(NPM) test

lint: lint-go lint-web

lint-go:
	$(GO) vet ./...
	$(GO) run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

lint-web:
	cd web && $(NPM) run typecheck && $(NPM) run lint

vuln:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

gen:
	cd web && $(NPM) run gen:api

run: build
	RDPANEL_DATA_DIR=./data RDPANEL_LOG_FORMAT=text ./$(BIN) serve

clean:
	rm -rf bin web/node_modules/.tmp
	find web/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
