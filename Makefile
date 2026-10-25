# BigLaw 2 — `make ci` is what GitHub CI runs (.github/workflows/ci.yml); the
# CI lint job adds `make lint vuln`, and the docker job builds the image.
# Database tests need BIGLAW_TEST_DSN; set BIGLAW_REQUIRE_DB=1 to make a
# missing database a failure instead of a skip (CI does).
GO ?= go
VERSION ?= 2.0.0-dev
STATICCHECK ?= honnef.co/go/tools/cmd/staticcheck@2026.1
GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.1.4
# `go run tool@version` picks its toolchain from the tool's go.mod, so pin it
# to ours: a tool built with an older Go cannot analyse this module.
TOOLCHAIN = $(shell sed -n 's/^toolchain //p' go.mod)

# The UI (ui/, Node 22): `make ui` = what CI's ui job runs; `make demo-bundle` = the static recorded
# demo under /demo/biglaw/ (ui/dist-recorded); `make demo-record` re-records it (needs BIGLAW_TEST_DSN).
NPM ?= npm

.PHONY: eval ui-embed ui demo-bundle demo-record e4-record e4-live build static-check test fmt-check vet mod-check loc compose-check lint vuln ci clean

# A static binary: no cgo, so no libc to match on the target (Pi, distroless). It embeds the web
# UI when one is built (`make ui build`): ui/dist is copied into internal/api/dist first; without
# it, "/" explains how to build the UI and the API works as before.
build: ui-embed
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o biglaw ./cmd/biglaw

static-check: build
	@$(GO) version -m biglaw | grep -q 'CGO_ENABLED=0' && echo "biglaw: static (built with CGO_ENABLED=0)" \
		|| { echo "biglaw was built with cgo: not a static binary"; exit 1; }

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

mod-check:
	$(GO) mod verify
	$(GO) mod tidy -diff

# The reader child's memory cap is off under the race detector (its shadow memory counts too),
# so the child's bounds are also tested without -race, and so is the server's ingest memory
# (TestIngestMemoryBounded skips itself under -race).
# The second pass runs -v and, where the database is required (CI), fails unless each guard it
# exists for reports PASS: a renamed test must not silently run nowhere (PR 3a r4 L1). It also
# prints gate E4's verdict, and there requires a PASS or the "E4 GATE NOT JUDGED" skip (r1 L-6).
NONRACE = TestChildBoundsItself TestChildNearTextCap TestIngestMemoryBounded
# Database connections are bounded in the tests themselves: a test store's pool is storetest.Pool (4)
# and at most storetest.Slots (8) tests across all -p packages hold a database at once, so the peak
# stays under 50 of Postgres's default max_connections of 100 whatever -p and -parallel are (PR 55 CI;
# lab-r4 r9 L5). TEST_P and TEST_PARALLEL now bound only CPU (the race detector is slow on a loaded host).
TEST_P ?= 4
TEST_PARALLEL ?= 4
test:
	$(GO) test -race -count=1 -shuffle=on -timeout 5m -p $(TEST_P) -parallel $(TEST_PARALLEL) ./...
	@log=$$(mktemp); $(GO) test -count=1 -v -run '^(TestChild|TestIngestMemoryBounded$$|TestHybridRecallProofRuns$$)' ./internal/docs >$$log 2>&1; rc=$$?; \
	cat $$log; test $$rc = 0 || { rm -f $$log; exit $$rc; }; \
	echo "make test: $$(grep -o -m1 'E4 GATE NOT JUDGED: [^;]*\|E4 gate met with .*' $$log || echo 'E4: no verdict line (no database, or the gate did not run)')"; \
	if [ -n "$$BIGLAW_REQUIRE_DB" ]; then for t in $(NONRACE); do grep -q -- "^--- PASS: $$t " $$log || \
		{ echo "make test: $$t did not PASS in the non-race pass"; rm -f $$log; exit 1; }; done; \
		grep -q -- "^--- PASS: TestHybridRecallProofRuns " $$log || grep -q "E4 GATE NOT JUDGED" $$log || \
		{ echo "make test: TestHybridRecallProofRuns neither passed nor skipped as not judged"; rm -f $$log; exit 1; }; fi; rm -f $$log

# Gate E4 (README § Documents): record eval/e4/embeddings.json once with a real embedding model, or
# measure live without writing it. Owner-run; both need BIGLAW_TEST_DSN (a Postgres with pgvector)
# and, unless EMBED_MODEL=biglaw-selftest-hash (the offline self-test tape), BIGLAW_MODEL_URL (and
# BIGLAW_MODEL_KEY if the endpoint needs one). The tape is recorded at the model's own vector
# length, as serve embeds. Example: make e4-record EMBED_MODEL=text-embedding-3-small
e4-record e4-live:
	@test -n "$(EMBED_MODEL)" || { echo "set EMBED_MODEL"; exit 2; }
	BIGLAW_REQUIRE_DB=1 $(if $(filter e4-record,$@),E4_RECORD=1,E4_LIVE=1) EMBED_MODEL='$(EMBED_MODEL)' \
		$(GO) test -count=1 -v -timeout 30m -run '^TestHybridRecallProofRuns$$' ./internal/docs

ui:
	cd ui && $(NPM) ci && $(NPM) run build && $(NPM) test

ui-embed:
	@find internal/api/dist -mindepth 1 ! -name README.md -delete
	@if [ -f ui/dist/index.html ]; then cp -R ui/dist/. internal/api/dist/ && echo "embedding ui/dist"; \
	else echo "no ui/dist: building the API without the web UI (make ui first to embed it)"; fi

# Gates E0 and E3 (PLAN § Eval gates): fixture/answer-key integrity, the scorer's unit tests, and
# the scorer's self-test on synthetic oracle/degraded/empty runs. No backend, no model.
eval:
	python3 eval/run.py validate
	python3 -m unittest discover -s eval/tests
	python3 eval/run.py selftest

demo-bundle:
	cd ui && $(NPM) ci && $(NPM) run demo-bundle

demo-record:
	BIGLAW_REQUIRE_DB=1 DEMO_RECORD=1 $(GO) test -count=1 -run '^TestDemoRecording$$' ./eval/demo

loc:
	python3 scripts/loc.py
	python3 -m unittest discover -s scripts -p 'test_*.py'

# Docker is required where CI is set (GitHub sets CI=true); elsewhere a
# missing docker is reported as a SKIP.
compose-check:
	@if command -v docker >/dev/null 2>&1; then \
		POSTGRES_PASSWORD=compose-check docker compose config -q && echo "docker-compose.yml: valid"; \
	elif [ -n "$$CI" ]; then echo "compose-check: docker is required when CI is set"; exit 1; \
	else echo "SKIP compose-check: docker not installed (CI fails here instead)"; fi

lint:
	GOTOOLCHAIN=$(TOOLCHAIN) $(GO) run $(STATICCHECK) ./...

vuln:
	GOTOOLCHAIN=$(TOOLCHAIN) $(GO) run $(GOVULNCHECK) ./...

ci: fmt-check vet mod-check build static-check test eval loc compose-check

clean:
	rm -f biglaw
