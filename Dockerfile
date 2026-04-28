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

# Build-arg (overridden by CI on tag pushes; defaults to "dev" for local
# builds and non-tag CI runs).
ARG VERSION=dev

# Import CA certs so record mode can dial HTTPS upstreams.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /usr/local/bin/httptape /usr/local/bin/httptape

# Pre-create mount-point directories via empty copies.
VOLUME ["/fixtures", "/config"]

EXPOSE 8081

USER 65534

# OCI image labels. See https://github.com/opencontainers/image-spec/blob/main/annotations.md
# Note: docker/metadata-action@v5 in CI also emits labels; for overlapping
# keys, the metadata-action value overrides on the published image. These
# Dockerfile labels are the floor that guarantees metadata exists on local
# `docker build .` and on downstream re-builds.
LABEL org.opencontainers.image.title="httptape" \
      org.opencontainers.image.description="HTTP traffic recording, redaction, and replay — embeddable Go library, CLI, and 3 MB Docker image." \
      org.opencontainers.image.source="https://github.com/httptape/httptape" \
      org.opencontainers.image.url="https://github.com/httptape/httptape" \
      org.opencontainers.image.documentation="https://vibewarden.dev/docs/httptape/" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"

ENTRYPOINT ["httptape"]
