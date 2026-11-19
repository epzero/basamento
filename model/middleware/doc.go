// Package middleware runs every chat call through one fixed chain:
//
//	call log ∘ recording ∘ fallback ∘ metering ∘ structured ∘ retry ∘ timeouts
//
// The chain counts every attempt of a call against one attempt budget, retries
// retryable failures with seeded jitter and Retry-After, times out a stream
// that falls silent, meters every attempt that reached the provider and logs
// one entry per attempt without content or secrets (REQ-MODEL-043 to
// REQ-MODEL-066, ADR-0006). Structured output is model/structured's ladder;
// this package owns the attempts, retries and timeouts around it.
//
// Time, randomness, metering, logging and the provider are injected. Tests
// pass a fake runtime and a seeded source, so the gate never reads a wall
// clock and never depends on global randomness.
package middleware
