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

## Open questions

- **Compiler (`WP-L4-01`) coverage of `START` and `FAIL` edge ends**: `docs/architecture/flow-engine.md` §1 uses `START` as the entry edge source and `FAIL` as the failure edge target in the IR, and `WP-L4-03` (`REQ-FLOW-014`) specifies that `flow/builder` emits them, but `specs/flow-engine/compile.feature` (`WP-L4-01`) has no explicit requirement or scenario yet stating that `flow/compile` accepts `"START"` as an edge source and `"FAIL"` as an edge target without declaring nodes of those names.
