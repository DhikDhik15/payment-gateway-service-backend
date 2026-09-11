# ── Stage 1: Builder ──────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

# Install build dependencies.
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Prevent Go from trying to download a newer toolchain at build time.
ENV GOTOOLCHAIN=local

# Copy dependency manifests first for better layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Copy source.
COPY . .

# Build a statically linked binary.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-w -s" -o /app/bin/server ./cmd/server

# ── Stage 2: Runtime ──────────────────────────────────────────────────────────
FROM alpine:3.20

# Pull in timezone data and trusted CA certificates from builder.
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Create a non-root user to run the service.
RUN addgroup -S appgroup && adduser -S appuser -G appgroup

WORKDIR /app

# Copy binary from builder stage.
COPY --from=builder /app/bin/server .

# Copy migrations so they're available inside the container.
COPY migrations ./migrations

USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD wget -qO- http://localhost:8080/health || exit 1

ENTRYPOINT ["./server"]
