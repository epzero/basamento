# Models: one invocation contract

Status: **Partly implemented** (WP-L3-01, WP-L3-02, WP-L3-03, WP-L3-06);
providers (WP-L3-04) and workers in WP-L3-07 and WP-L3-08 are planned.
Design: ADR-0006, ADR-0012,
`docs/architecture/model-layer.md`.

Every model call is an **invocation**, whatever the model does (chat,
embeddings, image, video, audio) and however its vendor answers (one streamed
response, submit-and-poll, webhook, WebSocket, a local command line). Callers
can block for the result or follow events; both use the same contract.

### REQ-MODEL-001 — One contract for every model
The model layer shall expose every model through one invocation contract with start, events, result, cancel and lookup, whatever the vendor's protocol.

### REQ-MODEL-002 — Blocking calls
When a caller invokes a model without following its events, the model layer shall return the same result the invocation ends with.

### REQ-MODEL-003 — Partial output as ordered events
While an invocation produces partial output, the model layer shall publish it as events in order, and the result shall contain the complete output.

### REQ-MODEL-004 — Stable client ref for a logical invocation
The model node shall use the same client ref across retries of one logical invocation, including a retry after a transient rejection or an unacknowledged start.

### REQ-MODEL-005 — Ambiguous starts are never repeated blindly
If the outcome of a start is unknown, then the model node shall look the invocation up by client ref before starting it again; and when the lookup finds no invocation or the provider cannot answer, the model node shall open a gate instead of starting it again.

### REQ-MODEL-006 — Reserve, then settle
Before starting a paid invocation, the system shall reserve the cost estimated by the model's price formula; when the invocation ends, the system shall charge the actual cost or release the reservation.

### REQ-MODEL-007 — Budget
If a reservation would exceed the run's remaining budget, then the system shall not start the invocation and shall fail the node with code `budget_exceeded`.

### REQ-MODEL-008 — Ingest with provenance
When an invocation succeeds with output files, the system shall store each one in content-addressed blob storage with its model, parameter hash, seed and cost.

### REQ-MODEL-009 — Cancellation
When a run with an invocation in progress is cancelled, the system shall cancel the invocation at the provider and settle the reservation according to the provider's final state.

### REQ-MODEL-010 — Parameter validation
If an invocation's parameters do not satisfy the model's parameter schema, then the system shall fail the node with code `invalid_argument` before starting the invocation.

### REQ-MODEL-011 — Machine-local models
While the profile is `cloud`, the model router shall not select a model whose locality is `machine_local`.

## Retention

A registry is the in-process memory of one provider's invocations: it answers
a client ref with the invocation, its event log from a cursor and its result.
It cannot grow without bound, so it forgets ended invocations within limits its
constructor sets, and nothing else.

The window bounds only what the registry remembers. The model node derives the
client ref from the run, the node path, the visit and the input
(`docs/architecture/model-layer.md` §4), and each attempt's ref from that call
ref and the attempt number (REQ-MODEL-054); it calls `Begin` inside one activity
attempt, and records the result durably before that attempt acknowledges, so a
late retry replays the recorded result and never reaches `Begin`.

### REQ-MODEL-088 — Ended invocations are forgotten within a window and a bound
When an invocation ends, the registry shall keep it for its retention window after its end and no longer, and, while it holds more ended invocations than its ended bound, shall evict ended invocations from the oldest first; it shall never evict a running invocation, and shall answer for an evicted client ref exactly as for one it never held: lookup finds no invocation, events, result and cancel answer `not_found`, and a start begins a new invocation.

## Workers

A worker serves the models it declared to the pool it joined (ADR-0012). The platform keeps the queue and decides what each worker gets.

### REQ-MODEL-012 — Work stays in its pool
The system shall lease an invocation only to a worker of the invocation's pool that declared the invocation's model.

### REQ-MODEL-013 — A pool keeps what it serves
While no present worker of a pool serves a model that a worker of the pool declared, the catalog shall list the model with availability `offline`.

### REQ-MODEL-014 — Queue timeout
If an invocation is not leased within its queue timeout, then the system shall fail it with code `unavailable` and release its reservation.

### REQ-MODEL-015 — Order by class, then age
When a worker asks for work, the system shall lease the waiting invocations the worker can serve in order of class (`interactive`, then `normal`, then `batch`) and, within a class, oldest first.

### REQ-MODEL-016 — Ageing
While an invocation has waited longer than its pool's ageing period, the system shall order it as one class higher.

### REQ-MODEL-017 — Fairness in the shared pool
While invocations of several tenants of the same class wait in the shared pool, the system shall lease first an invocation of the tenant with the least leased work relative to its weight.

### REQ-MODEL-018 — Tenant concurrency limit
While a tenant has as many invocations leased in the shared pool as its concurrency limit, the system shall not lease another of that tenant's invocations.

