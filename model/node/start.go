package node

import (
	"context"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// DefaultStartAttempts bounds the start attempts of one logical invocation.
// It mirrors the model layer's default attempt budget (REQ-MODEL-043): a start
// that fails without reaching the provider is retried, but never forever.
const DefaultStartAttempts = 3

// Starter starts one logical invocation with a stable client ref. Every
// attempt it makes carries req.ClientRef (REQ-MODEL-004), and an attempt whose
// outcome is unknown is resolved by lookup before anything is sent again
// (REQ-MODEL-005).
type Starter struct {
	// Invoker is the provider adapter.
	Invoker model.Invoker
	// MaxAttempts bounds the attempts of one logical invocation; zero means
	// DefaultStartAttempts.
	MaxAttempts int
}

// StartOutcome is how a start ended.
type StartOutcome struct {
	// Handle identifies the invocation. It is set when the invocation is
	// running, whether the start was acknowledged or found by lookup.
	Handle model.Handle `json:"handle"`
	// Attempts is how many times Start was called.
	Attempts int `json:"attempts"`
	// Resolved reports that an ambiguous attempt was resolved by lookup.
	Resolved bool `json:"resolved,omitempty"`
	// Ambiguous reports that the outcome of the start is unknown and the
	// provider cannot answer a lookup. The caller must open the
	// "ambiguous_start" gate instead of starting again (REQ-MODEL-005).
	Ambiguous bool `json:"ambiguous,omitempty"`
}

// Start calls Start until the provider accepts, the outcome is ambiguous, or
// the attempts are spent.
//
// A retryable start error that may have reached the provider (a timeout after
// sending) is resolved by Lookup before anything is sent again:
//   - lookup finds the invocation: the start is resolved, no second start.
//   - lookup answers "nothing" or cannot answer: the outcome is unknown — a
//     forgotten ref cannot prove the start never landed — and the call returns
//     Ambiguous; starting again would risk a second charge (REQ-MODEL-005).
//
// Any other retryable error means the provider rejected the start before
// accepting it, so the next attempt may use the same client ref.
//
// A non-retryable error ends the start at once: the provider refused it and
// there is nothing ambiguous about it.
func (s Starter) Start(ctx context.Context, req model.Request) (StartOutcome, error) {
	if s.Invoker == nil {
		return StartOutcome{}, core.NewError(core.CodeInternal, "model node: no invoker", nil)
	}
	if req.ClientRef == "" {
		return StartOutcome{}, core.NewError(core.CodeInvalidArgument, "model node: request has no client ref", nil)
	}
	attempts := s.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultStartAttempts
	}
	var last error
	for i := 0; i < attempts; i++ {
		handle, err := s.Invoker.Start(ctx, req)
		if err == nil {
			return StartOutcome{Handle: handle, Attempts: i + 1}, nil
		}
		if ctx.Err() != nil {
			// The run was cancelled while the attempt was in flight, so the
			// start may have reached the provider. Resolve it with a context
			// the cancellation no longer reaches: a found invocation is
			// followed (and settled) as usual, and anything else is ambiguous
			// so the reservation settles by the estimate instead of releasing
			// money a landed start may have spent (REQ-MODEL-005).
			found, lookupErr := s.Invoker.Lookup(context.WithoutCancel(ctx), req.ClientRef)
			if lookupErr == nil && found != nil {
				return StartOutcome{Handle: *found, Attempts: i + 1, Resolved: true}, nil
			}
			return StartOutcome{Attempts: i + 1, Ambiguous: true}, nil
		}
		if !core.IsRetryable(err) {
			return StartOutcome{Attempts: i + 1}, model.MapError(err)
		}
		last = err
		if core.CodeOf(err) != core.CodeTimeout {
			// The provider refused the start before accepting it: retrying
			// with the same client ref cannot charge twice.
			continue
		}
		found, lookupErr := s.Invoker.Lookup(ctx, req.ClientRef)
		if lookupErr != nil {
			return StartOutcome{Attempts: i + 1, Ambiguous: true}, nil
		}
		if found != nil {
			return StartOutcome{Handle: *found, Attempts: i + 1, Resolved: true}, nil
		}
		return StartOutcome{Attempts: i + 1, Ambiguous: true}, nil
	}
	return StartOutcome{Attempts: attempts}, model.MapError(last)
}
