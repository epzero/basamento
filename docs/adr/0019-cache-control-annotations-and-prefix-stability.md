# ADR-0019 — Cache-control annotations and prefix stability

**Status:** accepted · **Date:** 2026-11-22

## Context

A prompt cache only pays off when a provider sees the same bytes at the start
of a request twice. Basamento computes a cache boundary but never puts it on
the wire, computes only one of the three useful boundaries, and never asserts
that two consecutive agent turns share a prefix. EP0-185 verified the three
gaps.

- **Nothing marks the boundary on the wire.** `model.Request` has no cache
  field (`model/model.go:94-108`). The only chat adapter, `model/chat`, is
  generic over an eino `BaseChatModel` (`model/chat/chat.go:26-50`); it decodes
  messages and maps tools and params only (`model/chat/chat.go:106-126`,
  `model/chat/chat.go:353-386`). The embedding adapter has no cache notion
  (`model/embed.go:27-37`). Provider construction is WP-L3-04 and does not
  exist yet (`model/chat/doc.go:14-16`, `docs/architecture/model-layer.md:3-8`).
  Outside the prompt package and its tests, `CacheAfter` and `PrefixHash`
  appear only in BDD assertions (`internal/bdd/steps/prompt.go:513-545`).
- **One boundary, not three.** `prompt` orders segments static → semi-static →
  dynamic → history → user (`prompt/registry.go:199-205`) and sets
  `CacheAfter` at the last static or semi-static segment
  (`prompt/registry.go:370-386`), with `PrefixHash` over those parts
  (`prompt/hash.go:36-48`). There is no tool breakpoint and no rolling marker
  on the last message. The agent loop builds its request from hand-written
  `Spec.System` and tools (`agent/handler.go:156-173`,
  `agent/memory.go:70-90`, `agent/handler.go:414-454`) and does not import
  `prompt` (`agent/handler.go:3-18`), so the prompt hint cannot reach its
  request.
- **Nothing asserts prefix stability across turns.** The prompt scenarios
  assert two assemblies of one template share a prefix
  (`specs/prompt/segments.feature:120-143`,
  `specs/prompt/requirements.md:59-63`), but nothing asserts that model turn
  `n+1` starts with the prefix turn `n` cached. Cached and uncached input are
  priced differently (`model/catalog/price.go:246-254`), so a missed cache is
  a real cost.
- **Cache writes have no contract.** The three breakpoints write cache entries,
  and vendors bill those writes at a premium. `model.Usage` has no
  cache-write field and the default formula has no `rates.cache_write`
  (`specs/models/design.md` C3), so enabling the breakpoints first would let a
  tenant's bill exceed the reservation and the cost cap. The write contract
  lands before `System` and `Message` are enabled.

The agent turn already journals the whole `model.Request` in its durable step
input (`agent/handler.go:308-312`). That is the right place to pin a cache
hint: a replay reads the recorded request rather than rebuilding a marker from
live state.

## Decision

### 1. A neutral cache hint on the request

`model.Request` gains an optional, vendor-neutral hint, `Cache *CacheHint`:

```go
// CacheHint marks up to three cache breakpoints in a request: the last
// non-empty system message, the last non-empty tool definition, and the last
// message. A nil hint asks for none.
type CacheHint struct {
	System  bool `json:"system,omitempty"`
	Tools   bool `json:"tools,omitempty"`
	Message bool `json:"message,omitempty"`
}
```

It is advisory: a provider that cannot express a breakpoint ignores it, and a
hint never fails an invocation. It carries no `cache_control` JSON, no TTL and
no provider name.

The three anchors are the last **non-empty** system block, the last
**non-empty** tool definition, and the last **non-empty** content block of the
last non-empty message. An empty anchor is skipped: a provider such as
Anthropic rejects an empty cache-control block, and a system message or a
message block may end with `""`.

**The `cache_after` mapping.** A caller that maps an assembled prompt to a
model request places the `static` and `semi-static` parts in system messages
and the `dynamic`, `history` and `user` parts in non-system messages. It sets
`CacheHint.System` only when the last system message ends exactly at the
prompt's `cache_after`. When that precondition fails — a dynamic part follows
the boundary, or the boundary falls inside a system message — the caller sets
no system breakpoint; the request proceeds unannotated and the invocation never
fails for it. `specs/prompt/design.md` states the same constraint.

