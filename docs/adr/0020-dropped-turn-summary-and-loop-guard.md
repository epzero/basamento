# ADR-0020 — Dropped-turn summary and loop guard

**Status:** accepted · **Date:** 2026-11-22

## Context

The WP-L7-01 agent loop (`agent`) has two confirmed gaps. Both are in the code
as it stands, not in the design notes.

**Dropped turns vanish.** `buildMessages` (`agent/memory.go:19-108`) keeps the
system messages, the first model turn and the newest whole turns that fit
`input_tokens`; it drops the rest (`agent/memory.go:34-67`) and returns only a
count (`agent/memory.go:108`). The loop calls it at every boundary
(`agent/handler.go:156`), records the count in the turn step
(`agent/handler.go:208-209`, `agent/handler.go:317-323`) and keeps the complete
turns only in the run output (`agent/output.go:23-28`). A handoff carries those
complete turns (`agent/output.go:52-80`) and the target re-runs the same policy
with its own limit (`agent/handler.go:156`), so the target drops them again.
Nothing in the next prompt says what was dropped; a model that needed a fact
from a dropped turn cannot recover it.

**Repeated tool calls burn the caps.** The loop executes every requested
non-handoff tool call (`agent/handler.go:268-287`) and has no repetition
tracking. A model that repeats the same call with the same arguments pays for a
model turn and a tool step each time until `step_cap_reached`,
`token_cap_reached` or `cost_cap_reached`, and the model is never told that it
is repeating itself.

The reference implementation (`/Users/sin/work/agents/jenny`, outside this
repository) solves both: a durable compaction boundary with a summary between
pinned and tail messages (`internal/agent/compact.go:306-319`,
`internal/agent/compact.go:566-600`; `internal/agent/engine.go:505-529`), and a
loop guard with a soft marker at 3, a hard block at 6, a 100-entry LRU and
canonical arguments (`internal/agent/loop_guard.go:14-26`,
`internal/agent/loop_guard.go:56-92`, `internal/agent/loop_guard.go:202-233`).

## Decision

1. A **dropped-turn summary** is an opt-in part of the context-window policy
   (`input_summary: true`). When turns are dropped, the loop keeps a durable
   summary of them and puts one synthetic user message between the kept first
   turn and the kept recent tail.
2. The summary is produced by a **durable step** `summary-<from>-<to>` and is
   extended incrementally as the drop frontier advances; replay reads the
   recorded step, so it assembles the same prompt.
3. The summary call is a **metered model call**: its tokens count against
   `caps.tokens`, its charge against `caps.cost_mpts` and `Result.CostMpts`,
   and it is **not** a model turn for `caps.steps`. If it cannot be afforded it
   is skipped; a failure does not fail the run.
4. A handoff carries the active summary in a new optional `summary` field of
   the run output. The target reuses it when its own dropped range is the same
   or larger, and starts fresh when the carried summary would cover turns it
   keeps.
5. A **loop guard** always runs on non-handoff tool calls: identical canonical
   calls get a soft warning at the 3rd request and a hard block at the 6th. A
   hard block still journals its `tool-<n>-<k>` step but does not run the
   tool; the step returns a synthetic tool result the model sees, and the run
   continues.
6. The guard has **no durable counters**. Its state is rebuilt from the turn
   history the loop already assembles (`Turn.Message.ToolCalls` and
   `Turn.Results`, `agent/handler.go:239-243`, `agent/handler.go:286`); the
   `turnResult` JSON (`agent/handler.go:317-323`, asserted at
   `agent/handler_test.go:700-721`) is unchanged.
7. WP-L7-01's implemented scenarios stay untouched: the summary is opt-in, and
   the guard's thresholds are not reached by an existing scenario in a way
   that changes its assertions.

## 1. Dropped-turn summary

### 1.1 Placement and shape

When `input_summary` is true, `input_tokens > 0` and at least one turn is
dropped, the messages of model turn `n` are, in order:

1. the agent's system messages;
2. the kept first model turn and the tool results that followed it;
3. **one summary message** of the dropped turns;
4. the kept recent turns and their tool results.

