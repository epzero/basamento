// Package agent implements the bounded tool loop used by `agent` flow nodes.
// Each model turn and each tool call is a separate durable Step, so every LLM
// output is recorded, retried and metered. Caps (steps, cost, tokens, time)
// are checked in flow code. Handoff returns a target node to the interpreter.
//
// Status: planned (WP-L7-01).
package agent
