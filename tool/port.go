package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/epzero/basamento/core"
)

// Func runs a tool. Its error carries the core code and message a model sees
// (REQ-TOOL-004, REQ-TOOL-011); its Details never reach a model.
type Func func(ctx context.Context, args json.RawMessage) (Result, error)

// Tool is what one caller may call.
type Tool struct {
	Name        string          // as the model sees it; from an MCP server, mcp_<server>__<tool>
	Description string          // what the model reads
	Input       json.RawMessage // JSON Schema
	Timeout     time.Duration   // 0 = the layer's default
	MaxResult   int             // bytes kept before the result is stored by reference; 0 = default
	Run         Func
}

// Result is a successful tool result.
type Result struct {
	Content    json.RawMessage // or a JSON string, for a tool whose result is text; a sandbox result with both streams is an object
	BlobRef    string          // set instead of Content when the result exceeded MaxResult
	ExitStatus int             // set by a sandbox command (REQ-TOOL-021)
	Truncated  bool            // set by a sandbox result whose output was cut (REQ-TOOL-020)
}

// Set is one caller's tools: the registry names an agent was given.
type Set struct {
	Tools []Tool // sorted by Name, so replay is deterministic
}

// Get returns the tool called name.
func (s *Set) Get(name string) (Tool, bool) {
	if s == nil {
		return Tool{}, false
	}
	for _, t := range s.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Merge adds another set's tools to s, keeping the set sorted. A name that
// appears in both sets is rejected with code conflict: a duplicate would make
// Get return whichever came first, which could route a call to the wrong
// server with its credential.
func (s *Set) Merge(other *Set) error {
	if other == nil || len(other.Tools) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(s.Tools)+len(other.Tools))
	for _, t := range s.Tools {
		seen[t.Name] = true
	}
	for _, t := range other.Tools {
		if seen[t.Name] {
			return core.NewError(core.CodeConflict,
				fmt.Sprintf("tool %q is offered more than once", t.Name), map[string]any{"tool": t.Name})
		}
		seen[t.Name] = true
	}
	s.Tools = append(s.Tools, other.Tools...)
	sort.Slice(s.Tools, func(i, j int) bool { return s.Tools[i].Name < s.Tools[j].Name })
	return nil
}

// Names returns the set's tool names, in order.
func (s *Set) Names() []string {
	if s == nil {
		return nil
	}
	names := make([]string, len(s.Tools))
	for i, t := range s.Tools {
		names[i] = t.Name
	}
	return names
}

// Call is one tool call as the loop or the node received it.
type Call struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Outcome is what a caller gets back: one of Result, or Code and Message.
type Outcome struct {
	Result  Result    `json:"result,omitempty"`
	Code    core.Code `json:"code,omitempty"`
	Message string    `json:"message,omitempty"`
	Failed  bool      `json:"failed,omitempty"`
}

// Text renders what a model sees for the call (REQ-TOOL-011): the result's
// content, or on failure the code and the message, never Details.
func (o Outcome) Text() string {
	if o.Failed {
		if o.Message == "" {
			return string(o.Code)
		}
		return string(o.Code) + ": " + o.Message
	}
	if o.Result.BlobRef != "" {
		return o.Result.BlobRef
	}
	var text string
	if len(o.Result.Content) > 0 && json.Unmarshal(o.Result.Content, &text) == nil {
		return text
	}
	return string(o.Result.Content)
}

// Runner runs one call as the side effect of a durable step. The agent loop
// supplies it; a *Layer's Step is the production implementation (RunStep is
// the package-level convenience). It must not retry a failed call: an Outcome
// records the failure and the step succeeds, and a Go error means the layer
// itself failed.
type Runner func(ctx context.Context, set *Set, call Call) (Outcome, error)