The summary message is a synthetic user message:

```json
{"role": "user", "content": "<conversation-summary>\n…summary text…\n</conversation-summary>"}
```

It carries no `Name`, is not a `Turn`, and is not added to `Output.Turns`.
`Output.Turns` remains the complete history. The summary sits exactly where
the dropped turns were: after turn 1 and before the first kept tail turn; when
no tail turn fits, it is last.

The summary is never used to replace turn 1. The current policy always keeps
turn 1 when it drops anything (`agent/memory.go:44-54`), and the summary
covers turns `2..frontier`.

### 1.2 The active summary and incremental extension

The active summary is `Summary{Text, From: 2, To: frontier}`, where `frontier`
is the last dropped turn. It is not a new `Turn` field and not a new
`turnResult` field; it is the result of a summary step and an optional field of
the run output.

At a boundary:

1. Select the tail and the frontier with the current active summary's measured
   weight reserved (initially no summary, so the pre-change policy).
2. If turns are dropped and the active summary's `To` is below the frontier,
   run one or more summary steps covering `active.To+1..frontier`, each with
   the previous summary and the raw turns of its chunk. The new active summary
   covers `2..frontier`.
3. Recompute the tail with the new summary's measured weight. If the frontier
   advances, extend again. Repeat to a fixed point. Each extension is one
   durable step; the loop is bounded by the number of turns.
4. If the active summary's `To` is above the frontier (only possible after a
   handoff to an agent with a larger limit), discard it and, if turns are still
   dropped, start a fresh summary from turn 2.

The fixed point is deterministic: the same turn history and the same journaled
summary steps produce the same frontier, the same summary text and the same
messages. Re-summarising the whole dropped prefix from raw turns at every
boundary is rejected in §3: it costs a call proportional to the prefix each
time and the text drifts as the tail moves.

### 1.3 Step key, input and result

The step name is:

```text
<node>/<runID>/summary-<from>-<to>
```

with `IdempotencyKey` equal to the name, matching the turn and tool steps
(`agent/handler.go:208`, `agent/handler.go:269`). `from` and `to` are 1-based
turn numbers. The input is:

```json
{"model": "…", "request": { …model.Request… }, "from": 3, "to": 5,
 "previous": {"text": "…", "from": 2, "to": 2}}
```

`request` is a chat request (`model.CapChat`) whose messages are:

1. a system message with the summariser instructions;
2. when `previous` is present, a user message
   `<previous-summary>…</previous-summary>`;
3. the messages of turns `from..to` in order, rendered exactly as
   `buildMessages` renders a kept turn: the assistant message followed by its
   tool-result messages.

The request offers no tools. It asks for at most
`min(2048, max(256, input_tokens/8))` output tokens through the chat adapter's
`max_tokens` parameter, and the summary is measured after it returns. Its
`ClientRef` is `sha256(runID \x00 node \x00 "summary-<from>-<to>")` hex, the
same construction as `clientRef` (`agent/handler.go:539-546`) with the summary
kind in place of `turn-<n>`, so a retried step does not start a second vendor
invocation (REQ-MODEL-004).

The result is:

```json
{"type": "summary.completed",
 "summary": {"text": "…", "from": 2, "to": 5},
 "usage": {"input_tokens": 900, "output_tokens": 120},
 "cost_mpts": 40}
```

On a model error, a non-succeeded state, an empty message, or a request that
would exceed `input_tokens`, the step returns a **successful step result** of
type `summary.failed`:

```json
{"type": "summary.failed", "summary": null,
 "usage": {"input_tokens": 900, "output_tokens": 0},
 "cost_mpts": 40, "failed": true, "error": "…"}
```

The step function encodes the model failure rather than returning it, so the
run continues and replay does not retry the same range. A later frontier has a
new key and may succeed. A durable-runtime failure of the step itself is not
caught; it fails the run like any other step.

### 1.4 Token budget

