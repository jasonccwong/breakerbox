.PHONY: all test build agent hub web dev e2e lint release-dry clean

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)

all: build

# --- Test -------------------------------------------------------------------

test:
	cd pkg/protocol && go test ./...
	cd hub && go test ./...
	cd agent && go test ./...
	cd cmd/testapp && go build ./...

test-linux:
	docker run --rm -v "$(PWD)":/src -w /src/agent golang:1.26 go test ./internal/supervisor/

# --- Build ------------------------------------------------------------------

build: agent hub

agent:
	cd agent && go build -ldflags '$(LDFLAGS)' -o ../bin/breakerbox-agent .

hub: webassets
	cd hub && go build -ldflags '$(LDFLAGS)' -o ../bin/breakerbox-hub .

web:
	pnpm --filter web build

webassets: web
	rm -rf hub/internal/webassets/dist
	cp -r web/dist hub/internal/webassets/dist

# --- Dev --------------------------------------------------------------------

dev:
	@echo "hub:  http://127.0.0.1:8090  (API + admin)"
	@echo "web:  http://127.0.0.1:5173  (Vite dev server, proxies /api)"
	@(cd hub && go run . serve --http=127.0.0.1:8090) & \
	pnpm --filter web dev; kill %1

e2e: build
	./scripts/e2e.sh

lint:
	cd pkg/protocol && go vet ./...
	cd hub && go vet ./...
	cd agent && go vet ./...

release-dry:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin dist web/dist hub/internal/webassets/dist
