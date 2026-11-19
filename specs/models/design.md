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
  `invalid_argument` (REQ-MODEL-047, MODEL-047-01, MODEL-047-02, MODEL-054-01,
  MODEL-054-02, MODEL-062-01).

## 1a. Client refs

The caller passes the call's client ref. Attempt `n` of the call starts
with `ClientRef(call-ref, n)`: for attempt 1, the call's client ref itself;
for later attempts, a stable hash of the call's client ref and the attempt
number `n`, so a repeated call with the same client ref produces the same
ref for the same attempt, and the ref is never a literal derived by
concatenation. A retry of a start that the provider did not acknowledge
(REQ-MODEL-005) reuses that attempt's client ref; any other attempt gets its
own. The call log records each attempt's ref (MODEL-062-01, MODEL-054-01,
MODEL-054-02).

## 2. Rung support and the request

- A descriptor declares its rungs; the request for a rung uses the existing
  `Request` fields: native schema and JSON mode through `OutputSchema` plus
  the vendor's structured-output parameter, forced tool call through `Tools`.
  No new contract field is needed.
- A model that declares no rung fails with `unsupported` before any start
  (REQ-MODEL-046, MODEL-046-01).

## 1b. Placement of `model/structured` and `model/middleware`

`model/structured` sits between `metering` and `retry` in the middleware chain:
`call log ∘ recording ∘ fallback ∘ metering ∘ structured ∘ retry ∘ timeouts`.
For a plain chat call, the `structured` stage passes through with `rung = none`.
A shared per-call state tracks remaining attempt budget, the call total timeout
deadline, the call's retry index `k`, and the current rung:
- `retry` retries retryable provider errors on the current rung without advancing
  the ladder (REQ-MODEL-048) and preserves the call's backoff progression `k`;
- `structured` advances the rung only when `retry` returns a completed output
  that misses the schema (REQ-MODEL-044), and runs local JSON repair in-process
  after the last supported rung without entering `retry` or `timeouts` (REQ-MODEL-065);
- `metering` records every provider invocation across rungs and retries (REQ-MODEL-060)
  and does not meter local repair (0 invocations);
- `call log` records every attempt (using `rung: local_repair` for a local JSON
  repair attempt) and `recording` captures the whole call (REQ-MODEL-062, REQ-MODEL-064).

## 3. Retry

| Decision | Choice |
| --- | --- |
| Retried | `core.IsRetryable(err)` is true, the attempt produced no event, and attempt budget remains; the next attempt uses the same rung (REQ-MODEL-048) |
| Not retried | non-retryable errors (MODEL-049-01), failures after an event (MODEL-050-01), and a retryable error once the budget is spent, which fails the call with that error (REQ-MODEL-066) |
| Base and factor | the base wait is 1000 ms before the call's first retry and doubles on each subsequent retry of the call (`1000 ms × 2^(k-1)` before retry `k`, counting retries within the call) |
| Jitter | in `[0, 50%]` of that retry's base wait, added to it and drawn from the call's seeded randomness; the jitter never becomes part of the next base wait, and the same seed gives the same waits (REQ-MODEL-051) |
| Retry-After | the longer of `base wait + jitter` and `core.Error.RetryAfter` (REQ-MODEL-052) |
| Call total timeout | no call total timeout by default unless the call sets one (REQ-MODEL-059); when set, measured from the start of attempt 1 across attempts and waits, and if the required wait before the next attempt would reach or exceed it, the call fails at once with `core.CodeTimeout` without starting another attempt (REQ-MODEL-053) |
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
timeout, idle timeout and attempt total timeout. No call total timeout applies
unless the call sets one (§3, REQ-MODEL-059). The first-chunk timer starts at the
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

# Models: catalog and price table (WP-L3-06)

