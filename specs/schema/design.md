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
property `description`) is never part of the contract on its own: type and
constraint scenarios state behavior through validation, so a library upgrade
that changes how a generated schema expresses them but not what it accepts
keeps the scenarios green.

### Bytes and raw JSON

`encoding/json` writes a `[]byte` as a base64 string and a nil `[]byte` as
`null`. The generator gives `[]byte`, and any named type whose underlying type
is `[]byte`, the schema `{"type": ["null", "string"], "contentEncoding":
"base64"}` (REQ-SCHEMA-038). `contentEncoding` is an annotation in draft
2020-12, so validation checks the JSON type only and accepts any string; the
base64 form of the content is what `encoding/json` writes (REQ-SCHEMA-008).
The generator recognizes named byte-slice types only where it can name them,
so the scenarios declare them explicitly.

`json.RawMessage` is a `[]byte` with its own encoding, so it gets its own
entry in the generator's type schemas: the empty schema `{}`, which accepts
every JSON value including `null` (REQ-SCHEMA-039). Fixed-length `[N]byte`
arrays get no special case: they are JSON arrays of exactly `N` integers from
0 to 255, not excluding `null` (REQ-SCHEMA-040).

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
| Compilation | `violations`: list of `{schema_location, keyword, message}`; or `reference` for REQ-SCHEMA-012 |
| Validation | `violations`: list of `{location, keyword, message}` in the order of REQ-SCHEMA-022 |
| Canonicalization, duplicate names in instances | `location` |

Locations are JSON Pointers (RFC 6901): `~` is written `~0`, `/` is written
`~1`, and the root is the empty pointer `""`. A validation library reports
`required` and `additionalProperties` at the object; this package moves them
to the property (REQ-SCHEMA-021), one violation per property. Messages are
English for logs (`core.Error.Message`); none of them is part of a scenario.

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
