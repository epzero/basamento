# ADR-0003 — Flows as data, a pure interpreter, CEL

**Status:** accepted · **Date:** 2026-11-01

## Context
Flows are authored by developers, by end users and by LLMs (for example in code generation). They must be versioned, inspectable and deterministic on replay. Code-only graph libraries (Eino `compose`, LangGraph) are not serializable and not deterministic as Temporal workflow code (goroutines, wall clock, map iteration).

## Decision
- A flow is a content-addressed IR (nodes, edges, state schema, reducers). Runs pin the hash.
- A pure `flow.Machine` interprets it: superstep scheduling, reducers merging in node-id order, guarded cycles, budget in state.
- Expressions use CEL (`github.com/cel-expr/cel-go`): type-checked against the state schema at compile time, cost-limited, with no clock or random functions and with sorted map iteration. JSON mappings use gojq.
- Authoring: YAML, a typed Go builder, or LLM-generated IR. All compile through one compiler.

## Consequences
- Same behavior on every runtime; property tests over completion orders.
- LangGraph's concepts map directly (StateGraph → state and reducers, conditional edges → `when`, Send → `map`, interrupt → `gate`, checkpointer → runtime history), without its API.
