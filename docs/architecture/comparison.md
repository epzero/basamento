# Comparison with similar systems

Where Basamento sits next to the tools people would otherwise pick, and what
it takes from each. Facts about other projects were checked in November 2026;
they move fast, so verify before relying on a cell.

## 1. At a glance

| | Basamento | LangGraph | Dify | n8n | Temporal alone |
| --- | --- | --- | --- | --- | --- |
| What it is | Foundation: one Go binary and library | Python/TypeScript library, plus a hosted platform | Application platform with a visual builder | General automation with AI nodes | Durable execution engine |
| Flow definition | Data: content-addressed IR with CEL, from YAML, a Go builder or an LLM | Code: `StateGraph` in Python or TypeScript | Visual DSL, exportable | Visual DSL | Code: workflow functions |
| Durability | Replay of a recorded history (Temporal); crash resumes mid-run | Checkpoint per superstep; resumes from the last checkpoint | Runs on task queues; no replay | Executions stored; queue mode | Replay of a recorded history |
| Determinism enforced | Yes: pure machine, layering, replay tests | No: node code is free | No | No | By convention and replay tests |
| Runs outlive connections | Yes: cursor reattach with snapshots | With the platform's resumable streams | Partly | Not a streaming product | No client stream |
| Model scope | Chat, embeddings, image, video, audio through one invocation contract | Chat first, through LangChain | Chat, embeddings, rerank, speech through plugins | Per-node integrations | None |
| Money | Reserve before, settle after, no blind restart of a paid start | Left to the application | Usage tracking | No | No |
| Bring your own machine | Workers that declare and pull | Left to the application | Plugins and external tools | Its own workers | Workers on task queues |
| Self-hosting | One static binary, embedded SQLite, Temporal and NATS | A library; the server needs Postgres and Redis | Several services (API, workers, Postgres, Redis, vector store, sandbox, plugin daemon) | One service, or queue mode with Redis and Postgres | A cluster, or the dev server |
| License | Apache-2.0 | MIT library; the platform is commercial | Apache-2.0 with conditions: no multi-tenant service without permission, keep the logo | Sustainable Use License (source-available) | MIT |

## 2. What we take

| From | Idea | Here |
| --- | --- | --- |
| LangGraph | State with reducers, supersteps, conditional edges, `Send`, interrupts | The same model as data: reducers, `when` edges, `map`, `gate` (ADR-0003) |
| Temporal | Replay, activities, Updates, ContinueAsNew | The `temporal` runtime behind `DurableContext` (ADR-0002) |
| Dify | Flows as an artifact that non-developers edit; a model catalog | The IR is the artifact; UIs are built on the API, not in this repository |
| Eino, ADK | Message and tool schema, provider adapters | Used as libraries under `model` (overview §5) |
| ComfyUI | Node graphs for media generation | Called as a model with capability `workflow.graph`, usually through a worker |
| AG-UI | A common event vocabulary for agent UIs | An adapter at the edge (`streaming.md` §7) |

## 3. Where we differ on purpose

- **Flows are data and the interpreter is pure.** A flow can be generated,
  diffed, type-checked and pinned by hash, and it behaves the same on every
  runtime. Code-defined graphs cannot give that.
- **Durability is replay, not checkpoints.** A step that finished never runs
  again after a crash, which is what makes paid calls safe.
- **Every model is an invocation.** A ten-minute video and a chat reply share
  budget, cancellation, reattach and recording.
- **One binary.** Self-hosting needs no other service.
- **No builder UI, no RAG product, no marketplace.** Those belong to products
  built on the foundation.

## 4. When to pick something else

| Need | Pick |
| --- | --- |
| A visual builder and knowledge base for a team, today | Dify |
| An agent inside an existing Python or TypeScript service | LangGraph |
| Connecting SaaS tools with a little AI | n8n |
| Durable business workflows without models | Temporal alone |
