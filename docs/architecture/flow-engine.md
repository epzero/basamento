# Flow engine

Status: contracts in `flow/flow.go`; implementation **Planned** (WP-L4-*).
Decisions: ADR-0003, ADR-0011. Specs: `specs/flow-engine`.

## 1. IR

A flow is canonical JSON with `name`, `version`, a state schema with per-key
reducers, nodes and edges. Its sha256 is its identity, and runs pin it. Authors
write YAML, use the Go builder, or have an LLM generate the IR; all three
compile to the same IR.

```yaml
flow: codegen.feature
version: 3
state:
  schema: { $ref: codegen-state.json }
  reducers: { messages: append, files: merge_by(path), attempts: sum }
nodes:
  plan:   { kind: agent, spec: { agent: planner }, out: plan }
  impl:   { kind: map, spec: { over: "state.plan.modules", node: impl_module, max_parallel: 4 } }
  verify: { kind: tool, spec: { tool: run_tests } }
  repair: { kind: agent, spec: { agent: fixer }, guard: { max_visits: 3, cost_mpts: 5000 } }
  review: { kind: gate, spec: { schema: review.json, accept_if: "input.approved || size(input.notes) > 0", timeout: 72h } }
edges:
  - { from: START,  to: plan }
  - { from: plan,   to: impl }
  - { from: impl,   to: verify }
  - { from: verify, to: review, when: "state.tests.failed == 0" }
  - { from: verify, to: repair, when: "state.tests.failed > 0" }
  - { from: verify, to: FAIL }
  - { from: repair, to: verify }
```

Node kinds: `set` (pure CEL assignment inside flow code), `task`, `model` (any
model invocation, see `model-layer.md`), `agent`, `tool` (effectful steps), `map`
(dynamic fan-out), `parallel`, `subflow`, `gate`, `wait`.

## 2. Compiler

- Validates the schema, references and node specs.
- Type-checks every CEL expression against the state schema.
- Requires at least one guarded node on every cycle, which bounds it.
- Rejects unknown node kinds.
- Error messages name the node or edge and the expression (REQ-FLOW-003/004).

## 3. Machine

- **Pure**: `Start` and `Apply` take a snapshot and an event and return a new
  snapshot and commands. No I/O and no clock.
- **Supersteps**: every ready node runs. Updates merge through reducers **in
  node-id order** at the barrier, so the result does not depend on completion
  order (REQ-FLOW-002).
- **Edges**: the first edge whose `when` holds, in declaration order;
  otherwise the default edge (REQ-FLOW-001).
- **Budget**: `run.cost` is visible to CEL and bounded by guards
  (`cost_mpts`). Reserving and settling is the model node's work
  (`model-layer.md` §4), because the machine does no I/O.
- **Large values** are blob references in state.

Property tests generate random completion orders and check the final snapshots
are identical. Fuzzing covers the IR decoder and CEL inputs.

## 4. Host and node handlers

The machine decides; the **host** (`flow/host`, ADR-0011) acts. It is the one
`runtime.FlowHost`, so it runs unchanged on every runtime.

| Node kinds | Run by |
| --- | --- |
| `set` | The machine itself (pure CEL). |
| `gate`, `wait`, `subflow`, and the fan-out of `map` and `parallel` | The host, through `DurableContext`. |
| `model`, `agent` | A **node handler**: flow code registered by kind at the composition root (`model/node`, `agent`). Its side effects are steps. |
| `tool`, `task` | One step each, registered with the runtime. |

Package `flow` never imports `runtime`, and the host imports no handler.
