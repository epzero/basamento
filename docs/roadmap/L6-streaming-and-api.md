# L6 — Streaming and API

Runs outlive connections (ADR-0005, `specs/streaming`).

### WP-L6-01 — Durable event log
Depends: WP-L1-01, WP-L5-01.
Owns: `stream/eventlog`.
Per-run sequence, outbox written with state changes, relay to the bus.

### WP-L6-02 — Live streaming and reattach
Depends: WP-L6-01, WP-L1-03, WP-L3-03.
Owns: `stream`.
Deltas with `dseq`, snapshots, resets, detach policy, cursor resume over SSE and WebSocket. Specs: `specs/streaming`.

### WP-L6-03 — API surface
Depends: WP-L6-02, WP-L8-02, WP-L1-05.
Owns: `api`, `contracts/basamento/stream`.
connect-go services, OpenAI-compatible model endpoint with scoped tokens, AG-UI stream adapter (`streaming.md` §7), MCP server. Adds `@level:system` to `specs/streaming` and `specs/flow-engine/runs.feature`, and proves tenant isolation through the API (AUTH-002-01).

### WP-L6-04 — Run inspector UI
Depends: WP-L6-03.
Owns: `web/inspector` (TypeScript).
Runs, events, gates, rrweb playback.