The summary message's weight is estimated from its content at four bytes per
token, rounded up, plus one token for the envelope — the same estimate used for
a system message that declares no count (`agent/memory.go:111-119`). It is part
of the kept set:

```text
T_sys + T_1 + T_summary + Σ T_tail <= input_tokens
```

The tail is selected from `input_tokens - T_sys - T_1 - T_summary`, and the
fixed point in §1.2 recomputes it when the summary grows. If
`T_sys + T_1 + T_summary > input_tokens`, the summary is not used and the loop
falls back to the pre-change tail; if `T_sys + T_1 > input_tokens`, the
existing REQ-AGENT-027 failure applies. The post-build measurement
(`agent/memory.go:91-107`) includes the summary message.

The summary request is checked before it runs: if its estimated input tokens
exceed `input_tokens`, the loop records `summary.failed` with
`error: "input too large"` and makes no model call. A single huge dropped turn
therefore does not break the prompt; it stays unsummarised.

### 1.5 Cap accounting

The summary call is reserved and settled like a turn:

- before the call, `Estimate` is called on its request (`agent/handler.go:174-180`);
- if `used + estimate.InputTokens > caps.tokens` or
  `spent + estimate.CostMpts > caps.cost_mpts`, the summary is skipped: no
  step is created and no model call is made, and the turn's own checks follow.
  Replay recomputes the same skip from the journaled usage and charge;
- when it runs, `used += usage.InputTokens + usage.OutputTokens` and
  `spent += cost_mpts`; `*settled = spent`, so `Result.CostMpts` and a flow's
  cost guard (`agent/handler.go:89-96`) see the summary's charge.

The summary call does **not** increment `own`, does not appear in
`Output.Turns`, and does not count against `caps.steps`, which is defined as
model turns. The time cap is checked before the summary and again before the
turn; a summary that runs past the time cap is paid for, but the turn does not
start and the run ends with `time_cap_reached`.

`Observer.Dropped` keeps its meaning (the number of original messages dropped).
No `Observer` method is added for the summary; its cost and usage are in the
summary step result and `Result.CostMpts`.

### 1.6 Failure

A summary failure is best-effort: the run continues without a summary message
for that range, exactly as today's policy would have continued. The failure is
recorded in the `summary.failed` step result, so replay is stable and the
reason is visible. The model middleware's own retry policy may retry the
invocation inside the step; the durable step records the final outcome. A
failure is not retried for the same `from..to` range, because the step name
would replay the recorded failure; a later frontier is a new step and a new
attempt.

### 1.7 Handoff

`Output` gains one optional field:

```go
Summary *Summary `json:"summary,omitempty"`
```

`Summary` is `{Text string, From int, To int}`. `Turn` is unchanged.
`handoff` (`agent/output.go:72-80`) and `finish` (`agent/handler.go:291-293`)
carry the active summary; a completed output may carry it too, for
observability. `parseHandoff` (`agent/output.go:52-69`) reads it into the
carried state.

The target uses it as follows:

| Target's dropped range | Action |
| --- | --- |
| no drops | ignore the carried summary; keep all carried turns |
| `2..frontier`, same `To` | use the carried summary as-is, no call |
| `2..frontier`, larger `To` | use the carried summary as `previous` and extend it from `To+1` to `frontier` |
| `2..frontier`, smaller `To` | discard the carried summary; if turns are still dropped, start fresh from turn 2 |

The target must also have `input_summary: true`; otherwise it ignores the
carried summary. The carried turns are complete either way, so ignoring the
summary loses no raw history. The target's summary steps use its own
`node/runID` prefix, and its summary cost is charged to the target's caps, not
the first agent's.

### 1.8 What the summary contains

The summariser prompt asks for the goal and constraints, decisions and their
reasons, files, identifiers and commands touched, errors and fixes, and what
remains to be done. It forbids tool calls and asks for one plain-text updated
summary. The previous summary and the newly dropped turns are both input; the
output is one updated summary, not a concatenation of chunk summaries. The
summary text is opaque to the loop: it is never parsed or matched.

### 1.9 Opt-in

`input_summary` is an agent spec field, default `false`:

