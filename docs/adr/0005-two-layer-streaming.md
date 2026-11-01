# ADR-0005 — Two-layer streaming: SQL event log plus NATS JetStream

**Status:** accepted · **Date:** 2026-11-01

## Context
Users must be able to disconnect while a run (an LLM answer, a video generation) continues, and reattach from any device without losing or duplicating output. Temporal is not a message bus: Signals, Updates and Queries address one workflow. Temporal Workflow Streams (Public Preview) records every publish batch as a Signal and every subscriber poll as an Update in history, needs Temporal clients as subscribers, and does not serve producers outside workflows.

## Decision
- Layer 1: runs never depend on connections; only explicit cancellation or a declared `on_detach` policy stops them.
- Layer 2: durable events (per-run `seq`) are written to SQL with the state change and relayed to the bus; deltas (per-message `dseq`) are bus-only with short retention; reattach = replay durable events after the cursor, snapshot each in-flight message, then live deltas.
- The `Bus` port is backed by NATS JetStream: in-process for `embedded`, a cluster for `cloud`.
- Streaming steps send heartbeats; retries publish `reset`.
- The native stream is the contract. Client protocols such as AG-UI are adapters in `api` that translate it at the edge; none of them defines our events.

## Consequences
One extra stateful service in the cloud profile (NATS). Temporal Workflow Streams may later back the `Bus` for low-rate topics.
