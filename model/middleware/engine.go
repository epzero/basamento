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
