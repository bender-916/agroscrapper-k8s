# Build stage
FROM golang:1.23-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build with CGO enabled (pgx doesn't strictly need it, but some dependencies might)
# pgx/v5 is pure Go, so CGO_ENABLED=0 works!
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o agroscrapper .

# Runtime stage - use distroless/cc for glibc (pgx compatible)
FROM gcr.io/distroless/cc-debian12:latest

# Copy CA certificates and timezone data
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy binary
COPY --from=builder /app/agroscrapper /agroscrapper

ENTRYPOINT ["/agroscrapper"]
