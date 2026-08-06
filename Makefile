.PHONY: build run run-echo serve serve-expose web-build web-test web-check test vet cover cover-check clean setup restore tui-gen prompt-reg chaos

# Overall statement-coverage floor for `make cover-check` (local / optional CI).
# Soft baseline ~77%; keep below measured total so the gate does not flake.
# Raise as package coverage PRs land. Not a hard CI fail yet.
COVER_MIN ?= 75
COVER_PROFILE ?= coverage.out

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
# GO_LDFLAGS is intentionally not named LDFLAGS: many macOS/Homebrew shells
# export LDFLAGS for the C toolchain, and Make's "?=" would inherit that and
# break `go build -ldflags`.
GO_LDFLAGS ?= -X github.com/jonathanung/strike-cli/internal/version.Version=$(VERSION) -X github.com/jonathanung/strike-cli/internal/version.Commit=$(COMMIT)

# Flatten internal/tui/_src/* into package tui (Go one-directory packages).
tui-gen:
	go generate ./internal/tui

build: tui-gen
	go build -ldflags "$(GO_LDFLAGS)" -o strike ./cmd/strike

# Creates ~/.strike (config, example agent + skill); never overwrites.
setup:
	bash scripts/setup.sh

# Repair missing/corrupt ~/.strike layout (prefer: strike restore).
restore:
	bash scripts/restore.sh

# Runs with your configured/default provider (needs credentials; see README).
run: build
	./strike

# Offline dev loop — no API key needed.
run-echo: build
	./strike --provider echo

# Web workspace (unauthenticated loopback by default — see docs/web.md).
serve: build
	./strike serve --addr 127.0.0.1:8787

# LAN expose (WARNING: no TLS; authenticated with an auto-minted token).
serve-expose: build
	./strike serve --auth --expose

# Vite workspace: builds production assets embedded by the Go binary.
web-build:
	@if [ ! -f web/package.json ]; then echo "web-build: no web/package.json"; exit 0; fi
	cd web && npm ci && npm run build

web-test:
	@if [ ! -f web/package.json ]; then echo "web-test: no web/package.json"; exit 0; fi
	cd web && npm ci && npm test

web-check:
	@if [ ! -f web/package.json ]; then echo "web-check: no web/package.json"; exit 0; fi
	cd web && npm ci && npm run build && npm test

test: tui-gen
	go test ./...

# Failure-injection / chaos suite (#808). Also covered by `make test`.
# See docs/chaos.md.
chaos:
	go test ./internal/fault/ ./internal/session/ ./internal/tool/ ./internal/engine/ \
		-run 'Chaos|TestArm|TestCatalog|TestCheck|TestDisarm|TestConcurrent' -count=1

# E3.2 prompt regression report (also runs under `make test` via go test).
# Non-blocking metric deltas by default. After prompt.go / prompt_tools.go /
# prompt/*.txt / agent definition changes:
#   UPDATE_METRICS=1 make prompt-reg   # refresh testdata/metrics.json
#   PROMPT_REGRESSION_STRICT=1 make prompt-reg  # fail on deltas (future gate)
prompt-reg:
	go test ./internal/replay/ -run TestPromptRegressionReport -v -count=1

vet: tui-gen
	go vet ./...

# Per-package + total statement coverage. Writes $(COVER_PROFILE).
cover: tui-gen
	go test ./... -count=1 -coverprofile=$(COVER_PROFILE)
	@go tool cover -func=$(COVER_PROFILE) | awk '/^total:/{print}'
	@echo "per-package: go tool cover -func=$(COVER_PROFILE)"
	@echo "html:        go tool cover -html=$(COVER_PROFILE)"

# Fail if total statements % is below COVER_MIN (default $(COVER_MIN)).
cover-check: cover
	@go tool cover -func=$(COVER_PROFILE) | awk -v min="$(COVER_MIN)" '/^total:/{ \
		pct=$$3; gsub("%","",pct); \
		printf "cover-check: total %s%% (floor %s%%)\n", pct, min; \
		if (pct+0 < min+0) { print "cover-check: below COVER_MIN" > "/dev/stderr"; exit 1 } \
	}'

clean:
	rm -f strike $(COVER_PROFILE)
	rm -rf web/dist web/node_modules
