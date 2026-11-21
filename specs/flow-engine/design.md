# Flow engine: design

Normative for the implementers of `specs/flow-engine`. Decisions: ADR-0003,
ADR-0011; architecture: `docs/architecture/flow-engine.md`. Names in the Go
sketches may change; the behavior the requirements and scenarios state may not
without a spec change.

## 1. Package `flow/builder` (WP-L4-03)

`flow/builder` is a sub-package of layer `flow` in `layering.yaml`, so it may
import `core` (including `core/schema`), `expr` and `flow`, and no runtime
(ADR-0011). It builds a `*flow.IR`; it hashes and compiles nothing. The
content hash and the compiler's verdict come from the compiler (WP-L4-01) on
that IR, so the content hash of a built flow is the compiler's (REQ-FLOW-010).

```go
type Builder struct{ /* unexported */ }

func New(name string, version int) *Builder

// State: a JSON Schema as declared, or a Go type whose schema the schema
// generator in core/schema (WP-L2-02) produces.
func (b *Builder) StateSchema(schema json.RawMessage) *Builder
func (b *Builder) StateType(t reflect.Type) *Builder
// Reducer declares a state key's reducer: replace, append, merge, sum or
// merge_by(<field>).
func (b *Builder) Reducer(key, reducer string) *Builder

// Node declares a node: its name, kind and spec, with options In, Out and
// Guard(flow.Guard). The shape of the spec is the compiler's check.
func (b *Builder) Node(name, kind string, spec json.RawMessage, opts ...NodeOption) *Builder

// Edge declares an edge from a node or START to a node or FAIL, with
// an optional CEL condition. Edges keep their declaration order.
func (b *Builder) Edge(from, to, when string) *Builder
// Entry marks a node as the entry node: the same as Edge("START", node, "").
func (b *Builder) Entry(node string) *Builder

func (b *Builder) Build() (*flow.IR, error)
```

`START` and `FAIL` are the entry point and the failure target of the IR's edge
lists (`docs/architecture/flow-engine.md` §1); the builder writes `"START"` as
the entry edge source (including for a marked entry node) and `"FAIL"` as the
failure edge target in the IR (REQ-FLOW-014).

**What `Build` checks**, in this order, returning the first failure:

| Check | Code | REQ |
| --- | --- | --- |
| a state schema that is not a valid JSON Schema | `invalid_argument` | FLOW-026 |
| a reducer key that the state schema does not declare | `invalid_argument` | FLOW-024 |
| two nodes with one name | `invalid_argument` | FLOW-015 |
| an `Out` key that the state schema does not declare | `invalid_argument` | FLOW-019 |
| an edge source not a node or `START`, or target not a node or `FAIL` | `invalid_argument` | FLOW-016 |
| more than one marked entry node | `invalid_argument` | FLOW-018 |
| no edge from `START` and no marked entry node | `invalid_argument` | FLOW-017 |

The builder's errors are `*core.Error` with code `invalid_argument` carrying
the schema violation location (from `core/schema`), or naming the reducer's
state key, the duplicated node, the node and key, the missing edge end, or the
marked entry nodes, as the scenarios say.

**Left to the compiler:** node spec shape and kind (an unknown kind is
`invalid_argument` from the decoder or the compiler,
`docs/architecture/flow-engine.md` §2), guarded cycles (REQ-FLOW-003), the
`map` child errors of §2.6 (REQ-FLOW-062 … 066), and
every CEL or jq expression — edge `when` conditions, node `in` mappings, `set`
assignments, guard `while` conditions and gate `accept_if` conditions
(REQ-FLOW-004). The builder keeps expressions in the IR as written.

A jq `in` or `over` mapping is parsed, checked for reserved names and measured
against the expression size limit, but it cannot be type-checked against the
state schema: a jq mapping has no `state` binding, so REQ-FLOW-004's schema
check binds CEL in practice, and `over` is only required to be an expression
whose result shape is the `map` node's business. Checking what a node's input
must be, per kind, against the state schema is the machine's (WP-L4-02) and the
host's (WP-L4-04) work, because only they know the kind's contract.