### REQ-MODEL-019 — Capacity
The system shall lease to a worker only invocations whose units fit in the free units the worker reported with its request.

### REQ-MODEL-020 — Draining and paused workers
While a worker is draining or paused, the system shall lease it no invocation.

### REQ-MODEL-021 — Work that may be delivered again
When a lease expires for an invocation that its worker had not reported starting, or whose model is restart-safe, the system shall make the invocation available to another worker with the same client ref.

### REQ-MODEL-022 — A lost worker never causes a second start
If a lease expires after its worker reported starting an invocation whose model is not restart-safe, and the worker does not resume the invocation within the grace period, then the system shall fail the invocation with code `worker_lost`, charge its reservation, and not lease it again.

### REQ-MODEL-023 — A worker that comes back resumes
When a worker resumes an in-doubt invocation within the grace period, the system shall accept its events and its result.

### REQ-MODEL-024 — Stale reports
If a worker reports on an invocation that has already ended, then the system shall refuse the report with code `lease_lost` and keep the recorded result.

### REQ-MODEL-025 — Delivery limit
If an invocation's third lease expires, then the system shall fail the invocation with code `worker_lost` and not lease it again.

### REQ-MODEL-026 — Cancellation reaches the worker
When a run is cancelled while its invocation is leased to a worker, the system shall tell the worker to cancel the invocation in its next heartbeat answer.

### REQ-MODEL-027 — Leases survive a platform restart
When the platform starts, the system shall extend every lease by one lease period before it lets any lease expire.

### REQ-MODEL-028 — One descriptor per model in a pool
If a worker joins declaring a descriptor that differs from the one a present worker of its pool declared for the same model id, then the system shall refuse the join with code `conflict`.

### REQ-MODEL-029 — Joining a pool
If a worker joins with a pool token that is unknown or revoked, then the system shall refuse the join with code `unauthenticated` and record an audit event.

### REQ-MODEL-030 — Removing a worker
When an operator removes a worker, the system shall refuse that worker's further calls with code `unauthenticated`.

### REQ-MODEL-031 — The tenant's own pool first
While a tenant's own pool and another provider serve the same model id, the model router shall send that tenant's invocations of the model to the tenant's pool.

### REQ-MODEL-032 — No spill-over unless the tenant turned it on
While a tenant's spill-over setting is off, the system shall not start on another provider an invocation queued for the tenant's pool.

### REQ-MODEL-033 — Spill-over
Where a tenant has turned spill-over on, when an invocation has waited in the tenant's pool for the tenant's spill-over time and another provider serves its model, the system shall withdraw it from the queue, reserve the cost estimated at that provider's price, and start it there with the same client ref.

## Workers on demand

A pool can hold worker templates; a scaler asks a launcher for workers when
work waits (ADR-0012 §12).

### REQ-MODEL-034 — Models of a template are listed
While a pool has a worker template and no present worker serves the template's models, the catalog shall list those models with availability `on_demand` and the template's cold-start time.

### REQ-MODEL-035 — Launching for waiting work
When invocations wait that a worker template could serve and that neither the free units of present workers nor pending launches cover, the scaler shall ask the template's launcher for the number of workers whose capacity covers them.

### REQ-MODEL-036 — Launch limits
The scaler shall not ask for a worker that would make a template's present and pending workers exceed its maximum, or the deployment's present and pending workers exceed the deployment's cap.

### REQ-MODEL-037 — One tenant cannot inflate demand
While the scaler computes the demand on a template of the shared pool, the scaler shall count each tenant's waiting invocations only up to that tenant's concurrency limit.

### REQ-MODEL-038 — Minimum workers
While a worker template's present and pending workers are fewer than its minimum, the scaler shall ask the launcher for the difference.

### REQ-MODEL-039 — Launch join tokens
The system shall give each launched worker a join token that admits one join to the template's pool and expires at the launch's cold-start deadline.

### REQ-MODEL-040 — A launched worker matches its template
If a worker joins with a launch join token and declares descriptors, labels or capacity that differ from its template, then the system shall refuse the join with code `conflict`.

### REQ-MODEL-041 — Failed launches
If three launches of a worker template in a row do not join by their cold-start deadlines, then the system shall mark the template `degraded` and fail the invocations waiting for it with code `unavailable` and the launcher's last error at their queue timeouts.

### REQ-MODEL-042 — Idle workers leave
When a worker started with an idle-exit time has held no lease for that time, the worker shall leave its pool and exit.

## Structured output and middleware

Chat calls go through middleware in one fixed order (call log ∘ recording ∘
fallback ∘ metering ∘ structured ∘ retry ∘ timeouts) and structured output climbs one
ladder, native schema → JSON mode → forced tool call → local JSON repair, on
one attempt budget (`docs/architecture/model-layer.md` §5, ADR-0006).

