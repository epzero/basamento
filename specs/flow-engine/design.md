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

The builder's errors are `*core.Error` with code `invalid_argument` naming the
state key and type, the reducer's state key, the duplicated node, the node and
key, the missing edge end, or the marked entry nodes, as the scenarios say.

**Left to the compiler:** node spec shape and kind (an unknown kind is
`invalid_argument` from the decoder or the compiler,
`docs/architecture/flow-engine.md` §2), guarded cycles (REQ-FLOW-003), and
every CEL or jq expression — edge `when` conditions, node `in` mappings, `set`
assignments, guard `while` conditions and gate `accept_if` conditions
(REQ-FLOW-004). The builder keeps expressions in the IR as written.

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
  directly; test-only packages (`core/coretest`) remain restricted to
  `_test.go` files and `internal/bdd` (`layering.yaml`).

## 2. Compiler and machine

WP-L4-01 and WP-L4-02: `docs/architecture/flow-engine.md` §2 and §3,
requirements REQ-FLOW-001 … 006, scenarios `compile.feature`,
`routing.feature` and `runs.feature`.

## 3. Flow host (WP-L4-04)

`flow/host` is the layer ADR-0011 defines (`layering.yaml`): it may import
`core`, `expr`, `flow` and `runtime`, never a durable-execution vendor, and it
imports no node handler. It provides the one `runtime.FlowHost`
(`docs/architecture/runtime.md` §1, `docs/architecture/flow-engine.md` §4) and
owns `flow/host`. Requirements REQ-FLOW-027 … 033, 035 … 038, scenarios
`host.feature`.

```go
// NodeHandler is the flow code of one effectful node kind (ADR-0011).
type NodeHandler func(dc runtime.DurableContext, node flow.Node, input json.RawMessage) (json.RawMessage, error)

// Host implements runtime.FlowHost via its Run method, with a definition
// catalog and registered node handlers.
type Host struct{ /* unexported */ }

func New(catalog DefinitionCatalog) *Host
// RegisterHandler registers the handler for a node kind. The composition root
// calls it before the host is registered with a runtime.
func (h *Host) RegisterHandler(kind string, handler NodeHandler)
func (h *Host) Run(dc runtime.DurableContext, ref runtime.FlowRef, input json.RawMessage) (json.RawMessage, error)

// DefinitionCatalog resolves a flow reference to its compiled IR (WP-L4-01).
type DefinitionCatalog interface {
	Load(ctx context.Context, ref runtime.FlowRef) (*flow.IR, error)
}
```

**Pinned definition (REQ-FLOW-027).** The host loads the IR from the catalog by
the run's content-addressed `FlowRef` (`Name` and `Hash`) at the start of `Run`
and holds it in a local variable for that execution. Because `FlowRef.Hash`
identifies an immutable compiled IR, re-loading on replay returns the identical
IR, and registering a new version cannot affect a running run. Loading is not a
step.

**Node kinds (REQ-FLOW-028 … 033, 036).** The host checks the whole IR before
`flow.Machine.Start`: if any node's kind is `model` or `agent` and
`RegisterHandler` was not called for it, the host returns a `*core.Error` with
`CodeUnsupported` naming the node, and starts nothing (REQ-FLOW-028). The host
then drives the machine, and turns its commands into calls:

| Command, node kind | Durable call | Node event fed back |
| --- | --- | --- |
| `run_node`, kind `tool` or `task` | `dc.Step(cmd.Node, cmd.Input, runtime.StepOptions{IdempotencyKey: ...})` | `node_completed` with the step result |
| `run_node`, kind with a registered handler (`model`, `agent`) | `handler(dc, node, cmd.Input)` called directly as deterministic flow code; its own side effects are `dc.Step` calls | `node_completed` with the handler's output |
| `run_node`, kind `map` or `parallel` | the fan-out commands the machine returns for the node (WP-L4-02) | as the machine defines |
| `start_timer`, kind `wait` | `dc.Sleep(duration of the node spec)` | `timer_fired`, then `node_completed` with no result |
| `await_gate`, kind `gate` | `dc.Await(GateSpec{Name: cmd.Node, Schema, AcceptIf, Timeout from the node spec})` | `gate_resolved` with the accepted payload, then `node_completed` with it |
| `run_node`, kind `subflow` | `dc.Child(FlowRef of the flow named in the node spec, cmd.Input, ChildOptions{...})` | `node_completed` with the child result |
| `emit` | `dc.Emit(ProgressEvent{Type, Data})` | none |
| `checkpoint` | `dc.Checkpoint(SnapshotRef)` | none |

Pure `set` nodes and edge routing are evaluated by `flow/machine` itself without
host durable calls (REQ-FLOW-036, `docs/architecture/flow-engine.md` §4).

**Handler failure (REQ-FLOW-035).** A handler error becomes a `node_failed`
event carrying the error's `core.Code` and message (a non-`*core.Error` is
`internal`, `core.CodeOf`). Handing it to the machine is the whole decision;
routing to the failure edge or failing the run is the machine's (REQ-FLOW-001,
`routing.feature`). The run's error is the handler's error, so the run fails
with the same `core.Code`.

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
  `duration` (`wait`), `schema`, `accept_if` and `timeout` (`gate`) and `flow`
  (`subflow`) are read from the node spec as the scenarios write them; the
  compiler (WP-L4-01) does not check their shape yet. The `subflow` child's
  run id and parent-close policy are not specified here.
- **Compiler (`WP-L4-01`) coverage of `START` and `FAIL` edge ends**: `docs/architecture/flow-engine.md` §1 uses `START` as the entry edge source and `FAIL` as the failure edge target in the IR, and `WP-L4-03` (`REQ-FLOW-014`) specifies that `flow/builder` emits them, but `specs/flow-engine/compile.feature` (`WP-L4-01`) has no explicit requirement or scenario yet stating that `flow/compile` accepts `"START"` as an edge source and `"FAIL"` as an edge target without declaring nodes of those names.
