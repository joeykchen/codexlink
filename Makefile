GO ?= go
BINARY ?= codexlink
VERSION ?= $(shell tr -d '\r\n' < VERSION)
VERSION_PACKAGE := github.com/joeykchen/codexlink/internal/buildinfo.Version

.PHONY: all build fmt-check test vet race version-test check full-check install install-dev install-test clean

all: full-check build

build:
	$(GO) build -trimpath -ldflags "-s -w -X $(VERSION_PACKAGE)=$(VERSION)" -o bin/$(BINARY) ./cmd/codexlink

fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

race:
	$(GO) test -race ./...

version-test:
	./scripts/test-version.sh

check: fmt-check test vet version-test

full-check: check race install-test

install:
	./install.sh

install-dev:
	./scripts/install-dev.sh

install-test:
	./scripts/test-install.sh

clean:
	rm -rf bin dist
