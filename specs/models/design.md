# Models: structured output and middleware (WP-L3-02)

Decisions the implementer must follow in `model/structured` and
`model/middleware`. The requirements are REQ-MODEL-043 to REQ-MODEL-064
(`requirements.md`); where this note is silent, the requirements win.
`docs/architecture/model-layer.md` §5 and ADR-0006 set the rules; every
number below that the docs leave open is a choice for the reviewer to weigh.

## 1. Attempt accounting

- One **call** is one logical chat invocation entering the middleware chain.
  Its **attempts** are numbered from 1 and share one attempt budget (`3` by
  default, REQ-MODEL-043), across both structured-output ladder tries and
  retry middleware tries. Every attempt that reaches a model is one **model
  invocation** (`Invoker.Start`); the local JSON repair after the last
  supported rung is not a model invocation (it makes no `Invoker.Start` call)
  but consumes 1 attempt from the call's attempt budget (REQ-MODEL-065). A
  call refused before reaching any rung (REQ-MODEL-046) makes 0 attempts and
  0 invocations.
- The ladder moves forward only on an attempt whose output misses the schema
  (REQ-MODEL-044). Any provider error is the retry middleware's business (§3)
  and retries on the same rung. Waits spent between attempts do not consume
  the ladder.
- The local repair runs in-process on the output of the attempt on the last
  rung the model supports, provided attempt budget remains (REQ-MODEL-065). If
  the repair yields output that satisfies the schema, the call returns it
  (`structured-output.feature`, MODEL-065-02); if the repair still misses the
  schema, or if no attempt budget remains after the last supported rung, the
  call fails with `invalid_argument` (REQ-MODEL-047, MODEL-047-01,
  MODEL-047-02).

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
| Retried | `core.IsRetryable(err)` is true, the attempt produced no event, and attempt budget remains |
| Not retried | non-retryable errors (MODEL-049-01) and failures after an event (MODEL-050-01) |
| Base and factor | 1000 ms before attempt 2, doubled per subsequent retry (`1000 ms × 2^(k-1)` before attempt `k+1`) |
| Jitter | in `[0, 50%]` of the base wait, drawn from the call's seeded randomness; the same seed gives the same wait (REQ-MODEL-051) |
| Retry-After | the longer of `base wait + jitter` and `core.Error.RetryAfter` (REQ-MODEL-052) |
| Call total timeout | measured from the start of attempt 1 across attempts and waits; if the required wait before the next attempt would reach or exceed the call's total timeout, the call fails at once with `core.CodeTimeout` without starting another attempt (REQ-MODEL-053) |
| Whose clock | the middleware waits on `core.Clock` and timers; the gate advances `coretest.FakeClock`, so no wall time is read |
| Reset | each retry attempt (attempt ≥ 2) emits `EventReset` with `Attempt` before its own events (REQ-MODEL-055), as `docs/architecture/streaming.md` §4 states |

The seeded randomness is not in `core` yet. It is a source the caller
injects (`math/rand/v2` with a fixed seed in tests) and the middleware only
draws from it. If `core` needs a new port for it, the implementer lands that
in a pull request of its own before this WP starts, since `core` is outside
this WP's directories.

## 4. Timeouts

Per-attempt streaming defaults: first-chunk `30s`, idle `60s`, streaming total
`10m` (REQ-MODEL-059), override per call. The first-chunk timer starts at the
start of the attempt and is replaced by the idle timer at the first event
(reset on each subsequent event); the per-attempt total timer covers the
streaming attempt (REQ-MODEL-058). Each fires once on the fake clock and
cancels the attempt with `core.CodeTimeout`, `Retryable: true`, so
REQ-MODEL-048 retries it while attempt budget and the call's total timeout
(REQ-MODEL-053) allow (MODEL-056-01). A timer that fires cancels the attempt,
not the call; whether another attempt follows is decided by the retry rules
(REQ-MODEL-048, REQ-MODEL-050, REQ-MODEL-053).

## 5. Metering and the call log

- Metering records `Usage` and the cost in whole milli-points per attempt that
  reached the provider, failed attempts included (REQ-MODEL-060). The cost
  comes from the descriptor's price formula (WP-L3-06); until that lands, the
  test model's descriptor carries the formula
  `(usage.input_tokens * 1 + usage.output_tokens * 8) / 1000` milli-points. No
  usage reported means zero tokens and zero milli-points (REQ-MODEL-061).
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

## Open questions

