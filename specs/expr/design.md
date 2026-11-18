# Expressions: design

Normative for the implementer of WP-L2-01. Decisions: ADR-0003. Names in the
Go sketch may change; the behavior in the tables may not without a spec
change.

## 1. Shape of the package

```go
// Expressions (CEL).
type Var struct {
	Name   string          // a CEL identifier: "state", "input", "run", "params", "at"…
	Schema json.RawMessage // a JSON Schema, see §2
}
func NewEnv(vars []Var, opts ...EnvOption) (*Env, error) // opts: WithCostLimit(uint64)
func (e *Env) Compile(src string, result json.RawMessage /* expected result schema, or nil */) (*Program, error)
func (p *Program) Eval(vars map[string]any) (any, error) // JSON values in, a JSON value out

// Mappings (gojq).
func CompileMapping(src string, varNames []string, opts ...MappingOption) (*Mapping, error) // opts: WithStepLimit(uint64), WithOutputLimit(int)
func (m *Mapping) Eval(input any, vars map[string]any) (any, error)
```

- **JSON values** are trees of `map[string]any`, `[]any`, `string`, `bool`,
  `nil` and `json.Number`, so integers stay exact (REQ-EXPR-010,
  REQ-EXPR-018). If `core/schema` (WP-L2-02) defines a JSON value type, use it
  instead.
- **Errors** are `core` errors with code `invalid_argument` and a message that
  names the expression or mapping source, and the position, variable, limit
  or cause the requirement asks for. Callers (the flow compiler, the price
  table, authorization) add their own context, such as a node or edge name.
  `expr` owns the cause phrases of evaluation errors and maps the library's
  errors onto them, whatever the library's own wording:
  - `division by zero`
  - `overflow`
  - `out of bounds` (an index outside a list)
  - `no such key` (an absent map key or property)
  - `no JSON form` (a result that cannot be written as JSON)
  - `unordered key` (a map key whose kind has no place in the order of §4)
  - `output over the output limit` (a mapping output over its output limit,
    §5)
  - `mapping construction` (a mapping that builds a list or an object with
    more entries than its output limit allows, §5)
- A compiled `Program` or `Mapping` is immutable and safe for concurrent use.
- `expr` imports only `core` (`layering.yaml`). It reads schemas as JSON, so
  it does not need `core/schema` (§2).

## 2. Variable types from JSON Schema (REQ-EXPR-003)

