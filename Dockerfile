# Dockerfile — builds the full JustRAG stack (Go backend + React frontend).
# Context must be the project root so both web/ and go-backend/ are accessible.
#
# Build:
#   docker build -t justrag .
#
# Or use docker-compose.yml which sets context: . automatically.
#
# For the legacy Node.js backend, see Dockerfile.legacy.

# ── Stage 1: Build the React frontend ────────────────────────────────────────
FROM node:20-alpine AS frontend
WORKDIR /app

# Copy workspace manifests first for layer caching
COPY package.json package-lock.json ./
COPY web/package.json ./web/

RUN npm ci --workspace=web

COPY web/ ./web/

# Build the frontend — outputs to web/dist/
RUN cd web && npm run build

# ── Stage 2: Build the Go binaries ───────────────────────────────────────────
FROM golang:1.26-alpine AS builder
WORKDIR /build

# Default to a mirror that serves module zips directly. Networks here block
# storage.googleapis.com (Go's default zip CDN), which breaks `go mod download`.
# Override for faster downloads on unblocked networks:
#   docker build --build-arg GOPROXY=https://proxy.golang.org,direct .
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY}

COPY go-backend/go.mod go-backend/go.sum ./
RUN go mod download

COPY go-backend/ .

ARG BUILD_VERSION=unknown
RUN CGO_ENABLED=0 GOOS=linux go build \
    -tags chromium \
    -trimpath -ldflags "-s -w -X main.version=${BUILD_VERSION}" \
    -o /server ./cmd/server/

RUN CGO_ENABLED=0 GOOS=linux go build \
    -tags chromium \
    -trimpath -ldflags "-s -w" \
    -o /worker ./cmd/worker/

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags "-s -w" \
    -o /migrate ./cmd/migrate/

# ── Stage 3: Runtime image ───────────────────────────────────────────────────
FROM alpine:3.20
RUN apk --no-cache add \
    ca-certificates curl \
    poppler-utils \
    tesseract-ocr tesseract-ocr-data-deu tesseract-ocr-data-eng \
    # Chromium and required libraries for Tier 2/3 headless browser fetches.
    chromium nss freetype harfbuzz ttf-freefont

WORKDIR /app

# Chromium is launched with --no-sandbox inside this image
# (fetcher.Config.AllowNoSandbox). That is the in-container posture and is
# independent of the process UID — see the non-root USER below. Bare-metal
# deployments (go run) do NOT set this env var and keep Chromium's OS sandbox
# enabled.
ENV FETCHER_ALLOW_NO_SANDBOX=true

# Point rod at the system Chromium (installed via apk above) so it does
# NOT fall back to downloading its own Chromium bundle (~300 MB) from
# Google into ~/.cache/rod on the first Tier 2 request. Without this,
# cold starts of the crawl / academic-search endpoints stall for
# minutes while rod downloads over the public internet.
ENV FETCHER_BROWSER_BIN=/usr/bin/chromium

# Rod's leakless guardian binary is extracted to $TMPDIR (default /tmp)
# and executed from there. Some servers mount /tmp with noexec, which
# causes "permission denied" on the fork/exec. Redirect TMPDIR into
# /app/tmp so the binary lands on a filesystem that allows execution.
RUN mkdir -p /app/tmp
ENV TMPDIR=/app/tmp

COPY --from=builder /server  /app/server
COPY --from=builder /worker  /app/worker
COPY --from=builder /migrate /app/migrate

# Pre-built React SPA — served by Go at /client/dist in production
COPY --from=frontend /app/web/dist /app/client/dist

# Run as an unprivileged user. Chromium still uses --no-sandbox (see
# FETCHER_ALLOW_NO_SANDBOX above) — that is independent of the process UID;
# dropping root removes the root attack surface. The app user must own /app
# (and /app/tmp, where rod's leakless guardian execs from TMPDIR).
RUN addgroup -S app && adduser -S -G app -h /app app \
    && chown -R app:app /app
USER app

EXPOSE 3000
CMD ["/app/server"]
