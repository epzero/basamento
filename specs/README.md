# Specs

Specs are the contract between people and coding agents. Agents draft them, a
separate reviewer agent approves them, people review them later by priority,
and the gate executes them. Full rules: [`docs/engineering/bdd-convention.md`](../docs/engineering/bdd-convention.md).

| File | Role |
| --- | --- |
| [`constitution.md`](constitution.md) | Non-negotiable principles. Specs and code must not contradict it. |
| [`glossary.md`](glossary.md) | Ubiquitous language. Step phrases use these terms. |
| `<capability>/requirements.md` | Requirements in EARS form, one `### REQ-<CAP>-<NNN>` heading each. |
| `<capability>/*.feature` | Gherkin examples. Each scenario has `@id:<CAP>-<NNN>-<NN>` and at least one `@REQ-…`. |
| `<capability>/design.md` | Optional: interfaces and decisions the implementing agent must follow. |
| `<capability>/spec.lock.yaml` | Approval ledger: hash of each approved scenario, tier, author, reviewer, note, human status. |

Commands:

```bash
go tool gate spec lint     # format, tags, ids, traceability
go tool gate spec lock     # every scenario approved at its current text
go tool gate spec queue    # human review queue, P0 first
```

Capabilities:

| Capability | Status |
| --- | --- |
| [`core`](core/requirements.md) | Implemented (ids). |
| [`flow-engine`](flow-engine/requirements.md) | Planned (WP-L4-*). |
| [`runtime`](runtime/requirements.md) | Planned (WP-L5-*). |
| [`streaming`](streaming/requirements.md) | Planned (WP-L6-*). |
| [`models`](models/requirements.md) | Planned (WP-L3-*). |
| [`auth`](auth/requirements.md) | Planned (WP-L8-*). |
| [`browser`](browser/requirements.md) | Planned (WP-L9-01). |
| [`secrets`](secrets/requirements.md) | Planned (WP-L1-04). |
