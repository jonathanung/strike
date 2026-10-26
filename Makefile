.PHONY: build run run-echo serve webui-build webui-check webui-uat webui-uat-smoke web-check test vet cover cover-check clean setup restore tui-gen prompt-reg chaos harness-eval swebench-eval telemetry-check protocol-schema protocol-schema-check container-smoke

# Multi-module workspace (go.work): ., ./pkg/protocol, ./pkg/redact,
# ./harness.
# `go test ./...` from the root does not descend into nested go.mod
# directories, so leaf modules are tested with `go -C dir test`.
# -C must be the first go flag. GOWORK=off still builds via replace.
LEAF_MODS = pkg/protocol pkg/redact harness

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
GO_LDFLAGS ?= -X github.com/jonathanung/strike/internal/product/version.Version=$(VERSION) -X github.com/jonathanung/strike/internal/product/version.Commit=$(COMMIT)

# Flatten internal/frontend/tui/app/_src/* into package tui (Go one-directory packages).
tui-gen:
	go generate ./internal/frontend/tui/app

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

# Rust (Leptos) web UI: builds webui/dist into the Go embed dir.
# Toolchain comes from `nix develop` (rustup + trunk per rust-toolchain.toml).
webui-build:
	@if ! command -v trunk >/dev/null; then echo "webui-build: trunk not found (run inside nix develop)"; exit 1; fi
	cd webui && trunk build --release
	rm -rf internal/frontend/server/static/assets
	mkdir -p internal/frontend/server/static/assets
	cp webui/dist/index.html webui/dist/sw.js webui/dist/manifest.webmanifest internal/frontend/server/static/
	cp -r webui/dist/assets/. internal/frontend/server/static/assets/ 2>/dev/null || true
	for f in webui/dist/*.wasm webui/dist/*.js webui/dist/*.css webui/dist/*.svg; do \
		[ -e "$$f" ] && cp "$$f" internal/frontend/server/static/assets/ || true; \
	done

webui-check:
	@if ! command -v cargo >/dev/null; then echo "webui-check: cargo not found (run inside nix develop)"; exit 1; fi
	cd webui && cargo check --target wasm32-unknown-unknown

# Legacy alias: the old JS web/ UI is removed; web-check now means the Rust UI.
web-check: webui-check

# Headless cockpit pass. See docs/webui-uat.md. Artifacts: webui-uat-artifacts/.
webui-uat: build
	node --test scripts/webui-uat/lib.test.mjs
	node scripts/webui-uat.mjs

# Same assertions as webui-uat, without the video. CI webui-smoke runs this
# after scripts/webui-smoke.mjs (auth). STRIKE_BIN defaults to ./strike.
webui-uat-smoke: build
	node --test scripts/webui-uat/lib.test.mjs
	node scripts/webui-uat.mjs --suite smoke --no-video

test: tui-gen
	go test ./...
	@for m in $(LEAF_MODS); do go -C $$m test ./... || exit 1; done

# Failure-injection / chaos suite (#808). Also covered by `make test`.
# See docs/chaos.md.
chaos:
	go test ./internal/persist/session/ \
		-run 'Chaos|TestArm|TestCatalog|TestCheck|TestDisarm|TestConcurrent' -count=1
	go -C harness test ./fault/ ./tool/ ./engine/ \
		-run 'Chaos|TestArm|TestCatalog|TestCheck|TestDisarm|TestConcurrent' -count=1

# E3.2 prompt regression report (also runs under `make test` via go test).
# Non-blocking metric deltas by default. After prompt.go / prompt_tools.go /
# prompt/*.txt / agent definition changes:
#   UPDATE_METRICS=1 make prompt-reg   # refresh testdata/metrics.json
#   PROMPT_REGRESSION_STRICT=1 make prompt-reg  # fail on deltas (future gate)
prompt-reg:
	go test ./internal/eval/replay/ -run TestPromptRegressionReport -v -count=1

# Harness regression pack (#807): correctness/safety/recovery/latency-cost
# scenarios plus #791/#782 recording consumption. Offline (echo/fixtures).
# Scenario failures are hard errors (also under `make test`). The verbose
# report is non-blocking in CI (continue-on-error). Path to blocking:
# drop continue-on-error on the CI "Harness eval report" step, and/or set
# HARNESS_EVAL_STRICT=1 for report-write failures.
#   HARNESS_EVAL_REPORT=path make harness-eval   # write JSON artifact
#   UPDATE_HARNESS_EVAL=1 make harness-eval      # refresh testdata sample
harness-eval:
	go test ./internal/eval/replay/ -run 'TestHarnessEvalSuite|TestBuildEvalReport' -v -count=1

# Op/Event JSON Schema + Rust types (#1306). Writes schemas/protocol/v1 and
# webui/protocol-schema/src/lib.rs from pkg/protocol.
protocol-schema:
	UPDATE_SCHEMA=1 go -C pkg/protocol test -count=1 -run 'TestSchemaDrift'

# Fails when those artifacts drift, and when the generated Rust types cannot
# decode the golden event stream (including an unknown event).
protocol-schema-check:
	go -C pkg/protocol test -count=1 -run 'TestSchema'
	cargo test --manifest-path webui/protocol-schema/Cargo.toml --locked

# Security/harness telemetry schema drift gate (#894).
# Fails when schemas/telemetry/v1/registry.json diverges from Go export
# structs or the embedded pkg/telemetry/registry.json copy.
telemetry-check:
	go test ./pkg/telemetry/ -run 'TestRegistry|TestEmbedded|TestDisk|TestGolden|TestRedact' -count=1

# E3.3 SWE-bench Verified subset runner (#561). Offline unit tests always;
# full Docker+model runs are manual / nightly (large images, API cost).
#   make swebench-eval                    # package tests
#   ./strike eval swebench --dry-run ...  # wiring check
#   ./strike eval swebench --provider …   # real subset run → evals/swebench/results/
swebench-eval:
	go test ./internal/eval/swebench/ ./cmd/strike/ -run 'SWEBench|Subset|EvalSWE|EvalCLI' -count=1

vet: tui-gen
	go vet ./...
	@for m in $(LEAF_MODS); do go -C $$m vet ./... || exit 1; done

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

# Optional live docker smoke (E12.8): build default Dockerfile and run strike version inside.
# Not part of default CI; requires docker/podman on PATH.
container-smoke: build
	@command -v docker >/dev/null || command -v podman >/dev/null || { echo "container-smoke: no docker/podman"; exit 1; }
	@tmpdir=$$(mktemp -d); 	  ./strike container eject --out "$$tmpdir/Dockerfile" 2>/dev/null || true; 	  if [ ! -f "$$tmpdir/Dockerfile" ]; then echo "FROM ubuntu:24.04" > "$$tmpdir/Dockerfile"; fi; 	  engine=docker; command -v docker >/dev/null || engine=podman; 	  $$engine build -t strike-smoke:local -f "$$tmpdir/Dockerfile" "$$tmpdir"; 	  $$engine run --rm strike-smoke:local true; 	  rm -rf "$$tmpdir"; 	  echo "container-smoke: ok"

clean:
	rm -f strike $(COVER_PROFILE)
	rm -rf webui/dist webui/target

# Recompute flake.nix vendorHash after go.mod/go.sum changes (CI also does this).
.PHONY: nix-vendor-hash
nix-vendor-hash:
	./scripts/update-nix-vendor-hash.sh
