package temporal

import (
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestRetryPolicyDefaultAttempts(t *testing.T) {
	p := activityRetryPolicy(runtime.StepOptions{})
	if p.MaximumAttempts != int32(defaultAttempts) {
		t.Fatalf("maximum attempts %d, want %d", p.MaximumAttempts, defaultAttempts)
	}
}

func TestRetryPolicyHonorsMaxAttempts(t *testing.T) {
	p := activityRetryPolicy(runtime.StepOptions{MaxAttempts: 4})
	if p.MaximumAttempts != 4 {
		t.Fatalf("maximum attempts %d, want 4", p.MaximumAttempts)
	}
}

func TestRetryableHonorsNonRetryable(t *testing.T) {
	opts := runtime.StepOptions{NonRetryable: []string{"vendor_error"}}
	err := &core.Error{Code: "vendor_error", Retryable: true}
	if retryable(opts, err) {
		t.Fatal("listed code was still retryable")
	}
}

func TestFromActivityErrorPreservesCode(t *testing.T) {
	err := FromActivityError(activityApplicationError(&core.Error{Code: "vendor_error", Message: "fail", Retryable: true}))
	if core.CodeOf(err) != "vendor_error" {
		t.Fatalf("code %q, want vendor_error", core.CodeOf(err))
	}
	if !core.IsRetryable(err) {
		t.Fatal("retryable flag lost")
	}
}
