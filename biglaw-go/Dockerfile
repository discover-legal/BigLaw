# SPDX-License-Identifier: AGPL-3.0-only
# BigLaw Go core. Build context must be the repository root:
#   docker build -f biglaw-go/Dockerfile .
#
# Open, standards-based packaging:
#   • multi-stage, static (CGO disabled) → portable single binary, no glibc
#   • reproducible: -trimpath strips local paths, -buildid= clears the build id;
#     pass --build-arg SOURCE_DATE_EPOCH and OCI provenance args from CI
#   • rootless: runs as a fixed non-root numeric UID (k8s runAsNonRoot-friendly)
#   • OCI image-spec annotations below; generate an SBOM + provenance at build
#     time with:  docker buildx build --sbom=true --provenance=true ...
#     (the binary also embeds its full module manifest: `go version -m biglaw`)

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY biglaw-go/go.mod biglaw-go/go.sum ./
RUN go mod download
COPY biglaw-go/ ./
# -buildid= makes the output bit-for-bit reproducible across builds.
RUN CGO_ENABLED=0 GOFLAGS=-trimpath go build -ldflags="-s -w -buildid=" -o /out/biglaw ./cmd/biglaw

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata wget \
    && addgroup -g 65532 -S biglaw \
    && adduser -u 65532 -S biglaw -G biglaw
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

USER 65532:65532
EXPOSE 3101
ENV API_HOST=0.0.0.0 \
    BIG_MICHAEL_MODE=backend \
    RUVECTOR_DATA_DIR=/app/data

HEALTHCHECK --interval=15s --timeout=3s --start-period=10s \
  CMD wget -qO- http://127.0.0.1:3101/health || exit 1

ENTRYPOINT ["biglaw"]

# ─── OCI image-spec annotations (https://github.com/opencontainers/image-spec) ──
# CI should override the dynamic ones: --build-arg VERSION/REVISION/CREATED.
ARG VERSION=dev
ARG REVISION=unknown
ARG CREATED=1970-01-01T00:00:00Z
LABEL org.opencontainers.image.title="BigLaw" \
      org.opencontainers.image.description="Open, free, multi-agent legal AI orchestration platform" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.source="https://github.com/discover-legal/BigLaw" \
      org.opencontainers.image.documentation="https://github.com/discover-legal/BigLaw#readme" \
      org.opencontainers.image.vendor="Discover Legal" \
      org.opencontainers.image.base.name="docker.io/library/alpine:3.21" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}" \
      org.opencontainers.image.created="${CREATED}"
