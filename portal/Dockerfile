# SiYuan self-host portal image.
#
# Two-stage build: golang-alpine for compilation (needs CGO + sqlite), alpine:latest for
# runtime. The final image is small (~25 MB) and runs as an unprivileged user by default.
# When deployed via docker-compose, the portal container talks to the Docker daemon via
# tecnativa/docker-socket-proxy on an internal network — see docker-compose.yml.
#
# Build context MUST be the repo root so the portal/ subdirectory is reachable:
#
#   docker build -f portal/Dockerfile -t siyuan-portal:latest .

FROM golang:1.25-alpine AS build

# CGO is required by github.com/mattn/go-sqlite3. Alpine needs gcc + musl-dev for CGO.
RUN apk add --no-cache gcc musl-dev

WORKDIR /src
COPY portal/go.mod portal/go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build --mount=type=cache,target=/go/pkg \
    go mod download

COPY portal/ ./
RUN --mount=type=cache,target=/root/.cache/go-build --mount=type=cache,target=/go/pkg \
    CGO_ENABLED=1 go build -ldflags "-s -w" -o /out/portal ./cmd/portal

FROM alpine:latest
LABEL org.opencontainers.image.description="SiYuan self-host portal: login, user management, reverse proxy"

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -g 1000 -S portal && \
    adduser  -u 1000 -S portal -G portal && \
    mkdir -p /var/lib/portal && \
    chown portal:portal /var/lib/portal

COPY --from=build --chown=portal:portal /out/portal /usr/local/bin/portal

USER portal
WORKDIR /var/lib/portal
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/portal"]
