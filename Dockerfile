# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum* ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /resillm ./cmd/resillm

# Runtime stage
FROM alpine:3.19

# Install ca-certificates for HTTPS
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -S resillm && adduser -S resillm -G resillm

WORKDIR /app

# Copy binary from builder
COPY --from=builder /resillm /app/resillm

# Copy default config
COPY config.example.yaml /app/config.yaml

# Set ownership
RUN chown -R resillm:resillm /app

USER resillm

# Expose ports
EXPOSE 8080 9090

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8080/health || exit 1

ENTRYPOINT ["/app/resillm"]
CMD ["--config", "/app/config.yaml"]
