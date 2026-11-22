package agent

import (
	"bytes"
	"encoding/json"
	"time"
)

// Run states of an agent run's output.
const (
	StateCompleted = "completed"
	StateHandoff   = "handoff"
	StateStepCap   = "step_cap_reached"
	StateCostCap   = "cost_cap_reached"
	StateTokenCap  = "token_cap_reached"
	StateTimeCap   = "time_cap_reached"
)

// Turn is one completed model turn: its assistant message and the tool-result
// messages that followed it. Tokens is the turn's counted usage (input plus
// output), used by the memory policy; Dropped is how many messages the memory
// policy left out before the turn ran.
type Turn struct {
	Message json.RawMessage   `json:"message"`
	Results []json.RawMessage `json:"results,omitempty"`
	Tokens  int64             `json:"tokens,omitempty"`
	Dropped int               `json:"dropped,omitempty"`
}

// Output is an agent node's output, and the input a handoff target receives
// (specs/agents/design.md §4). TimeCap is the time cap in effect for the
// chain, written as a duration string, or null when there is none.
type Output struct {
	State     string     `json:"state"`
	Answer    string     `json:"output,omitempty"`
	Target    string     `json:"target,omitempty"`
	Turns     []Turn     `json:"turns,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	TimeCap   *Duration  `json:"time_cap"`
}

// carried is the part of an incoming handoff the target needs.
type carried struct {
	turns     []Turn
	startedAt time.Time
	hasStart  bool
	timeCap   *Duration
}

// parseHandoff reads an agent node's input. It returns nil when the input is
// not a handoff, so the node starts its own run with a user message from it
// (REQ-AGENT-031): only a JSON object can carry a handoff, so any other JSON
// is plain input rather than an invalid handoff.
func parseHandoff(input json.RawMessage) (*carried, error) {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, nil
	}
	var out Output
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, invalid("agent: invalid handoff input: "+err.Error(), nil)
	}
	if out.State != StateHandoff {
		return nil, nil
	}
	c := &carried{turns: out.Turns, timeCap: out.TimeCap}
	if out.StartedAt != nil {
		c.startedAt = out.StartedAt.UTC()
		c.hasStart = true
	}
	return c, nil
}

// handoff returns the output that ends a run as a handoff.
func handoff(target string, turns []Turn, startedAt time.Time, timeCap *Duration) Output {
	started := startedAt.UTC()
	return Output{
		State:     StateHandoff,
		Target:    target,
		Turns:     turns,
		StartedAt: &started,
		TimeCap:   timeCap,
	}
}