| Field | Meaning | Default |
| --- | --- | --- |
| `input_summary` | summarise the model turns the context-window policy drops (ADR-0020) | `false` |

The default keeps WP-L7-01's behavior and its scenarios untouched. A deployment
that wants the summary turns it on in the agent spec; the follow-up work
package's scenarios do so.

## 2. Loop guard

### 2.1 Identity and canonicalisation

The guard applies to every non-handoff tool call. `splitCalls`
(`agent/handler.go:245`) already separates `handoff_to`; the reserved handoff
tool is never guarded.

The key is:

```text
sha256(toolName + "\x00" + canonicalArgs)
```

in lowercase hex. `canonicalArgs` is built from `Function.Arguments`:

1. parse it as JSON;
2. when it parses, recursively sort object keys, collapse each string value's
   whitespace runs to one space and trim
   (`strings.Join(strings.Fields(s), " ")`), preserve array order, and leave
   numbers, booleans and null alone; marshal compactly;
3. when it does not parse, use the raw argument string as-is;
4. empty arguments, `null` and `{}` all canonicalise to `{}`.

The tool name is compared exactly, case-sensitively. String values are not
lowercased: paths and commands are case-sensitive. The canonical form ignores
object-key order and insignificant whitespace, so `{"a":1,"b":2}` and
`{"b":2,"a":1}` are the same call, while `{"path":"A"}` and `{"path":"a"}` are
not. Collapsing whitespace inside a string value can merge two calls whose
arguments differ only in whitespace; that is the intended trade-off, because a
model that re-emits a call with cosmetic whitespace changes is repeating
itself. The soft threshold and the model's ability to change the call bound the
cost of a false positive.

### 2.2 Thresholds and LRU

The guard holds at most **100** entries, keyed by the hash. Each entry has a
count; an LRU list orders keys by last observation. `observe(key)` increments
the count and moves the key to the most-recent end. A new key when 100 entries
exist evicts the least-recently-used key and forgets its count.

The decision uses the post-increment count:

| Count | Action |
| ---: | --- |
| `< 3` | execute silently |
| `3..5` | execute, append the soft marker to the tool result |
| `>= 6` | do not execute; return the synthetic tool result |

The thresholds are fixed. No agent spec field enables, disables or tunes them.

### 2.3 What the model sees

**Soft marker.** The tool runs normally. The tool-result message content is the
tool's text followed by:

```text
\n\n<loop-guard>\ntool "<name>" with identical arguments has been requested <count> times; repeating it is unlikely to help. Change the arguments or the approach.\n</loop-guard>
```

The marker is part of the durable `Turn.Results` entry. On replay the loop
recomputes the same count and appends the same marker.

**Hard block.** The tool does not run. The `tool-<n>-<k>` step still exists, so
REQ-AGENT-003 (one tool-call step per requested tool) is unchanged. Its input
carries the decision:

```json
{"agent": "…", "id": "…", "call": { … },
 "blocked": true, "count": 6}
```

`runTool` returns a successful `tool.Outcome` whose content is:

```text
loop guard: tool "<name>" with identical arguments has been requested <count> times; the call was not run. Change the arguments or the approach.
```

`Failed` is false, so the run continues; the model sees the synthetic result
and can choose another call. The step is journaled and replayed; the tool port
is not invoked. A hard block is not an error code and does not end the run.
Further repeats are blocked at counts 7, 8, …; the model still sees each
synthetic result.

### 2.4 Rebuilding the counters

The guard state is a pure function of the turn history, not a durable counter:

1. for each turn in order, read `Turn.Message.ToolCalls`;
2. match each non-handoff call to a result in `Turn.Results` by
   `tool_call_id` (positional fallback for a result without an ID);
3. `observe` only calls that have a result.

A handoff turn's other calls have no result because the loop makes no tool
steps for them (`agent/handler.go:251-263`); they are not observed. A blocked
call has its synthetic result, so it is observed. A soft-marked call has its
result, so it is observed. The current turn's calls are observed in order as
the loop processes them.

