# Go may live off-PATH (e.g. ~/.local/go/bin); resolve it once here.
GO ?= $(shell command -v go 2>/dev/null || echo $(HOME)/.local/go/bin/go)

.PHONY: build test run

# Prebuilt binary used by .mcp.json (Claude Code MCP) and local runs.
# Rebuild after any change under biglaw-go/.
build:
	cd biglaw-go && $(GO) build -o ../bin/biglaw ./cmd/biglaw

test:
	cd biglaw-go && $(GO) test ./...

# Run from the repo root so templates/ and deadlines/rules/ resolve.
run: build
	./bin/biglaw
