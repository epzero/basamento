# ADR-0010 — BDD specs, agent-first review with tiers, deterministic gate

**Status:** accepted · **Date:** 2026-11-01

## Decision
- Behavior is specified as EARS requirements and Gherkin scenarios under `specs/`, executed with godog in strict mode through `go test`.
- A reviewer agent that did not author a spec approves it and assigns a tier; a ledger per capability (`specs/<capability>/spec.lock.yaml`) records the hash of the approved text, the tier, the author and reviewer identities and a note. Review discussion stays in issues and pull requests. People review asynchronously by tier; P0 blocks a release until accepted.
- Agents never change approved text to make tests pass. Newly active scenarios must fail at the base and pass at head (`gate redgreen`).
- The PR gate is deterministic. AI acts only in labelled lanes: `@eval` (LLM judge, replayed from recordings in the gate), `@journey` (resolve once, record, replay).

## Consequences
Spec authoring and review become explicit agent roles. The gate tool (`tools/gate`) is itself tested.
