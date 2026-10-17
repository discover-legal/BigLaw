# syntax=docker/dockerfile:1
# BigLaw 2: one static binary. Postgres runs separately (docker-compose.yml).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY . .
ARG VERSION=2.0.0-dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/biglaw ./cmd/biglaw

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/biglaw /usr/local/bin/biglaw
# Inside the container the server must listen on every interface; publish the
# port on the host's loopback only unless auth is configured (README § Auth).
ENV BIGLAW_ADDR=0.0.0.0:3101
EXPOSE 3101
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/biglaw"]
CMD ["serve"]
