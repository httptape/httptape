package httptape

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCert holds PEM-encoded certificate and key bytes generated for testing.
type testCert struct {
	CertPEM []byte
	KeyPEM  []byte
	Cert    *x509.Certificate
	Key     *ecdsa.PrivateKey
}

// generateTestCA creates a self-signed CA certificate for testing.
func generateTestCA(t *testing.T) testCert {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal CA key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return testCert{CertPEM: certPEM, KeyPEM: keyPEM, Cert: cert, Key: key}
}

// generateTestLeaf creates a leaf certificate signed by the given CA.
func generateTestLeaf(t *testing.T, ca testCert) testCert {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "Test Client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse leaf certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return testCert{CertPEM: certPEM, KeyPEM: keyPEM, Cert: cert, Key: key}
}

// writePEM writes PEM data to a temp file and returns the path.
func writePEM(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestBuildTLSConfig_NoArgs(t *testing.T) {
	cfg, err := BuildTLSConfig("", "", "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatal("expected nil config when no args provided")
	}
}

func TestBuildTLSConfig_CertWithoutKey(t *testing.T) {
	_, err := BuildTLSConfig("cert.pem", "", "", false)
	if err == nil {
		t.Fatal("expected error when cert provided without key")
	}
}

func TestBuildTLSConfig_KeyWithoutCert(t *testing.T) {
	_, err := BuildTLSConfig("", "key.pem", "", false)
	if err == nil {
		t.Fatal("expected error when key provided without cert")
	}
}

func TestBuildTLSConfig_InsecureOnly(t *testing.T) {
	cfg, err := BuildTLSConfig("", "", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config with insecure=true")
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("expected InsecureSkipVerify=true")
	}
}

func TestBuildTLSConfig_CertAndKey(t *testing.T) {
	ca := generateTestCA(t)
	leaf := generateTestLeaf(t, ca)

	dir := t.TempDir()
	certPath := writePEM(t, dir, "client.crt", leaf.CertPEM)
	keyPath := writePEM(t, dir, "client.key", leaf.KeyPEM)

	cfg, err := BuildTLSConfig(certPath, keyPath, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
}

func TestBuildTLSConfig_CAOnly(t *testing.T) {
	ca := generateTestCA(t)

	dir := t.TempDir()
	caPath := writePEM(t, dir, "ca.crt", ca.CertPEM)

	cfg, err := BuildTLSConfig("", "", caPath, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.RootCAs == nil {
		t.Fatal("expected non-nil RootCAs")
	}
}

func TestBuildTLSConfig_AllOptions(t *testing.T) {
	ca := generateTestCA(t)
	leaf := generateTestLeaf(t, ca)

	dir := t.TempDir()
	certPath := writePEM(t, dir, "client.crt", leaf.CertPEM)
	keyPath := writePEM(t, dir, "client.key", leaf.KeyPEM)
	caPath := writePEM(t, dir, "ca.crt", ca.CertPEM)

	cfg, err := BuildTLSConfig(certPath, keyPath, caPath, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("expected 1 certificate, got %d", len(cfg.Certificates))
	}
	if cfg.RootCAs == nil {
		t.Fatal("expected non-nil RootCAs")
	}
	if !cfg.InsecureSkipVerify {
		t.Fatal("expected InsecureSkipVerify=true")
	}
}

func TestBuildTLSConfig_NonExistentCertFile(t *testing.T) {
	_, err := BuildTLSConfig("/nonexistent/cert.pem", "/nonexistent/key.pem", "", false)
	if err == nil {
		t.Fatal("expected error for non-existent cert file")
	}
}

func TestBuildTLSConfig_NonExistentCAFile(t *testing.T) {
	_, err := BuildTLSConfig("", "", "/nonexistent/ca.pem", false)
	if err == nil {
		t.Fatal("expected error for non-existent CA file")
	}
}

func TestBuildTLSConfig_InvalidPEMCA(t *testing.T) {
	dir := t.TempDir()
	badCA := filepath.Join(dir, "bad-ca.pem")
	if err := os.WriteFile(badCA, []byte("not valid PEM"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := BuildTLSConfig("", "", badCA, false)
	if err == nil {
		t.Fatal("expected error for invalid PEM in CA file")
	}
}

// TestRecorderTLSIntegration verifies that a Recorder can record from an
// HTTPS server when configured with the server's CA certificate.
func TestRecorderTLSIntegration(t *testing.T) {
	// Start a TLS test server.
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello tls"))
	}))
	defer ts.Close()

	// Extract the server's CA certificate and write it to a temp file.
	serverCert := ts.TLS.Certificates[0]
	parsed, err := x509.ParseCertificate(serverCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: parsed.Raw})

	dir := t.TempDir()
	caPath := writePEM(t, dir, "server-ca.crt", caPEM)

	tlsCfg, err := BuildTLSConfig("", "", caPath, false)
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}

	store := NewMemoryStore()
	recorder := NewRecorder(store,
		WithAsync(false),
		WithRecorderTLSConfig(tlsCfg),
	)
	defer recorder.Close()

	client := &http.Client{Transport: recorder}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != "hello tls" {
		t.Fatalf("expected 'hello tls', got %q", string(body))
	}

	// Verify tape was recorded.
	tapes, err := store.List(t.Context(), Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape, got %d", len(tapes))
	}
}

