package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/runtime"
)

func activityRetryPolicy(opts runtime.StepOptions) *temporal.RetryPolicy {
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	policy := &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    100 * time.Second,
		MaximumAttempts:    int32(attempts),
	}
	if len(opts.NonRetryable) > 0 {
		policy.NonRetryableErrorTypes = append([]string(nil), opts.NonRetryable...)
	}
	return policy
}