`model.PrefixHash(r Request) string` returns the SHA-256 of a canonical byte
string: the request's tool definitions in request order, then its system
messages in request order. It is order-sensitive. A tool definition
contributes its name, description and parameter schema; a system message
contributes the JSON object `Request.Messages` carries — `role`, `content`,
`multi_content`, `user_input_multi_content`, `name` and every other field —
with object keys sorted and no insignificant whitespace. The hash is empty
when the request has neither a tool definition nor a system message. It
ignores the hint: two requests with the same tools and system messages hash
the same whether or not they mark breakpoints. It is the model-layer
counterpart of `prompt.PrefixHash` (`prompt/hash.go:36-48`), which covers only
the rendered `static` and `semi-static` parts.

`PrefixHash` is internal. It is never logged, never returned to a tenant, and
never used as a cache key without tenant and credential scope. It is a plain
SHA-256, not an HMAC; an identity that crosses a trust boundary is hashed
again with the tenant and the credential.

The rolling message marker is deliberately not part of `PrefixHash`: it moves
forward each turn, so its prefix grows, while the stable part must stay equal.
The tool set is part of the stable prefix: a tool set that changes between
turns changes the hash, which is expected and is why REQ-AGENT-031's equality
is qualified by an unchanged tool set.

### 2. The three breakpoints and the provider adapters

For the providers that expose per-block breakpoints, the prefix order is
tools, then system, then messages. The three breakpoints are cumulative:

| Hint | Breakpoint | What it caches |
| --- | --- | --- |
| `System` | last non-empty system block | tools + system |
| `Tools` | last non-empty tool definition | tools |
| `Message` | last non-empty content block of the last non-empty message | tools + system + messages |

Each provider adapter maps the hint to its own wire form, or ignores it:

| Provider family | What the adapter does |
| --- | --- |
| Anthropic-compatible | Marks the last non-empty system block, the last non-empty tool definition and the last non-empty content block of the last non-empty message with `cache_control: {"type":"ephemeral"}`. Up to three breakpoints; a missing or empty anchor is skipped. |
| OpenAI-compatible (OpenAI, DeepSeek, Qwen, MiniMax, GLM, OpenRouter) | Relies on the provider's automatic prefix cache and ignores the hint; an adapter with an explicit cache API may use the stable prefix. |
| Ark/Doubao | OpenAI-compatible automatic prefix cache; ignores the hint. |
| Qianfan | Automatic prefix cache where the vendor offers one; ignores the hint. |
| Ollama and local providers | Automatic prefix cache; ignores the hint. |
| Workers | The worker start request carries the same additive cache hint; the worker's adapter maps it as its provider does. |
| Remote invokers | The invocation contract's additive `StartRequest.cache` carries the hint; the remote service maps or ignores it. |
| Generic HTTP/CLI providers | Ignores the hint unless the manifest declares an annotation mapping. |
| Embeddings | Never marked; an embedding request is one batch. |

Every provider not listed here ignores the hint. Gemini explicit cached
content is out of scope (see Non-goals); until its own ADR lands, the Gemini
adapter ignores the hint.

The exact field is the adapter's. `model/chat` stays vendor-neutral.

### 3. Where the annotation is set

The annotation is set in the **provider adapter**, under a new
`model/providers` package (WP-L3-04). `model/chat.Config` gains an optional
hook:

```go
// CacheAnnotator places a request's cache hint in the provider's wire form.
// It is a pure function of its arguments: no clock, no turn counter, no
// mutable policy.
type CacheAnnotator func(messages []*schema.Message, tools []*schema.ToolInfo, hint model.CacheHint) ([]*schema.Message, []*schema.ToolInfo, error)
```

`model/chat.Start` decodes messages and tools, calls the annotator when the
hint is non-zero, and then executes. **If the annotator returns an error,
`Start` ignores it and proceeds with the decoded messages and tools unchanged;
the error carries neither message text nor a provider handle.** A failed
annotation is indistinguishable from no annotation, and no invocation fails
because of a hint. The provider composition root supplies the annotator and
the eino model wrapper that emits the vendor fields.

