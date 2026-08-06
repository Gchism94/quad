# syntax=docker/dockerfile:1
#
# Cairn control plane: dashboard + server in one image.
#
# Build from the repository root:
#   docker build -t cairn:local .
#
# The image ships the docker CLI so grading can drive the host's container
# runtime through a mounted socket. It does NOT ship a daemon.

# --- stage 1: the instructor dashboard -------------------------------------
FROM node:22-alpine AS web
WORKDIR /src/web
# Copy manifests first so dependency installation caches independently of source.
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- stage 2: the control plane --------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# CGO is off: both drivers (pgx, modernc sqlite) are pure Go, so the binary is
# static and the runtime stage needs no libc compatibility shims.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cairn ./cmd/cairn

# --- stage 3: runtime -------------------------------------------------------
FROM alpine:3.21

# git   — the grading checkout clones student repos
# docker-cli — grading runs each spec in a sandboxed container via the mounted socket
# ca-certificates — TLS to the Git hosts
RUN apk add --no-cache ca-certificates git docker-cli

# Run as a fixed non-root uid. It must own the grading work directory on the
# HOST too, because that directory is bind-mounted at the same path on both
# sides — see deploy/docker-compose.yml and docs/deploy.md.
RUN adduser -D -u 1000 cairn

COPY --from=build /out/cairn /usr/local/bin/cairn
COPY --from=web /src/web/dist /srv/cairn/web

ENV CAIRN_WEB_DIR=/srv/cairn/web \
    CAIRN_LISTEN_ADDR=:8080

USER cairn
WORKDIR /srv/cairn
EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null || exit 1

ENTRYPOINT ["cairn"]
