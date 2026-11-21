package temporal

import (
	"time"

	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func activityRetryPolicy(opts runtime.StepOptions) *temporal.RetryPolicy {
	attempts := opts.MaxAttempts
	if attempts <= 0 {
		attempts = defaultAttempts
	}
	policy := &temporal.RetryPolicy{
		InitialInterval:    time.Millisecond,
		BackoffCoefficient: 1.0,
		MaximumInterval:    24 * time.Hour,
		MaximumAttempts:    int32(attempts),
	}
	if len(opts.NonRetryable) > 0 {
		policy.NonRetryableErrorTypes = append([]string(nil), opts.NonRetryable...)
	}
	return policy
}

func retryable(opts runtime.StepOptions, err error) bool {
	if !core.IsRetryable(err) {
		return false
	}
	code := string(core.CodeOf(err))
	for _, listed := range opts.NonRetryable {
		if listed == code {
			return false
		}
	}
	return true
}