// TestProxyTLSIntegration verifies that a Proxy can forward to an HTTPS
// server when configured with the server's CA certificate.
func TestProxyTLSIntegration(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello proxy tls"))
	}))
	defer ts.Close()

	serverCert := ts.TLS.Certificates[0]
	parsed, err := x509.ParseCertificate(serverCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: parsed.Raw})

	dir := t.TempDir()
	caPath := writePEM(t, dir, "server-ca.crt", caPEM)

	tlsCfg, err := BuildTLSConfig("", "", caPath, false)
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}

	l1 := NewMemoryStore()
	l2 := NewMemoryStore()
	proxy, err := NewProxy(l1, l2, WithProxyTLSConfig(tlsCfg))
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Transport: proxy}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if string(body) != "hello proxy tls" {
		t.Fatalf("expected 'hello proxy tls', got %q", string(body))
	}

	// Verify tapes were saved.
	tapes, err := l1.List(t.Context(), Filter{})
	if err != nil {
		t.Fatalf("l1.List: %v", err)
	}
	if len(tapes) != 1 {
		t.Fatalf("expected 1 tape in L1, got %d", len(tapes))
	}
}

// TestWithRecorderTLSConfig_InsecureSkipVerify verifies the insecure shortcut.
func TestWithRecorderTLSConfig_InsecureSkipVerify(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("insecure ok"))
	}))
	defer ts.Close()

	tlsCfg, err := BuildTLSConfig("", "", "", true)
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}

	store := NewMemoryStore()
	recorder := NewRecorder(store,
		WithAsync(false),
		WithRecorderTLSConfig(tlsCfg),
	)
	defer recorder.Close()

	client := &http.Client{Transport: recorder}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// TestWithProxyTLSConfig_NilConfig verifies that passing nil is a no-op.
func TestWithProxyTLSConfig_NilConfig(t *testing.T) {
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()

	// Should not panic or change transport.
	proxy, err := NewProxy(l1, l2, WithProxyTLSConfig(nil))
	if err != nil {
		t.Fatal(err)
	}
	if proxy.transport != http.DefaultTransport {
		t.Fatal("expected default transport when TLS config is nil")
	}
}

// TestWithRecorderTLSConfig_NilConfig verifies that passing nil is a no-op.
func TestWithRecorderTLSConfig_NilConfig(t *testing.T) {
	store := NewMemoryStore()
	recorder := NewRecorder(store, WithRecorderTLSConfig(nil))
	defer recorder.Close()

	if recorder.transport != http.DefaultTransport {
		t.Fatal("expected default transport when TLS config is nil")
	}
}

// TestWithProxyTLSConfig_CustomTransport verifies that TLS config is applied
// to an existing *http.Transport.
func TestWithProxyTLSConfig_CustomTransport(t *testing.T) {
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	existing := &http.Transport{MaxIdleConns: 42}

	l1 := NewMemoryStore()
	l2 := NewMemoryStore()
	proxy, err := NewProxy(l1, l2,
		WithProxyTransport(existing),
		WithProxyTLSConfig(tlsCfg),
	)
	if err != nil {
		t.Fatal(err)
	}

	transport, ok := proxy.transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}
	if transport.TLSClientConfig != tlsCfg {
		t.Fatal("expected TLS config to be set on existing transport")
	}
	if transport.MaxIdleConns != 42 {
		t.Fatal("expected existing transport settings to be preserved")
	}
}

// TestWithProxyTLSConfig_DoesNotMutateDefaultTransport verifies that using
// WithProxyTLSConfig without a custom transport does not mutate
// http.DefaultTransport.
func TestWithProxyTLSConfig_DoesNotMutateDefaultTransport(t *testing.T) {
	defaultTransport := http.DefaultTransport.(*http.Transport)
	originalTLS := defaultTransport.TLSClientConfig

	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	l1 := NewMemoryStore()
	l2 := NewMemoryStore()
	proxy, err := NewProxy(l1, l2, WithProxyTLSConfig(tlsCfg))
	if err != nil {
		t.Fatal(err)
	}

	// The proxy must have a new transport, not the default one.
	if proxy.transport == http.DefaultTransport {
		t.Fatal("expected proxy transport to differ from http.DefaultTransport")
	}

	// http.DefaultTransport must not have been mutated.
	if defaultTransport.TLSClientConfig != originalTLS {
		t.Fatal("http.DefaultTransport.TLSClientConfig was mutated by WithProxyTLSConfig")
	}
}

