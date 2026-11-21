package authn

import (
	"bytes"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func testToken(t *testing.T, expires time.Time) (string, Token) {
	t.Helper()
	entropy := bytes.NewReader(bytes.Repeat([]byte{0x42}, tokenBytes))
	secret, token, err := NewToken(entropy, "tok_1", "acme", []string{"runs:read", "runs:write"}, expires)
	if err != nil {
		t.Fatal(err)
	}
	return secret, token
}

func TestNewTokenHashesSecretAndKeepsScopes(t *testing.T) {
	expires := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	secret, token := testToken(t, expires)

	if got, want := secret, base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, tokenBytes)); got != want {
		t.Fatalf("secret = %q, want %q", got, want)
	}
	if token.ID != "tok_1" || token.Tenant != "acme" {
		t.Fatalf("token id/tenant = %q/%q", token.ID, token.Tenant)
	}
	if !token.Expires.Equal(expires) {
		t.Fatalf("expires = %v, want %v", token.Expires, expires)
	}
	if bytes.Contains([]byte(secret), token.Hash) {
		t.Fatal("stored record contains the secret")
	}
}

func TestTokenVerify(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	secret, token := testToken(t, issued.Add(time.Hour))

	p, err := token.Verify(secret, issued.Add(30*time.Minute))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if p.ID != "tok_1" || p.Tenant != "acme" {
		t.Fatalf("principal = %+v", p)
	}
	if len(p.Scopes) != 2 || p.Scopes[0] != "runs:read" {
		t.Fatalf("scopes = %v", p.Scopes)
	}

	cases := map[string]struct {
		secret string
		now    time.Time
	}{
		"wrong secret": {secret: secret + "x", now: issued},
		"empty secret": {secret: "", now: issued},
		"expired":      {secret: secret, now: issued.Add(time.Hour)},
		"after expiry": {secret: secret, now: issued.Add(2 * time.Hour)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := token.Verify(tc.secret, tc.now); core.CodeOf(err) != core.CodeUnauthenticated {
				t.Fatalf("code = %s, want unauthenticated", core.CodeOf(err))
			}
		})
	}
}

func TestTokenVerifyRejectsEmptyHash(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := (Token{ID: "x", Tenant: "acme"}).Verify("secret", now); core.CodeOf(err) != core.CodeUnauthenticated {
		t.Fatalf("code = %s, want unauthenticated", core.CodeOf(err))
	}
}

func TestNewTokenRejectsInvalidArguments(t *testing.T) {
	expires := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	entropy := bytes.NewReader(make([]byte, tokenBytes))
	cases := map[string]struct {
		entropy io.Reader
		id      string
		tenant  core.TenantID
		scopes  []string
		expires time.Time
	}{
		"no entropy":   {id: "tok_1", tenant: "acme", scopes: []string{"read"}, expires: expires},
		"no id":        {entropy: entropy, tenant: "acme", scopes: []string{"read"}, expires: expires},
		"no tenant":    {entropy: entropy, id: "tok_1", scopes: []string{"read"}, expires: expires},
		"bad tenant":   {entropy: entropy, id: "tok_1", tenant: "acme/../../etc", scopes: []string{"read"}, expires: expires},
		"no expiry":    {entropy: entropy, id: "tok_1", tenant: "acme", scopes: []string{"read"}},
		"nil scopes":   {entropy: entropy, id: "tok_1", tenant: "acme", scopes: nil, expires: expires},
		"empty scopes": {entropy: entropy, id: "tok_1", tenant: "acme", scopes: []string{}, expires: expires},
		"empty entry":  {entropy: entropy, id: "tok_1", tenant: "acme", scopes: []string{""}, expires: expires},
		"blank entry":  {entropy: entropy, id: "tok_1", tenant: "acme", scopes: []string{"read", "  "}, expires: expires},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := NewToken(tc.entropy, tc.id, tc.tenant, tc.scopes, tc.expires)
			if core.CodeOf(err) != core.CodeInvalidArgument {
				t.Fatalf("code = %s, want invalid_argument", core.CodeOf(err))
			}
		})
	}
}

func TestNewTokenScopesAreCloned(t *testing.T) {
	expires := time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)
	entropy := bytes.NewReader(make([]byte, tokenBytes))
	scopes := []string{"read"}
	_, token, err := NewToken(entropy, "tok_1", "acme", scopes, expires)
	if err != nil {
		t.Fatal(err)
	}
	scopes[0] = "mutated"
	if token.Scopes[0] != "read" {
		t.Fatalf("scopes = %v, want a clone", token.Scopes)
	}
	entropy = bytes.NewReader(make([]byte, tokenBytes))
	_, token, err = NewToken(entropy, "tok_2", "acme", []string{"read", "write"}, expires)
	if err != nil {
		t.Fatal(err)
	}
	if len(token.Scopes) != 2 || token.Scopes[0] != "read" || token.Scopes[1] != "write" {
		t.Fatalf("scopes = %v", token.Scopes)
	}
}

func TestNewTokenNeverEchoesTheSecret(t *testing.T) {
	secret, token := testToken(t, time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC))
	_, err := token.Verify("wrong", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("Verify accepted a wrong secret")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("error contains the secret")
	}
}