Decisions the implementer must follow in `model/catalog`. The requirements
are REQ-MODEL-067 to REQ-MODEL-082 (`requirements.md`, section "Catalog and
price table"); where this section is silent, the requirements win.
`docs/architecture/model-layer.md` §2 and §7 and ADR-0006 set the rules; every
number or default below that the docs leave open is a choice for the reviewer
to weigh.

## C1. Descriptors by model id

- `model/catalog` holds the descriptors: those of the built-in providers and
  the snapshot, and the descriptors that workers declare to their pools (ADR-0012
  §2, WP-L3-07) once that work lands. The catalog answers by model id
  (REQ-MODEL-067); a lookup of an id it does not hold fails with
  `core.CodeNotFound` (REQ-MODEL-068, MODEL-068-01). Availability of a
  worker-served model is REQ-MODEL-013/REQ-MODEL-034's and is not specified
  here.
- The descriptor is `model.Descriptor` as it stands in `model/model.go`: the
  catalog adds no field to it. `Price` stays the CEL formula over `params`,
  `usage` and `rates`. A worker descriptor and a catalog descriptor with the
  same id must be identical strings (REQ-MODEL-028 compares them).

## C2. The price table

Three layers, most specific first: the tenant's override, the deployment's
override, the snapshot shipped with the binary (REQ-MODEL-071). The most
specific layer that has an entry for the model supplies that model's whole
price entry for the call (layers do not merge rules or fall through when a
rule's `when` does not hold: MODEL-073-03).
- A model's price in a layer is either a **set of rate rules** (each with
  rates and an optional `when`, as in `docs/architecture/model-layer.md` §7
  YAML) or a **formula** of its own. A test or a flow that states a price as
  `Given a model "X" priced at "<CEL>" milli-points` (the phrase of
  `cost.feature` and `middleware.feature`) sets the model's formula in every
  layer; `is priced by the shipped snapshot at "<CEL>" milli-points` sets
  only the shipped snapshot layer; `has the rate rules "input <a> and
  output <b>" per million tokens` sets one unconditional rule in the shipped
  snapshot, the variant `"input <a>, output <b> and cached <c>"` sets the
  same rule with a cached rate, and `the model "X" has the rate rules:` /
  `tenant "T" overrides the rate rules of "X" with:` followed by a table with
  a `when` column and
  an `input`, an `output` and optionally a `cached` column (milli-points per
  million tokens; an empty cell omits that rate) set
  those rules, in table order, in the shipped snapshot or in the tenant's
  layer. A price rule table may also set the deployment's layer the same way
  (an empty layer holds no entry).
- The chosen layer's rules are tried in their table order; the first whose
  `when` holds for the invocation applies to all its tokens (MODEL-069-01,
  MODEL-070-01). A rule whose `when` cell is `always` has no condition and
  always holds. Token rates are in
  **milli-points per million tokens**: a million input tokens cost `input`
  milli-points (rate `280` = 280 milli-points per million tokens = 0.28
  milli-points per thousand tokens), and a million output tokens cost `output`
  milli-points.
- `when` is CEL over `at` (the start time of the invocation, `core.Clock` at
  its start, recorded with it), `params`, `usage` and `tenant`. `usage` is the
  usage being priced: the estimated usage (`input_tokens` = counted input
  tokens, `output_tokens` = output limit) when estimating before start, and
  actual usage at settlement. The evaluation sees no clock.
- A `when` that errors (division by zero, a missing parameter key) or returns
  anything but a boolean fails pricing with `core.CodeInvalidArgument` and
  stops the rule search (REQ-MODEL-083, MODEL-083-01). The rule is not
  skipped: skipping it would silently price the call with a later, possibly
  cheaper, rule, and the caller would never learn that its price data or its
  parameters are broken. Errors from `expr` already carry that code, so the
  catalog returns them unchanged. This is the fail-closed rule of C3 applied
  to the condition.
- A rate below zero is refused where it is set — tenant, deployment or
  snapshot layer — and while the shipped snapshot loads, with
  `core.CodeInvalidArgument` and details `model` and `rate` naming the model
  id and the rate key (REQ-MODEL-084, MODEL-084-01, MODEL-084-02). A negative
  rate would turn a charge into a credit, so bad price data fails closed
  exactly as bad usage does in C3; nothing downstream has to defend against a
  rate the catalog accepted. The shipped snapshot is a golden file generated
  at build time (C6), so the load check is what keeps a bad generation from
  shipping.
- The tenant is taken from the tenant context of the call (constitution
  article 8); the catalog never keys an override by a value in the request
  (REQ-MODEL-072). A call made outside any tenant uses the deployment layer:
  its tenant is the empty tenant, which has no overrides (MODEL-072-02).
- Where no layer has an entry for the model, or the chosen layer's rate rules
  have none whose `when` holds for the call, pricing fails with
  `core.CodeUnsupported` (REQ-MODEL-073, MODEL-073-01..03) —
  the same code a model with no rung gets (REQ-MODEL-046), since `core` has no
  code for "no price".

## C3. The formula and the token default

- **Usage convention (A):** `usage.cached_tokens` is a part of
  `usage.input_tokens`, and `usage.reasoning_tokens` is a part of
  `usage.output_tokens`; neither is added to its total. `model.Usage`
  (`model/model.go`) has exactly `InputTokens`, `OutputTokens`,
  `CachedTokens` and `ReasoningTokens`. Providers differ — OpenAI's prompt
  tokens include the cached ones, Anthropic reports cache reads apart from
  its input tokens — so the provider adapter (WP-L3-04) normalizes what the
  vendor reports into this convention before the usage reaches the price
  table.
- **The default token formula:** a model whose price is a set of rules and
  that declares no formula in its descriptor is priced by the chosen rule's
  rates on the call's usage (REQ-MODEL-074). Rates are per million tokens;
  all amounts below are divided by 1e6 before rounding (REQ-MODEL-076), and
  `I`, `O`, `C`, `R` are the usage's input, output, cached and reasoning
  tokens.
  - **Rule defines `rates.cached`** (REQ-MODEL-075):
    `(I − C) × rates.input + C × rates.cached + O × rates.output`. The
    cached tokens are subtracted from the input tokens and added back at the
    cached rate; they are neither left at the input rate nor counted on top
    of the input tokens.
  - **Rule omits `rates.cached`** (REQ-MODEL-079):
    `I × rates.input + O × rates.output`. Every input token, cached ones
    included, costs the input rate; the cached tokens are neither subtracted
    nor double-counted.
  - **Cached rate zero (B)** (REQ-MODEL-080): a rule that sets
    `cached: 0` defines the rate, so cache reads are free; only an absent
    `cached` rate falls back to the input rate (MODEL-080-01).
  - **Reasoning** (REQ-MODEL-081): `reasoning_tokens` are already inside
    `O` and are never added again; there is no reasoning rate in the default
    formula (a rule's `rates.reasoning`, if a table sets one, is not used by
    it). MODEL-081-01 prices a call with reasoning but no cached tokens:
    with `I = 3000`, `O = 600`, `R = 300` and the rule `input 4000 and
    output 10000`, `3000 × 4000 + 600 × 10000 = 12,000,000 + 6,000,000 =
    18,000,000` → **18**; counting the reasoning twice (`O + R = 900` output
    tokens) gives `12,000,000 + 9,000,000 = 21,000,000` → 21.
- **Malformed usage (C)** (REQ-MODEL-082): if `C > I`, no formula is
  evaluated — the uncached part `I − C` would be negative — and pricing fails
  with `core.CodeInvalidArgument`. The check runs before any formula, the
  default one or a model's own, since a model's formula may read `usage` too;
  it is what keeps bad provider data from turning into a credit. The
  reservation estimate sets `C = 0` (C4), so it passes the check every time.
  A negative `I`, `O`, `C` or `R` fails pricing the same way, before any
  formula and naming the model and the offending count (REQ-MODEL-085,
  MODEL-085-01); the details are `model` and `field`, the latter one of
  `input_tokens`, `output_tokens`, `cached_tokens` or `reasoning_tokens` as
  the CEL bindings name them. The negative-count checks run before the `C > I`
  check, so each of MODEL-085-01's rows names its own field; MODEL-082-01
  (every count non-negative) still fails for the cached-versus-input reason.
  Both orders return `core.CodeInvalidArgument`, which is what a caller can
  observe. The check is needed even though a negative amount later fails too
  (below): a formula such as `usage.input_tokens * usage.input_tokens` would
  otherwise price a negative count as a positive amount.
- **Cache writes are not priced.** `model.Usage` has no cache-write or
  cache-creation field, so cache-write tokens, a `rates.cache_write`, TTL or
  storage are not priced by the default formula and no rule rate for them
  applies. Pricing them needs a separate additive `feat(model)` contract
  change (new `Usage` fields, new rates) with its own spec and review;
  this WP does not change `model/model.go`, and MODEL-075-02 prices a usage in
  which the vendor's cache reads, and nothing more, are reported.
- A model that declares its own formula (image, video, `params.duration_s`,
  …) uses it with `rates` bound to the chosen rule's rates, whatever usage
  it reads.
- **A formula's amount is whole, and never negative** (REQ-MODEL-086,
  REQ-MODEL-087): a formula that returns an amount below zero fails pricing
  with `core.CodeInvalidArgument`, while `0` is a valid amount and prices the
  call at nothing (MODEL-086-01, MODEL-086-02). The amount must also be a
  whole number of milli-points: CEL has no exact decimal, so
  `double(usage.input_tokens) * 0.145` on 100 tokens is the `double`
  `14.499999999999998`, and half-up rounding (REQ-MODEL-076) would silently
  turn it into 14 instead of 15. (CEL needs the explicit `double(...)`
  conversion; `int × double` does not type-check, and the failure of that
  form is a compile error, not this check.) The catalog therefore accepts an
  `int`, a `uint` that fits in `int64`, or a `double` that is an exact integer
  of magnitude at most 2^53, and fails any other `double` with
  `core.CodeInvalidArgument` naming the model (MODEL-087-01). Fractional
  formulas wait for an exact decimal in the CEL environment — a separate
  design decision for the architect, not a WP-L3-06 change. The default token
  formula is unaffected: it computes in exact `big.Int` arithmetic and divides
  by one million once, as above.
