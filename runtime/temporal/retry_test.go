package temporal

import (
	"testing"

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

func TestRetryPolicyBackoffShape(t *testing.T) {
	p := activityRetryPolicy(runtime.StepOptions{})
	if p.InitialInterval.Seconds() != 1 || p.BackoffCoefficient != 2.0 {
		t.Fatalf("backoff %+v, want 1s initial and coefficient 2", p)
	}
}
