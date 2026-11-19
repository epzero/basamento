# Flow engine

Status: IR and compiler in `flow/ir` and `flow/compile` (WP-L4-01); contracts
in `flow/flow.go`; machine, builder and host **Planned** (WP-L4-02 … 04).
Decisions: ADR-0003, ADR-0011. Specs: `specs/flow-engine`.

## 1. IR

A flow is canonical JSON with `name`, `version`, a state schema with per-key
reducers, nodes and edges. Its sha256 is its identity, and runs pin it. Authors
write YAML, use the Go builder, or have an LLM generate the IR; all three
compile to the same IR.

`flow/ir` decodes that JSON or YAML, rejects unknown fields, duplicate keys and
a stream carrying more than one document, produces the RFC 8785 canonical bytes
and the content hash, and enforces the structural rules below. `flow/compile`
type-checks expressions, enforces the definition limits and returns the
validated canonical form of the same IR.

```yaml
name: codegen.feature
version: 3
state:
  schema:
    type: object
    properties:
      messages: { type: array, items: { type: object } }
      files:    { type: array, items: { type: object } }
      attempts: { type: integer }
      plan:     { type: object, properties: { modules: { type: array, items: { type: object } } } }
      tests:    { type: object, properties: { failed: { type: integer } } }
    required: [messages, files, attempts, plan, tests]
  reducers: { messages: append, files: merge_by(path), attempts: sum }
nodes:
  plan:   { kind: agent, spec: { agent: planner }, out: plan }
  impl:   { kind: map, spec: { over: "state.plan.modules", node: impl_module, max_parallel: 4 } }
  verify: { kind: tool, spec: { tool: run_tests } }
  repair: { kind: agent, spec: { agent: fixer }, guard: { max_visits: 3, cost_mpts: 5000 } }
  review: { kind: gate, spec: { schema: "review.json", accept_if: "input.approved || size(input.notes) > 0", timeout: "72h" } }
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

- Validates the state schema and its reducer keys and forms, the node names
  and kinds, the edge references and the node specs.
- Type-checks every CEL expression against the state schema: edge `when`
  conditions, node `in` mappings, guard `while` conditions, and the `accept_if`
  of a `gate` and the `over` of a `map` in their node specs.
- Enforces the compiler's limits, in the order `specs/flow-engine/design.md`
  §2.2 and §2.3 state: at most 4,096 nodes and 16,384 edges, a guard budget
  inside its range (`max_visits` ≤ 1,000,000, `cost_mpts` ≤ 1,000,000,000,
  neither negative), each expression at most 16,384 bytes, and a canonical
  definition at most 1 MiB (REQ-FLOW-045, REQ-FLOW-046).
- Requires at least one guarded node on every cycle. A guard's `max_visits` or
  `cost_mpts` is the budget that bounds a cycle (REQ-FLOW-003); a `while`
  condition only chooses when the cycle leaves.
- Rejects unknown node kinds.
- Accepts `START` and `FAIL` as edge endpoints without declaring nodes of
  those names; a node named `START` or `FAIL`, an edge into `START`, an edge
  out of `FAIL`, or a flow without exactly one edge from `START` is rejected.
- Rejects nodes unreachable from `START`.
- Error messages name the node or edge and what is wrong: the expression that
  does not type-check (REQ-FLOW-004), or the node on the cycle and the
  missing guard (REQ-FLOW-003), and all rejections use code
  `invalid_argument`.

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