### REQ-MODEL-043 — One attempt budget for a chat call
The model layer shall count every attempt of one chat call, across retries and structured-output ladder rungs, against one attempt budget of 3 attempts unless the call sets another.

### REQ-MODEL-044 — Which rung is tried
When a structured-output call runs, the model layer shall try the supported rungs of the ladder in order, and shall advance to the next supported rung only after an attempt whose output does not satisfy the output schema.

### REQ-MODEL-045 — One rung per attempt
The model layer shall ask one model call of a structured-output call to produce its output on at most one rung of the ladder.

### REQ-MODEL-046 — A model that supports no rung is not called
If a structured-output call targets a model that supports no rung of the structured-output ladder, then the model layer shall fail the call with code `unsupported` and shall start no invocation.

### REQ-MODEL-047 — Budget or ladder spent
If a structured-output attempt does not satisfy the output schema and either the attempt budget is spent or no further supported rung and no local JSON repair remains, then the model layer shall fail the call with code `invalid_argument` and report that no attempt satisfied the output schema.

### REQ-MODEL-048 — Which failures are retried
When an attempt fails with a retryable error and the attempt budget remains, the retry middleware shall make the next attempt of the call on the same rung as the failed attempt.

### REQ-MODEL-049 — Which failures are not retried
When an attempt fails with an error that is not retryable, the retry middleware shall fail the call with that error and make no further attempt.

### REQ-MODEL-050 — No retry after output has started
If an attempt has produced an event before it fails, then the retry middleware shall fail the call with that attempt's error and make no further attempt.

### REQ-MODEL-051 — Backoff with seeded jitter
When the retry middleware waits before a retry of a call, it shall wait a base wait that starts at 1 second and doubles on each subsequent retry of the call, plus up to 50% of that base wait drawn from the call's seeded randomness.

### REQ-MODEL-052 — Retry-After wins
When a provider answers a failed attempt with a Retry-After time longer than the computed wait, the retry middleware shall wait for the Retry-After time instead.

### REQ-MODEL-053 — A call total timeout ends retrying
If the elapsed time of a call from the start of its first attempt reaches, or the required wait before a next attempt would reach or exceed, the call's call total timeout before the call succeeds, then the model layer shall fail the call with code `timeout` without starting another attempt.

### REQ-MODEL-054 — A client ref per attempt
The model layer shall derive a deterministic client ref for each attempt of one logical call from the call's client ref and the attempt number, and shall start an attempt with that client ref, except that it shall reuse the client ref of an attempt whose start was not acknowledged when it retries that start.

### REQ-MODEL-055 — A retried attempt resets partial output
When the model layer makes a retry attempt, it shall emit a `reset` event carrying the attempt number before the events of that attempt.

### REQ-MODEL-056 — First-chunk timeout
If a streaming attempt produces no event within its first-chunk timeout, then the timeout middleware shall cancel the attempt with code `timeout` and a retryable error.

### REQ-MODEL-057 — Idle timeout
If a streaming attempt in progress produces no event for its idle timeout, then the timeout middleware shall cancel the attempt with code `timeout` and a retryable error.

### REQ-MODEL-058 — Attempt total timeout for streaming
If a streaming attempt runs for its attempt total timeout, then the timeout middleware shall cancel the attempt with code `timeout` and a retryable error.

### REQ-MODEL-059 — Default timeouts
The model layer shall time out a streaming attempt after 30 seconds without a first event, 60 seconds without one in progress, and 10 minutes in total, and shall apply no call total timeout, unless the call sets another first-chunk timeout, idle timeout, attempt total timeout or call total timeout.

### REQ-MODEL-060 — Metering per attempt
The metering middleware shall record the usage and the cost of every attempt of a call that reaches the provider, including each failed attempt, in milli-points.

### REQ-MODEL-061 — An attempt the provider does not bill costs nothing
If a provider reports no usage for an attempt, then the metering middleware shall record that attempt with zero tokens and zero milli-points.

### REQ-MODEL-062 — One entry per attempt in the call log
The call log middleware shall record one entry per attempt of a call, carrying the attempt's client ref, the credential reference if the attempt used a credential, the attempt number, the model, the rung of the ladder it used or `none` when it was not structured, its start time and its outcome.

### REQ-MODEL-063 — The call log carries no secrets or content
The call log shall record no credential, no message text and no model output.

### REQ-MODEL-064 — Middleware order
The model layer shall apply the middleware to a chat call in one order, outermost first: call log, recording, fallback, metering, structured, retry, timeouts.

### REQ-MODEL-065 — Local JSON repair after the last supported rung
If the attempt on the last rung the model supports does not satisfy the output schema and attempt budget remains, then the model layer shall run local JSON repair on that attempt's output as the next attempt without starting a model invocation. Local JSON repair shall strip the surrounding prose or code fences around a JSON value and repair malformed JSON syntax so that the value parses, and shall not add, remove or change any field.