**Determinism** (REQ-FLOW-011): nodes go into `flow.IR.Nodes` and reducers
into `flow.IR.State.Reducers`, both maps whose keys the canonical JSON form
(`core/schema`, RFC 8785) sorts, so their declaration order does not reach the
canonical IR bytes. Edges go into `flow.IR.Edges` in declaration order
(REQ-FLOW-013), and both the builder and the compiler preserve that order
because outgoing edge routing is evaluated in declaration order (REQ-FLOW-001).
`Build` reads no clock, no randomness and no instance identity.

**State schema and reducers** (REQ-FLOW-021, REQ-FLOW-024, REQ-FLOW-026):
`StateType` calls the schema generator in `core/schema` (WP-L2-02) to produce
the JSON Schema for the Go struct type; `StateSchema` compiles the raw JSON
Schema with `core/schema` to verify it is valid. `Build` writes the canonical
schema into `flow.IR.State.Schema` and the declared reducers into
`flow.IR.State.Reducers` after checking that every reducer and node `Out` key
exists in the state schema's properties.

**Boundary:**

- `flow/builder` is owned by WP-L4-03; `flow/ir` and `flow/compile` are owned
  by WP-L4-01.
- The BDD steps for `builder.feature` go into
  `internal/bdd/steps/flow_builder.go`. `flow/builder` may import `core/schema`
  directly; packages listed in `layering.yaml` `test_only` remain restricted to
  `_test.go` files and `internal/bdd`.

## 2. Compiler and machine

WP-L4-01 and WP-L4-02: `docs/architecture/flow-engine.md` §2 and §3,
requirements REQ-FLOW-001 … 006, 045 … 049, 051 and 062 … 066, scenarios
`compile.feature`, `routing.feature`, `runs.feature` and `host.feature`.

### 2.1 Bounded cycles (REQ-FLOW-003)

A cycle terminates only if something it passes through is consumed on every
lap. `guard.max_visits` counts the visits of its node, and `guard.cost_mpts`
bounds the run's settled milli-points (`Snapshot.Cost`, CEL `run.cost`) at the
moment the node is about to start — it is a budget for the run, not for the
milli-points spent inside that node; either is a finite number in the IR, so a
bound exists for the machine to enforce. What a run does when a guard is
reached is the machine's behavior (§2.4, `specs/glossary.md` "Repair guard"),
not the compiler's. One of the two is enough: a node on the cycle with
`{max_visits: 3}` and a node on it with `{cost_mpts: 5000}` both bound the
cycle.

`guard.while` does not bound a cycle. A predicate says when the cycle may
leave; it can hold on every lap, and the compiler cannot prove a CEL predicate
false from the definition alone — `{"while": "true"}` is a valid predicate and
bounds nothing. It narrows an exit, it does not count. In the example of
`docs/architecture/flow-engine.md` §1 the bound is `repair`'s
`{max_visits: 3, cost_mpts: 5000}`; the `when` on `verify -> repair` and any
`while` only choose when the loop leaves.

`flow/ir` therefore accepts a cycle only when one of its nodes has a guard
carrying a positive `max_visits` or `cost_mpts`, and rejects it otherwise
naming a node on the cycle and the missing bound. REQ-FLOW-045 separately
rejects a budget above its maximum (§2.2), so a cycle can only be bounded by a
guard the compiler accepts. It names the smallest node id on the cycle, so two
runs of the compiler report the same node whatever the map order.

### 2.2 Guard maxima (REQ-FLOW-045)

| Field | Maximum | Reason |
| --- | --- | --- |
| `max_visits` | **1,000,000** visits | 244 complete passes over the largest accepted flow (4,096 nodes); above that a flow that loops over a collection is a `map` node, and the bound is indistinguishable from no bound. It is almost 13 orders of magnitude below the greatest value a 64-bit `int` field admits. |
| `cost_mpts` | **1,000,000,000** milli-points (1,000,000 points) | 200,000 times the 5,000 mpts of the repair guard in `docs/architecture/flow-engine.md` §1, so an expensive cycle still fits; a single-cycle budget need not approach a run's whole budget. |

Both ceilings are round decimal values that fit a signed 32-bit integer, so
neither a counter nor a stored `int32` can overflow, and both are far below
the `2^63 - 1` that made a guard meaningless.

