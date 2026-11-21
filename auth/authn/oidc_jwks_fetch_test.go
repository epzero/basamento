package authn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func assertAuthenticateErrOmitsSecret(t *testing.T, err error, raw, jwksURL string) {
	t.Helper()
	if err == nil {
		t.Fatal("nil error")
	}
	msg := err.Error()
	if strings.Contains(msg, raw) {
		t.Fatal("error contains the token")
	}
	if strings.Contains(msg, jwksURL) {
		t.Fatal("error contains the JWKS URL")
	}
}

func TestOIDCAuthenticateJWKSFetchDeadline(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{rsaJWK("k1", &key.PublicKey)}})
	}))
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		closeRelease()
		server.Close()
	})

	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	set.client = &http.Client{}
	set.fetchTimeout = time.Nanosecond

	verifier, err := NewOIDC(OIDCOptions{
		Issuer: testIssuer, Audience: testAudience, Keys: set, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := signToken(t, jwt.SigningMethodRS256, key, "k1", testClaims(clock.Now()))

	for attempt := 1; attempt <= 2; attempt++ {
		_, err := verifier.Authenticate(context.Background(), raw)
		if core.CodeOf(err) != core.CodeTimeout {
			t.Fatalf("attempt %d: code = %s, want timeout", attempt, core.CodeOf(err))
		}
		var pe *core.Error
		if !errors.As(err, &pe) || pe.Message != "timeout" {
			t.Fatalf("attempt %d: message = %q, want %q", attempt, err.Error(), "timeout")
		}
		assertAuthenticateErrOmitsSecret(t, err, raw, server.URL)
	}
}

func TestOIDCAuthenticateJWKSFetchHTTPFailure(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	set := testJWKS(t, server.URL, clock, defaultMinRefresh)
	verifier, err := NewOIDC(OIDCOptions{
		Issuer: testIssuer, Audience: testAudience, Keys: set, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := signToken(t, jwt.SigningMethodRS256, key, "k1", testClaims(clock.Now()))

	_, err = verifier.Authenticate(context.Background(), raw)
	if core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("code = %s, want unauthenticated", core.CodeOf(err))
	}
	assertAuthenticateErrOmitsSecret(t, err, raw, server.URL)
}
