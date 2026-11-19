package middleware

import (
	"errors"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/structured"
)

// RecordedError is a failure captured by a recording, in a form a replay can
// reconstruct.
type RecordedError struct {
	Code       core.Code
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *RecordedError) core() *core.Error {
	if e == nil {
		return nil
	}
	return &core.Error{
		Code:       e.Code,
		Message:    e.Message,
		Retryable:  e.Retryable,
		RetryAfter: e.RetryAfter,
	}
}

// RecordedAttempt is one attempt captured by a recording.
type RecordedAttempt struct {
	Number    int
	Rung      structured.Rung
	ClientRef string
	Events    []model.Event
	Usage     *model.Usage
	Outcome   Outcome
	Code      core.Code
}

// Recording holds one logical call: its attempts, its final result and its
// final error. Its granularity is one call with its attempts inside
// (MODEL-064-01); what a recording contains beyond this is owned elsewhere.
type Recording struct {
	Attempts []RecordedAttempt
	Result   model.Result
	Error    *RecordedError
}

// Recorder captures one call. It is safe to use from the call's goroutine.
type Recorder struct {
	attempts []RecordedAttempt
	result   model.Result
	err      *RecordedError
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder { return &Recorder{} }

func (r *Recorder) add(a RecordedAttempt) { r.attempts = append(r.attempts, a) }

func (r *Recorder) finish(res model.Result, err error) {
	r.result = res
	r.err = nil
	if err == nil {
		return
	}
	out := &RecordedError{Code: core.CodeOf(err), Message: err.Error()}
	var pe *core.Error
	if errors.As(err, &pe) {
		out.Message = pe.Message
		out.Retryable = pe.Retryable
		out.RetryAfter = pe.RetryAfter
	}
	r.err = out
}

// Recording returns a snapshot of the recorded call.
func (r *Recorder) Recording() *Recording {
	if r == nil {
		return nil
	}
	out := &Recording{
		Attempts: append([]RecordedAttempt(nil), r.attempts...),
		Result:   r.result,
		Error:    r.err,
	}
	return out
}

// replayCall answers a call from a recording: it emits the recorded events and
// returns the recorded result, with no provider invocation and no metering.
// The call log still holds one entry for the replayed call (MODEL-064-02).
func replayCall(cfg Config) (CallResult, error) {
	e := &engine{cfg: cfg, rt: cfg.Runtime}
	if e.rt == nil {
		e.rt = SystemRuntime{}
	}
	start := e.rt.Now()
	for _, a := range cfg.Replay.Attempts {
		for _, ev := range a.Events {
			e.emit(ev)
		}
	}
	err := cfg.Replay.Error.core()
	state := OutcomeSucceeded
	code := core.Code("")
	if err != nil {
		state = OutcomeFailed
		code = core.CodeOf(err)
	}
	if cfg.CallLog != nil {
		cfg.CallLog.Record(CallLogEntry{
			Attempt:       1,
			ClientRef:     cfg.ClientRef,
			CredentialRef: cfg.CredentialRef,
			Model:         cfg.Model,
			Rung:          structured.RungNone,
			Start:         start,
			End:           e.rt.Now(),
			State:         state,
			Code:          code,
		})
	}
	records := make([]AttemptRecord, 0, len(cfg.Replay.Attempts))
	for _, a := range cfg.Replay.Attempts {
		records = append(records, AttemptRecord{
			Number: a.Number, Rung: a.Rung, ClientRef: a.ClientRef,
			Outcome: a.Outcome, Code: a.Code,
		})
	}
	return CallResult{
		Result:    cfg.Replay.Result,
		Message:   cfg.Replay.Result.Message,
		Attempts:  records,
		Recording: cfg.Replay,
	}, err
}
