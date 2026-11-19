package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
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

type engine struct {
	cfg Config
	rt  Runtime

	seq           int64
	attemptEvents []model.Event
	records       []AttemptRecord
}

func (e *engine) call(ctx context.Context) (CallResult, error) {
	cfg := e.cfg
	budget := cfg.Attempts
	if budget <= 0 {
		budget = DefaultAttempts
	}
	timeouts := cfg.Timeouts.WithDefaults()

	spec := cfg.Structured
	var rungs []structured.Rung
	if spec != nil {
		rungs = spec.Supported()
		if len(rungs) == 0 {
			return CallResult{}, core.NewError(core.CodeUnsupported,
				"model supports no structured-output rung",
				map[string]any{"model": cfg.Model})
		}
	}

	start := e.rt.Now()
	var callDeadline time.Time
	if timeouts.CallTotal > 0 {
		callDeadline = start.Add(timeouts.CallTotal)
	}

	attempt := 0
	rungIdx := 0
	retries := 0
	forcedRef := ""
	var lastAnswer json.RawMessage
	var lastErr error

	for attempt < budget {
		rung := structured.RungNone
		repair := false
		if spec != nil {
			if rungIdx < len(rungs) {
				rung = rungs[rungIdx]
			} else {
				repair = true
				rung = structured.RungLocalRepair
			}
		}
		attempt++
		ref := forcedRef
		if ref == "" {
			ref = structured.ClientRef(cfg.ClientRef, attempt)
		}
		forcedRef = ""
		started := e.rt.Now()
		e.attemptEvents = nil

		if repair {
			answer, ok := spec.Repair(lastAnswer)
			rec := AttemptRecord{
				Number: attempt, Rung: rung, ClientRef: ref,
				Start: started, End: e.rt.Now(), LocalRepair: true,
			}
			if ok {
				rec.Outcome = OutcomeRepaired
				e.log(rec)
				e.recordAttempt(rec)
				e.appendAttempt(rec)
				return e.done(model.Result{}, answer, nil)
			}
			rec.Outcome = OutcomeFailed
			rec.Code = core.CodeInvalidArgument
			e.log(rec)
			e.recordAttempt(rec)
			e.appendAttempt(rec)
			lastErr = e.noAttemptsError(attempt)
			break
		}

		if attempt >= 2 {
			e.emit(model.Event{Kind: model.EventReset, Attempt: attempt})
		}
		req, err := e.requestFor(rung)
		if err != nil {
			return CallResult{}, err
		}
		res, timedOut, events, attemptErr := e.runAttempt(ctx, Attempt{
			Number: attempt, Rung: rung, ClientRef: ref, Request: req,
		})
		ended := e.rt.Now()

		usage := model.Usage{}
		if res.Usage != nil {
			usage = *res.Usage
		}
		cost := int64(0)
		if cfg.Price != nil {
			cost = cfg.Price(usage)
		}
		rec := AttemptRecord{
			Number: attempt, Rung: rung, ClientRef: ref,
			Start: started, End: ended, Usage: usage, CostMpts: cost, Events: events,
		}
		switch {
		case timedOut:
			rec.Outcome = OutcomeCancelled
			rec.Code = core.CodeTimeout
		case attemptErr != nil:
			rec.Outcome = OutcomeFailed
			rec.Code = core.CodeOf(attemptErr)
		default:
			rec.Outcome = OutcomeSucceeded
		}
		e.meter(rec)
		e.log(rec)
		e.recordAttempt(rec)
		e.appendAttempt(rec)

		if attemptErr != nil {
			lastErr = attemptErr
			if timedOut {
				lastErr = timeoutError("attempt")
			}
			if core.IsRetryable(lastErr) && events == 0 && attempt < budget {
				wait := e.retryWait(lastErr, retries)
				retries++
				if !callDeadline.IsZero() && !e.rt.Now().Add(wait).Before(callDeadline) {
					lastErr = timeoutError("call total")
					break
				}
				if err := e.rt.Sleep(ctx, wait); err != nil {
					lastErr = mapContextErr(ctx, err)
					break
				}
				if UnacknowledgedStart(attemptErr) {
					forcedRef = ref
				}
				continue
			}
			break
		}

		if spec != nil {
			answer, ok := spec.Validate(res.Message)
			if !ok {
				lastAnswer = res.Message
				if rungIdx+1 < len(rungs) {
					rungIdx++
					continue
				}
				rungIdx = len(rungs)
				if attempt >= budget {
					lastErr = e.noAttemptsError(attempt)
					break
				}
				continue
			}
			return e.done(res, answer, nil)
		}
		return e.done(res, nil, nil)
	}

	if lastErr == nil {
		lastErr = e.noAttemptsError(attempt)
	}
	return e.done(model.Result{}, nil, lastErr)
}

func (e *engine) requestFor(rung structured.Rung) (model.Request, error) {
	if rung == structured.RungNone {
		return e.cfg.Request, nil
	}
	return structured.RequestFor(e.cfg.Request, rung, e.cfg.Structured.Schema())
}