`0` means the field is absent: both fields are `omitempty`, and a guard with
neither field is treated as no guard at all, so a cycle it guards is rejected
by REQ-FLOW-003 rather than by the range check. A **negative** value is
contradictory and rejected wherever the guard appears, on a cycle or not. The
error is `*core.Error` with code `invalid_argument` naming the node, the field
and the maximum, for example
`flow: node "repair": guard max_visits 1000001 is above the maximum 1000000`,
and, for a negative value,
`flow: node "repair": guard max_visits -1 is negative; the maximum is 1000000`.

The check is per guard field. A node whose guard carries only `cost_mpts`
bounds a cycle; its `max_visits` stays absent.

### 2.3 Definition size limits (REQ-FLOW-046)

| Measure | Maximum | Reason |
| --- | --- | --- |
| Node count | **4,096** nodes | The recursive cycle search follows the longest path, and the measured cost of validation was 0.95 s at 20,000 nodes (quadratic before the linearization in WP-L4-01's pull request) and 15 s at 80,000, with stack exhaustion on multi-million-node chains. 4,096 is five times below the first measurement and about a thousandth of the chain that exhausts the stack. Real flows have tens of nodes; fan-out is a `map` or `parallel` node. |
| Edge count | **16,384** edges | Four edges per node at the node maximum: enough for if/else ladders on many nodes. Routing scans a node's outgoing edges (REQ-FLOW-001), and reachability scans the edge list, so this bounds both the compile and the run cost of routing. |
| Expression size | **16,384 bytes** | The UTF-8 length of one expression in the IR: an edge `when`, a node `in` mapping (CEL or jq), a guard `while`, a gate's `accept_if` or a map's `over`. A hand-written condition is a few hundred bytes, so this is two orders of magnitude of headroom. The parser and type-checker run once per expression, and every node and edge may carry one, so a single expression must not consume the definition's budget. |
| Definition size | **1,048,576 bytes** (1 MiB) | The canonical JSON bytes, because those are what is hashed, pinned and stored, independent of the YAML or JSON spelling. 1 MiB repeats the repository's message-size precedent (REQ-BUS-011) and the expression output limit (REQ-EXPR-020). A flow at the node and edge maxima is about 0.6 MiB, so the graph limits are reachable inside it. |

The limits are requirements, not implementation guidance: REQ-FLOW-046 makes
each one a rejection with code `invalid_argument` naming the limit and its
maximum, for example `flow: node count limit of 4096 nodes exceeded: 4097
nodes`. An expression rejection also names where the expression sits, for
example `flow: expression size limit of 16384 bytes exceeded: edge "verify" ->
"repair" when is 16385 bytes`. They are constants in `flow/ir`, not
configuration, and apply to an IR built in Go as well as to one decoded from
YAML or JSON.

The IR has no field for a `set` node's assignments yet (Open questions), so
`flow/ir` measures the expressions that do exist: an edge `when`, a node `in`,
a guard `while`, a gate's `accept_if` and a map's `over`.

`core/schema`'s untrusted-JSON limits (WP-L2-02) bound the nesting depth and
the number literals of one JSON value, not the size of a document; they do not
replace these limits. A definition that passes one set can fail the other, and
both apply: the state schema, a node spec or a default value is still checked
by the schema package when it is compiled or canonicalized.

**Enforcement order.** `flow/ir.Validate` checks the node and edge counts, the
guard values and the expression sizes first, in linear time, so an over-limit
definition is rejected before any graph work. `flow/compile.Compile`
canonicalizes once and rejects a canonical form above 1 MiB before hashing and
before type-checking expressions; `ir.Canonical` and `ir.Hash` are then never
asked to hash a definition the compiler would reject. A definition over
several limits reports the first check in that order, deterministically.

### 2.4 Guard budgets at run time (REQ-FLOW-047)

`max_visits` and `cost_mpts` are enforced by the machine at the moment a node
is about to start, before a command for that node exists. **Reached** means
`>=`: a cost or a visit count one below its budget still lets the node start.

- `max_visits`: the node may start while `Snapshot.Visit[node]` is below the
  budget. A guard of 3 lets the node run 3 times and refuses the fourth start,
  so the run still does the work its last allowed visit pays for. Scenario
  `FLOW-047-01`.
- `cost_mpts`: the budget is compared with the run's settled milli-points
  (`Snapshot.Cost`, which is the `run.cost` CEL sees), not with a per-node
  total: a budget protects the run, and the node that spends is not always the
  guarded one. The node may start while `Snapshot.Cost` is below `cost_mpts`;
  at or above it, the start fails. A `cost_mpts` of 80 with 40 milli-points
  settled per visit therefore stops the run before the third visit: `40` after
  the first, `80` after the second, refused before the third. Scenario
  `FLOW-047-02`; the per-visit number is the cost the node's event carries, §3.

A refused start returns the machine's `fail` command with code
`budget_exceeded` and a message naming the node and the budget, for example
`flow: node "repair": visit budget 3 reached` and
`flow: node "repair": cost budget 80 milli-points reached with 80 settled`.
The host turns that command into the run's error (REQ-FLOW-035), so the run
ends `failed` with `budget_exceeded`, as `specs/glossary.md` ("Repair guard")
and the preset scenarios already state. A guard field that is absent or 0 is
no budget (§2.2) and never refuses a start.

### 2.5 Map fan-out limit (REQ-FLOW-048)

A `map` node fans out to one command per item of its collection. `flow/machine`
exports `DefaultMaxFanout = 10000`, the most items one `map` node may fan out
to in a run, and a collection with more items than that fails the run with
code `budget_exceeded` before the machine returns any fan-out command for the
node, so the host creates no step and no child run for it. The message names
the node, the item count and the limit, for example
`flow: node "impl": fan-out limit 10000 items exceeded: 10001 items`, and the
`fail` command carries no state data: the run fails at the start of the
superstep, before any item's result could be merged.

A collection of exactly the limit is not over it, so every item fans out
(scenario `FLOW-048-02`). `WithMaxFanout(n)` lowers the limit for the runs the
host starts (§3); it never raises it above the default. A value above the
default is clamped to the default (REQ-FLOW-060); a value below 1 fails each
run the host starts with code `invalid_argument` before any durable call
(REQ-FLOW-061).

`max_parallel` in a `map` node's spec is the host's throttle on how many of the
node's items are in flight at once. It bounds neither the superstep nor the
size of the collection: REQ-FLOW-046 limits the definition — its nodes, edges,
expressions and canonical bytes — not the number of items a run's collection
holds, which is data the run receives and not part of the definition.

### 2.6 Definition errors the machine cannot repair (REQ-FLOW-062 … 066)

`flow/ir.Validate` rejects six defects that leave a definition unrunnable — a
`map` spec whose `node` is absent, not a string or empty, a `map` child that is
not a declared node, a `map` child of a kind the machine runs itself (`set`,
`parallel`, `map`), one child used by two `map` nodes, a `map` child carrying a
guard, and a condition on the edge from `START` — with code `invalid_argument`,
naming the `map` node and its child, both `map` nodes, or the edge. The `map`
child checks decide the child from the spec's `node` field alone, not from both
`over` and `node` the way `flow/machine`'s `parseMapSpec` reads them, and they
scan `IR.Nodes` in sorted id order, so two `map` nodes using one child always
report them in the same order.

- **A spec with no map node**: a `map` spec whose `node` is absent, not a string
  or empty names no child the machine can run, so the compiler rejects it before
  any child check. Message:
  `flow: map node "fanout": spec needs a non-empty "node" child name`.
- **A child that is not a node** (REQ-FLOW-062): the machine emits no `run_node`
  command for the child, so the host would never run it. Message:
  `flow: map node "fanout": child node "ghost" does not exist`.
- **A kind the machine runs itself** (REQ-FLOW-063): `set` is evaluated by the
  machine, `parallel` and `map` are expanded by it, so none of them produces a
  `run_node` command for the host. Message:
  `flow: map node "fanout": child node "fanout_item" has kind "set", which the machine runs itself`.
- **One child for two `map` nodes** (REQ-FLOW-064): steps are identified by
  `(node, attempt)` (REQ-FLOW-002, scenario FLOW-002-01), so both fan-outs
  produce the same step identities and the canonical merge sees whichever
  completed first. Message:
  `flow: map nodes "fanout" and "review" both use child node "fanout_item"`.
- **A `map` child with a guard** (REQ-FLOW-065): a guard bounds a cycle by
  counting the visits of the nodes the machine routes to (REQ-FLOW-003, §2.1),
  and the machine starts a `map` child once per element without routing to it,
  so the count never advances. A `map` → child → `map` cycle that only the
  child's `max_visits` bounds would pass the cycle check and never stop.
  Message: `flow: map node "fanout": child node "fanout_item" has a guard: a guard counts visits of nodes the machine routes to and a map child runs once per element`.
- **A condition on the edge from `START`** (REQ-FLOW-066): `START` is where a
  run enters, before any node has routed, so the first superstep has no state
  to route on and the machine never evaluates the condition — today it is
  silently ignored. Message:
  `flow: edge "START" -> "plan" takes no condition`.

**Enforcement order.** `flow/ir.Validate` runs the `START`-edge check with the
other edge checks, which have already established that exactly one edge leaves
`START` (REQ-FLOW-043), and the `map` checks after the node and edge checks and
before reachability and the cycle search. A `map` child with a guard is
therefore reported as REQ-FLOW-065, not as a bounded cycle by REQ-FLOW-003.

**Defence in depth.** The five original defects are checks `flow/machine.New`
makes for itself before it compiles an expression, with the same code and the
same wording; they stay there, because an IR can be built in Go without the
compiler (ADR-0003) and a machine must refuse to run one it cannot run. The sixth,
a `map` spec with no usable `node`, breaks the "same wording" claim: the
machine's own missing-`node` check lives in `parseMapSpec` and says
`map spec needs over and node`, while the validator says
`flow: map node "fanout": spec needs a non-empty "node" child name`. The
compiler adds all six so the author sees the error when the flow is registered
(`flow/compile.Compile`, `flow/ir.Validate`) rather than when a run starts.
`flow/builder` keeps a node spec as written (REQ-FLOW-012) and has no `map`
check of its own: it checks only what its declarations make knowable, and the
compiler is the one validator — a second copy would be a second source of
truth. A flow built in Go therefore meets these checks at compile time exactly
as one written as data does.

### 2.7 Default node input (REQ-FLOW-051)

A node with no input mapping (`in`) receives the run's input as its command
input; an empty run input becomes `{}`. This is the machine's `nodeInput`, so
it holds for every node kind, including a `subflow` node whose spec names a
child flow but gives no `in` (scenario `FLOW-033-01`). It is not a mapping the
host adds: REQ-FLOW-036 still forbids a durable call for pure `set` nodes and
routing.

## 3. Flow host (WP-L4-04)

`flow/host` is the layer ADR-0011 defines (`layering.yaml`): it may import
`core`, `expr`, `flow` and `runtime`, never a durable-execution vendor, and it
imports no node handler. It provides the one `runtime.FlowHost`
(`docs/architecture/runtime.md` §1, `docs/architecture/flow-engine.md` §4) and
owns `flow/host`. Requirements REQ-FLOW-027 … 050 and 052 … 061 (REQ-FLOW-051
is the machine's default node input, §2.7), scenarios `host.feature`.

```go
// NodeResult is what a node handler returns: the node's output and the
// milli-points it settled for the call (REQ-FLOW-050, REQ-FLOW-059).
type NodeResult struct {
	Output   json.RawMessage
	CostMpts int64
}

// NodeHandler is the flow code of one effectful node kind (ADR-0011).
type NodeHandler func(dc runtime.DurableContext, node flow.Node, input json.RawMessage) (NodeResult, error)

// flow.Event carries CostMpts int64 on node_completed and node_failed only
// (REQ-FLOW-049); every other event leaves it zero.

// Host implements runtime.FlowHost via its Run method, with a definition
// catalog and registered node handlers.
type Host struct{ /* unexported */ }

func New(catalog DefinitionCatalog, opts ...Option) *Host
// RegisterHandler registers the handler for a node kind. The composition root
// calls it before the host is registered with a runtime.
func (h *Host) RegisterHandler(kind string, handler NodeHandler)
func (h *Host) Run(dc runtime.DurableContext, ref runtime.FlowRef, input json.RawMessage) (json.RawMessage, error)

// WithMaxFanout lowers the map fan-out limit for the runs the host starts
// (REQ-FLOW-048, design.md §2.5); the machine's DefaultMaxFanout is the cap.
// A value above the cap is clamped to it (REQ-FLOW-060); a value below 1
// fails each run the host starts with invalid_argument before any durable
// call (REQ-FLOW-061).
func WithMaxFanout(limit int) Option

// DefinitionCatalog resolves a flow reference to its compiled IR (WP-L4-01).
type DefinitionCatalog interface {
	Load(ctx context.Context, ref runtime.FlowRef) (*flow.IR, error)
}
```

**Pinned definition (REQ-FLOW-027, REQ-FLOW-054, REQ-FLOW-055).** The host loads
the IR from the catalog by the run's content-addressed `FlowRef` (`Name` and
`Hash`) at the start of `Run` and holds it in a local variable for that
execution. Because `FlowRef.Hash` identifies an immutable compiled IR, re-loading
on replay returns the identical IR, and registering a new version cannot affect
a running run. Loading is not a step.

The caller that starts a top-level run resolves the flow name through the
definition catalog and passes the resolved content hash in its `FlowRef`; the
host never resolves a hash itself. A `subflow` node's `spec.hash`, when present,
pins the child run to that hash; when absent, the child starts with an empty
hash — the host does not require a hash the spec does not carry. When the host
loads a definition and the run's pinned hash is not empty, it compares the
loaded definition's content hash with the pin and fails the run with code
`internal`, naming the flow, if they differ (the catalog returned something
other than what was pinned), before any durable call (REQ-FLOW-054).

An empty hash is accepted and leaves the run unpinned. The host then runs
whatever definition the catalog resolves when the run starts or restarts, so
REQ-FLOW-005 (runs continue with the definition hash they started with) does
not apply to such a run (REQ-FLOW-055).

**Node kinds (REQ-FLOW-028 … 033, 036).** The host checks the whole IR before
`flow.Machine.Start`: if any node's kind is `model` or `agent` and
`RegisterHandler` was not called for it, the host returns a `*core.Error` with
`CodeUnsupported` naming the node, and starts nothing (REQ-FLOW-028). Nodes are
scanned in sorted node-id order, so two missing handlers always report the same
(`IR.Nodes` is a map, and Go map order is not deterministic). The host then
drives the machine, and turns its commands into calls:

| Command, node kind | Durable call | Node event fed back |
| --- | --- | --- |
| `run_node`, kind `tool` or `task` | `dc.Step(stepName, cmd.Input, runtime.StepOptions{IdempotencyKey: ...})` where `stepName` is `spec["tool"]` / `spec["task"]` | `node_completed` with the step result |
| `run_node`, kind with a registered handler (`model`, `agent`) | `handler(dc, node, cmd.Input)` called directly as deterministic flow code; its own side effects are `dc.Step` calls | `node_completed` with the handler's output |
| `run_node`, kind `map` or `parallel` | the fan-out commands the machine returns for the node (WP-L4-02) | as the machine defines |
| `start_timer`, kind `wait` | `dc.Sleep(duration of the node spec)` | `timer_fired` with no payload |
| `await_gate`, kind `gate` | `dc.Await(GateSpec{Name: cmd.Node, Schema, AcceptIf, Timeout from the node spec})` | `gate_resolved` with the accepted payload |
| `run_node`, kind `subflow` | `dc.Child(FlowRef of the flow named in the node spec, cmd.Input, ChildOptions{...})` | `node_completed` with the child result |
| `emit` | `dc.Emit(ProgressEvent{Type, Data})` | none |
| `checkpoint` | `dc.Checkpoint(SnapshotRef)` | none |

Pure `set` nodes and edge routing are evaluated by `flow/machine` itself without
host durable calls (REQ-FLOW-036, `docs/architecture/flow-engine.md` §4).

**Idempotency keys (REQ-FLOW-058).** A `tool` or `task` step carries the key
`<definition name>@<hash>/<node>#<attempt>@<superstep sequence>`: the loaded
definition's name, the run's pinned hash, the node id, the command's attempt and
the machine's superstep sequence (the snapshot's `Seq`). A normal node's attempt
is 0 on every visit and a `map` child's attempt is its element index, so
`(node, attempt)` repeats on a loop's second visit; the sequence is what makes
the key unique per logical step. Replay therefore issues the same key for the
same logical step, and a second visit of a node in a loop issues a different one.

