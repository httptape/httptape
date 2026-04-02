# syntax=docker/dockerfile:1

# --- Builder stage ---
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# Static binary, no cgo — required for scratch base.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /usr/local/bin/httptape \
    ./cmd/httptape

# --- Final stage ---
FROM scratch

# Import CA certs so record mode can dial HTTPS upstreams.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /usr/local/bin/httptape /usr/local/bin/httptape

# Pre-create mount-point directories via empty copies.
VOLUME ["/fixtures", "/config"]

EXPOSE 8081

USER 65534

ENTRYPOINT ["httptape"]