The compiler reads this subset of a schema; every other keyword is ignored
for typing (validation is `core/schema`'s work).

| Schema | CEL type |
| --- | --- |
| `{"type": "boolean"}` | `bool` |
| `{"type": "integer"}` | `int` |
| `{"type": "number"}` | `double` |
| `{"type": "string"}` | `string` |
| `{"type": "string", "format": "date-time"}` | `google.protobuf.Timestamp` |
| `{"type": "null"}` | `null_type` |
| `{"type": "array", "items": S}` | `list(T(S))` |
| `{"type": "array"}` without `items` | `list(dyn)` |
| `{"type": "object", "properties": {…}}` | an object type with one field per property. Reading a property that is not declared is a type error. Properties not in `required` may be absent: test them with `has()` (REQ-EXPR-009) or the optional syntax `x.?p`. Reading an absent property fails with the cause `no such key`; it never yields a zero value. |
| `{"type": "object", "additionalProperties": S}` without `properties` | `map(string, T(S))` |
| `{"type": "object"}` with neither | `map(string, dyn)` |
| `enum` or `const` whose values all have one JSON type | the type of those values |
| a `type` list, `anyOf`, `oneOf`, `allOf`, a mixed `enum`, `{}` or `true` | `dyn`, checked when evaluated |
| `{"$ref": "#/$defs/<name>"}` | the type of that definition; where a definition refers back to itself, `dyn` at that point |
| `false`, a `$ref` to a missing `#/$defs/<name>`, or any other `$ref` | rejected with `invalid_argument` (the caller resolves external references first) |

The boolean schema in the last row is the schema itself being `false`, which
accepts no value; an object schema's `"additionalProperties": false` is
unaffected and is not rejected.

A result schema (REQ-EXPR-002) is mapped the same way. A result of type `dyn`
is accepted at compile time and converted when evaluated (§3); an `int`
result is assignable to a `double` schema, not the reverse.

Binding values (REQ-EXPR-010, REQ-EXPR-011): a JSON integer, or a number with
no fractional part such as `3.0`, binds to `int` if it fits in 64 bits; any
other value for an `int` variable fails. A `date-time` string binds to a
timestamp if it parses as RFC 3339. A value of the wrong JSON type fails;
nothing is coerced from strings.

## 3. Results as JSON (REQ-EXPR-012)

| CEL value | JSON value |
| --- | --- |
| `bool`, `string`, `null` | the same |
| `int`, `uint` | an integer, exact |
| `double` | a number; `NaN` and infinities fail with `invalid_argument` and the cause `no JSON form` |
| `google.protobuf.Timestamp` | an RFC 3339 string in UTC with `Z`, with only the fractional digits needed (`2026-11-16T09:00:00Z`) |
| `google.protobuf.Duration` | a string in seconds with an `s` suffix, as in protobuf JSON (`"90s"`, `"1.5s"`) |
| `bytes` | a standard base64 string |
| `list` | an array, in list order |
| `map` with string keys, object type | an object |
| a map with non-string keys, a type value, an empty optional | fails with `invalid_argument` and the cause `no JSON form` |

## 4. The CEL environment (REQ-EXPR-005, REQ-EXPR-006, REQ-EXPR-007)

- **Libraries**: the CEL standard library, optional types, and the cel-go
  extensions `strings` (locale fixed to `en_US`), `math`, `lists`, `sets`,
  `encoders` and `bindings`. Nothing else, and no custom function that
  reads the clock, randomness, the process environment, files or the
  network. There is no `now()`: callers declare the time they mean as a
  variable (`run.now` in flows, `at` in price rules) and record it.
- **Time zones**: timestamp getters that take a zone name use the IANA
  database embedded in the binary (`time/tzdata`), never the host's.
- **Map iteration** (REQ-EXPR-021): the keys of a map sort `bool` < `int` <
  `uint` < `double` < `string`, and `bool` and `string` keys sort by value.
  Within `double`, keys sort numerically with `NaN`s after every number, and
  two bit-identical `NaN` keys tie-break deterministically (for example by
  encoded value). This governs `map`, `filter`, `all`, `exists`,
  `exists_one` and two-variable comprehensions, making them independent of
  Go map order, including which error an evaluation reports first. A custom
  type adapter that returns a sorted map for every native map, and an AST
  or evaluation decorator for map literals, is one way to do it.
- **Keys of no order** (REQ-EXPR-022, REQ-EXPR-023): a map key with any
  other CEL type, such as a timestamp, a duration or a list, has no place in
  that order. An evaluation that iterates over such a map fails with the
  cause `unordered key`; it never falls back to Go map order and never drops
  the entry. The same cause fails an evaluation that passes such a map on,
  because a map with non-string keys has no JSON form (§3). The error does
  not name the key or its value, which may be a credential a mapping has
  resolved.
- **Keys that are not values** (REQ-EXPR-024): CEL builds a map literal
  before anything can iterate it, and an entry such as `{[1, 2]: 'a'}` goes
  in without error, while the map is not equal to itself. The adapter keeps
  every entry it cannot order instead of dropping it, and building or
  reading such a map fails with the cause `no such key`. An evaluation that
  reads one key of the map and never needs the entry, as with `exists`,
  succeeds.
- **Cost**: cel-go's runtime cost tracking, with a limit per environment,
  default **1 000 000**. Cost is counted in cel-go's units, which do not
  depend on time or machine, so the same evaluation fails at the same point
  every time. The compile-time cost estimate is not used to reject
  expressions: it is unbounded for any comprehension over a list of unknown
  size.

## 5. Mappings (REQ-EXPR-013 … REQ-EXPR-020)

- **Variables**: named variables (`$params`, `$inputs`…) declared at compile
  time; `$ENV` is never one of them.
- **Rejected** at compile time, by walking the parsed query, wherever they
  appear, including inside function definitions:

  | Rejected | Why |
  | --- | --- |
  | `now`, `localtime`, `strflocaltime` | read the clock or the host's time zone |
  | `env`, `$ENV` | read the process environment |
  | `input`, `inputs`, `input_filename` | read further inputs |
  | `debug`, `stderr`, `halt`, `halt_error` | write outside the result or stop the process |
  | `import`, `include`, `modulemeta` | read modules from files |

  Pure time functions on given values (`gmtime`, `mktime`, `strftime`,
  `strptime`, `todate`, `fromdate`) are allowed.
- **Object order**: gojq iterates objects (`.[]`, `to_entries`, `keys`) and
  encodes them (`tojson`) in ascending key order. Scenarios pin this so a
  future gojq upgrade cannot change it silently. Functions the gojq library
  does not define (`debug`, `input_filename`, `stderr`) are rejected like
  the others, with the same error.
- **Step limit**: default **1 000 000** steps per evaluation. gojq checks its
  context before every instruction it executes, so a context whose `Done`
  channel closes after the limit's number of checks bounds evaluation
  deterministically, by instructions rather than by time. The evaluator
  reports the step limit, not a context error.
- **Output limit** (REQ-EXPR-020): default **1 048 576 bytes** (1 MiB, the
  largest message the bus accepts, `specs/bus/design.md` §5), the size of
  the value in RFC 8785 canonical JSON (`core/schema`, REQ-SCHEMA-030), set
  per mapping with `WithOutputLimit`. The limit bounds the memory a mapping
  holds: each value it builds is refused before an over-limit result is
  allocated, not measured afterwards.

  gojq offers no runtime hook that sees every intermediate value an
  expression builds, and its `WithFunction` and `WithIterFunction` options
  only define functions not already in gojq's built-in internal table
  (`*` compiles to `_multiply`, and `+` to `_add`, before custom functions
  are looked up). Counting only the top-level results on the iterator is
  also not enough: `"x" * 2147483647` and `reduce range(30) as $i ("x"; . + .)`
  build gigabyte values inside the virtual machine before any result is
  yielded. The evaluator therefore enforces the limit in three layers:

  - **AST rewriting before compilation.** After parsing and before
    `gojq.Compile`, the walk that rejects banned names also rewrites the
    `*gojq.Query` AST:
    - each left-associative chain of operator queries (`OpMul`, `OpAdd`,
      `OpSub`), which gojq compiles to `_multiply`, `_add` and `_subtract`,
      becomes one call to a bounded function with one argument per operand,
      so the bounded function sees the left value before it is combined with
      the right;
    - array construction `[f]` becomes the bounded function for arrays, which
      measures the array `f` builds: entries it collects follow the step
      limit, and an array over the limit fails there;
    - object construction `{…}` becomes the bounded function for objects;
    - the calls `join`, `implode`, `@base64` and `@base64d` become bounded
      functions.

    Each bounded function computes the RFC 8785 size of its result from its
    operands without allocating the result: numbers by arithmetic, a
    repeated string as its length times the count, `join` as the sum of its
    parts plus separators. It fails with the cause `output over the output
    limit` when that size is over the limit, and otherwise computes the value
    from the operands it has already checked. The operator cases call gojq's
    own operator function.
  - **Every yielded value.** Each value a mapping yields, top-level or inside
    an array or object being built, has its RFC 8785 size walked and fails
    the same way when it is over the limit. The walk gives up after visiting
    the limit's number of nodes (an array of `null`s costs one byte per
    node), so it is linear in the limit.
  - **Bounded input and output.** Before entering gojq, the input and the
    variables are validated against the node and size bounds; each top-level
    result is measured again in RFC 8785 canonical bytes.

  The bounded functions are registered under names that lex as bare `_`
  followed by the `(` of their call, a shape the jq grammar gives no other
  meaning. Registrations are one per arity and lexically targeted, so a user
  function of the same name is never called instead.

  Two kinds of operation build a large value before any bounded function
  sees it, because gojq implements them inside the virtual machine:
  - a container whose entries are collected by `f` in `[f]` or produced by
    `reduce`, `foreach` or an object's value queries follows the step limit,
    not the output limit: `[range(100000)] | .[0]` builds 100 000 entries,
    and the bounded function for arrays runs only after the last entry;
  - the builtins written in gojq's own language, such as `add`/`add(f)`,
    `while`, `until`, `repeat` and `last`, compose their values with the
    internal `+` after compilation. Their intermediate values are not
    measured; the first value they return, and every value yielded inside a
    rewritten array or object, is.

  Together these keep a mapping within a constant factor of the limit in
  memory rather than guaranteeing it. Bounding these cases as well needs a
  gojq that exposes its internal `+` and array construction to the library
  or limits construction itself.

  **gojq dependency.** gojq's unexported operator function behind `+` and
  `-` (with its exact `*big.Int` arithmetic) is reached through a
  `//go:linkname` declaration into gojq v0.12.20, the version `go.mod`
  pins. A gojq upgrade that moves or renames it fails the build, not
  silently the bound; `go.sum` and the version pin move together with it.

  Stopping is deterministic: the same mapping fails at the same operation
  or value every time, without allocating an over-limit value.
- **Outputs**: the evaluator takes at most two values from the iterator. One
  is the result; none or two is an error (REQ-EXPR-014).
- **Numbers**: inputs keep integers exact (`json.Number` converted to `int`
  or `*big.Int`, as gojq accepts), and results are converted back the same
  way.
