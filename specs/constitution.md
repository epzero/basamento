# Constitution

Non-negotiable principles. A spec, design or change that contradicts an article
is rejected by the reviewer agent. Amend an article only with an ADR and a
P0 spec review.

1. **One binary, pure Go.** The OSS edition is a single static binary built
   with `CGO_ENABLED=0`. Every infrastructure dependency has an embedded,
   in-process implementation for single-user daily use and a cloud
   implementation for SaaS, behind the same port (ADR-0001, ADR-0004).
2. **Runtime neutrality.** Flow code and the agent loop are written against
   `runtime.DurableContext` and never import a durable-execution vendor
   (ADR-0002). Runtimes declare what they cannot do through `Caps`; nobody
   fakes semantics.
3. **Determinism.** Flow code reads no wall clock, no global randomness and no
   Go map order. Side effects happen only in steps. A recorded run replays to
   the same result.
4. **Runs outlive connections.** No client connection, disconnect or
   reconnect changes the outcome of a run. Only an explicit cancellation or
   a declared policy stops it (ADR-0005).
5. **Money is explicit.** Every paid call reserves an estimate before it
   starts and settles after. A submission whose outcome is unknown is never
   resubmitted blindly (ADR-0006).
6. **Contracts are additive.** Protobuf, event and error contracts gain
   fields and codes; they never rename or remove them (`buf breaking`).
7. **Secrets never travel in state.** Credentials are references resolved at
   the edge of a step; payloads in history are encrypted and large payloads
   are stored by reference.
8. **Tenancy is structural.** Every repository call carries a tenant context;
   cross-tenant access is impossible by construction, not by convention.
9. **Specs before code.** Behavior is specified as EARS requirements and
   Gherkin scenarios, approved by a reviewer agent, then implemented. A
   locked scenario changes only through a spec-change review
   (docs/engineering/spec-review.md).
10. **The gate is deterministic.** `make gate` gives the same answer for the
    same commit. Live vendors, LLM judges and goal-driven browser sessions run in separate,
    labelled lanes.
11. **No fakes in production.** Test doubles live in test-only packages that
    production code cannot import (`gate layering`). Production code never
    contains mock data.
12. **English in code and docs.** User-facing text comes from catalogs.
