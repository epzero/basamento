package secrets

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/core"
	"gocloud.dev/gcerrors"
	gcloudsecrets "gocloud.dev/secrets"
	"gocloud.dev/secrets/driver"
)

type codeErr struct {
	code gcerrors.ErrorCode
}

func (e *codeErr) Error() string { return "keeper failed" }

func (e *codeErr) Code() gcerrors.ErrorCode { return e.code }

type fakeDriverKeeper struct {
	enc, dec gcerrors.ErrorCode
}

func (f *fakeDriverKeeper) Encrypt(context.Context, []byte) ([]byte, error) {
	if f.enc != gcerrors.OK {
		return nil, &codeErr{code: f.enc}
	}
	return []byte("ct"), nil
}

func (f *fakeDriverKeeper) Decrypt(context.Context, []byte) ([]byte, error) {
	if f.dec != gcerrors.OK {
		return nil, &codeErr{code: f.dec}
	}
	return FrameValuePlaintext([]byte("plain")), nil
}

func (f *fakeDriverKeeper) Close() error                           { return nil }
func (f *fakeDriverKeeper) ErrorAs(error, any) bool                { return false }
func (f *fakeDriverKeeper) ErrorCode(err error) gcerrors.ErrorCode { return gcerrors.Code(err) }

var _ driver.Keeper = (*fakeDriverKeeper)(nil)

func keeperWithScheme(scheme string, enc, dec gcerrors.ErrorCode) *keeperKeyService {
	k := gcloudsecrets.NewKeeper(&fakeDriverKeeper{enc: enc, dec: dec})
	return &keeperKeyService{keeper: k, scheme: scheme, id: scheme + "://test"}
}

func TestClassifyKeeperErrTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		scheme    string
		enc, dec  gcerrors.ErrorCode
		op        string
		wantCode  core.Code
		wantMsg   string
		retryable bool
	}{
		{"localsecrets", gcerrors.OK, gcerrors.Unknown, "decrypt", core.CodeInvalidArgument, msgValueCannotDecrypt, false},
		{"awskms", gcerrors.OK, gcerrors.Unknown, "decrypt", core.CodeUnavailable, "awskms", true},
		{"localsecrets", gcerrors.OK, gcerrors.Unknown, "unwrap", core.CodeInternal, msgKeysetCannotUnwrap, false},
		{"awskms", gcerrors.OK, gcerrors.Unknown, "unwrap", core.CodeUnavailable, "awskms", true},
		{"awskms", gcerrors.ResourceExhausted, gcerrors.OK, "wrap", core.CodeUnavailable, "awskms", true},
		{"localsecrets", gcerrors.InvalidArgument, gcerrors.OK, "wrap", core.CodeInternal, msgKeyServiceRefused, false},
		{"localsecrets", gcerrors.OK, gcerrors.InvalidArgument, "decrypt", core.CodeInvalidArgument, msgValueCannotDecrypt, false},
		{"awskms", gcerrors.OK, gcerrors.FailedPrecondition, "decrypt", core.CodeInvalidArgument, msgValueCannotDecrypt, false},
	}
	for _, tc := range cases {
		var keeperOpVal keeperOp
		switch tc.op {
		case "decrypt":
			keeperOpVal = opDecryptValue
		case "encrypt":
			keeperOpVal = opEncryptValue
		case "unwrap":
			keeperOpVal = opUnwrapKeyset
		case "wrap":
			keeperOpVal = opWrapKeyset
		}
		var code gcerrors.ErrorCode
		if tc.dec != gcerrors.OK {
			code = tc.dec
		} else {
			code = tc.enc
		}
		err := classifyKeeperErr(ctx, &codeErr{code: code}, tc.scheme, keeperOpVal)
		var pe *core.Error
		if !errors.As(err, &pe) {
			t.Fatalf("%s %s: %v", tc.scheme, tc.op, err)
		}
		if pe.Code != tc.wantCode || pe.Message != tc.wantMsg || core.IsRetryable(err) != tc.retryable {
			t.Fatalf("%s %s: got %s %q retry=%v", tc.scheme, tc.op, pe.Code, pe.Message, core.IsRetryable(err))
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ks := keeperWithScheme("awskms", gcerrors.OK, gcerrors.Unknown)
	_, err := ks.decryptValue(cancelled, []byte("ct"))
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("cancelled: %v", err)
	}
	_, err = ks.decryptValue(ctx, []byte("ct"))
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("net path via unknown decrypt: %v", err)
	}
	err = classifyKeeperErr(ctx, context.Canceled, "awskms", opDecryptValue)
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("ctx canceled err: %v", err)
	}
	err = classifyKeeperErr(ctx, context.DeadlineExceeded, "awskms", opDecryptValue)
	if core.CodeOf(err) != core.CodeTimeout || !core.IsRetryable(err) {
		t.Fatalf("deadline: %v", err)
	}
	err = classifyKeeperErr(ctx, &net.OpError{Op: "dial", Err: errors.New("refused")}, "awskms", opDecryptValue)
	if core.CodeOf(err) != core.CodeUnavailable || !core.IsRetryable(err) {
		t.Fatalf("net err: %v", err)
	}
	err = classifyKeeperErr(ctx, &codeErr{code: gcerrors.PermissionDenied}, "awskms", opEncryptValue)
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("permission denied encrypt: %v", err)
	}
}

func TestPortDecryptCloudUnknownUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ks := keeperWithScheme("awskms", gcerrors.OK, gcerrors.Unknown)
	p := &Port{ks: ks}
	_, err := p.Decrypt(ctx, []byte("ct"))
	if err == nil || core.CodeOf(err) != core.CodeUnavailable || !core.IsRetryable(err) {
		t.Fatalf("decrypt: %v", err)
	}
}

func TestCodecDecodeColdMasterUnreachable(t *testing.T) {
	t.Parallel()
	ks := &budgetKey{id: "mk"}
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	c, err := NewCodec(store, []KeyService{ks})
	if err != nil {
		t.Fatal(err)
	}
	ctx := core.WithTenant(context.Background(), "acme")
	enc, err := c.Encode(ctx, plainPayload("hi"))
	if err != nil {
		t.Fatal(err)
	}
	ks.failUnwrap = true
	c2, _ := NewCodec(store, []KeyService{ks})
	_, err = c2.Decode(ctx, enc)
	if err == nil || core.CodeOf(err) != core.CodeUnavailable || !core.IsRetryable(err) {
		t.Fatalf("decode: %v", err)
	}
}

type casOnceStore struct {
	memKeysetStore
	casCalls int32
}

func (s *casOnceStore) CompareAndSwap(ctx context.Context, tenant string, expect uint64, record StoredKeyset) error {
	if atomic.AddInt32(&s.casCalls, 1) > 1 {
		return errConflict(msgKeysetRevisionConflict)
	}
	return s.memKeysetStore.CompareAndSwap(ctx, tenant, expect, record)
}

func TestRotateDataKeyCASConflict(t *testing.T) {
	t.Parallel()
	store := &casOnceStore{memKeysetStore: memKeysetStore{data: map[string]StoredKeyset{}}}
	ks := &budgetKey{id: "mk"}
	c1, _ := NewCodec(store, []KeyService{ks})
	c2, _ := NewCodec(store, []KeyService{ks})
	ctx := core.WithTenant(context.Background(), "acme")
	p := Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain")}, Data: []byte("x")}
	if _, err := c1.Encode(ctx, p); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { done <- c1.RotateDataKey(ctx) }()
	go func() { done <- c2.RotateDataKey(ctx) }()
	var conflict bool
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil && core.CodeOf(err) == core.CodeConflict {
			conflict = true
		}
	}
	if !conflict {
		t.Fatal("expected conflict from simultaneous rotation")
	}
}
