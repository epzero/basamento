# Prompt: design

Decisions the implementer of WP-L3-05 follows. The package `prompt` imports
only `core` (`layering.yaml`); callers such as `agent` hand the assembled
prompt to the model layer, which maps the cache hint to a vendor's cache
control. That mapping is not part of this package.

## 1. Templates and references

- A template has an `id`, a `version` (a non-empty string without `@`), its
  declared **inputs** (name, and `trust`: `trusted` or `untrusted`, default
  `untrusted`), its **segments** and an optional **total budget**.
- A reference is `<id>@<version>`. There is no "latest": a run pins the
  version it uses, so a replay assembles the same prompt (constitution §3).
- Registration is idempotent for identical content and refused with
  `conflict` for different content under the same `<id>@<version>`
  (REQ-PROMPT-002). Content identity is the template's content hash.
- Variables are written `{{name}}`; a name is `[a-z][a-z0-9_]*`. Substitution
  is a single pass: a value is inserted as is and never scanned again
  (REQ-PROMPT-006). There are no conditionals, loops or functions.

## 2. Segments

| Kind | Holds | Inputs | Cacheable |
| --- | --- | --- | --- |
| `static` | Fixed instructions. | none | yes |
| `semi-static` | Text that changes rarely, such as a tenant's name. | `trusted` only | yes |
| `dynamic` | Per-call context, such as retrieved documents. | any | no |
| `history` | Earlier turns of a conversation: ordered entries, each a role and a text. | the history list | no |
| `user` | The current turn. | any | no |

A template has at most one `history` segment. Segments of other kinds may
repeat; each has a name unique within the template.

## 3. Sizes and budgets

- A segment's **size** is the number of Unicode code points of its content:
  its fixed text plus the original text of each substituted value. Delimiters
  and separators the assembler adds are not counted, nor are characters
  added by neutralization (§5). The history segment's size is the sum of
  its entries' sizes.
- The size of an assembled prompt is the sum of its segments' sizes.
- A segment without a budget, and a template without a total budget, are
  unbounded (prompt lint warns, REQ-PROMPT-019).
- Only history is trimmed, and only by whole entries, oldest first
  (REQ-PROMPT-010). Every other overflow fails the assembly with
  `invalid_argument` and `core.Error` details `segment` (or `total`), `size`
  and `budget`.

## 4. The assembled prompt

An assembled prompt holds:

- its ordered parts, each a segment name, kind and rendered text (history
  parts keep one entry per item with its role);
- `cache_after`: the name of the segment the cache hint follows, or empty;
- `prefix_hash`: SHA-256 of the cacheable prefix (the rendered `static` and
  `semi-static` parts), empty without one;
- `hash`: SHA-256 of the whole assembled prompt;
- `history_dropped`: the number of history entries dropped;
- the template reference it was assembled from.

Hashes cover a canonical encoding, so they do not depend on input order or Go
map order (REQ-PROMPT-007).

## 5. Untrusted values

A wrapped value reads:

```
<untrusted source="NAME">VALUE</untrusted>
```

`NAME` is the input name or `history`. Before wrapping, every `<` in the
value that begins a delimiter-like sequence is replaced by `&lt;`. A
delimiter-like sequence is `<`, optional whitespace, an optional `/`,
optional whitespace, then `untrusted` in any letter case. No other text is
changed, so a value without such a sequence is wrapped unchanged.

## 6. Prompt lint

Findings carry a rule, a severity (`error` or `warning`) and a segment name.

| Rule | Severity | Checked on |
| --- | --- | --- |
| `variable_in_static` | error | template |
| `untrusted_in_cacheable` | error | template |
| `fixed_text_over_budget` | error | template |
| `no_budget` | warning | template |
| `unpaired_delimiter` | error | assembled prompt |

The registry lints every template it registers: errors refuse it
(REQ-PROMPT-020), warnings are returned with it (REQ-PROMPT-023). An
undeclared variable is refused by the registry itself (REQ-PROMPT-004). Rule
names are stable: new rules are added, none is renamed or removed.
