# Multi-stage build for optimal image size
FROM golang:1.25-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /build

# Copy go mod files first (better caching)
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o health-checker

# Final stage - minimal runtime image
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN addgroup -g 1000 healthchecker && \
    adduser -D -u 1000 -G healthchecker healthchecker

# Set working directory
WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/health-checker .

# Copy certificates
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Set ownership
RUN chown -R healthchecker:healthchecker /app

# Switch to non-root user
USER healthchecker

# Expose UDP port for health checks
EXPOSE 8080/udp

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD netstat -ul | grep :8080 || exit 1

# Run the application
ENTRYPOINT ["./health-checker"]
CMD ["config.yaml"]