# Flow engine

Status: IR, compiler and Go builder in `flow/ir`, `flow/compile` and
`flow/builder` (WP-L4-01, WP-L4-03); the machine in `flow/machine` (WP-L4-02);
the host in `flow/host` (WP-L4-04); contracts in `flow/flow.go`.
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
validated canonical form of the same IR. `flow/builder` writes the same IR from
typed Go declarations; it checks only what those declarations can tell it and
leaves kinds, spec shapes, expressions and cycles to the compiler.

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
  plan:        { kind: agent, spec: { agent: planner }, out: plan }
  impl:        { kind: map, spec: { over: "state.plan.modules", node: impl_module, max_parallel: 4 } }
  impl_module: { kind: task }
  verify: { kind: tool, spec: { tool: run_tests } }
  repair: { kind: agent, spec: { agent: fixer }, guard: { max_visits: 3, cost_mpts: 5000 } }
  review: { kind: gate, spec: { schema: "review.json", accept_if: "input.approved || size(input.notes) > 0", timeout: "72h" } }
edges:
  - { from: START,       to: plan }
  - { from: plan,        to: impl }
  - { from: impl_module, to: verify }
  - { from: verify,      to: review, when: "state.tests.failed == 0" }
  - { from: verify,      to: repair, when: "state.tests.failed > 0" }
  - { from: verify,      to: FAIL }
  - { from: repair,      to: verify }
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

A jq `in` or `over` mapping is parsed, measured and checked for reserved names,
but it cannot be type-checked against the state schema: a jq mapping has no
`state` binding, so only CEL is checked against the schema. Checking what a
node's input must be, per kind, is the machine's (WP-L4-02) and the host's
(WP-L4-04) work.

## 3. Machine

`flow/machine` implements `flow.Machine` over one compiled definition: `New`
validates the IR and compiles its expressions, then `Start` and `Apply` run it.

- **Pure**: `Start` and `Apply` take a snapshot and an event and return a new
  snapshot and commands. The snapshot passed to `Apply` is never modified; on
  error the host keeps it. Raw JSON in snapshots, events and commands shares
  backing arrays across return values; callers must treat them as read-only. No
  I/O and no clock.
- **Supersteps**: every ready node runs. Updates merge through reducers **in
  node-id order** at the barrier, so the result does not depend on completion
  order (REQ-FLOW-002). A map node's attempts merge in attempt order after the
  node id. Steps stay sorted in the snapshot, so two runs that differ only in
  completion order have identical snapshots.
- **Edges**: the first edge whose `when` holds, in declaration order;
  otherwise the default edge (REQ-FLOW-001). A node with no outgoing edge ends
  its branch; reaching the reserved `FAIL` endpoint fails the run. An edge from
  `START` must not carry a condition (`New` rejects it).
- **Map children**: at `New`, every `map` node's `spec.node` must name an
  existing node whose kind is one the host runs (not `set`, `map` or `parallel`),
  must not carry a guard, and must not be shared by two map nodes. A map's
  child needs no incoming edge — the map starts it once per element — and
  continuation after the map comes from the child's outgoing edges.
- **Node kinds**: `set` nodes and routing are evaluated in the machine with no
  command (REQ-FLOW-036). A node with no `in` mapping receives the run's input
  as its command input. A `parallel` node starts every node named by its
  outgoing edges in one superstep. A `map` node evaluates `spec.over` to an
  array and starts the node named by `spec.node` once per element, carrying the
  element as input and the one-based index as the attempt, up to
  `DefaultMaxFanout` (10 000; the host may lower it via `WithMaxFanout`). Every
  other kind becomes a `run_node`, `await_gate` or `start_timer` command for the
  host.
- **Guards**: a `while` that does not hold skips the node and ends its branch;
  a visit or cost bound that is reached fails the run with `budget_exceeded`.
  A map child must not have a guard (rejected at `New`). Consecutive
  command-free supersteps inside one `advance` are capped: the limit is
  `max(floor, nodes × (1 + sum of declared max_visits))` with floor
  `DefaultMaxPureSupersteps` (100 000, overridable via `WithMaxPureSupersteps`);
  exceeding it fails with `budget_exceeded` when cost-only bounds would not
  accrue on the machine.
  `cost_mpts` is compared with `Snapshot.Cost`, the run's total settled cost,
  when the node is about to start; a node does not start once the cost has
  reached the budget (`cost >= cost_mpts`), like `max_visits` after the last
  allowed visit.
- **Budget**: `run.cost` is visible to CEL and bounded by guards
  (`cost_mpts`). The host puts each node's settled milli-points on
  `Event.cost_mpts` for `node_completed` and `node_failed`; `Apply` adds them
  to `Snapshot.Cost`. A `node_failed` accrues its cost once, for the pending
  step it names. Reserving and settling is the model node's work
  (`model-layer.md` §4), because the machine does no I/O.
- **Guards at run time**: a node does not start once the run's settled cost
  (`Snapshot.Cost`, CEL `run.cost`) has reached its guard's `cost_mpts`, or
  once it has had the guard's `max_visits` visits; the run then fails with code
  `budget_exceeded` (REQ-FLOW-047). A finished node's settled cost arrives with
  its event, in `flow.Event.CostMpts` (REQ-FLOW-049), so a replay accrues the
  same cost instead of reading a ledger.
- **Fan-out bound**: a `map` node over more than `DefaultMaxFanout` (10,000)
  items fails the run with `budget_exceeded` before any fan-out, carrying no
  state data (REQ-FLOW-048). `max_parallel` throttles the host; it does not
  bound the collection.
- **Large values** are blob references in state.

Property tests generate random completion orders and check the final snapshots
are identical. Fuzzing covers the IR decoder and CEL inputs.

## 4. Host and node handlers

The machine decides; the **host** (`flow/host`, ADR-0011) acts. It is the one
`runtime.FlowHost`, so it runs unchanged on every runtime. It checks the pinned
definition hash after load, builds step idempotency keys from the loaded
definition's name and pinned hash together with the superstep sequence, stops
handler calls in a superstep after the first failure, ends
the run on the first failing future in a superstep (in `Select` order), copies
each handler's settled milli-points into `node_completed` and `node_failed`
events, and may lower the map fan-out limit with `WithMaxFanout` (values above
`DefaultMaxFanout` are clamped; values below 1 make every run fail with
`invalid_argument` before any durable call, without error from the option
itself). It checks the run context before each superstep, each handler call
and each wait on futures; when the context is done the run ends with
`cancelled`, or with `timeout` when the deadline has passed, which is a run
outcome and not a node failure.

| Node kinds | Run by |
| --- | --- |
| `set` | The machine itself (pure CEL). |
| `gate`, `wait`, `subflow`, and the fan-out of `map` and `parallel` | The host, through `DurableContext`. |
| `model`, `agent` | A **node handler**: flow code registered by kind at the composition root (`model/node`, `agent`). Its side effects are steps. |
| `tool`, `task` | One step each, registered with the runtime. |

Package `flow` never imports `runtime`, and the host imports no handler.
