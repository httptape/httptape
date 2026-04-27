# TLS / mTLS Configuration

httptape supports TLS configuration for both **outbound** connections
(httptape to upstream) and **inbound** connections (clients to httptape).

## Inbound TLS (listener)

httptape can listen on HTTPS instead of plain HTTP. This is useful when
clients require TLS (e.g., mobile SDKs that reject plain HTTP, or
browser-based dev tools with mixed-content restrictions).

Two modes are available:

| Mode | Use case | Flags |
|------|----------|-------|
| **Self-signed** | Quick local dev/CI -- no cert files needed | `--tls-listener-auto` |
| **Explicit cert** | Bring your own PEM cert/key pair | `--tls-listener-cert` + `--tls-listener-key` |

These flags are available on the `serve`, `record`, and `proxy` commands.
The existing `--tls-*` flags (outbound) are unrelated and continue to work.

### Self-signed (auto-generate)

```bash
httptape serve \
  --fixtures ./fixtures \
  --tls-listener-auto
```

At startup, httptape generates an ECDSA P-256 self-signed certificate
(24h validity, 1h clock-skew window) covering the default SANs
`localhost`, `127.0.0.1`, and `::1`. The SHA-256 fingerprint is printed
to stderr.

To customize the SANs:

```bash
httptape serve \
  --fixtures ./fixtures \
  --tls-listener-auto \
  --tls-listener-san "myhost.local,10.0.0.1"
```

!!! warning "For development and tests only"
    Self-signed certificates are not trusted by default. Clients must
    either skip verification or programmatically trust the certificate.
    Never use self-signed certificates in production.

### Explicit certificate

```bash
httptape serve \
  --fixtures ./fixtures \
  --tls-listener-cert /path/to/server.crt \
  --tls-listener-key  /path/to/server.key
```

### Mutual exclusion

`--tls-listener-auto` is mutually exclusive with
`--tls-listener-cert`/`--tls-listener-key`. Using both produces a
usage error. `--tls-listener-san` requires `--tls-listener-auto`.

### Go library usage (GenerateSelfSignedCert)

The `GenerateSelfSignedCert` function generates a self-signed certificate
for programmatic use in tests:

```go
import "github.com/VibeWarden/httptape"

sc, err := httptape.GenerateSelfSignedCert("localhost", "127.0.0.1")
if err != nil {
    log.Fatal(err)
}

// Use sc.TLSCertificate in a tls.Config for the listener.
srv := &http.Server{
    Handler: myHandler,
    TLSConfig: &tls.Config{
        Certificates: []tls.Certificate{sc.TLSCertificate},
    },
}

// Trust the cert in test clients via sc.CertPEM.
pool := x509.NewCertPool()
pool.AppendCertsFromPEM(sc.CertPEM)
client := &http.Client{
    Transport: &http.Transport{
        TLSClientConfig: &tls.Config{RootCAs: pool},
    },
}
```

With no arguments, `GenerateSelfSignedCert()` covers `localhost`,
`127.0.0.1`, and `::1`.

## Outbound TLS (upstream)

httptape supports custom TLS configuration for **outbound** connections
(httptape to upstream). This enables recording and proxying through backends
that use self-signed certificates, internal CAs, or mutual TLS (mTLS).

## Four levels of outbound TLS

| Level | Use case | Configuration |
|-------|----------|---------------|
| **Basic TLS** | Upstream uses HTTPS with a publicly trusted certificate | None -- works out of the box |
| **Custom CA** | Upstream uses a self-signed or internal CA certificate | `--tls-ca` |
| **mTLS** | Upstream requires a client certificate | `--tls-cert` + `--tls-key` (+ optional `--tls-ca`) |
| **Skip verify** | Dev shortcut when certs are broken or self-signed | `--tls-insecure` |

## CLI usage

### Custom CA

```bash
httptape record \
  --upstream https://internal-api.corp:8443 \
  --fixtures ./fixtures \
  --tls-ca /path/to/internal-ca.pem
```

### Mutual TLS (mTLS)

```bash
httptape proxy \
  --upstream https://secure-api.corp:8443 \
  --fixtures ./fixtures \
  --tls-cert /path/to/client.crt \
  --tls-key  /path/to/client.key \
  --tls-ca   /path/to/internal-ca.pem
```

### Skip TLS verification (dev only)

```bash
httptape record \
  --upstream https://localhost:8443 \
  --fixtures ./fixtures \
  --tls-insecure
```

!!! warning
    `--tls-insecure` disables all certificate verification. **Never use this in
    production.** A warning is printed to stderr when this flag is active.

## Go library usage

### BuildTLSConfig helper

The `BuildTLSConfig` function converts file paths into a `*tls.Config`:

```go
import "github.com/VibeWarden/httptape"

// Custom CA only
tlsCfg, err := httptape.BuildTLSConfig("", "", "/path/to/ca.pem", false)

// mTLS with custom CA
tlsCfg, err := httptape.BuildTLSConfig(
    "/path/to/client.crt",
    "/path/to/client.key",
    "/path/to/ca.pem",
    false,
)

// Skip verification (dev only)
tlsCfg, err := httptape.BuildTLSConfig("", "", "", true)
```

When all arguments are zero-valued, `BuildTLSConfig` returns `nil, nil` (use Go
defaults).

### Recorder with TLS

```go
tlsCfg, err := httptape.BuildTLSConfig("", "", "ca.pem", false)
if err != nil {
    log.Fatal(err)
}

store, _ := httptape.NewFileStore(httptape.WithDirectory("fixtures"))
rec := httptape.NewRecorder(store, httptape.WithRecorderTLSConfig(tlsCfg))
defer rec.Close()

client := &http.Client{Transport: rec}
resp, err := client.Get("https://internal-api.corp:8443/v1/data")
```

### Proxy with TLS

```go
tlsCfg, err := httptape.BuildTLSConfig("client.crt", "client.key", "ca.pem", false)
if err != nil {
    log.Fatal(err)
}

l1 := httptape.NewMemoryStore()
l2, _ := httptape.NewFileStore(httptape.WithDirectory("fixtures"))
proxy := httptape.NewProxy(l1, l2, httptape.WithProxyTLSConfig(tlsCfg))

client := &http.Client{Transport: proxy}
```

## Docker

When running httptape in Docker, mount your certificate files into the
container:

```bash
docker run -v /host/certs:/certs:ro \
  httptape record \
  --upstream https://backend:8443 \
  --fixtures /data/fixtures \
  --tls-cert /certs/client.crt \
  --tls-key  /certs/client.key \
  --tls-ca   /certs/ca.pem
```

## Troubleshooting

### "x509: certificate signed by unknown authority"

The upstream certificate is not trusted. Provide the CA certificate with
`--tls-ca`, or use `--tls-insecure` as a temporary workaround.

### "x509: certificate has expired or is not yet valid"

The upstream (or client) certificate is outside its validity window. Check the
`NotBefore` and `NotAfter` fields with:

```bash
openssl x509 -in cert.pem -noout -dates
```

### "tls: private key does not match public key"

The `--tls-cert` and `--tls-key` files do not form a valid pair. Verify with:

```bash
openssl x509 -in client.crt -noout -modulus | md5
openssl ec    -in client.key -noout         -modulus 2>/dev/null | md5
# (Use openssl rsa for RSA keys)
```

### "--tls-cert requires --tls-key" (or vice versa)

Client certificate and key must be provided together. Supply both or neither.