The composition root is **`model/providers`**, not `cmd/kit`:

- It is the only package that knows vendor SDKs and descriptors; `cmd/kit` is
  the port opener (`cmd/kit/serve.go:33-40`, `wire`).
- It is testable and shared by `cmd/kit`, the BDD harness and the worker.
- It sits under the existing `model` layer row (`layering.yaml:16`), so no new
  layer and no `layering.yaml` change: the `model` row already grants `core`,
  `platform` and `expr`, and eino is not denied. It may not import `runtime`
  (not granted) or `agent` (a cycle).
- `cmd/kit` and `internal/bdd` already import `model` (`layering.yaml:28`,
  `layering.yaml:31`), so they can import `model/providers`.

The hint crosses the network in an additive `StartRequest.cache` field
(`contracts/basamento/invoke/v1/invoke.proto`), and the worker contract
carries the same field when WP-L3-07 defines it. Both are additive, so
`buf breaking` passes. A remote invoker or worker that cannot express a
breakpoint ignores it; no call fails.

`agent.Options.Models` is the resolver this composition root returns
(`agent/handler.go:53-56`).

### 4. Who sets the hint

- **Agent loop.** `agent` sets the hint while building the request
  (`agent/handler.go:156-173`): `System` when it has a non-empty system
  message, `Tools` when it has a non-empty tool definition, and `Message`
  always (the rolling marker). It sets no vendor syntax. The request, hint
  included, is journaled in `turnInput` (`agent/handler.go:308-312`). The
  agent's system messages are trusted flow text (`Spec.System`); tool results
  and user input are non-system messages, so the system breakpoint never
  covers them.
  **Ordering:** `System` and `Message` are enabled only in the same change as
  the cache-write contract (§7). The `Tools` breakpoint is the only one an
  adapter may set before then.
- **Prompt callers.** `prompt.Assembled.CacheAfter`
  (`prompt/registry.go:370-386`) is the prompt-level form of the system
  breakpoint. A caller that hands an assembled prompt to the model layer sets
  `CacheHint.System` only under the mapping precondition in §1, and sets
  `Tools` and `Message` from the request it builds. The prompt package does not
  import `model`.
- **Model node.** The `model` node leaves the hint zero: a model node spec has
  no cache policy. A later spec can add one additively.
- **Trusted code only.** The hint is set by trusted flow code (`agent`, a
  prompt caller, the model node), never from a client request; the API edge
  strips a client-supplied cache field. The rolling marker may sit on a tool
  result, which is content the model reads anyway; the request is bounded by
  the agent's input token limit (REQ-AGENT-027) and the provider's limits.
- **Handoff.** A handoff starts the target agent with a new system prefix; the
  target's first turn is a new stable prefix. That is expected, not a
  REQ-AGENT-031 violation.

### 5. Replay stability

- **Journaled:** the whole `model.Request` in the turn step input
  (`agent/handler.go:308-312`): model, messages, tools (the definitions
  themselves, not just their names), cache hint and client ref. The adapter
  never re-resolves tools from live state. The hint adds three booleans and no
  payload.
- **Derived:** the vendor wire annotation, from the journaled request and the
  hint only. The rolling marker's position is the last message block of that
  request; it is not a turn counter and is not read from `dc.Now()` or any
  live state.
- **Not used:** a per-run marker counter, a wall clock, the live `turns` slice
  at adapter time, or a recomputation from the provider's view of the
  conversation.
- A completed step returns its recorded result without calling the adapter
  (`agent/handler.go:208-209`, `runStep`); a retried step gets the same
  recorded input. A replay of the same turn therefore cannot produce a
  different durable request or a different annotation.
- The memory policy can drop whole turns (`agent/memory.go:19-109`). A drop
  changes the message prefix and the rolling marker misses, which is allowed;
  the stable system+tools prefix is unchanged and hits while the provider's
  cache entry lives. A provider TTL that expires the entry costs a new write,
  not a different prefix.
