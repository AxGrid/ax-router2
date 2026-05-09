.PHONY: all build build-host build-linux build-linux-amd64 build-linux-arm64 \
        build-darwin build-darwin-amd64 build-darwin-arm64 build-all \
        release web web-deps test clean run lint bump version

# Override these if your toolchain isn't on PATH.
NPM ?= npm
NODE ?= node
GO ?= go

# Version is read from the tracked ./VERSION file (one line, e.g. "0.1.1").
# Override on the CLI if you need to: `make build VERSION=2.0.0-rc1`.
VERSION_FILE := VERSION
VERSION ?= $(shell cat $(VERSION_FILE) 2>/dev/null | tr -d '[:space:]' || echo 0.0.0)

# Build counter — gitignored, lives only on the local machine. Bumped once
# per top-level `make build-*` invocation by the `bump` target. Make will
# evaluate `bump` exactly once per invocation thanks to its prerequisite
# memoization; subsequent build targets in the same run share the new
# number.
BUILD_FILE := .build-number
BUILD = $(shell cat $(BUILD_FILE) 2>/dev/null || echo 0)

# Optional: short git SHA, baked in for traceability.
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "")

# CGO disabled by default — works around a macOS Sonoma + Go 1.22 dyld bug
# and produces statically-linked binaries on Linux.
GOTEST_FLAGS ?= -count=1 -timeout=60s
GOENV := CGO_ENABLED=0

# `BUILD` is re-read inside each recipe via `$$(cat …)` so that the value
# reflects the just-bumped counter, not the value captured at make startup.
LDFLAGS = -s -w \
          -X main.version=$(VERSION) \
          -X main.build=$$(cat $(BUILD_FILE) 2>/dev/null || echo 0) \
          -X main.commit=$(COMMIT)
GOFLAGS_BUILD = -trimpath -ldflags="$(LDFLAGS)"
GOBUILD = $(GOENV) $(GO) build $(GOFLAGS_BUILD)

# bin/ layout:
#   bin/ax-router                       (host)
#   bin/ax-router-linux-amd64
#   bin/ax-router-linux-arm64
#   bin/ax-router-darwin-amd64
#   bin/ax-router-darwin-arm64
PKG := ./cmd/ax-router

all: web build

# bump increments BUILD_FILE by one and prints the new triple. Make memoises
# prerequisites within a single invocation, so all build-* targets reachable
# from one `make` command share the same incremented number.
bump:
	@n=$$(cat $(BUILD_FILE) 2>/dev/null || echo 0); \
	  n=$$((n + 1)); \
	  echo $$n > $(BUILD_FILE); \
	  if [ -n "$(COMMIT)" ]; then \
	    printf ">> ax-router %s build #%s (%s)\n" "$(VERSION)" "$$n" "$(COMMIT)"; \
	  else \
	    printf ">> ax-router %s build #%s\n"      "$(VERSION)" "$$n"; \
	  fi

# Read the current build number without changing it.
version:
	@n=$$(cat $(BUILD_FILE) 2>/dev/null || echo 0); \
	  if [ -n "$(COMMIT)" ]; then \
	    printf "ax-router %s build #%s (%s)\n" "$(VERSION)" "$$n" "$(COMMIT)"; \
	  else \
	    printf "ax-router %s build #%s\n"      "$(VERSION)" "$$n"; \
	  fi

build: build-host

build-host: web bump
	$(GOBUILD) -o bin/ax-router $(PKG)

build-linux: build-linux-amd64 build-linux-arm64

build-linux-amd64: web bump
	GOOS=linux GOARCH=amd64 $(GOBUILD) -o bin/ax-router-linux-amd64 $(PKG)

build-linux-arm64: web bump
	GOOS=linux GOARCH=arm64 $(GOBUILD) -o bin/ax-router-linux-arm64 $(PKG)

build-darwin: build-darwin-amd64 build-darwin-arm64

build-darwin-amd64: web bump
	GOOS=darwin GOARCH=amd64 $(GOBUILD) -o bin/ax-router-darwin-amd64 $(PKG)

build-darwin-arm64: web bump
	GOOS=darwin GOARCH=arm64 $(GOBUILD) -o bin/ax-router-darwin-arm64 $(PKG)

build-all: build-linux build-darwin

# release: produce gzipped tarballs in dist/ alongside .env.example and README.
#
# COPYFILE_DISABLE=1 stops macOS BSD-tar from emitting AppleDouble (._foo)
# resource-fork files. --no-xattrs is GNU-tar's belt; BSD-tar ignores it
# silently.
release: build-linux build-darwin
	@mkdir -p dist
	@build=$$(cat $(BUILD_FILE) 2>/dev/null || echo 0); \
	tag="$(VERSION)-b$$build"; \
	for f in bin/ax-router-linux-amd64 bin/ax-router-linux-arm64 \
	          bin/ax-router-darwin-amd64 bin/ax-router-darwin-arm64; do \
	  name=$$(basename $$f); \
	  tmpdir=$$(mktemp -d); \
	  install -d $$tmpdir/$$name; \
	  install -m 0755 $$f $$tmpdir/$$name/ax-router; \
	  install -m 0644 README.md .env.example $$tmpdir/$$name/; \
	  COPYFILE_DISABLE=1 tar --no-xattrs -C $$tmpdir -czf dist/$$name-$$tag.tar.gz $$name 2>/dev/null \
	    || COPYFILE_DISABLE=1 tar -C $$tmpdir -czf dist/$$name-$$tag.tar.gz $$name; \
	  rm -rf $$tmpdir; \
	  echo "  → dist/$$name-$$tag.tar.gz"; \
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
