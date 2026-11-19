# One entry point for agents, people and CI: `make gate`.
# Optional tools are skipped locally when missing; CI sets GATE_STRICT=1.
# BASE=<git ref> enables the red→green and `buf breaking` checks.

GO   ?= go
BASE ?=
GATE := $(GO) tool gate

# The versions of the optional tools, pinned here once for people, agents and CI.
BUF_VERSION           := v1.75.0
GOVULNCHECK_VERSION   := v1.9.0
GO_LICENSES_VERSION   := v2.0.1
GOLANGCI_LINT_VERSION := v2.15.0

# Kept in a variable: a literal comma would split the arguments of $(call tool).
LICENSES_CHECK := GOROOT=$$($(GO) env GOROOT) go-licenses check ./... --disallowed_types=forbidden,restricted --ignore github.com/cactus/go-statsd-client/statsd

# $(call tool,<binary>,<command>)
define tool
	@if command -v $(1) >/dev/null 2>&1; then $(2); \
	elif [ -n "$(GATE_STRICT)" ]; then echo "gate: $(1) is required (GATE_STRICT=1)"; exit 1; \
	else echo "gate: skip $(1) (not installed)"; fi
endef

.PHONY: gate tools static spec test bdd redgreen fmt vet build lint vuln licenses buf buf-breaking layering release-check

# Built from source with this project's Go: a prebuilt golangci-lint cannot
# type-check a standard library newer than the Go it was built with.
tools: ## install the pinned optional tools into $(go env GOPATH)/bin
	$(GO) install github.com/bufbuild/buf/cmd/buf@$(BUF_VERSION)
	$(GO) install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	$(GO) install github.com/google/go-licenses/v2@$(GO_LICENSES_VERSION)
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

gate: static spec test redgreen ## everything the PR gate runs
	@echo "gate: all checks passed"

static: fmt vet build layering lint vuln licenses buf ## G1

fmt:
	@out="$$(git ls-files -co --exclude-standard '*.go' | xargs gofmt -l)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

build:
	CGO_ENABLED=0 $(GO) build ./...

layering:
	$(GATE) layering

lint:
	$(call tool,golangci-lint,golangci-lint run ./...)

vuln:
	$(call tool,govulncheck,govulncheck ./...)

# Dependencies must stay compatible with Apache-2.0 (no GPL, AGPL, SSPL, ...).
# go-statsd-client/statsd (MIT, via the embedded Temporal server) keeps its
# license file in a parent module path; .github/workflows/embedded.yml ignores
# the same package.
licenses:
	$(call tool,go-licenses,$(LICENSES_CHECK))

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

test: ## G3 (includes the BDD suite)
	$(GO) test -race -count=1 ./...

bdd:
	$(GO) test -count=1 ./internal/bdd/

redgreen: ## G4
	@if [ -n "$(BASE)" ]; then $(GATE) redgreen -base $(BASE); else echo "gate: skip redgreen (set BASE=<ref>)"; fi

release-check:
	$(GATE) release-check
