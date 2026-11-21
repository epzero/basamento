package temporal

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/epzero/basamento/core"
)

// DefaultCheckpointEventThreshold is used when Options.CheckpointEventThreshold
// is zero. It sits far below Temporal's 10,240-event warning and the server's
// 51,200-event limit.
const DefaultCheckpointEventThreshold = 2000

// Continuation is the journal a continued workflow execution carries in
// HostInput.Continuation. Its JSON shape is stable and persisted in history.
//
// JSON fields: steps (map step key to StepOutcome), children (map child run id
// to ChildOutcome), gates (map gate name to GateJournal), timers (ordered
// TimerJournal), selects (ordered branch indices), nows (ordered Now samples),
// emitted (count of stored Emit events), emit_failures (terminal Emit failures
// by call ordinal), checkpoints (count of honored Checkpoint calls).
//
// It grows with the run (every finished step result stays in it) and passes
// through the shared data converter like every payload (encrypted per tenant,
// claim-checked over the threshold).
type Continuation struct {
	Steps        map[string]StepOutcome  `json:"steps,omitempty"`
	Children     map[string]ChildOutcome `json:"children,omitempty"`
	Gates        map[string]GateJournal  `json:"gates,omitempty"`
	Timers       []TimerJournal          `json:"timers,omitempty"`
	Selects      []int                   `json:"selects,omitempty"`
	Nows         []time.Time             `json:"nows,omitempty"`
	Emitted      uint64                  `json:"emitted,omitempty"`
	EmitFailures []EmitFailure           `json:"emit_failures,omitempty"`
	Checkpoints  uint64                  `json:"checkpoints,omitempty"`
}

// EmitFailure records a terminal Emit failure at a 1-based call ordinal.
type EmitFailure struct {
	Call uint64       `json:"call"`
	Err  JournalError `json:"err"`
}

// StepOutcome records one finished step keyed by the step memo key (step name
// today, REQ-RT-007).
type StepOutcome struct {
	Result json.RawMessage `json:"result,omitempty"`
	Err    *JournalError   `json:"err,omitempty"`
}

// ChildOutcome records one finished child keyed by child run id.
type ChildOutcome struct {
	Result json.RawMessage `json:"result,omitempty"`
	Err    *JournalError   `json:"err,omitempty"`
}

// JournalError is a core error without Details, safe for JSON history.
type JournalError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// GateJournal records gate registration and settlement for replay after
// ContinueAsNew.
type GateJournal struct {
	Name           string          `json:"name"`
	TimeoutNs      int64           `json:"timeout_ns,omitempty"`
	Deadline       time.Time       `json:"deadline"`
	Resolved       bool            `json:"resolved,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	EndedByTimeout bool            `json:"ended_by_timeout,omitempty"`
	EndedByCancel  bool            `json:"ended_by_cancel,omitempty"`
}

// TimerJournal records one Sleep(d>0) call in order.
type TimerJournal struct {
	Deadline time.Time `json:"deadline"`
	Fired    bool      `json:"fired"`
}

func stepKey(name string) string { return name }

func (c *Continuation) ensure() {
	if c == nil {
		return
	}
	if c.Steps == nil {
		c.Steps = map[string]StepOutcome{}
	}
	if c.Children == nil {
		c.Children = map[string]ChildOutcome{}
	}
	if c.Gates == nil {
		c.Gates = map[string]GateJournal{}
	}
}

func errToJournal(err error) *JournalError {
	if err == nil {
		return nil
	}
	var pe *core.Error
	if errors.As(err, &pe) {
		return &JournalError{
			Code:      string(pe.Code),
			Message:   pe.Message,
			Retryable: pe.Retryable,
		}
	}
	return &JournalError{Code: string(core.CodeInternal), Message: "temporal: operation failed"}
}

func journalToErr(j *JournalError) error {
	if j == nil {
		return nil
	}
	return &core.Error{Code: core.Code(j.Code), Message: j.Message, Retryable: j.Retryable}
}
