package model

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func TestMapError(t *testing.T) {
	platform := core.NewError(core.CodeRateLimited, "slow down", nil)
	cases := []struct {
		name      string
		err       error
		wantCode  core.Code
		retryable bool
	}{
		{name: "nil", err: nil},
		{name: "platform passes through", err: platform, wantCode: core.CodeRateLimited},
		{name: "canceled", err: context.Canceled, wantCode: core.CodeCancelled},
		{name: "deadline", err: context.DeadlineExceeded, wantCode: core.CodeTimeout, retryable: true},
		{name: "vendor", err: errors.New("boom"), wantCode: core.CodeVendor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapError(tc.err)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("MapError(nil) = %v, want nil", got)
				}
				return
			}
			if code := core.CodeOf(got); code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
			if tc.retryable != core.IsRetryable(got) {
				t.Fatalf("retryable = %v, want %v", core.IsRetryable(got), tc.retryable)
			}
		})
	}
}

func TestFailureOf(t *testing.T) {
	if got := FailureOf(core.NewError(core.CodeConflict, "taken", nil)); got.Code != core.CodeConflict {
		t.Fatalf("code = %q, want conflict", got.Code)
	}
	if got := FailureOf(&core.Error{Code: core.CodeRateLimited, Retryable: true, RetryAfter: 5 * time.Second}); got.RetryAfter != 5*time.Second {
		t.Fatalf("retry after = %v, want 5s", got.RetryAfter)
	}
	if got := FailureOf(errors.New("boom")); got.Code != core.CodeVendor || got.Message != "vendor call failed" {
		t.Fatalf("got %+v, want vendor_error with fixed message", got)
	}
	if got := FailureOf(MapError(errors.New("401: invalid key sk-secret"))); got.Message != "vendor call failed" {
		t.Fatalf("message = %q, want vendor call failed", got.Message)
	}
	if got := FailureOf(nil); got.Code != core.CodeInternal {
		t.Fatalf("code = %q, want internal", got.Code)
	}
}

func TestSelect(t *testing.T) {
	ds := []Descriptor{
		{ID: "chat-1", Caps: []Capability{CapChat}},
		{ID: "embed-1", Caps: []Capability{CapEmbed}},
		{ID: "both", Caps: []Capability{CapChat, CapEmbed}},
	}
	if d, err := Select(ds, "chat-1", CapChat); err != nil || d.ID != "chat-1" {
		t.Fatalf("select chat = (%+v, %v)", d, err)
	}
	if d, err := Select(ds, "both", CapEmbed); err != nil || d.ID != "both" {
		t.Fatalf("select both/embed = (%+v, %v)", d, err)
	}
	if _, err := Select(ds, "chat-1", CapEmbed); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("wrong capability code = %q, want unsupported", core.CodeOf(err))
	}
	if _, err := Select(ds, "missing", CapChat); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("unknown model code = %q, want not_found", core.CodeOf(err))
	}
}
