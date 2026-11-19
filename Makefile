# One entry point for agents, people and CI: `make gate`.
# Optional tools are skipped locally when missing; CI sets GATE_STRICT=1.
# BASE=<git ref> enables the red→green and `buf breaking` checks.

GO   ?= go
BASE ?=
GATE := $(GO) tool gate

# Local runs use the patch from go.mod unless GOTOOLCHAIN is already set (ADR-0014).
GO_PATCH := $(shell sed -n 's/^go \([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)$$/\1/p' go.mod 2>/dev/null)
ifneq ($(GO_PATCH),)
export GOTOOLCHAIN ?= go$(GO_PATCH)
endif

# The versions of the optional tools, pinned here once for people, agents and CI.
BUF_VERSION           := v1.75.0
GOVULNCHECK_VERSION   := v1.9.0
GO_LICENSES_VERSION   := v2.0.1
GOLANGCI_LINT_VERSION := v2.15.0

# $(call tool,<binary>,<command>)
define tool
	@if command -v $(1) >/dev/null 2>&1; then $(2); \
	elif [ -n "$(GATE_STRICT)" ]; then echo "gate: $(1) is required (GATE_STRICT=1)"; exit 1; \
	else echo "gate: skip $(1) (not installed)"; fi
endef

.PHONY: gate tools static spec test bdd bdd-skips redgreen fmt vet build lint vuln licenses buf buf-breaking layering gomod release-check pg-prefetch

EP_EMBEDDED_POSTGRES ?= github.com/fergusstrange/embedded-postgres/v2/cmd/embedded-postgres
GATE_PG_BIN := .gate/bin/embedded-postgres
GATE_PG_CACHE ?= $(BDD_POSTGRES_CACHE_DIR)

# Built from source with this project's Go: a prebuilt golangci-lint cannot
# type-check a standard library newer than the Go it was built with.
tools: ## install the pinned optional tools into $(go env GOPATH)/bin
	$(GO) install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

gate: static spec test redgreen ## everything the PR gate runs
	@echo "gate: all checks passed"

static: fmt vet build layering gomod lint vuln licenses buf ## G1

fmt:
	@out="$$(git ls-files -co --exclude-standard '*.go' | xargs gofmt -l)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

build:
	CGO_ENABLED=0 $(GO) build ./...

layering:
	$(GATE) layering

gomod:
	$(GATE) gomod

lint:
	$(call tool,golangci-lint,golangci-lint run ./...)

vuln:
	$(call tool,govulncheck,set -e; roots="$$($(GATE) module-roots)" || exit 1; [ -n "$$roots" ] || { echo "gate: no module roots"; exit 1; }; for dir in $$roots; do (cd "$$dir" && govulncheck ./...) || exit 1; done)

# Dependencies must stay compatible with Apache-2.0 (no GPL, AGPL, SSPL, ...).
licenses:
	$(call tool,go-licenses,GOROOT=$$($(GO) env GOROOT) go-licenses check ./... --disallowed_types=forbidden,restricted)

buf:
	$(call tool,buf,cd contracts && buf lint)
	@if [ -n "$(BASE)" ] && git cat-file -e $(BASE):contracts/buf.yaml 2>/dev/null; then \
		$(MAKE) --no-print-directory buf-breaking; fi

# Kept in a variable: a literal comma would split the $(call) arguments.
BUF_AGAINST := ../.git\#ref=$(BASE),subdir=contracts

buf-breaking:
	$(call tool,buf,cd contracts && buf breaking --against '$(BUF_AGAINST)')

spec: ## G2
	$(GATE) spec lint
	$(GATE) spec lock
	$(GATE) docs

# Packages excluding internal/bdd (BDD runs separately so the skip summary runs after it).
GATE_TEST_PKGS := $(shell $(GO) list ./... | grep -v '/internal/bdd$$')

test: ## G3 (includes the BDD suite)
	$(GO) test -race -count=1 $(GATE_TEST_PKGS)
	$(MAKE) --no-print-directory bdd

bdd:
	@mkdir -p .gate/bin
	@if [ -z "$$EP_SUPERVISOR" ] && [ -x "$(GATE_PG_BIN)" ]; then export EP_SUPERVISOR="$$(pwd)/$(GATE_PG_BIN)"; fi; \
	$(GO) test -race -count=1 ./internal/bdd/
	$(MAKE) --no-print-directory bdd-skips

bdd-skips:
	$(GATE) bdd-skips

pg-prefetch: ## download Postgres bundles into BDD_POSTGRES_CACHE_DIR (ADR-0015)
	@mkdir -p .gate/bin
	$(GO) build -o $(GATE_PG_BIN) $(EP_EMBEDDED_POSTGRES)
	@cache="$(GATE_PG_CACHE)"; \
	if [ -z "$$cache" ]; then echo "gate: pg-prefetch requires BDD_POSTGRES_CACHE_DIR"; exit 1; fi; \
	mkdir -p "$$cache"; \
	attempt=0; \
	until EP_SUPERVISOR="$$(pwd)/$(GATE_PG_BIN)" "$(GATE_PG_BIN)" prefetch --postgres-version 16.15.1 --cache-dir "$$cache" --json; do \
		attempt=$$((attempt + 1)); \
		if [ "$$attempt" -ge 5 ]; then exit 1; fi; \
		sleep 2; \
	done

redgreen: ## G4
	@if [ -n "$(BASE)" ]; then $(GATE) redgreen -base $(BASE); else echo "gate: skip redgreen (set BASE=<ref>)"; fi

release-check:
	$(GATE) release-check