// TestWithRecorderTLSConfig_DoesNotMutateDefaultTransport verifies that using
// WithRecorderTLSConfig without a custom transport does not mutate
// http.DefaultTransport.
func TestWithRecorderTLSConfig_DoesNotMutateDefaultTransport(t *testing.T) {
	defaultTransport := http.DefaultTransport.(*http.Transport)
	originalTLS := defaultTransport.TLSClientConfig

	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	store := NewMemoryStore()
	recorder := NewRecorder(store,
		WithAsync(false),
		WithRecorderTLSConfig(tlsCfg),
	)
	defer recorder.Close()

	// The recorder must have a new transport, not the default one.
	if recorder.transport == http.DefaultTransport {
		t.Fatal("expected recorder transport to differ from http.DefaultTransport")
	}

	// http.DefaultTransport must not have been mutated.
	if defaultTransport.TLSClientConfig != originalTLS {
		t.Fatal("http.DefaultTransport.TLSClientConfig was mutated by WithRecorderTLSConfig")
	}
}

// TestWithRecorderTLSConfig_CustomTransport verifies that TLS config is
// applied to an existing *http.Transport.
func TestWithRecorderTLSConfig_CustomTransport(t *testing.T) {
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	existing := &http.Transport{MaxIdleConns: 42}

	store := NewMemoryStore()
	recorder := NewRecorder(store,
		WithTransport(existing),
		WithRecorderTLSConfig(tlsCfg),
	)
	defer recorder.Close()

	transport, ok := recorder.transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}
	if transport.TLSClientConfig != tlsCfg {
		t.Fatal("expected TLS config to be set on existing transport")
	}
	if transport.MaxIdleConns != 42 {
		t.Fatal("expected existing transport settings to be preserved")
	}
}

// --- GenerateSelfSignedCert tests ---

func TestGenerateSelfSignedCert_DefaultSANs(t *testing.T) {
	sc, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() error: %v", err)
	}

	parsed, err := x509.ParseCertificate(sc.TLSCertificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	if len(parsed.DNSNames) != 1 || parsed.DNSNames[0] != "localhost" {
		t.Errorf("DNSNames = %v, want [localhost]", parsed.DNSNames)
	}

	wantIPs := []string{"127.0.0.1", "::1"}
	if len(parsed.IPAddresses) != len(wantIPs) {
		t.Fatalf("IPAddresses count = %d, want %d", len(parsed.IPAddresses), len(wantIPs))
	}
	for i, wantIP := range wantIPs {
		if !parsed.IPAddresses[i].Equal(net.ParseIP(wantIP)) {
			t.Errorf("IPAddresses[%d] = %v, want %s", i, parsed.IPAddresses[i], wantIP)
		}
	}
}

func TestGenerateSelfSignedCert_CustomHosts(t *testing.T) {
	sc, err := GenerateSelfSignedCert("myhost.local", "10.0.0.1", "fe80::1")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() error: %v", err)
	}

	parsed, err := x509.ParseCertificate(sc.TLSCertificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	if len(parsed.DNSNames) != 1 || parsed.DNSNames[0] != "myhost.local" {
		t.Errorf("DNSNames = %v, want [myhost.local]", parsed.DNSNames)
	}

	if len(parsed.IPAddresses) != 2 {
		t.Fatalf("IPAddresses count = %d, want 2", len(parsed.IPAddresses))
	}
	if !parsed.IPAddresses[0].Equal(net.ParseIP("10.0.0.1")) {
		t.Errorf("IPAddresses[0] = %v, want 10.0.0.1", parsed.IPAddresses[0])
	}
	if !parsed.IPAddresses[1].Equal(net.ParseIP("fe80::1")) {
		t.Errorf("IPAddresses[1] = %v, want fe80::1", parsed.IPAddresses[1])
	}
}

