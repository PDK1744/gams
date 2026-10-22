# syntax=docker/dockerfile:1

# Stage 1: Build static pure-Go binary
FROM golang:1.24-alpine AS builder

WORKDIR /build

# Install CA certificates for outgoing HTTPS calls
RUN apk add --no-cache ca-certificates

# Copy go module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source tree
COPY . .

# Compile static binary with CGO disabled (pure-Go SQLite)
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w -X main.Version=1.0.0" \
    -o /build/bin/gams ./cmd/gams

# Stage 2: Distroless static runtime
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/bin/gams /usr/local/bin/gams
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Expose default HTTP/MCP port
EXPOSE 8080

# Mountable persistent storage directory
VOLUME ["/data"]

ENV GAMS_DATA_PATH=/data/gams.db
ENV GAMS_SERVER_HOST=0.0.0.0
ENV GAMS_SERVER_HTTP_PORT=8080

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/gams"]
CMD ["serve"]
