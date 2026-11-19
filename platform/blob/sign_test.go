package blob

import (
	"testing"
	"time"
)

func TestSignReadRoundTrip(t *testing.T) {
	t.Parallel()
	key := []byte("signing-key-for-tests-only")
	tenant := "acme"
	hex := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	exp := time.Date(2026, 11, 16, 9, 15, 0, 0, time.UTC).Unix()
	sig := signRead(key, tenant, hex, exp)
	if !verifyRead(key, tenant, hex, sig, exp, time.Unix(exp-1, 0)) {
		t.Fatal("signature should verify before expiry")
	}
	if verifyRead(key, tenant, hex, sig, exp, time.Unix(exp, 0)) {
		t.Fatal("signature must not verify at expiry")
	}
	if verifyRead([]byte("other"), tenant, hex, sig, exp, time.Unix(exp-1, 0)) {
		t.Fatal("wrong key must not verify")
	}
}
