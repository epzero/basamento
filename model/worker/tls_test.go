package worker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

// TestWorkerServiceTLSPair proves the platform and a worker can talk over the
// generated contract with mutual TLS and that the same listener refuses to
// serve plaintext off loopback.
func TestWorkerServiceTLSPair(t *testing.T) {
	dir := t.TempDir()
	caFile, serverCert, serverKey, clientCert, clientKey := writeTestTLS(t, dir)

	store, _ := testStore(t, time.Unix(1_700_000_000, 0))
	pool, _, err := store.EnsurePoolWithToken(context.Background(),
		PoolSpec{ID: "acme-gpu", Lease: 5 * time.Minute, Grace: time.Minute},
		"0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}

	serverCreds, err := (TLSConfig{CertFile: serverCert, KeyFile: serverKey, CAFile: caFile}).ServerCredentials()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewService(store).GRPCServer(serverCreds)
	go func() { _ = ServeWorkerService(ctx, ln, srv, true) }()

	clientCreds, err := ClientCredentials(clientCert, clientKey, caFile)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := DialWorkerService(ln.Addr().String(), clientCreds)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tr.Close() }()
	w, err := tr.Join(context.Background(), JoinRequest{
		PoolToken: pool.Token, ProtocolVersion: ProtocolVersion,
		Descriptors: []Descriptor{descriptor("sdxl", 1, false)}, Capacity: 4,
	})
	if err != nil {
		t.Fatalf("join over TLS: %v", err)
	}
	if w.ID == "" || w.Credential == "" {
		t.Fatalf("join over TLS returned %+v, want a worker identity", w)
	}

	// The peer verifies the client certificate: a client without one is
	// refused even though the server certificate is trusted.
	noCert, err := ClientCredentials("", "", caFile)
	if err != nil {
		t.Fatal(err)
	}
	bad, err := DialWorkerService(ln.Addr().String(), noCert)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bad.Close() }()
	if _, err := bad.Join(context.Background(), JoinRequest{
		PoolToken: pool.Token, ProtocolVersion: ProtocolVersion,
		Descriptors: []Descriptor{descriptor("sdxl", 1, false)}, Capacity: 4,
	}); err == nil {
		t.Fatal("join without a client certificate succeeded, want a TLS refusal")
	}
}

func TestPlaintextWorkerListenerRefusesOffLoopback(t *testing.T) {
	ln := &addrListener{Listener: mustListen(t), addr: &net.TCPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 8443}}
	err := ServeWorkerService(context.Background(), ln, NewService(nil).GRPCServer(nil), false)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("ServeWorkerService off loopback = %v, want invalid_argument", err)
	}
}

func TestWeakPoolTokenRefused(t *testing.T) {
	store, _ := testStore(t, time.Unix(1_700_000_000, 0))
	if _, _, err := store.EnsurePoolWithToken(context.Background(),
		PoolSpec{ID: "acme-gpu", Lease: time.Minute, Grace: time.Minute}, "short"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("EnsurePoolWithToken(short) = %v, want invalid_argument", err)
	}
	if _, err := store.Join(context.Background(), JoinRequest{
		PoolToken: "short", ProtocolVersion: ProtocolVersion,
		Descriptors: []Descriptor{descriptor("sdxl", 1, false)}, Capacity: 1,
	}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("Join(short) = %v, want invalid_argument", err)
	}
}

func mustListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// addrListener overrides the address a listener reports so a loopback socket
// can stand in for an off-host bind without opening one.
type addrListener struct {
	net.Listener
	addr net.Addr
}

func (l *addrListener) Addr() net.Addr { return l.addr }

func writeTestTLS(t *testing.T, dir string) (caFile, serverCert, serverKey, clientCert, clientKey string) {
	t.Helper()
	ca, caKey := newCA(t)
	serverCert, serverKey = newLeaf(t, dir, "server", ca, caKey, x509.ExtKeyUsageServerAuth, true)
	clientCert, clientKey = newLeaf(t, dir, "client", ca, caKey, x509.ExtKeyUsageClientAuth, false)
	caFile = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return caFile, serverCert, serverKey, clientCert, clientKey
}

func newCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "worker test CA"},
		NotBefore:             time.Unix(1_600_000_000, 0),
		NotAfter:              time.Unix(1_900_000_000, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func newLeaf(t *testing.T, dir, name string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, usage x509.ExtKeyUsage, server bool) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(int64(len(name))),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Unix(1_600_000_000, 0),
		NotAfter:     time.Unix(1_900_000_000, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	if server {
		tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1)}
		tmpl.DNSNames = []string{"localhost"}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(dir, name+".pem")
	keyFile = filepath.Join(dir, name+".key")
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
