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
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func testJWKS(t *testing.T, rawURL string, clock core.Clock, minRefresh time.Duration) *JWKS {
	t.Helper()
	if clock == nil {
		clock = coretest.NewFakeClock(testNow())
	}
	set, err := NewJWKS(JWKSOptions{URL: rawURL, Clock: clock, MinRefresh: minRefresh})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

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
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	server, requests := jwksServer(t, func() []map[string]any {
		return []map[string]any{rsaJWK("k1", &key.PublicKey)}
	})
	set := testJWKS(t, server.URL, clock, 0)
	got, err := set.VerificationKey(context.Background(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := got.(*rsa.PublicKey)
	if !ok || !pub.Equal(&key.PublicKey) {
		t.Fatalf("key = %T, want the configured RSA key", got)
	}
	got2, err := set.VerificationKey(context.Background(), "k1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got2.(*rsa.PublicKey); !ok {
		t.Fatal("cached lookup failed")
	}
	if *requests != 1 {
		t.Fatalf("requests = %d, want 1", *requests)
	}
}

func TestJWKSResolvesECAndEd25519Keys(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
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
	set := testJWKS(t, server.URL, clock, 0)
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
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
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
	clock.Advance(defaultMinRefresh)
	second := signToken(t, jwt.SigningMethodRS256, key, "k2", testClaims(clock.Now()))
	if _, err := verifier.Authenticate(context.Background(), second); err != nil {
		t.Fatalf("rotated key: %v", err)
	}
	clock.Advance(defaultMinRefresh)
	if _, err := verifier.Authenticate(context.Background(), first); err == nil {
		t.Fatal("token signed with dropped key must fail")
	}
}

func TestJWKSFailsClosed(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	set := testJWKS(t, server.URL, clock, 0)
	if _, err := set.VerificationKey(context.Background(), "k1"); err == nil {
		t.Fatal("VerificationKey succeeded against a failing server")
	}
}

func jwksErrKey(err error) string {
	var pe *core.Error
	if errors.As(err, &pe) && pe.Details != nil {
		if k, ok := pe.Details["key"].(string); ok {
			return k
		}
	}
	return ""
}

func TestNewJWKSRejectsBadURL(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	base := JWKSOptions{URL: "https://issuer.test/jwks", Clock: clock}
	cases := []struct {
		name    string
		opts    JWKSOptions
		wantKey string
	}{
		{"malformed url", JWKSOptions{URL: "not a url", Clock: clock}, "auth.oidc.jwks"},
		{"http non-loopback", JWKSOptions{URL: "http://example.test/jwks", Clock: clock}, "auth.oidc.jwks"},
		{"empty url", JWKSOptions{URL: "", Clock: clock}, "auth.oidc.jwks"},
		{"negative min refresh", JWKSOptions{URL: base.URL, Clock: clock, MinRefresh: -time.Second}, "auth.oidc.jwks"},
		{"nil clock", JWKSOptions{URL: base.URL}, "auth.oidc.jwks"},
		{"url with user", JWKSOptions{URL: "https://user@issuer.test/jwks", Clock: clock}, "auth.oidc.jwks"},
		{"url with user and password", JWKSOptions{URL: "https://user:secret@issuer.test/jwks", Clock: clock}, "auth.oidc.jwks"},
		{"url with empty user", JWKSOptions{URL: "https://@issuer.test/jwks", Clock: clock}, "auth.oidc.jwks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewJWKS(tc.opts)
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid_argument", core.CodeOf(err))
			}
			if got := jwksErrKey(err); got != tc.wantKey {
				t.Fatalf("key = %q, want %q", got, tc.wantKey)
			}
		})
	}
}

func TestJWKSAcceptsHTTPLoopbackURL(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	for _, rawURL := range []string{
		"https://issuer.test/jwks?query=1",
		"http://127.0.0.1:9/jwks",
		"http://localhost:9/jwks",
		"http://[::1]:9/jwks",
	} {
		if _, err := NewJWKS(JWKSOptions{URL: rawURL, Clock: clock}); err != nil {
			t.Fatalf("url %q: %v", rawURL, err)
		}
	}
}

func TestJWKSSkipsEncryptionKeys(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	server, _ := jwksServer(t, func() []map[string]any {
		enc := rsaJWK("enc", &key.PublicKey)
		enc["use"] = "enc"
		return []map[string]any{enc}
	})
	set := testJWKS(t, server.URL, clock, 0)
	if _, err := set.VerificationKey(context.Background(), "enc"); !errors.Is(err, errNoUsableSigningKey) {
		t.Fatalf("err = %v, want errNoUsableSigningKey", err)
	}
}

func TestJWKSBadResponseKeepsOldKeys(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	var body []byte
	var status int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if body == nil && status == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{rsaJWK("k1", &key.PublicKey)}})
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	if _, err := set.VerificationKey(context.Background(), "k1"); err != nil {
		t.Fatal(err)
	}
	assertKey := func() {
		got, err := set.VerificationKey(context.Background(), "k1")
		if err != nil {
			t.Fatal(err)
		}
		if pub, ok := got.(*rsa.PublicKey); !ok || !pub.Equal(&key.PublicKey) {
			t.Fatal("old key missing")
		}
	}
	tests := []struct {
		name   string
		status int
		body   []byte
	}{
		{"empty keys", http.StatusOK, mustJSON(t, map[string]any{"keys": []any{}})},
		{"all enc", http.StatusOK, mustJSON(t, map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": "x", "use": "enc", "n": rsaJWK("x", &key.PublicKey)["n"], "e": rsaJWK("x", &key.PublicKey)["e"]}}})},
		{"unparsable", http.StatusOK, []byte(`{"keys":[{"kty":"RSA","kid":"bad"}]}`)},
		{"500", http.StatusInternalServerError, []byte("no")},
		{"not json", http.StatusOK, []byte("not-json")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body = tc.body
			status = tc.status
			clock.Advance(defaultMinRefresh)
			if _, err := set.VerificationKey(context.Background(), "new"); err == nil {
				t.Fatal("want error")
			}
			assertKey()
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestJWKSDefaultClientRejectsRedirects(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	var seen atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(source.Close)
	set := testJWKS(t, source.URL, clock, 0)
	if _, err := set.VerificationKey(context.Background(), "k1"); err == nil {
		t.Fatal("want redirect failure")
	}
	if seen.Load() {
		t.Fatal("redirect target was contacted")
	}
}
