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

func TestOpenCodecClosesOnPreviousKeyOpenFailure(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	var n atomic.Int32
	k1 := &closeCountKey{budgetKey: budgetKey{id: "a"}, closes: &n}
	open := func(_ context.Context, raw string) (KeyService, error) {
		if raw == "url1" {
			return k1, nil
		}
		return nil, errInvalidArgument("open failed")
	}
	_, err := openCodec(context.Background(), store, "url1", []string{"url2"}, open)
	if err == nil {
		t.Fatal("expected err")
	}
	if n.Load() != 1 {
		t.Fatalf("closes %d want 1", n.Load())
	}
}

func TestOpenCodecClosesKeysWhenNewCodecFails(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	var n atomic.Int32
	k1 := &closeCountKey{budgetKey: budgetKey{id: "dup"}, closes: &n}
	k2 := &closeCountKey{budgetKey: budgetKey{id: "dup"}, closes: &n}
	nOpen := 0
	open := func(_ context.Context, _ string) (KeyService, error) {
		nOpen++
		if nOpen == 1 {
			return k1, nil
		}
		return k2, nil
	}
	_, err := openCodec(context.Background(), store, "u1", []string{"u2"}, open)
	if err == nil {
		t.Fatal("expected err")
	}
	if n.Load() != 2 {
		t.Fatalf("closes %d want 2", n.Load())
	}
}

func TestOpenCodecSuccessNoPrematureClose(t *testing.T) {
	t.Parallel()
	store := &memKeysetStore{data: map[string]StoredKeyset{}}
	var n atomic.Int32
	k := &closeCountKey{budgetKey: budgetKey{id: "only"}, closes: &n}
	open := func(_ context.Context, _ string) (KeyService, error) {
		return k, nil
	}
	c, err := openCodec(context.Background(), store, "u1", nil, open)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if n.Load() != 0 {
		t.Fatalf("closes during successful open %d", n.Load())
	}
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
