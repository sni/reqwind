#!/usr/bin/make -f

PROJECT=reqwind
SHELL:=bash
GO ?= go
PREFIX ?= /usr/local
DESTDIR ?=
GOVERSION:=$(shell \
    $(GO) env GOVERSION | \
    awk '{ sub(/^go/, ""); split($$0, a, /\./); printf ("%04d%04d", a[1], a[2]); exit; }' \
)
MINGOVERSION:=00010023
MINGOVERSIONSTR:=1.23
TOOLSFOLDER=$(shell pwd)/tools
export GOBIN := $(TOOLSFOLDER)
export PATH := $(GOBIN):$(PATH)

# pinned tool versions; install with: make tools
GOLANGCI_LINT_VERSION=v2.15.0
GOVULNCHECK_VERSION=v1.9.0

BUILD_FLAGS=-trimpath
TEST_FLAGS=-timeout=5m $(BUILD_FLAGS)

.PHONY: all reqwind tools build build-linux test testf longtest benchmark racetest covertest coverweb check fmt versioncheck golangci govulncheck citest install clean

all: build

reqwind:
	$(MAKE) build

tools: $(TOOLSFOLDER)/golangci-lint $(TOOLSFOLDER)/govulncheck

$(TOOLSFOLDER)/golangci-lint: Makefile | versioncheck
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

$(TOOLSFOLDER)/govulncheck: Makefile | versioncheck
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)

build: versioncheck
	$(GO) build $(BUILD_FLAGS) -o reqwind ./cmd/reqwind

build-linux: versioncheck
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BUILD_FLAGS) -o reqwind.linux.amd64 ./cmd/reqwind

test:
	$(GO) test -race $(TEST_FLAGS) ./...

# test with filter, e.g. make testf Parser
testf:
	$(GO) test -count 1 -v $(TEST_FLAGS) ./... -run "$(filter-out $@,$(MAKECMDGOALS))"

longtest:
	$(GO) test -v $(TEST_FLAGS) ./...

benchmark:
	$(GO) test $(TEST_FLAGS) -v -bench=B\* -run=^$$ -benchmem ./pkg/reqwind

racetest:
	$(GO) test -race -short $(TEST_FLAGS) -coverprofile=coverage.txt -covermode=atomic -gcflags "-d=checkptr=0" ./...

covertest:
	$(GO) test -v $(TEST_FLAGS) -coverprofile=cover.out ./...
	$(GO) tool cover -func=cover.out
	$(GO) tool cover -html=cover.out -o coverage.html

coverweb:
	$(GO) test -v $(TEST_FLAGS) -coverprofile=cover.out ./...
	$(GO) tool cover -html=cover.out

check: test
	$(GO) vet ./...
	test -z "$$(gofmt -l cmd pkg assets)"

fmt:
	gofmt -w -s cmd pkg assets

versioncheck:
	@[ $$( printf '%s\n' $(GOVERSION) $(MINGOVERSION) | sort | head -n 1 ) = $(MINGOVERSION) ] || { \
		echo "**** ERROR:"; \
		echo "**** $(PROJECT) requires at least golang version $(MINGOVERSIONSTR) or higher"; \
		echo "**** this is: $$(go version)"; \
		exit 1; \
	}

golangci: $(TOOLSFOLDER)/golangci-lint
	@$(GOBIN)/golangci-lint version
	@$(GOBIN)/golangci-lint run --show-stats=0 --timeout=5m ./...

govulncheck: $(TOOLSFOLDER)/govulncheck
	$(GOBIN)/govulncheck -version
	$(GOBIN)/govulncheck ./...

# Fail CI when the vulnerability check fails, including toolchain findings.
citest: golangci govulncheck longtest benchmark racetest
	@echo "all CI tests successful"

install: build
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 reqwind $(DESTDIR)$(PREFIX)/bin/reqwind

clean:
	rm -f reqwind reqwind.linux.amd64
	rm -f cover.out coverage.html coverage.txt
	rm -rf tools

# just skip unknown make targets
.DEFAULT:
	@if [ "$(firstword $(MAKECMDGOALS))" = "testf" ]; then \
		: ; \
	else \
		echo "unknown make target(s): $(MAKECMDGOALS)"; \
		exit 1; \
	fi
