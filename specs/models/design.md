# Models: structured output and middleware (WP-L3-02)

Decisions the implementer must follow in `model/structured` and
`model/middleware`. The requirements are REQ-MODEL-043 to REQ-MODEL-064
(`requirements.md`); where this note is silent, the requirements win.
`docs/architecture/model-layer.md` §5 and ADR-0006 set the rules; every
number below that the docs leave open is a choice for the reviewer to weigh.

## 1. Attempt accounting

- One **call** is one logical chat invocation. Its attempts are numbered from
  1. The budget (`3` by default, REQ-MODEL-043) counts every attempt that
  reaches a model and the local repair as one attempt (REQ-MODEL-045). A rung
  whose request is refused before reaching the model counts nothing.
- The ladder moves forward only on an attempt whose output misses the schema
  (REQ-MODEL-044). Any other failure is the retry middleware's business (§3).
  Waits spent between attempts do not consume the ladder.
- The local repair is not a model call: after the attempt on the last rung
  the model supports, the repair runs on that attempt's output. If the repair
  yields output that satisfies the schema, the call returns it
  (`structured-output.feature`, MODEL-045-02); otherwise the attempt has
  missed the schema. The budget still bounds the whole call
  (MODEL-047-02).

## 2. Rung support and the request

- A descriptor declares its rungs; the request for a rung uses the existing
  `Request` fields: native schema and JSON mode through `OutputSchema` plus
  the vendor's structured-output parameter, forced tool call through `Tools`.
  No new contract field is needed.
- A model that declares no rung fails with `unsupported` before any start
  (REQ-MODEL-046, MODEL-046-01).

## 3. Retry

| Decision | Choice |
| --- | --- |
| Retried | `core.IsRetryable(err)` is true, and the attempt produced no event |
| Not retried | non-retryable errors (MODEL-049-01) and failures after an event (MODEL-050-01) |
| Base and factor | 1000 ms, doubled per attempt, capped by the total timeout |
| Jitter | up to 50% of the computed wait, drawn from the call's seeded randomness; the same seed gives the same wait (REQ-MODEL-051) |
| Retry-After | the longer of the computed wait and `core.Error.RetryAfter` (REQ-MODEL-052) |
| Whose clock | the middleware waits on `core.Clock` and timers; the gate advances `coretest.FakeClock`, so no wall time is read |
| Reset | each new attempt emits `EventReset` with `Attempt` before its own events (REQ-MODEL-055), as `docs/architecture/streaming.md` §4 states |

The seeded randomness is not in `core` yet. It is a source the caller
injects (`math/rand/v2` with a fixed seed in tests) and the middleware only
draws from it. If `core` needs a new port for it, the implementer lands that
in a pull request of its own before this WP starts, since `core` is outside
this WP's directories.

## 4. Timeouts

First-chunk `30s`, idle `60s`, total `10m` (REQ-MODEL-059), override per call.
The first-chunk timer starts at the start of the attempt and is replaced by
the idle timer at the first event; the total timer covers the attempt. Each
fires once on the fake clock and cancels the attempt with `core.CodeTimeout`,
`Retryable: true`, so REQ-MODEL-048 retries it until the attempt budget is
spent (MODEL-056-01).

## 5. Metering and the call log

- Metering records `Usage` and the cost in milli-points per attempt, failed
  attempts included (REQ-MODEL-060). The cost comes from the descriptor's
  price formula (WP-L3-06); until that lands, the test model's descriptor
  carries the formula. The ordering used: `input × 1` plus `output × 8`
  milli-points per 1000 tokens. No usage means zero tokens and zero cost
  (REQ-MODEL-061).
- The call log has one entry per attempt with the client ref, the attempt
  number, the model, the rung, the start time (`core.Clock`) and the
  outcome. It names a credential by its `cred://` reference only
  (REQ-MODEL-063). It goes to `slog` with structured fields
  (`docs/engineering/coding-guidelines.md`), behind a small sink interface
  the gate can read back in-process.
- Metering is recorded per attempt; whether settlement charges the attempts
  is WP-L3-03's `model/node` concern, not this WP's.

## 6. Recording and fallback

They sit in the chain (REQ-MODEL-064) and wrap a whole call, so the
recording holds every attempt of the call (MODEL-064-01). What a recording
contains is owned by another work package; this WP only fixes its place in
the order and its granularity (per call, attempts inside).

## 7. Out of scope

The invocation contract (WP-L3-01), the `model` node, reservations and
settlement (WP-L3-03), providers (WP-L3-04), the price table (WP-L3-06) and
JSON Schema validation itself (`specs/schema`). Scenarios here use the
scripted model and the fake clock only.
