.PHONY: build run run-echo test vet cover cover-check clean setup

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

build:
	go build -ldflags "$(GO_LDFLAGS)" -o strike ./cmd/strike

# Creates ~/.strike (config, example agent + skill); never overwrites.
setup:
	bash scripts/setup.sh

# Runs with your configured/default provider (needs credentials; see README).
run: build
	./strike

# Offline dev loop — no API key needed.
run-echo: build
	./strike --provider echo

test:
	go test ./...

vet:
	go vet ./...

# Per-package + total statement coverage. Writes $(COVER_PROFILE).
cover:
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
