# Build Stage
FROM golang:1.22-alpine AS builder

WORKDIR /build

# Install git (needed for go mod download if dependencies existed, though we use stdlib)
RUN apk add --no-cache git

# Copy go mod files (even if empty, good practice)
COPY go.mod ./
# If you had dependencies: RUN go mod download

# Copy source code
COPY . .

# Build the binary
# -ldflags="-w -s" strips debug symbols for smaller size
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags="-w -s" -o pod-server .

# Runtime Stage
FROM alpine:latest

WORKDIR /app

# Install ca-certificates for HTTPS (if needed later) and tzdata
RUN apk --no-cache add ca-certificates tzdata

# Copy the binary from builder
COPY --from=builder /build/pod-server .

# Copy static assets and templates
COPY --from=builder /build/static ./static
COPY --from=builder /build/templates ./templates
COPY --from=builder /build/storage ./storage

# Create a non-root user for security
RUN adduser -D -g '' appuser
USER appuser

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget --quiet --tries=1 --spider http://localhost:8080/ || exit 1

# Run the server
CMD ["./pod-server"]