### REQ-MODEL-066 — A spent budget ends the retries
If an attempt fails with a retryable error and the attempt budget is spent, then the retry middleware shall fail the call with that error and make no further attempt.

## Catalog and price table

The catalog answers which descriptor a model id stands for and at which price
a call of it is charged. Prices are the price table's: rate rules per model,
overridden per deployment and per tenant (`docs/architecture/model-layer.md`
§7). Reservations, their budget check and settlement consume what this section
supplies and are specified in `cost.feature`.

### REQ-MODEL-067 — A descriptor by model id
When the catalog looks up a model id it holds, the catalog shall return that model's descriptor.

### REQ-MODEL-068 — An unknown model id
If the catalog looks up a model id it does not hold, then the catalog shall fail with code `not_found`.

### REQ-MODEL-069 — A rate rule applies by its condition
While a model's rate rule holds for an invocation, the price table shall price that invocation at the rule's rates.

### REQ-MODEL-070 — The first holding rate rule wins
When several of a model's rate rules hold for an invocation, the price table shall price it at the first of them in the table's order.

### REQ-MODEL-071 — Prices come from the most specific layer
When a call of a model is priced in a tenant that has a price for that model, the price table shall take the price from the tenant's override; otherwise, when the deployment has one, from the deployment's override; and otherwise from the snapshot shipped with the binary.

### REQ-MODEL-072 — Tenant prices stay in their tenant
The price table shall never apply a tenant's override to a call outside that tenant.

### REQ-MODEL-073 — A model without a holding price fails pricing
If no layer of the price table has an entry for a model, or the chosen layer's price has no rate rule that holds for a call, then the price table shall fail pricing the call with code `unsupported`.

### REQ-MODEL-074 — The default token formula
Where a model declares no price formula of its own, the model layer shall price its call with the chosen rates' per-million-token rates, where a million tokens cost as many milli-points as that rate.

### REQ-MODEL-075 — The default formula prices cache reads at the cached rate
Where the chosen rate rule defines a cached rate, the default token formula shall price the call's non-cached input tokens at the input rate, its cached tokens at the cached rate, and its output tokens at the output rate; the cached tokens are neither left at the input rate nor counted in addition to the input tokens.

### REQ-MODEL-076 — Whole milli-points, rounded half up
The price table and the price formulas shall return a whole number of milli-points, rounding half up when the exact amount falls between two whole milli-points.

### REQ-MODEL-077 — The reservation estimate
When the price table estimates the cost of a call before it starts, the estimate shall be the model's price formula on the call's parameters, its counted input tokens and its output limit as usage.

### REQ-MODEL-078 — Prices do not change what a started call is charged
When a price changes after a call has started, the price table shall price that call's settled usage using the price entry and start time that applied when the call started.

### REQ-MODEL-079 — Without a cached rate, cached tokens cost the input rate
Where the chosen rate rule does not define a cached rate, the default token formula shall price every input token of the call, cached ones included, at the input rate.

### REQ-MODEL-080 — A cached rate of zero is a rate, not a missing one
While the chosen rate rule defines a cached rate of zero, the default token formula shall price the call's cached tokens at zero milli-points.

### REQ-MODEL-081 — Reasoning tokens are never counted a second time
The default token formula shall count every reasoning token of the call exactly once, at the output rate.

### REQ-MODEL-082 — Usage with more cached than input tokens fails pricing
If a call's usage reports more cached tokens than input tokens, then the price table shall fail pricing the call with code `invalid_argument`.

### REQ-MODEL-083 — A rate condition that errors fails pricing
If a rate rule's `when` condition fails to evaluate or yields a value that is not a boolean, then the price table shall fail pricing the call with code `invalid_argument` and shall not try a later rate rule.

### REQ-MODEL-084 — Rates are never negative
When any layer of the price table, or the shipped snapshot as it loads, sets a rate below zero, the price table shall refuse it with code `invalid_argument`, naming the model id and the rate key.

### REQ-MODEL-085 — Usage counts are never negative
If a call's usage reports a negative input, output, cached or reasoning token count, then the price table shall fail pricing the call with code `invalid_argument` before it evaluates any formula, naming the model id and the offending count.

### REQ-MODEL-086 — A priced amount is never negative
If a price formula returns an amount below zero, then the price table shall fail pricing the call with code `invalid_argument`; an amount of zero shall be a valid price.

### REQ-MODEL-087 — A model's own formula returns whole milli-points
If a model's own formula returns a floating-point amount that is not an exact integer of magnitude at most 2^53, then the price table shall fail pricing the call with code `invalid_argument`, naming the model id.
