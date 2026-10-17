# BigLaw 2 — `make ci` is what GitHub CI runs (.github/workflows/ci.yml).
# Database tests need BIGLAW_TEST_DSN; set BIGLAW_REQUIRE_DB=1 to make a
# missing database a failure instead of a skip (CI does).
GO ?= go

.PHONY: build test fmt-check vet loc compose-check ci clean

build:
	$(GO) build -trimpath -o biglaw ./cmd/biglaw

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

test:
	$(GO) test -race -count=1 ./...

loc:
	python3 scripts/loc.py

compose-check:
	@if command -v docker >/dev/null 2>&1; then \
		POSTGRES_PASSWORD=compose-check docker compose config -q && echo "docker-compose.yml: valid"; \
	else echo "SKIP compose-check: docker not installed"; fi

ci: fmt-check vet build test loc compose-check

clean:
	rm -f biglaw
