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

The agent turn already journals the whole `model.Request` in its durable step
input (`agent/handler.go:308-312`). That is the right place to pin a cache
hint: a replay reads the recorded request rather than rebuilding a marker from
live state.

## Decision

### 1. A neutral cache hint on the request

`model.Request` gains an optional, vendor-neutral hint, `Cache *CacheHint`:

```go
// CacheHint marks up to three cache breakpoints in a request: the last system
// message, the last tool definition, and the last message. A nil hint asks
// for none.
type CacheHint struct {
	System  bool `json:"system,omitempty"`
	Tools   bool `json:"tools,omitempty"`
	Message bool `json:"message,omitempty"`
}
```

It is advisory: a provider that cannot express a breakpoint ignores it, and a
hint never fails an invocation. It carries no `cache_control` JSON, no TTL and
no provider name.

`model.PrefixHash(r Request) string` returns the SHA-256 of the stable
cacheable prefix — the tool definitions and the system messages, in canonical
order — and is empty when the request has neither. It is the model-layer
counterpart of `prompt.PrefixHash` (`prompt/hash.go:36-48`). The rolling
message marker is deliberately not part of `PrefixHash`: it moves forward
each turn, so its prefix grows, while the stable part must stay equal.

### 2. The three breakpoints and the provider adapters

For the providers that expose per-block breakpoints, the prefix order is
tools, then system, then messages. The three breakpoints are cumulative:

| Hint | Breakpoint | What it caches |
| --- | --- | --- |
| `System` | last system block | tools + system |
| `Tools` | last tool definition | tools |
| `Message` | last content block of the last non-empty message | tools + system + messages |

Each provider adapter maps the hint to its own wire form, or ignores it:

| Provider family | What the adapter does |
| --- | --- |
| Anthropic-compatible | Marks the last system block, the last tool definition and the last content block of the last non-empty message with `cache_control: {"type":"ephemeral"}`. Up to three breakpoints; a missing anchor is skipped. |
| OpenAI-compatible (OpenAI, DeepSeek, Qwen, MiniMax, GLM, OpenRouter) | Relies on the provider's automatic prefix cache and ignores the hint; an adapter with an explicit cache API may use the stable prefix. |
| Gemini | Uses the stable system+tools prefix as explicit cached content where the model supports it; ignores the rolling message marker, which has no per-block equivalent. |
| Ollama and local providers | Automatic prefix cache; ignores the hint. |
| Generic HTTP/CLI providers | Ignores the hint unless the manifest declares an annotation mapping. |
| Embeddings | Never marked; an embedding request is one batch. |

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
hint is non-zero, and then executes. The provider composition root supplies
the annotator and the eino model wrapper that emits the vendor fields. A
provider that cannot express a breakpoint leaves it out; it does not fail.

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

`agent.Options.Models` is the resolver this composition root returns
(`agent/handler.go:53-56`).

### 4. Who sets the hint

- **Agent loop.** `agent` sets the hint while building the request
  (`agent/handler.go:156-173`): `System` when it has system messages, `Tools`
  when it has tools, and `Message` always (the rolling marker). It sets no
  vendor syntax. The request, hint included, is journaled in `turnInput`
  (`agent/handler.go:308-312`).
- **Prompt callers.** `prompt.Assembled.CacheAfter`
  (`prompt/registry.go:370-386`) is the prompt-level form of the system
  breakpoint. A caller that hands an assembled prompt to the model layer sets
  `CacheHint.System` when `CacheAfter` is non-empty, and sets `Tools` and
  `Message` from the request it builds. The prompt package does not import
  `model`.
- **Model node.** The `model` node leaves the hint zero: a model node spec has
  no cache policy. A later spec can add one additively.

### 5. Replay stability

- **Journaled:** the whole `model.Request` in the turn step input
  (`agent/handler.go:308-312`): model, messages, tools, cache hint and client
  ref. The hint is computed in flow code from the same messages and tools that
  are marshaled, so it is part of the durable request.
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
  the stable system+tools prefix is unaffected and still hits.

