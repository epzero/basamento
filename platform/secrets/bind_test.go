package secrets

import (
	"testing"

	"github.com/epzero/basamento/core"
)

func TestTenantCollisionRejected(t *testing.T) {
	t.Parallel()
	if err := ValidateTenant("acme"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTenant("acme\x00evil"); err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("nul tenant err=%v", err)
	}
}

func TestFrameBoundRoundTrip(t *testing.T) {
	t.Parallel()
	plain := []byte("keyset-bytes")
	fr, err := FrameBoundPlaintext("acme", plain)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ParseBoundPlaintext("acme", fr)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(plain) {
		t.Fatal("mismatch")
	}
}

func TestFrameBoundWrongTenantInternal(t *testing.T) {
	t.Parallel()
	fr, err := FrameBoundPlaintext("acme", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ParseBoundPlaintext("other", fr)
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("err=%v", err)
	}
}