- **Rounding (REQ-MODEL-076):** the formula and the rule rates are evaluated
  over exact decimal values and the result is rounded to the nearest whole
  milli-point, halves away from zero (up, since prices are not negative:
  MODEL-076-01, MODEL-076-02). The rounding happens once, on the final
  amount; no intermediate value is rounded. Implementations evaluate in
  `math/big.Rat` or integers scaled by 10^6, never `float64` — except that a
  model's own formula may return a `double` (above), which is converted
  exactly with `big.Rat.SetFloat64` and accepted only when that exact value is
  a whole number, so the check itself introduces no rounding.

## C4. Estimation for a reservation

- **Counted input tokens (definition, used by REQ-MODEL-077 and the cost
  specs):** for a chat call, the tokens the model's tokenizer gives the
  messages, the tool definitions and local repair input of the request; the
  tokenizer is the model's descriptor's, so the same request always counts
  the same. A call made outside a run (middleware, metering) counts the
  messages and tools of the request the same way.
- **Estimate (REQ-MODEL-077):** the model's formula (default or its own) on
  `params` = the call's parameters, `usage.input_tokens` = the counted input
  tokens, `usage.output_tokens` = the call's output limit (`0` when the call
  sets no output limit), cached and reasoning at 0, and `rates` = the rule
  the `when` chooses with that estimated usage and the recorded start time.
  The estimate is what REQ-MODEL-006 reserves; the budget comparison and the
  `budget_exceeded` failure are REQ-MODEL-007's and not specified here.
