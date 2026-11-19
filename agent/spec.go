package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/epzero/basamento/core"
)

// SystemMessage is one system message of an agent. Tokens is the message's
// counted input tokens, used by the memory policy; zero means unknown.
type SystemMessage struct {
	Content string `json:"content"`
	Tokens  int64  `json:"tokens,omitempty"`
}

// UnmarshalJSON accepts either a string or an object, so a flow may write a
// system message as "You are a reviewer." and still give a token count where
// the memory policy needs one.
func (m *SystemMessage) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		return json.Unmarshal(trimmed, &m.Content)
	}
	type plain SystemMessage
	var p plain
	if err := json.Unmarshal(trimmed, &p); err != nil {
		return err
	}
	*m = SystemMessage(p)
	return nil
}

// Caps are an agent's caps. A nil field is no cap; zero is a cap of zero.
type Caps struct {
	Steps    *int      `json:"steps,omitempty"`     // model turns
	CostMpts *int64    `json:"cost_mpts,omitempty"` // milli-points
	Tokens   *int64    `json:"tokens,omitempty"`    // input plus output tokens
	Time     *Duration `json:"time,omitempty"`      // on the run clock
}

// Spec is the `agent` node's spec (specs/agents/design.md §3). The node id is
// the agent's name.
type Spec struct {
	Agent       string          `json:"agent,omitempty"`
	Model       string          `json:"model"`
	System      []SystemMessage `json:"system,omitempty"`
	Tools       []string        `json:"tools,omitempty"`
	Checks      int             `json:"checks,omitempty"`
	Caps        Caps            `json:"caps,omitempty"`
	InputTokens int64           `json:"input_tokens,omitempty"`
}

// parseSpec decodes a node spec, rejecting unknown fields so a typo in a flow
// fails the run instead of silently dropping a cap.
func parseSpec(raw json.RawMessage) (Spec, error) {
	var spec Spec
	if len(bytes.TrimSpace(raw)) == 0 {
		return Spec{}, invalid("agent: node spec is empty", nil)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, invalid("agent: invalid node spec: "+err.Error(), nil)
	}
	if spec.Model == "" {
		return Spec{}, invalid("agent: node spec has no model", nil)
	}
	if spec.Checks <= 0 {
		spec.Checks = 1
	}
	return spec, nil
}

// Duration is a duration on the run clock, written as a Go duration string in
// JSON ("1m", "90s").
type Duration time.Duration

// MarshalJSON writes the duration as a Go duration string, dropping a zero
// seconds part so a minute reads "1m" and not "1m0s".
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(formatDuration(time.Duration(d)))
}

// UnmarshalJSON reads a Go duration string.
func (d *Duration) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(text)
	if err != nil {
		return err
	}
	*d = Duration(parsed)
	return nil
}

// Duration returns the duration as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// formatDuration writes a duration the way a scenario states it: whole minutes
// as "1m" or "2m", whole seconds as "90s", otherwise time.Duration's form.
func formatDuration(d time.Duration) string {
	switch {
	case d == 0:
		return "0s"
	case d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	case d%time.Second == 0:
		return fmt.Sprintf("%ds", d/time.Second)
	default:
		return d.String()
	}
}

func invalid(message string, details map[string]any) *core.Error {
	return core.NewError(core.CodeInvalidArgument, message, details)
}