- **Tool changes.** A tool set that changes between turns changes the stable
  prefix; REQ-AGENT-031's equality holds only while the tool set is unchanged.

### 6. What asserts prefix stability

**Agents owns it.** The prompt capability already asserts assembly-level
stability: REQ-PROMPT-012 and REQ-PROMPT-013
(`specs/prompt/requirements.md:59-63`) and PROMPT-012-01/02,
PROMPT-013-01/02 (`specs/prompt/segments.feature:98-143`). That covers two
assemblies of one template; it does not cover two consecutive agent turns.

Proposed requirement, using the next free number
(`specs/agents/requirements.md:17-31`; 006, 013-015, 021, 022 and 026 are
retired):

> **REQ-AGENT-031 — Consecutive turns share the cacheable prefix**
> When the agent loop builds the request for a model turn after the first, it
> shall set the request's cache hint to mark the last non-empty system message
> when the request has one, the last non-empty tool definition when it has one,
> and the last message, and it shall build the request so that the previous
> turn's messages are a byte prefix of its own messages when the memory policy
> drops no turn and the tool set is unchanged; the stable prefix hash
> (`model.PrefixHash`) of the two requests shall be equal.

Proposed scenarios for caster to draft in `specs/agents/cache.feature`, not
this change:

- `@REQ-AGENT-031 @id:AGENT-031-01`, "Two consecutive turns share the
  cacheable prefix". An agent with two system messages and two tools makes a
  first turn that calls a tool and a second turn after the tool result (so the
  last message is the tool result); the scenario asserts both requests mark
  all three breakpoints, `model.PrefixHash` is equal for the two requests, and
  the first request's messages are a byte prefix of the second's.
- `@REQ-AGENT-031 @id:AGENT-031-02`, "A different tool set changes the stable
  prefix hash". The same agent with a different tool set between turns
  produces a different `model.PrefixHash`; the previous messages are still a
  byte prefix, but the stable prefix is not equal, so the cache cannot hit and
  REQ-AGENT-031's equality does not apply.

**Models owns the model-layer half.** Proposed requirements, using the next
free numbers (REQ-MODEL-089 onward):

> **REQ-MODEL-089 — A cache hint is advisory**
> When a caller starts an invocation whose request carries a cache hint, the
> model layer shall pass the hint to the provider adapter unchanged; an
> adapter that cannot express a breakpoint shall ignore it, and no invocation
> shall fail because of a hint.

> **REQ-MODEL-090 — The stable prefix hash**
> The model layer shall compute the stable prefix hash as the SHA-256 of the
> request's tool definitions in request order followed by its system messages
> in request order, order-sensitive, and shall return an empty hash when the
> request has neither; the hash is internal and is never returned to a tenant.

> **REQ-MODEL-091 — Cache writes are counted and priced**
> When a provider reports cache reads and cache creations, the adapter shall
> set `Usage.InputTokens` to the sum of input, cache-read and cache-creation
> tokens, `Usage.CachedTokens` to the cache-read tokens and
> `Usage.CacheWriteTokens` to the cache-creation tokens; the default token
> formula shall price the non-cached, non-write input at the input rate,
> cached tokens at `rates.cached` when the rule defines it and the input rate
> otherwise, cache-write tokens at `rates.cache_write` when the rule defines it
> and the input rate otherwise, and output tokens at the output rate; the
> invocation's `CostMpts` shall include the cache-write premium.

> **REQ-MODEL-092 — The reservation covers cache writes**
> When the price table estimates the cost of a call whose request carries a
> cache hint, the estimate shall include the cache-write premium by counting
> the call's counted input tokens as cache-write tokens; and if a call's usage
> reports more cached and cache-write tokens together than input tokens, then
> the price table shall fail pricing the call with code `invalid_argument`.

Proposed scenarios for caster:

- `@REQ-MODEL-089 @id:MODEL-089-01`, "The system breakpoint follows the last
  semi-static part". A prompt caller builds a request from an assembled prompt
  whose static and semi-static parts are system messages and whose dynamic,
  history and user parts are not; the request sets `CacheHint.System`, and the
  last system message ends at `cache_after`.
