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
func CompileMapping(src string, varNames []string, opts ...MappingOption) (*Mapping, error) // opts: WithStepLimit(uint64)
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
- **Map iteration**: every map an expression sees, bound from a variable,
  written as a literal or built during evaluation, iterates its keys in
  ascending order. Keys of one type sort by value (strings by Unicode code
  point); mixed key types sort `bool` < `int` < `uint` < `double` < `string`,
  with `double` keys ordered numerically (all NaNs last, −0 before +0). If a map
  at runtime contains a key kind outside this ladder, evaluation fails rather than
  iterating in Go map order. This makes `map`, `filter`, `all`, `exists`,
  `exists_one` and two-variable comprehensions independent of Go map order,
  including which error an evaluation reports first. A custom type adapter that
  returns a sorted map for every native map is one way to do it.
- **Cost**: cel-go's runtime cost tracking, with a limit per environment,
  default **1 000 000**. Cost is counted in cel-go's units, which do not
  depend on time or machine, so the same evaluation fails at the same point
  every time. The compile-time cost estimate is not used to reject
  expressions: it is unbounded for any comprehension over a list of unknown
  size.

## 5. Mappings (REQ-EXPR-013 … REQ-EXPR-019)

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
- **Outputs**: the evaluator takes at most two values from the iterator. One
  is the result; none or two is an error (REQ-EXPR-014).
- **Numbers**: inputs keep integers exact (`json.Number` converted to `int`
  or `*big.Int`, as gojq accepts), and results are converted back the same
  way.
