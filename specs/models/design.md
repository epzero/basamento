# Models: structured output and middleware (WP-L3-02)

Decisions the implementer must follow in `model/structured` and
`model/middleware`. The requirements are REQ-MODEL-043 to REQ-MODEL-066
(`requirements.md`); where this note is silent, the requirements win.
`docs/architecture/model-layer.md` §5 and ADR-0006 set the rules; every
number below that the docs leave open is a choice for the reviewer to weigh.

## 1. Attempt accounting

- One **call** is one logical chat invocation entering the middleware chain.
  Its **attempts** are numbered from 1 and share one attempt budget (`3` by
  default, REQ-MODEL-043), across both structured-output ladder tries and
  retry middleware tries. A call that only needs a plain chat answer still
  has that budget: a non-structured call retries until the budget is spent
  (REQ-MODEL-048, REQ-MODEL-066). Every attempt that reaches a model is one **model
  invocation** (`Invoker.Start`); the local JSON repair after the last
  supported rung is not a model invocation (it makes no `Invoker.Start` call)
  but consumes 1 attempt from the call's attempt budget (REQ-MODEL-065). A
  call refused before reaching any rung (REQ-MODEL-046) makes 0 attempts and
  0 invocations.
- The ladder moves forward only on an attempt whose output misses the schema
  (REQ-MODEL-044). Any provider error is the retry middleware's business (§3)
  and retries on the same rung. Waits spent between attempts do not consume
  the ladder: a retry after a retryable error reuses the failed attempt's rung.
- The local repair runs in-process on the output of the attempt on the last
  rung the model supports, provided attempt budget remains (REQ-MODEL-065). If
  the repair yields output that satisfies the schema, the call returns it
  (`structured-output.feature`, MODEL-065-02). The repair strips surrounding
  prose or code fences and fixes JSON syntax only (trailing commas, quotes,
  escapes): it never adds, drops or changes a field, so a missing `answer`
  cannot be fabricated. If the repair still misses the schema, or if no
  attempt budget remains after the last supported rung, the call fails with
  `invalid_argument` (REQ-MODEL-047, MODEL-047-01, MODEL-047-02).

## 1a. Client refs

The caller passes the call's client ref. Attempt `n` of the call starts
with `ClientRef(call-ref, n)`: for attempt 1, the call's client ref itself;
for later attempts, a deterministic derivation of it and `n` (a stable
hash, so a repeated call with the same client ref produces the same
refs). A retry of a start that the provider did not acknowledge (REQ-MODEL-005)
reuses that attempt's client ref; any other attempt gets its own. The call
log records both (MODEL-062-01).

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
| Retried | `core.IsRetryable(err)` is true, the attempt produced no event, and attempt budget remains; the next attempt uses the same rung (REQ-MODEL-048) |
| Not retried | non-retryable errors (MODEL-049-01), failures after an event (MODEL-050-01), and a retryable error once the budget is spent, which fails the call with that error (REQ-MODEL-066) |
| Base and factor | the base wait is 1000 ms before the call's first retry and doubles on each subsequent retry of the call (`1000 ms × 2^(k-1)` before retry `k`, counting retries within the call) |
| Jitter | in `[0, 50%]` of that retry's base wait, added to it and drawn from the call's seeded randomness; the jitter never becomes part of the next base wait, and the same seed gives the same waits (REQ-MODEL-051) |
| Retry-After | the longer of `base wait + jitter` and `core.Error.RetryAfter` (REQ-MODEL-052) |
| Call total timeout | measured from the start of attempt 1 across attempts and waits; if the required wait before the next attempt would reach or exceed the call total timeout, the call fails at once with `core.CodeTimeout` without starting another attempt (REQ-MODEL-053) |
| Whose clock | the middleware waits on `core.Clock` and timers; the gate advances `coretest.FakeClock`, so no wall time is read |
| Reset | each retry attempt (attempt ≥ 2) emits `EventReset` with `Attempt` before its own events (REQ-MODEL-055), as `docs/architecture/streaming.md` §4 states |

The seeded randomness is not in `core` yet. It is a source the caller
injects (`math/rand/v2` with a fixed seed in tests) and the middleware only
draws from it. If `core` needs a new port for it, the implementer lands that
in a pull request of its own before this WP starts, since `core` is outside
this WP's directories.

## 4. Timeouts

Per-attempt streaming defaults: first-chunk `30s`, idle `60s`, streaming total
`10m` (REQ-MODEL-059), overridden per call with the call's first-chunk
timeout, idle timeout and attempt total timeout. The first-chunk timer starts at the
start of the attempt and is replaced by the idle timer at the first event
(reset on each subsequent event); the per-attempt total timer covers the
streaming attempt (REQ-MODEL-058). Each fires once on the fake clock and
cancels the attempt with `core.CodeTimeout`, `Retryable: true`, so
REQ-MODEL-048 retries it while attempt budget (REQ-MODEL-066) and the call
total timeout (REQ-MODEL-053) allow (MODEL-056-01). A timer that fires cancels the attempt,
not the call; whether another attempt follows is decided by the retry rules
(REQ-MODEL-048, REQ-MODEL-050, REQ-MODEL-053).

## 5. Metering and the call log

- Metering records `Usage` and the cost in whole milli-points per attempt that
  reached the provider, failed attempts included (REQ-MODEL-060). The cost
  comes from the descriptor's price formula (WP-L3-06); until that lands, the
  test model's descriptor carries the formula
  `(usage.input_tokens * 1 + usage.output_tokens * 8) / 1000` milli-points. No
  usage reported means zero tokens and zero milli-points (REQ-MODEL-061).
- The call log has one entry per attempt with the attempt's client ref
  (REQ-MODEL-054), the credential `cred://` reference if the attempt used a
  credential, the attempt number, the model, the rung or `none` when the call
  was not structured, the start time (`core.Clock`) and the outcome. It names a credential by its `cred://` reference only
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

## Open questions

1. **Seeded randomness port (`design.md` §3):** `core` currently provides `core.Clock` and `coretest.FakeClock`, but no randomness port. Should `model/middleware` accept a narrow local seed/source option, or should a shared randomness port land in `core` in its own pull request before WP-L3-02 implementation starts?
2. **Author choices of numeric constants (`REQ-MODEL-043`, `REQ-MODEL-051`, `REQ-MODEL-059`):** Default attempt budget (`3`), retry backoff (`1000 ms` base, `2×` factor, `[0, 50%]` jitter), and streaming timeouts (`30s` first-chunk, `60s` idle, `10m` total) are not fixed by `docs/architecture/model-layer.md` §5 or ADR-0006 and are proposed here for reviewer confirmation.
3. **Local JSON repair attempt accounting (`REQ-MODEL-065`, `design.md` §1):** Local repair makes no `Invoker.Start` call (0 model invocations) but consumes 1 attempt from the structured-output call's attempt budget; if all supported model rungs miss the schema on a model with fewer rungs than the budget (e.g., 1 supported rung with default budget 3), local repair runs once on the last supported rung's output and, if it still misses the schema, ends the call after attempt 2 (`MODEL-047-02`).