- `@REQ-MODEL-089 @id:MODEL-089-02`, "A boundary that cannot be honoured sets
  no system breakpoint". When a dynamic part would follow `cache_after` inside
  a system message, the caller sets no system breakpoint and the invocation
  still succeeds unannotated.
- `@REQ-MODEL-089 @id:MODEL-089-03`, "An adapter without a breakpoint ignores
  the hint". A provider that cannot express a breakpoint starts and ends
  successfully with a non-zero hint.
- `@REQ-MODEL-089 @id:MODEL-089-04`, "The hint crosses the invocation
  contract". A request with a cache hint sent to a remote invoker carries the
  same hint in `StartRequest.cache`; the invoker maps or ignores it and the
  invocation succeeds either way.
- `@REQ-MODEL-090 @id:MODEL-090-01`, "The prefix hash is order-sensitive over
  tools then system messages". Two requests with the same tools in a different
  order, or the same system messages in a different order, have different
  hashes; two requests with the same tools and system messages and different
  dynamic messages have the same hash.
- `@REQ-MODEL-091 @id:MODEL-091-01`, "Cache writes are counted and priced". A
  provider reports 7000 input tokens, 4000 cache reads and 2000 cache
  creations; the usage carries 7000 input, 4000 cached and 2000 cache-write
  tokens; with rates input 1000, cached 500, cache_write 1250 and output 2000
  per million and 500 output tokens, the call costs 7 milli-points.
- `@REQ-MODEL-092 @id:MODEL-092-01`, "The reservation covers the cache-write
  premium". A request with a cache hint and 7000 counted input tokens reserves
  the write rate for those tokens, so a call that writes the whole prefix does
  not exceed its reservation.
- `@REQ-MODEL-092 @id:MODEL-092-02`, "More cached and cache-write tokens than
  input fails pricing". Usage with 4000 cached and 2000 cache-write tokens but
  5000 input tokens fails with `invalid_argument`.

No `REQ-PROMPT` change is needed: `cache_after` stays the prompt-level
boundary, and the mapping constraint is stated in `specs/prompt/design.md` and
proved by MODEL-089-01/02.

### 7. Cache writes

This section decides the contract that must land before `System` and
`Message` are enabled.

- `model.Usage` gains `CacheWriteTokens int64` (additive JSON
  `cache_write_tokens,omitempty`). `InputTokens` becomes the total input:
  input + cache reads + cache creations. `CachedTokens` stays the cache-read
  part and `CacheWriteTokens` the cache-creation part, both inside
  `InputTokens`.
- A rate rule gains `cache_write` beside `input`, `output` and `cached`. A rule
  that omits it prices cache-write tokens at the input rate, as it does for
  cached tokens; `cache_write: 0` is a rate.
- The default formula is
  `(I − C − W) × input + C × cached + W × cache_write + O × output`, where
  `I`, `C`, `W` and `O` are input, cached, cache-write and output tokens. The
  `C > I` check becomes `C + W > I`.
- The reservation estimate includes the write premium: for a request with a
  cache hint, the estimate counts the call's counted input tokens as
  cache-write tokens, so a full write is reserved. The actual usage settles
  it, and `Result.CostMpts` includes it. This also fixes the agent's token
  cap seeing `input_tokens: 0` for a fully cached request: `InputTokens` now
  includes the cache reads and writes.
- This supersedes the "Cache writes are not priced" note in
  `specs/models/design.md` C3; the spec change that adds REQ-MODEL-091/092
  updates that note. REQ-MODEL-075 and REQ-MODEL-082 remain true for `W = 0`;
  the spec change adds the `W` term to the default formula and the combined
  `C + W > I` check.

## Non-goals

- **Gemini explicit cached content.** `CachedContent` is a server-side,
  model-scoped resource with a TTL and a handle, not an annotation: the
  annotator has no model id, cannot return a handle, and a retry would create
  a second cache. It gets its own ADR (not yet numbered) covering lifecycle, tenant and
  credential scope, model scope, degrade-to-unannotated on any failure, and
  stripping the cached prefix from `contents`. Until then the Gemini adapter
  ignores the hint.
