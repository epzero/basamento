# Flow engine: design

Normative for the implementers of `specs/flow-engine`. Decisions: ADR-0003,
ADR-0011; architecture: `docs/architecture/flow-engine.md`. Names in the Go
sketches may change; the behavior the requirements and scenarios state may not
without a spec change.

## 1. Package `flow/builder` (WP-L4-03)

`flow/builder` is a sub-package of layer `flow` in `layering.yaml`, so it may
import `core`, `expr` and `flow`, and no runtime (ADR-0011). It builds a
`*flow.IR`; it hashes and compiles nothing (REQ-FLOW-023). The content hash and
the compiler's verdict come from the compiler (WP-L4-01) on that IR, so the
content hash of a built flow is the compiler's (REQ-FLOW-010).

```go
type Builder struct{ /* unexported */ }

func New(name string, version int) *Builder

// State: a JSON Schema as declared, or a Go type whose schema the schema
// generator (WP-L2-02) produces.
func (b *Builder) StateSchema(schema json.RawMessage) *Builder
func (b *Builder) StateType(t reflect.Type) *Builder
// Reducer declares a state key's reducer: replace, append, merge, sum or
// merge_by(<field>).
func (b *Builder) Reducer(key, reducer string) *Builder

// Node declares a node: its name, kind and spec, with options In, Out and
// Guard(flow.Guard). The shape of the spec is the compiler's check.
func (b *Builder) Node(name, kind string, spec json.RawMessage, opts ...NodeOption) *Builder

// Edge declares an edge from a node, START or FAIL, to a node or FAIL, with
// an optional CEL condition. Edges keep their declaration order.
func (b *Builder) Edge(from, to, when string) *Builder
// Entry marks a node as the entry node: the same as Edge("START", name, "").
func (b *Builder) Entry(node string) *Builder

func (b *Builder) Build() (*flow.IR, error)
```

`START` and `FAIL` are the entry point and the failure target of the IR's edge
lists (`docs/architecture/flow-engine.md` §1); the compiler accepts them
(REQ-FLOW-014).

**What `Build` checks**, in this order, returning the first failure:

| Check | Code | REQ |
| --- | --- | --- |
| two nodes with one name | `conflict` | FLOW-015 |
| an edge end that is not a node, `START` or `FAIL` | `not_found` | FLOW-016 |
| no edge from `START` and no entry node | `invalid_argument` | FLOW-017 |
| more than one entry node | `invalid_argument` | FLOW-018 |
| an `Out` key that the state schema does not declare | `invalid_argument` | FLOW-019 |
| an edge `when` that type-checks with a result other than `bool` | `invalid_argument` | FLOW-020 |
| a state schema or reducer the builder cannot put into the IR | the code of the schema compiler's error (`invalid_argument` or `not_found`) | FLOW-024 |

A `when` that the expression compiler cannot type-check at all is kept in the
IR unchanged, for the compiler to reject (REQ-FLOW-025); the builder does not
reject it, so the verdict stays the compiler's (REQ-FLOW-022). The builder
tells the two apart by asking `expr` for the `when`'s result type, without
failing on an untypable expression.

The builder's errors are `*core.Error` naming the duplicated node, the missing
node, the entry nodes, the node and key, or the edge and expression, as the
scenarios say.

**Left to the compiler:** node spec shape and kind (an unknown kind is
`invalid_argument` from the decoder or the compiler,
`docs/architecture/flow-engine.md` §2), `in` mappings, guards' `while`,
guarded cycles (REQ-FLOW-003), and every expression other than a well-typed
top-level `when` (REQ-FLOW-004). A built flow passes the compiler as a
hand-written one does (REQ-FLOW-022).

**Determinism** (REQ-FLOW-011): nodes go into `flow.IR.Nodes`, a map whose
keys the canonical form sorts, so their declaration order does not reach the
bytes. Edges go into a slice in declaration order (REQ-FLOW-013), and the
content hash must not depend on that order, so the compiler's canonical form
of the edge list is a canonical order (for example, sorted by source, target
and condition) rather than declaration order; WP-L4-01's design owns that
ordering. `Build` reads no clock, no randomness and no instance identity.

**Schema from a Go type** (REQ-FLOW-021): `StateType` stores the type;
`Build` calls the schema generator (REQ-SCHEMA-001 … 004, 007) and writes its
output as the state schema. The reducers are written as declared.

**Boundary:**

- `flow/builder` is WP-L4-03's, and so is WP-L4-01's acceptance of `START` and
  `FAIL` in the compiler (REQ-FLOW-014). The implementer lands that change
  first, in a pull request of its own in `flow/compile`.
- The BDD steps for `builder.feature` go into
  `internal/bdd/steps/flow_builder.go`; they may use `core/schema` and the
  schema generator there and in `_test.go` files, never in `flow/builder`.

## 2. Compiler and machine

WP-L4-01 and WP-L4-02: `docs/architecture/flow-engine.md` §2 and §3,
requirements REQ-FLOW-001 … 006, scenarios `compile.feature`,
`routing.feature` and `runs.feature`.
