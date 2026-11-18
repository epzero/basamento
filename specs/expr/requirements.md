# Expressions: CEL and JSON mappings

Status: **Planned** (WP-L2-01). Design: ADR-0003, [`design.md`](design.md),
`docs/architecture/flow-engine.md`, `docs/architecture/model-layer.md` §7.

Package `expr` gives every other layer one way to write conditions and
computed values (**expressions**, in CEL) and one way to reshape JSON
(**mappings**, in gojq). Flow conditions, guards, `set` nodes, gate
acceptance, price rules and authorization conditions all use it. Both must
give the same answer on every replay, so neither may read the clock, global
randomness, the environment or Go map order, and both are bounded by
deterministic limits rather than by time.

JSON Schema itself (generation, validation, canonical JSON) belongs to
`core/schema` (WP-L2-02, `specs/schema`). These requirements cover only how an
expression is type-checked against the schemas of the variables it reads.

## Expressions (CEL)

### REQ-EXPR-001 — Type-checked against declared variables
If an expression does not type-check against the schemas of the variables declared in its expression environment, then the expression compiler shall reject it with code `invalid_argument`, naming the expression and the line and column of the error.

### REQ-EXPR-002 — Expected result type
Where a caller gives the schema of the result it expects, the expression compiler shall reject with code `invalid_argument` an expression whose result type is not assignable to that schema.

### REQ-EXPR-003 — Variable types from JSON Schema
The expression compiler shall give each declared variable the CEL type that its JSON Schema maps to in the type mapping of `design.md` §2.

### REQ-EXPR-004 — Syntax errors
If an expression cannot be parsed, then the expression compiler shall reject it with code `invalid_argument`, naming the expression and the line and column of the error.

### REQ-EXPR-005 — No clock, randomness or environment
The expression environment shall offer no function whose result depends on the clock, randomness, process environment, file system or network, so that time enters an expression only as a declared variable.

### REQ-EXPR-006 — Sorted map iteration
When an expression iterates over a map, the expression evaluator shall visit its keys in ascending order.

### REQ-EXPR-007 — Cost limit
If evaluating an expression exceeds the cost limit of its expression environment, then the expression evaluator shall stop and fail with code `invalid_argument`, naming the expression and the cost limit.

### REQ-EXPR-008 — Evaluation errors
If an expression fails while it is evaluated, then the expression evaluator shall fail with code `invalid_argument`, naming the expression and the cause.

### REQ-EXPR-009 — Absent optional properties
When an expression tests with `has()` an optional property that the value of a variable does not contain, the expression evaluator shall evaluate the test to `false` without an error.

### REQ-EXPR-010 — Exact integers in variables
When a variable whose schema maps to `int` is bound to a JSON integer, the expression evaluator shall keep the integer's exact value.

### REQ-EXPR-011 — Values that do not fit their variable
If the value bound to a variable cannot be converted to the variable's CEL type, then the expression evaluator shall fail with code `invalid_argument`, naming the variable.

### REQ-EXPR-012 — Results as JSON values
When an expression evaluates successfully, the expression evaluator shall return its result as the JSON value given by the result mapping of `design.md` §3.

## Mappings (gojq)

### REQ-EXPR-013 — One output value
When a mapping is evaluated on a JSON value with its named variables, the mapping evaluator shall return the single JSON value the mapping produces.

### REQ-EXPR-014 — Zero or several output values
If a mapping produces no value or more than one value, then the mapping evaluator shall fail with code `invalid_argument`, stating whether it produced no value or more than one.

### REQ-EXPR-015 — No clock, environment, input or output
If a mapping calls a function or uses a directive that reads the clock, the local time zone, the process environment, further inputs, files or modules, or writes to a stream outside its result, then the mapping compiler shall reject it with code `invalid_argument`, naming that function or directive.

### REQ-EXPR-016 — Sorted object iteration
When a mapping iterates over an object, the mapping evaluator shall visit its keys in ascending order.

### REQ-EXPR-017 — Step limit
If evaluating a mapping exceeds its step limit, then the mapping evaluator shall stop and fail with code `invalid_argument`, naming the step limit.

### REQ-EXPR-018 — Exact integers in mappings
When a mapping passes through an integer of its input unchanged, the mapping evaluator shall return the integer's exact value, whatever its magnitude.

### REQ-EXPR-019 — Mapping syntax errors
If a mapping cannot be parsed, then the mapping compiler shall reject it with code `invalid_argument`, naming the mapping and the position of the error.