1. **Seeded randomness port (`design.md` §3):** `core` currently provides `core.Clock` and `coretest.FakeClock`, but no randomness port. Should `model/middleware` accept a narrow local seed/source option, or should a shared randomness port land in `core` in its own pull request before WP-L3-02 implementation starts?
2. **Author choices of numeric constants (`REQ-MODEL-043`, `REQ-MODEL-051`, `REQ-MODEL-059`):** Default attempt budget (`3`), retry backoff (`1000 ms` base, `2×` factor, `[0, 50%]` jitter), and streaming timeouts (`30s` first-chunk, `60s` idle, `10m` total) are not fixed by `docs/architecture/model-layer.md` §5 or ADR-0006 and are proposed here for reviewer confirmation.
3. **Local JSON repair attempt accounting (`REQ-MODEL-065`, `design.md` §1):** Local repair makes no `Invoker.Start` call (0 model invocations) but consumes 1 attempt from the structured-output call's attempt budget; if all supported model rungs miss the schema on a model with fewer rungs than the budget (e.g., 1 supported rung with default budget 3), local repair runs once on the last supported rung's output and, if it still misses the schema, ends the call after attempt 2 (`MODEL-047-02`).

# Models: catalog and price table (WP-L3-06)

Decisions the implementer must follow in `model/catalog`. The requirements
are REQ-MODEL-066 to REQ-MODEL-077 (`requirements.md`, section "Catalog and
price table"); where this section is silent, the requirements win.
`docs/architecture/model-layer.md` §2 and §7 and ADR-0006 set the rules; every
number or default below that the docs leave open is a choice for the reviewer
to weigh.

## C1. Descriptors by model id

- `model/catalog` holds the descriptors: those of the built-in providers and
  the snapshot, and the descriptors that workers declare to their pools (ADR-0012
  §2, WP-L3-07) once that work lands. The catalog answers by model id
  (REQ-MODEL-066); a lookup of an id it does not hold fails with
  `core.CodeNotFound` (REQ-MODEL-067, MODEL-067-01). Availability of a
  worker-served model is REQ-MODEL-013/REQ-MODEL-034's and is not specified
  here.
- The descriptor is `model.Descriptor` as it stands in `model/model.go`: the
  catalog adds no field to it. `Price` stays the CEL formula over `params`,
  `usage` and `rates`. A worker descriptor and a catalog descriptor with the
  same id must be identical strings (REQ-MODEL-028 compares them).

## C2. The price table

Three layers, most specific first: the tenant's override, the deployment's
override, the snapshot shipped with the binary (REQ-MODEL-070). Layer *n*
answers for a model when it has an entry for it; the first layer that answers
decides, for the whole price of the call.
- A model's price in a layer is either a **set of rate rules** (each with
  rates and an optional `when`, as in `docs/architecture/model-layer.md` §7
  YAML) or a **formula** of its own. A test or a flow that states a price as
  `Given a model "X" priced at "<CEL>" milli-points` (the phrase of
  `cost.feature` and `middleware.feature`) sets the model's formula in every
  layer; the phrase `has the rate rules "input <a> and output <b>" per million
  tokens` sets one unconditional rule.
- The rules are tried in their table order; the first whose `when` holds for
  the invocation applies to all its tokens (MODEL-068-01). A rule without
  `when` always holds (`always` in the scenarios). Prices are per **million**
  tokens: a million input tokens cost `input` milli-points (rate `0.28` =
  0.28 milli-points per thousand tokens), million output tokens cost `output`.
- `when` is CEL over `at` (the start time of the invocation, `core.Clock` at
  its start, recorded with it), `params`, `usage` and `tenant`. `usage` is the
  usage being priced: actual at settlement. The evaluation sees no clock.
- The tenant is taken from the tenant context of the call (constitution
  article 8); the catalog never keys an override by a value in the request
  (REQ-MODEL-071). A call made outside any tenant uses the deployment layer:
  its tenant is the empty tenant, which has no overrides (MODEL-071-02).
- Where no layer answers with a rule that holds, the call fails with
  `core.CodeUnsupported` before any start (REQ-MODEL-072, MODEL-072-01) —
  the same code a model with no rung gets (REQ-MODEL-046), since `core` has no
  code for "no price".

## C3. The formula and the token default

- A model whose price is a set of rules and that declares no formula in its
  descriptor uses the **default token formula** on the chosen rule's rates:
  `input_tokens × rates.input / 1e6 + output_tokens × rates.output / 1e6`
  (REQ-MODEL-073). Cached and reasoning tokens are not in it (REQ-MODEL-074):
  `input_tokens` is the total input usage, cached tokens included, and is
  counted at the input rate; reasoning tokens, already part of
  `output_tokens`, are not added again. A model that declares its own formula
  (image, video, `params.duration_s`, …) uses it with `rates` bound to the
  chosen rule's rates.
