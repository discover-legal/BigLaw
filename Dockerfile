# syntax=docker/dockerfile:1
# BigLaw 2: one static binary. Postgres runs separately (docker-compose.yml).
# The Go version matches go.mod's toolchain line and CI (TestGoVersionPinnedEverywhere).
FROM golang:1.25.14-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
ARG VERSION=2.0.0-dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/biglaw ./cmd/biglaw

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/biglaw /usr/local/bin/biglaw
# Apache-2.0 §4 and lib/pq's MIT terms: the notices travel with the binary.
COPY LICENSE NOTICE /usr/share/doc/biglaw/
# Inside a container the server must listen on every interface. Until auth
# lands (PR 8), `serve` refuses a non-loopback address unless it is started
# with --allow-no-auth, which docker-compose.yml does because it publishes the
# port on the host's loopback only. This image's default command does NOT
# pass it, so a plain `docker run -p` or a Render deploy refuses to start
# rather than serve an unauthenticated API (README § Exposure).
ENV BIGLAW_ADDR=0.0.0.0:3101
EXPOSE 3101
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/biglaw"]
CMD ["serve"]