On a handoff, the target seeds the guard from the carried turns
(`agent/output.go:63`). On a resume, the loop rebuilds from the turn steps it
has replayed. No counter is stored in `turnResult`, `Turn`, `Output` or a new
step, and the turn JSON asserted at `agent/handler_test.go:700-721` is
unchanged.

### 2.5 Scope

Assistant-text repetition (`RecordAssistantText` in the reference
implementation) is not adopted here. It has no tool side effect, it would need
a separate message-level mechanism and threshold, and it can false-positive on
legitimate repeated answers. It is a candidate for a later ADR.

The guard is always on. Existing WP-L7-01 scenarios stay green: none reaches
six identical calls, and the scenarios that reach three identical calls
(`AGENT-023-01`, `AGENT-024-01`, `AGENT-025-01`) do not assert the result text;
the soft marker's tokens still fit their limits (for example, `AGENT-024-01`'s
turn 3 grows from 400 to roughly 440 tokens against a 1000-token limit with a
300-token first turn).

## 3. Alternatives rejected

### 3.1 Dropped-turn summary

| Alternative | Cost | Verdict |
| --- | --- | --- |
| Keep the count only (today) | The model cannot recover a dropped fact; the gap this ADR closes. | Rejected. |
| Raise `input_tokens` or keep all turns | The request exceeds the model's input limit; the run fails (REQ-AGENT-027). | Rejected. |
| Summarise only the first drop, never update | Turns dropped later are unrepresented; the summary goes stale. | Rejected. |
| Re-summarise the whole dropped prefix at every boundary | A model call proportional to the prefix at every boundary, and the summary drifts as the tail moves. | Rejected; incremental extension chosen. |
| Keep the summary in memory or a side file | Replay assembles a different prompt; violates determinism (constitution article 3). | Rejected. |
| A separate summariser model field | A new config surface for a call the agent's own model can make. | Rejected for the first version. |
| Count the summary as a model turn | It would consume `caps.steps` and change turn numbering; it is memory policy, not an agent turn. | Rejected. |
| Fail the run when the summariser fails | A best-effort memory optimisation would kill an otherwise healthy run. | Rejected; fall back to the pre-change drop. |
| Summarise by default | Every memory scenario would need a scripted summariser and its token accounting would change; WP-L7-01's scenarios would not stay untouched. | Rejected; `input_summary` is opt-in. |

### 3.2 Loop guard

| Alternative | Cost | Verdict |
| --- | --- | --- |
| No guard (today) | A repeating model burns the caps with no explanation. | Rejected. |
| Fail the run on a repeat | The model cannot recover; a transient repeat becomes fatal. | Rejected. |
| Block at the first repeat | False positives on legitimate retries and paginated reads. | Rejected. |
| Deduplicate calls within one turn | Even read-only tools may depend on side effects of another call in the same batch; the reference implementation explicitly does not deduplicate a batch. | Rejected. |
| Store counters in `turnResult` | Changes the durable JSON and makes replay depend on a field the turn did not record; `agent/handler_test.go:700-721` asserts that JSON. | Rejected. |
| Hash raw argument bytes only | Key order and whitespace differences evade the guard. | Rejected. |
| Score assistant-text repetition too | No tool side effect; false positives on legitimate repeated answers; a separate mechanism. | Out of scope; later ADR. |
| No LRU bound | Memory grows with every distinct call. | Rejected; 100 entries. |
| Block without a synthetic result | The model sees a missing tool result and no explanation; the transcript is invalid. | Rejected. |

## 4. Consequences

- `agent` gains an opt-in summary path, one new step name, one new `Output`
  field and one new agent spec field. The `turnResult` JSON and `Turn` are
  unchanged.
- The summary costs one model call per frontier advance, metered against the
  same caps as turns. A run that cannot afford the summary still runs, without
  it.
- The loop guard costs one map and one 100-entry LRU per run, and one
  synthetic tool result per hard block. It adds no durable field.
- A hard block still makes a `tool-<n>-<k>` step, so REQ-AGENT-003 and the
  replay story stay intact.