- In MODEL-077-01 the estimate counts the request's counted input tokens
  (1000), the output limit (500) and the parameter `tier`, so `1000 × 1 + 500
  × 8 + 100 = 5100`. In MODEL-078-01 the call starts when the snapshot prices
  it at `input × 1 + output × 8`, so its estimate is `1000 × 1 + 500 × 8 =
  5000`; the snapshot then prices it at `input × 9 + output × 8`, but the
  charge is the captured entry on the settled usage, `1000 × 1 + 200 × 8 =
  2600`.
- In MODEL-078-02 the call starts at `2026-10-01T16:59Z`, when only the
  `always` rule holds, and the estimate is `1000 × 280 / 1e6 + 500 × 1100 /
  1e6 = 0.28 + 0.55 = 0.83`, rounded to `1` milli-point. It settles at
  `17:05Z`, when the clock-selected rule `at.getHours('UTC') >= 17` holds,
  but `at` is the recorded start time, so the charge is `250000 × 280 / 1e6 +
  20000 × 1100 / 1e6 = 70 + 22 = 92` milli-points; a settlement that read the
  clock again would charge the first rule's `35 + 11 = 46`.
- **Cached-rate worked examples (C4, D).** Each number below was checked
  after rounding (half up, in milli-points, the total divided by 1e6) for
  the correct formula and for each wrong one: *ignore the cached rate*,
  *subtract the cached tokens but do not add them back*, *add the cached
  tokens on top of the input tokens*, and *count the reasoning tokens twice*.
  - MODEL-075-01: a rule with `cached`, `I = 4000`, `C = 1000`, `O = 1000`,
    `R = 100`, rates input 2500, output 10000, cached 1250. Correct:
    `(4000 − 1000) × 2500 + 1000 × 1250 + 1000 × 10000 = 7,500,000 + 1,250,000
    + 10,000,000 = 18,750,000` → **19**. Ignore cached: `4000 × 2500 + 1000 ×
    10000 = 10,000,000 + 10,000,000 = 20,000,000` → 20. Subtract only:
    `3000 × 2500 + 1000 × 10000 = 7,500,000 + 10,000,000 = 17,500,000` → 18.
    Add on top: `5000 × 2500 + 1000 × 10000 = 12,500,000 + 10,000,000 =
    22,500,000` → 23. Reasoning twice: `(3000 × 2500 + 1000 × 1250) + 1100 ×
    10000 = 8,750,000 + 11,000,000 = 19,750,000` → 20.
  - MODEL-075-02: no `cached` rate, `I = 6000`, `C = 4000`, `O = 1200`,
    `R = 300`, rates input 2000, output 10000. Correct: `6000 × 2000 + 1200 ×
    10000 = 12,000,000 + 12,000,000 = 24,000,000` → **24**. Subtract only, as
    if a cached rate of zero applied: `2000 × 2000 + 12,000,000 =
    16,000,000` → 16. Add on top: `10000 × 2000 + 12,000,000 = 32,000,000` →
    32. Reasoning twice: `12,000,000 + 1500 × 10000 = 27,000,000` → 27.
  - MODEL-080-01: `cached: 0` with `I = 5000`, `C = 2000`, `O = 400`, rates
    input 3000, output 10000. Correct: `3000 × 3000 + 2000 × 0 + 400 × 10000
    = 9,000,000 + 4,000,000 = 13,000,000` → **13**. Reading `0` as *absent*
    (fall back to the input rate, MODEL-075-02's rule): `5000 × 3000 +
    4,000,000 = 19,000,000` → 19. Add on top: `7000 × 3000 + 4,000,000 =
    25,000,000` → 25.
  - MODEL-081-01: no `cached` rate, `I = 3000`, `O = 600`, `R = 300`, rates
    input 4000, output 10000. Correct: `3000 × 4000 + 600 × 10000 =
    12,000,000 + 6,000,000 = 18,000,000` → **18**. Reasoning twice:
    `12,000,000 + 900 × 10000 = 21,000,000` → 21.
  - MODEL-082-01: `I = 3000`, `C = 3500`, `O = 400`, rates input 4000,
    output 10000. The correct answer is the failure `invalid_argument`; any
    formula that ran would give a negative uncached part (`−500 × 4000`)
    and a charge below the zero-cached charge `3000 × 4000 + 400 × 10000 =
    16,000,000` → 16.

## C5. Pricing is fixed when the invocation starts

- The price entry from the winning layer (its ordered rate rules or its
  formula), the model descriptor's formula if any, and the start time `at`
  are captured when the call starts (and returned as a pinned price handle
  for the caller to retain across the call). Pricing the call's settled
  usage evaluates `when` and the formula against actual `usage`, `params`,
  `tenant`, and that recorded `at` using the captured price entry, whatever
  the price table changes while the call is running (REQ-MODEL-078,
  MODEL-078-01); MODEL-078-02 settles a call started just before a rule's
  window opens), whatever the clock reads. A retry of the same logical call (same client ref) uses the
  same captured pricing.

## C6. Consumers of this section

- WP-L3-03 (`model/node`) reserves the estimate of C4 and settles with the
  formula on actual usage; WP-L3-02's metering prices each attempt with the
  same formula. Those mechanics are theirs (`cost.feature`, `middleware.feature`).
- The shipped snapshot is generated at build time from a public dataset and
  is a golden file (`docs/architecture/model-layer.md` §7); nothing fetches
  prices at run time.
- `model/catalog` lives in package `model/catalog` (layer `model` in
  `layering.yaml`), which may import `core`, `platform` and `expr` (the CEL
  environment of WP-L2-01); it cannot import `model`'s children that import
  it. If the catalog must live inside `model` itself to avoid a cycle, the
  implementer lands that move in a pull request of its own before this WP
  starts, so the package graph is settled before any catalog code lands.

## Open questions

1. **Cost requirement: REQ-MODEL-006 re-locking (`requirements.md`, approved text).** REQ-MODEL-077 says the reservation uses the estimate of C4 for **every** paid call, while REQ-MODEL-006 says "estimated by the model's price formula". They agree for a model with rate rules; for a model that declares its own formula (a video model priced on `params.duration_s`), REQ-MODEL-006 read literally leaves the estimate to that formula, which the docs define no usage for; under C4 it gets the same usage (counted input tokens, output limit). Should REQ-MODEL-006 be re-locked to cite REQ-MODEL-077, or does this WP leave it (cost, P1 re-review either way)?
2. **Default token formula: cached and reasoning tokens.** The default formula uses `rates.cached` when the rule defines it and the input rate when it omits it, a `cached: 0` is free rather than absent, reasoning tokens are counted once inside `output_tokens`, and `C > I` fails pricing with `invalid_argument` (REQ-MODEL-075, REQ-MODEL-079 to REQ-MODEL-082, C3, C4). The usage convention (`cached_tokens` ⊂ `input_tokens`, `reasoning_tokens` ⊂ `output_tokens`) is the adapter's normalization (C3, A). Cache-write and creation tokens stay unpriced until an additive `feat(model)` change to `model.Usage`; confirm that separate change is the intended route rather than pricing cache writes from the existing fields.
3. **Default formula and the WP-L3-02 fixture (`design.md` §5, MODEL-060-01, MODEL-061-01).** `middleware.feature` and the earlier section's §5 price `sim-chat` with `(usage.input_tokens * 1 + usage.output_tokens * 8) / 1000` milli-points, which at the per-million rates of C2 would be 1,000× the default formula of REQ-MODEL-074 (`input × 1e-6 + output × 8e-6`: `1000 × 1e-6 + 1000 × 8e-6 = 0.009` against `1 + 8 = 9`). The scenarios are approved text; the implementer of WP-L3-02 must state that price as a model formula in the manner of C2 (a fixture), not as rules fed to the default formula. If the two are reconciled, it is a WP-L3-02 spec change (P1).
4. **Where overrides are configured (Open Question 4).** The roadmap and docs name the layers but not who writes deployment and tenant overrides (a config file for the deployment, which API or command for a tenant). WP-L3-06 specifies only reading and applying the three layers in `model/catalog`; should the write side be a task of WP-L3-06, or another work package?
5. **Rounding of an exact half (REQ-MODEL-076, MODEL-076-01..02).** The docs give no rule; half up was chosen (MODEL-076-01) because it never under-charges a fraction of cost. Confirm, or choose half to even.
6. **The estimate's counted input tokens (REQ-MODEL-077, C4, MODEL-077-01, MODEL-078-01).** The docs say "counted input tokens" without saying by which tokenizer. The model's own tokenizer is proposed, with a deterministic test tokenizer in the gate; where a descriptor names none, the implementer must pick one and say so.
7. **Error codes when no price rule holds (REQ-MODEL-073, MODEL-073-01) and for an unknown model id (REQ-MODEL-068, MODEL-068-01).** The price table uses the existing `core.CodeUnsupported` (`"unsupported"`) when no price rule matches and `core.CodeNotFound` (`"not_found"`) when a model id is not in the catalog (`core` already has the code, and ADR-0012 §2 uses the word for the catalog), adding nothing to `core/errors.go`; confirm both codes, the latter over `invalid_argument`.
