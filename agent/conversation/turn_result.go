package conversation

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/epzero/basamento/agent"
	"github.com/epzero/basamento/core"
)

// decodeTurnResult turns a child run's final state into the turn's output.
// A flow run returns its state, not a TurnOutput. When outKey names the state
// key of the turn flow's agent node (Input.TurnOut), the value under it is an
// agent.Output and its turns after the carried history become the turn's
// messages. Without a key the flow's final state itself must be a TurnOutput.
func decodeTurnResult(raw json.RawMessage, outKey string, carried int) (TurnOutput, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return TurnOutput{}, core.NewError(core.CodeInternal, "conversation: the turn flow returned no state", nil)
	}
	if outKey == "" {
		var out TurnOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			return TurnOutput{}, core.NewError(core.CodeInternal,
				"conversation: the turn result is not a turn output: "+err.Error(), nil)
		}
		switch out.State {
		case TurnCompleted, TurnFailed, TurnCancelled:
			return out, nil
		}
		return TurnOutput{}, core.NewError(core.CodeInternal,
			fmt.Sprintf("conversation: the turn flow's final state is %q, not a turn output", out.State),
			map[string]any{"state": out.State})
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		return TurnOutput{}, core.NewError(core.CodeInternal,
			"conversation: the turn flow's state is not an object: "+err.Error(), nil)
	}
	value, ok := state[outKey]
	if !ok {
		return TurnOutput{}, core.NewError(core.CodeInternal,
			fmt.Sprintf("conversation: the turn flow's state has no %q", outKey), map[string]any{"turn_out": outKey})
	}
	var out agent.Output
	if err := json.Unmarshal(value, &out); err != nil {
		return TurnOutput{}, core.NewError(core.CodeInternal,
			fmt.Sprintf("conversation: the turn flow's %q is not an agent output: %s", outKey, err.Error()), nil)
	}
	return turnOutput(out, carried), nil
}

// turnOutput translates an agent run's output into a turn's output. Only the
// model turns the agent added after the carried history are the turn's
// messages: the carried turns are the conversation's own history, which the
// coordinator recorded already (REQ-CONV-012). An agent run that ends without
// an answer (a cap) is a failed turn whose code names the cap.
func turnOutput(out agent.Output, carried int) TurnOutput {
	t := TurnOutput{State: TurnCompleted}
	switch out.State {
	case agent.StateCompleted, agent.StateHandoff:
	case agent.StateStepCap, agent.StateCostCap, agent.StateTokenCap, agent.StateTimeCap:
		t.State = TurnFailed
		t.Code = out.State
	default:
		t.State = TurnFailed
	}
	if carried < 0 {
		carried = 0
	}
	for i := carried; i < len(out.Turns); i++ {
		var m struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(out.Turns[i].Message, &m); err != nil || m.Content == "" {
			continue
		}
		t.Messages = append(t.Messages, ModelMessage{Text: m.Content})
	}
	return t
}