A command for a kind the machine returns without a follow-up event is answered
with the one event in its row, so the machine advances: for `wait` and `gate`
the host feeds `timer_fired` or `gate_resolved` alone, and adds no
`node_completed` after it. A negative `wait` `duration` or `gate` `timeout`
fails the run with code `invalid_argument` when the node runs, before the host
makes any durable call for it (`dc.Sleep` or `dc.Await`); it never becomes a zero
wait and never panics (REQ-FLOW-056).

**Superstep concurrency (REQ-FLOW-044).** When `Machine.Start` or
`Machine.Apply` returns multiple commands in one superstep, the host starts all
of their Future-returning durable calls (`dc.Step`, `dc.Sleep`, `dc.Await` and
`dc.Child`) in command order and then waits for completions with `dc.Select`,
rather than blocking on each command sequentially. `NodeHandler` is synchronous
and returns a `NodeResult` and no `Future`: the host calls it directly, one
command at a time, as the table above says; it occupies the host while it runs,
so the host has no other concurrent work to wait on around it.

When several futures of one superstep fail, the host fails the run with the
error of the future `Select` reports first, does not await the other futures and
runs no later superstep (REQ-FLOW-052). The host calls node handlers in command
order; once a handler returns an error, it does not call the handlers of the
nodes that follow it in command order in that superstep (REQ-FLOW-053).

