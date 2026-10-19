# RD Panel (rdpanel) build targets. Requires Go 1.26 and Node 20.19+ (npm).
#
#   make web      build the SPA into web/dist (embedded by the Go binary)
#   make build    build bin/rdpanel (static, CGO disabled)
#   make all      web + build
#   make test     go vet + go test (race) + web tests
#   make lint     go vet + staticcheck + web lint/typecheck
#   make gen      regenerate web/src/api/schema.d.ts from api/openapi.yaml
#   make hbb      build static (musl) hbbs/hbbr/rustdesk-utils from HBB_SRC into
#                 deploy/bin/<arch>/ (docker, rust:alpine)
#   make image    docker image for the host arch (runs `make hbb` if needed)

GO        ?= go
NPM       ?= npm
BIN       ?= bin/rdpanel
PKG       := github.com/bropines/rdpanel
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT    ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null)
DATE      ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS   := -s -w \
	-X $(PKG)/internal/buildinfo.Version=$(VERSION) \
	-X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
	-X $(PKG)/internal/buildinfo.Date=$(DATE)
STATICCHECK_VERSION ?= 2024.1.1
GOVULNCHECK_VERSION ?= v1.1.4

# Container image (see docs/deploy.md). HBB_SRC is a checkout of
# github.com/bropines/rustdesk-server with submodules.
DOCKER     ?= docker
IMAGE      ?= rdpanel:local
HBB_SRC    ?= $(HOME)/build/rustdesk-server
IMAGE_ARCH ?= $(shell uname -m | sed -e 's/x86_64/amd64/' -e 's/aarch64/arm64/')
HBB_BIN    := deploy/bin/$(IMAGE_ARCH)

.PHONY: all web web-deps build test test-go test-web lint lint-go lint-web vuln gen run clean hbb image

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

hbb:
	sh deploy/scripts/build-hbb.sh $(HBB_SRC) $(HBB_BIN)

image:
	@test -x $(HBB_BIN)/hbbs -a -x $(HBB_BIN)/hbbr -a -x $(HBB_BIN)/rustdesk-utils || $(MAKE) hbb
	$(DOCKER) buildx build --load -f deploy/Dockerfile \
		--platform linux/$(IMAGE_ARCH) \
		--build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) --build-arg DATE=$(DATE) \
		--build-arg HBB_REF=$$(git -C $(HBB_SRC) describe --always --dirty 2>/dev/null || echo unknown) \
		-t $(IMAGE) .

clean:
	rm -rf bin web/node_modules/.tmp
	find web/dist -mindepth 1 ! -name .gitkeep -exec rm -rf {} +