- **Encrypting the journaled request.** `turnInput` journals the whole
  `model.Request` while constitution §7 requires history payloads to be
  encrypted. This ADR adds no payload — the hint is three booleans — and does
  not change journaling; a separate ticket tracks encryption.
- **An agent with no system message.** An agent that sends no system message
  sets no `System` breakpoint and the adapter sees no system anchor. The
  no-system request path is WP-L7-01's; this ADR does not change it.
- **Cache isolation on the shared pool.** Provider caches are
  credential-scoped, and a shared key can make `Usage.CachedTokens` a
  cross-tenant confirmation oracle. This ADR adds no cache key and
  `PrefixHash` is internal; a tenant-scoped cache namespace or disabling
  caching on the shared pool belongs to the credential and pool design
  (ADR-0012) and the Gemini ADR.
- **A cache-write price for vendors that do not charge one.** The contract
  prices writes at `rates.cache_write`; a vendor with no write premium sets
  `cache_write: 0`. No vendor-specific pricing model is added here.

## Alternatives considered

- **Keep the single prompt hint and no model-layer hint.** Cost: the tools and
  the rolling message prefix are never cached, a long agent conversation pays
  full input every turn, and the hint stays computed and discarded. This is
  the status quo EP0-185 verifies.
- **Let each provider adapter choose the breakpoints from the messages
  alone.** Cost: the policy is duplicated per adapter, the agent cannot assert
  it, and two adapters can cache different prefixes for the same run. The hint
  belongs in the contract; the mapping belongs in the adapter.
- **One `CacheAfter` index instead of three breakpoints.** Cost: the request
  cannot express system, tools and messages at once; a provider that allows
  several breakpoints loses two of the three hits.
- **Map `cache_after` to the last system message without a precondition.**
  Cost: a prompt that puts a dynamic, history or user part in a system message
  would cache untrusted content, bypassing REQ-PROMPT-017 and PROMPT-012-01.
  Require the mapping precondition and set no system breakpoint when it fails.
- **Sort tools and system messages before hashing.** Cost: a different wire
  order would hash equal and the provider would miss; the hash must be
  order-sensitive.
- **Journal the vendor wire annotation in the request.** Cost: the durable
  request carries `cache_control` JSON, so a model or adapter change breaks
  replay and vendor syntax leaks into the model layer. Journal the neutral
  hint; derive the annotation.
- **Derive the rolling marker from a turn counter or the clock.** Cost: a
  resumed run can produce a different marker and a different request, which is
  non-deterministic and against the constitution. The marker is the last
  message block of the journaled request.
- **Put the composition root in `cmd/kit`.** Cost: model construction cannot
  be tested without the binary, and the BDD harness and the worker duplicate
  it. Use `model/providers`.
- **Add a `model/providers` layer row.** Cost: a layering change and an ADR
  for a grant the `model` row already gives. `model/providers` is a
  subpackage of `model`.
- **Reuse `prompt.PrefixHash` for the agent.** Cost: it hashes only
  static/semi-static prompt parts (`prompt/hash.go:36-48`), not tools, and the
  agent has no prompt assembly (`agent/handler.go:3-18`). Add
  `model.PrefixHash`.
- **Let workers and remote invokers drop the hint.** Cost: a routed model
  silently loses caching while REQ-AGENT-031 still passes locally. Carry the
  hint in additive `StartRequest.cache`.
- **Enable all three breakpoints before pricing cache writes.** Cost: the
  write premium is invisible to the reservation and the cost cap; a tenant
  bill can exceed the cap. Price writes first, and keep `System` and `Message`
  off until then.
- **Skip the `Message` marker when the last message is a tool result.** Cost:
  the tool result is the largest and most reused part of the next turn, so
  skipping it loses the main hit; the content is in the request regardless,
  and the request is bounded by the input token limit. Keep the marker.
- **Mark the rolling message even when the memory policy drops turns.**
  Cost: the marker is still correct (it is the last message), but the previous
  cache cannot hit. Kept as a consequence, not a failure: the stable prefix is
  unchanged and hits while the provider's cache entry lives.

## Consequences

- This change is docs-only: the ADR, the two design documents, the glossary
  and the ADR index. The wire annotation, `model.PrefixHash`, the cache-write
  contract and the agent hint land in WP-L3-04, WP-L7-01 and the spec changes
  listed below.