**Node failure (REQ-FLOW-035, REQ-FLOW-057).** Any error from a `NodeHandler`,
`dc.Step`, `dc.Await` (including `CodeTimeout`), or `dc.Child` becomes a
`node_failed` event carrying the error's `core.Code` and message (`core.CodeOf`,
so a non-`*core.Error` becomes `internal`). A non-`*core.Error` is reported as
code `internal` with the message `flow: node "<id>" failed`. Handing
`node_failed` to the machine is the host's whole decision; when the machine
returns `fail`, the host returns a `*core.Error` with that code and message,
which is the error the caller of the run observes. That run's error carries only that code and message — no error details, no node input and no state — and the original error text of a non-`*core.Error` is not reported (REQ-FLOW-057).

Unit-level invariant (not observable by a scenario): the `node_failed` event the
host feeds `machine.Apply` carries only the code and message in its payload — no
error details, no node input and no state. It is an internal hand-off between
the host and the machine: `DurableContext` does not carry it, so FLOW-057-01
asserts the caller-observable run error instead. The event-payload invariant is
pinned by `TestNodeFailedEventPayloadIsOnlyCodeAndMessage` in
`flow/host/run_handlers_test.go`, which calls the host's single event
constructor `failedEvent`, used by every failure path.

**Node cost (REQ-FLOW-049, REQ-FLOW-050, REQ-FLOW-059).** The host reports a
finished node's settled cost to the machine in `flow.Event.CostMpts`: an
additive field of `flow.Event`, an `int64` of milli-points, non-negative, set
only on `node_completed` and `node_failed` and left zero on every other event.
The machine adds it to `Snapshot.Cost` with the node's other updates, when it
applies the event (REQ-FLOW-049), so the edges out of that node and every later
expression see it in `run.cost`, which is what a guard's `cost_mpts` compares
against (§2.4). The number is part of the node's recorded result — the amount
the node's handler returned — and never a value read from a ledger, a price
table or a meter while replaying: the event the host feeds back already carries
it, so replaying the same history accrues the same cost and reaches the same
guard verdicts (REQ-FLOW-037). A `node_failed` accrues the settled cost of the
pending step the event names, once: applying it removes that pending step. A
pure `set` node, a wait node and a gate node cost nothing, so their completions
carry no cost. The field is non-negative by contract — settlement charges a
non-negative number of milli-points (`specs/models/design.md` §C3 and §C5) — so
the machine never has to read a negative cost as a refund.

