# Model layer

Status: the invocation contract and adapters in `model` and `model/chat`
(WP-L3-01), structured output and the call middleware in `model/structured`
and `model/middleware` (WP-L3-02), the catalog and price table in
`model/catalog` (WP-L3-06) and the model node in `model/node` (WP-L3-03) are
implemented; provider construction and routing **Planned** (WP-L3-04).
Decision: ADR-0006. Specs: `specs/models`.

## 1. One invocation contract

Every model call is an **invocation**, whatever the model does and however
its vendor answers. There is no separate synchronous and asynchronous API.

```go
type Invoker interface {
    Describe(ctx) ([]Descriptor, error)
    Start(ctx, Request) (Handle, error)                    // returns at once; idempotent per ClientRef
    Events(ctx, Handle, after int64) (<-chan Event, error) // ordered partial output: text, reasoning, tool calls, audio, progress, reset
    Result(ctx, Handle) (Result, error)                    // waits for the end
    Cancel(ctx, Handle) error
    Lookup(ctx, clientRef string) (*Handle, error)         // settle an ambiguous start
}
func Invoke(ctx, Invoker, Request) (Result, error)          // blocking form: Start + Result
```

| Vendor protocol | Adapter mapping | Typical capabilities |
| --- | --- | --- |
| `stream` (one request, streamed reply) | `Start` opens the stream; `Events` yields chunks; `Result` is the assembled reply | chat, streaming TTS |
| `single` (one request, one reply) | `Start` sends; `Result` returns the reply; no events | embeddings |
| `poll` | `Start` submits; `Events` reports progress from polling; `Result` when done | video, image, music |
| `webhook` | `Start` submits; the callback resolves the result | video, image |
| `socket` | `Start` submits; progress arrives over a WebSocket | generation servers |
| `command` | `Start` runs a local command; progress parsed from stdout; outputs are files | local tools and apps |

Why one contract: inside a run, a chat reply is just as asynchronous as a
video. It outlives the client connection, streams, can be cancelled, retried
and recorded. A single contract gives every model the same path for budget,
cancellation, reattach and replay. Small calls such as embeddings use the
blocking `Invoke` in process.

`model.Registry` holds what every adapter owes regardless of protocol:
idempotent starts per client ref within the tenant of the call's context, the
ordered event log with replay from a cursor, the result, cancellation and
lookup. It does not grow without bound: an ended invocation is kept for its
retention window after its end and no longer (1 hour by default), and while the
registry holds more ended invocations than its ended bound (10,000 by default)
it evicts them oldest-ended first, never a running one; an evicted client ref
then answers exactly as one the registry never held (REQ-MODEL-088). Both bounds
are set through `NewRegistry`'s options and read on a `core.Clock` the caller
supplies, and the sweep runs inline in the registry's methods: no timer, no
goroutine per invocation, and nothing in the gate depends on wall time. The chat
adapter (`model/chat`) and the embedding adapter (`model.NewEmbedding`) drive
eino models; `Request.Messages` and `Request.Tools` are JSON of `eino/schema`
values, so the contract itself stays free of the dependency. The chat adapter
caps one vendor stream at 8 MiB of payload and 262144 chunks; exceeding either
limit ends the invocation with `vendor_error`. The payload counts assistant text
and reasoning, every string of a tool call, the text and the URL or URI of legacy
multi-content parts, and the text, reasoning, URL, base64 data, file id and MIME
type of assistant-generated parts; Extra and ResponseMeta are metadata and are not
counted. A stream that ends with only vendor metadata
(`Extra`, `ResponseMeta` such as a content filter or refusal finish reason) and
no assistant content is `vendor_error` with a fixed message; provider adapters
map refusals to text or typed errors before that check. A vendor panic inside the
adapter's execute goroutine is converted to `internal` at that boundary without
crashing the process. Unmapped `Request.Params` keys (for example `tier` for
pricing) are ignored; the adapter maps `temperature`, `top_p`, `max_tokens` and
`stop`. Provider construction is WP-L3-04's, including the OpenAI-compatible
mode MiniMax and GLM use.

## 2. Descriptors

