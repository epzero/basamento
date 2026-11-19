# L2 — Expressions and schemas

### WP-L2-01 — CEL environment
Depends: WP-L0-01.
Owns: `expr`.
cel-go (`cel.dev/cel-go`) with no clock or random functions, sorted map iteration, cost limits, type-checking against JSON Schema; gojq mappings (ADR-0003).

### WP-L2-02 — JSON Schema
Depends: WP-L0-01.
Owns: `core/schema`.
Generation from Go types (`google/jsonschema-go`), validation (`santhosh-tekuri/jsonschema`), canonical JSON.