**The node handler is the only cost source (REQ-FLOW-059).** `NodeHandler`
returns `NodeResult{Output json.RawMessage; CostMpts int64}`. The host copies
`CostMpts` into the `node_completed` event for a successful handler, and into
the `node_failed` event when the handler returns both a result and an error.
Only the model layer prices a call (`specs/models/design.md` §C4 and §C5), so
the handler of a `model` or `agent` node is where cost is settled. A `tool` or
`task` step's result and a `subflow` child run's result carry no settled cost:
`runtime.StepFunc` returns an output only, and a child run completes with its
final state (REQ-FLOW-038). Carrying cost in either would change the step or the
child contract — a runtime contract change that needs the architect and an
ADR. The host therefore reports `0` for a tool/task step and for a subflow child
run. A child run's cost accrues inside the child and is bounded by the child's
own guards.

**Replay and state (REQ-FLOW-037, 038).** The host keeps no state between calls
other than local variables of the `FlowHost` call and the machine `Snapshot`
it carries: the snapshot is fed back to `Machine.Apply` from the bookkeeping of
that replay, so the same history and the same IR give the same commands and
the same `DurableContext` calls. Step, timer, gate and child-run results come
from the history, so re-executing deterministic handler code on replay replays
its `DurableContext` calls from history without re-running side effects. When
the machine returns `complete`, the host returns the run's final state as the
`FlowHost` result, and `host.feature`'s "no durable call after it completed"
excludes the runtime's own recording of that result. When the machine returns
`fail`, the host returns a `*core.Error` with the run's code.

