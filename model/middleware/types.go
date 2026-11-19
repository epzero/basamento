package middleware

import (
	"encoding/json"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/structured"
)

// Outcome is how one attempt ended.
type Outcome string

const (
	// OutcomeSucceeded is an attempt whose output arrived and held.
	OutcomeSucceeded Outcome = "succeeded"
	// OutcomeFailed is an attempt that failed with an error.
	OutcomeFailed Outcome = "failed"
	// OutcomeCancelled is an attempt a timeout cancelled.
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeRepaired is the local JSON repair attempt (REQ-MODEL-065).
	OutcomeRepaired Outcome = "repaired"
)

// AttemptRecord is what the call observed of one attempt.
type AttemptRecord struct {
	Number      int
	Rung        structured.Rung
	ClientRef   string
	Start       time.Time
	End         time.Time
	Outcome     Outcome
	Code        core.Code
	Usage       model.Usage
	CostMpts    int64
	Events      int
	LocalRepair bool
}

// CallResult is the outcome of one logical chat call. Attempts includes the
// local repair attempt, which is not a model invocation.
type CallResult struct {
	Result   model.Result
	Message  json.RawMessage
	Attempts []AttemptRecord

	// FallbackTriggered reports whether the fallback stage was consulted.
	FallbackTriggered bool
	// Recording is the call's recording when a Recorder was configured, or the
	// replayed one.
	Recording *Recording
}

// AttemptMeter is the usage and cost of one attempt that reached the provider
// (REQ-MODEL-060).
type AttemptMeter struct {
	Attempt  int
	Usage    model.Usage
	CostMpts int64
}

// Meter receives one record per attempt, failed attempts included.
type Meter interface {
	Record(AttemptMeter)
}

// CallLogEntry is one call-log line. It carries no message text and no model
// output, and names a credential only by its reference (REQ-MODEL-063).
type CallLogEntry struct {
	Attempt       int
	ClientRef     string
	CredentialRef string
	Model         string
	Rung          structured.Rung
	Start         time.Time
	End           time.Time
	State         Outcome
	Code          core.Code
	CostMpts      int64
}

// CallLog receives one entry per attempt.
type CallLog interface {
	Record(CallLogEntry)
}
