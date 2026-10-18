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

.PHONY: build static-check test fmt-check vet mod-check loc compose-check lint vuln ci clean

# A static binary: no cgo, so no libc to match on the target (Pi, distroless).
build:
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
# so the child's bounds are also tested without -race.
test:
	$(GO) test -race -count=1 -shuffle=on -timeout 5m ./...
	$(GO) test -count=1 -run '^TestChild' ./internal/docs

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

ci: fmt-check vet mod-check build static-check test loc compose-check

clean:
	rm -f biglaw