**Waiting.** The local runtime and the replay tests read the run's waiting
state from the fake's gate; no scenario of this work package carries
`@runtime:local` or `@runtime:temporal`. The fake is the recording
`DurableContext` in `internal/bdd` (steps: `internal/bdd/steps/flow_host.go`),
which records every call in order, replays a scripted history and advances only
on the test's clock. Every name, gate payload and timer in `host.feature` is
given literally in its step, so no scenario derives a number.

**Boundary:**

- `flow/host` is owned by WP-L4-04; it does not import `internal/bdd` code and
  contains no handler.
- `flow` stays free of `runtime`; `gate layering` enforces the `flow`,
  `flow/host` and `deny_external` lines of `layering.yaml` (ADR-0011).
- Node handlers of the other kinds (WP-L3-03, WP-L7-01) only call
  `RegisterHandler` at the composition root; nothing here changes for them.

## Open questions

- **Durable progress events (`node.started`, `node.completed`, `run.*`)**:
  `docs/architecture/streaming.md` §2 and §7 name `node.started` and
  `node.completed` as durable stream events, and `flow.Command` includes `emit`,
  but neither ADR-0011 nor `docs/architecture/flow-engine.md` specifies whether
  `flow/machine` emits them as `emit` commands or `flow/host` emits them
  around node transitions, nor their exact `Data` JSON shape (deferred to
  WP-L6-01 / `specs/streaming`).
- **Shapes that the compiler does not yet fix**: the node-spec fields
  `duration` (`wait`), `schema` and `timeout` (`gate`) and `flow`
  (`subflow`) are read from the node spec as the scenarios write them; the
  compiler (WP-L4-01) checks the expression in `accept_if` (`gate`) and `over`
  (`map`) but not the shape of the rest of the spec. The `subflow` child's
  run id and parent-close policy are not specified here.
- **Compiler (`WP-L4-01`) coverage of `START` and `FAIL` edge ends**: `docs/architecture/flow-engine.md` §1 uses `START` as the entry edge source and `FAIL` as the failure edge target in the IR, and `WP-L4-03` (`REQ-FLOW-014`) specifies that `flow/builder` emits them, but `specs/flow-engine/compile.feature` (`WP-L4-01`) has no explicit requirement or scenario yet stating that `flow/compile` accepts `"START"` as an edge source and `"FAIL"` as an edge target without declaring nodes of those names.
