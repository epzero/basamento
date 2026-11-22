package authn

import (
	"context"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

const (
	testIssuer   = "https://id.example.test"
	testAudience = "kit"
	testKeyID    = "rsa-1"
)

func testNow() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func rsaPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	data, err := os.ReadFile("testdata/rsa.pem")
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(data)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func otherRSAPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	data, err := os.ReadFile("testdata/unknown-rsa.pem")
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM(data)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func testOIDC(t *testing.T, clock core.Clock) *OIDC {
	t.Helper()
	key := rsaPrivateKey(t)
	v, err := NewOIDC(OIDCOptions{
		Issuer:   testIssuer,
		Audience: testAudience,
		Keys:     StaticKeys{testKeyID: &key.PublicKey},
		Clock:    clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func testClaims(now time.Time) jwt.MapClaims {
	return jwt.MapClaims{
		"iss":    testIssuer,
		"aud":    testAudience,
		"sub":    "user-1",
		"exp":    jwt.NewNumericDate(now.Add(time.Hour)),
		"iat":    jwt.NewNumericDate(now.Add(-time.Minute)),
		"tenant": "acme",
		"scope":  "runs:read runs:write",
	}
}

func signToken(t *testing.T, method jwt.SigningMethod, key any, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	if kid != "" {
		token.Header["kid"] = kid
	}
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func tamperSignature(t *testing.T, raw string) string {
	t.Helper()
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	signature[0] ^= 0xff
	parts[2] = base64.RawURLEncoding.EncodeToString(signature)
	return strings.Join(parts, ".")
}

func unsignedToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func TestOIDCAuthenticateAcceptsValidToken(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	verifier := testOIDC(t, clock)
	key := rsaPrivateKey(t)
	now := clock.Now()

	cases := map[string]func(jwt.MapClaims){
		"provider scope claim": func(claims jwt.MapClaims) {
			claims["scope"] = "openid profile email"
		},
		"no scope claim": func(claims jwt.MapClaims) {
			delete(claims, "scope")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			claims := testClaims(now)
			mutate(claims)
			raw := signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
			p, err := verifier.Authenticate(context.Background(), raw)
			if err != nil {
				t.Fatalf("Authenticate: %v", err)
			}
			if p.ID != "user-1" || p.Tenant != "acme" {
				t.Fatalf("principal = %+v", p)
			}
			if len(p.Scopes) != 0 {
				t.Fatalf("scopes = %v, want none", p.Scopes)
			}
		})
	}
}

func TestOIDCAuthenticateRejectsBadTokens(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	verifier := testOIDC(t, clock)
	key := rsaPrivateKey(t)
	now := clock.Now()

	valid := func() jwt.MapClaims { return testClaims(now) }
	cases := map[string]func() string{
		"corrupted signature": func() string {
			return tamperSignature(t, signToken(t, jwt.SigningMethodRS256, key, testKeyID, valid()))
		},
		"signed by another key": func() string {
			return signToken(t, jwt.SigningMethodRS256, otherRSAPrivateKey(t), testKeyID, valid())
		},
		"unknown key id": func() string {
			return signToken(t, jwt.SigningMethodRS256, key, "other", valid())
		},
		"wrong audience": func() string {
			claims := valid()
			claims["aud"] = "other"
			return signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
		},
		"wrong issuer": func() string {
			claims := valid()
			claims["iss"] = "https://other.example.test"
			return signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
		},
		"expired": func() string {
			claims := valid()
			claims["exp"] = jwt.NewNumericDate(now.Add(-time.Minute))
			return signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
		},
		"no expiration": func() string {
			claims := valid()
			delete(claims, "exp")
			return signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
		},
		"no subject": func() string {
			claims := valid()
			delete(claims, "sub")
			return signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
		},
		"invalid tenant": func() string {
			claims := valid()
			claims["tenant"] = "acme/other"
			return signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
		},
		"tenant is not a string": func() string {
			claims := valid()
			claims["tenant"] = 7
			return signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
		},
		"alg none": func() string { return unsignedToken(t, valid()) },
		"empty":    func() string { return "" },
		"garbage":  func() string { return "not.a.token" },
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			raw := token()
			_, err := verifier.Authenticate(context.Background(), raw)
			if core.CodeOf(err) != core.CodeUnauthenticated {
				t.Fatalf("code = %s, want unauthenticated", core.CodeOf(err))
			}
			if raw != "" && strings.Contains(err.Error(), raw) {
				t.Fatal("error contains the token")
			}
		})
	}
}

func TestOIDCAuthenticateAcceptsOtherAlgorithms(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())

	ecData, err := os.ReadFile("testdata/ec.pem")
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := jwt.ParseECPrivateKeyFromPEM(ecData)
	if err != nil {
		t.Fatal(err)
	}
	ecVerifier, err := NewOIDC(OIDCOptions{
		Issuer: testIssuer, Audience: testAudience,
		Keys: StaticKeys{"ec-1": &ecKey.PublicKey}, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	ecToken := signToken(t, jwt.SigningMethodES256, ecKey, "ec-1", testClaims(clock.Now()))
	if _, err := ecVerifier.Authenticate(context.Background(), ecToken); err != nil {
		t.Fatalf("ES256 token: %v", err)
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
	edVerifier, err := NewOIDC(OIDCOptions{
		Issuer: testIssuer, Audience: testAudience,
		Keys: StaticKeys{"ed-1": edKey.Public()}, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	edToken := signToken(t, jwt.SigningMethodEdDSA, edKey, "ed-1", testClaims(clock.Now()))
	if _, err := edVerifier.Authenticate(context.Background(), edToken); err != nil {
		t.Fatalf("EdDSA token: %v", err)
	}
}

func TestOIDCLeewayIsExplicit(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	claims := testClaims(clock.Now())
	claims["exp"] = jwt.NewNumericDate(clock.Now().Add(-10 * time.Second))
	raw := signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)

	if _, err := testOIDC(t, clock).Authenticate(context.Background(), raw); core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("without leeway: code = %s, want unauthenticated", core.CodeOf(err))
	}

	verifier, err := NewOIDC(OIDCOptions{
		Issuer: testIssuer, Audience: testAudience,
		Keys: StaticKeys{testKeyID: &key.PublicKey}, Clock: clock, Leeway: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Authenticate(context.Background(), raw); err != nil {
		t.Fatalf("with leeway: %v", err)
	}
}

func TestOIDCTenantClaimIsConfigurable(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	verifier, err := NewOIDC(OIDCOptions{
		Issuer: testIssuer, Audience: testAudience,
		Keys: StaticKeys{testKeyID: &key.PublicKey}, Clock: clock, TenantClaim: "org",
	})
	if err != nil {
		t.Fatal(err)
	}
	claims := testClaims(clock.Now())
	delete(claims, "tenant")
	claims["org"] = "globex"
	raw := signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
	p, err := verifier.Authenticate(context.Background(), raw)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if p.Tenant != "globex" {
		t.Fatalf("tenant = %q, want globex", p.Tenant)
	}
}

func TestNewOIDCAcceptsMaxLeeway(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	if _, err := NewOIDC(OIDCOptions{
		Issuer: testIssuer, Audience: testAudience,
		Keys: StaticKeys{testKeyID: &key.PublicKey}, Clock: clock, Leeway: MaxLeeway,
	}); err != nil {
		t.Fatal(err)
	}
}

type errKeySet struct{ err error }

func (e errKeySet) VerificationKey(context.Context, string) (any, error) {
	return nil, e.err
}

type ctxHonoringKeySet struct {
	key any
}

func (c ctxHonoringKeySet) VerificationKey(ctx context.Context, _ string) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.key, nil
}

func TestOIDCAuthenticateMapsContextErrors(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	raw := signToken(t, jwt.SigningMethodRS256, key, testKeyID, testClaims(clock.Now()))

	cases := map[string]struct {
		keys     KeySet
		ctx      context.Context
		token    string
		wantCode core.Code
	}{
		"key set cancelled": {
			keys:     errKeySet{err: context.Canceled},
			ctx:      context.Background(),
			wantCode: core.CodeCancelled,
		},
		"key set deadline": {
			keys:     errKeySet{err: context.DeadlineExceeded},
			ctx:      context.Background(),
			wantCode: core.CodeTimeout,
		},
		"caller cancelled": {
			keys:     ctxHonoringKeySet{key: &key.PublicKey},
			ctx:      func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }(),
			wantCode: core.CodeCancelled,
		},
		"parse fails before key function on cancelled ctx": {
			keys:     ctxHonoringKeySet{key: &key.PublicKey},
			ctx:      func() context.Context { c, cancel := context.WithCancel(context.Background()); cancel(); return c }(),
			token:    "not.a.token",
			wantCode: core.CodeUnauthenticated,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			verifier, err := NewOIDC(OIDCOptions{
				Issuer: testIssuer, Audience: testAudience, Keys: tc.keys, Clock: clock,
			})
			if err != nil {
				t.Fatal(err)
			}
			token := raw
			if tc.token != "" {
				token = tc.token
			}
			_, err = verifier.Authenticate(tc.ctx, token)
			if core.CodeOf(err) != tc.wantCode {
				t.Fatalf("code = %s, want %s", core.CodeOf(err), tc.wantCode)
			}
		})
	}

	verifier := testOIDC(t, clock)
	claims := testClaims(clock.Now())
	claims["exp"] = jwt.NewNumericDate(clock.Now().Add(-time.Hour))
	expired := signToken(t, jwt.SigningMethodRS256, key, testKeyID, claims)
	if _, err := verifier.Authenticate(context.Background(), expired); core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("expired token: code = %s, want unauthenticated", core.CodeOf(err))
	}
}

func TestNewOIDCRejectsIncompleteOptions(t *testing.T) {
	clock := coretest.NewFakeClock(testNow())
	key := rsaPrivateKey(t)
	keys := StaticKeys{testKeyID: &key.PublicKey}
	cases := map[string]OIDCOptions{
		"no issuer":        {Audience: testAudience, Keys: keys, Clock: clock},
		"bad issuer":       {Issuer: "not a url", Audience: testAudience, Keys: keys, Clock: clock},
		"no audience":      {Issuer: testIssuer, Keys: keys, Clock: clock},
		"no keys":          {Issuer: testIssuer, Audience: testAudience, Clock: clock},
		"no clock":         {Issuer: testIssuer, Audience: testAudience, Keys: keys},
		"bad leeway":       {Issuer: testIssuer, Audience: testAudience, Keys: keys, Clock: clock, Leeway: -time.Second},
		"leeway too large": {Issuer: testIssuer, Audience: testAudience, Keys: keys, Clock: clock, Leeway: MaxLeeway + time.Nanosecond},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewOIDC(opts); core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid_argument", core.CodeOf(err))
			}
		})
	}
}
