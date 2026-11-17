# Models: one invocation contract

Status: **Planned** (WP-L3-01, WP-L3-03, WP-L3-04; workers in WP-L3-07 and
WP-L3-08). Design: ADR-0006, ADR-0012,
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

### REQ-MODEL-004 — Stable client ref
The model node shall start every attempt of one logical invocation with the same client ref.

### REQ-MODEL-005 — Ambiguous starts are never repeated blindly
If the outcome of a start is unknown, then the model node shall look the invocation up by client ref before starting it again, and if the provider cannot answer, shall open a gate instead of starting it again.

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
fallback ∘ metering ∘ retry ∘ timeouts) and structured output climbs one
ladder, native schema → JSON mode → forced tool call → local JSON repair, on
one attempt budget (`docs/architecture/model-layer.md` §5, ADR-0006).

### REQ-MODEL-043 — One attempt budget for structured output
The model layer shall count every attempt of one structured-output call against one attempt budget, and the budget shall be 3 attempts unless the call sets another.

### REQ-MODEL-044 — Which rung is tried
The model layer shall try the first rung of the ladder the model supports, and after an attempt that does not satisfy the output schema, shall try the next rung of the ladder the model supports.

### REQ-MODEL-045 — One rung per attempt
The model layer shall ask one model call of a structured-output call to produce its output on at most one rung of the ladder.

### REQ-MODEL-046 — A model that supports no rung is not called
If a model supports no rung of the structured-output ladder, then the model layer shall fail the call with code `unsupported` and shall start no invocation.

### REQ-MODEL-047 — Budget spent
If a structured-output attempt does not satisfy the output schema and the attempt budget is spent, then the model layer shall fail the call with code `invalid_argument` and report that no attempt satisfied the output schema.

### REQ-MODEL-048 — Which failures are retried
When an attempt fails with a retryable error, the retry middleware shall make the next attempt of the call.

### REQ-MODEL-049 — Which failures are not retried
When an attempt fails with an error that is not retryable, the retry middleware shall fail the call with that error and make no further attempt.

### REQ-MODEL-050 — No retry after output has started
If an attempt has produced an event before it fails, then the retry middleware shall fail the call with that attempt's error and make no further attempt.

### REQ-MODEL-051 — Backoff with seeded jitter
When the retry middleware waits before an attempt, it shall wait double the previous wait, counting from a 1-second base, plus up to 50% of that wait drawn from the call's seeded randomness.

### REQ-MODEL-052 — Retry-After wins
When a provider answers a failed attempt with a Retry-After time longer than the computed wait, the retry middleware shall wait for the Retry-After time instead.

### REQ-MODEL-053 — A total timeout ends retrying
If the time from the first attempt of a structured-output or retrying call reaches its total timeout before an attempt satisfies it, then the model layer shall fail the call with code `timeout`.

### REQ-MODEL-054 — Attempts share one client ref
The model layer shall start every attempt of one logical call with the same client ref.

### REQ-MODEL-055 — A retried attempt resets partial output
When the model layer makes a new attempt, it shall emit a `reset` event carrying the attempt number before the events of that attempt.

### REQ-MODEL-056 — First-chunk timeout
If a streaming attempt produces no event within its first-chunk timeout, then the timeout middleware shall cancel the attempt with code `timeout` and a retryable error.

### REQ-MODEL-057 — Idle timeout
If a streaming attempt in progress produces no event for its idle timeout, then the timeout middleware shall cancel the attempt with code `timeout` and a retryable error.

### REQ-MODEL-058 — Total timeout for streaming
If a streaming attempt runs for its total timeout, then the timeout middleware shall cancel the attempt with code `timeout` and a retryable error.

### REQ-MODEL-059 — Default timeouts
The model layer shall time out a streaming attempt after 30 seconds without a first event, 60 seconds without one in progress, and 10 minutes in total, unless the call sets other timeouts.

### REQ-MODEL-060 — Metering per attempt
The metering middleware shall record the usage and the cost of every attempt of a call that reaches the provider, including each failed attempt, in milli-points.

### REQ-MODEL-061 — An attempt the provider does not bill costs nothing
If a provider reports no usage for an attempt, then the metering middleware shall record that attempt with zero tokens and zero milli-points.

### REQ-MODEL-062 — One entry per attempt in the call log
The call log middleware shall record one entry per attempt of a call, carrying the call's client ref, the attempt number, the model, the rung of the ladder it used, its start time and its outcome.

### REQ-MODEL-063 — The call log carries no secrets or content
The call log shall record no credential, no message text and no model output.

### REQ-MODEL-064 — Middleware order
The model layer shall apply the middleware to a chat call in one order, outermost first: call log, recording, fallback, metering, retry, timeouts.
