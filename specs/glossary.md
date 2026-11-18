# Glossary

Use these terms in requirements, scenarios and step phrases. Add a term here in
the same change that first uses it.

| Term | Meaning |
| --- | --- |
| **Flow** | A versioned definition (IR) of nodes and edges, stored as data and identified by its content hash. |
| **Run** | One execution of a flow with an input. Has a run id, a tenant and a state: running, completed, failed, cancelled. |
| **Node** | One step of a flow: `set`, `task`, `model`, `agent`, `tool`, `map`, `parallel`, `subflow`, `gate`, `wait`. |
| **Flow host** | The flow code that drives a flow: it feeds the flow machine events and turns its commands into durable calls. The same host runs on every runtime. |
| **Node handler** | The flow code of one effectful node kind (`model`, `agent`), registered with the flow host. Its side effects are steps. |
| **Step** | A side-effecting unit executed by a runtime (a Temporal activity on the temporal runtime). Retried; its result is recorded. |
| **Gate** | A node that waits for a human or external decision; its payload is validated by schema and CEL. |
| **Heartbeat** | The periodic signal a long or streaming step sends while it works. Cancellation reaches the step through it. |
| **Conformance flow** | A golden flow with a recorded final state and durable events that every runtime must reproduce. |
| **Runtime** | An implementation of the durable contract: `local` (in-process, not durable) or `temporal`. |
| **Profile** | Deployment shape: `embedded` (one process, single user) or `cloud` (SaaS services). |
| **Durable event** | A run event persisted in order (`seq`) and replayable from any cursor. |
| **Delta** | An ephemeral increment of a message (text, audio, progress), ordered by `dseq`, kept while its message is in flight. |
| **Message** | A model output inside a run, streamed as deltas and completed as a durable event. |
| **Cursor** | What a client sends to resume a stream: last durable `seq` and last `dseq` per open message. |
| **Snapshot** | The text of an in-flight message so far, sent on reconnect before further deltas. |
| **Detach / reattach** | A client closing / reopening its stream without affecting the run. |
| **Invocation** | One call of a model (or remote service) through the invocation contract: start, events, result, cancel, lookup. |
| **Provider** | An adapter that reaches a vendor's models through the invocation contract. |
| **Simulated provider** | A deterministic stand-in for a provider, used by the gate. It never calls a vendor. A **scripted model** is a simulated chat model that replays a fixed sequence of deltas. |
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
| **Credential** | A named secret of a tenant, such as a vendor API key, kept in the vault and used by reference (`cred://<name>`). |
| **Client ref** | The idempotency key of an invocation, stable across retries of one logical call. |
| **Ambiguous start** | A start whose acceptance by the provider is unknown (for example a timeout after sending). |
| **Milli-point** | The unit of cost: prices, budgets, reservations and charges are whole numbers of milli-points. A point's value in a currency is set per deployment. |
| **Price table** | The rate rules of each model (rates per token, image or second, with the condition under which they apply), shipped with the binary and overridden per deployment and tenant. |
| **Budget** | The most a run may spend, in milli-points. A browser session's budget also limits steps and time. |
| **Reservation** | Estimated cost held against a budget before a paid call; settled to a charge or released after. |
| **Asset** | An output file stored in blob storage under its content hash, with the model, parameter hash, seed and cost that produced it. |
| **Tenant** | The isolation boundary for data and spend. The embedded profile has one tenant. |
| **Project** | A container of flows, runs and assets inside a tenant; roles are granted per project. |
| **Principal** | An authenticated user or service token acting within a tenant. |
| **Worker token** | A token issued to an external service for one session (such as a browser session), limited to that session's resources and a fixed expiry. A **pool token** instead lets a worker join one pool, and a **join token** admits one launched worker. |
| **Audit event** | A record of a security-relevant decision, such as a denied action and who attempted it. |
| **Identity** | Who acted: `<harness>:<role>:<instance>` for an agent session, `human:<login>` for a person. |
| **Browser session** | One run of the browser runner against a web app: scripted (from a feature) or goal-driven (an agent pursues a goal within a budget). |
| **Tier** | Review priority of a scenario: P0 critical … P3 editorial. |
| **Work package (WP)** | One unit of roadmap work with its dependencies and owned directories. Its state (done, in progress, ready, blocked) is derived from the repository. |
| **Preset version** | The content hash of a preset's flows, prompts and evals, together with the `@eval` scores recorded for it. A run pins the version it started with; a new version becomes current only when it compiles, does not regress, and a person resolves its review gate. |
| **Codegen stage** | One of `plan`, `implement`, `verify`, `repair`, `self-review` and `commit` in `codegen.bootstrap` and `codegen.iterate`. A completed stage is checkpointed; a resumed run starts again no stage that has a checkpoint. |
| **Repair guard** | The bound on the codegen loop: a visit limit (how many times `repair` may run) and a cost limit in milli-points. A guard that is reached ends the run `failed` with code `budget_exceeded`. |
| **Sandbox** | An isolated workspace in which a codegen run writes modules and runs the plan's tests; nothing it writes reaches a repository. A **simulated sandbox** is its deterministic stand-in in the gate. |
| **Git host** | The remote a codegen run reads a repository from and commits to, behind the repository's own port. A **simulated git host** is its deterministic stand-in in the gate, recording commits, branches and writer calls. |
| **Review gate** | A `gate` node where a person approves or rejects a proposal. The preset uses it only for a new preset version, never in a codegen run, which commits without one. |
| **Self-review** | The `self-review` codegen stage, in which an agent reviews the workspace for findings before the commit under a visit limit (the **self-review guard**); findings go back to repair. It starts no reviewer session and asks no person. |
