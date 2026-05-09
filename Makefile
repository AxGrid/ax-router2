.PHONY: all build build-host build-linux build-linux-amd64 build-linux-arm64 \
        build-darwin build-darwin-amd64 build-darwin-arm64 build-all \
        release web web-deps test clean run lint

# Override these if your toolchain isn't on PATH.
NPM ?= npm
NODE ?= node
GO ?= go

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# CGO disabled by default — works around a macOS Sonoma + Go 1.22 dyld bug
# and produces statically-linked binaries on Linux.
GOTEST_FLAGS ?= -count=1 -timeout=60s
GOENV := CGO_ENABLED=0
GOFLAGS_BUILD := -trimpath -ldflags="-s -w -X main.version=$(VERSION)"
GOBUILD := $(GOENV) $(GO) build $(GOFLAGS_BUILD)

# bin/ layout:
#   bin/ax-router                       (host)
#   bin/ax-router-linux-amd64
#   bin/ax-router-linux-arm64
#   bin/ax-router-darwin-amd64
#   bin/ax-router-darwin-arm64
PKG := ./cmd/ax-router

all: web build

build: build-host

build-host: web
	$(GOBUILD) -o bin/ax-router $(PKG)

build-linux: build-linux-amd64 build-linux-arm64

build-linux-amd64: web
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o bin/ax-router-linux-amd64 $(PKG)

build-linux-arm64: web
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o bin/ax-router-linux-arm64 $(PKG)

build-darwin: build-darwin-amd64 build-darwin-arm64

build-darwin-amd64: web
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o bin/ax-router-darwin-amd64 $(PKG)

build-darwin-arm64: web
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o bin/ax-router-darwin-arm64 $(PKG)

build-all: build-linux build-darwin

# release: produce gzipped tarballs in dist/ alongside .env.example and README.
release: build-linux build-darwin
	@mkdir -p dist
	@for f in bin/ax-router-linux-amd64 bin/ax-router-linux-arm64 \
	          bin/ax-router-darwin-amd64 bin/ax-router-darwin-arm64; do \
	  name=$$(basename $$f); \
	  tmpdir=$$(mktemp -d); \
	  install -d $$tmpdir/$$name; \
	  install -m 0755 $$f $$tmpdir/$$name/ax-router; \
	  install -m 0644 README.md .env.example $$tmpdir/$$name/; \
	  tar -C $$tmpdir -czf dist/$$name-$(VERSION).tar.gz $$name; \
	  rm -rf $$tmpdir; \
	  echo "  → dist/$$name-$(VERSION).tar.gz"; \
	done

web: web-deps
	cd web && $(NPM) run build

web-deps:
	@if [ ! -d web/node_modules ]; then \
	  echo ">> installing web dependencies"; \
	  cd web && $(NPM) install; \
	fi

run: build
	./bin/ax-router

test:
	$(GOENV) $(GO) test ./... $(GOTEST_FLAGS)

lint:
	$(GO) vet ./...

clean:
	rm -rf bin/ dist/ web/node_modules web/dist
	mkdir -p web/dist
	@echo "<!-- placeholder; run 'make web' to build the dashboard -->" > web/dist/index.html
