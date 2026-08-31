GO ?= go
BINARY ?= codexlink
VERSION ?= $(shell tr -d '\r\n' < VERSION)

.PHONY: all build test vet race check install install-dev install-test clean

all: check build

build:
	$(GO) build -trimpath -ldflags "-s -w -X github.com/joeykchen/codexlink/internal/buildinfo.Version=$(VERSION)" -o bin/$(BINARY) ./cmd/codexlink

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

race:
	$(GO) test -race ./...

check: test vet

install:
	./install.sh

install-dev:
	./scripts/install-dev.sh

install-test:
	./scripts/test-install.sh

clean:
	rm -rf bin dist