Every model has a `Descriptor`:
- capabilities (`chat`, `embed`, `image.*`, `video.*`, `audio.*`, `workflow.graph`, `browser.session`);
- a JSON Schema for its parameters;
- input slots (`messages`, `prompt`, `first_frame`, `reference[]`, `audio`, `mask`);
- a **CEL price formula**, in milli-points (§7);
- its vendor protocol;
- its locality: `cloud`; `machine_local`, which is never routed in the cloud
  profile; or `worker`, served by the workers of a pool (§6).

## 3. Providers

| Provider | Notes |
| --- | --- |
| Chat and embeddings (OpenAI, Anthropic, Gemini, DeepSeek, Qwen, Ark/Doubao, Qianfan, Ollama, OpenRouter) | `eino-ext` adapters and official SDKs behind `Invoker`. MiniMax and GLM use OpenAI-compatible mode. |
| **Generic HTTP API provider** | Configured by a manifest, not code: endpoints for start, status, cancel and lookup; auth by credential reference; request and response mappings (gojq) to our params, inputs and outputs; progress by polling, webhook or WebSocket. Covers most cloud media vendors and local generation servers. |
| **Generic CLI provider** | Configured by a manifest: command template, argument mapping, working directory, output file globs, progress parsing from stdout; `machine_local`. |
| Workers | Models and tools that the workers of a pool declared (§6). |
| Remote invokers | Any service implementing `basamento.invoke.v1.InvocationService` over connect (for example the browser runner, ADR-0009). |
| Vendor-specific adapters | Only where a manifest cannot express the protocol. |

Every provider ships with a **simulator** and contract tests (WP-L3-04).

## 4. The model node in a flow

The `model` node is a node handler in `model/node` (ADR-0011):

1. **Validate** params against the descriptor schema (REQ-MODEL-010).
2. **Reserve** the CEL price estimate against the run budget, or fail with
   `budget_exceeded` (REQ-MODEL-006, REQ-MODEL-007).
3. **Start** step with `ClientRef = hash(run, node path, visit, input)`. It is
   stable across retries of one logical invocation (REQ-MODEL-004) and
   different for the next one: a node visited again on a cycle, or another
   item of a `map`, starts a new invocation even when its input is the same.
   The node path includes the `map` index and the enclosing subflows.
4. **Follow** events to the bus (`streaming.md`), and wait for the result in a
   heartbeating step, or through a gate resolved by a webhook.
5. **Ingest** output files into content-addressed blob storage with provenance
   (REQ-MODEL-008).
6. **Settle**: charge the actual cost or release the reservation. The cost is
   the provider's, or else the price formula on the actual usage; a run
   without a budget is unmetered and reserves nothing.

**Ambiguous start** (for example a timeout after sending): call `Lookup`. If
the provider can't answer, open the `ambiguous_start` gate for a person.
**Never start blindly again** (REQ-MODEL-005). Cancelling the run cancels the
invocation and settles by the provider's final state (REQ-MODEL-009).

## 5. Chat specifics

- **Structured output** (`model/structured`) uses one attempt budget across
  native schema → JSON mode → forced tool call → local JSON repair. The ladder
  moves forward only on an answer that misses the output schema; a retryable
  provider error retries the same rung. The local repair runs in process after
  the last supported rung: it strips prose and code fences and fixes JSON
  syntax, never a field, so a missing field is not fabricated.
- **Middleware order** (`model/middleware`): call log ∘ recording ∘ fallback ∘
  metering ∘ retry (jitter, Retry-After) ∘ timeouts (first chunk, idle, total).
  Every attempt of one call, across rungs and retries, spends one budget of 3
  unless the call sets another; waits double from one second plus up to half
  of it from the call's seeded randomness, and a longer Retry-After wins.
  Streaming defaults are 30 seconds without a first event, 60 seconds
  without one in progress and 10 minutes per attempt, with no call total
  timeout unless the call sets one. Metering records every attempt that reached
  the provider; the call log records one entry per attempt without message
  text or secrets.
