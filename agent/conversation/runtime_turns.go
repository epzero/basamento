package conversation

import (
	"context"
	"encoding/json"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// CodeNoTurnPartial is the code RuntimeTurns.Partial reports: no durable turn
// partial exists yet. A cancelled or failed turn must record the model text it
// had written (REQ-CONV-009, REQ-CONV-013), but reading it from the running
// child is not replay-deterministic and the runtime's control plane exposes no
// turn output. The journaled partial is WP-L7-04's design; until it lands,
// those requirements are unproven on a real runtime and this code names the gap
// instead of hiding it behind a generic unsupported.
const CodeNoTurnPartial core.Code = "turn_partial_unavailable"

// SteerInput is the payload the coordinator delivers to a running turn's
// typed input channel (design §3).
type SteerInput struct {
	Text string `json:"text"`
}

// RuntimeTurns implements Turns over a runtime's child-run control plane: a
// steering message resolves the child run's input gate and a turn cancel
// cancels the child run. The composition root passes it to New.
type RuntimeTurns struct {
	Runtime runtime.Runtime
	Gate    string // the child's steering gate; empty means "steer"
}

// Steer delivers text to the running child run's steering gate.
func (t RuntimeTurns) Steer(ctx context.Context, child runtime.RunRef, text string) error {
	if t.Runtime == nil {
		return core.NewError(core.CodeUnsupported, "conversation: no runtime for steering", nil)
	}
	gate := t.Gate
	if gate == "" {
		gate = "steer"
	}
	payload, err := json.Marshal(SteerInput{Text: text})
	if err != nil {
		return core.NewError(core.CodeInternal, "conversation: encode steering input", map[string]any{"cause": err.Error()})
	}
	return t.Runtime.Resolve(ctx, child, gate, payload)
}

// Cancel cancels the running child run.
func (t RuntimeTurns) Cancel(ctx context.Context, child runtime.RunRef, reason string) error {
	if t.Runtime == nil {
		return core.NewError(core.CodeUnsupported, "conversation: no runtime for cancelling a turn", nil)
	}
	return t.Runtime.Cancel(ctx, child, reason)
}

// Partial reports the model messages a running turn has published so far. It
// is a stub: the runtime's control plane exposes no turn output, and a live
// child query is not replay-safe. A durable partial for cancelled and failed
// turns (REQ-CONV-009, REQ-CONV-013) is WP-L7-04's design; until then the
// coordinator records such a turn without its partial text and this returns
// CodeNoTurnPartial so the gap stays visible.
func (t RuntimeTurns) Partial(context.Context, runtime.RunRef) ([]ModelMessage, error) {
	return nil, core.NewError(CodeNoTurnPartial, "conversation: no durable turn partial exists (WP-L7-04)", nil)
}
