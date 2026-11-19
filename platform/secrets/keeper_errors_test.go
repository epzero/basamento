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
	return frameValuePlaintext([]byte("plain")), nil
}

func (f *fakeDriverKeeper) Close() error                           { return nil }
func (f *fakeDriverKeeper) ErrorAs(error, any) bool                { return false }
func (f *fakeDriverKeeper) ErrorCode(err error) gcerrors.ErrorCode { return gcerrors.Code(err) }

var _ driver.Keeper = (*fakeDriverKeeper)(nil)

func keeperWithScheme(scheme string, enc, dec gcerrors.ErrorCode) *keeperKeyService {
	k := gcloudsecrets.NewKeeper(&fakeDriverKeeper{enc: enc, dec: dec})
	return &keeperKeyService{keeper: k, scheme: scheme, id: scheme + "://test"}
}

func assertClassify(t *testing.T, ctx context.Context, scheme string, code gcerrors.ErrorCode, op keeperOp, wantCode core.Code, wantMsg string, wantRetry bool) {
	t.Helper()
	err := classifyKeeperErr(ctx, &codeErr{code: code}, scheme, op)
	var pe *core.Error
	if !errors.As(err, &pe) {
		t.Fatalf("%s %v %v: not *core.Error: %v", scheme, code, op, err)
	}
	if pe.Code != wantCode || pe.Message != wantMsg || core.IsRetryable(err) != wantRetry {
		t.Fatalf("%s %v %v: got code=%s msg=%q retry=%v want code=%s msg=%q retry=%v",
			scheme, code, op, pe.Code, pe.Message, core.IsRetryable(err), wantCode, wantMsg, wantRetry)
	}
}

// TestClassifyKeeperErrTable mirrors design §2.2 (scheme × keeper outcome × operation).
func TestClassifyKeeperErrTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cloudSchemes := []string{"awskms", "gcpkms"}
	localScheme := "localsecrets"
	ops := []struct {
		name string
		op   keeperOp
	}{
		{"unwrap", opUnwrapKeyset},
		{"wrap", opWrapKeyset},
		{"encrypt", opEncryptValue},
		{"decrypt", opDecryptValue},
	}

	for _, scheme := range cloudSchemes {
		for _, op := range ops {
			assertClassify(t, ctx, scheme, gcerrors.InvalidArgument, op.op, cloudInvalidArgCode(op.op), cloudInvalidArgMsg(op.op), false)
			assertClassify(t, ctx, scheme, gcerrors.FailedPrecondition, op.op, cloudInvalidArgCode(op.op), cloudInvalidArgMsg(op.op), false)
		}
		for _, code := range []gcerrors.ErrorCode{
			gcerrors.PermissionDenied, gcerrors.NotFound, gcerrors.Unimplemented, gcerrors.AlreadyExists,
		} {
			for _, op := range ops {
				assertClassify(t, ctx, scheme, code, op.op, deniedRowCode(op.op), deniedRowMsg(op.op), false)
			}
		}
		for _, code := range []gcerrors.ErrorCode{gcerrors.Unknown, gcerrors.Internal, gcerrors.ResourceExhausted} {
			for _, op := range ops {
				assertClassify(t, ctx, scheme, code, op.op, core.CodeUnavailable, scheme, true)
			}
		}
	}

	for _, op := range ops {
		assertClassify(t, ctx, localScheme, gcerrors.InvalidArgument, op.op, localInvalidArgCode(op.op), localInvalidArgMsg(op.op), false)
		assertClassify(t, ctx, localScheme, gcerrors.FailedPrecondition, op.op, localInvalidArgCode(op.op), localInvalidArgMsg(op.op), false)
	}
	for _, code := range []gcerrors.ErrorCode{
		gcerrors.PermissionDenied, gcerrors.NotFound, gcerrors.Unimplemented, gcerrors.AlreadyExists,
	} {
		for _, op := range ops {
			assertClassify(t, ctx, localScheme, code, op.op, deniedRowCode(op.op), deniedRowMsg(op.op), false)
		}
	}
	for _, code := range []gcerrors.ErrorCode{gcerrors.Unknown, gcerrors.Internal, gcerrors.ResourceExhausted} {
		for _, op := range ops {
			assertClassify(t, ctx, localScheme, code, op.op, localOtherCode(op.op), localOtherMsg(op.op), false)
		}
	}

	for _, scheme := range append(cloudSchemes, localScheme) {
		for _, op := range ops {
			cancelled, cancel := context.WithCancel(context.Background())
			cancel()
			err := classifyKeeperErr(cancelled, &codeErr{code: gcerrors.Unknown}, scheme, op.op)
			if core.CodeOf(err) != core.CodeCancelled {
				t.Fatalf("%s %v cancelled: %v", scheme, op.op, err)
			}
			err = classifyKeeperErr(ctx, context.DeadlineExceeded, scheme, op.op)
			if core.CodeOf(err) != core.CodeTimeout || !core.IsRetryable(err) {
				t.Fatalf("%s %v deadline: %v", scheme, op.op, err)
			}
		}
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ks := keeperWithScheme("awskms", gcerrors.OK, gcerrors.Unknown)
	_, err := ks.decryptValue(cancelled, []byte("ct"))
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("cancelled via keeper: %v", err)
	}
	_, err = ks.decryptValue(ctx, []byte("ct"))
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("net path via unknown decrypt: %v", err)
	}
	err = classifyKeeperErr(ctx, &net.OpError{Op: "dial", Err: errors.New("refused")}, "awskms", opDecryptValue)
	if core.CodeOf(err) != core.CodeUnavailable || !core.IsRetryable(err) {
		t.Fatalf("net err: %v", err)
	}
}

func cloudInvalidArgCode(op keeperOp) core.Code {
	if op == opDecryptValue {
		return core.CodeInvalidArgument
	}
	return core.CodeInternal
}

func cloudInvalidArgMsg(op keeperOp) string {
	switch op {
	case opDecryptValue:
		return msgValueCannotDecrypt
	case opUnwrapKeyset:
		return msgKeysetCannotUnwrap
	case opWrapKeyset:
		return msgKeysetCannotWrap
	default:
		return msgKeyServiceRefused
	}
}

func localInvalidArgCode(op keeperOp) core.Code {
	if op == opDecryptValue {
		return core.CodeInvalidArgument
	}
	return core.CodeInternal
}

func localInvalidArgMsg(op keeperOp) string {
	switch op {
	case opDecryptValue:
		return msgValueCannotDecrypt
	case opUnwrapKeyset:
		return msgKeysetCannotUnwrap
	default:
		return msgKeyServiceRefused
	}
}

func deniedRowCode(op keeperOp) core.Code {
	return core.CodeInternal
}

func deniedRowMsg(op keeperOp) string {
	if op == opUnwrapKeyset {
		return msgKeysetCannotUnwrap
	}
	return msgKeyServiceRefused
}

func localOtherCode(op keeperOp) core.Code {
	if op == opDecryptValue {
		return core.CodeInvalidArgument
	}
	return core.CodeInternal
}

func localOtherMsg(op keeperOp) string {
	switch op {
	case opDecryptValue:
		return msgValueCannotDecrypt
	case opUnwrapKeyset:
		return msgKeysetCannotUnwrap
	default:
		return msgKeyServiceRefused
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
