# Prompt: design

Decisions the implementer of WP-L3-05 follows. The package `prompt` imports
only `core` (`layering.yaml`); callers hand the assembled prompt to the model
layer. The prompt-level hint is `cache_after`; the model-level hint is
`model.CacheHint` and the vendor mapping is the provider adapters'
(ADR-0019). Neither is part of this package.

## 1. Templates and references

- A template has an `id`, a `version` (a non-empty string without `@`), its
  declared **inputs** (name, and `trust`: `trusted` or `untrusted`, default
  `untrusted`), its **segments** and an optional **total budget**. An empty
  id or version, or one containing `@`, is reported by prompt lint as
  `empty_template_id`, `at_in_template_id`, `empty_template_version` or
  `at_in_template_version`. Input names are unique within a template; a
  repeat is `duplicate_input`.
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

Each history entry's **role** is a short ASCII token matching
`^[A-Za-z][A-Za-z0-9_-]{0,31}$`. The rule exists because the role is written
to model-facing output outside the untrusted wrapper that carries the entry's
text, so any character a history entry can supply reaches the model as
prompt structure rather than as wrapped content. The grammar keeps a role to
one word of letters, digits, `_` and `-`. The assembler applies it to every
entry of an assembly before any part is rendered, and fails the assembly with
`invalid_argument` and an error that does not contain the rejected role
(REQ-PROMPT-024). Entry text follows REQ-PROMPT-014 / REQ-PROMPT-015 whatever
its role.

A template has at most one `history` segment; a second is
`multiple_history`. Segments of other kinds may repeat; each has a name
unique within the template, and a repeated name is `duplicate_segment`. A
segment whose kind is outside the table above is `unknown_segment_kind`.

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

`cache_after` is the prompt-level form of the model layer's system
breakpoint. A caller that maps an assembled prompt to a model request places
the `static` and `semi-static` parts in system messages and the `dynamic`,
`history` and `user` parts in non-system messages. It sets
`model.CacheHint.System` only when the last system message ends exactly at
`cache_after`. When that precondition fails — for example a dynamic part
follows the boundary, or the boundary falls inside a system message — the
caller sets no system breakpoint and the request proceeds unannotated; the
invocation never fails for it (ADR-0019). The model layer's tool and
rolling-message breakpoints are not prompt segments; the caller sets them from
the request it builds. `prefix_hash` stays the hash of the prompt's cacheable
parts; the model layer's `model.PrefixHash` covers the tool definitions as
well and is order-sensitive over tools then system messages.

The prompt hashes cover a canonical encoding, so they do not depend on input
order or Go map order (REQ-PROMPT-007).

## 5. Untrusted values

A wrapped value reads:

```
<untrusted source="NAME">VALUE</untrusted>
```

`NAME` is the input name or `history`. It keeps only `A-Z`, `a-z`, `0-9`,
`_`, `.` and `-`; every other character becomes `_`, so a label can never end
the attribute or the tag. An input name a template can substitute already
matches `[a-z][a-z0-9_]*`, and `history` matches as well, so the restriction
guards the wrapper against a label built from any other text.

Before wrapping, every `<` in the value that begins a delimiter-like sequence
is replaced by `&lt;`. A delimiter-like sequence is `<`, optional whitespace,
an optional `/`, optional whitespace, then `untrusted` in any letter case. No
other text is changed, so a value without such a sequence is wrapped
unchanged.

The assembler records each delimiter pair it writes. Spans are per rendered
text: one per segment, and one per history entry within its own rendered
text. The opening delimiter's span runs from its `<` through its `>`, and the
closing delimiter's span from its `<` through its `>`. Prompt lint of an
assembled prompt accepts a delimiter only when both delimiters of its
recorded pair are at their recorded spans; every other delimiter, including
either half of a recorded pair whose other half is not at its span, is
`unpaired_delimiter`, and a prompt that carries no record reports every
delimiter. The record decides, not a scan of the text, because text alone
cannot tell the assembler's own pairs from a pair forged by trusted values
or by fixed text. The record is not part of `hash` or `prefix_hash` (§4), and
it survives a JSON round trip of the assembled prompt.

## 6. Prompt lint

Findings carry a rule, a severity (`error` or `warning`) and a segment name.
The segment name is empty for a template-level rule (`empty_template_id`,
`at_in_template_id`, `empty_template_version`, `at_in_template_version`,
`duplicate_input` and `multiple_history`). A segment-level rule names its
segment: `duplicate_segment` names the repeated segment, and every rule
checked on one segment names that segment.

| Rule | Severity | Checked on |
| --- | --- | --- |
| `variable_in_static` | error | template |
| `untrusted_in_cacheable` | error | template |
| `fixed_text_over_budget` | error | template |
| `no_budget` | warning | template |
| `empty_template_id` | error | template |
| `at_in_template_id` | error | template |
| `empty_template_version` | error | template |
| `at_in_template_version` | error | template |
| `duplicate_input` | error | template |
| `duplicate_segment` | error | template |
| `multiple_history` | error | template |
| `unknown_segment_kind` | error | template |
| `unpaired_delimiter` | error | assembled prompt |

The registry lints every template it registers: errors refuse it
(REQ-PROMPT-020), warnings are returned with it (REQ-PROMPT-023). An
undeclared variable is refused by the registry itself (REQ-PROMPT-004). Rule
names are stable: new rules are added, none is renamed or removed.

`PROMPT-021-02` is the scenario that keeps the lint from being satisfied by
refusing everything: the assembler's own recorded pairs are linted clean
there, and the unit tests pin the two sides.
