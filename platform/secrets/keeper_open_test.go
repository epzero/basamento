package secrets

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/epzero/basamento/core"
	"gocloud.dev/gcerrors"
)

func TestClassifyOpenKeeperErrTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const scheme = "awskms"
	cases := []struct {
		name      string
		ctx       context.Context
		err       error
		wantCode  core.Code
		retryable bool
	}{
		{name: "plain", err: fmt.Errorf("driver rejected url"), wantCode: core.CodeInvalidArgument},
		{name: "invalid arg", err: &codeErr{code: gcerrors.InvalidArgument}, wantCode: core.CodeInvalidArgument},
		{name: "permission", err: &codeErr{code: gcerrors.PermissionDenied}, wantCode: core.CodeInvalidArgument},
		{name: "unknown", err: &codeErr{code: gcerrors.Unknown}, wantCode: core.CodeInvalidArgument},
		{name: "exhausted", err: &codeErr{code: gcerrors.ResourceExhausted}, wantCode: core.CodeUnavailable, retryable: true},
		{name: "cancelled", ctx: cancelledCtx(), err: errors.New("x"), wantCode: core.CodeCancelled},
		{name: "deadline", err: context.DeadlineExceeded, wantCode: core.CodeTimeout, retryable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.ctx
			if c == nil {
				c = ctx
			}
			err := classifyOpenKeeperErr(c, tc.err, scheme)
			if core.CodeOf(err) != tc.wantCode {
				t.Fatalf("code %s want %s: %v", core.CodeOf(err), tc.wantCode, err)
			}
			retry := false
			var pe *core.Error
			if errors.As(err, &pe) {
				retry = pe.Retryable
			}
			if retry != tc.retryable {
				t.Fatalf("retryable=%v want %v", retry, tc.retryable)
			}
			if tc.wantCode == core.CodeInvalidArgument && !strings.Contains(err.Error(), scheme) {
				t.Fatalf("message %q", err.Error())
			}
		})
	}
}

func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestOpenKeyServiceInvalidCloudURL(t *testing.T) {
	t.Parallel()
	_, err := OpenKeyService(context.Background(), "awskms://alias/foo?bogus=1")
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err=%v", err)
	}
	if core.IsRetryable(err) {
		t.Fatal("retryable")
	}
	if !strings.Contains(err.Error(), "awskms") || strings.Contains(err.Error(), "bogus") {
		t.Fatalf("message %q", err.Error())
	}
}

type closeCountKey struct {
	budgetKey
	closes *atomic.Int32
}

func (c *closeCountKey) Close() error {
	if c.closes != nil {
		c.closes.Add(1)
	}
	return c.budgetKey.Close()
}

func TestOpenCodecClosesKeysWhenNewCodecFails(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	var n atomic.Int32
	k1 := &closeCountKey{budgetKey: budgetKey{id: "dup"}, closes: &n}
	k2 := &closeCountKey{budgetKey: budgetKey{id: "dup"}, closes: &n}
	_, err := NewCodec(store, []KeyService{k1, k2})
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("NewCodec: %v", err)
	}
	if n.Load() != 0 {
		t.Fatalf("NewCodec alone must not close")
	}
	_, err = openCodecWithKeys(context.Background(), store, []KeyService{k1, k2}, nil)
	if err == nil {
		t.Fatal("expected open failure")
	}
	if n.Load() != 2 {
		t.Fatalf("closes %d want 2", n.Load())
	}
}

func openCodecWithKeys(ctx context.Context, store KeysetStore, keys []KeyService, opts []Option) (*Codec, error) {
	c, err := NewCodec(store, keys, opts...)
	if err != nil {
		closeKeyServices(keys)
		return nil, err
	}
	return c, nil
}

func TestOpenCodecRejectsDuplicateMasterURL(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	raw := testLocalSecretsURL(3)
	_, err := OpenCodec(context.Background(), store, raw, []string{raw})
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("OpenCodec duplicate master: %v", err)
	}
}

func testLocalSecretsURL(seed int) string {
	key := bytesFromTestSeed(seed, 32)
	return "localsecrets://" + base64.StdEncoding.EncodeToString(key)
}