- WP-L7-01's implemented scenarios stay untouched. New scenarios are
  `@planned` under the follow-up work packages.

### 4.1 Work packages

| WP | Depends | Owns | What |
| --- | --- | --- | --- |
| WP-L7-05 — Dropped-turn summary | WP-L7-01 | `agent`, `specs/agents`, `internal/bdd/steps/agent.go` | §1: `input_summary`, `summary-<from>-<to>`, `Output.Summary`, cap accounting, handoff carry, new scenarios. |
| WP-L7-06 — Loop guard | WP-L7-01 | the same | §2: canonical identity, 100-entry LRU, soft/hard thresholds, synthetic result, rebuild, new scenarios. |

Both edit `agent/handler.go`, so land WP-L7-05 first and WP-L7-06 rebased on
it. The guard has no behavioral dependency on the summary; the sequence is for
review and merge order, not for semantics. If the lead prefers one change,
combine them under WP-L7-05, but the review is smaller split.

### 4.2 Proposed new requirements

`specs/agents/requirements.md` is unchanged by this ADR. The designer adds
these with `@planned` scenarios; the numbers start at the next free one
(REQ-AGENT-031).

| Requirement | Text |
| --- | --- |
| REQ-AGENT-031 | When the context-window policy drops one or more model turns from the messages of a model turn and the agent's `input_summary` is true, the agent loop shall include one summary message of those dropped turns in the messages, after the kept first model turn and before the kept recent model turns. |
| REQ-AGENT-032 | The agent loop shall record the summary of dropped turns in a durable step named `summary-<from>-<to>` and shall use the recorded summary when it replays the run, so the replayed messages are the messages of the first execution. |
| REQ-AGENT-033 | When the agent loop makes a summarising model call, the agent loop shall reserve its estimated cost before the call, add its input and output tokens to the run's used tokens and its charge to the run's spent cost, and not count the call as a model turn for the step cap. |
| REQ-AGENT-034 | If the summarising model call fails or its request would exceed the agent's input token limit, then the agent loop shall continue the run without a summary message for the dropped turns. |
| REQ-AGENT-035 | When a handoff carries a summary of dropped turns and the target agent drops turns that the summary covers, the target agent shall use the carried summary for those turns instead of summarising them again. |
| REQ-AGENT-036 | When a model turn requests a non-handoff tool call whose canonical name and arguments have already been requested at least 3 times in the run, the agent loop shall run the call and append a loop-guard warning to its tool result. |
| REQ-AGENT-037 | When a model turn requests a non-handoff tool call whose canonical name and arguments have already been requested at least 6 times in the run, the agent loop shall not run the call and shall give the model a synthetic tool result that says the call was blocked. |
| REQ-AGENT-038 | When the loop guard compares two tool calls, the agent loop shall treat them as the same call when their tool names are equal and their arguments are equal after JSON canonicalisation: object keys sorted recursively, whitespace in string values collapsed to one space, arrays in order. |
| REQ-AGENT-039 | When an agent run resumes or receives a handoff, the agent loop shall rebuild the loop guard's counts from the model turns and tool results it has recorded, without reading a separate durable counter. |

### 4.3 What does not change

- `REQ-AGENT-003` (one tool-call step per requested tool) still holds: a
  hard-blocked call keeps its `tool-<n>-<k>` step.
- `REQ-AGENT-023`–`REQ-AGENT-025` still hold: dropped turns are dropped; the
  summary is an additional message, not a kept raw turn.
- `REQ-AGENT-027` still holds: a kept set over `input_tokens` fails the run.
  The summary is skipped when it would not fit, so it does not create a new
  failure.
- The durable `turnResult` JSON asserted in `agent/handler_test.go:700-721` is
  unchanged.

## 5. Revert

Revert the merge commit. Remove this ADR and its README row, the summary step
and `input_summary` field, the `Output.Summary` field and its handoff carry,
and the loop guard. The `turnResult`, `Turn` and tool-step result JSON need no
migration because they were not changed.
