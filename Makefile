.PHONY: all build web web-deps test clean run lint

# Override these if your toolchain isn't on PATH.
NPM ?= npm
NODE ?= node
GO ?= go

# CGO disabled by default — works around a macOS Sonoma + Go 1.22 dyld bug.
GOTEST_FLAGS ?= -count=1 -timeout=60s
GOENV := CGO_ENABLED=0

all: web build

build:
	$(GOENV) $(GO) build -o bin/ax-router ./cmd/ax-router

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
	rm -rf bin/ web/node_modules web/dist
	mkdir -p web/dist
	@echo "<!-- placeholder; run 'make web' to build the dashboard -->" > web/dist/index.html
