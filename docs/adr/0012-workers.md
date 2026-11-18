# ADR-0012 — Workers declare what they serve and pull invocations

**Status:** accepted · **Date:** 2026-11-16

## Context
Work can leave the `kit` process in three ways, and none fits a machine that
owns one capability (an API key, a ComfyUI install, `ffmpeg`) and sits behind
NAT:

| Way | Limit |
| --- | --- |
| Runtime task queues (`StepOptions.Queue`) | Temporal only; the worker holds orchestration credentials. |
| Remote invokers (`InvocationService`) | The platform dials the service, so it must be reachable and registered by hand. |
| Machine-local providers | Run on the machine `kit` runs on; never routed in the cloud profile. |

Such machines are also often not there: a GPU is expensive to keep idle, and
services such as Modal, Cloud Run, Lambda, Fly and RunPod start compute in
seconds to minutes and bill by use. The platform should be able to bring
capacity up when work waits and let it go when there is none, without
becoming a cluster manager.

## Decision
- A **worker** is the same binary, `kit worker`. It loads the same providers
  (HTTP API, command line, chat adapters) from local manifests and has no
  flows and no database.
- **It dials out** to the platform (`basamento.worker.v1`, connect), so it
  works behind NAT, and **joins** one pool with a pool token.
- **It declares** the descriptors of the models and tools its providers
  serve, with labels (`gpu`, `region`) and a capacity. The platform lists
  them in the catalog with locality `worker`.
- **It pulls.** The platform queues invocations for the pool and decides
  their order. A worker asks for work, is leased only what it declared and
  can fit, keeps the lease with heartbeats, and sends events and the result
  back. Output files go to blob storage through scoped URLs (ADR-0013).
- **To a flow it is one more provider.** The platform side implements
  `model.Invoker`, so budget, cancellation, reattach and replay work as for
  any model. The router matches capability, tenant and required labels.
- **Vendor credentials stay on the worker.** The platform never sees them.
- Runtime task queues stay internal to the platform's own processes.

The sections below settle what those sentences leave open.

### 1. A pool is a unit of trust
There are two kinds of pool and no third: a **tenant's** pool, whose workers
see only that tenant's work, and the **platform's** shared pool, run by
whoever runs the deployment. Within a pool the platform believes what a
worker reports (usage, results). Workers of one party serving another
party's work (a marketplace) would need attested usage, payouts and consent,
and an ADR of their own.

A worker joins one pool. A tenant may have several pools.

### 2. A pool remembers what it serves
The descriptors a worker declares are stored with the pool and stay in the
catalog when the worker leaves, with an **availability**: `online` (a worker
that serves it is present), `offline`, or `on_demand` (§12). An
invocation for an offline model waits in the queue; the catalog never
answers `not_found` for a model whose worker is restarting.

Within a pool a model id has one descriptor. A worker that declares a
different descriptor for an id another present worker serves is refused with
`conflict`. A rollout uses a new id, or drains the old workers first.

### 3. The queue is a table, the bus only wakes
Invocations for workers are rows in the SQL store, one per client ref,
leased in a transaction. The bus carries "there is work for pool P", which
wakes waiting lease requests; losing such a message costs a short delay,
never an invocation. Both profiles behave the same, and several platform
replicas can serve one pool.

### 4. Nothing depends on one connection
The worker contract (`basamento.worker.v1`) is a set of short calls, each
naming the worker and the lease it is about:

| Call | What it does |
| --- | --- |
| `Join` | Pool token → a worker id and a short-lived worker credential; the worker sends its protocol version, descriptors, labels and capacity; the platform answers with the heartbeat interval. An incompatible version is refused with a message that says which side to upgrade. |
| `Lease` | A long poll: "I have this much free, these models are warm, I will run for this much longer". Returns invocations, or nothing after a while. |
| `Starting`, `Started` | Sent before the worker calls its provider, and after, with the provider's handle (§7). |
| `Events` | Partial output, in batches. |
| `Finish` | The result. Files went to blob storage through scoped URLs before it (ADR-0013). |
| `Release` | Gives a lease back: unused, or "not before" a time (the vendor's rate limit). |
| `Heartbeat` | Keeps the worker present and its leases alive. The answer carries what the platform wants: cancel these invocations, drain, new expiry. |
| `Leave` | A clean exit. |

A worker that loses its network keeps working and reports when it is back,
as long as its leases have not expired. The platform times every lease on
its own clock, and after a restart of its own it extends every lease by one
period before letting any expire, so a platform outage does not turn into
lost work.

### 5. The platform decides the order
A worker never chooses among invocations; it says what it can take and the
platform picks, in this order:

1. **Class.** `interactive`, `normal` (the default) or `batch`, set by the
   run. An invocation that has waited long enough moves up one class, so
   nothing starves.
2. **Fairness**, in the shared pool: the tenant with the least work running
   relative to its weight goes first, and no tenant runs more than its
   concurrency limit. Weight and limit are settings of the tenant.
3. **Age:** oldest first.

Among invocations that tie, one whose model is already loaded on the asking
worker is preferred. Once an invocation has aged into the top class, smaller
ones behind it are not handed to a worker it is waiting for. Running work is
never preempted: it has already cost money.

When a tenant's own pool and another provider serve the same model id, the
tenant's pool is used.

### 6. Capacity, and saying "busy"
A worker has a **capacity** in units, and each model it declares costs a
number of units (1 unless its manifest says more). Every `Lease` call says
how many units are free, so a busy worker is simply one that does not ask,
or asks for less. A lease is given only if the invocation fits the free
units and its time limit fits the time the worker says it has left.

| State | Means | Entered by |
| --- | --- | --- |
| `active` | Takes work. | joining |
| `draining` | Finishes what it holds, takes no more, then leaves. | `SIGTERM`, the operator, or the platform's order |
| `paused` | Takes no work but stays; resumes later. | the operator (the owner wants the machine back), or the platform's order |

### 7. A worker that is lost
A model's manifest says whether an interrupted invocation may be started
again: **`restart: safe`** (a local `ffmpeg`, a local GPU model: only time
is lost) or not, which is the default and always the case for tools and paid
vendor calls. When a lease expires:

| The worker had | Model is | The platform |
| --- | --- | --- |
| not sent `Starting` | either | gives the invocation to another worker: nothing was started. |
| sent `Starting` | restart-safe | gives it to another worker; followers get a `reset` event. |
| sent `Starting` | not restart-safe | holds it **in doubt** for a grace period, in which only the same worker can take it back. After that the invocation **fails with `worker_lost`** and its reservation is settled as spent. It is never started again on the platform's own decision; the flow's retry or fallback policy, written by its author, decides whether to pay again. |

- A worker keeps a small **journal** in its data directory: its leases, the
  provider handles and results not yet delivered. With it a worker that
  crashed, restarted or lost its network resumes its invocations and
  delivers what it has. It is a file, not a database.
- Every lease carries an **attempt number**. If two attempts of one
  invocation are ever alive, the first to finish wins and the other is told
  to cancel. A report for an invocation that has already ended is refused
  with `lease_lost`, and the worker cancels at its provider.
- An invocation is leased at most three times; when the third lease
  expires it fails with `worker_lost`. One that crashes every worker does
  not circulate forever.
- **Cancellation** removes a queued invocation, or reaches a leased one in
  the next heartbeat answer; the worker cancels at its provider and reports
  the final state, which settles the reservation as for any model.
- An invocation not leased within its **queue timeout** fails with
  `unavailable`, with the reason (no worker online, all busy, launch failed).

To a flow nothing changes: the platform side is still a `model.Invoker`,
`Start` is idempotent per client ref because the queue has one row per
client ref, and `Lookup` is always answered from the queue.

### 8. Spilling over to another provider
The platform does not move work from a busy or offline worker to a paid
provider on its own. A tenant may turn **spill-over** on, off by default,
with a waiting time: an invocation still queued after it, for a model
another provider of that tenant also serves, is withdrawn from the queue
and started there, with its reservation re-estimated at that provider's
price. The catalog shows queue depth and expected wait per model, so a
flow's own fallback policy can decide earlier.

### 9. Prices
A worker's descriptor carries a price formula like any other, and the price
table overrides it per deployment and per tenant. The reservation is made
before the invocation is queued and held while it waits. Time is billed
from `Started` to `Finish`; waiting and a worker's idle time are not.

### 10. Security
- A pool token only lets a machine **join**. Joining yields a worker id and
  a short-lived credential that is renewed by heartbeats and can be revoked
  per worker. Pool tokens are stored hashed, listed and revocable. Tokens of
  the shared pool are issued by deployment administrators only.
- What a worker receives holds no credential and no more about the tenant
  than the work needs. Input and output URLs are scoped to the one
  invocation and renewed with its lease.
- A worker validates parameters against its own descriptor again, runs each
  invocation in a working directory of its own and removes it afterwards.
- Joining, leaving, removal and refused joins write audit events.

### 11. Operating it
- Metrics per pool, model and class: queue depth, waiting time, lease age,
  workers by state, units in use. The same queue depth drives the scaler (§12)
  and any external autoscaler.
- The API lists workers with their state and what they hold, and lets an
  operator pause, drain or remove one.
- Each lease carries the trace context, so a worker's spans join the run's.

### 12. Workers started on demand

Those services come in two shapes:

| Shape | Examples | Fits |
| --- | --- | --- |
| **Request-driven**: runs while it answers a request, scales itself | Cloud Run services, Lambda behind a function URL | A **remote invoker** (ADR-0006): the platform dials it and the service's own autoscaling does the rest. Nothing new is needed. |
| **Run to completion**: started through an API, runs until it exits | Modal functions, Cloud Run Jobs, Kubernetes Jobs, Fly Machines, RunPod, a spot VM | A **worker**: it starts, joins, pulls until idle, exits. This section. |

#### A worker template
A pool may hold **worker templates**. A template describes workers that do
not exist yet:

| Field | Meaning |
| --- | --- |
| Descriptors, labels, capacity | What one such worker serves, exported from the image with `kit worker describe`. The catalog lists these models as `on_demand`. A worker started from the template must declare exactly this, or its join is refused: an image that drifted is caught at once. |
| `min`, `max` | Workers to keep even when idle (0 allows scaling to zero), and the most to run at once. |
| `cold_start` | How long a worker normally takes from launch to joining. Shown in the catalog. The pool's queue timeout must be at least twice this, checked when the template is saved. |
| `idle_exit`, `max_lifetime` | Given to the worker: leave after this long without work; drain and leave after this long in total (15 minutes for a Lambda-sized runtime). |
| Launcher | Which launcher starts it and with what settings; credentials by `cred://` reference. |

#### A launcher port
`platform.Launcher` has one operation: start *n* workers from a template,
each with a join token, under an idempotency key. It reports whether the
request was accepted, nothing more: **a worker joining is the only proof it
started**. The first version ships two launchers:

| Launcher | Does | Reaches |
| --- | --- | --- |
| `http` | Sends a templated HTTP request with a credential reference, as the generic HTTP model provider does, and expects a 2xx. Subject to the same egress rules as that provider. | Modal, Fly Machines, RunPod, a function URL, a CI dispatch, or a small relay the operator writes. |
| `process` | Starts `kit worker` (or a command that does) on the platform's machine. Deployment administrators only, never configurable by a tenant. | The embedded profile: a laptop that starts a local generation server only when it is needed. |

Planned, not in the first version, each as a launcher of its own with that
vendor's authentication: **Cloud Run Jobs**, **Kubernetes Jobs**, and **AWS**
(ECS tasks; Lambda is request-driven and already fits as a remote invoker).
Until then Cloud Run Jobs and AWS are reached through a relay behind the
`http` launcher, and a Kubernetes cluster scales workers with KEDA on the
queue-depth metric (§11), with no launcher at all.

#### A join token per launch
The launcher hands each worker a token that can be used once, expires at the
cold-start deadline and is bound to its template. The pool token never
leaves the platform. Vendor credentials the worker needs come from that
cloud's own secret store, so the platform still never sees them.

#### The scaler
One scaler runs per deployment (a lease in the KV store picks the replica).
For each template, on every change of the queue and on a timer:

1. **Demand** is the units of queued invocations this template could serve
   that neither the free units of present workers nor launches still pending
   will cover. A tenant's queued work counts only up to its concurrency
   limit, so one tenant cannot make the shared pool launch without bound.
2. **Wanted** workers are demand divided by a worker's capacity, rounded up,
   kept between `min` and `max` together with those present and pending.
3. It **launches** the difference, within a launch rate and a cooldown.

A pending launch that has not joined by the cold-start deadline counts as
failed. Failures back off exponentially; after three in a row the template
is `degraded`: the scaler tries one worker per back-off period, and queued
invocations fail with `unavailable` and the launcher's last error at their
queue timeout. Launches, failures and state changes are audited and
measured.

#### Scaling down is the worker's own exit
The platform never kills a worker, because it cannot know what a kill would
cost. A worker leaves by itself when idle for `idle_exit`, after
`max_lifetime`, or after `--max-invocations`. The platform may order a
drain (§6). When the service takes the machine away (a spot
preemption), `SIGTERM` starts a drain, the worker releases the leases it has
not started, and §7 covers the rest.

#### What it costs, and who may set it up
- `max` bounds each template and a deployment-wide cap bounds them all. The
  scaler never exceeds either, whatever the queue holds.
- Tenants are billed for execution time (§9). Cold starts and idle
  time are the pool owner's cost: the tenant's own bill for its own pool,
  the deployment's for the shared one.
- A tenant's administrators define templates for the tenant's pools, with
  the tenant's own cloud credentials. Deployment administrators define them
  for the shared pool.

#### Not an orchestrator
There is no desired-state reconciliation beyond keeping `min`, no placement,
no restarting of unhealthy workers, no image management and no node
inventory. Leases already cover failure, and the services that run the
workers already do the rest. The scaler only answers one question: how many
more workers should be asked for now.

## Consequences
- One worker serves only LLM APIs, another only ComfyUI, another only
  `ffmpeg`; each takes the work it can do, on any machine, in either profile.
- A machine-local model becomes usable from the SaaS when its owner runs a
  worker for it; `machine_local` itself still never routes in the cloud.
- The browser runner keeps its own contract (ADR-0009) and may later connect
  the same way.
- `WP-L3-07` implements §1 to §11 and `WP-L3-08` implements §12 (templates,
  the `Launcher` port with its conformance suite and two launchers, the
  scaler, `kit worker describe`, the exit flags). The worker package becomes
  a layer of its own, `model/worker: [core, platform, expr, model, tool]`,
  added to `layering.yaml` by `WP-L3-07`. Each native launcher (Cloud Run
  Jobs, Kubernetes Jobs, AWS) is a later work package and adds its vendor's
  client and authentication to the binary.
- Contract: error codes `worker_lost` and `lease_lost`; a class on a run's
  start options; `restart` and `units` in provider manifests; availability
  and cold-start time in the catalog.
- P0 behavior in `specs/models`: an invocation that may have started on a
  lost worker is never started again unless its model is restart-safe; a
  worker never receives another tenant's work; a stale attempt cannot
  overwrite a result.
- `kit worker` needs a writable data directory for its journal.
- Interactive use of a model that scales to zero pays the cold start on its
  first call; `min: 1` is the remedy and is a cost decision.
- Not decided here: preemption of running work, keeping a conversation on
  one worker, and third-party pools.
