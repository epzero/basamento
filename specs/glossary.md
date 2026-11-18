# Glossary

Use these terms in requirements, scenarios and step phrases. Add a term here in
the same change that first uses it.

| Term | Meaning |
| --- | --- |
| **Flow** | A versioned definition (IR) of nodes and edges, stored as data and identified by its content hash. |
| **Run** | One execution of a flow with an input. Has a run id, a tenant and a state: running, completed, failed, cancelled, terminated. |
| **Node** | One step of a flow: `set`, `task`, `model`, `agent`, `tool`, `map`, `parallel`, `subflow`, `gate`, `wait`. |
| **Edge endpoint** | The two ends of an edge: a node, or one of the reserved endpoints `START` (the flow's entry, only an edge source) and `FAIL` (its failure exit, only an edge target). `START` and `FAIL` are not nodes. |
| **Flow host** | The flow code that drives a flow: it feeds the flow machine events and turns its commands into durable calls. The same host runs on every runtime. |
| **Node handler** | The flow code of one effectful node kind (`model`, `agent`), registered with the flow host. Its side effects are steps. |
| **Step** | A side-effecting unit executed by a runtime (a Temporal activity on the temporal runtime). Retried; its result is recorded. |
| **Gate** | A node that waits for a human or external decision; its payload is validated by schema and CEL. |
| **Heartbeat** | The periodic signal a long or streaming step sends while it works. Cancellation reaches the step through it. |
| **Conformance flow** | A golden flow with a recorded final state and durable events that every runtime must reproduce. |
| **Runtime** | An implementation of the durable contract: `local` (in-process, not durable) or `temporal`. |
| **Profile** | Deployment shape: `embedded` (one process, single user) or `cloud` (SaaS services). |
| **Local runtime** | The `local` runtime: runs a flow in-process on goroutines with a journal held in memory only. Not durable: a run does not survive its process (`REQ-RT-005`, `REQ-RT-006`). |
| **Child run** | A run started by a parent run for a child flow under the child run id the parent chooses, in the parent's tenant. When the parent ends, an in-flight child run follows its close policy: `terminate` moves it to `terminated`, `request_cancel` cancels it to `cancelled`, and `abandon` leaves it `running` (`REQ-RT-014`, `REQ-RT-015`). |
| **Durable event** | A run event persisted in order (`seq`) and replayable from any cursor. |
| **Delta** | An ephemeral increment of a message (text, audio, progress), ordered by `dseq`, kept while its message is in flight. |
| **Message** | A model output inside a run, streamed as deltas and completed as a durable event. |
| **Cursor** | What a client sends to resume a stream: last durable `seq` and last `dseq` per open message. |
| **Snapshot** | The text of an in-flight message so far, sent on reconnect before further deltas. |
| **Detach / reattach** | A client closing / reopening its stream without affecting the run. |
| **Invocation** | One call of a model (or remote service) through the invocation contract: start, events, result, cancel, lookup. |
| **Provider** | An adapter that reaches a vendor's models through the invocation contract. |
| **Simulated provider** | A deterministic stand-in for a provider, used by the gate. It never calls a vendor. A **scripted model** is a simulated chat model that replays a fixed sequence of deltas. |
| **Attempt** | One try of a logical call — either a model invocation or an in-process local JSON repair — counted against the call's attempt budget. |
| **Structured-output ladder** | The rungs a chat call tries, in order, to make the model answer with JSON that satisfies the output schema: native schema, JSON mode, forced tool call, and local JSON repair after the last supported rung. |
| **Rung** | One step of the structured-output ladder; a model supports some rungs and not others. |
| **Call** | One logical chat call made through the middleware: a result or an error, whatever its attempts. |
| **Call log** | What the model layer records for every call: one entry per attempt, carrying the attempt's client ref and attempt metadata, with no secret and no content. |
| **Model invocation** | A start of a model through the invocation contract, and the attempt it serves. Every attempt but a local JSON repair is one model invocation; a call refused before any start makes none. |
| **Model capability** | What a model can do: `chat`, `embed`, `image.generate`, `video.generate`, `audio.tts` and so on. |
| **Locality** | Where a model can run: `cloud`, `machine_local` for apps and servers on the machine `kit` runs on, or `worker`. |
| **Worker** | A `kit worker` process on any machine that declares the models and tools it serves and pulls only that work. A worker is **present** from joining until it leaves or stops sending heartbeats; it is **draining** (finishing what it holds, taking no more) or **paused** (taking no work, staying) when told so. |
| **Pool** | The workers of one tenant, or the **shared pool** run by the deployment for every tenant. A worker **joins** one pool with a **pool token**, and the pool keeps what its workers declared. |
| **Availability** | Whether a model served by a pool can run now: `online` (a present worker serves it), `offline`, or `on_demand` (a worker template can start one). |
| **Lease** | A worker's hold on one invocation, kept by heartbeats for a **lease period** and counted per **attempt**. A worker **asks for work** to get leases. |
| **Queue timeout** | How long an invocation may wait for a lease before it fails with `unavailable`. |
| **Class** | The priority of a run's invocations in a pool's queue: `interactive`, `normal` or `batch`. After the pool's **ageing period** a waiting invocation counts as one class higher. |
| **Unit** | The measure of a worker's capacity. A worker has a capacity in units, each model costs a number of them, and a worker says how many are **free** when it asks for work. |
| **Weight**, **concurrency limit** | A tenant's share of the shared pool relative to other tenants, and the most invocations it may have leased there at once. |
| **Restart-safe** | Said of a model whose interrupted invocation may be started again at no cost but time, such as a local command. Paid vendor calls and tools are not. |
| **In doubt** | The state of a started invocation whose lease expired and whose model is not restart-safe. Only the same worker can **resume** it, within the **grace period**; otherwise it fails with `worker_lost`. |
| **Spill-over** | A tenant's setting, off by default: after the **spill-over time**, an invocation still waiting for the tenant's pool moves to another provider of the same model. |
| **Worker template** | The description of workers a pool can start on demand: what one serves, its capacity, the minimum and maximum to run, its **cold start** (the usual time from launch to joining) and its launcher. A template is `degraded` after repeated failed launches. |
| **Launcher** | What starts workers from a worker template when the **scaler** asks. A **launch** is pending until its worker joins or its **cold-start deadline** passes. A launched worker joins with a single-use **join token**. |
| **Idle-exit time** | How long a worker waits without a lease before it leaves its pool and exits. |
| **Credential** | A named secret of a tenant, such as a vendor API key, kept in the vault and used by reference (`cred://<name>`). The tenant is the caller's, from the context; names are 1 to 128 characters from `A`–`Z`, `a`–`z`, `0`–`9`, `.`, `_`, `-` and `/`, made of `/`-separated segments none of which is empty, `.` or `..` (REQ-CRED-006, REQ-CRED-007). |
| **Client ref** | The idempotency key of an invocation, stable across retries of one logical invocation (including retries of an unacknowledged start). |
| **Ambiguous start** | A start whose acceptance by the provider is unknown (for example a timeout after sending). |
| **Milli-point** | The unit of cost: prices, budgets, reservations and charges are whole numbers of milli-points. A point's value in a currency is set per deployment. |
| **Price table** | The rate rules of each model (rates per token, image or second, with the condition under which they apply), shipped with the binary and overridden per deployment and tenant. |
| **Budget** | The most a run may spend, in milli-points. A browser session's budget also limits steps and time. |
| **Reservation** | Estimated cost held against a budget before a paid call; settled to a charge or released after. |
| **Asset** | An output file stored in blob storage under its content hash, with the model, parameter hash, seed and cost that produced it. |
| **Tenant** | The isolation boundary for data and spend. The embedded profile has one tenant. It travels in the context (`core.TenantFrom`); an empty id means no tenant. |
| **Project** | A container of flows, runs and assets inside a tenant; roles are granted per project. |
| **Principal** | An authenticated user or service token acting within a tenant. |
| **Worker token** | A token issued to an external service for one session (such as a browser session), limited to that session's resources and a fixed expiry. A **pool token** instead lets a worker join one pool, and a **join token** admits one launched worker. |
| **Audit event** | A record of a security-relevant decision, such as a denied action and who attempted it, with the time, tenant, principal, action, resource and outcome, and optionally the run, step and reason. Never a secret value or a raw token. Its tenant is the caller's; an event with no tenant is **deployment-level** (a refused join with an unknown pool token, shared-pool and launch events, operator actions), kept out of tenant-scoped reads. |
| **Identity** | Who acted: `<harness>:<role>:<instance>` for an agent session, `human:<login>` for a person. |
| **Browser session** | One run of the browser runner against a web app: scripted (from a feature) or goal-driven (an agent pursues a goal within a budget). |
| **Tier** | Review priority of a scenario: P0 critical … P3 editorial. |
| **Agent** | A named behavior in a flow node: a model, system messages, the tools it allows, caps and a memory policy. The `agent` node handler runs the **agent loop**. |
| **Agent loop** | The flow code of the `agent` node handler: one model turn per step, one tool call per step, caps checked in flow code. |
| **Agent run** | One execution of an `agent` node, from its start (or its handoff) to its end. |
| **Model turn** | One call of the agent's model with its messages, made as one step of the agent run. |
| **Tool call** | One tool step of an agent run: a tool the model requested, run by the tool port, with its result. |
| **Agent output** | What an agent run ends with: its answer, or the state and turns of a run that a cap ended or that handed off. |
| **Cap** | A limit of an agent: steps (model turns), cost in milli-points, tokens, or time on the run clock. Reaching one ends the agent run as completed with a state naming it. |
| **Handoff** | A model request that ends its agent run so that another agent node continues with the model turns so far. |
| **Memory policy** | What of the history goes into a model turn when it does not fit the model's input token limit. |
| **Run clock** | The time `DurableContext.Now` reports for a run; it advances with the fake clock in tests. |
| **Work package (WP)** | One unit of roadmap work with its dependencies and owned directories. Its state (done, in progress, ready, blocked) is derived from the repository. |
| **Prompt template** | A prompt stored under an id and a version (`<id>@<version>`, the **template reference**) that never changes once registered in the **prompt registry**. It declares its inputs, each `trusted` or not, and is made of segments. |
| **Segment** | A named part of a prompt template of one kind: `static`, `semi-static`, `dynamic`, `history` or `user`. A **segment budget** is the most characters it may hold; a template may also have a **total budget**. |
| **Assembled prompt** | What the **assembler** makes from a template reference, inputs and history: segments in order, history trimmed to fit, a content hash. |
| **Cache hint** | The mark an assembled prompt carries after its **cacheable prefix**, the `static` and `semi-static` segments; the **prefix hash** identifies that prefix. |
| **Untrusted value** | An input value not declared `trusted`, or a history entry. The assembler wraps it in **untrusted-content delimiters** labelled with its source. |
| **Prompt lint** | The checks run on a prompt template before it is registered, and on an assembled prompt; each finding has a rule, a severity (`error` or `warning`) and a segment. |
| **Setting** | One configuration key of a `kit` process, written with dots (`listen.addr`). Each process reads settings from flags, environment variables, a configuration file and defaults. |
| **Effective configuration** | The settings a `kit` process ends with, after precedence, with the source each one came from: `flag`, `env`, `file` or `default`. |
| **Port URL** | The setting that selects a port's implementation by its scheme (`sql.url`, `blob.url`, `bus.url`, `secrets.url`). Which schemes a profile accepts, and which implementation each selects, is `specs/config`; how the implementation behaves is the port's own capability. |
| **Ready** | Said of `kit serve` once its runtime started, every port is open and its listener is bound; it reports it with one `ready` line on stdout. |
| **Smoke check** | The gate G6 run: the built binary, started with `kit serve --profile embedded` in an empty directory, must pass every `@smoke` scenario. |
| **Telemetry setup** | The wiring of `slog` log records and OpenTelemetry traces and metrics (`platform/telemetry`), built with a profile, a clock, the credential values to redact and, in the cloud profile, a collector URL. |
| **Span** | One timed unit of work in a trace, with a trace id, a span id, an optional parent span id, a name, start and end times, a status (`ok`, `error`, `unset`), a run id, a tenant and attributes. |
| **Trace context** | A trace id and a span id carried in a `context.Context` and serialized across process or lease boundaries so child spans join the parent trace. |
| **Log record** | One `slog` record with structured attributes, including `trace_id`, `span_id`, `run_id` and `tenant`. |
| **Metric label** | A key/value attribute attached to a metric point. |
| **Redaction** | Replacing a credential value or the value of a field named `password`, `secret`, `token` or `authorization` at any depth with the literal `[REDACTED]` before a log record, span attribute or metric label is kept or exported. |
| **Span store** | The SQLite table of kept spans in the embedded profile, read back per run ordered by start time then span id, and purged past the configured retention period. |
| **Simulated collector** | A deterministic in-process OTLP receiver used by the gate in place of an external collector. |
| **Export batch** | The spans, log records or metric points sent in one OTLP export attempt; dropped if it cannot be delivered. |
| **Schema set** | The closed collection of JSON Schemas, each under its id, that a schema may refer to. Nothing outside it is ever fetched. |
| **Instance location** | Where a value sits inside a JSON document, as a JSON Pointer (`/reviewers/1`); the whole document is the empty pointer `""`. |
| **Violation** | One way an instance fails a schema: its instance location and the schema keyword that failed. |
| **Canonical form** | The RFC 8785 bytes of a JSON value: the same for every text with the same value. |
| **Tool** | A named operation an agent or a flow's `tool` node may call: its name, description and input JSON Schema, and a run with arguments that satisfy the schema. A call is one step that runs once. |
| **Allowlist** | The tools one caller was given. A call to any other tool is refused. |
| **MCP server** | An external service that lists tools with their schemas and runs them when called; the platform reaches it through an MCP client. A **simulated MCP server** stands in for it in the gate. |
| **Sandbox** | An isolated place where a command runs: no host files, environment, instance metadata or network unless granted, with limits on time and output. A **simulated sandbox** stands in for it in the gate. |
| **Durable event log** | The per-run, ordered record of a run's durable events (`seq`), written in the same transaction as the state change that records them. |
| **Outbox** | The durable event log, read as work to relay: each entry is an event pending publication to the bus until it is marked relayed. |
| **Relay** | The component that publishes a run's outbox entries to the bus in `seq` order and marks them relayed after the bus accepts them. |
| **Event envelope** | What the bus carries for a durable event: run id, `seq`, event type, schema version (`v`) and data. A consumer recognises a repeated event by run id and `seq`. |
| **Inspector** | The run inspector, a web UI that reads runs through the API: the runs a person may see, a run's events, its open gates and the recordings attached to it. |
| **Account** | A person's login in the inspector, identified by a username (`"ana"`). |
| **Recording** | The rrweb DOM recording of a browser session, replayed in the inspector. |
| **Bus** | The publish/subscribe port. A message is published on a **subject** (dot-separated tokens, no wildcards, such as `run.r1.deltas`) and kept until the subject is **purged**. A **subscriber** subscribes live, from the start, or after a bus sequence it already has. |
| **Bus sequence** | The number the bus gives a message when it is published. It increases on each subject but is not contiguous, and it is neither a durable event's `seq` nor a delta's `dseq`. |
| **KV entry** | A value under a key in the KV store, with a **revision** that grows on every change and an optional **TTL** after which the entry **expires** and reads as absent. A **swap** changes an entry only at its current revision; a **create** succeeds only if the key has no entry. |
| **Expression** | A CEL condition or computed value (an edge's `when`, a guard, a gate's acceptance, a price rule, an authorization condition), type-checked against the schemas of the variables its **expression environment** declares and evaluated within that environment's **cost limit**. |
| **Mapping** | A gojq program that reshapes one JSON value into exactly one other, such as a provider's request or response mapping, evaluated within a **step limit**. |
| **Blob** | Immutable content kept by a tenant in blob storage under its **content key**, `sha256:` and the hex SHA-256 of the content. |
| **Blob URL** | A URL that lets a client without credentials fetch one blob until its lifetime ends. In the embedded profile it is a **proxied blob URL**, signed and served by the platform. |
| **Master key** | The key-encryption key held by the key service (a local key, a KMS key or a Vault Transit key). It wraps data keys and never leaves the key service. |
| **Key service** | What holds the master key, selected by URL: `localsecrets://` in the embedded profile, a KMS or Vault Transit in the cloud one. The gate uses a **simulated key service**. |
| **Data key**, **keyset** | A tenant's keyset is its data keys, one of them **primary**, stored only wrapped by the master key. New payloads use the primary data key; earlier ones still decrypt. |
| **Payload codec** | Encrypts each payload a runtime records in history with the tenant's primary data key, and decrypts it on the way back. |
| **SQL store** | The SQL port of the platform: a SQLite file in the embedded profile, Postgres in the cloud profile, opened by URL. Its repositories are scoped to the tenant of each call. |
| **Migration**, **migration set**, **schema version** | A numbered schema change embedded in the binary; the ordered migrations a SQL store applies on open; the number of the last migration applied to a database. |