func TestGenerateSelfSignedCert_CertificateValidity(t *testing.T) {
	sc, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() error: %v", err)
	}

	parsed, err := x509.ParseCertificate(sc.TLSCertificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	now := time.Now()

	// NotBefore should be approximately 1 hour in the past.
	expectedNotBefore := now.Add(-1 * time.Hour)
	if parsed.NotBefore.After(expectedNotBefore.Add(5 * time.Second)) {
		t.Errorf("NotBefore = %v, want approximately %v", parsed.NotBefore, expectedNotBefore)
	}
	if parsed.NotBefore.Before(expectedNotBefore.Add(-5 * time.Second)) {
		t.Errorf("NotBefore = %v, want approximately %v", parsed.NotBefore, expectedNotBefore)
	}

	// NotAfter should be approximately 24 hours from now.
	expectedNotAfter := now.Add(24 * time.Hour)
	if parsed.NotAfter.After(expectedNotAfter.Add(5 * time.Second)) {
		t.Errorf("NotAfter = %v, want approximately %v", parsed.NotAfter, expectedNotAfter)
	}
	if parsed.NotAfter.Before(expectedNotAfter.Add(-5 * time.Second)) {
		t.Errorf("NotAfter = %v, want approximately %v", parsed.NotAfter, expectedNotAfter)
	}
}

func TestGenerateSelfSignedCert_ECDSAKeyType(t *testing.T) {
	sc, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() error: %v", err)
	}

	parsed, err := x509.ParseCertificate(sc.TLSCertificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	ecKey, ok := parsed.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("public key type = %T, want *ecdsa.PublicKey", parsed.PublicKey)
	}
	if ecKey.Curve != elliptic.P256() {
		t.Errorf("curve = %v, want P-256", ecKey.Curve.Params().Name)
	}
}

func TestGenerateSelfSignedCert_FingerprintFormat(t *testing.T) {
	sc, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() error: %v", err)
	}

	parts := strings.Split(sc.Fingerprint, ":")
	if len(parts) != 32 {
		t.Fatalf("fingerprint has %d parts, want 32 (SHA-256)", len(parts))
	}
	for i, p := range parts {
		if len(p) != 2 {
			t.Errorf("fingerprint part %d = %q, want 2 hex chars", i, p)
		}
		if p != strings.ToUpper(p) {
			t.Errorf("fingerprint part %d = %q, want uppercase", i, p)
		}
	}
}

func TestGenerateSelfSignedCert_CertPEMIsValid(t *testing.T) {
	sc, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() error: %v", err)
	}

	block, rest := pem.Decode(sc.CertPEM)
	if block == nil {
		t.Fatal("CertPEM did not decode as PEM")
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("PEM type = %q, want CERTIFICATE", block.Type)
	}
	if len(rest) != 0 {
		t.Error("unexpected trailing data after PEM block")
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(sc.CertPEM) {
		t.Fatal("CertPEM could not be added to x509.CertPool")
	}
}

func TestGenerateSelfSignedCert_TLSHandshakeSucceeds(t *testing.T) {
	sc, err := GenerateSelfSignedCert("localhost", "127.0.0.1")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() error: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	tlsLn := tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{sc.TLSCertificate},
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "pong")
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(tlsLn)
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(sc.CertPEM)
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}

	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/ping", ln.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != "pong" {
		t.Errorf("body = %q, want %q", string(body), "pong")
	}
}

func TestGenerateSelfSignedCert_UntrustedClientRejects(t *testing.T) {
	sc, err := GenerateSelfSignedCert("localhost", "127.0.0.1")
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() error: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	tlsLn := tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{sc.TLSCertificate},
	})

	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	go srv.Serve(tlsLn)
	defer srv.Close()

	// Client without custom RootCAs should reject the self-signed cert.
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{},
		},
	}

	_, err = client.Get(fmt.Sprintf("https://127.0.0.1:%d/ping", ln.Addr().(*net.TCPAddr).Port))
	if err == nil {
		t.Fatal("expected TLS handshake error when client does not trust self-signed cert")
	}
}

func TestGenerateSelfSignedCert_ServerAuthExtKeyUsage(t *testing.T) {
	sc, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() error: %v", err)
	}

	parsed, err := x509.ParseCertificate(sc.TLSCertificate.Certificate[0])
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	found := false
	for _, usage := range parsed.ExtKeyUsage {
		if usage == x509.ExtKeyUsageServerAuth {
			found = true
			break
		}
	}
	if !found {
		t.Error("certificate missing ExtKeyUsageServerAuth")
	}
}

func TestGenerateSelfSignedCert_UniqueSerialNumbers(t *testing.T) {
	sc1, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() #1 error: %v", err)
	}
	sc2, err := GenerateSelfSignedCert()
	if err != nil {
		t.Fatalf("GenerateSelfSignedCert() #2 error: %v", err)
	}

	p1, _ := x509.ParseCertificate(sc1.TLSCertificate.Certificate[0])
	p2, _ := x509.ParseCertificate(sc2.TLSCertificate.Certificate[0])

	if p1.SerialNumber.Cmp(p2.SerialNumber) == 0 {
		t.Error("two generated certificates have the same serial number")
	}
}
