package httptape

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// BuildTLSConfig constructs a *tls.Config from optional PEM file paths and an
// insecure flag. All parameters are optional; an all-zero call returns nil
// (meaning "use Go defaults").
//
// Parameters:
//   - certFile, keyFile: PEM-encoded client certificate and private key for
//     mTLS. Both must be provided together; providing one without the other
//     returns an error.
//   - caFile: PEM-encoded CA certificate(s) to use as the root CA pool for
//     verifying the upstream server. When empty, the system root CAs are used.
//   - insecure: when true, sets InsecureSkipVerify on the returned config.
//
// Returns nil, nil when all parameters are zero-valued (no custom TLS needed).
func BuildTLSConfig(certFile, keyFile, caFile string, insecure bool) (*tls.Config, error) {
	// Validate cert/key pairing: both or neither.
	if certFile != "" && keyFile == "" {
		return nil, fmt.Errorf("httptape: --tls-cert requires --tls-key")
	}
	if keyFile != "" && certFile == "" {
		return nil, fmt.Errorf("httptape: --tls-key requires --tls-cert")
	}

	// If nothing is configured, return nil (use Go defaults).
	if certFile == "" && keyFile == "" && caFile == "" && !insecure {
		return nil, nil
	}

	cfg := &tls.Config{}

	// Load client certificate for mTLS.
	if certFile != "" && keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("httptape: load client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}

	// Load custom CA.
	if caFile != "" {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("httptape: read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("httptape: no valid certificates found in CA file %q", caFile)
		}
		cfg.RootCAs = pool
	}

	// Insecure skip verify.
	if insecure {
		cfg.InsecureSkipVerify = true
	}

	return cfg, nil
}