- `model/chat` gains one optional hook; providers without cache control
  ignore it. No invocation fails because of a hint, and an annotator error is
  ignored with no text or handle.
- The stable system+tools prefix is cacheable on every turn; the rolling
  message prefix is best-effort and is invalidated by a memory-policy drop.
  The stable prefix is unchanged and hits while the provider's cache entry
  lives; a provider TTL expiry costs a new write, not a different prefix.
- Cache writes are counted and priced in the same change that enables
  `System` and `Message`; until then `Tools` is the only breakpoint an adapter
  may set. A vendor that charges no write premium sets `cache_write: 0`.
- `StartRequest.cache` is additive in the invocation contract and the worker
  contract; `buf breaking` passes.
- `docs/architecture/model-layer.md` gains the hint, the prefix hash, the
  provider mapping, the transport boundary and the cache-write price;
  `specs/prompt/design.md` states the `cache_after` mapping.
- `specs/agents` gains REQ-AGENT-031 and `cache.feature`; `specs/models`
  gains REQ-MODEL-089 to REQ-MODEL-092 and their scenarios.
  `specs/glossary.md` splits the prompt and model cache hints.

## Revert

Revert the merge commit. Remove the ADR-0019 row from `docs/adr/README.md`,
the cache-hint text from `docs/architecture/model-layer.md`, the `cache_after`
mapping note from `specs/prompt/design.md`, and the split cache-hint rows from
`specs/glossary.md`. No code, ledger or layering change is part of this ADR.

## Implementation changes these files

| Path | Change | Owner |
| --- | --- | --- |
| `docs/adr/README.md` | Row for ADR-0019. | this change |
| `docs/architecture/model-layer.md` | §1 hint, mapping and `model.PrefixHash`; §3 provider mapping and transport; §5 annotator and replay; §7 cache-write price. | this change |
| `specs/prompt/design.md` | Intro and §4: the `cache_after` mapping; the vendor mapping is ADR-0019's. | this change |
| `specs/glossary.md` | Split the prompt and model cache hints and the two prefix hashes. | this change |
| `model/model.go` | `CacheHint`, `Request.Cache`, `PrefixHash`, `Usage.CacheWriteTokens`. | WP-L3-01 (done); changed by WP-L3-04 |
| `model/chat/chat.go` | `CacheAnnotator`; ignore an annotator error. | WP-L3-01 (done); changed by WP-L3-04 |
| `model/providers/` | Provider composition root and per-vendor annotators. | WP-L3-04 |
| `model/catalog/price.go` | `rates.cache_write`, the default formula and the reservation estimate. | WP-L3-06 (done); changed by WP-L3-04 |
| `contracts/basamento/invoke/v1/invoke.proto` | Additive `StartRequest.cache`. | WP-L3-01 (done); changed by WP-L3-04 |
| `contracts/basamento/worker` | Additive cache hint in the worker start request. | WP-L3-07 |
| `agent/handler.go` | Set and journal the hint; include cache writes in the reservation. | WP-L7-01 |
| `specs/models/requirements.md` | REQ-MODEL-089 to REQ-MODEL-092. | caster, after this ADR |
| `specs/models/contract.feature` | MODEL-089-01..04 and MODEL-090-01. | caster |
| `specs/models/cost.feature` | MODEL-091-01 and MODEL-092-01..02. | caster |
| `specs/models/design.md` | Replace the "Cache writes are not priced" note with §7's contract. | caster |
| `specs/agents/requirements.md` | REQ-AGENT-031. | caster |
| `specs/agents/cache.feature` | AGENT-031-01 and AGENT-031-02. | caster |

Rows outside WP-L3-04's owned directory (`model`, `model/chat`,
`model/catalog`, `contracts/basamento/invoke`) are cross-directory changes
under AGENTS.md: the pull request says so and a reviewer from the owning area
reviews it. WP-L3-01 and WP-L3-06 are done; their directories are changed by
WP-L3-04's pull request, not reopened as work packages. The agent side is
WP-L7-01, which owns `agent`.