- **External services** (the browser runner, a product's own backend) reach
  models through the API's OpenAI-compatible endpoint with scoped tokens, so
  keys, routing and metering stay central.

## 6. Workers

A worker is `kit worker` on any machine: the same providers, loaded from local
manifests, with no flows and no database (ADR-0012).

```mermaid
sequenceDiagram
  participant W as kit worker (LLM keys | ComfyUI | ffmpeg)
  participant P as Platform
  participant Q as Queue (SQL)
  W->>P: Join with pool token: descriptors, labels, capacity
  P->>P: pool stores the descriptors, catalog shows them online
  P->>Q: an invocation for the pool (class, tenant, units)
  W->>P: Lease: free units, warm models, time left
  P-->>W: the next invocation in order that fits
  W->>P: Starting, Started, Events, Finish (files through scoped URLs)
  W->>P: Heartbeat
  P-->>W: cancel these, drain, new expiry
```

| Question | Answer |
| --- | --- |
| Who connects? | The worker dials out, in short calls, so it works behind NAT and survives a dropped connection. |
| Whose work? | Its pool's: one tenant's own machines, or the platform's shared capacity. A pool is a unit of trust; there is no third kind. |
| What is listed? | What the pool's workers declared, kept when they leave: `online`, `offline` or `on_demand`. |
| What does a worker take? | What the platform hands it. It only says how many units are free; a busy worker does not ask. It can also be `draining` or `paused`. |
| In what order? | Class (`interactive`, `normal`, `batch`, with ageing), then fairness between tenants in the shared pool, then age. Running work is never preempted. |
| What if a worker is lost? | Nothing started, or a `restart: safe` model: another worker gets it. Otherwise it is held in doubt for the same worker, then fails with `worker_lost`; the flow decides whether to pay again. |
| What if all are busy? | The invocation waits up to its queue timeout. A tenant may turn on spill-over to another provider of the same model after a waiting time; it is off by default. |
| Where are vendor keys? | On the worker. The platform never sees them. |

**On demand.** A pool can hold worker templates: what a worker would serve,
how many at least and at most, how long a cold start takes, and a launcher.
A scaler compares the queue with the capacity present and pending and asks
the launcher for more; workers leave by themselves when idle. The first
launchers are `http` (a templated request: Modal, Fly, RunPod, a relay) and
`process` (a local command). Services that scale on requests (Cloud Run
services, Lambda) are remote invokers instead and need none of this.

## 7. Prices

Costs are whole **milli-points**. A point is the deployment's unit of account;
configuration sets its value in a currency.

| Piece | What it is |
| --- | --- |
| **Price table** | Per model, an ordered list of **rate rules**. A rule has rates (per million input, output, cached and reasoning tokens; per image, second or character) and an optional `when`. The first rule that holds applies. |
| `when` | CEL over `at` (the start time of the invocation), `params`, `usage` and `tenant`. It expresses off-peak hours, long-context tiers, batch discounts and dated price changes. |
| Layers | A snapshot shipped with the binary, then deployment overrides, then tenant overrides (negotiated rates, markup). |
| **Price formula** | The descriptor's CEL over `params`, `usage` and the chosen `rates`. Token models share a default formula. |
| Reserve | The formula on estimated usage: counted input tokens and the output limit. |
| Settle | The cost the vendor reports, or else the formula on actual usage. |

```yaml
example-chat:
  currency: USD
  rules:
    - when: "at.getHours('UTC') >= 17"        # off-peak window
      rates: { input: 0.14, output: 0.55 }
    - when: "usage.input_tokens > 200000"     # long-context tier
      rates: { input: 0.56, output: 2.20 }
    - rates: { input: 0.28, output: 1.10 }
```

CEL has no clock here: `at` is passed in and recorded with the invocation, so
a replay prices it the same way. A vendor whose pricing no rule can express
gets a formula of its own in its descriptor.

The shipped snapshot is generated from a public dataset (LiteLLM's cost map
or models.dev) and reviewed like a golden file. Nothing fetches prices at run
time, so the same commit prices the same call the same way. `model/catalog`
loads that snapshot from `model/catalog/snapshot.yaml`, applies the deployment
and tenant layers above it, and keeps the entry and start time of a call in a
quote until settlement. A model's own formula must return a whole number of
milli-points in integer arithmetic; rates and priced amounts are never negative.
