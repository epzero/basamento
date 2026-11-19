# Schema: design

Package `core/schema` (WP-L2-02). It sits in `core`, so it imports nothing of
Basamento but `core`, and every layer may use it. CEL type-checking against a
schema is not here; `expr` (WP-L2-01) builds on this package.

## Libraries

- Generation: `github.com/google/jsonschema-go` (`jsonschema.ForType`).
- Validation: `github.com/santhosh-tekuri/jsonschema` (v6), asserting only the
  seven `format` values of REQ-SCHEMA-024 (overriding or disabling the
  library's other built-in format validators so REQ-SCHEMA-026 holds) and
  using a loader that serves only the schema set (REQ-SCHEMA-012). The default
  HTTP and file loaders are never installed.
- Canonical JSON: RFC 8785, written in this package. Numbers use the
  ECMAScript algorithm (`strconv.FormatFloat(f, 'g', -1, 64)` adjusted to the
  ECMAScript exponent rules); no third-party canonicalizer.

Library output beyond the required object shape (`properties`, `required` and
property `description` or `contentEncoding`) is never part of the contract on
its own: type and constraint scenarios state behavior through validation, so
a library upgrade that changes how a generated schema expresses them but not
what it accepts keeps the scenarios green.

### Bytes and raw JSON

`encoding/json` writes a `[]byte` as a base64 string and a nil `[]byte` as
`null`. The generator gives `[]byte`, and any named type whose underlying type
is `[]byte` and which implements neither `encoding.TextMarshaler` nor
`json.Marshaler`, the schema `{"type": ["null", "string"], "contentEncoding":
"base64"}` (REQ-SCHEMA-038). `contentEncoding` is an annotation in draft
2020-12, so validation checks the JSON type only and accepts any string; the
base64 form of the content is what `encoding/json` writes (REQ-SCHEMA-008).
A named byte slice with its own encoding takes the rule below instead.

`json.RawMessage` is a `[]byte` with its own encoding, so it gets its own
entry in the generator's type schemas: a schema that admits every JSON value
including `null` (REQ-SCHEMA-039). The contract is what the schema accepts,
not the form it is written in: the generator emits the boolean schema `true`
where `google/jsonschema-go` produces one and `{}` where the package builds
the entry itself, and neither form is part of the contract.
Fixed-length byte arrays with no encoding of their own get no special case:
under REQ-SCHEMA-002 they are JSON arrays of exactly their length in integers,
excluding `null` (REQ-SCHEMA-040).

### Types with their own encoding

`encoding/json` asks a type how to encode itself before it looks at the type's
structure. The generator follows the same order, and its answer wins wherever
the type appears: a struct field, an array or slice element, a map value, a
map key, or the whole type.

1. `json.RawMessage` admits every value (REQ-SCHEMA-039).
2. `time.Time` is a string (REQ-SCHEMA-043).
3. `json.Number` is a number (REQ-SCHEMA-044).
4. A type whose method set includes `json.Marshaler` admits every value
   (REQ-SCHEMA-042): what `MarshalJSON` writes cannot be known from the type.
5. A type whose method set includes `encoding.TextMarshaler` is a string
   (REQ-SCHEMA-041).
6. A type with neither interface in its own method set but one of them in the
   pointer's method set admits every value (REQ-SCHEMA-045):
   `encoding/json` uses the pointer's method only when the value is
   addressable, so the schema must admit both the plain encoding and the
   encoded form.

Promoted methods count, because they are in the method set: an embedded
`Stamp` makes the embedding struct a `TextMarshaler`, exactly as
`encoding/json` sees it, and a named byte slice or byte array that implements
an interface takes this rule instead of REQ-SCHEMA-038 or REQ-SCHEMA-040.
`json.Marshaler` is checked before `encoding.TextMarshaler`, as
`encoding/json` does, and either is checked before the `string` tag option: a
field of a type with its own encoding keeps that encoding when tagged
`,string`.

The generator writes these schemas through `ForOptions.TypeSchemas` for every
type it finds with a method in either interface, which overrides the
library's built-in table of standard-library types. That table is not the
contract: it describes `big.Int` as a string, while `encoding/json` writes
`{}` for a value and a number for a `*big.Int`, and a value that follows the
library would be rejected.

### Required fields

A property is required when its tag has neither `omitempty` nor `omitzero`,
whatever the field's kind (REQ-SCHEMA-003). `google/jsonschema-go` marks a
field without `omitempty` required and follows the option for strings,
numbers and booleans, but the package's own object patches re-add a slice,
map, pointer or `[]byte` field to `required` after the library has dropped
it. Required therefore comes from the tag alone. `encoding/json` never treats
a struct as empty, so a struct field tagged `omitempty` is always written; the
schema admits the member being absent, which is weaker than the encoding and
never a false rejection.

### The `string` tag option

`encoding/json` stores a field tagged `,string` as a JSON string holding the
field's JSON literal; a string field is double-encoded. The generator gives
the property a string schema whose pattern matches exactly the literals
`encoding/json` writes, after following pointers:

| field type | pattern |
| --- | --- |
| signed integer kinds | `^-?(0\|[1-9][0-9]*)$` |
| unsigned integer kinds | `^(0\|[1-9][0-9]*)$` |
| floating-point kinds | `^-?(0\|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$` |
| `bool` | `^(true\|false)$` |
| string kinds | `^"([^"\\]\|\\.)*"$` |

Floats are written in the shortest form that reads back to the same value, so
the pattern admits `0`, `-4`, `1.5`, `0.000001`, `1e-7` and `1e+21`; `NaN`
and the infinities fail the encoding outright, so no schema sees them.

A pointer field tagged `,string` writes `null` when it is nil and the same
literals when it is not, so its schema adds `null` to the string schema, as a
pointer field does everywhere else.

### Map keys

Object member names are strings, and `encoding/json` writes a map key as one:
the raw string for a string kind, the result of `MarshalText` for a key type
that implements `encoding.TextMarshaler`, and `strconv.FormatInt` or
`FormatUint` for integer kinds. Every map with a key type the generator does
not reject gets the schema of a `map[string]V`: `null` or an object whose
values use the element schema (REQ-SCHEMA-048). A key type with none of these
— a float, a struct without `MarshalText`, an interface — is rejected with
`invalid_argument`, naming the field and the key type (REQ-SCHEMA-049), as
`encoding/json` refuses to marshal it too. The kind is checked before the
interface because `encoding/json` does: a named string type with `MarshalText`
is written as its raw string, not as its text.

## Interfaces the implementer follows

Names are guidance; the behaviors are the contract.

```go
// Generation
func For[T any]() (*Schema, error)
func ForType(t reflect.Type) (*Schema, error)

// Compilation: a schema set is the closed world of schemas a schema may refer to.
type Set struct{ /* id → raw schema */ }
func (s *Set) Add(id string, raw json.RawMessage) error
func (s *Set) Compile(id string) (*Compiled, error)
func Compile(raw json.RawMessage) (*Compiled, error) // a set of one, no references

// Validation
func (c *Compiled) Validate(instance json.RawMessage) error

// Canonical JSON
func Canonicalize(text []byte) ([]byte, error)
func Marshal(v any) ([]byte, error) // encoding/json, then Canonicalize
```

## Errors

Every failure is a `*core.Error` with code `invalid_argument`
(`core/errors.go`). Locations are in `Details`:

| Failure | `Details` |
| --- | --- |
| Generation | `field`: Go field path, dot separated (`In.Bad`) |
| Generation, a map key type with no JSON text | `field` and `key`: the key type's name (`float64`) |
| Compilation | `violations`: list of `{schema_location, keyword, message}`; or `reference` for REQ-SCHEMA-012 |
| Validation | `violations`: list of `{location, keyword, message}` in the order of REQ-SCHEMA-022 |
| Canonicalization, duplicate names in instances | `location` |

Locations are JSON Pointers (RFC 6901): `~` is written `~0`, `/` is written
`~1`, and the root is the empty pointer `""`. A validation library reports
`required` and `additionalProperties` at the object; this package moves them
to the property (REQ-SCHEMA-021), one violation per property. Messages are
English for logs (`core.Error.Message`); none of them is part of a scenario.

## How the scenarios run

The scenarios build Go types with `reflect.StructOf`, which cannot create a
named type at run time. A named type used by a scenario, such as `NamedBytes`
with underlying type `[]byte`, or a type that carries its own encoding, such
as `TextStamp`, is therefore declared in the step driver and looked up by its
name in the field tables. A scenario places such a type in a holder with one
step, as a field named `v`, as an embedded field, or as the whole type, so the
same behavior is proved in every position.

## Instances and numbers

Instances are read with `json.Decoder.UseNumber`, never into `float64`, so
the validator compares exact decimals (REQ-SCHEMA-023). Duplicate member
names are detected while reading, before validation (REQ-SCHEMA-025), because
two readers of the same payload (the validator and CEL) would otherwise see
different values. Invalid UTF-8 bytes in a text are refused like an unpaired
surrogate escape (REQ-SCHEMA-037); the scenarios cover the escape only,
because a feature file cannot hold invalid bytes.

## Out of scope

- CEL type-checking against schemas (WP-L2-01, `specs/expr`).
- The flow IR hash itself: the flow compiler hashes the canonical form
  (`docs/architecture/flow-engine.md`), WP-L4-01.
- Structured-output repair strategies (`docs/architecture/model-layer.md` §5).
