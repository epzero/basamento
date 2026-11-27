package worker

import (
	"context"
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

	serverTLS, err := (TLSConfig{CertFile: serverCert, KeyFile: serverKey, CAFile: caFile}).ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewService(store).Server(serverTLS)
	go func() { _ = srv.Serve(ctx, ln) }()

	clientTLS, err := ClientTLS(clientCert, clientKey, caFile)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := DialWorkerService(ln.Addr().String(), clientTLS)
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
	noCert, err := ClientTLS("", "", caFile)
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
	err := NewService(nil).Server(nil).Serve(context.Background(), ln)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("Serve off loopback = %v, want invalid_argument", err)
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

// The TLS fixtures are fixed PEM files, not generated at run time: tests use
// no global randomness, and the same pair exercises every round.
const (
	testCACert = `-----BEGIN CERTIFICATE-----
MIIBZDCCAQugAwIBAgIBATAKBggqhkjOPQQDAjAZMRcwFQYDVQQDEw53b3JrZXIg
dGVzdCBDQTAgFw0wMDAxMDEwMDAwMDBaGA8yMTAwMDEwMTAwMDAwMFowGTEXMBUG
A1UEAxMOd29ya2VyIHRlc3QgQ0EwWTATBgcqhkjOPQIBBggqhkjOPQMBBwNCAAQ5
HxuHhym3TrbD0VSiahM/PsWkybOX/yP5u0ZxQ1rDwtgpKcDSDkz1wKCkqofNZtLI
/bDyP7uw8LE1NRZ3Vi18o0IwQDAOBgNVHQ8BAf8EBAMCAoQwDwYDVR0TAQH/BAUw
AwEB/zAdBgNVHQ4EFgQUuQGkeSK6pF3vDN4MOXI2BtDty/kwCgYIKoZIzj0EAwID
RwAwRAIgHeOpUQpbiPtFXWPBLYrYQP7vwd1C930Ry27ontitXZMCIEOFRHEVs64G
SQYKUKvyNDqd8R+meeKQ3c4OfSJHdl3j
-----END CERTIFICATE-----
`
	testServerCert = `-----BEGIN CERTIFICATE-----
MIIBfzCCASWgAwIBAgIBBjAKBggqhkjOPQQDAjAZMRcwFQYDVQQDEw53b3JrZXIg
dGVzdCBDQTAgFw0wMDAxMDEwMDAwMDBaGA8yMTAwMDEwMTAwMDAwMFowETEPMA0G
A1UEAxMGc2VydmVyMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEmKGsoWAKUDec
pYRO+7dpqIkrrXCoeu5nQdqLYLy0kv2VXsNg1a2dP8TK/d/xlPSJTv0fZOHXcU9a
mZG8S5W3jaNkMGIwDgYDVR0PAQH/BAQDAgeAMBMGA1UdJQQMMAoGCCsGAQUFBwMB
MB8GA1UdIwQYMBaAFLkBpHkiuqRd7wzeDDlyNgbQ7cv5MBoGA1UdEQQTMBGCCWxv
Y2FsaG9zdIcEfwAAATAKBggqhkjOPQQDAgNIADBFAiBfnK4oT+wEseOmfjYBzJi+
BQcnGFJEbX6IX35FSPuKQQIhAOanHShGkEzwQJpq6dgGyJnmK9pasOoZnqWUQ92z
dElO
-----END CERTIFICATE-----
`
	testServerKey = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEILxHfUj893U+qJuE1Zyk+KO14xWN/K5KHH/i5vwWZvEAoAoGCCqGSM49
AwEHoUQDQgAEmKGsoWAKUDecpYRO+7dpqIkrrXCoeu5nQdqLYLy0kv2VXsNg1a2d
P8TK/d/xlPSJTv0fZOHXcU9amZG8S5W3jQ==
-----END EC PRIVATE KEY-----
`
	testClientCert = `-----BEGIN CERTIFICATE-----
MIIBYzCCAQmgAwIBAgIBBjAKBggqhkjOPQQDAjAZMRcwFQYDVQQDEw53b3JrZXIg
dGVzdCBDQTAgFw0wMDAxMDEwMDAwMDBaGA8yMTAwMDEwMTAwMDAwMFowETEPMA0G
A1UEAxMGY2xpZW50MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAExV8IaXoICd4Y
7bI++adZ3L5qTe94SNSFyLd8+Kl+u/HMc8tu1IRKWYzCKSh8AIFPSSVGBEXPVrL/
4Ht1AS3Uy6NIMEYwDgYDVR0PAQH/BAQDAgeAMBMGA1UdJQQMMAoGCCsGAQUFBwMC
MB8GA1UdIwQYMBaAFLkBpHkiuqRd7wzeDDlyNgbQ7cv5MAoGCCqGSM49BAMCA0gA
MEUCIEG/9Cz0qa2rypVBrqV76rd3ePRprtzyY9RBaX4PIpW6AiEAzZ8jXB5VmeQP
uRAVDG3nplebR6jbV7jYHl6ukDn5osE=
-----END CERTIFICATE-----
`
	testClientKey = `-----BEGIN EC PRIVATE KEY-----
MHcCAQEEINLF2C8yh+ReOuU2Bpf4ncpGDDpEHt5x1vvw75ffJYRboAoGCCqGSM49
AwEHoUQDQgAExV8IaXoICd4Y7bI++adZ3L5qTe94SNSFyLd8+Kl+u/HMc8tu1IRK
WYzCKSh8AIFPSSVGBEXPVrL/4Ht1AS3Uyw==
-----END EC PRIVATE KEY-----
`
)

func writeTestTLS(t *testing.T, dir string) (caFile, serverCert, serverKey, clientCert, clientKey string) {
	t.Helper()
	files := map[string]string{
		"ca.pem":     testCACert,
		"server.pem": testServerCert,
		"server.key": testServerKey,
		"client.pem": testClientCert,
		"client.key": testClientKey,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "ca.pem"), filepath.Join(dir, "server.pem"), filepath.Join(dir, "server.key"), filepath.Join(dir, "client.pem"), filepath.Join(dir, "client.key")
}

func TestDialWorkerServiceRefusesPlaintextOffLoopback(t *testing.T) {
	if _, err := DialWorkerService("10.0.0.1:8443"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("plaintext dial off loopback = %v, want invalid_argument", err)
	}
	tr, err := DialWorkerService("127.0.0.1:1")
	if err != nil {
		t.Fatalf("plaintext dial on loopback: %v", err)
	}
	_ = tr.Close()
}

func TestClampLeaseWait(t *testing.T) {
	for _, tc := range []struct{ in, want time.Duration }{
		{-time.Second, 0},
		{30 * time.Second, 30 * time.Second},
		{68 * 365 * 24 * time.Hour, maxLeaseWait},
	} {
		if got := clampLeaseWait(tc.in); got != tc.want {
			t.Fatalf("clampLeaseWait(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