// runAttempt runs one provider invocation under the attempt's three timers.
func (e *engine) runAttempt(parent context.Context, a Attempt) (model.Result, bool, int, error) {
	timeouts := e.cfg.Timeouts.WithDefaults()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var mu sync.Mutex
	timedOut := false
	what := ""
	fire := func(reason string) {
		mu.Lock()
		if !timedOut {
			timedOut = true
			what = reason
		}
		mu.Unlock()
		cancel()
	}

	first := e.rt.AfterFunc(timeouts.FirstChunk, func() { fire("first-chunk") })
	total := e.rt.AfterFunc(timeouts.AttemptTotal, func() { fire("attempt total") })
	var idle Timer
	stopIdle := func() {
		if idle != nil {
			idle.Stop()
			idle = nil
		}
	}

	events := 0
	emit := func(ev model.Event) {
		if first != nil {
			first.Stop()
			first = nil
		}
		stopIdle()
		idle = e.rt.AfterFunc(timeouts.Idle, func() { fire("idle") })
		events++
		ev.Attempt = a.Number
		e.emit(ev)
	}

	res, err := e.cfg.Provider.Attempt(ctx, a, emit)
	if first != nil {
		first.Stop()
	}
	total.Stop()
	stopIdle()

	mu.Lock()
	to, reason := timedOut, what
	mu.Unlock()
	if to {
		return res, true, events, timeoutError(reason)
	}
	if err != nil {
		return res, false, events, mapContextErr(ctx, err)
	}
	return res, false, events, nil
}

func (e *engine) done(res model.Result, answer json.RawMessage, err error) (CallResult, error) {
	if answer != nil {
		message, marshalErr := json.Marshal(map[string]any{
			"role":    "assistant",
			"content": string(answer),
		})
		if marshalErr != nil {
			err = core.NewError(core.CodeInternal, "cannot encode the structured answer", nil)
		} else {
			res.Message = message
		}
	}
	result := CallResult{
		Result:   res,
		Message:  res.Message,
		Attempts: append([]AttemptRecord(nil), e.records...),
	}
	if e.cfg.Recorder != nil {
		e.cfg.Recorder.finish(res, err)
		result.Recording = e.cfg.Recorder.Recording()
	}
	if err != nil && e.cfg.Fallback != nil && e.cfg.Fallback(err) {
		result.FallbackTriggered = true
	}
	return result, err
}

func (e *engine) emit(ev model.Event) {
	e.seq++
	ev.Seq = e.seq
	e.attemptEvents = append(e.attemptEvents, ev)
	if e.cfg.Emit != nil {
		e.cfg.Emit(ev)
	}
}

func (e *engine) meter(rec AttemptRecord) {
	if e.cfg.Meter == nil {
		return
	}
	e.cfg.Meter.Record(AttemptMeter{
		Attempt:  rec.Number,
		Usage:    rec.Usage,
		CostMpts: rec.CostMpts,
	})
}

func (e *engine) log(rec AttemptRecord) {
	if e.cfg.CallLog == nil {
		return
	}
	e.cfg.CallLog.Record(CallLogEntry{
		Attempt:       rec.Number,
		ClientRef:     rec.ClientRef,
		CredentialRef: e.cfg.CredentialRef,
		Model:         e.cfg.Model,
		Rung:          rec.Rung,
		Start:         rec.Start,
		End:           rec.End,
		State:         rec.Outcome,
		Code:          rec.Code,
		CostMpts:      rec.CostMpts,
	})
}

func (e *engine) appendAttempt(rec AttemptRecord) {
	e.records = append(e.records, rec)
	if e.cfg.OnAttempt != nil {
		e.cfg.OnAttempt(rec)
	}
}

func (e *engine) recordAttempt(rec AttemptRecord) {
	if e.cfg.Recorder == nil {
		return
	}
	e.cfg.Recorder.add(RecordedAttempt{
		Number:    rec.Number,
		Rung:      rec.Rung,
		ClientRef: rec.ClientRef,
		Events:    append([]model.Event(nil), e.attemptEvents...),
		Usage:     usagePtr(rec.Usage),
		Outcome:   rec.Outcome,
		Code:      rec.Code,
	})
}

func usagePtr(u model.Usage) *model.Usage {
	if u == (model.Usage{}) {
		return nil
	}
	return &u
}

func (e *engine) noAttemptsError(attempts int) error {
	return core.NewError(core.CodeInvalidArgument,
		"no attempt satisfied the output schema",
		map[string]any{"model": e.cfg.Model, "attempts": attempts})
}

func timeoutError(what string) *core.Error {
	return &core.Error{
		Code:      core.CodeTimeout,
		Message:   what + " timeout",
		Retryable: true,
	}
}

// mapContextErr turns a cancelled attempt context into the platform taxonomy,
// keeping a provider's own *core.Error untouched.
func mapContextErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return model.MapError(ctxErr)
	}
	return model.MapError(err)
}
