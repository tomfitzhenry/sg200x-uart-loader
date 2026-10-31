GO      ?= go
GOFMT   ?= gofmt
BINARY  ?= sg200x-uart-loader
DIST    := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build arm64 amd64 test selftest e2e e2e-race check clean

all: build

build: amd64

amd64:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST)/$(BINARY)-linux-amd64 .

arm64:
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(DIST)/$(BINARY)-linux-arm64 .

test:
	$(GO) test ./...

selftest:
	$(GO) run . --selftest

e2e:
	$(GO) test -tags=e2e ./internal/romsim -count=1

e2e-race:
	E2E_RACE=1 $(GO) test -race -tags=e2e ./internal/romsim -count=1

check:
	@test -z "$$($(GOFMT) -l .)" || { echo "gofmt needed:"; $(GOFMT) -l .; exit 1; }
	$(GO) vet ./...
	$(GO) test ./...
	$(GO) test -tags=e2e ./internal/romsim -count=1
	$(GO) run . --selftest
	$(MAKE) amd64
	$(MAKE) arm64

clean:
	rm -rf $(DIST)
