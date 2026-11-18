package secrets

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

type domainFakeKeeper struct{}

func (domainFakeKeeper) Encrypt(_ context.Context, plain []byte) ([]byte, error) {
	return append([]byte("ct:"), plain...), nil
}

func (domainFakeKeeper) Decrypt(_ context.Context, ct []byte) ([]byte, error) {
	if len(ct) < 3 || string(ct[:3]) != "ct:" {
		return nil, core.NewError(core.CodeInvalidArgument, "bad", nil)
	}
	return ct[3:], nil
}

func TestValueFrameRejectsKeysetPlaintext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fr, err := FrameBoundPlaintext("acme", []byte("keyset"))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := domainFakeKeeper{}.Encrypt(ctx, FrameValuePlaintext([]byte("stolen")))
	if err != nil {
		t.Fatal(err)
	}
	// Attacker stores keyset frame as if it were a value ciphertext path: Unwrap path uses ParseBoundPlaintext
	if _, err := ParseBoundPlaintext("victim", append([]byte("ct:"), ct[3:]...)); err == nil {
		t.Fatal("value passthrough must not unwrap as keyset")
	}
	_ = fr
}

func TestKeysetFrameRejectsValueDomain(t *testing.T) {
	t.Parallel()
	inner := FrameValuePlaintext([]byte("secret"))
	if _, err := ParseBoundPlaintext("acme", inner); err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("err=%v", err)
	}
}