- **Rounding (REQ-MODEL-075):** the formula and the rule rates are evaluated
  over exact decimal values and the result is rounded to the nearest whole
  milli-point, halves away from zero (up, since prices are not negative:
  MODEL-075-01, MODEL-075-02). The rounding happens once, on the final
  amount; no intermediate value is rounded. Implementations evaluate in
  `math/big.Rat` or integers scaled by 10^6, never `float64`.

## C4. Estimation for a reservation

- **Counted input tokens (definition, used by REQ-MODEL-076 and the cost
  specs):** for a chat call, the tokens the model's tokenizer gives the
  messages, the tool definitions and local repair input of the request; the
  tokenizer is the model's descriptor's, so the same request always counts
  the same. A call made outside a run (middleware, metering) counts the
  messages and tools of the request the same way.
- **Estimate (REQ-MODEL-076):** the model's formula (default or its own) on
  `params` = the call's parameters, `usage.input_tokens` = the counted input
  tokens, `usage.output_tokens` = the call's output limit (a parameter of
  `params`, `max_tokens` in the scenarios and in the snapshot entries of
  `catalog.feature`), cached and reasoning at 0, and `rates` = the rule
  the `when` chooses with that estimated usage and the recorded start time.
  The estimate is what REQ-MODEL-006 reserves; the budget comparison and the
  `budget_exceeded` failure are REQ-MODEL-007's and not specified here.
- In MODEL-076-01 the provider would use 4000 input tokens on its own, yet
  the estimate counts the request's tokens, not the provider's: the step's
  request carries messages the model's tokenizer gives 1000 tokens, so
  `1000 × 1 + 500 × 8 = 5000`. The implementer writes the messages so the
  count is 1000 under the test tokenizer and notes it in the step.

## C5. Pricing is fixed when the invocation starts

- The layer, the rule and the formula a call uses are chosen when its
  invocation starts, and recorded with the invocation (the start time `at` is
  already recorded with it, `docs/architecture/model-layer.md` §7). The
  reservation and the settlement of that call use them, whatever the price
  table changes meanwhile (REQ-MODEL-077, MODEL-077-01). A retry of the same
  logical call (same client ref) is priced the same.

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
  implementer lands that move in a pull request of its own before this WP.

## Open questions

1. **Cost requirement: REQ-MODEL-006 re-locking (`requirements.md`, approved text).** REQ-MODEL-076 says the reservation uses the estimate of C4 for **every** paid call, while REQ-MODEL-006 says "estimated by the model's price formula". They agree for a model with rules; for a model that declares its own formula (a video model priced on `params.duration_s`), REQ-MODEL-006 read literally leaves the estimate to that formula, which the docs define no usage for; under C4 it gets the same usage (counted input tokens, output limit). Should REQ-MODEL-006 be re-locked to cite REQ-MODEL-076, or does this WP leave it (cost, P1 re-review either way)?
2. **Default formula and the WP-L3-02 fixture (`design.md` §5, MODEL-060-01, MODEL-061-01).** `middleware.feature` and the earlier section's §5 price `sim-chat` with `(usage.input_tokens * 1 + usage.output_tokens * 8) / 1000` milli-points, which at per-million rates of 1 and 8 per token would be 1,000,000,000× the default formula of REQ-MODEL-073 (`input × 1e-6 + output × 8e-6`). The scenarios are approved text; the implementer of WP-L3-02 must state that price as a model formula in the manner of C2 (a fixture), not as rules fed to the default formula. If the owner wants the two reconciled, it is a WP-L3-02 spec change (P1).
3. **Where overrides are configured.** The roadmap and docs name the layers but not who writes deployment and tenant overrides (a config file for the deployment, which API or command for a tenant). This WP only reads them; should the write side be a task of WP-L3-06, or another work package?
4. **Rounding of an exact half (REQ-MODEL-075).** The docs give no rule; half up was chosen (MODEL-075-01) because it never under-charges a fraction of cost. Confirm, or choose half to even.
5. **The estimate's counted input tokens (C4).** The docs say "counted input tokens" without saying by which tokenizer. The model's own tokenizer is proposed; where a descriptor names none, the implementer must pick one and say so.
6. **`not_found` for an unknown model id (REQ-MODEL-067).** `core` already has the code, and ADR-0012 §2 uses the word for the catalog; confirm it over `invalid_argument`.
