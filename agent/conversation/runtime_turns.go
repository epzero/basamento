package conversation

import (
	"context"
	"encoding/json"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

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

// Partial reports the model messages a running turn has published so far.
// The runtime control plane exposes no turn output, so the composition root
// that owns the turn flow (WP-L7-01) supplies this through Turns.Partial; the
// default implementation has nothing to report.
func (t RuntimeTurns) Partial(context.Context, runtime.RunRef) ([]ModelMessage, error) {
	return nil, core.NewError(core.CodeUnsupported, "conversation: the runtime exposes no turn partial", nil)
}
