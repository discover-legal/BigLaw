# SPDX-License-Identifier: AGPL-3.0-only
# BigLaw Go core. Build context must be the repository root:
#   docker build -f biglaw-go/Dockerfile .

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY biglaw-go/go.mod biglaw-go/go.sum ./
RUN go mod download
COPY biglaw-go/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/biglaw ./cmd/biglaw

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -S biglaw && adduser -S biglaw -G biglaw
WORKDIR /app

COPY --from=build /out/biglaw /usr/local/bin/biglaw

# Config payloads loaded at startup (templates, Lavern/MikeOSS workflows,
# Lavern agents, external plugin adapters, jurisdiction deadline rules).
COPY templates/       ./templates/
COPY workflows/       ./workflows/
COPY adapters/        ./adapters/
COPY agents/lavern/   ./agents/lavern/
COPY deadlines/rules/ ./deadlines/rules/

# /run/biglaw is shared with the sidecar container via a named volume; 1777
# (sticky, /tmp-style) so either uid can create the socket regardless of which
# image initialises the volume.
RUN mkdir -p /app/data /run/biglaw && chown -R biglaw:biglaw /app && chmod 1777 /run/biglaw

USER biglaw
EXPOSE 3101
ENV API_HOST=0.0.0.0 \
    BIG_MICHAEL_MODE=backend \
    RUVECTOR_DATA_DIR=/app/data

HEALTHCHECK --interval=15s --timeout=3s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:3101/health || exit 1

ENTRYPOINT ["biglaw"]
