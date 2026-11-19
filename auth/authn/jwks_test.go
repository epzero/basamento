package authn

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func jwkBase64(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func rsaJWK(kid string, pub *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": jwkBase64(pub.N.Bytes()),
		"e": jwkBase64(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func ecJWK(t *testing.T, kid string, pub *ecdsa.PublicKey) map[string]any {
	t.Helper()
	point, err := pub.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	size := (len(point) - 1) / 2
	return map[string]any{
		"kty": "EC", "kid": kid, "use": "sig", "alg": "ES256", "crv": "P-256",
		"x": jwkBase64(point[1 : 1+size]),
		"y": jwkBase64(point[1+size:]),
	}
}

func edJWK(kid string, pub ed25519.PublicKey) map[string]any {
	return map[string]any{
		"kty": "OKP", "kid": kid, "use": "sig", "alg": "EdDSA", "crv": "Ed25519",
		"x": jwkBase64(pub),
	}
}

func jwksServer(t *testing.T, keys func() []map[string]any) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		current := keys()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"keys": current}); err != nil {
			t.Errorf("encode JWKS: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestJWKSResolvesRSAKey(t *testing.T) {
	key := rsaPrivateKey(t)
	server, _ := jwksServer(t, func() []map[string]any {
		return []map[string]any{rsaJWK("k1", &key.PublicKey)}
	})
	set, err := NewJWKS(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, err := set.VerificationKey(context.Background(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := got.(*rsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		t.Fatalf("key = %T, want the configured RSA key", got)
	}
}

func TestJWKSResolvesECAndEd25519Keys(t *testing.T) {
	ecData, err := os.ReadFile("testdata/ec.pem")
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := jwt.ParseECPrivateKeyFromPEM(ecData)
	if err != nil {
		t.Fatal(err)
	}
	edData, err := os.ReadFile("testdata/ed25519.pem")
	if err != nil {
		t.Fatal(err)
	}
	parsedEdKey, err := jwt.ParseEdPrivateKeyFromPEM(edData)
	if err != nil {
		t.Fatal(err)
	}
	edKey, ok := parsedEdKey.(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("Ed25519 key is %T", parsedEdKey)
	}
	edPub, ok := edKey.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("Ed25519 public key is %T", edKey.Public())
	}

	server, _ := jwksServer(t, func() []map[string]any {
		return []map[string]any{
			ecJWK(t, "ec", &ecKey.PublicKey),
			edJWK("ed", edPub),
		}
	})
	set, err := NewJWKS(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	gotEC, err := set.VerificationKey(context.Background(), "ec")
	if err != nil {
		t.Fatal(err)
	}
	if pub, ok := gotEC.(*ecdsa.PublicKey); !ok || !pub.Equal(&ecKey.PublicKey) {
		t.Fatalf("EC key = %T", gotEC)
	}
	gotED, err := set.VerificationKey(context.Background(), "ed")
	if err != nil {
		t.Fatal(err)
	}
	if pub, ok := gotED.(ed25519.PublicKey); !ok || !pub.Equal(edPub) {
		t.Fatalf("Ed25519 key = %T", gotED)
	}
}

func TestJWKSRefreshesOnUnknownKey(t *testing.T) {
	key := rsaPrivateKey(t)
	var mu sync.Mutex
	kid := "k1"
	server, requests := jwksServer(t, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return []map[string]any{rsaJWK(kid, &key.PublicKey)}
	})
	set, err := NewJWKS(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	if *requests != 1 {
		t.Fatalf("requests = %d, want 1", *requests)
	}

	mu.Lock()
	kid = "k2"
	mu.Unlock()
	if _, err := set.VerificationKey(context.Background(), "k2"); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if *requests != 2 {
		t.Fatalf("requests = %d, want 2", *requests)
	}

	if _, err := set.VerificationKey(context.Background(), "missing"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}

func TestJWKSRotationWithOIDC(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	var mu sync.Mutex
	kid := "k1"
	server, _ := jwksServer(t, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return []map[string]any{rsaJWK(kid, &key.PublicKey)}
	})
	set, err := NewJWKS(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewOIDC(OIDCOptions{
		Issuer: testIssuer, Audience: testAudience, Keys: set, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := signToken(t, jwt.SigningMethodRS256, key, "k1", testClaims(clock.Now()))
	if _, err := verifier.Authenticate(context.Background(), first); err != nil {
		t.Fatalf("first key: %v", err)
	}

	mu.Lock()
	kid = "k2"
	mu.Unlock()
	second := signToken(t, jwt.SigningMethodRS256, key, "k2", testClaims(clock.Now()))
	if _, err := verifier.Authenticate(context.Background(), second); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
}

func TestJWKSFailsClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	set, err := NewJWKS(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.VerificationKey(context.Background(), "k1"); err == nil {
		t.Fatal("VerificationKey succeeded against a failing server")
	}
}

func TestNewJWKSRejectsBadURL(t *testing.T) {
	if _, err := NewJWKS("not a url"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code = %s, want invalid_argument", core.CodeOf(err))
	}
}

func TestJWKSSkipsEncryptionKeys(t *testing.T) {
	key := rsaPrivateKey(t)
	server, _ := jwksServer(t, func() []map[string]any {
		enc := rsaJWK("enc", &key.PublicKey)
		enc["use"] = "enc"
		return []map[string]any{enc}
	})
	set, err := NewJWKS(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := set.VerificationKey(context.Background(), "enc"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}