### 6. What asserts prefix stability

**Agents owns it.** The prompt capability already asserts assembly-level
stability: REQ-PROMPT-012 and REQ-PROMPT-013
(`specs/prompt/requirements.md:59-63`) and PROMPT-012-01/02,
PROMPT-013-01/02 (`specs/prompt/segments.feature:98-143`). That covers two
assemblies of one template; it does not cover two consecutive agent turns.

Proposed requirement, using the next free number
(`specs/agents/requirements.md:17-31`):

> **REQ-AGENT-031 — Consecutive turns share the cacheable prefix**
> When the agent loop builds the request for a model turn after the first, it
> shall set the request's cache hint to mark the last system message when the
> request has one, the last tool definition when it has one, and the last
> message, and it shall build the request so that the previous turn's messages
> are a byte prefix of its own messages when the memory policy drops no turn;
> the stable prefix hash of the two requests shall be equal.

Proposed scenario for caster to draft in `specs/agents/cache.feature`, not
this change: `@REQ-AGENT-031 @id:AGENT-031-01`, "Two consecutive turns share
the cacheable prefix". An agent with two system messages and two tools makes a
first turn that calls a tool and a second turn after the tool result; the
scenario asserts the second request marks all three breakpoints,
`model.PrefixHash` is equal for the two requests, and the first request's
messages are a byte prefix of the second's. No `REQ-PROMPT` or `REQ-MODEL`
change is needed.

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
- **Mark the rolling message even when the memory policy drops turns.**
  Cost: the marker is still correct (it is the last message), but the previous
  cache cannot hit. Kept as a consequence, not a failure: the stable prefix
  still hits.

## Consequences

- This change is docs-only: the ADR and the two design documents. The wire
  annotation lands with WP-L3-04's provider adapters; the agent hint and
  `model.PrefixHash` land with the agent follow-up that also adds
  REQ-AGENT-031.
- `model/chat` gains one optional hook; providers without cache control
  ignore it. No invocation fails because of a hint.
- The stable system+tools prefix is cacheable on every turn; the rolling
  message prefix is best-effort and is invalidated by a memory-policy drop.
- Cache writes remain unpriced: `model.Usage` has no cache-write field
  (`specs/models/design.md:288-294`). This ADR does not change pricing; a
  vendor that charges cache writes needs a separate additive contract change.
- `docs/architecture/model-layer.md` gains the hint, the provider mapping and
  the composition root; `specs/prompt/design.md` says `cache_after` maps to
  the system breakpoint.
- `specs/agents` gains REQ-AGENT-031 and `cache.feature`; `specs/models`
  needs no requirement change.

## Revert

Revert the merge commit. Remove the ADR-0019 row from `docs/adr/README.md`,
the cache-hint text from `docs/architecture/model-layer.md`, and the
`cache_after` mapping note from `specs/prompt/design.md`. No code, ledger or
layering change is part of this ADR.

## Implementation changes these files

| Path | Change |
| --- | --- |
| `docs/adr/README.md` | Row for ADR-0019. |
| `docs/architecture/model-layer.md` | §1 cache hint and `model.PrefixHash`; §3 provider mapping and `model/providers`; §5 annotator. |
| `specs/prompt/design.md` | Intro and §4: `cache_after` maps to `CacheHint.System`; the vendor mapping is ADR-0019's. |
| `model/model.go` | `CacheHint`, `Request.Cache`, `PrefixHash` (WP-L3-04 / agent follow-up). |
| `model/chat/chat.go` | Optional `CacheAnnotator` hook (WP-L3-04). |
| `model/providers/` | Provider composition root and per-vendor annotators (WP-L3-04). |
| `agent/handler.go` | Set the hint when building `model.Request` (agent follow-up). |
| `specs/agents/requirements.md` | REQ-AGENT-031 (caster, after this ADR). |
| `specs/agents/cache.feature` | AGENT-031-01 (caster, after this ADR). |
