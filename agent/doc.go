// Package agent implements the bounded tool loop used by `agent` flow nodes.
// Each model turn and each tool call is a separate durable Step, so every LLM
// output is recorded, retried and metered. Caps (steps, cost, tokens, time)
// are checked at each turn boundary in flow code. A run that does not start
// from a handoff seeds its history with one user message from the node input;
// a request with no non-system message fails before the model is invoked
// (REQ-AGENT-032), while a handoff target carries no user message and must run.
// A handoff returns the target node and the run's turns to the flow; the target
// starts with them and is not seeded again.
//
// The loop is runtime-neutral: it is written against runtime.DurableContext
// and imports no durable-execution vendor (ADR-0002). It is composed with the
// `model` invocation contract, the `prompt` package and the `tool` port; the
// composition root adapts a flow node to Node and hands the handler to the flow
// host (ADR-0011).
//
// The loop does not own long-term memory, permission gating or lifecycle
// policy: those belong to the host, the composition root and the flow around
// the agent node. It takes the node input, the `System` messages and the
// `input_tokens` limit and does the rest by replay: a turn's messages are
// rebuilt from the run's recorded turns. An in-loop policy engine for them
// would be a second, non-deterministic one, which the constitution's
// determinism article rejects.
//
// Status: implemented (WP-L7-01). Tools, allowlists, MCP and sandboxes are
// WP-L7-02; conversations are WP-L7-03.
package agent
