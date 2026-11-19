package middleware

import (
	"context"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/structured"
)

// DefaultAttempts is the attempt budget of a chat call that does not set one
// (REQ-MODEL-043).
const DefaultAttempts = 3

// Timeouts are the time limits of one chat call. A zero value takes the
// streaming defaults of REQ-MODEL-059; CallTotal is off unless the call sets
// it.
type Timeouts struct {
	FirstChunk   time.Duration // no event at all within this time
	Idle         time.Duration // no event in an attempt already streaming
	AttemptTotal time.Duration // one streaming attempt
	CallTotal    time.Duration // the whole call, attempts and waits included
}

// WithDefaults fills the streaming defaults of REQ-MODEL-059.
func (t Timeouts) WithDefaults() Timeouts {
	if t.FirstChunk <= 0 {
		t.FirstChunk = 30 * time.Second
	}
	if t.Idle <= 0 {
		t.Idle = 60 * time.Second
	}
	if t.AttemptTotal <= 0 {
		t.AttemptTotal = 10 * time.Minute
	}
	return t
}

// Attempt identifies one model invocation of a call.
type Attempt struct {
	Number    int
	Rung      structured.Rung
	ClientRef string
	Request   model.Request
}

// Provider runs one model invocation. Attempt must call emit for every event
// as it arrives and return when the invocation ends or ctx is cancelled. It
// returns *core.Error failures so the retry stage can classify them; a
// StartError marks a start the provider did not acknowledge (REQ-MODEL-005).
type Provider interface {
	Attempt(ctx context.Context, a Attempt, emit func(model.Event)) (model.Result, error)
}

// PriceFunc turns one attempt's usage into whole milli-points (REQ-MODEL-060).
// The price table of WP-L3-06 supplies it; a call without one meters zero.
type PriceFunc func(model.Usage) int64

// Rand is the call's seeded randomness (REQ-MODEL-051). Int64n returns a
// uniform value in [0, n).
type Rand interface {
	Int64n(n int64) int64
}

// Fallback is consulted once the primary call has failed. It is the seam of
// the fallback stage in REQ-MODEL-064; which model replaces the failed one is
// the router's decision (WP-L3-04). A true answer marks the stage as triggered.
type Fallback func(err error) bool

// Config is one logical chat call.
type Config struct {
	Model     string
	ClientRef string
	Request   model.Request

	// Structured turns the call into a structured-output call. Nil leaves the
	// rung at none.
	Structured *structured.Spec
	// Attempts is the shared attempt budget; zero takes DefaultAttempts.
	Attempts int

	Provider Provider
	Runtime  Runtime
	Rand     Rand
	Price    PriceFunc

	Meter    Meter
	CallLog  CallLog
	Recorder *Recorder
	// Replay short-circuits the call with a recorded one: no provider, no
	// metering, one call-log entry (MODEL-064-02).
	Replay *Recording

	CredentialRef string
	Timeouts      Timeouts
	Fallback      Fallback

	// Emit receives the call's events in order. The reset event of a retried
	// attempt precedes that attempt's events (REQ-MODEL-055).
	Emit func(model.Event)
	// OnAttempt receives each attempt when it ends, while the call is still
	// running. CallResult.Attempts holds the same records at the end.
	OnAttempt func(AttemptRecord)
}

func (c Config) validate() error {
	if c.Model == "" {
		return core.NewError(core.CodeInvalidArgument, "call has no model", nil)
	}
	if c.ClientRef == "" {
		return core.NewError(core.CodeInvalidArgument, "call has no client ref", nil)
	}
	if c.Provider == nil && c.Replay == nil {
		return core.NewError(core.CodeInvalidArgument, "call has no provider", nil)
	}
	return nil
}

// Call runs one logical chat call and returns its result and terminal error.
// Every error is a *core.Error.
func Call(ctx context.Context, cfg Config) (CallResult, error) {
	if err := cfg.validate(); err != nil {
		return CallResult{}, err
	}
	if cfg.Replay != nil {
		return replayCall(cfg)
	}
	e := &engine{cfg: cfg, rt: cfg.Runtime}
	if e.rt == nil {
		e.rt = SystemRuntime{}
	}
	if e.cfg.Rand == nil {
		e.cfg.Rand = zeroRand{}
	}
	return e.call(ctx)
}
