// Package agent implements the bounded tool loop used by `agent` flow nodes.
// Each model turn and each tool call is a separate durable Step, so every LLM
// output is recorded, retried and metered. Caps (steps, cost, tokens, time)
// are checked at each turn boundary in flow code. A handoff returns the target
// node and the run's turns to the flow; the target starts with them.
//
// The loop is runtime-neutral: it is written against runtime.DurableContext
// and imports no durable-execution vendor (ADR-0002). It is composed with the
// `model` invocation contract, the `prompt` package and the `tool` port; the
// composition root adapts a flow node to Node and hands the handler to the flow
// host (ADR-0011).
//
// Status: implemented (WP-L7-01). Tools, allowlists, MCP and sandboxes are
// WP-L7-02; conversations are WP-L7-03.
package agent
